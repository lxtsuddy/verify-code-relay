package watcher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/auth"
)

func startRun(t *testing.T, e *env) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.w.Run(ctx); close(done) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not stop")
		}
	}
}

func TestRunReconnectsAfterKill(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop := startRun(t, e)
	defer stop()
	eventually(t, "connected", func() bool { return e.w.Status() == StatusConnected })
	e.dialer.last().Conn.Close()
	eventually(t, "redialled", func() bool { return e.dialer.count.Load() >= 2 && e.w.Status() == StatusConnected })
	// dial() bumps count before it registers the new conn, so wait for the second
	// conn itself; otherwise last() could still be the killed one.
	eventually(t, "second connection idling", func() bool {
		e.dialer.mu.Lock()
		defer e.dialer.mu.Unlock()
		return len(e.dialer.conns) >= 2 && e.dialer.conns[len(e.dialer.conns)-1].idles.Load() >= 1
	})
	e.srv.appendMsg(t, "after reconnect")
	eventually(t, "uid 1 processed", func() bool { return equalUIDs(e.proc.got(), 1) })
}

func TestRunRetriesFailedUIDExactlyOnce(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.proc.failOnce[1] = true
	stop := startRun(t, e)
	defer stop()
	eventually(t, "connected", func() bool { return e.w.Status() == StatusConnected })
	waitIdle(t, e)
	e.srv.appendMsg(t, "flaky")
	eventually(t, "uid 1 processed after retry", func() bool { return equalUIDs(e.proc.got(), 1) })
	time.Sleep(200 * time.Millisecond)
	if !equalUIDs(e.proc.got(), 1) {
		t.Fatalf("processed = %v (must be exactly once)", e.proc.got())
	}
}

func TestRunDetectsBlackHoleAndRecovers(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.IdleCycle = 100 * time.Millisecond
		c.CmdTimeout = 200 * time.Millisecond
	})
	stop := startRun(t, e)
	defer stop()
	eventually(t, "connected", func() bool { return e.w.Status() == StatusConnected })
	e.dialer.last().frozen.Store(true)
	eventually(t, "reconnected", func() bool { return e.dialer.count.Load() >= 2 && e.w.Status() == StatusConnected })
}

func TestRunAuthFailedAlertsOnceAndKeepsRetrying(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.Auth = auth.Password{Username: "me@example.com", Password: "nope"}
		c.AlertAfter = 50 * time.Millisecond
	})
	stop := startRun(t, e)
	defer stop()
	// Retry long enough (backoff 20-100ms) that AlertAfter has elapsed several times over.
	eventually(t, "auth_failed and retrying", func() bool { return e.w.Status() == StatusAuthFailed && e.dialer.count.Load() >= 6 })
	alerts, _ := e.alerts.snapshot()
	n := 0
	for _, k := range alerts {
		if k == "auth:mb/INBOX" {
			n++
		}
		if strings.HasPrefix(k, "disconnected:") {
			t.Fatalf("auth failure must not also raise a disconnected alert: %v", alerts)
		}
	}
	if n != 1 {
		t.Fatalf("auth alerts = %v", alerts)
	}
}

type needsLoginAuth struct{}

func (needsLoginAuth) Authenticate(context.Context, *imapclient.Client) error {
	return auth.ErrNeedsLogin
}

func TestRunNeedsLoginStops(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.Auth = needsLoginAuth{} })
	done := make(chan struct{})
	go func() { e.w.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run should return on needs_login")
	}
	if e.w.Status() != StatusNeedsLogin {
		t.Fatalf("status = %s", e.w.Status())
	}
	alerts, _ := e.alerts.snapshot()
	if len(alerts) != 1 || alerts[0] != "needs_login:mb" {
		t.Fatalf("alerts = %v", alerts)
	}
}

func TestRunDisconnectedAlertAndRecovery(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.AlertAfter = 100 * time.Millisecond })
	e.dialer.fail.Store(true)
	stop := startRun(t, e)
	defer stop()
	eventually(t, "disconnected alert", func() bool {
		a, _ := e.alerts.snapshot()
		return len(a) == 1 && strings.HasPrefix(a[0], "disconnected:mb/INBOX")
	})
	if e.w.Status() != StatusReconnecting {
		t.Errorf("status = %s", e.w.Status())
	}
	e.dialer.fail.Store(false)
	eventually(t, "recovered", func() bool {
		_, r := e.alerts.snapshot()
		return len(r) == 1 && r[0] == "disconnected:mb/INBOX"
	})
}

func TestRunDisconnectedAlertFiresAtDeadlineNotAfterBackoff(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.MinBackoff = 2 * time.Second
		c.MaxBackoff = 2 * time.Second
		c.AlertAfter = 300 * time.Millisecond
	})
	e.dialer.fail.Store(true)
	start := time.Now()
	stop := startRun(t, e)
	defer stop()
	for {
		if a, _ := e.alerts.snapshot(); len(a) == 1 && strings.HasPrefix(a[0], "disconnected:mb/INBOX") {
			break
		}
		if time.Since(start) > 1500*time.Millisecond {
			t.Fatal("disconnected alert not raised within 1.5s (backoff overshoots AlertAfter)")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNextBackoff(t *testing.T) {
	cases := []struct{ cur, max, want time.Duration }{
		{time.Second, time.Minute, 2 * time.Second},
		{20 * time.Second, time.Minute, 40 * time.Second},
		{40 * time.Second, time.Minute, time.Minute},
		{time.Minute, time.Minute, time.Minute},
		{5 * time.Minute, time.Minute, time.Minute},
	}
	for _, c := range cases {
		if got := nextBackoff(c.cur, c.max); got != c.want {
			t.Errorf("nextBackoff(%s, %s) = %s, want %s", c.cur, c.max, got, c.want)
		}
	}
}

func TestJitterBounds(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if d := jitter(time.Second); d < 800*time.Millisecond || d > 1200*time.Millisecond {
			t.Fatalf("jitter(1s) = %s, outside [0.8s, 1.2s]", d)
		}
	}
}
