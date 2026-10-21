package mailparse

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

func TestParseMultipartAlternative(t *testing.T) {
	raw := crlf(`From: "GitHub" <noreply@github.com>
To: me+gh@gmail.com
Cc: Other <other@x.com>, me+gh@gmail.com
Delivered-To: me@gmail.com
Subject: =?UTF-8?B?W0dpdEh1Yl0g6amX6K2J56K8?=
Date: Mon, 19 Oct 2026 08:00:00 +0800
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="b1"

--b1
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

Your code is 123456=0Ahttps://github.com/plain-link
--b1
Content-Type: text/html; charset=utf-8

<p>Your code is <b>123456</b></p><a href="https://github.com/verify?x=1">Verify</a>
--b1--
`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.From != (Address{Name: "GitHub", Addr: "noreply@github.com"}) {
		t.Errorf("from = %+v", m.From)
	}
	if m.Subject != "[GitHub] 驗證碼" {
		t.Errorf("subject = %q", m.Subject)
	}
	if len(m.To) != 3 || m.To[0].Addr != "me+gh@gmail.com" || m.To[2].Addr != "me@gmail.com" {
		t.Errorf("to = %+v", m.To)
	}
	if m.Date.IsZero() || m.Date.UTC().Hour() != 0 {
		t.Errorf("date = %v", m.Date)
	}
	if !strings.Contains(m.Text, "Your code is 123456") || strings.Contains(m.Text, "plain-link") {
		t.Errorf("text should come from text/html: %q", m.Text)
	}
	if !strings.Contains(m.AltText, "Your code is 123456\nhttps://github.com/plain-link") {
		t.Errorf("alt text should come from text/plain: %q", m.AltText)
	}
	if !reflect.DeepEqual(m.Emphasis, []Span{{13, 19}}) {
		t.Errorf("emphasis = %v", m.Emphasis)
	}
	urls := map[string]string{}
	for _, l := range m.Links {
		urls[l.URL] = l.Text
	}
	if urls["https://github.com/verify?x=1"] != "Verify" {
		t.Errorf("html link missing: %+v", m.Links)
	}
	if _, ok := urls["https://github.com/plain-link"]; !ok {
		t.Errorf("plain link missing: %+v", m.Links)
	}
}

func TestParseHTMLOnlyBig5(t *testing.T) {
	body, _ := traditionalchinese.Big5.NewEncoder().String(`<div>您的驗證碼是：<b>482913</b></div>`)
	subj, _ := traditionalchinese.Big5.NewEncoder().String("登入驗證碼")
	raw := crlf("From: =?big5?B?" + base64.StdEncoding.EncodeToString([]byte(subj)) + "?= <svc@example.com.tw>\n" +
		"To: me@example.com\nSubject: =?big5?B?" + base64.StdEncoding.EncodeToString([]byte(subj)) + "?=\n" +
		"Content-Type: text/html; charset=big5\nContent-Transfer-Encoding: base64\n\n" +
		base64.StdEncoding.EncodeToString([]byte(body)) + "\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "登入驗證碼" || m.From.Name != "登入驗證碼" {
		t.Errorf("subject/from = %q / %q", m.Subject, m.From.Name)
	}
	if !strings.Contains(m.Text, "您的驗證碼是：\n482913") && !strings.Contains(m.Text, "您的驗證碼是：482913") {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParseGB2312Plain(t *testing.T) {
	gb, _ := simplifiedchinese.GBK.NewEncoder().String("您的验证码为 735102，5分钟内有效。")
	raw := crlf("From: svc@example.cn\nTo: me@example.com\nSubject: code\n" +
		"Content-Type: text/plain; charset=gb2312\nContent-Transfer-Encoding: base64\n\n" +
		base64.StdEncoding.EncodeToString([]byte(gb)) + "\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "您的验证码为 735102") {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParseIgnoresAttachmentsAndBadDate(t *testing.T) {
	raw := crlf(`From: a@b.c
Subject: s
Date: not a date
Content-Type: multipart/mixed; boundary="m"

--m
Content-Type: text/plain

body text
--m
Content-Type: text/plain
Content-Disposition: attachment; filename="a.txt"

attachment 999999
--m--
`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Text, "999999") || m.Text != "body text" {
		t.Errorf("text = %q", m.Text)
	}
	if !m.Date.IsZero() {
		t.Errorf("date should be zero: %v", m.Date)
	}
}

func TestParseUnknownCharsetStillReturnsMessage(t *testing.T) {
	raw := crlf("From: a@b.c\nSubject: s\nContent-Type: text/plain; charset=x-unknown\n\ncode 1234\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "1234") {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParseUnknownTransferEncodingNoPanic(t *testing.T) {
	raw := crlf("From: a@b.c\nSubject: s\nContent-Transfer-Encoding: x-foo\nContent-Type: text/plain\n\ncode 1234\n")
	var m *Message
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("Parse panicked: %v", p)
			}
		}()
		m, err = Parse(raw)
	}()
	if err == nil && m == nil {
		t.Fatal("Parse returned neither a message nor an error")
	}
}

func TestParseMultipleDeliveredTo(t *testing.T) {
	raw := crlf("From: a@b.c\nTo: list@x.org\nDelivered-To: one@example.com\nDelivered-To: two@example.com\n" +
		"Subject: s\n\nbody\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, a := range m.To {
		have[a.Addr] = true
	}
	for _, want := range []string{"list@x.org", "one@example.com", "two@example.com"} {
		if !have[want] {
			t.Errorf("to missing %s: %+v", want, m.To)
		}
	}
}

func TestParseEmptyPlainFallsBackToHTML(t *testing.T) {
	raw := crlf(`From: a@b.c
Subject: s
Content-Type: multipart/alternative; boundary="b"

--b
Content-Type: text/plain; charset=utf-8


--b
Content-Type: text/html; charset=utf-8

<p>Code 1234</p>
--b--
`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "Code 1234") {
		t.Errorf("text should fall back to html: %q", m.Text)
	}
}

func hasWarning(m *Message, sub string) bool {
	for _, w := range m.Warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestParseUnreadablePartFallsBackToOtherPart(t *testing.T) {
	raw := crlf(`From: a@b.c
Subject: s
Content-Type: multipart/alternative; boundary="b"

--b
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: base64

!!!!not base64!!!!
--b
Content-Type: text/html; charset=utf-8

<p>Code 1234</p>
--b--
`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "Code 1234" {
		t.Errorf("text should fall back to html: %q", m.Text)
	}
	if !hasWarning(m, "text/plain") {
		t.Errorf("warnings should report the unreadable text/plain part: %q", m.Warnings)
	}
}

func TestParseUnreadablePartKeepsDecodedPrefix(t *testing.T) {
	body := base64.StdEncoding.EncodeToString([]byte("Your code is 482913.")) + "!!!!"
	raw := crlf("From: a@b.c\nSubject: s\nContent-Type: text/plain\nContent-Transfer-Encoding: base64\n\n" + body + "\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "Your code is 482913") {
		t.Errorf("decoded prefix lost: %q", m.Text)
	}
	if len(m.Warnings) == 0 {
		t.Error("read error should be reported in Warnings")
	}
}

func TestParseNothingReadable(t *testing.T) {
	raw := func(subject string) []byte {
		return crlf("From: a@b.c\nSubject: " + subject +
			"\nContent-Type: text/plain\nContent-Transfer-Encoding: base64\n\n!!!!\n")
	}
	// The subject alone can still carry a code, so the message is kept.
	m, err := Parse(raw("Your code 123456"))
	if err != nil {
		t.Fatalf("subject is usable, want no error: %v", err)
	}
	if m.Subject != "Your code 123456" || len(m.Warnings) == 0 {
		t.Errorf("subject = %q, warnings = %q", m.Subject, m.Warnings)
	}
	// Nothing usable at all: report the read error instead of an empty message.
	if m, err := Parse(raw("")); err == nil {
		t.Errorf("want error when nothing is readable, got %+v", m)
	}
}

func TestParseTruncatesHugePart(t *testing.T) {
	// The 10-byte prefix makes the 5 MiB cut land inside a 3-byte rune.
	var sb strings.Builder
	sb.WriteString("code 1234\n")
	for sb.Len() <= maxPartBytes {
		sb.WriteString("中")
	}
	sb.WriteString("\nTAIL999")
	raw := append(crlf("From: a@b.c\nSubject: s\nContent-Type: text/plain; charset=utf-8\n\n"), sb.String()...)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Text) > maxPartBytes {
		t.Errorf("text is %d bytes, want <= %d", len(m.Text), maxPartBytes)
	}
	if !strings.HasPrefix(m.Text, "code 1234\n中") {
		t.Errorf("text prefix = %q", m.Text[:min(len(m.Text), 20)])
	}
	if strings.Contains(m.Text, "TAIL999") {
		t.Error("content past the limit should be dropped")
	}
	if !utf8.ValidString(m.Text) {
		t.Errorf("truncation split a rune: tail %q", m.Text[max(0, len(m.Text)-4):])
	}
	if !hasWarning(m, "截斷") {
		t.Errorf("truncation should be reported: %q", m.Warnings)
	}
}

func TestParseTextSources(t *testing.T) {
	multi := func(plain, html string) []byte {
		return crlf("From: a@b.c\nSubject: s\nContent-Type: multipart/alternative; boundary=\"b\"\n\n--b\nContent-Type: text/plain; charset=utf-8\n\n" +
			plain + "\n--b\nContent-Type: text/html; charset=utf-8\n\n" + html + "\n--b--\n")
	}
	single := func(ct, body string) []byte {
		return crlf("From: a@b.c\nSubject: s\nContent-Type: " + ct + "; charset=utf-8\n\n" + body + "\n")
	}
	cases := []struct {
		name      string
		raw       []byte
		text, alt string
		emphasis  []Span
	}{
		{"plain only", single("text/plain", "Code 1234"), "Code 1234", "", nil},
		{"html only", single("text/html", "<p>Code <b>1234</b></p>"), "Code 1234", "", []Span{{5, 9}}},
		{"html first", multi("Please use an HTML mail client.", "<p>Code <b>1234</b></p>"), "Code 1234", "Please use an HTML mail client.", []Span{{5, 9}}},
		{"same text kept once", multi("Code 1234", "<p>Code 1234</p>"), "Code 1234", "", nil},
		{"blank html falls back", multi("Code 1234", "<p> </p>"), "Code 1234", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := Parse(c.raw)
			if err != nil {
				t.Fatal(err)
			}
			if m.Text != c.text || m.AltText != c.alt || !reflect.DeepEqual(m.Emphasis, c.emphasis) {
				t.Errorf("text %q alt %q emphasis %v", m.Text, m.AltText, m.Emphasis)
			}
		})
	}
}

func TestParseBareLinkBefore(t *testing.T) {
	raw := crlf("From: a@b.c\nSubject: s\nContent-Type: multipart/alternative; boundary=\"b\"\n\n" +
		"--b\nContent-Type: text/plain; charset=utf-8\n\nClick the link below to verify:\nhttps://x.test/v?t=1\n\nOr open https://x.test/both\n" +
		"--b\nContent-Type: text/html; charset=utf-8\n\n<p>Welcome</p><a href=\"https://x.test/both\" class=\"btn\">Verify</a>\n--b--\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]Link{}
	for _, l := range m.Links {
		byURL[l.URL] = l
	}
	if l := byURL["https://x.test/v?t=1"]; l.Before != "Click the link below to verify:\n" || l.Text != "" {
		t.Errorf("bare link = %+v", l)
	}
	if l := byURL["https://x.test/both"]; l.Text != "Verify" || !l.Button || l.Before != "Welcome\n" {
		t.Errorf("html link should win over the bare one: %+v", l)
	}
}
