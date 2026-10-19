package recognize

import (
	"testing"

	"verify-code-relay/internal/mailparse"
)

func TestCodeFinder(t *testing.T) {
	f := newCodeFinder([]string{"認証コード"})
	cases := []struct {
		name, subject, text, want string
	}{
		{"github", "[GitHub] Please verify your device", "Verification code: 123456\nIf you did not ...", "123456"},
		{"subject code", "839201 is your Facebook confirmation code", "Someone tried to log in.", "839201"},
		{"zh-tw", "登入通知", "您的驗證碼為 482913，請於 5 分鐘內輸入。\n客服專線 0800-123-456", "482913"},
		{"zh-cn", "验证码", "【某服务】您的验证码是735102，10分钟内有效。", "735102"},
		{"ja extra keyword", "ログイン", "認証コード: 551204", "551204"},
		{"alnum", "Your login code", "Use this code: K7Q2ZP to sign in.", "K7Q2ZP"},
		{"split", "Security code", "Your security code is 123 456.", "123456"},
		{"own line beats earlier noise", "Sign-in code", "Your code is below.\nOrder #20261019\n\n839201\n", "839201"},
		{"no keyword", "Weekly digest", "Room 1234 has 5678 items.", ""},
		{"amount excluded", "Payment verification code", "Amount NT$ 2500 charged. Verification code: 9921", "9921"},
		{"year excluded", "Verification code", "Date: Oct 19, 2026\nThat's all.", ""},
		{"url excluded", "Verification code", "Visit https://ex.com/a/123456 now", ""},
		{"phone excluded", "OTP", "Call +1 415 555 0100 for help", ""},
		{"time excluded", "Login code", "Valid until 12:34 only", ""},
		{"too far", "Your verification code", longPad(300) + "123456", ""},
		{"word boundary", "Hotpot recipes", "Serves 4 people, 1234 calories", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Trace{}
			got := f.find(&mailparse.Message{Subject: c.subject, Text: c.text}, tr)
			if got != c.want {
				t.Errorf("got %q want %q\ntrace:\n%v", got, c.want, tr.Steps)
			}
		})
	}
}

func longPad(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = '.'
	}
	return string(b)
}
