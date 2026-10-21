// Package recognize decides whether a message carries a verification code or
// a verification link, and extracts it.
package recognize

import (
	"fmt"
	"strings"

	"verify-code-relay/internal/mailparse"
)

type Result struct {
	Code string
	URL  string
	// LinkText is the visible text of the chosen link in the mail ("Verify email"), used
	// as the Telegram button label; empty for bare URLs.
	LinkText string
	Service  string
	Rule     string
}

// Trace is a human-readable log of the decision, printed by `relay test`. It may
// contain codes and URLs, so it must never be written to the service log.
type Trace struct{ Steps []string }

func (t *Trace) Addf(format string, args ...any) {
	if t != nil {
		t.Steps = append(t.Steps, fmt.Sprintf(format, args...))
	}
}

type Stage interface {
	Run(msg *mailparse.Message, tr *Trace) (res *Result, ignore bool)
}

type Recognizer interface {
	Recognize(msg *mailparse.Message) (res *Result, ignore bool, tr *Trace)
}

type Chain struct{ stages []Stage }

func NewChain(stages ...Stage) *Chain { return &Chain{stages: stages} }

// Recognize runs the stages on the message; when none finds anything and the message
// has a second body (AltText), it runs them again on a copy reading that body instead.
func (c *Chain) Recognize(msg *mailparse.Message) (*Result, bool, *Trace) {
	tr := &Trace{}
	rounds := []*mailparse.Message{msg}
	if msg.AltText != "" {
		alt := *msg
		alt.Text, alt.AltText, alt.Emphasis = msg.AltText, "", nil
		rounds = append(rounds, &alt)
		tr.Addf("第一輪：HTML 內文")
	}
	for i, m := range rounds {
		if i == 1 {
			tr.Addf("第二輪：純文字內文")
		}
		for _, s := range c.stages {
			res, ignore := s.Run(m, tr)
			if res != nil {
				cp := *res // the stage may reuse its Result; never modify it
				res = &cp
			}
			if ignore {
				return res, true, tr
			}
			if res != nil {
				if res.Service == "" {
					res.Service = ServiceName(msg.From)
				}
				return res, false, tr
			}
		}
	}
	tr.Addf("結果：不符合任何規則")
	return nil, false, tr
}

func ServiceName(from mailparse.Address) string {
	if n := strings.TrimSpace(from.Name); n != "" {
		return n
	}
	at := strings.LastIndexByte(from.Addr, '@')
	if at < 0 {
		return from.Addr
	}
	return MainLabel(from.Addr[at+1:])
}

var secondLevel = map[string]bool{"com": true, "co": true, "net": true, "org": true, "gov": true, "edu": true, "ac": true, "or": true, "ne": true, "go": true}

// MainLabel returns the registrable name of a host: the label before the public
// suffix, approximating two-part suffixes like com.tw / co.uk.
func MainLabel(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	labels := strings.Split(host, ".")
	n := len(labels)
	switch {
	case n >= 3 && len(labels[n-1]) == 2 && secondLevel[labels[n-2]]:
		return labels[n-3]
	case n >= 2:
		return labels[n-2]
	default:
		return host
	}
}
