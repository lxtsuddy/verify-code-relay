package render

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"verify-code-relay/internal/config"
)

func cfgWith(global config.Templates, perMailbox map[string]config.Templates) *config.Config {
	c := &config.Config{Templates: global, Mailboxes: map[string]*config.Mailbox{}}
	for name, t := range perMailbox {
		c.Mailboxes[name] = &config.Mailbox{Name: name, Templates: t}
	}
	return c
}

var sample = Data{Code: "123456", URL: "https://example.com/v?a=1&b=2", Service: "GitHub", FromName: "GitHub",
	FromAddr: "noreply@github.com", To: "me@gmail.com", Subject: "Verify <device> & more", Mailbox: "個人 Gmail",
	Rule: "builtin:code", Date: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)}

func TestDefaultCodeTemplateEscapes(t *testing.T) {
	r, err := New(cfgWith(config.Templates{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	d := sample
	d.URL = ""
	out, err := r.Render("", d)
	if err != nil {
		t.Fatal(err)
	}
	want := "🔐 <b>GitHub</b> 驗證碼\n<code>123456</code>\n📧 個人 Gmail · Verify &lt;device&gt; &amp; more"
	if out.Text != want {
		t.Errorf("text:\n%q\nwant\n%q", out.Text, want)
	}
	var mk struct {
		InlineKeyboard [][]map[string]any `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(out.ReplyMarkup), &mk); err != nil {
		t.Fatal(err)
	}
	btn := mk.InlineKeyboard[0][0]
	if btn["text"] != "📋 複製驗證碼" || btn["copy_text"].(map[string]any)["text"] != "123456" || len(mk.InlineKeyboard[0]) != 1 {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestBothTemplateAndButtons(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	out, err := r.Render("", sample)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "🔗 https://example.com/v?a=1&amp;b=2") || !strings.Contains(out.Text, "<code>123456</code>") {
		t.Errorf("text = %q", out.Text)
	}
	if !strings.Contains(out.ReplyMarkup, `"copy_text"`) || !strings.Contains(out.ReplyMarkup, `"url":"https://example.com/v?a=1&b=2"`) {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestLinkOnly(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	d := sample
	d.Code = ""
	out, _ := r.Render("", d)
	if !strings.HasPrefix(out.Text, "🔗 <b>GitHub</b> 認證連結\nhttps://example.com/v?a=1&amp;b=2") {
		t.Errorf("text = %q", out.Text)
	}
	if strings.Contains(out.ReplyMarkup, "copy_text") || !strings.Contains(out.ReplyMarkup, `"url"`) {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestLookupOrder(t *testing.T) {
	r, err := New(cfgWith(config.Templates{Code: "G {{.Code}}"}, map[string]config.Templates{"work": {Code: "W {{.Code}} {{.Date.Format \"2006\"}}"}}))
	if err != nil {
		t.Fatal(err)
	}
	d := sample
	d.URL = ""
	if out, _ := r.Render("work", d); out.Text != "W 123456 2026" {
		t.Errorf("mailbox override: %q", out.Text)
	}
	if out, _ := r.Render("other", d); out.Text != "G 123456" {
		t.Errorf("global override: %q", out.Text)
	}
	d.Code, d.URL = "", "https://x.y"
	if out, _ := r.Render("work", d); !strings.HasPrefix(out.Text, "🔗") {
		t.Errorf("builtin link fallback: %q", out.Text)
	}
}

func TestNewReportsTemplateErrors(t *testing.T) {
	_, err := New(cfgWith(config.Templates{Link: "{{.Nope}}"}, nil))
	if err == nil || !strings.Contains(err.Error(), "templates.link") {
		t.Fatalf("want templates.link error, got %v", err)
	}
	_, err = New(cfgWith(config.Templates{}, map[string]config.Templates{"m": {Both: "{{"}}))
	if err == nil || !strings.Contains(err.Error(), "mailboxes.m.templates.both") {
		t.Fatalf("want mailbox path, got %v", err)
	}
}

func TestTruncation(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	d := sample
	d.URL = ""
	d.Subject = strings.Repeat("長", 5000)
	out, err := r.Render("", d)
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(out.Text); n > MaxTextRunes {
		t.Fatalf("len = %d", n)
	}
	if !strings.Contains(out.Text, "<code>123456</code>") || !strings.HasSuffix(out.Text, "…") {
		t.Errorf("code must survive and subject end with …: %q", out.Text[len(out.Text)-20:])
	}
}

func TestHugeURLKeepsButtonOnly(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	d := sample
	d.Code = ""
	d.URL = "https://example.com/?" + strings.Repeat("a", 4100)
	out, err := r.Render("", d)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(out.Text) > MaxTextRunes || strings.Contains(out.Text, "aaaa") {
		t.Errorf("text should drop URL: %d runes", utf8.RuneCountInString(out.Text))
	}
	if !strings.Contains(out.ReplyMarkup, `"url"`) {
		t.Errorf("button must keep URL")
	}
}

func TestLongCodeHasNoCopyButton(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	d := sample
	d.URL = ""
	d.Code = strings.Repeat("A", 257)
	out, _ := r.Render("", d)
	if out.ReplyMarkup != "" {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}
