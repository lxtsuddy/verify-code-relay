package watcher

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
	eventually(t, "second connection registered", func() bool {
		e.dialer.mu.Lock()
		defer e.dialer.mu.Unlock()
		return len(e.dialer.conns) >= 2
	})
	waitIdle(t, e)
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
	if txt := e.alerts.alertText("needs_login:mb"); !strings.Contains(txt, "Web 管理介面的信箱頁重新授權") || !strings.Contains(txt, "relay login mb") {
		t.Errorf("alert text = %q, want the Web admin and the relay login command", txt)
	}
}

func TestRunStoppedAfterCancelWhileConnected(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop := startRun(t, e)
	eventually(t, "connected", func() bool { return e.w.Status() == StatusConnected })
	stop()
	if s := e.w.Status(); s != StatusStopped {
		t.Fatalf("status = %s, want stopped", s)
	}
}

func TestRunStoppedAfterCancelDuringBackoff(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.MinBackoff = time.Minute
		c.MaxBackoff = time.Minute
	})
	e.dialer.fail.Store(true)
	stop := startRun(t, e)
	eventually(t, "waiting to redial", func() bool { return e.w.Status() == StatusReconnecting })
	stop() // fails if Run sleeps out the one-minute backoff
	if s := e.w.Status(); s != StatusStopped {
		t.Fatalf("status = %s, want stopped", s)
	}
}

func TestRunDisconnectedAlertedOnlyOnce(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.AlertAfter = 50 * time.Millisecond })
	e.dialer.fail.Store(true)
	stop := startRun(t, e)
	defer stop()
	eventually(t, "disconnected alert", func() bool {
		a, _ := e.alerts.snapshot()
		return len(a) >= 1
	})
	base := e.dialer.count.Load()
	eventually(t, "more failed dials", func() bool { return e.dialer.count.Load()-base >= 4 })
	if a, _ := e.alerts.snapshot(); len(a) != 1 || a[0] != "disconnected:mb/INBOX" {
		t.Fatalf("alerts = %v, want a single disconnected alert", a)
	}
}

// switchAuth logs in with the right or the wrong password.
type switchAuth struct{ bad atomic.Bool }

func (a *switchAuth) Authenticate(ctx context.Context, c *imapclient.Client) error {
	pw := "pw"
	if a.bad.Load() {
		pw = "nope"
	}
	return auth.Password{Username: "me@example.com", Password: pw}.Authenticate(ctx, c)
}

func TestRunAuthRecovered(t *testing.T) {
	sw := &switchAuth{}
	sw.bad.Store(true)
	e := newEnv(t, nil, func(c *Config) { c.Auth = sw })
	stop := startRun(t, e)
	defer stop()
	eventually(t, "auth alert", func() bool {
		a, _ := e.alerts.snapshot()
		return len(a) == 1 && a[0] == "auth:mb/INBOX" && e.w.Status() == StatusAuthFailed
	})
	if txt := e.alerts.alertText("auth:mb/INBOX"); !strings.Contains(txt, "認證失敗") {
		t.Errorf("alert text = %q", txt)
	}
	sw.bad.Store(false)
	eventually(t, "recovered", func() bool {
		_, r := e.alerts.snapshot()
		return len(r) == 1 && r[0] == "auth:mb/INBOX" && e.w.Status() == StatusConnected
	})
	if txt := e.alerts.recoveredText("auth:mb/INBOX"); !strings.Contains(txt, "認證已恢復") {
		t.Errorf("recovered text = %q", txt)
	}
	if a, _ := e.alerts.snapshot(); len(a) != 1 {
		t.Errorf("alerts = %v, want only the auth alert", a)
	}
}

// An auth failure suppresses the disconnected alert, but once the failure turns
// into a network one the disconnected alert is raised. The outage is counted
// from the first failure of either kind, so with AlertAfter already elapsed it
// fires on the first network failure.
func TestRunAuthFailureThenNetworkFailureAlerts(t *testing.T) {
	sw := &switchAuth{}
	sw.bad.Store(true)
	e := newEnv(t, nil, func(c *Config) {
		c.Auth = sw
		c.AlertAfter = 100 * time.Millisecond
	})
	stop := startRun(t, e)
	defer stop()
	eventually(t, "auth alert", func() bool {
		a, _ := e.alerts.snapshot()
		return len(a) == 1 && a[0] == "auth:mb/INBOX"
	})
	time.Sleep(150 * time.Millisecond) // past AlertAfter, still only the auth alert
	if a, _ := e.alerts.snapshot(); len(a) != 1 {
		t.Fatalf("alerts = %v during the auth failure", a)
	}
	e.dialer.fail.Store(true)
	eventually(t, "disconnected alert", func() bool {
		a, _ := e.alerts.snapshot()
		return len(a) == 2 && a[1] == "disconnected:mb/INBOX"
	})
	if txt := e.alerts.alertText("disconnected:mb/INBOX"); !strings.Contains(txt, "無法連線") {
		t.Errorf("alert text = %q", txt)
	}
	if e.w.Status() != StatusReconnecting {
		t.Errorf("status = %s", e.w.Status())
	}
	e.dialer.fail.Store(false)
	sw.bad.Store(false)
	eventually(t, "both recovered", func() bool {
		_, r := e.alerts.snapshot()
		return len(r) == 2 && e.w.Status() == StatusConnected
	})
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
	if txt := e.alerts.alertText("disconnected:mb/INBOX"); !strings.Contains(txt, "無法連線") || !strings.Contains(txt, "dial refused") {
		t.Errorf("alert text = %q, want 無法連線 with the dial error", txt)
	}
	e.dialer.fail.Store(false)
	eventually(t, "recovered", func() bool {
		_, r := e.alerts.snapshot()
		return len(r) == 1 && r[0] == "disconnected:mb/INBOX"
	})
	if txt := e.alerts.recoveredText("disconnected:mb/INBOX"); !strings.Contains(txt, "已恢復連線") {
		t.Errorf("recovered text = %q", txt)
	}
}

// When the server is reachable but the first catch-up keeps failing, the alert
// must say the catch-up fails, not that the server cannot be reached.
func TestRunCatchUpFailureAlertText(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.AlertAfter = 100 * time.Millisecond })
	initWatermark(t, e)
	e.srv.appendMsg(t, "always fails")
	e.proc.failAll.Store(true)
	stop := startRun(t, e)
	defer stop()
	eventually(t, "disconnected alert", func() bool {
		a, _ := e.alerts.snapshot()
		return len(a) == 1 && a[0] == "disconnected:mb/INBOX"
	})
	txt := e.alerts.alertText("disconnected:mb/INBOX")
	if !strings.Contains(txt, "無法完成補抓") || strings.Contains(txt, "無法連線") || !strings.Contains(txt, "boom") {
		t.Errorf("alert text = %q, want 無法完成補抓 with the processing error", txt)
	}
	e.proc.failAll.Store(false)
	eventually(t, "recovered", func() bool {
		_, r := e.alerts.snapshot()
		return len(r) == 1 && r[0] == "disconnected:mb/INBOX"
	})
	if txt := e.alerts.recoveredText("disconnected:mb/INBOX"); strings.Contains(txt, "連線") {
		t.Errorf("recovered text = %q, must not talk about the connection", txt)
	}
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

// initWatermark runs one session on the empty mailbox so that mail appended
// afterwards counts as new instead of being skipped as first-run backlog.
func initWatermark(t *testing.T, e *env) {
	t.Helper()
	stop, _, _ := runSession(t, e)
	stop()
}

// Backoff is reset only after a session's first catch-up succeeds: a message
// that keeps failing must not turn into a reconnect loop at MinBackoff.
func TestRunBackoffGrowsWhileProcessingFails(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.MinBackoff = 50 * time.Millisecond
		c.MaxBackoff = 400 * time.Millisecond
	})
	initWatermark(t, e)
	e.srv.appendMsg(t, "always fails")
	e.proc.failAll.Store(true)
	base := e.dialer.count.Load()
	stop := startRun(t, e)
	defer stop()
	time.Sleep(1200 * time.Millisecond)
	// 50+100+200+400 ms (±20 %) between dials: at most 6 dials fit in 1.2 s,
	// whereas resetting the backoff at SELECT redials every ~50 ms (20+).
	if n := e.dialer.count.Load() - base; n < 3 || n > 6 {
		t.Fatalf("dials in 1.2s = %d, want 3..6 (exponential backoff)", n)
	}
	if e.w.Status() == StatusConnected {
		t.Errorf("status = %s while catch-up keeps failing", e.w.Status())
	}
	if len(e.proc.got()) != 0 {
		t.Errorf("processed = %v", e.proc.got())
	}
}

// The recovered notice is sent only once a session has caught up, not merely
// because SELECT succeeded.
func TestRunRecoveredOnlyAfterSuccessfulCatchUp(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.MinBackoff = 50 * time.Millisecond
		c.MaxBackoff = 200 * time.Millisecond
		c.AlertAfter = 100 * time.Millisecond
	})
	initWatermark(t, e)
	e.srv.appendMsg(t, "fails until fixed")
	e.proc.failAll.Store(true)
	e.dialer.fail.Store(true)
	stop := startRun(t, e)
	defer stop()
	eventually(t, "disconnected alert", func() bool {
		a, _ := e.alerts.snapshot()
		return len(a) == 1 && a[0] == "disconnected:mb/INBOX"
	})
	base := e.dialer.count.Load()
	e.dialer.fail.Store(false) // SELECT now succeeds, but the catch-up keeps failing
	eventually(t, "sessions reach the failing catch-up", func() bool { return e.dialer.count.Load()-base >= 3 })
	if _, r := e.alerts.snapshot(); len(r) != 0 {
		t.Fatalf("recovered = %v before any catch-up succeeded", r)
	}
	if e.w.Status() == StatusConnected {
		t.Errorf("status = %s while catch-up keeps failing", e.w.Status())
	}
	e.proc.failAll.Store(false)
	eventually(t, "recovered after catch-up", func() bool {
		_, r := e.alerts.snapshot()
		return len(r) == 1 && r[0] == "disconnected:mb/INBOX" && e.w.Status() == StatusConnected
	})
	if !equalUIDs(e.proc.got(), 1) {
		t.Errorf("processed = %v, want [1]", e.proc.got())
	}
}

// Spec §13: a disconnect while a message body is being fetched must not lose
// or duplicate the message; the next session fetches it again, exactly once.
func TestRunDisconnectDuringBodyFetch(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop := startRun(t, e)
	defer stop()
	eventually(t, "connected", func() bool { return e.w.Status() == StatusConnected })
	waitIdle(t, e)
	e.dialer.killBodyFetch.Store(true)
	e.srv.appendMsg(t, "interrupted")
	eventually(t, "uid 1 processed after reconnect", func() bool { return equalUIDs(e.proc.got(), 1) })
	if e.dialer.killBodyFetch.Load() {
		t.Fatal("the body FETCH was never interrupted")
	}
	if n := e.dialer.count.Load(); n < 2 {
		t.Fatalf("dials = %d, want a reconnect", n)
	}
	time.Sleep(200 * time.Millisecond)
	if !equalUIDs(e.proc.got(), 1) {
		t.Fatalf("processed = %v (must be exactly once)", e.proc.got())
	}
	st, _, _ := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if st.LastUID != 1 {
		t.Fatalf("watermark = %+v, want last_uid 1", st)
	}
}

// lockedBuf is a bytes.Buffer safe for the watcher's goroutine and the test.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The reconnect log gives retry_in as a readable duration ("1.5s"), not nanoseconds.
func TestRunRetryInIsReadable(t *testing.T) {
	var buf lockedBuf
	e := newEnv(t, nil, func(c *Config) { c.Log = slog.New(slog.NewJSONHandler(&buf, nil)) })
	e.dialer.fail.Store(true)
	stop := startRun(t, e)
	eventually(t, "reconnect log", func() bool { return strings.Contains(buf.String(), "連線中斷，稍後重試") })
	stop()
	if !regexp.MustCompile(`"retry_in":"[0-9.]+(ms|s)"`).MatchString(buf.String()) {
		t.Fatalf("log = %s", buf.String())
	}
}
