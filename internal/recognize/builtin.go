package recognize

import (
	"github.com/lxtsuddy/verify-code-relay/internal/config"
	"github.com/lxtsuddy/verify-code-relay/internal/mailparse"
)

type Builtin struct {
	code *codeFinder
	link *linkFinder
}

func NewBuiltin(opts config.RecognizeOptions) *Builtin {
	return &Builtin{code: newCodeFinder(opts.ExtraCodeKeywords), link: newLinkFinder(opts.ExtraLinkKeywords)}
}

func (b *Builtin) Run(msg *mailparse.Message, tr *Trace) (*Result, bool) {
	code := b.code.find(msg, tr)
	link := b.link.pick(msg, tr, code != "")
	switch {
	case code != "" && link.URL != "":
		return &Result{Code: code, URL: link.URL, LinkText: link.Text, Rule: "builtin:code+link"}, false
	case code != "":
		return &Result{Code: code, Rule: "builtin:code"}, false
	case link.URL != "":
		return &Result{URL: link.URL, LinkText: link.Text, Rule: "builtin:link"}, false
	}
	return nil, false
}
