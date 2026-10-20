package pipeline

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/recognize"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/store"
)

var ctx = context.Background()

func newProc(t *testing.T) (*Processor, *store.Store, *int, time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rec, err := recognize.New([]config.Rule{{Name: "promo", From: "*@news.example.com", Ignore: true}}, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Mailboxes: map[string]*config.Mailbox{"gmail": {Name: "gmail"}}}
	rnd, err := render.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	notified := 0
	now := time.Unix(1_800_000_000, 0)
	p := &Processor{Store: st, Recognizer: rec, Renderer: rnd, Notify: func() { notified++ },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	return p, st, &notified, now
}

var mb = Mailbox{
	Name: "gmail", DisplayName: "個人 Gmail", Username: "me@gmail.com",
	Targets: []Target{{Name: "me", Bot: "main", ChatID: 1}, {Name: "fam", Bot: "main", ChatID: 2}},
	MaxAge:  10 * time.Minute,
	TTL:     15 * time.Minute,
}

func eml(from, to, subject, body string) []byte {
	return []byte(strings.ReplaceAll("From: "+from+"\nTo: "+to+"\nSubject: "+subject+"\nContent-Type: text/plain; charset=utf-8\n\n"+body+"\n", "\n", "\r\n"))
}

// capturingStore records every LogEntry handed to RecordMessage before delegating.
type capturingStore struct {
	*store.Store
	entries []store.LogEntry
}

func (c *capturingStore) RecordMessage(ctx context.Context, e store.LogEntry, items []store.OutboxItem, st store.MailboxState) (int64, error) {
	c.entries = append(c.entries, e)
	return c.Store.RecordMessage(ctx, e, items, st)
}

func TestProcessCodeEnqueuesPerTarget(t *testing.T) {
	p, st, notified, now := newProc(t)
	raw := eml("GitHub <noreply@github.com>", "me+gh@gmail.com", "[GitHub] Please verify your device", "Verification code: 482913")
	err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 11, InternalDate: now, Size: int64(len(raw))}, raw)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := st.DueOutbox(ctx, now, 10)
	if len(items) != 2 || *notified != 1 {
		t.Fatalf("items=%d notified=%d", len(items), *notified)
	}
	it := items[0]
	if !strings.Contains(it.Text, "<code>482913</code>") || !strings.Contains(it.Text, "個人 Gmail") || it.ReplyMarkup == "" {
		t.Errorf("text = %q markup = %q", it.Text, it.ReplyMarkup)
	}
	if !it.ExpiresAt.Equal(now.Add(15*time.Minute)) || it.Bot != "main" || it.LogID == 0 {
		t.Errorf("item = %+v", it)
	}
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if s != (store.MailboxState{UIDValidity: 7, LastUID: 11}) {
		t.Errorf("state = %+v", s)
	}
}

func TestProcessNoMatchAdvancesState(t *testing.T) {
	p, st, notified, now := newProc(t)
	raw := eml("a@b.c", "me@gmail.com", "Hello", "Just saying hi")
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 12, InternalDate: now}, raw); err != nil {
		t.Fatal(err)
	}
	items, _ := st.DueOutbox(ctx, now, 10)
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if len(items) != 0 || *notified != 0 || s.LastUID != 12 {
		t.Fatalf("items=%d notified=%d state=%+v", len(items), *notified, s)
	}
}

func TestProcessIgnored(t *testing.T) {
	p, st, _, now := newProc(t)
	raw := eml("promo@news.example.com", "me@gmail.com", "Your code is SAVE2026", "promo code SAVE2026")
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 13, InternalDate: now}, raw); err != nil {
		t.Fatal(err)
	}
	items, _ := st.DueOutbox(ctx, now, 10)
	if len(items) != 0 {
		t.Fatalf("ignored mail forwarded: %+v", items)
	}
}

func TestProcessUnparseableAdvancesState(t *testing.T) {
	p, st, _, now := newProc(t)
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 14, InternalDate: now, From: "x@y.z", Subject: "s"}, []byte("\x00\x01 not mail")); err != nil {
		t.Fatal(err)
	}
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if s.LastUID != 14 {
		t.Fatalf("state = %+v", s)
	}
}

func TestDecideAndRecordSkipped(t *testing.T) {
	p, st, _, now := newProc(t)
	cases := []struct {
		env  Envelope
		want string
	}{
		{Envelope{UID: 1, InternalDate: now.Add(-time.Minute), Size: 1000}, ""},
		{Envelope{UID: 2, InternalDate: now.Add(-11 * time.Minute), Size: 1000}, "skipped_age"},
		{Envelope{UID: 3, InternalDate: now, Size: MaxMessageSize + 1}, "skipped_size"},
	}
	for _, c := range cases {
		if got := p.Decide(mb, c.env); got != c.want {
			t.Errorf("Decide(uid %d) = %q, want %q", c.env.UID, got, c.want)
		}
	}
	if err := p.RecordSkipped(ctx, mb, "INBOX", 7, Envelope{UID: 2, From: "a@b.c", Subject: "old"}, "skipped_age"); err != nil {
		t.Fatal(err)
	}
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if s.LastUID != 2 {
		t.Fatalf("state = %+v", s)
	}
}

// The verification code must never reach forward_log or the slog output, but the
// Telegram text must keep the real subject and code.
func TestProcessRedactsCodeInLogButNotInMessage(t *testing.T) {
	p, st, _, now := newProc(t)
	cs := &capturingStore{Store: st}
	p.Store = cs
	var buf bytes.Buffer
	p.Log = slog.New(slog.NewTextHandler(&buf, nil))
	const subject = "839201 is your code (839201)"
	raw := eml("Facebook <security@facebookmail.com>", "me@gmail.com",
		subject, "Your verification code is 839201.")
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 15, InternalDate: now, Size: int64(len(raw))}, raw); err != nil {
		t.Fatal(err)
	}

	// Telegram text: real subject kept verbatim, no redaction markers.
	items, _ := st.DueOutbox(ctx, now, 10)
	if len(items) == 0 {
		t.Fatal("no outbox items enqueued")
	}
	if !strings.Contains(items[0].Text, subject) {
		t.Errorf("outbox text lost the real subject %q: %q", subject, items[0].Text)
	}
	if strings.Contains(items[0].Text, "******") {
		t.Errorf("outbox text was redacted: %q", items[0].Text)
	}

	// forward_log: every occurrence of the code replaced.
	if len(cs.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(cs.entries))
	}
	const wantSubj = "****** is your code (******)"
	if got := cs.entries[0].Subject; got != wantSubj {
		t.Errorf("forward_log subject = %q, want %q", got, wantSubj)
	}

	// slog output: logged, code absent, redaction marker present.
	logged := buf.String()
	if logged == "" {
		t.Fatal("expected slog output")
	}
	if strings.Contains(logged, "839201") {
		t.Errorf("slog output leaked the code: %q", logged)
	}
	if !strings.Contains(logged, "******") {
		t.Errorf("slog output not redacted: %q", logged)
	}
}

// recognize strips separators from split codes, so the subject's "123 456" and
// "123-456" forms must be masked too.
func TestProcessRedactsSplitCodeInSubject(t *testing.T) {
	p, st, _, now := newProc(t)
	cs := &capturingStore{Store: st}
	p.Store = cs
	var buf bytes.Buffer
	p.Log = slog.New(slog.NewTextHandler(&buf, nil))
	const subject = "Your code 123 456 (123-456)"
	raw := eml("Example <no-reply@example.com>", "me@gmail.com", subject, "Your security code is 123 456.")
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 16, InternalDate: now, Size: int64(len(raw))}, raw); err != nil {
		t.Fatal(err)
	}
	if len(cs.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(cs.entries))
	}
	if got, want := cs.entries[0].Subject, "Your code ****** (******)"; got != want {
		t.Errorf("forward_log subject = %q, want %q", got, want)
	}
	if logged := buf.String(); strings.Contains(logged, "123 456") || strings.Contains(logged, "123-456") {
		t.Errorf("slog output leaked the code: %q", logged)
	}
	items, _ := st.DueOutbox(ctx, now, 10)
	if len(items) == 0 || !strings.Contains(items[0].Text, subject) || !strings.Contains(items[0].Text, "<code>123456</code>") {
		t.Errorf("outbox text must keep the real subject and code: %+v", items)
	}
}

// Messages that are not forwarded must record why (kind) and that nothing was
// sent (result), and still advance the mailbox state.
func TestProcessNotForwardedKindAndResult(t *testing.T) {
	cases := []struct {
		name     string
		env      Envelope
		raw      []byte
		wantKind string
		wantRule string
		wantFrom string
	}{
		{"ignored", Envelope{UID: 21}, eml("promo@news.example.com", "me@gmail.com", "Your code is SAVE2026", "promo code SAVE2026"),
			"ignored", "custom:promo", "promo@news.example.com"},
		{"none", Envelope{UID: 22}, eml("a@b.c", "me@gmail.com", "Hello", "Just saying hi"),
			"none", "", "a@b.c"},
		{"unparseable", Envelope{UID: 23, From: "x@y.z", Subject: "s"}, []byte("\x00\x01 not mail"),
			"none", "", "x@y.z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, st, notified, now := newProc(t)
			cs := &capturingStore{Store: st}
			p.Store = cs
			c.env.InternalDate = now
			if err := p.Process(ctx, mb, "INBOX", 7, c.env, c.raw); err != nil {
				t.Fatal(err)
			}
			if len(cs.entries) != 1 {
				t.Fatalf("entries = %d, want 1", len(cs.entries))
			}
			e := cs.entries[0]
			if e.Kind != c.wantKind || e.Result != "not_forwarded" || e.Rule != c.wantRule || e.FromAddr != c.wantFrom {
				t.Errorf("entry = %+v, want kind=%q result=not_forwarded rule=%q from=%q", e, c.wantKind, c.wantRule, c.wantFrom)
			}
			if e.Mailbox != "gmail" || e.Folder != "INBOX" || e.UID != c.env.UID || !e.At.Equal(now) {
				t.Errorf("entry identity = %+v", e)
			}
			items, _ := st.DueOutbox(ctx, now, 10)
			s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
			if len(items) != 0 || *notified != 0 || s != (store.MailboxState{UIDValidity: 7, LastUID: c.env.UID}) {
				t.Errorf("items=%d notified=%d state=%+v", len(items), *notified, s)
			}
		})
	}
}

func TestRecordSkippedKindAndResult(t *testing.T) {
	for _, kind := range []string{"skipped_age", "skipped_size"} {
		t.Run(kind, func(t *testing.T) {
			p, st, notified, now := newProc(t)
			cs := &capturingStore{Store: st}
			p.Store = cs
			env := Envelope{UID: 31, From: "a@b.c", Subject: "old"}
			if err := p.RecordSkipped(ctx, mb, "INBOX", 7, env, kind); err != nil {
				t.Fatal(err)
			}
			if len(cs.entries) != 1 {
				t.Fatalf("entries = %d, want 1", len(cs.entries))
			}
			want := store.LogEntry{At: now, Mailbox: "gmail", Folder: "INBOX", UID: 31, FromAddr: "a@b.c",
				Subject: "old", Kind: kind, Result: "not_forwarded"}
			if e := cs.entries[0]; e != want {
				t.Errorf("entry = %+v, want %+v", e, want)
			}
			items, _ := st.DueOutbox(ctx, now, 10)
			s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
			if len(items) != 0 || *notified != 0 || s != (store.MailboxState{UIDValidity: 7, LastUID: 31}) {
				t.Errorf("items=%d notified=%d state=%+v", len(items), *notified, s)
			}
		})
	}
}

// corpus reads a message from the shared recognition test corpus.
func corpus(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "eml", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestProcessForwardedKind(t *testing.T) {
	cases := []struct {
		name     string
		raw      []byte
		wantKind string
	}{
		{"code", eml("GitHub <noreply@github.com>", "me@gmail.com", "[GitHub] Please verify your device", "Verification code: 482913"), "code"},
		{"link", corpus(t, "verify-email-link-en.eml"), "link"},
		{"both", corpus(t, "code-and-link-en.eml"), "both"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, st, notified, now := newProc(t)
			cs := &capturingStore{Store: st}
			p.Store = cs
			if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 41, InternalDate: now}, c.raw); err != nil {
				t.Fatal(err)
			}
			if len(cs.entries) != 1 {
				t.Fatalf("entries = %d, want 1", len(cs.entries))
			}
			e := cs.entries[0]
			if e.Kind != c.wantKind || e.Result != "pending" || e.Rule == "" {
				t.Errorf("entry = %+v, want kind=%q result=pending and a rule", e, c.wantKind)
			}
			items, _ := st.DueOutbox(ctx, now, 10)
			if len(items) != len(mb.Targets) || *notified != 1 {
				t.Errorf("items=%d notified=%d", len(items), *notified)
			}
		})
	}
}

type failingRenderer struct{ err error }

func (f failingRenderer) Render(string, render.Data) (render.Rendered, error) {
	return render.Rendered{}, f.err
}

// A template failure records the message as failed, enqueues nothing, still
// advances the state (so the message is not retried forever) and never logs the code.
func TestProcessRenderFailure(t *testing.T) {
	p, st, notified, now := newProc(t)
	cs := &capturingStore{Store: st}
	p.Store = cs
	p.Renderer = failingRenderer{err: errors.New("template: code: boom")}
	var buf bytes.Buffer
	p.Log = slog.New(slog.NewTextHandler(&buf, nil))
	raw := eml("GitHub <noreply@github.com>", "me@gmail.com", "482913 is your GitHub code", "Verification code: 482913")
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 51, InternalDate: now}, raw); err != nil {
		t.Fatal(err)
	}
	if len(cs.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(cs.entries))
	}
	e := cs.entries[0]
	if e.Kind != "code" || e.Result != "failed" || e.Rule == "" || e.Subject != "****** is your GitHub code" {
		t.Errorf("entry = %+v", e)
	}
	items, _ := st.DueOutbox(ctx, now, 10)
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if len(items) != 0 || *notified != 0 || s != (store.MailboxState{UIDValidity: 7, LastUID: 51}) {
		t.Errorf("items=%d notified=%d state=%+v", len(items), *notified, s)
	}
	logged := buf.String()
	if !strings.Contains(logged, "套用範本失敗") || !strings.Contains(logged, "boom") {
		t.Errorf("render failure not logged: %q", logged)
	}
	if strings.Contains(logged, "482913") {
		t.Errorf("slog output leaked the code: %q", logged)
	}
}
