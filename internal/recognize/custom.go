package recognize

import (
	"fmt"
	"regexp"
	"strings"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

type customRule struct {
	name           string
	from, subject  *regexp.Regexp
	codeRe, linkRe *regexp.Regexp
	ignore         bool
}

type Custom struct{ rules []customRule }

// globToRegexp converts a case-insensitive address glob (* and ?) to an anchored regexp.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func NewCustom(rules []config.Rule) (*Custom, error) {
	c := &Custom{}
	for i, r := range rules {
		cr := customRule{name: r.Name, ignore: r.Ignore}
		var err error
		compile := func(field, expr string) *regexp.Regexp {
			if expr == "" || err != nil {
				return nil
			}
			var re *regexp.Regexp
			if re, err = regexp.Compile(expr); err != nil {
				err = fmt.Errorf("rules[%d].%s: %w", i, field, err)
			}
			return re
		}
		if r.From != "" {
			if cr.from, err = globToRegexp(r.From); err != nil {
				return nil, fmt.Errorf("rules[%d].from: %w", i, err)
			}
		}
		cr.subject = compile("subject", r.Subject)
		cr.codeRe = compile("code_regex", r.CodeRegex)
		cr.linkRe = compile("link_regex", r.LinkRegex)
		if err != nil {
			return nil, err
		}
		c.rules = append(c.rules, cr)
	}
	return c, nil
}

func (c *Custom) Run(msg *mailparse.Message, tr *Trace) (*Result, bool) {
	for _, r := range c.rules {
		if r.from != nil && !r.from.MatchString(msg.From.Addr) {
			continue
		}
		if r.subject != nil && !r.subject.MatchString(msg.Subject) {
			continue
		}
		rule := "custom:" + r.name
		if r.ignore {
			tr.Addf("自訂規則 %s：明確略過", r.name)
			return &Result{Rule: rule}, true
		}
		res := &Result{Rule: rule}
		if r.codeRe != nil {
			if m := r.codeRe.FindStringSubmatch(msg.Subject + "\n" + msg.Text); m != nil {
				res.Code = m[0]
				if len(m) > 1 {
					res.Code = m[1]
				}
			}
		}
		if r.linkRe != nil {
			for _, l := range msg.Links {
				if m := r.linkRe.FindString(l.URL); m != "" {
					res.URL = m
					break
				}
			}
		}
		if res.Code != "" || res.URL != "" {
			tr.Addf("自訂規則 %s：命中", r.name)
			return res, false
		}
		tr.Addf("自訂規則 %s：適用但沒有擷取到內容，繼續比對", r.name)
	}
	return nil, false
}

func New(rules []config.Rule, opts config.RecognizeOptions) (*Chain, error) {
	custom, err := NewCustom(rules)
	if err != nil {
		return nil, err
	}
	return NewChain(custom, NewBuiltin(opts)), nil
}
