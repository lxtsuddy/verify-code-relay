package alert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"verify-code-relay/internal/store"
	"verify-code-relay/internal/tglen"
)

type fakeQ struct{ items []store.OutboxItem }

func (f *fakeQ) Enqueue(_ context.Context, it store.OutboxItem) (int64, error) {
	f.items = append(f.items, it)
	return int64(len(f.items)), nil
}

func TestAlertDedupAndRecovered(t *testing.T) {
	q := &fakeQ{}
	woke := 0
	a := New(q, &Target{Name: "me", Bot: "main", ChatID: 42}, 24*time.Hour, func() { woke++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }

	a.Alert("disconnected:g/INBOX", "斷線 <b>")
	a.Alert("disconnected:g/INBOX", "斷線")
	if len(q.items) != 1 || woke != 1 {
		t.Fatalf("dedup failed: %d items", len(q.items))
	}
	it := q.items[0]
	if it.LogID != 0 || it.Bot != "main" || it.ChatID != 42 || it.Target != "me" || !it.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Errorf("item = %+v", it)
	}
	if !strings.HasPrefix(it.Text, "⚠️ ") || !strings.Contains(it.Text, "&lt;b&gt;") {
		t.Errorf("text not prefixed/escaped: %q", it.Text)
	}
	a.Recovered("disconnected:g/INBOX", "已恢復")
	if len(q.items) != 2 || !strings.HasPrefix(q.items[1].Text, "✅ ") {
		t.Fatalf("recovered not sent: %+v", q.items)
	}
	a.Alert("disconnected:g/INBOX", "又斷線")
	if len(q.items) != 3 {
		t.Fatal("recovered must reset dedup")
	}
	now = now.Add(61 * time.Minute)
	a.Alert("other", "x")
	a.Alert("disconnected:g/INBOX", "還是斷線")
	if len(q.items) != 5 {
		t.Fatalf("after an hour the same key may alert again: %d", len(q.items))
	}
}

func TestNoAdminLogsOnly(t *testing.T) {
	q := &fakeQ{}
	a := New(q, nil, time.Hour, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Alert("k", "x")
	a.Recovered("k", "y")
	if len(q.items) != 0 {
		t.Fatal("must not enqueue without admin")
	}
}

// flakyQ fails the first `fail` Enqueue calls, then succeeds.
type flakyQ struct {
	fail  int
	items []store.OutboxItem
}

func (f *flakyQ) Enqueue(_ context.Context, it store.OutboxItem) (int64, error) {
	if f.fail > 0 {
		f.fail--
		return 0, errors.New("boom")
	}
	f.items = append(f.items, it)
	return int64(len(f.items)), nil
}

func TestFailedEnqueueDoesNotSuppressKey(t *testing.T) {
	q := &flakyQ{fail: 1}
	woke := 0
	a := New(q, &Target{Name: "me", Bot: "main", ChatID: 42}, time.Hour, func() { woke++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))

	a.Alert("k", "first")
	if len(q.items) != 0 || woke != 0 {
		t.Fatalf("failed enqueue must not produce an item or wake: items=%d woke=%d", len(q.items), woke)
	}
	a.Alert("k", "second")
	if len(q.items) != 1 || woke != 1 {
		t.Fatalf("key must be retried after failed enqueue: items=%d woke=%d", len(q.items), woke)
	}
	if !strings.Contains(q.items[0].Text, "second") {
		t.Errorf("retried item text = %q", q.items[0].Text)
	}
}

func TestAlertTextCapped(t *testing.T) {
	for _, long := range []string{strings.Repeat("<", 5000), strings.Repeat("😀", 3000)} {
		q := &fakeQ{}
		a := New(q, &Target{Name: "me", Bot: "main", ChatID: 42}, time.Hour, nil, discard())
		a.Alert("k", long)
		a.Recovered("k2", long)
		if len(q.items) != 2 {
			t.Fatalf("items = %d", len(q.items))
		}
		for _, it := range q.items {
			// Telegram counts UTF-16 units: 3000 emoji are 3000 runes but 6000 units.
			if n := tglen.Len(it.Text); n > tglen.Max || n < tglen.Max-1 {
				t.Errorf("text is %d UTF-16 units, want %d (or one less)", n, tglen.Max)
			}
			if !strings.HasSuffix(it.Text, "…") {
				t.Errorf("truncated text must end with …: %q", it.Text[len(it.Text)-10:])
			}
		}
		if strings.HasPrefix(long, "<") && !strings.Contains(q.items[0].Text, "&lt;") {
			t.Errorf("text must be escaped")
		}
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// prefixLen is the size of "⚠️ " (U+26A0 U+FE0F and a space); the budget for the escaped
// text is tglen.Max - prefixLen, one unit of which is reserved for the ellipsis.
const prefixLen = 3

// The cut must fall between whole entities and whole surrogate pairs, never inside one.
func TestFitUnalignedBoundary(t *testing.T) {
	room := tglen.Max - prefixLen - 1 // units left for escaped text before "…"
	for _, tc := range []struct {
		name, text, wantSuffix string
	}{
		{"entity straddles the limit", strings.Repeat("a", room-1) + "<<", "a…"},
		{"entity ends at the limit", strings.Repeat("a", room-4) + "<<", "a&lt;…"},
		{"ampersand straddles the limit", strings.Repeat("a", room-2) + "&&", "a…"},
		{"surrogate pair straddles the limit", strings.Repeat("a", room-1) + "😀😀", "a…"},
		{"surrogate pair ends at the limit", strings.Repeat("a", room-2) + "😀😀", "a😀…"},
	} {
		got := fit("⚠️ ", tc.text)
		if !strings.HasPrefix(got, "⚠️ ") || !strings.HasSuffix(got, tc.wantSuffix) {
			t.Errorf("%s: ends %q, want suffix %q", tc.name, got[len(got)-12:], tc.wantSuffix)
		}
		if n := tglen.Len(got); n > tglen.Max {
			t.Errorf("%s: %d units", tc.name, n)
		}
	}
	// Exactly at the limit nothing is cut.
	exact := strings.Repeat("a", tglen.Max-prefixLen-4) + "<"
	if got := fit("⚠️ ", exact); got != "⚠️ "+strings.Repeat("a", tglen.Max-prefixLen-4)+"&lt;" {
		t.Errorf("text that fits exactly must not be cut: ends %q", got[len(got)-12:])
	}
}

func TestDedupWithinTheHour(t *testing.T) {
	q := &fakeQ{}
	a := New(q, &Target{Name: "me"}, time.Hour, nil, discard())
	start := time.Unix(1_800_000_000, 0)
	now := start
	a.now = func() time.Time { return now }

	a.Alert("k", "1")
	for _, d := range []time.Duration{time.Minute, 30 * time.Minute, 59*time.Minute + 59*time.Second} {
		now = start.Add(d)
		a.Alert("k", "again")
		if len(q.items) != 1 {
			t.Fatalf("after %v the key must still be suppressed: %d items", d, len(q.items))
		}
	}
	now = start.Add(time.Hour)
	a.Alert("k", "an hour later")
	if len(q.items) != 2 {
		t.Fatalf("after an hour the key may alert again: %d items", len(q.items))
	}
}

func TestRecoveredTwice(t *testing.T) {
	q := &fakeQ{}
	woke := 0
	a := New(q, &Target{Name: "me"}, time.Hour, func() { woke++ }, discard())

	a.Alert("k", "down")
	a.Recovered("k", "up")
	a.Recovered("k", "up again")
	if len(q.items) != 3 || woke != 3 || !strings.HasPrefix(q.items[1].Text, "✅ ") || !strings.HasPrefix(q.items[2].Text, "✅ ") {
		t.Fatalf("every Recovered is sent: %d items, woke %d", len(q.items), woke)
	}
	a.Alert("k", "down")
	a.Alert("k", "down")
	if len(q.items) != 4 {
		t.Fatalf("dedup must still work after repeated Recovered: %d items", len(q.items))
	}
}

func TestRecoveredEnqueueFailure(t *testing.T) {
	q := &flakyQ{fail: 1}
	woke := 0
	var logs strings.Builder
	a := New(q, &Target{Name: "me"}, time.Hour, func() { woke++ }, slog.New(slog.NewTextHandler(&logs, nil)))

	a.Alert("k", "down") // fails
	a.Recovered("k", "up")
	if len(q.items) != 1 || woke != 1 || !strings.Contains(q.items[0].Text, "up") {
		t.Fatalf("items=%+v woke=%d", q.items, woke)
	}
	if !strings.Contains(logs.String(), "寫入告警失敗") {
		t.Errorf("failure must be logged: %s", logs.String())
	}

	q.fail = 1
	a.Recovered("k", "up again") // fails
	if len(q.items) != 1 || woke != 1 {
		t.Fatalf("failed Recovered must not produce an item or wake: items=%d woke=%d", len(q.items), woke)
	}
	a.Alert("k", "down")
	if len(q.items) != 2 {
		t.Fatalf("a failed Recovered must not suppress the next Alert: %d items", len(q.items))
	}
}

func TestDedupMapIsSwept(t *testing.T) {
	q := &fakeQ{}
	a := New(q, &Target{Name: "me"}, time.Hour, nil, discard())
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }

	for i := range 100 {
		a.Alert(fmt.Sprintf("send_failed:%d", i), "x")
	}
	now = now.Add(30 * time.Minute)
	a.Alert("late", "x")
	now = now.Add(31 * time.Minute)
	a.Alert("new", "x")
	a.mu.Lock()
	n := len(a.last)
	_, late := a.last["late"]
	a.mu.Unlock()
	if n != 2 || !late {
		t.Fatalf("expired keys must be swept, unexpired kept: %d left (late kept: %v)", n, late)
	}
	if len(q.items) != 102 {
		t.Fatalf("items = %d", len(q.items))
	}
}

func TestNonPositiveTTLUsesDefault(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Hour} {
		q := &fakeQ{}
		var logs strings.Builder
		a := New(q, &Target{Name: "me"}, ttl, nil, slog.New(slog.NewTextHandler(&logs, nil)))
		now := time.Unix(1_800_000_000, 0)
		a.now = func() time.Time { return now }
		a.Alert("k", "x")
		if len(q.items) != 1 || !q.items[0].ExpiresAt.Equal(now.Add(24*time.Hour)) {
			t.Fatalf("ttl %v: item = %+v", ttl, q.items)
		}
		if !strings.Contains(logs.String(), "alert_ttl") {
			t.Errorf("ttl %v: fallback must be logged: %s", ttl, logs.String())
		}
	}
}

// lockedQ is safe for concurrent use.
type lockedQ struct {
	mu    sync.Mutex
	items []store.OutboxItem
}

func (f *lockedQ) Enqueue(_ context.Context, it store.OutboxItem) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = append(f.items, it)
	return int64(len(f.items)), nil
}

// Run with -race.
func TestConcurrentAlerts(t *testing.T) {
	q := &lockedQ{}
	var woke atomic.Int32
	a := New(q, &Target{Name: "me"}, time.Hour, func() { woke.Add(1) }, discard())
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			a.Alert("same", "x")
			a.Alert(fmt.Sprintf("k%d", i), "x")
			if i%10 == 0 {
				a.Recovered(fmt.Sprintf("k%d", i), "y")
			}
		})
	}
	wg.Wait()
	if len(q.items) != 1+50+5 || int(woke.Load()) != len(q.items) {
		t.Fatalf("items = %d, woke = %d", len(q.items), woke.Load())
	}
}

// TestNotifyIsNotDedupedAndIsCapped: Notify queues every call with the 🔔 prefix and
// truncates over-long text to Telegram's limit like Alert does.
func TestNotifyIsNotDedupedAndIsCapped(t *testing.T) {
	q := &fakeQ{}
	woke := 0
	a := New(q, &Target{Name: "me", Bot: "main", ChatID: 42}, time.Hour, func() { woke++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Notify("登入成功 <b>")
	a.Notify("登入成功 <b>")
	if len(q.items) != 2 || woke != 2 {
		t.Fatalf("items = %d, woke = %d, want 2 each", len(q.items), woke)
	}
	if q.items[0].Text != "🔔 登入成功 &lt;b&gt;" || q.items[0].Target != "me" || q.items[0].ChatID != 42 {
		t.Errorf("item = %+v", q.items[0])
	}
	a.Notify(strings.Repeat("長", tglen.Max+100))
	it := q.items[2]
	if n := tglen.Len(it.Text); n > tglen.Max || !strings.HasPrefix(it.Text, "🔔 ") || !strings.HasSuffix(it.Text, "…") {
		t.Fatalf("len = %d, text prefix/suffix wrong: %q…", n, it.Text[:20])
	}

	none := New(q, nil, time.Hour, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	none.Notify("x") // no admin target: logged only
	if len(q.items) != 3 {
		t.Fatal("Notify without admin target must not queue")
	}
}
