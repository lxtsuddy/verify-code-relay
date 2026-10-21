// Package alert sends system notifications to the optional admin target.
//
// Every alert is also written to the log in full, so callers must never put secrets
// (passwords, tokens, verification codes, links) in the key or text.
package alert

import (
	"context"
	"html"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lxtsuddy/verify-code-relay/internal/store"
	"github.com/lxtsuddy/verify-code-relay/internal/tglen"
)

// Enqueuer stores an outgoing message; *store.Store implements it.
type Enqueuer interface {
	Enqueue(ctx context.Context, it store.OutboxItem) (int64, error)
}

// Target is the Telegram chat that receives alerts (the configured admin target).
type Target struct {
	Name   string // target name in the config
	Bot    string // bot name in the config
	ChatID int64
}

const (
	dedupWindow = time.Hour
	defaultTTL  = 24 * time.Hour // same as the defaults.alert_ttl default in config
)

// Alerter logs alerts and queues them for the admin target. The same key alerts at most
// once per hour until Recovered is called for it. It is safe for concurrent use.
type Alerter struct {
	q     Enqueuer
	admin *Target
	ttl   time.Duration
	wake  func()
	log   *slog.Logger
	now   func() time.Time

	mu    sync.Mutex
	last  map[string]time.Time // key -> time of the last alert queued for it
	swept time.Time            // last time expired entries were removed from last
}

// New returns an Alerter that queues alerts for admin through q, each expiring ttl after
// it is created, and calls wake (if non-nil) after queueing one. A nil admin means alerts
// are only logged. A ttl <= 0 would expire alerts before they are sent, so it falls back
// to 24 hours with a warning.
func New(q Enqueuer, admin *Target, ttl time.Duration, wake func(), log *slog.Logger) *Alerter {
	if wake == nil {
		wake = func() {}
	}
	if ttl <= 0 {
		log.Warn("alert_ttl 必須大於 0，改用預設值", "alert_ttl", ttl, "default", defaultTTL)
		ttl = defaultTTL
	}
	return &Alerter{q: q, admin: admin, ttl: ttl, wake: wake, log: log, now: time.Now, last: map[string]time.Time{}}
}

// Alert logs text at warning level and queues "⚠️ text" for the admin, unless the same key
// was alerted less than an hour ago. If queueing fails the key is not held back, so the
// next Alert for it tries again. The key and text are written to the log as they are:
// they must not contain secrets.
func (a *Alerter) Alert(key, text string) {
	a.mu.Lock()
	now := a.now()
	if t, ok := a.last[key]; ok && now.Sub(t) < dedupWindow {
		a.mu.Unlock()
		return
	}
	a.sweep(now)
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

// sweep removes keys whose dedup window has passed, at most once per window, so keys
// that never recover (such as per-target send failures) do not accumulate. a.mu must be held.
func (a *Alerter) sweep(now time.Time) {
	if now.Sub(a.swept) < dedupWindow {
		return
	}
	for k, t := range a.last {
		if now.Sub(t) >= dedupWindow {
			delete(a.last, k)
		}
	}
	a.swept = now
}

// Recovered logs text and queues "✅ text" for the admin, and clears key so the next
// Alert for it is sent at once. It is not deduplicated; callers send it once per recovery.
// The key and text are written to the log as they are: they must not contain secrets.
func (a *Alerter) Recovered(key, text string) {
	a.mu.Lock()
	delete(a.last, key)
	a.mu.Unlock()
	a.log.Info("已恢復", "key", key, "text", text)
	a.enqueue(fit("✅ ", text))
}

// Notify logs text and queues "🔔 text" for the admin straight away, without
// deduplication. Like Alert, over-long text is truncated to Telegram's limit. The text is
// written to the log as it is: it must not contain secrets.
func (a *Alerter) Notify(text string) {
	a.log.Info("通知", "text", text)
	a.enqueue(fit("🔔 ", text))
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
// tglen.Max UTF-16 units. Truncation is done on the escaped output, whole runes at a time,
// so an entity such as &lt; or a surrogate pair is never split and escaping cannot push
// the result over the limit.
func fit(prefix, text string) string {
	budget := tglen.Max - tglen.Len(prefix)
	esc := html.EscapeString(text)
	if tglen.Len(esc) <= budget {
		return prefix + esc
	}
	var b strings.Builder
	n := 0
	for _, r := range text {
		e := html.EscapeString(string(r))
		en := tglen.Len(e)
		if n+en > budget-1 { // reserve one unit for the ellipsis
			break
		}
		b.WriteString(e)
		n += en
	}
	return prefix + b.String() + "…"
}
