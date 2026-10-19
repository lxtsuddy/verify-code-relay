// Package alert sends system notifications to the optional admin target.
package alert

import (
	"context"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

const (
	dedupWindow = time.Hour
	maxText     = 4096 // Telegram sendMessage limit, in runes
)

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
	if !a.enqueue(fit("⚠️ ", text)) {
		// Release the key so the next Alert retries instead of being suppressed for an hour.
		a.mu.Lock()
		if t, ok := a.last[key]; ok && t.Equal(now) {
			delete(a.last, key)
		}
		a.mu.Unlock()
	}
}

func (a *Alerter) Recovered(key, text string) {
	a.mu.Lock()
	delete(a.last, key)
	a.mu.Unlock()
	a.log.Info("已恢復", "key", key, "text", text)
	a.enqueue(fit("✅ ", text))
}

// enqueue reports whether the alert was stored (or there is no admin target to store it for).
func (a *Alerter) enqueue(text string) bool {
	if a.admin == nil {
		return true
	}
	now := a.now()
	_, err := a.q.Enqueue(context.Background(), store.OutboxItem{
		Target: a.admin.Name, Bot: a.admin.Bot, ChatID: a.admin.ChatID, Text: text,
		CreatedAt: now, ExpiresAt: now.Add(a.ttl), NextAttemptAt: now,
	})
	if err != nil {
		a.log.Error("寫入告警失敗", "err", err)
		return false
	}
	a.wake()
	return true
}

// fit returns prefix plus the HTML-escaped text, truncated so the whole result is at most
// maxText runes. Truncation is done on the escaped output, whole runes at a time, so an
// entity such as &lt; is never split and escaping cannot push the result over the limit.
func fit(prefix, text string) string {
	budget := maxText - utf8.RuneCountInString(prefix)
	esc := html.EscapeString(text)
	if utf8.RuneCountInString(esc) <= budget {
		return prefix + esc
	}
	var b strings.Builder
	n := 0
	for _, r := range text {
		e := html.EscapeString(string(r))
		en := utf8.RuneCountInString(e)
		if n+en > budget-1 { // reserve one rune for the ellipsis
			break
		}
		b.WriteString(e)
		n += en
	}
	return prefix + b.String() + "…"
}
