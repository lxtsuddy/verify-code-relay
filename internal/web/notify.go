package web

import (
	"fmt"
	"time"
)

const (
	failWindow    = time.Hour
	failThreshold = 10 // more failures than this within failWindow trigger a summary
)

// notifyLogin tells the admin about a successful sign-in. Never include secrets.
func (s *Server) notifyLogin(ip, ua string) {
	s.o.Runtime.Notify(fmt.Sprintf("Web 後台登入成功（IP %s，%s）", ip, truncate(ua, 120)))
}

// recordFailure remembers a failed login; when more than failThreshold failures happened
// within the last hour it sends a summary, at most once an hour.
func (s *Server) recordFailure(ip string) {
	now := s.now()
	s.failMu.Lock()
	keep := s.failures[:0]
	for _, t := range s.failures {
		if now.Sub(t) < failWindow {
			keep = append(keep, t)
		}
	}
	s.failures = append(keep, now)
	n := len(s.failures)
	send := n > failThreshold && (s.lastSummary.IsZero() || now.Sub(s.lastSummary) >= failWindow)
	if send {
		s.lastSummary = now
	}
	s.failMu.Unlock()
	if send {
		s.o.Runtime.Notify(fmt.Sprintf("Web 後台過去一小時有 %d 次登入失敗，最近一次來自 %s", n, ip))
	}
}
