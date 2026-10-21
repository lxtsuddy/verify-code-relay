package recognize

import (
	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
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
	link := b.link.find(msg, tr, code != "")
	switch {
	case code != "" && link != "":
		return &Result{Code: code, URL: link, Rule: "builtin:code+link"}, false
	case code != "":
		return &Result{Code: code, Rule: "builtin:code"}, false
	case link != "":
		return &Result{URL: link, Rule: "builtin:link"}, false
	}
	return nil, false
}
