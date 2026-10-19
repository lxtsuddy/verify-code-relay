package watcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/store"
)

func runSession(t *testing.T, e *env) (cancel func(), connected chan struct{}, done chan error) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())
	connected = make(chan struct{})
	done = make(chan error, 1)
	go func() {
		done <- e.w.session(ctx, func() { close(connected) })
	}()
	select {
	case <-connected:
	case err := <-done:
		t.Fatalf("session ended before connecting: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not connected")
	}
	return func() {
		cancelFn()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("session did not stop")
		}
	}, connected, done
}

// waitIdle blocks until the session has sent IDLE. imapmemserver only announces
// mail that arrives once its IDLE loop is registered; a message appended while
// the session is still catching up (before IDLE) is not announced until the next
// event. Real servers do not have that gap, so tests append only once idling.
func waitIdle(t *testing.T, e *env) {
	t.Helper()
	eventually(t, "session idling", func() bool { return e.dialer.last().idles.Load() >= 1 })
}

func TestFirstRunSkipsBacklogThenIdleDelivers(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.srv.appendMsg(t, "old") // uid 1: backlog, must not be processed
	stop, _, _ := runSession(t, e)
	defer stop()
	st, ok, _ := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if !ok || st.LastUID != 1 || st.UIDValidity == 0 {
		t.Fatalf("watermark = %+v ok=%v", st, ok)
	}
	waitIdle(t, e)
	e.srv.appendMsg(t, "new") // uid 2 arrives during IDLE
	eventually(t, "uid 2 processed", func() bool { return equalUIDs(e.proc.got(), 2) })
	if n := e.srv.seenFlags(t); n != 0 {
		t.Errorf("%d messages marked \\Seen; mailbox must stay untouched", n)
	}
}

func TestRestartCatchesUp(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop, _, _ := runSession(t, e)
	stop()
	e.srv.appendMsg(t, "while down 1")
	e.srv.appendMsg(t, "while down 2")
	stop, _, _ = runSession(t, e)
	defer stop()
	eventually(t, "uids 1,2 processed", func() bool { return equalUIDs(e.proc.got(), 1, 2) })
}

func TestUIDValidityChangeResetsWatermark(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.srv.appendMsg(t, "a")
	e.st.SetMailboxState(context.Background(), "mb", "INBOX", store.MailboxState{UIDValidity: 999, LastUID: 0})
	stop, _, _ := runSession(t, e)
	defer stop()
	st, _, _ := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if st.UIDValidity == 999 || st.LastUID != 1 || len(e.proc.got()) != 0 {
		t.Fatalf("state=%+v processed=%v", st, e.proc.got())
	}
}

func TestDecideSkipsWithoutFetchingBody(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop, _, _ := runSession(t, e)
	defer stop()
	waitIdle(t, e)
	e.proc.mu.Lock()
	e.proc.skip[1] = "skipped_age"
	e.proc.mu.Unlock()
	e.srv.appendMsg(t, "old one")
	eventually(t, "uid 1 skipped", func() bool {
		e.proc.mu.Lock()
		defer e.proc.mu.Unlock()
		return equalUIDs(e.proc.skipped, 1)
	})
	e.srv.appendMsg(t, "fresh")
	eventually(t, "uid 2 processed", func() bool { return equalUIDs(e.proc.got(), 2) })
}

func TestBurstWhileBusyIsDrained(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop, _, _ := runSession(t, e)
	defer stop()
	waitIdle(t, e)
	for i := 0; i < 5; i++ {
		e.srv.appendMsg(t, "burst") // some land while the watcher is fetching, not idling
	}
	eventually(t, "all 5 processed", func() bool { return equalUIDs(e.proc.got(), 1, 2, 3, 4, 5) })
}

func TestPollingWhenNoIdle(t *testing.T) {
	// imapserver advertises IDLE after login regardless of Options.Caps, so force polling.
	e := newEnv(t, nil, nil)
	e.w.forcePoll = true
	stop, _, _ := runSession(t, e)
	defer stop()
	e.srv.appendMsg(t, "polled")
	eventually(t, "uid 1 processed by polling", func() bool { return equalUIDs(e.proc.got(), 1) })
	c := e.dialer.last()
	if n := c.idles.Load(); n != 0 {
		t.Errorf("IDLE issued %d times while polling", n)
	}
	if n := c.noops.Load(); n < 1 {
		t.Errorf("NOOP issued %d times, want >= 1", n)
	}
}

func TestLargeBacklogIsProcessedInChunks(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop, _, _ := runSession(t, e) // initialises the watermark on the empty mailbox
	stop()
	const n = 120
	want := make([]uint32, n)
	for i := range want {
		want[i] = uint32(i + 1)
		e.srv.appendMsg(t, "bulk")
	}
	stop, _, _ = runSession(t, e)
	defer stop()
	eventually(t, "all 120 processed in order", func() bool { return equalUIDs(e.proc.got(), want...) })
}

func TestIdleCycleKeepsHealthyConnection(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.IdleCycle = 50 * time.Millisecond })
	stop, _, done := runSession(t, e)
	defer stop()
	time.Sleep(300 * time.Millisecond) // several Close->NOOP->Idle cycles
	select {
	case err := <-done:
		t.Fatalf("session died on healthy connection: %v", err)
	default:
	}
	e.srv.appendMsg(t, "after cycles")
	eventually(t, "uid 1 processed", func() bool { return equalUIDs(e.proc.got(), 1) })
}

func TestBlackHoleConnectionIsDetected(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.IdleCycle = 100 * time.Millisecond
		c.CmdTimeout = 200 * time.Millisecond
	})
	_, _, done := runSession(t, e)
	e.dialer.last().frozen.Store(true)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dead connection not detected")
	}
}

func TestProcessErrorEndsSession(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.proc.failOnce[1] = true
	_, _, done := runSession(t, e)
	waitIdle(t, e)
	e.srv.appendMsg(t, "will fail")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session should end on processing error")
	}
	st, _, _ := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if st.LastUID != 0 {
		t.Errorf("watermark advanced past failed uid: %+v", st)
	}
}

func TestAuthFailureReturnsAuthError(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.w.cfg.Auth = auth.Password{Username: "me@example.com", Password: "nope"}
	err := e.w.session(context.Background(), func() { t.Error("must not connect") })
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("want AuthError, got %T %v", err, err)
	}
}
