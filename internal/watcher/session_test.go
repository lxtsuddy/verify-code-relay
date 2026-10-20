package watcher

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/pipeline"
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

// waitIdle blocks until the server is serving an IDLE on the latest connection.
// imapmemserver only announces mail that arrives once its IDLE loop is
// registered; a message appended while the session is still catching up (after
// its last search, before IDLE) is not announced until the next event. Tests
// that exercise the IDLE path therefore append only once the server is idling.
// The hook fires on the server, so this does not race with the client merely
// having written IDLE.
func waitIdle(t *testing.T, e *env) {
	t.Helper()
	waitIdles(t, e, 1)
}

// waitIdles waits until the server has started n IDLE commands on the latest
// connection, e.g. n=2 once the session went back to IDLE after handling mail.
func waitIdles(t *testing.T, e *env, n int) {
	t.Helper()
	eventually(t, "session idling", func() bool {
		e.dialer.mu.Lock()
		defer e.dialer.mu.Unlock()
		return len(e.dialer.conns) > 0 && e.srv.idles(e.dialer.conns[len(e.dialer.conns)-1].LocalAddr()) >= n
	})
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
	waitIdles(t, e, 2) // back in IDLE after handling uid 1
	if n := e.dialer.last().bodies.Load(); n != 0 {
		t.Fatalf("%d body FETCHes for a skipped message, want 0", n)
	}
	e.srv.appendMsg(t, "fresh")
	eventually(t, "uid 2 processed", func() bool { return equalUIDs(e.proc.got(), 2) })
	if n := e.dialer.last().bodies.Load(); n != 1 {
		t.Fatalf("%d body FETCHes, want 1 (uid 2 only)", n)
	}
}

// Servers may hold back untagged EXISTS outside IDLE. Mail that arrives while
// the session is busy fetching must still be picked up before it goes back to
// IDLE: drain repeats the catch-up until a pass finds nothing. With a single
// pass, uid 2 would wait for the next event or the 25-minute IDLE cycle.
func TestDrainPicksUpMailHeldBackOutsideIdle(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.dialer.rewriter = holdExists
	var once sync.Once
	e.proc.onDecide = func(env pipeline.Envelope) {
		if env.UID == 1 {
			// Arrives after the catch-up's SEARCH, before the body FETCH of uid 1.
			once.Do(func() {
				if err := e.srv.add("arrives while busy"); err != nil {
					t.Error(err)
				}
			})
		}
	}
	stop, _, _ := runSession(t, e)
	defer stop()
	waitIdle(t, e)
	e.srv.appendMsg(t, "wakes the session")
	eventually(t, "uids 1,2 processed", func() bool { return equalUIDs(e.proc.got(), 1, 2) })
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
		// Detected by await's timeout, not by some unrelated connection error.
		if !errors.Is(err, errDead) {
			t.Fatalf("err = %v, want errDead", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dead connection not detected")
	}
}

// dropLines makes a rewriter that drops server lines containing any of subs
// and counts the dropped lines in dropped.
func dropLines(dropped *atomic.Int32, subs ...string) func() func([]byte) []byte {
	return func() func([]byte) []byte {
		return func(line []byte) []byte {
			for _, s := range subs {
				if bytes.Contains(line, []byte(s)) {
					dropped.Add(1)
					return nil
				}
			}
			return line
		}
	}
}

// threeMinusLast leaves uids 1 and 2 in the mailbox with UIDNEXT 4, so that
// UIDNEXT-1 (3) and the highest UID (2) tell the two fallbacks apart.
func threeMinusLast(t *testing.T, e *env) {
	t.Helper()
	for i := 0; i < 3; i++ {
		e.srv.appendMsg(t, "backlog")
	}
	if err := e.srv.expunge(3); err != nil {
		t.Fatal(err)
	}
}

// firstRunWatermark runs one session and returns the watermark it stored.
func firstRunWatermark(t *testing.T, e *env) uint32 {
	t.Helper()
	stop, _, _ := runSession(t, e)
	stop()
	st, ok, err := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if err != nil || !ok {
		t.Fatalf("state: ok=%v err=%v", ok, err)
	}
	if n := len(e.proc.got()); n != 0 {
		t.Errorf("%d backlog messages processed", n)
	}
	return st.LastUID
}

// SELECT without UIDNEXT: the first-run watermark comes from STATUS.
func TestUIDNextFromStatus(t *testing.T) {
	e := newEnv(t, nil, nil)
	var dropped atomic.Int32
	e.dialer.rewriter = dropLines(&dropped, "[UIDNEXT ")
	threeMinusLast(t, e)
	if got := firstRunWatermark(t, e); got != 3 {
		t.Fatalf("last_uid = %d, want 3 (UIDNEXT 4 from STATUS)", got)
	}
	if dropped.Load() == 0 {
		t.Fatal("SELECT response had no UIDNEXT to drop; the fallback was not exercised")
	}
}

// Neither SELECT nor STATUS reports UIDNEXT: fall back to the highest UID.
func TestUIDNextFromSearch(t *testing.T) {
	e := newEnv(t, nil, nil)
	var dropped atomic.Int32
	e.dialer.rewriter = dropLines(&dropped, "[UIDNEXT ", "* STATUS ")
	threeMinusLast(t, e)
	if got := firstRunWatermark(t, e); got != 2 {
		t.Fatalf("last_uid = %d, want 2 (highest UID from SEARCH)", got)
	}
	if dropped.Load() < 2 {
		t.Fatalf("dropped %d lines, want UIDNEXT from both SELECT and STATUS", dropped.Load())
	}
}

func TestUIDNextFromSearchEmptyMailbox(t *testing.T) {
	e := newEnv(t, nil, nil)
	var dropped atomic.Int32
	e.dialer.rewriter = dropLines(&dropped, "[UIDNEXT ", "* STATUS ")
	if got := firstRunWatermark(t, e); got != 0 {
		t.Fatalf("last_uid = %d, want 0", got)
	}
	stop, _, _ := runSession(t, e)
	defer stop()
	waitIdle(t, e)
	e.srv.appendMsg(t, "first")
	eventually(t, "uid 1 processed", func() bool { return equalUIDs(e.proc.got(), 1) })
}

// A message expunged after its metadata FETCH but before its body FETCH is
// passed over: the watermark moves past it and later mail still flows.
func TestMessageExpungedBetweenFetches(t *testing.T) {
	e := newEnv(t, nil, nil)
	var once sync.Once
	e.proc.onDecide = func(env pipeline.Envelope) {
		if env.UID == 1 {
			once.Do(func() {
				if err := e.srv.expunge(1); err != nil {
					t.Error(err)
				}
			})
		}
	}
	stop, _, done := runSession(t, e)
	defer stop()
	waitIdle(t, e)
	e.srv.appendMsg(t, "deleted right away")
	waitIdles(t, e, 2) // handled uid 1, back in IDLE
	st, _, _ := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if st.LastUID != 1 {
		t.Fatalf("watermark = %+v, want last_uid 1", st)
	}
	if n := e.dialer.last().bodies.Load(); n != 1 {
		t.Fatalf("%d body FETCHes, want 1", n)
	}
	e.srv.appendMsg(t, "next")
	eventually(t, "uid 2 processed", func() bool { return equalUIDs(e.proc.got(), 2) })
	select {
	case err := <-done:
		t.Fatalf("session ended: %v", err)
	default:
	}
}

func TestToEnvelope(t *testing.T) {
	date := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	full := toEnvelope(&imapclient.FetchMessageBuffer{
		UID: 7, InternalDate: date, RFC822Size: 1234,
		Envelope: &imap.Envelope{
			Subject: "登入驗證碼",
			From: []imap.Address{
				{Name: "Bank", Mailbox: "noreply", Host: "bank.example"},
				{Mailbox: "second", Host: "x.example"},
			},
		},
	})
	want := pipeline.Envelope{UID: 7, InternalDate: date, Size: 1234, Subject: "登入驗證碼", From: "noreply@bank.example"}
	if full != want {
		t.Errorf("full = %+v, want %+v", full, want)
	}
	if got := toEnvelope(&imapclient.FetchMessageBuffer{UID: 8, Envelope: &imap.Envelope{Subject: "s"}}); got != (pipeline.Envelope{UID: 8, Subject: "s"}) {
		t.Errorf("no From = %+v", got)
	}
	if got := toEnvelope(&imapclient.FetchMessageBuffer{UID: 9, RFC822Size: 5}); got != (pipeline.Envelope{UID: 9, Size: 5}) {
		t.Errorf("no ENVELOPE = %+v", got)
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
