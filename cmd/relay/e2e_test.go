package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lxtsuddy/verify-code-relay/internal/runtime"
	"github.com/lxtsuddy/verify-code-relay/internal/store"
	"github.com/lxtsuddy/verify-code-relay/internal/telegram"
)

// TestEndToEnd wires the real components the way `relay run` does — config,
// SQLite store, recognizer, renderer, pipeline, dispatcher and watcher — against
// an in-memory IMAP server and a fake Telegram API.
func TestEndToEnd(t *testing.T) {
	srv := startMemIMAP(t)
	tg := startFakeTelegram(t)
	snapshot := tg.messages

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := `bots: { main: { token: "123:abc" } }
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
	st, err := store.Open(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := &telegram.Client{BaseURL: tg.URL, HTTP: tg.Client()}
	rt := runtime.New(runtime.Options{Store: st, Log: log, Sender: sender, Dial: srv.dial(t)})
	rt.Apply(a)
	defer rt.Stop()

	// imapmemserver announces new mail only to a client that is already idling.
	waitFor(t, "watcher idling", func() bool { return srv.idlingSince(0) })
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "eml", "github-device-en.eml"))
	if err != nil {
		t.Fatal(err)
	}
	srv.appendMail(t, raw)

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

// Mail found in a folder goes to that folder's targets only.
func TestEndToEndRoutesByFolder(t *testing.T) {
	srv := startMemIMAP(t)
	tg := startFakeTelegram(t)
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgYAML := `bots: { main: { token: "123:abc" } }
targets:
  me: { bot: main, chat_id: 42 }
  family: { bot: main, chat_id: 43 }
mailboxes:
  test:
    host: 127.0.0.1
    username: me@example.com
    auth: { type: password, password: pw }
    routes:
      - { folder: INBOX, target: me }
      - { folder: Spam, target: family }
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := loadApp(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sender := &telegram.Client{BaseURL: tg.URL, HTTP: tg.Client()}
	rt := runtime.New(runtime.Options{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Sender: sender, Dial: srv.dial(t)})
	rt.Apply(a)
	defer rt.Stop()

	waitFor(t, "both watchers idling", func() bool { return srv.idling(2) })
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "eml", "github-device-en.eml"))
	if err != nil {
		t.Fatal(err)
	}
	srv.appendMailTo(t, "Spam", raw)
	waitFor(t, "a message from Spam", func() bool { return len(tg.messages()) >= 1 })
	time.Sleep(300 * time.Millisecond) // nothing more may follow
	if got := tg.messages(); len(got) != 1 || got[0].ChatID != 43 {
		t.Fatalf("mail in Spam sent to %+v, want only chat 43 (family)", got)
	}
}
