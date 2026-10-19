package dispatcher

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/store"
	"verify-code-relay/internal/telegram"
)

type fakeSender struct {
	errs []error // consumed in order; nil = success
	reqs []telegram.SendRequest
}

func (f *fakeSender) Send(_ context.Context, r telegram.SendRequest) error {
	f.reqs = append(f.reqs, r)
	if len(f.errs) == 0 {
		return nil
	}
	e := f.errs[0]
	f.errs = f.errs[1:]
	return e
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type alerts struct{ keys []string }

func (a *alerts) alert(key, _ string) { a.keys = append(a.keys, key) }

func setup(t *testing.T, errs ...error) (*Dispatcher, *store.Store, *fakeSender, *clock, *alerts) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := &fakeSender{errs: errs}
	ck := &clock{t: time.Unix(1_800_000_000, 0)}
	al := &alerts{}
	d := New(Options{Store: st, Sender: fs, Tokens: map[string]string{"main": "TOKEN"}, Alert: al.alert,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: ck.now})
	return d, st, fs, ck, al
}

var ctx = context.Background()

func record(t *testing.T, st *store.Store, now time.Time, chats ...int64) int64 {
	t.Helper()
	var items []store.OutboxItem
	for _, c := range chats {
		items = append(items, store.OutboxItem{Target: "t", Bot: "main", ChatID: c, Text: "<b>hi</b>",
			ReplyMarkup: `{"inline_keyboard":[]}`, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), NextAttemptAt: now})
	}
	id, err := st.RecordMessage(ctx, store.LogEntry{At: now, Mailbox: "g", Folder: "INBOX", UID: 1, Kind: "code", Result: "pending"},
		items, store.MailboxState{UIDValidity: 1, LastUID: 1})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pending(t *testing.T, st *store.Store, now time.Time) []store.OutboxItem {
	items, err := st.DueOutbox(ctx, now.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestSendSuccess(t *testing.T) {
	d, st, fs, ck, _ := setup(t)
	record(t, st, ck.t, 1, 2)
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fs.reqs) != 2 || fs.reqs[0].Token != "TOKEN" || !fs.reqs[0].HTML || fs.reqs[0].ReplyMarkup == "" {
		t.Fatalf("reqs = %+v", fs.reqs)
	}
	if len(pending(t, st, ck.t)) != 0 {
		t.Error("outbox not drained")
	}
}

func TestPerChatSpacing(t *testing.T) {
	d, st, fs, ck, _ := setup(t)
	record(t, st, ck.t, 1)
	record(t, st, ck.t, 1)
	d.RunOnce(ctx)
	d.RunOnce(ctx) // same instant: second message to same chat must wait
	if len(fs.reqs) != 1 {
		t.Fatalf("sent %d, want 1", len(fs.reqs))
	}
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 2 {
		t.Fatalf("sent %d, want 2", len(fs.reqs))
	}
}

func TestRateLimited(t *testing.T) {
	d, st, fs, ck, _ := setup(t, &telegram.SendError{Kind: telegram.RateLimited, RetryAfter: 7 * time.Second})
	record(t, st, ck.t, 1)
	d.RunOnce(ctx)
	ck.t = ck.t.Add(6 * time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 1 {
		t.Fatalf("retried too early: %d", len(fs.reqs))
	}
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 2 || len(pending(t, st, ck.t)) != 0 {
		t.Fatalf("reqs=%d pending=%d", len(fs.reqs), len(pending(t, st, ck.t)))
	}
}

func TestTransientBackoff(t *testing.T) {
	d, st, fs, ck, _ := setup(t, &telegram.SendError{Kind: telegram.Transient}, &telegram.SendError{Kind: telegram.Transient})
	record(t, st, ck.t, 1)
	d.RunOnce(ctx) // fail #1 -> next in 2s
	ck.t = ck.t.Add(2 * time.Second)
	d.RunOnce(ctx) // fail #2 -> next in 4s
	ck.t = ck.t.Add(3 * time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 2 {
		t.Fatalf("reqs = %d", len(fs.reqs))
	}
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 3 {
		t.Fatalf("reqs = %d", len(fs.reqs))
	}
}

func TestBadRequestFallsBackToPlainThenFails(t *testing.T) {
	d, st, fs, ck, al := setup(t, &telegram.SendError{Kind: telegram.BadRequest}, &telegram.SendError{Kind: telegram.BadRequest})
	record(t, st, ck.t, 1)
	d.RunOnce(ctx)
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 2 {
		t.Fatalf("reqs = %d", len(fs.reqs))
	}
	if fs.reqs[1].HTML || fs.reqs[1].ReplyMarkup != "" || fs.reqs[1].Text != "hi" {
		t.Errorf("fallback request = %+v", fs.reqs[1])
	}
	if len(pending(t, st, ck.t)) != 0 || len(al.keys) != 1 || !strings.HasPrefix(al.keys[0], "send_failed:") {
		t.Errorf("pending/alerts wrong: %v", al.keys)
	}
}

func TestForbiddenAlertsOnceButNotForAlertItems(t *testing.T) {
	d, st, _, ck, al := setup(t, &telegram.SendError{Kind: telegram.Forbidden}, &telegram.SendError{Kind: telegram.Forbidden})
	record(t, st, ck.t, 1)
	st.Enqueue(ctx, store.OutboxItem{Target: "admin", Bot: "main", ChatID: 9, Text: "alert", CreatedAt: ck.t,
		ExpiresAt: ck.t.Add(time.Hour), NextAttemptAt: ck.t})
	d.RunOnce(ctx)
	if len(al.keys) != 1 {
		t.Fatalf("alerts = %v (alert-item failure must not alert)", al.keys)
	}
}

func TestExpired(t *testing.T) {
	d, st, fs, ck, al := setup(t)
	record(t, st, ck.t, 1)
	ck.t = ck.t.Add(16 * time.Minute)
	d.RunOnce(ctx)
	if len(fs.reqs) != 0 || len(pending(t, st, ck.t)) != 0 || len(al.keys) != 0 {
		t.Fatalf("reqs=%d alerts=%v", len(fs.reqs), al.keys)
	}
}

func TestUnknownBotFailsItem(t *testing.T) {
	d, st, fs, ck, al := setup(t)
	st.Enqueue(ctx, store.OutboxItem{LogID: 0, Target: "x", Bot: "ghost", ChatID: 1, Text: "x", CreatedAt: ck.t,
		ExpiresAt: ck.t.Add(time.Hour), NextAttemptAt: ck.t})
	d.RunOnce(ctx)
	if len(fs.reqs) != 0 || len(pending(t, st, ck.t)) != 0 || len(al.keys) != 0 {
		t.Fatalf("reqs=%d alerts=%v", len(fs.reqs), al.keys)
	}
}
