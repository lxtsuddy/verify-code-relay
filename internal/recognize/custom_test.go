package recognize

import (
	"testing"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

func TestCustomRules(t *testing.T) {
	c, err := NewCustom([]config.Rule{
		{Name: "quiet", From: "*@news.example.com", Ignore: true},
		{Name: "gh", From: "*@GitHub.com", Subject: "(?i)verify", CodeRegex: `code:\s*(\d{6})`},
		{Name: "lnk", From: "noreply@example.org", LinkRegex: `https://example\.org/v\?t=\S+`},
		{Name: "nomatch", Subject: "zzz", CodeRegex: `(\d+)`},
		{Name: "wild", From: "user?@example.net", CodeRegex: `\b\d{6}\b`},
		{Name: "alt", From: "alt@example.net", CodeRegex: `PIN (\d{4})|code (\d{6})`},
		{Name: "opt", From: "opt@example.net", CodeRegex: `code(?: is (\d{6}))?`},
		{Name: "partial", From: "partial@example.net", LinkRegex: `example\.net/v\?t=\w+`},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		msg        mailparse.Message
		wantCode   string
		wantURL    string
		wantRule   string
		wantIgnore bool
		wantNil    bool
	}{
		{name: "ignore", msg: mailparse.Message{From: mailparse.Address{Addr: "promo@news.example.com"}}, wantIgnore: true, wantRule: "custom:quiet"},
		{name: "code", msg: mailparse.Message{From: mailparse.Address{Addr: "noreply@github.com"}, Subject: "Please verify", Text: "code: 123456"}, wantCode: "123456", wantRule: "custom:gh"},
		{name: "subject mismatch falls through", msg: mailparse.Message{From: mailparse.Address{Addr: "noreply@github.com"}, Subject: "Hello", Text: "code: 123456"}, wantNil: true},
		{name: "applies but no match falls through", msg: mailparse.Message{From: mailparse.Address{Addr: "noreply@github.com"}, Subject: "verify", Text: "nothing"}, wantNil: true},
		{name: "glob ? matches one char", msg: mailparse.Message{From: mailparse.Address{Addr: "User1@example.net"}, Text: "x 123456 y"}, wantCode: "123456", wantRule: "custom:wild"},
		{name: "glob ? needs a char", msg: mailparse.Message{From: mailparse.Address{Addr: "user@example.net"}, Text: "x 123456 y"}, wantNil: true},
		{name: "glob ? only one char", msg: mailparse.Message{From: mailparse.Address{Addr: "user12@example.net"}, Text: "x 123456 y"}, wantNil: true},
		{name: "no group takes whole match", msg: mailparse.Message{From: mailparse.Address{Addr: "user2@example.net"}, Subject: "Hi", Text: "ref 12 code 654321"}, wantCode: "654321", wantRule: "custom:wild"},
		{name: "second alternative group", msg: mailparse.Message{From: mailparse.Address{Addr: "alt@example.net"}, Text: "your code 654321"}, wantCode: "654321", wantRule: "custom:alt"},
		{name: "first alternative group", msg: mailparse.Message{From: mailparse.Address{Addr: "alt@example.net"}, Text: "your PIN 4321"}, wantCode: "4321", wantRule: "custom:alt"},
		{name: "optional group skips empty match", msg: mailparse.Message{From: mailparse.Address{Addr: "opt@example.net"}, Subject: "Your code", Text: "The code is 654321"}, wantCode: "654321", wantRule: "custom:opt"},
		{name: "optional group never set falls through", msg: mailparse.Message{From: mailparse.Address{Addr: "opt@example.net"}, Subject: "Your code", Text: "no number"}, wantNil: true},
		{name: "link_regex returns whole URL", msg: mailparse.Message{From: mailparse.Address{Addr: "partial@example.net"}, Links: []mailparse.Link{{URL: "https://example.net/v?t=abc-def&x=1"}}}, wantURL: "https://example.net/v?t=abc-def&x=1", wantRule: "custom:partial"},
		{name: "link", msg: mailparse.Message{From: mailparse.Address{Addr: "NoReply@Example.org"}, Links: []mailparse.Link{{URL: "https://x.com"}, {URL: "https://example.org/v?t=abc"}}}, wantURL: "https://example.org/v?t=abc", wantRule: "custom:lnk"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, ignore := c.Run(&tc.msg, &Trace{})
			if tc.wantNil {
				if res != nil || ignore {
					t.Fatalf("want fallthrough, got %+v %v", res, ignore)
				}
				return
			}
			if res == nil || ignore != tc.wantIgnore || res.Code != tc.wantCode || res.URL != tc.wantURL || res.Rule != tc.wantRule {
				t.Fatalf("got %+v ignore=%v", res, ignore)
			}
		})
	}
}

func TestNewChainCustomBeforeBuiltin(t *testing.T) {
	ch, err := New([]config.Rule{{Name: "x", From: "*@example.com", CodeRegex: `ID (\d{4})`}}, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, _, _ := ch.Recognize(&mailparse.Message{From: mailparse.Address{Addr: "a@example.com"}, Subject: "Your verification code", Text: "ID 4321 code 999999"})
	if res == nil || res.Code != "4321" || res.Rule != "custom:x" {
		t.Fatalf("res = %+v", res)
	}
}

func TestNewCustomBadRegex(t *testing.T) {
	if _, err := NewCustom([]config.Rule{{Name: "x", Subject: "(", Ignore: true}}); err == nil {
		t.Fatal("want error")
	}
}

func TestNewRuleFallsThroughToBuiltin(t *testing.T) {
	ch, err := New([]config.Rule{{Name: "x", From: "*@example.com", CodeRegex: `PIN (\d{4})`}}, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, ignore, tr := ch.Recognize(&mailparse.Message{From: mailparse.Address{Addr: "a@example.com"}, Subject: "Your verification code", Text: "Code: 482913"})
	if ignore || res == nil || res.Code != "482913" || res.Rule != "builtin:code" || res.Service != "example" {
		t.Fatalf("res = %+v ignore=%v trace=%v", res, ignore, tr.Steps)
	}
}
