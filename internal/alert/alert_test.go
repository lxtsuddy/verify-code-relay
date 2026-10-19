package alert

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/store"
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
