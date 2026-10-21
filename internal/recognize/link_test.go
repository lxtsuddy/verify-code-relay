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
		{"no keyword", "Newsletter", "Our news", []mailparse.Link{{URL: "https://example.com/verify", Text: "Open"}}, ""},
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
		{"verify button on a tracking link", "Welcome", "Verify", []mailparse.Link{
			{URL: "https://u1.ct.sendgrid.net/ls/click?upn=abc", Text: "Verify", Button: true},
		}, "https://u1.ct.sendgrid.net/ls/click?upn=abc"},
		{"button alone is not enough", "Please verify", "x", []mailparse.Link{
			{URL: "https://example.com/blog/post", Text: "Read more", Button: true},
		}, ""},
		{"click here after a pointing line", "Welcome", "Please verify your email by clicking the link below.", []mailparse.Link{
			{URL: "https://example.com/account/email?k=8d1f", Text: "Click here", Before: "Please verify your email by clicking the link below.\n"},
		}, "https://example.com/account/email?k=8d1f"},
		{"click here on the keyword's line", "Welcome", "To verify your email, click here", []mailparse.Link{
			{URL: "https://example.com/a/b", Text: "click here.", Before: "Hi\nTo verify your email,"},
		}, "https://example.com/a/b"},
		{"click here below an unrelated mention", "Account", "Confirm your settings in your account.", []mailparse.Link{
			{URL: "https://example.com/blog/october", Text: "Click here", Before: "We shipped dark mode. Confirm your settings in your account.\n"},
		}, ""},
		{"bare url after a colon line", "Welcome", "Verify your email:\nhttps://example.com/x/y", []mailparse.Link{
			{URL: "https://example.com/x/y", Before: "Verify your email:\n"},
		}, "https://example.com/x/y"},
		{"gate on link text", "Welcome", "Please use an HTML mail client.", []mailparse.Link{
			{URL: "https://x.test/l/1", Text: "驗證"},
		}, "https://x.test/l/1"},
		{"activation stem", "Welcome", "Activate your account to get started.", []mailparse.Link{
			{URL: "https://example.com/account/activation?key=Qm9yZWFsaXNEaWdpdGFsQWJj", Text: "Continue"},
		}, "https://example.com/account/activation?key=Qm9yZWFsaXNEaWdpdGFsQWJj"},
		{"auth path", "Please sign in", "x", []mailparse.Link{
			{URL: "https://example.com/auth?t=Zx8_kq2LmN0pQr7sTuVwXy-1", Text: "Open"},
		}, "https://example.com/auth?t=Zx8_kq2LmN0pQr7sTuVwXy-1"},
		{"author is not auth", "Account verified", "verify", []mailparse.Link{
			{URL: "https://example.com/author/jane?ref=Zx8_kq2LmN0pQr7sTuVwXy-1", Text: "Read more", Button: true},
		}, ""},
		{"pdf excluded", "Please verify", "x", []mailparse.Link{{URL: "https://example.com/verify/guide.pdf", Text: "Verify"}}, ""},
		{"contact path excluded", "Please verify", "x", []mailparse.Link{{URL: "https://example.com/contact?x=1", Text: "Verify"}}, ""},
		{"contact in query kept", "Please verify", "x", []mailparse.Link{
			{URL: "https://example.com/verify?email=contact%40corp.com", Text: "Verify email"},
		}, "https://example.com/verify?email=contact%40corp.com"},
		{"order confirmation bare url", "Your order", "Order confirmation:\nhttps://shop.example.com/o/48213", []mailparse.Link{
			{URL: "https://shop.example.com/o/48213", Before: "Order confirmation:\n"},
		}, ""},
		{"order confirmation link below", "Thanks for shopping", "View your order confirmation at the link below:\nhttps://example.com/o/48213", []mailparse.Link{
			{URL: "https://example.com/o/48213", Before: "View your order confirmation at the link below:\n"},
		}, ""},
		{"order details zh", "感謝您的訂購", "請確認以下訂單資訊：\nhttps://example.com/o/48213", []mailparse.Link{
			{URL: "https://example.com/o/48213", Before: "請確認以下訂單資訊：\n"},
		}, ""},
		{"payment subject blocks the text before", "Payment received", "Please confirm your email at the link below:", []mailparse.Link{
			{URL: "https://example.com/x/y", Text: "Click here", Before: "Please confirm your email at the link below:\n"},
		}, ""},
		{"in order to is not an order", "Welcome", "In order to verify your email, click the link below:", []mailparse.Link{
			{URL: "https://example.com/x/y", Text: "Click here", Before: "In order to verify your email, click the link below:\n"},
		}, "https://example.com/x/y"},
		{"confirmation is not confirm", "Hello", "Your confirmation is below:", []mailparse.Link{
			{URL: "https://example.com/x/y", Text: "Click here", Before: "Your confirmation is below:\n"},
		}, ""},
		{"button needs keyword text", "Please confirm", "x", []mailparse.Link{
			{URL: "https://example.com/posts/confirm-your-seat", Text: "Read more", Button: true},
		}, ""},
		{"button beats an equal plain link", "Please verify", "x", []mailparse.Link{
			{URL: "https://other.test/a", Text: "Verify"},
			{URL: "https://other.test/b", Text: "Verify", Button: true},
		}, "https://other.test/b"},
		{"excluded link does not open the gate", "Weekly news", "Our latest posts.", []mailparse.Link{
			{URL: "https://example.com/preferences", Text: "Confirm email preferences"},
			{URL: "https://example.com/posts/1?token=Zx8_kq2LmN0pQr7sTuVwXy-1", Text: "Read more"},
		}, ""},
		{"view in browser excluded", "Please verify", "x", []mailparse.Link{
			{URL: "https://example.com/v/verify-123", Text: "View in browser"},
			{URL: "https://example.com/verify?t=1", Text: "Verify"},
		}, "https://example.com/verify?t=1"},
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
