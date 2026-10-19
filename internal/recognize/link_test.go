package recognize

import (
	"testing"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

func configOpts(code, link []string) config.RecognizeOptions {
	return config.RecognizeOptions{ExtraCodeKeywords: code, ExtraLinkKeywords: link}
}

func TestLinkFinder(t *testing.T) {
	f := newLinkFinder(nil)
	from := mailparse.Address{Addr: "no-reply@accounts.example.com"}
	cases := []struct {
		name    string
		subject string
		text    string
		links   []mailparse.Link
		want    string
	}{
		{"anchor text", "Confirm your email", "Click below", []mailparse.Link{
			{URL: "https://example.com/help", Text: "Help center"},
			{URL: "https://click.example.com/ls/abc", Text: "Confirm email address"},
			{URL: "https://example.com/unsubscribe?u=1", Text: "Unsubscribe"},
		}, "https://click.example.com/ls/abc"},
		{"magic link url", "Sign in to Example", "Use the link", []mailparse.Link{
			{URL: "https://example.com/"},
			{URL: "https://example.com/auth/magic?token=Zx8_kq2LmN0pQr7sTuVwXy-1"},
		}, "https://example.com/auth/magic?token=Zx8_kq2LmN0pQr7sTuVwXy-1"},
		{"reset password zh", "重設密碼", "請點擊下方連結重設密碼", []mailparse.Link{
			{URL: "https://www.facebook.com/example", Text: "Facebook"},
			{URL: "https://example.com/account/reset?code=123", Text: "重設密碼"},
		}, "https://example.com/account/reset?code=123"},
		{"no keyword", "Newsletter", "Our news", []mailparse.Link{{URL: "https://example.com/verify", Text: "Verify"}}, ""},
		{"below threshold", "Please confirm", "text", []mailparse.Link{{URL: "https://example.com/blog/post", Text: "Read more"}}, ""},
		{"image excluded", "Verify", "x", []mailparse.Link{{URL: "https://example.com/verify.png", Text: "Verify"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Trace{}
			got := f.find(&mailparse.Message{From: from, Subject: c.subject, Text: c.text, Links: c.links}, tr)
			if got != c.want {
				t.Errorf("got %q want %q\ntrace: %v", got, c.want, tr.Steps)
			}
		})
	}
}

func TestBuiltinBoth(t *testing.T) {
	b := NewBuiltin(configOpts(nil, nil))
	msg := &mailparse.Message{
		From:    mailparse.Address{Addr: "noreply@example.com"},
		Subject: "Verify your email",
		Text:    "Your verification code is 482913 or click the link.",
		Links:   []mailparse.Link{{URL: "https://example.com/verify?t=abc", Text: "Verify email"}},
	}
	res, ignore := b.Run(msg, &Trace{})
	if ignore || res == nil || res.Code != "482913" || res.URL != "https://example.com/verify?t=abc" || res.Rule != "builtin:code+link" {
		t.Fatalf("res = %+v", res)
	}
}
