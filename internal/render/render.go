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
	"verify-code-relay/internal/tglen"
)

const (
	maxCopyRunes = 256
	maxInlineURL = 4000 // UTF-16 units of the escaped URL
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
	t, err := template.New(path).Option("missingkey=error").Funcs(funcs).Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := checkTree(t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if _, err := execute(t, trialData); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// Fields can be empty at run time (and Render empties the subject and URL to fit the
	// limit), so branches taken only on empty values must be checked too.
	if _, err := execute(t, Data{}); err != nil {
		return nil, fmt.Errorf("%s: 欄位為空值時執行失敗：%w", path, err)
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
	builtin, err := compileSet("builtin.", Builtin(), nil)
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

	text, err := fitText(t, d)
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{Text: text, ReplyMarkup: markup}, nil
}

func fits(text string) bool { return tglen.Len(text) <= tglen.Max }

// fitText renders d and, while the text is over Telegram's limit, gives things up in
// this order: an over-long URL (the button keeps it), the tail of the subject, any URL,
// and finally whatever a hard cut removes. The code is never shortened before the cut.
func fitText(t *template.Template, d Data) (string, error) {
	text, err := exec(t, d)
	if err != nil || fits(text) {
		return text, err
	}
	// 1) a URL that cannot fit by itself stays only in the button
	if tglen.Len(template.HTMLEscapeString(d.URL)) > maxInlineURL {
		d.URL = ""
	}
	// 2) keep as much of the subject as fits
	if text, err = fitSubject(t, d); err != nil || fits(text) {
		return text, err
	}
	// 3) drop any URL from the text, then try the whole subject again
	if d.URL != "" {
		d.URL = ""
		if text, err = fitSubject(t, d); err != nil || fits(text) {
			return text, err
		}
	}
	// 4) last resort: hard cut (may break HTML; the dispatcher's plain-text fallback handles that)
	return tglen.Cut(text, tglen.Max), nil
}

// fitSubject renders d with the longest subject prefix (ending in "…") that fits, or with
// no subject at all if not even one rune fits. The result may still be over the limit.
func fitSubject(t *template.Template, d Data) (string, error) {
	text, err := exec(t, d)
	if err != nil || fits(text) || d.Subject == "" {
		return text, err
	}
	subj := []rune(d.Subject)
	// Binary search for the largest n in [1, len(subj)) whose text fits: escaping and
	// characters outside the BMP make one rune of subject cost a varying number of units.
	best, bestText := 0, ""
	for lo, hi := 1, len(subj)-1; lo <= hi; {
		mid := lo + (hi-lo)/2
		e := d
		e.Subject = string(subj[:mid]) + "…"
		text, err := exec(t, e)
		if err != nil {
			return "", err
		}
		if fits(text) {
			best, bestText = mid, text
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if best > 0 {
		return bestText, nil
	}
	d.Subject = ""
	return exec(t, d)
}

func exec(t *template.Template, d Data) (string, error) {
	text, err := execute(t, d)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
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
