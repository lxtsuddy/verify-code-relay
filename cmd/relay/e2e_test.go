package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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

	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/telegram"
	"verify-code-relay/internal/watcher"
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

type sentMessage struct {
	Path        string          `json:"-"`
	ChatID      int64           `json:"chat_id"`
	Text        string          `json:"text"`
	ParseMode   string          `json:"parse_mode"`
	ReplyMarkup json.RawMessage `json:"reply_markup"`
}

// TestEndToEnd wires the real components the way `relay run` does — config,
// SQLite store, recognizer, renderer, pipeline, dispatcher and watcher — against
// an in-memory IMAP server and a fake Telegram API.
func TestEndToEnd(t *testing.T) {
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("me@example.com", "pw")
	u.Create("INBOX", nil)
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
	defer srv.Close()
	imapAddr := ln.Addr().(*net.TCPAddr)

	var mu sync.Mutex
	var sent []sentMessage
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m sentMessage
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("bad sendMessage body: %v", err)
		}
		m.Path = r.URL.Path
		mu.Lock()
		sent = append(sent, m)
		mu.Unlock()
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer tg.Close()
	snapshot := func() []sentMessage {
		mu.Lock()
		defer mu.Unlock()
		return append([]sentMessage(nil), sent...)
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := `data_dir: ` + filepath.Join(dir, "data") + `
bots: { main: { token: "123:abc" } }
targets:
  me: { bot: main, chat_id: 42 }
  family: { bot: main, chat_id: 43 }
mailboxes:
  test:
    display_name: 測試信箱
    host: 127.0.0.1
    username: me@example.com
    auth: { type: password, password: pw }
    targets: [me, family]
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := loadApp(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStore(a.Config)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var idles atomic.Int32
	dial := func(host string, _ int) watcher.DialFunc {
		if host != "127.0.0.1" {
			t.Errorf("dial host = %q", host)
		}
		return func(_ context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
			nc, err := net.Dial("tcp", imapAddr.String())
			if err != nil {
				return nil, err
			}
			return imapclient.New(idleConn{Conn: nc, idles: &idles}, opts), nil
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := &telegram.Client{BaseURL: tg.URL, HTTP: tg.Client()}
	rt := runtime.New(runtime.Options{Store: st, Log: log, Sender: sender, Dial: dial})
	rt.Apply(a)
	defer rt.Stop()

	// imapmemserver announces new mail only to a client that is already idling.
	waitFor(t, "watcher idling", func() bool { return idles.Load() >= 1 })
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "eml", "github-device-en.eml"))
	if err != nil {
		t.Fatal(err)
	}
	appendMail(t, imapAddr.String(), raw)

	waitFor(t, "two sendMessage calls", func() bool { return len(snapshot()) >= 2 })
	time.Sleep(300 * time.Millisecond) // no duplicates may follow
	got := snapshot()
	if len(got) != 2 {
		t.Fatalf("sendMessage calls = %d, want 2: %+v", len(got), got)
	}
	chats := map[int64]bool{}
	for _, m := range got {
		chats[m.ChatID] = true
		if m.Path != "/bot123:abc/sendMessage" {
			t.Errorf("path = %q", m.Path)
		}
		if m.ParseMode != "HTML" {
			t.Errorf("parse_mode = %q, want HTML", m.ParseMode)
		}
		if !strings.Contains(m.Text, "<code>482913</code>") {
			t.Errorf("text lacks the code: %q", m.Text)
		}
		var markup struct {
			InlineKeyboard [][]struct {
				Text     string `json:"text"`
				CopyText *struct {
					Text string `json:"text"`
				} `json:"copy_text"`
			} `json:"inline_keyboard"`
		}
		if err := json.Unmarshal(m.ReplyMarkup, &markup); err != nil {
			t.Fatalf("reply_markup %s: %v", m.ReplyMarkup, err)
		}
		copyBtn := false
		for _, row := range markup.InlineKeyboard {
			for _, b := range row {
				if b.CopyText != nil && b.CopyText.Text == "482913" {
					copyBtn = true
				}
			}
		}
		if !copyBtn {
			t.Errorf("no copy_text button with the code: %s", m.ReplyMarkup)
		}
	}
	if !chats[42] || !chats[43] {
		t.Errorf("chats = %v, want 42 and 43", chats)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
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

func appendMail(t *testing.T, addr string, raw []byte) {
	t.Helper()
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("me@example.com", "pw").Wait(); err != nil {
		t.Fatal(err)
	}
	msg := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n", "\r\n")
	cmd := c.Append("INBOX", int64(len(msg)), nil)
	cmd.Write([]byte(msg))
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
