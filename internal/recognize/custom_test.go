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
