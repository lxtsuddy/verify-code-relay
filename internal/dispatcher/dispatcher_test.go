package dispatcher

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"verify-code-relay/internal/store"
	"verify-code-relay/internal/telegram"
)

type fakeSender struct {
	mu     sync.Mutex
	errs   []error // consumed in order; nil = success
	reqs   []telegram.SendRequest
	before func()                    // optional hook run at the start of each Send
	sent   chan telegram.SendRequest // optional; receives every request after it is recorded
}

func (f *fakeSender) Send(_ context.Context, r telegram.SendRequest) error {
	if f.before != nil {
		f.before()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	if f.sent != nil {
		f.sent <- r
	}
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
	d, st, fs, ck, al, _ := setupDB(t, errs...)
	return d, st, fs, ck, al
}

// setupDB is setup that also returns the database path, for tests that inspect forward_log directly.
func setupDB(t *testing.T, errs ...error) (*Dispatcher, *store.Store, *fakeSender, *clock, *alerts, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := &fakeSender{errs: errs}
	ck := &clock{t: time.Unix(1_800_000_000, 0)}
	al := &alerts{}
	d := New(Options{Store: st, Sender: fs, Tokens: map[string]string{"main": "TOKEN"}, Alert: al.alert,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: ck.now})
	return d, st, fs, ck, al, path
}

var ctx = context.Background()

func record(t *testing.T, st *store.Store, now time.Time, chats ...int64) int64 {
	t.Helper()
	return recordAs(t, st, now, "main", "<b>hi</b>", chats...)
}

func recordAs(t *testing.T, st *store.Store, now time.Time, bot, text string, chats ...int64) int64 {
	t.Helper()
	var items []store.OutboxItem
	for _, c := range chats {
		items = append(items, store.OutboxItem{Target: "t", Bot: bot, ChatID: c, Text: text,
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

// flakyStore wraps a real store, failing DeleteOutbox / RescheduleOutbox once on request
// and recording every SetLogResult call.
type flakyStore struct {
	*store.Store
	failDelete     bool
	failReschedule bool
	results        []string
}

func (f *flakyStore) DeleteOutbox(ctx context.Context, id int64) error {
	if f.failDelete {
		f.failDelete = false
		return errors.New("disk I/O error")
	}
	return f.Store.DeleteOutbox(ctx, id)
}

func (f *flakyStore) RescheduleOutbox(ctx context.Context, id int64, attempts int, next time.Time, plainFallback bool) error {
	if f.failReschedule {
		f.failReschedule = false
		return errors.New("disk I/O error")
	}
	return f.Store.RescheduleOutbox(ctx, id, attempts, next, plainFallback)
}

func (f *flakyStore) SetLogResult(ctx context.Context, logID int64, result string) error {
	f.results = append(f.results, result)
	return f.Store.SetLogResult(ctx, logID, result)
}

func TestDeleteErrorAfterSendIsNotMarkedSent(t *testing.T) {
	d, st, fs, ck, al := setup(t)
	record(t, st, ck.t, 1)
	fl := &flakyStore{Store: st, failDelete: true}
	d.o.Store = fl
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fs.reqs) != 1 {
		t.Fatalf("sent %d, want 1", len(fs.reqs))
	}
	if len(fl.results) != 0 || len(al.keys) != 0 {
		t.Errorf("follow-ups after failed delete: results=%v alerts=%v", fl.results, al.keys)
	}
	if len(pending(t, st, ck.t)) != 1 {
		t.Error("item should remain in outbox after failed delete")
	}
	// Next cycle: the message already went out, so only the delete is retried.
	ck.t = ck.t.Add(time.Second)
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fs.reqs) != 1 {
		t.Errorf("re-sent after failed delete: sent %d, want 1", len(fs.reqs))
	}
	if len(fl.results) != 1 || fl.results[0] != "sent" {
		t.Errorf("results = %v, want [sent]", fl.results)
	}
	if len(pending(t, st, ck.t)) != 0 {
		t.Error("outbox not drained after delete retry")
	}
}

func TestSentButUndeletedExpiresAsSent(t *testing.T) {
	d, st, fs, ck, _ := setup(t)
	record(t, st, ck.t, 1)
	fl := &flakyStore{Store: st, failDelete: true}
	d.o.Store = fl
	d.RunOnce(ctx)
	// The row expires before the delete is retried: it was delivered, so it must be recorded as sent.
	ck.t = ck.t.Add(16 * time.Minute)
	d.RunOnce(ctx)
	if len(fs.reqs) != 1 || len(fl.results) != 1 || fl.results[0] != "sent" {
		t.Errorf("reqs=%d results=%v, want 1 [sent]", len(fs.reqs), fl.results)
	}
	if len(d.sentUndeleted) != 0 {
		t.Errorf("sentUndeleted = %v, want empty", d.sentUndeleted)
	}
}

func TestRescheduleErrorSkipsFollowUps(t *testing.T) {
	d, st, fs, ck, al := setup(t, &telegram.SendError{Kind: telegram.Transient})
	record(t, st, ck.t, 1)
	fl := &flakyStore{Store: st, failReschedule: true}
	d.o.Store = fl
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fs.reqs) != 1 || len(fl.results) != 0 || len(al.keys) != 0 {
		t.Errorf("reqs=%d results=%v alerts=%v", len(fs.reqs), fl.results, al.keys)
	}
}

func TestDeleteErrorOnFailureSkipsResultAndAlert(t *testing.T) {
	d, st, _, ck, al := setup(t, &telegram.SendError{Kind: telegram.Forbidden})
	record(t, st, ck.t, 1)
	fl := &flakyStore{Store: st, failDelete: true}
	d.o.Store = fl
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fl.results) != 0 || len(al.keys) != 0 {
		t.Errorf("follow-ups after failed delete: results=%v alerts=%v", fl.results, al.keys)
	}
}

func TestRetryScheduledFromFailureTime(t *testing.T) {
	d, st, fs, ck, _ := setup(t, &telegram.SendError{Kind: telegram.RateLimited, RetryAfter: 7 * time.Second})
	start := ck.t
	record(t, st, start, 1)
	// The send itself takes 5 s; retry_after must count from when the failure was observed.
	fs.before = func() { ck.t = ck.t.Add(5 * time.Second) }
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	ck.t = start.Add(11 * time.Second)
	if n := len(dueAt(t, st, ck.t)); n != 0 {
		t.Fatalf("due at start+11s = %d, want 0", n)
	}
	ck.t = start.Add(12 * time.Second)
	if n := len(dueAt(t, st, ck.t)); n != 1 {
		t.Fatalf("due at start+12s = %d, want 1", n)
	}
}

func dueAt(t *testing.T, st *store.Store, now time.Time) []store.OutboxItem {
	t.Helper()
	items, err := st.DueOutbox(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestPerChatGapMeasuredFromSendStart(t *testing.T) {
	d, st, fs, ck, _ := setup(t)
	record(t, st, ck.t, 1)
	record(t, st, ck.t, 1)
	// Each send takes 5 s. The gap is start-to-start, so the second message is due as soon as
	// the first send has finished, with no extra wait.
	fs.before = func() { ck.t = ck.t.Add(5 * time.Second) }
	d.RunOnce(ctx) // starts at T, ends at T+5
	d.RunOnce(ctx) // clock is T+5: 5 s since the last send started, so it must go out now
	if len(fs.reqs) != 2 {
		t.Fatalf("sent %d, want 2", len(fs.reqs))
	}
}

func TestRateLimitDoesNotCountAsAttempt(t *testing.T) {
	d, st, fs, ck, _ := setup(t,
		&telegram.SendError{Kind: telegram.RateLimited, RetryAfter: time.Second},
		&telegram.SendError{Kind: telegram.Transient})
	record(t, st, ck.t, 1)
	d.RunOnce(ctx) // 429 -> retry in 1s
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx) // transient: first real failure, so the backoff starts at 2s
	ck.t = ck.t.Add(2 * time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 3 {
		t.Fatalf("reqs = %d, want 3 (backoff should not count the 429)", len(fs.reqs))
	}
}

func TestPlainFallbackDoesNotCountAsAttempt(t *testing.T) {
	d, st, fs, ck, _ := setup(t,
		&telegram.SendError{Kind: telegram.BadRequest},
		&telegram.SendError{Kind: telegram.Transient})
	record(t, st, ck.t, 1)
	d.RunOnce(ctx) // HTML rejected -> plain text retry right away
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx) // transient: first real failure, so the backoff starts at 2s
	ck.t = ck.t.Add(2 * time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 3 {
		t.Fatalf("reqs = %d, want 3 (backoff should not count the plain-text fallback)", len(fs.reqs))
	}
}

func TestNonSendErrorRedactsToken(t *testing.T) {
	d, st, fs, ck, _ := setup(t, errors.New(`Post "https://api.telegram.org/botTOKEN/sendMessage": boom`))
	var buf bytes.Buffer
	d.o.Log = slog.New(slog.NewTextHandler(&buf, nil))
	record(t, st, ck.t, 1)
	d.RunOnce(ctx)
	if len(fs.reqs) != 1 || len(pending(t, st, ck.t)) != 1 {
		t.Fatalf("reqs=%d pending=%d, want a scheduled retry", len(fs.reqs), len(pending(t, st, ck.t)))
	}
	if strings.Contains(buf.String(), "TOKEN") || !strings.Contains(buf.String(), "<token>") {
		t.Errorf("log leaks or loses the error:\n%s", buf.String())
	}
}

func TestUnknownBotAlertsForMessages(t *testing.T) {
	d, st, fs, ck, al := setup(t)
	recordAs(t, st, ck.t, "ghost", "x", 1)
	d.RunOnce(ctx)
	if len(fs.reqs) != 0 || len(pending(t, st, ck.t)) != 0 {
		t.Fatalf("reqs=%d pending=%d", len(fs.reqs), len(pending(t, st, ck.t)))
	}
	if len(al.keys) != 1 || al.keys[0] != "send_failed:t" {
		t.Errorf("alerts = %v, want [send_failed:t]", al.keys)
	}
}

func TestLastSentPruned(t *testing.T) {
	d, st, _, ck, _ := setup(t)
	record(t, st, ck.t, 1, 2, 3)
	d.RunOnce(ctx)
	if len(d.lastSent) != 3 {
		t.Fatalf("lastSent = %d, want 3", len(d.lastSent))
	}
	ck.t = ck.t.Add(perChatGap)
	d.RunOnce(ctx)
	if len(d.lastSent) != 0 {
		t.Errorf("lastSent = %v, want stale entries removed", d.lastSent)
	}
}

func logResults(t *testing.T, path string) map[int64]string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id, result FROM forward_log`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var r string
		if err := rows.Scan(&id, &r); err != nil {
			t.Fatal(err)
		}
		out[id] = r
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestForwardLogResult(t *testing.T) {
	d, st, _, ck, _, path := setupDB(t, nil, &telegram.SendError{Kind: telegram.Forbidden})
	sent := record(t, st, ck.t, 1)
	failed := record(t, st, ck.t, 2)
	waiting := record(t, st, ck.t, 2) // queued behind the failed one, then expires
	d.RunOnce(ctx)
	got := logResults(t, path)
	if got[sent] != "sent" || got[failed] != "failed" || got[waiting] != "pending" {
		t.Fatalf("results = %v", got)
	}
	ck.t = ck.t.Add(16 * time.Minute)
	d.RunOnce(ctx)
	if got := logResults(t, path); got[waiting] != "expired" {
		t.Errorf("results = %v, want %d expired", got, waiting)
	}
}

func TestSameChatOrder(t *testing.T) {
	d, st, fs, ck, _ := setup(t, &telegram.SendError{Kind: telegram.Transient})
	recordAs(t, st, ck.t, "main", "first", 1)
	recordAs(t, st, ck.t, "main", "second", 1)
	d.RunOnce(ctx) // "first" fails -> retry in 2s
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx) // gap has passed, but "second" must wait behind "first"
	if len(fs.reqs) != 1 {
		t.Fatalf("sent %d, want 1 (second overtook first)", len(fs.reqs))
	}
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	var texts []string
	for _, r := range fs.reqs {
		texts = append(texts, r.Text)
	}
	if strings.Join(texts, ",") != "first,first,second" {
		t.Errorf("order = %v", texts)
	}
}

// cycleStore signals every DueOutbox call so tests know a dispatch cycle has started.
type cycleStore struct {
	*store.Store
	cycles chan struct{}
}

func (c *cycleStore) DueOutbox(ctx context.Context, now time.Time, limit int) ([]store.OutboxItem, error) {
	items, err := c.Store.DueOutbox(ctx, now, limit)
	select {
	case c.cycles <- struct{}{}:
	default:
	}
	return items, err
}

func TestRunWakeAndStop(t *testing.T) {
	d, st, fs, ck, _ := setup(t)
	d.tick = time.Hour // only Wake can start a new cycle
	cs := &cycleStore{Store: st, cycles: make(chan struct{}, 1)}
	d.o.Store = cs
	fs.sent = make(chan telegram.SendRequest, 10)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { d.Run(runCtx); close(done) }()

	waitFor := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", what)
		}
	}
	waitFor(cs.cycles, "the initial cycle") // the initial cycle ran with an empty outbox

	record(t, st, ck.t, 1)
	d.Wake()
	select {
	case r := <-fs.sent:
		if r.ChatID != 1 {
			t.Errorf("sent to chat %d", r.ChatID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wake did not trigger a send")
	}

	cancel()
	waitFor(done, "Run to return after cancel")
}

func TestWakeDoesNotBlock(t *testing.T) {
	d, _, _, _, _ := setup(t)
	d.Wake()
	d.Wake()
	d.Wake()
}

func TestStopMidBatchRecordsSentItem(t *testing.T) {
	d, st, fs, ck, _, path := setupDB(t)
	first := record(t, st, ck.t, 1)
	second := record(t, st, ck.t, 2)
	runCtx, cancel := context.WithCancel(ctx)
	fs.before = cancel // shutdown arrives while the first message is being sent
	if err := d.RunOnce(runCtx); err != nil {
		t.Fatal(err)
	}
	if len(fs.reqs) != 1 {
		t.Fatalf("sent %d after cancel, want 1", len(fs.reqs))
	}
	got := logResults(t, path)
	if got[first] != "sent" || got[second] != "pending" {
		t.Errorf("results = %v", got)
	}
	if p := pending(t, st, ck.t); len(p) != 1 || p[0].ChatID != 2 {
		t.Errorf("pending = %+v, want only chat 2", p)
	}
}

func TestUnconfirmedIsNotResent(t *testing.T) {
	d, st, fs, ck, al, path := setupDB(t, &telegram.SendError{Kind: telegram.Unconfirmed, Description: "HTTP 200, parsing response"})
	id := record(t, st, ck.t, 1)
	d.RunOnce(ctx)
	ck.t = ck.t.Add(time.Hour)
	d.RunOnce(ctx)
	if len(fs.reqs) != 1 || len(pending(t, st, ck.t)) != 0 || len(al.keys) != 0 {
		t.Fatalf("reqs=%d alerts=%v (an unconfirmed 200 must not be resent)", len(fs.reqs), al.keys)
	}
	if got := logResults(t, path); got[id] != "sent" {
		t.Errorf("result = %q, want sent", got[id])
	}
}
