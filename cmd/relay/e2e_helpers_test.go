package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/lxtsuddy/verify-code-relay/internal/runtime"
	"github.com/lxtsuddy/verify-code-relay/internal/watcher"
)

// Shared helpers for the end-to-end tests: an in-memory IMAP server whose client
// connections count IDLE commands, and a fake Telegram API.

const (
	memUser = "me@example.com"
	memPass = "pw"
)

type e2eNopLogger struct{}

func (e2eNopLogger) Printf(string, ...any) {}

// idleConn counts IDLE commands written by the client.
type idleConn struct {
	net.Conn
	idles *atomic.Int32
}

func (c idleConn) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(" IDLE\r\n")) {
		c.idles.Add(1)
	}
	return c.Conn.Write(p)
}

// memIMAP is an in-memory IMAP server with user memUser (password memPass) and an
// empty INBOX, listening on 127.0.0.1 without TLS.
type memIMAP struct {
	addr *net.TCPAddr

	mu    sync.Mutex
	conns []*atomic.Int32 // IDLE commands sent on each connection made through dial, in dial order
}

// dials returns the number of connections made through dial so far.
func (m *memIMAP) dials() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.conns)
}

// idlingSince reports whether a connection dialed after the first n has sent IDLE.
// imapmemserver announces new mail only to a client that is already idling, so a test
// must wait for this before appending mail. Every config save restarts the watchers:
// take n from dials before the save to wait for the new watcher, not the old one.
func (m *memIMAP) idlingSince(n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conns[min(n, len(m.conns)):] {
		if c.Load() > 0 {
			return true
		}
	}
	return false
}

func startMemIMAP(t *testing.T) *memIMAP {
	t.Helper()
	mem := imapmemserver.New()
	u := imapmemserver.NewUser(memUser, memPass)
	u.Create("INBOX", nil)
	u.Create("Spam", nil)
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
		Logger:       e2eNopLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &memIMAP{addr: ln.Addr().(*net.TCPAddr)}
}

// dial is a runtime.DialFactory that connects every mailbox on host 127.0.0.1 to this
// server in plain text, counting IDLE commands per connection.
func (m *memIMAP) dial(t *testing.T) runtime.DialFactory {
	return func(host string, _ int) watcher.DialFunc {
		if host != "127.0.0.1" {
			t.Errorf("dial host = %q", host)
		}
		return func(_ context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
			nc, err := net.Dial("tcp", m.addr.String())
			if err != nil {
				return nil, err
			}
			idles := new(atomic.Int32)
			m.mu.Lock()
			m.conns = append(m.conns, idles)
			m.mu.Unlock()
			return imapclient.New(idleConn{Conn: nc, idles: idles}, opts), nil
		}
	}
}

// idling reports whether at least n connections have sent IDLE.
func (m *memIMAP) idling(n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := 0
	for _, c := range m.conns {
		if c.Load() > 0 {
			k++
		}
	}
	return k >= n
}

// appendMail stores raw (any line ends) in INBOX through a separate client.
func (m *memIMAP) appendMail(t *testing.T, raw []byte) {
	t.Helper()
	m.appendMailTo(t, "INBOX", raw)
}

// appendMailTo is appendMail into folder.
func (m *memIMAP) appendMailTo(t *testing.T, folder string, raw []byte) {
	t.Helper()
	c, err := imapclient.DialInsecure(m.addr.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login(memUser, memPass).Wait(); err != nil {
		t.Fatal(err)
	}
	msg := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", "\r\n")
	cmd := c.Append(folder, int64(len(msg)), nil)
	cmd.Write([]byte(msg))
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

type sentMessage struct {
	Path        string          `json:"-"`
	ChatID      int64           `json:"chat_id"`
	Text        string          `json:"text"`
	ParseMode   string          `json:"parse_mode"`
	ReplyMarkup json.RawMessage `json:"reply_markup"`
}

// fakeTelegram records sendMessage calls and answers getMe for any token.
type fakeTelegram struct {
	*httptest.Server
	mu   sync.Mutex
	sent []sentMessage
}

func startFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var m sentMessage
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				t.Errorf("bad sendMessage body: %v", err)
			}
			m.Path = r.URL.Path
			f.mu.Lock()
			f.sent = append(f.sent, m)
			f.mu.Unlock()
			io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Relay","username":"relay_test_bot"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// messages returns every sendMessage call so far.
func (f *fakeTelegram) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

// waitFor polls cond for up to 10 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
