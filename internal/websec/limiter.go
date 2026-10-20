package websec

import (
	"sync"
	"time"
)

// Limiter is a token bucket; it is safe for concurrent use.
type Limiter struct {
	mu        sync.Mutex
	perSecond float64
	burst     float64
	tokens    float64
	last      time.Time
	now       func() time.Time
}

// NewLimiter returns a limiter refilling perSecond tokens per second, holding at most burst.
// It starts full. A nil now defaults to time.Now.
func NewLimiter(perSecond float64, burst int, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{perSecond: perSecond, burst: float64(burst), tokens: float64(burst), last: now(), now: now}
}

// Allow consumes one token if available.
func (l *Limiter) Allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if t := l.now(); t.After(l.last) {
		l.tokens += t.Sub(l.last).Seconds() * l.perSecond
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = t
	}
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}
