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
		{"not-you remedy excluded", "New sign-in to your account", "If this wasn't you, reset your password now.", []mailparse.Link{
			{URL: "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "reset your password",
				Before: "We noticed a new sign-in to your Example account.\nIf this was you, ignore this email. If this wasn't you,"},
		}, ""},
		{"not-you with a curly apostrophe", "New sign-in", "If this wasn’t you, secure your account.", []mailparse.Link{
			{URL: "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Reset password", Before: "\nIf this wasn’t you,"},
		}, ""},
		{"didn't try to sign in", "Sign-in attempt", "Someone may know your password.", []mailparse.Link{
			{URL: "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Reset your password",
				Before: "\nIf you didn't try to sign in, someone may know your password."},
		}, ""},
		{"若非本人 remedy excluded", "登入通知", "若非本人操作，您的密碼可能已外洩，請立即重設密碼。", []mailparse.Link{
			{URL: "https://www.example.com.tw/account/reset-password?t=Qm9yZWFsaXNEaWdpdGFsQWJj", Text: "重設密碼", Before: "\n若非本人操作，您的密碼可能已外洩，請立即"},
		}, ""},
		{"a requested reset is kept", "Reset your password", "We received a request to reset your password.", []mailparse.Link{
			{URL: "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Reset password",
				Before: "We received a request to reset your password.\n"},
		}, "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy"},
		{"not-you then ignore keeps a sign-in link", "Sign in to Example", "Sign in", []mailparse.Link{
			{URL: "https://example.com/login?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Sign in",
				Before: "\nIf you didn't try to sign in, you can safely ignore this email. Otherwise click the button below:"},
		}, "https://example.com/login?token=Zx8_kq2LmN0pQr7sTuVwXy"},
		{"若非本人請忽略 keeps a sign-in link", "登入連結", "點擊以下連結登入", []mailparse.Link{
			{URL: "https://example.com/login?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "登入", Before: "\n點擊以下連結登入，若非本人操作請忽略："},
		}, "https://example.com/login?token=Zx8_kq2LmN0pQr7sTuVwXy"},
		{"a requested reset that says ignore if not you", "Password reset", "Someone requested a password reset.", []mailparse.Link{
			{URL: "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Reset password",
				Before: "Someone requested a password reset for your account.\nIf this was not you, ignore this email."},
		}, "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy"},
		{"a (not you?) greeting is not a remedy", "Sign in to Medium", "Sign in", []mailparse.Link{
			{URL: "https://example.com/m/callback?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Sign in to Medium", Before: "Hi alice@example.com (not you?)\n"},
		}, "https://example.com/m/callback?token=Zx8_kq2LmN0pQr7sTuVwXy"},
		{"password-changed alert", "Your password was changed", "If you didn't make this change, reset your password.", []mailparse.Link{
			{URL: "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "reset your password",
				Before: "\nIf you didn't make this change, reset your password immediately."},
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Trace{}
			got := f.find(&mailparse.Message{From: from, Subject: c.subject, Text: c.text, Links: c.links}, tr, false)
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
	if got := newLinkFinder(nil).find(msg, &Trace{}, false); got != "" {
		t.Fatalf("without extra keyword: got %q", got)
	}
	if got := newLinkFinder([]string{" Anmelden "}).find(msg, &Trace{}, false); got != "https://other.test/a" {
		t.Fatalf("with extra keyword: got %q", got)
	}
}

func TestLinkFinderWithCode(t *testing.T) {
	f := newLinkFinder(nil)
	from := mailparse.Address{Addr: "no-reply@example.com"}
	cases := []struct {
		name string
		link mailparse.Link
		want bool // forwarded next to a code
	}{
		{"reset password", mailparse.Link{URL: "https://example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Reset your password"}, false},
		{"change password", mailparse.Link{URL: "https://example.com/account/change?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Change password"}, false},
		{"password in the URL only", mailparse.Link{URL: "https://example.com/account/password?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Confirm"}, false},
		{"secure your account", mailparse.Link{URL: "https://example.com/security/verify?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Verify and secure your account"}, false},
		{"review activity", mailparse.Link{URL: "https://example.com/activity/confirm?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Confirm recent activity"}, false},
		{"重設密碼", mailparse.Link{URL: "https://example.com/a/b?t=Zx8_kq2LmN0pQr7sTuVwXy", Text: "重設密碼"}, false},
		{"sign in", mailparse.Link{URL: "https://example.com/login?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Sign in to Example"}, true},
		{"verify email", mailparse.Link{URL: "https://example.com/verify?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Verify email"}, true},
		{"security in the query only", mailparse.Link{URL: "https://example.com/verify?token=Zx8_kq2LmN0pQr7sTuVwXy&next=%2Fsettings%2Fsecurity", Text: "Verify email"}, true},
		{"securely is not secure", mailparse.Link{URL: "https://example.com/login?token=Zx8_kq2LmN0pQr7sTuVwXy", Text: "Securely sign in"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := &mailparse.Message{From: from, Subject: "Your verification code", Text: "Code 482913", Links: []mailparse.Link{c.link}}
			tr := &Trace{}
			if got := f.find(msg, tr, true); (got != "") != c.want {
				t.Errorf("with a code: got %q, want forwarded=%v\ntrace: %v", got, c.want, tr.Steps)
			}
			if c.link.Text == "Reset your password" {
				if got := f.find(msg, &Trace{}, false); got == "" {
					t.Errorf("without a code a reset link must still be found")
				}
			}
		})
	}
}

// TestBuiltinCodeSkipsRemedyLink checks whole messages: a sign-in code with a "reset your
// password if this wasn't you" link forwards the code alone.
func TestBuiltinCodeSkipsRemedyLink(t *testing.T) {
	cases := map[string]string{
		"en": `<p>Your sign-in verification code is</p><p><b>482913</b></p><p>If you didn't try to sign in, someone may know your password. <a href="https://account.example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy">Reset your password</a></p>`,
		"zh": `<p>您的登入驗證碼為 <b>604217</b>，10 分鐘內有效。</p><p>若非本人操作，您的密碼可能已外洩，請立即 <a href="https://www.example.com.tw/account/reset-password?t=Qm9yZWFsaXNEaWdpdGFsQWJj">重設密碼</a>。</p>`,
		"no not-you wording": `<p>Your sign-in code is <b>739201</b>.</p><p><a href="https://account.example.com/password/reset?token=Zx8_kq2LmN0pQr7sTuVwXy">Reset your password</a></p>`,
	}
	b := NewBuiltin(configOpts(nil, nil))
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			text, links := mailparse.HTMLToText(h)
			tr := &Trace{}
			res, _ := b.Run(&mailparse.Message{From: mailparse.Address{Addr: "no-reply@example.com"}, Subject: "Sign-in code", Text: text, Links: links}, tr)
			if res == nil || res.Code == "" || res.URL != "" || res.Rule != "builtin:code" {
				t.Fatalf("res = %+v\ntrace: %v", res, tr.Steps)
			}
		})
	}
}
