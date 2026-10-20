package web

import (
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/tglen"
)

// Template preview: POST /api/templates/preview {"kind":"code|link|both","template":"…",
// "mailbox":"name" (optional)} renders the template with sample data through the real
// renderer and answers
//
//	{"ok":true,"html":"<sanitised>","text":"<raw output>","buttons":[{"text","url"|"copy"}],
//	 "length":n,"max":4096,"builtin":bool,"unsupported":["div",…]}
//
// or {"ok":false,"error":"…"}. An empty template previews what applies instead: the
// built-in one, or for a mailbox the stored global one. With a mailbox the sample uses its
// display name. The renderer only ever sees a config holding this one template, so neither
// a stored secret nor a broken stored template can reach the answer.

const (
	sampleCode = "834291"
	sampleURL  = "https://github.com/login/device?code=834291"
)

type previewRequest struct {
	Kind     string `json:"kind"`
	Template string `json:"template"`
	Mailbox  string `json:"mailbox"`
}

type previewButton struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
	Copy string `json:"copy,omitempty"`
}

func (s *Server) postAPITemplatePreview(w http.ResponseWriter, r *http.Request) {
	var in previewRequest
	if !s.readJSON(w, r, &in) {
		return
	}
	if in.Kind != "code" && in.Kind != "link" && in.Kind != "both" {
		s.fail(w, r, http.StatusBadRequest, "kind 必須是 code、link 或 both")
		return
	}
	bad := func(msg string) { s.json(w, http.StatusOK, map[string]any{"ok": false, "error": msg}) }
	src := strings.TrimSpace(strings.ReplaceAll(in.Template, "\r\n", "\n"))
	if len(src) > maxTemplateLen {
		bad(msgTemplateTooLong)
		return
	}
	data := render.Data{
		Code: sampleCode, URL: sampleURL, Service: "GitHub", FromName: "GitHub", FromAddr: "noreply@github.com",
		To: "me@example.com", Subject: "[GitHub] Please verify your device", Mailbox: "個人 Gmail", Rule: "builtin",
		Date: s.now(),
	}
	switch in.Kind {
	case "code":
		data.URL = ""
	case "link":
		data.Code = ""
	}
	if in.Mailbox != "" {
		raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		m := raw.Mailboxes[in.Mailbox]
		if m == nil {
			bad("找不到信箱 " + in.Mailbox + "，可能已被刪除")
			return
		}
		data.Mailbox = m.DisplayName
		if data.Mailbox == "" {
			data.Mailbox = in.Mailbox // as config defaults do
		}
		if src == "" {
			src = kindOf(raw.Templates, in.Kind)
		}
	}
	var t config.Templates
	setKind(&t, in.Kind, src)
	rnd, err := render.New(&config.Config{Templates: t})
	if err != nil {
		bad(err.Error())
		return
	}
	out, err := rnd.Render("", data)
	if err != nil {
		bad(err.Error())
		return
	}
	safe, unsupported := sanitize(out.Text)
	s.json(w, http.StatusOK, map[string]any{
		"ok": true, "html": safe, "text": out.Text, "buttons": previewButtons(out.ReplyMarkup),
		"length": tglen.Len(out.Text), "max": tglen.Max, "builtin": src == "", "unsupported": unsupported,
	})
}

func kindOf(t config.Templates, kind string) string {
	switch kind {
	case "code":
		return t.Code
	case "link":
		return t.Link
	}
	return t.Both
}

func setKind(t *config.Templates, kind, src string) {
	switch kind {
	case "code":
		t.Code = src
	case "link":
		t.Link = src
	default:
		t.Both = src
	}
}

// previewButtons reads the inline keyboard the renderer made.
func previewButtons(markup string) []previewButton {
	out := []previewButton{}
	var kb struct {
		InlineKeyboard [][]struct {
			Text     string `json:"text"`
			URL      string `json:"url"`
			CopyText *struct {
				Text string `json:"text"`
			} `json:"copy_text"`
		} `json:"inline_keyboard"`
	}
	if markup == "" || json.Unmarshal([]byte(markup), &kb) != nil {
		return out
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			pb := previewButton{Text: b.Text, URL: b.URL}
			if b.CopyText != nil {
				pb.Copy = b.CopyText.Text
			}
			out = append(out, pb)
		}
	}
	return out
}

// telegramTags are the tags of Telegram's HTML subset that the preview keeps.
var telegramTags = map[string]bool{
	"b": true, "strong": true, "i": true, "em": true, "u": true, "ins": true, "s": true, "strike": true,
	"del": true, "code": true, "pre": true, "a": true, "blockquote": true,
}

// telegramOther are tags Telegram accepts that the preview shows as plain text.
var telegramOther = map[string]bool{"span": true, "tg-spoiler": true, "tg-emoji": true}

// sanitizeTelegramHTML turns a rendered message into HTML that is safe to show in the
// admin: only Telegram's formatting tags survive, without attributes except an http,
// https or tg href on <a>; other tags are removed and their text kept; text is escaped
// again; tags are balanced; line breaks become <br>.
func sanitizeTelegramHTML(s string) string {
	out, _ := sanitize(s)
	return out
}

// sanitize is sanitizeTelegramHTML that also lists, once each in order of appearance,
// the tags it removed (Telegram rejects such a message and it is then sent as plain text).
func sanitize(s string) (string, []string) {
	z := xhtml.NewTokenizer(strings.NewReader(strings.ReplaceAll(s, "\r\n", "\n")))
	var b strings.Builder
	var open []string
	unsupported := []string{}
	seen := map[string]bool{}
	drop := func(name string) {
		if !seen[name] {
			seen[name] = true
			unsupported = append(unsupported, name)
		}
	}
	for {
		tt := z.Next()
		switch tt {
		case xhtml.ErrorToken:
			if z.Err() != io.EOF {
				// the tokenizer only fails on reading, which a strings.Reader never does
				b.WriteString(html.EscapeString(string(z.Raw())))
			}
			for i := len(open) - 1; i >= 0; i-- {
				b.WriteString("</" + open[i] + ">")
			}
			return b.String(), unsupported
		case xhtml.TextToken:
			b.WriteString(strings.ReplaceAll(html.EscapeString(string(z.Text())), "\n", "<br>"))
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tok := z.Token()
			if !telegramTags[tok.Data] {
				if !telegramOther[tok.Data] {
					drop(tok.Data)
				}
				continue
			}
			if tt == xhtml.SelfClosingTagToken {
				continue // <b/> formats nothing
			}
			if tok.Data == "a" {
				if href := safeHref(tok.Attr); href != "" {
					b.WriteString(`<a href="` + html.EscapeString(href) + `">`)
				} else {
					b.WriteString("<a>")
				}
			} else {
				b.WriteString("<" + tok.Data + ">")
			}
			open = append(open, tok.Data)
		case xhtml.EndTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if !telegramTags[tag] {
				if !telegramOther[tag] {
					drop(tag)
				}
				continue
			}
			at := -1
			for i := len(open) - 1; i >= 0; i-- {
				if open[i] == tag {
					at = i
					break
				}
			}
			if at < 0 {
				continue // a stray end tag
			}
			for i := len(open) - 1; i >= at; i-- {
				b.WriteString("</" + open[i] + ">")
			}
			open = open[:at]
		}
		// comments and doctypes are dropped
	}
}

// safeHref returns the href attribute if it is an http, https or tg URL, else "".
func safeHref(attrs []xhtml.Attribute) string {
	for _, a := range attrs {
		if a.Namespace != "" || a.Key != "href" {
			continue
		}
		v := strings.TrimSpace(a.Val)
		u, err := url.Parse(v)
		if err != nil {
			return ""
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "tg":
			return v
		}
		return ""
	}
	return ""
}
