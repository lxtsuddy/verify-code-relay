package watcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/store"
)

type testServer struct {
	addr string
	user *imapmemserver.User
	srv  *imapserver.Server
}

func startServer(t *testing.T, caps imap.CapSet) *testServer {
	t.Helper()
	if caps == nil {
		caps = imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}}
	}
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("me@example.com", "pw")
	u.Create("INBOX", nil)
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         caps,
		InsecureAuth: true,
		Logger:       nopLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &testServer{addr: ln.Addr().String(), user: u, srv: srv}
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

func (s *testServer) appendMsg(t *testing.T, subject string) {
	t.Helper()
	c, err := imapclient.DialInsecure(s.addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("me@example.com", "pw").Wait(); err != nil {
		t.Fatal(err)
	}
	msg := strings.ReplaceAll("From: a@b.c\nTo: me@example.com\nSubject: "+subject+"\n\nbody\n", "\n", "\r\n")
	cmd := c.Append("INBOX", int64(len(msg)), nil)
	cmd.Write([]byte(msg))
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func (s *testServer) seenFlags(t *testing.T) int {
	t.Helper()
	c, err := imapclient.DialInsecure(s.addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Login("me@example.com", "pw").Wait()
	c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	var all imap.SeqSet
	all.AddRange(1, 0) // 1:*
	bufs, err := c.Fetch(all, &imap.FetchOptions{Flags: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range bufs {
		for _, f := range b.Flags {
			if f == imap.FlagSeen {
				n++
			}
		}
	}
	return n
}

// ctlConn lets tests kill a connection or turn it into a black hole.
type ctlConn struct {
	net.Conn
	frozen atomic.Bool
}

func (c *ctlConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if err != nil || !c.frozen.Load() {
			return n, err
		}
	}
}

func (c *ctlConn) Write(p []byte) (int, error) {
	if c.frozen.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

type testDialer struct {
	addr  string
	fail  atomic.Bool
	count atomic.Int32
	mu    sync.Mutex
	conns []*ctlConn
}

func (d *testDialer) dial(_ context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
	d.count.Add(1)
	if d.fail.Load() {
		return nil, errors.New("dial refused")
	}
	nc, err := net.Dial("tcp", d.addr)
	if err != nil {
		return nil, err
	}
	cc := &ctlConn{Conn: nc}
	d.mu.Lock()
	d.conns = append(d.conns, cc)
	d.mu.Unlock()
	return imapclient.New(cc, opts), nil
}

func (d *testDialer) last() *ctlConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[len(d.conns)-1]
}

type fakeProc struct {
	st        *store.Store
	mu        sync.Mutex
	processed []uint32
	skipped   []uint32
	skip      map[uint32]string
	failOnce  map[uint32]bool
}

func (f *fakeProc) Decide(_ pipeline.Mailbox, env pipeline.Envelope) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.skip[env.UID]
}

func (f *fakeProc) RecordSkipped(ctx context.Context, mb pipeline.Mailbox, folder string, uv uint32, env pipeline.Envelope, _ string) error {
	f.mu.Lock()
	f.skipped = append(f.skipped, env.UID)
	f.mu.Unlock()
	return f.st.SetMailboxState(ctx, mb.Name, folder, store.MailboxState{UIDValidity: uv, LastUID: env.UID})
}

func (f *fakeProc) Process(ctx context.Context, mb pipeline.Mailbox, folder string, uv uint32, env pipeline.Envelope, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOnce[env.UID] {
		delete(f.failOnce, env.UID)
		return errors.New("boom")
	}
	if !strings.Contains(string(raw), "Subject:") {
		return errors.New("body missing")
	}
	f.processed = append(f.processed, env.UID)
	return f.st.SetMailboxState(ctx, mb.Name, folder, store.MailboxState{UIDValidity: uv, LastUID: env.UID})
}

func (f *fakeProc) got() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.processed...)
}

type fakeAlerts struct {
	mu        sync.Mutex
	alerts    []string
	recovered []string
}

func (a *fakeAlerts) Alert(key, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, key)
}

func (a *fakeAlerts) Recovered(key, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered = append(a.recovered, key)
}

func (a *fakeAlerts) snapshot() ([]string, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.alerts...), append([]string(nil), a.recovered...)
}

type env struct {
	srv    *testServer
	dialer *testDialer
	proc   *fakeProc
	st     *store.Store
	alerts *fakeAlerts
	w      *Watcher
}

func newEnv(t *testing.T, caps imap.CapSet, mutate func(*Config)) *env {
	t.Helper()
	srv := startServer(t, caps)
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{srv: srv, dialer: &testDialer{addr: srv.addr}, proc: &fakeProc{st: st, skip: map[uint32]string{}, failOnce: map[uint32]bool{}},
		st: st, alerts: &fakeAlerts{}}
	cfg := Config{
		Mailbox:      pipeline.Mailbox{Name: "mb", Username: "me@example.com"},
		Folder:       "INBOX",
		Dial:         e.dialer.dial,
		Auth:         auth.Password{Username: "me@example.com", Password: "pw"},
		Processor:    e.proc,
		State:        st,
		Alert:        e.alerts,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: 100 * time.Millisecond,
		AlertAfter:   time.Hour,
		CmdTimeout:   2 * time.Second,
		FetchTimeout: 2 * time.Second,
		MinBackoff:   20 * time.Millisecond,
		MaxBackoff:   100 * time.Millisecond,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e.w = New(cfg)
	return e
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func equalUIDs(a []uint32, b ...uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
