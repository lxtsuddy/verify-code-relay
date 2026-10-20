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
		{"keyword case insensitive", "CONFIRM YOUR EMAIL", "x", []mailparse.Link{{URL: "https://example.com/a", Text: "CONFIRM EMAIL"}}, "https://example.com/a"},
		{"hint word in host does not count", "Please verify", "x", []mailparse.Link{
			{URL: "https://verify.example.com/x?t=Zx8_kq2LmN0pQr7sTuVwXy-1", Text: "Open"},
		}, ""},
		{"hint word in fragment counts", "Please verify", "x", []mailparse.Link{
			{URL: "https://example.com/a", Text: "Verify"},
			{URL: "https://example.com/#/verify", Text: "Verify"},
		}, "https://example.com/#/verify"},
		{"token in later value", "Please verify", "x", []mailparse.Link{
			{URL: "https://other.test/a", Text: "Verify"},
			{URL: "https://other.test/b?t=x&t=Zx8_kq2LmN0pQr7sTuVwXy-1", Text: "Verify"},
		}, "https://other.test/b?t=x&t=Zx8_kq2LmN0pQr7sTuVwXy-1"},
		{"mailto and x.com excluded", "Verify", "x", []mailparse.Link{
			{URL: "mailto:verify@example.com", Text: "Verify"},
			{URL: "https://x.com/example/verify", Text: "Verify"},
			{URL: "https://mobile.x.com/verify", Text: "Verify"},
		}, ""},
		{"x.com suffix only", "Verify", "x", []mailparse.Link{{URL: "https://inbox.com/verify", Text: "Verify"}}, "https://inbox.com/verify"},
		{"zh unsubscribe text", "驗證", "x", []mailparse.Link{{URL: "https://example.com/verify?t=1", Text: "取消訂閱"}}, ""},
		{"zh privacy text", "驗證", "x", []mailparse.Link{{URL: "https://example.com/verify?t=1", Text: "隱私權政策"}}, ""},
		{"zh terms text", "驗證", "x", []mailparse.Link{{URL: "https://example.com/verify?t=1", Text: "服務條款"}}, ""},
		{"zh help text", "驗證", "x", []mailparse.Link{{URL: "https://example.com/verify?t=1", Text: "說明中心"}}, ""},
		{"zh unsubscribe url", "验证", "x", []mailparse.Link{{URL: "https://example.com/verify/退订", Text: "验证"}}, ""},
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

func TestLinkFinderExtraKeywords(t *testing.T) {
	msg := &mailparse.Message{
		From:    mailparse.Address{Addr: "no-reply@example.de"},
		Subject: "Jetzt anmelden",
		Text:    "Bitte klicken",
		Links:   []mailparse.Link{{URL: "https://other.test/a", Text: "Anmelden"}},
	}
	if got := newLinkFinder(nil).find(msg, &Trace{}); got != "" {
		t.Fatalf("without extra keyword: got %q", got)
	}
	if got := newLinkFinder([]string{" Anmelden "}).find(msg, &Trace{}); got != "https://other.test/a" {
		t.Fatalf("with extra keyword: got %q", got)
	}
}
