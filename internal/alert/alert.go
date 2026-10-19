// Package alert sends system notifications to the optional admin target.
package alert

import (
	"context"
	"html"
	"log/slog"
	"sync"
	"time"

	"verify-code-relay/internal/store"
)

type Enqueuer interface {
	Enqueue(ctx context.Context, it store.OutboxItem) (int64, error)
}

type Target struct {
	Name   string
	Bot    string
	ChatID int64
}

const dedupWindow = time.Hour

type Alerter struct {
	q     Enqueuer
	admin *Target
	ttl   time.Duration
	wake  func()
	log   *slog.Logger
	now   func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

func New(q Enqueuer, admin *Target, ttl time.Duration, wake func(), log *slog.Logger) *Alerter {
	if wake == nil {
		wake = func() {}
	}
	return &Alerter{q: q, admin: admin, ttl: ttl, wake: wake, log: log, now: time.Now, last: map[string]time.Time{}}
}

func (a *Alerter) Alert(key, text string) {
	a.mu.Lock()
	now := a.now()
	if t, ok := a.last[key]; ok && now.Sub(t) < dedupWindow {
		a.mu.Unlock()
		return
	}
	a.last[key] = now
	a.mu.Unlock()
	a.log.Warn("告警", "key", key, "text", text)
	a.enqueue("⚠️ " + html.EscapeString(text))
}

func (a *Alerter) Recovered(key, text string) {
	a.mu.Lock()
	delete(a.last, key)
	a.mu.Unlock()
	a.log.Info("已恢復", "key", key, "text", text)
	a.enqueue("✅ " + html.EscapeString(text))
}

func (a *Alerter) enqueue(text string) {
	if a.admin == nil {
		return
	}
	now := a.now()
	_, err := a.q.Enqueue(context.Background(), store.OutboxItem{
		Target: a.admin.Name, Bot: a.admin.Bot, ChatID: a.admin.ChatID, Text: text,
		CreatedAt: now, ExpiresAt: now.Add(a.ttl), NextAttemptAt: now,
	})
	if err != nil {
		a.log.Error("寫入告警失敗", "err", err)
		return
	}
	a.wake()
}
