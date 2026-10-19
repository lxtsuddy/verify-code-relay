// Package render turns a recognition result into a Telegram HTML message.
package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"
	"unicode/utf8"

	"verify-code-relay/internal/config"
)

const (
	MaxTextRunes = 4096
	maxCopyRunes = 256
	maxInlineURL = 4000
	kindCode     = "code"
	kindLink     = "link"
	kindBoth     = "both"
)

type Data struct {
	Code     string
	URL      string
	Service  string
	FromName string
	FromAddr string
	To       string
	Subject  string
	Mailbox  string
	Rule     string
	Date     time.Time
}

type Rendered struct {
	Text        string
	ReplyMarkup string
}

type tset map[string]*template.Template // kind -> template

type Renderer struct {
	global  tset
	mailbox map[string]tset
}

var trialData = Data{Code: "123456", URL: "https://example.com/verify?token=abc", Service: "Example",
	FromName: "Example", FromAddr: "noreply@example.com", To: "me@example.com", Subject: "Subject",
	Mailbox: "Mailbox", Rule: "builtin:code", Date: time.Unix(0, 0)}

func compile(path, src string) (*template.Template, error) {
	t, err := template.New(path).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := t.Execute(&bytes.Buffer{}, trialData); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

func compileSet(prefix string, tp config.Templates, fallback tset) (tset, error) {
	out := tset{}
	var errs []error
	for kind, src := range map[string]string{kindCode: tp.Code, kindLink: tp.Link, kindBoth: tp.Both} {
		if src == "" {
			if fallback != nil {
				out[kind] = fallback[kind]
			}
			continue
		}
		t, err := compile(prefix+kind, src)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[kind] = t
	}
	return out, errors.Join(errs...)
}

func New(cfg *config.Config) (*Renderer, error) {
	builtin, err := compileSet("builtin.", config.Templates{Code: defaultCode, Link: defaultLink, Both: defaultBoth}, nil)
	if err != nil {
		return nil, err // programming error
	}
	r := &Renderer{mailbox: map[string]tset{}}
	var errs []error
	if r.global, err = compileSet("templates.", cfg.Templates, builtin); err != nil {
		errs = append(errs, err)
	}
	for _, name := range cfg.SortedMailboxNames() {
		set, err := compileSet("mailboxes."+name+".templates.", cfg.Mailboxes[name].Templates, r.global)
		if err != nil {
			errs = append(errs, err)
		}
		r.mailbox[name] = set
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Renderer) Render(mailbox string, d Data) (Rendered, error) {
	kind := kindCode
	switch {
	case d.Code != "" && d.URL != "":
		kind = kindBoth
	case d.Code == "" && d.URL != "":
		kind = kindLink
	case d.Code == "" && d.URL == "":
		return Rendered{}, errors.New("render: 沒有驗證碼也沒有連結")
	}
	set, ok := r.mailbox[mailbox]
	if !ok {
		set = r.global
	}
	t := set[kind]
	markup, err := buttons(d)
	if err != nil {
		return Rendered{}, err
	}

	text, err := exec(t, d)
	if err != nil {
		return Rendered{}, err
	}
	// 1) shrink the subject until it fits
	for utf8.RuneCountInString(text) > MaxTextRunes && d.Subject != "" {
		subj := []rune(strings.TrimSuffix(d.Subject, "…"))
		over := utf8.RuneCountInString(text) - MaxTextRunes
		keep := len(subj) - over - 1
		if keep < 0 {
			keep = 0
		}
		d.Subject = string(subj[:keep]) + "…"
		if keep == 0 {
			d.Subject = ""
		}
		if text, err = exec(t, d); err != nil {
			return Rendered{}, err
		}
	}
	// 2) a gigantic URL stays only in the button
	if utf8.RuneCountInString(text) > MaxTextRunes && utf8.RuneCountInString(d.URL) > maxInlineURL {
		d.URL = ""
		if text, err = exec(t, d); err != nil {
			return Rendered{}, err
		}
	}
	// 3) last resort: hard cut (may break HTML; the dispatcher's plain-text fallback handles that)
	if utf8.RuneCountInString(text) > MaxTextRunes {
		text = string([]rune(text)[:MaxTextRunes])
	}
	return Rendered{Text: text, ReplyMarkup: markup}, nil
}

func exec(t *template.Template, d Data) (string, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

type button struct {
	Text     string    `json:"text"`
	URL      string    `json:"url,omitempty"`
	CopyText *copyText `json:"copy_text,omitempty"`
}

type copyText struct {
	Text string `json:"text"`
}

func buttons(d Data) (string, error) {
	var row []button
	if n := utf8.RuneCountInString(d.Code); n >= 1 && n <= maxCopyRunes {
		row = append(row, button{Text: "📋 複製驗證碼", CopyText: &copyText{Text: d.Code}})
	}
	if d.URL != "" {
		row = append(row, button{Text: "🔗 開啟連結", URL: d.URL})
	}
	if len(row) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep '&' in URLs readable; Telegram accepts either form
	if err := enc.Encode(map[string][][]button{"inline_keyboard": {row}}); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}
