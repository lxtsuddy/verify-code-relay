package watcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"regexp"
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

	mu     sync.Mutex
	idling map[string]int // client address -> IDLE commands the server has started serving
}

// idleSession reports every IDLE the server starts serving. imapmemserver
// registers the IDLE right after the call (same goroutine, no blocking), and only
// announces mail that arrives once registered: it does not report changes queued
// before IDLE started. Waiting for this hook, rather than for the client to write
// IDLE, is what lets tests append "during IDLE" reliably.
type idleSession struct {
	imapserver.SessionIMAP4rev2
	idle func()
}

func (s *idleSession) Idle(w *imapserver.UpdateWriter, stop <-chan struct{}) error {
	s.idle()
	return s.SessionIMAP4rev2.Idle(w, stop)
}

// idles returns how many IDLE commands the server has started on the
// connection whose client side is local.
func (s *testServer) idles(local net.Addr) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idling[local.String()]
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
	ts := &testServer{user: u, idling: map[string]int{}}
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(conn *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			client := conn.NetConn().RemoteAddr().String()
			return &idleSession{
				SessionIMAP4rev2: mem.NewSession().(imapserver.SessionIMAP4rev2),
				idle: func() {
					ts.mu.Lock()
					ts.idling[client]++
					ts.mu.Unlock()
				},
			}, nil, nil
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
	ts.addr, ts.srv = ln.Addr().String(), srv
	return ts
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

func (s *testServer) appendMsg(t *testing.T, subject string) {
	t.Helper()
	if err := s.add(subject); err != nil {
		t.Fatal(err)
	}
}

// add appends a message; unlike appendMsg it is safe to call from the
// watcher's goroutine (Processor hooks).
func (s *testServer) add(subject string) error {
	return s.with(func(c *imapclient.Client) error {
		msg := strings.ReplaceAll("From: a@b.c\nTo: me@example.com\nSubject: "+subject+"\n\nbody\n", "\n", "\r\n")
		cmd := c.Append("INBOX", int64(len(msg)), nil)
		cmd.Write([]byte(msg))
		cmd.Close()
		_, err := cmd.Wait()
		return err
	})
}

// expunge permanently deletes the message with the given UID.
func (s *testServer) expunge(uid uint32) error {
	return s.with(func(c *imapclient.Client) error {
		if _, err := c.Select("INBOX", nil).Wait(); err != nil {
			return err
		}
		set := imap.UIDSetNum(imap.UID(uid))
		if err := c.Store(set, &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, nil).Close(); err != nil {
			return err
		}
		return c.UIDExpunge(set).Close()
	})
}

// with runs f on a fresh logged-in connection of the test user.
func (s *testServer) with(f func(*imapclient.Client) error) error {
	c, err := imapclient.DialInsecure(s.addr, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Login("me@example.com", "pw").Wait(); err != nil {
		return err
	}
	return f(c)
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
	// killBody, when set and true, is cleared and the connection closed right
	// after the client writes a BODY.PEEK[] fetch, so the fetch is in flight.
	killBody *atomic.Bool
	frozen   atomic.Bool
	// killed is set when the body fetch is killed: from then on every read fails,
	// so a reply the server sends before Close lands cannot reach the client.
	killed atomic.Bool
	idles  atomic.Int32 // IDLE commands written by the client
	noops  atomic.Int32 // NOOP commands written by the client
	bodies atomic.Int32 // BODY.PEEK[] fetches written by the client

	// rewrite, when set, sees every complete line the server sends (CRLF
	// included, literal contents too) and returns its replacement; nil drops it.
	rewrite func(line []byte) []byte
	partial []byte // server bytes after the last complete line
	out     []byte // rewritten bytes not yet returned to the client
}

func (c *ctlConn) Read(p []byte) (int, error) {
	if c.rewrite == nil {
		return c.read(p)
	}
	buf := make([]byte, 4096)
	for len(c.out) == 0 {
		n, err := c.read(buf)
		c.partial = append(c.partial, buf[:n]...)
		for {
			i := bytes.IndexByte(c.partial, '\n')
			if i < 0 {
				break
			}
			c.out = append(c.out, c.rewrite(c.partial[:i+1])...)
			c.partial = c.partial[i+1:]
		}
		if err != nil && len(c.out) == 0 {
			return 0, err
		}
	}
	n := copy(p, c.out)
	c.out = c.out[n:]
	return n, nil
}

func (c *ctlConn) read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if c.killed.Load() {
			return 0, net.ErrClosed
		}
		if err != nil || !c.frozen.Load() {
			return n, err
		}
	}
}

func (c *ctlConn) Write(p []byte) (int, error) {
	if c.frozen.Load() {
		return len(p), nil
	}
	if bytes.Contains(p, []byte(" IDLE\r\n")) {
		c.idles.Add(1)
	}
	if bytes.Contains(p, []byte(" NOOP\r\n")) {
		c.noops.Add(1)
	}
	if bytes.Contains(p, []byte("BODY.PEEK[]")) {
		c.bodies.Add(1)
	}
	if c.killBody != nil && bytes.Contains(p, []byte("BODY.PEEK[]")) && c.killBody.CompareAndSwap(true, false) {
		c.killed.Store(true)
		n, err := c.Conn.Write(p)
		c.Conn.Close()
		return n, err
	}
	return c.Conn.Write(p)
}

type testDialer struct {
	addr string
	fail atomic.Bool
	// killBodyFetch kills the connection during the next body FETCH (one shot).
	killBodyFetch atomic.Bool
	// rewriter, when set, makes the rewrite function of each new connection.
	rewriter func() func(line []byte) []byte
	count    atomic.Int32
	mu       sync.Mutex
	conns    []*ctlConn
}

var existsLine = regexp.MustCompile(`^\* \d+ EXISTS\r\n$`)

// holdExists drops "* n EXISTS" outside IDLE, like servers that report new mail
// only while idling. The IDLE state follows the server stream: a continuation
// request starts it (the watcher sends no other command that needs one) and the
// next tagged response ends it.
func holdExists() func([]byte) []byte {
	idling := false
	return func(line []byte) []byte {
		switch {
		case bytes.HasPrefix(line, []byte("+ ")):
			idling = true
		case !bytes.HasPrefix(line, []byte("* ")):
			idling = false // tagged response (or literal data, which never occurs while idling)
		case !idling && existsLine.Match(line):
			return nil
		}
		return line
	}
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
	cc := &ctlConn{Conn: nc, killBody: &d.killBodyFetch}
	if d.rewriter != nil {
		cc.rewrite = d.rewriter()
	}
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
	failAll   atomic.Bool // every Process call fails
	// onDecide, when set, runs on the watcher's goroutine (without f.mu held)
	// after the metadata FETCH and before the body FETCH of each message.
	onDecide func(env pipeline.Envelope)
}

func (f *fakeProc) Decide(_ pipeline.Mailbox, env pipeline.Envelope) string {
	f.mu.Lock()
	hook, kind := f.onDecide, f.skip[env.UID]
	f.mu.Unlock()
	if hook != nil {
		hook(env)
	}
	return kind
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
	if f.failAll.Load() {
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
	texts     map[string]string // key -> text of the latest Alert or Recovered
}

func (a *fakeAlerts) Alert(key, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, key)
	a.setText("alert:"+key, text)
}

func (a *fakeAlerts) Recovered(key, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered = append(a.recovered, key)
	a.setText("recovered:"+key, text)
}

func (a *fakeAlerts) setText(k, text string) {
	if a.texts == nil {
		a.texts = map[string]string{}
	}
	a.texts[k] = text
}

// alertText and recoveredText return the latest text sent for key.
func (a *fakeAlerts) alertText(key string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.texts["alert:"+key]
}

func (a *fakeAlerts) recoveredText(key string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.texts["recovered:"+key]
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
