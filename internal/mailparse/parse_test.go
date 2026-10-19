package mailparse

import (
	"encoding/base64"
	"strings"
	"testing"

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
	if !strings.Contains(m.Text, "Your code is 123456\nhttps://github.com/plain-link") {
		t.Errorf("text should come from text/plain: %q", m.Text)
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
