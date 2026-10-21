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
		{"time excluded", "Login code", "Sent at 9:1234 today", ""},
		{"month-like words", "Verification code", "Your code is 2048. Please decide to separate.", "2048"},
		{"may as month", "Verification code", "Sent 19 May 2026", ""},
		{"too far", "Your verification code", longPad(300) + "123456", ""},
		{"word boundary", "Hotpot recipes", "Serves 4 people, 1234 calories", ""},
		{"email excluded", "Verification code", "The code was sent to 482913@example.com", ""},
		{"yuan excluded", "驗證碼通知", "本次扣款 1500 元", ""},
		{"yuan excluded without space", "驗證碼通知", "本次扣款1500元", ""},
		{"order excluded", "驗證碼", "訂單 12345678 已成立", ""},
		{"order zh substring", "驗證碼", "您的訂單編號12345678", ""},
		{"order en excluded", "Verification code", "Order 482913 shipped", ""},
		{"invoice excluded", "Verification code", "Invoice 482913 attached", ""},
		{"hash excluded", "Verification code", "Ticket #482913", ""},
		{"border is not order", "Verification code", "Border 482913", "482913"},
		{"reorder is not order", "Your code", "Reorder 482913", "482913"},
		{"tie keeps first", "Notice", "OTP 111111 OTP 222222", "111111"},
		{"before keyword counts double", "Notice", "111111 OTP 222222", "222222"},
		{"keyword case insensitive", "NOTICE", "YOUR VERIFICATION CODE: 482913", "482913"},
		{"hyphen split", "Security code", "Your security code is 123-456.", "123456"},
		{"may without digit is not a month", "Verification code", "Your code is 2026. You may now sign in.", "2026"},
		{"spaced dash is not a phone", "Verification code", "Your code: 654321 - 12345 users trust us", "654321"},
		{"double space is not a phone", "Verification code", "Code  654321  1234-5678", "654321"},
		{"paren phone excluded", "OTP", "Call (800) 555-0199 now", ""},
		{"dashed phone excluded", "OTP", "客服 02-2345-6789", ""},
		{"this code", "Use 123456 to set up recovery email", "Use this code to finish setting up:\n123456\n\nThis code will expire in 24 hours.", "123456"},
		{"pin", "Your PIN", "PIN: 4821", "4821"},
		{"pin boundary", "Spinning class", "Room 4821", ""},
		{"dynamic password zh", "通知", "您的動態密碼為 739201", "739201"},
		{"card last4 zh", "交易通知", "您末四碼 1234 的信用卡刷卡成功，請勿將驗證碼告訴他人", ""},
		{"card ending in", "Verification code", "Card ending in 4821 was charged", ""},
		{"card last 4 digits", "Verification code", "Card (last 4 digits 4821) was charged", ""},
		{"zip word", "Verification code", "We never ask for your verification code. Zip: 94043", ""},
		{"unzip is not zip", "Verification code", "Unzip 482913", "482913"},
		{"postcode zh", "验证码", "邮编 100080", ""},
		{"street number", "Verification code", "We never ask for your verification code.\nExample Inc., 1600 Main Street, Springfield", ""},
		{"parkway", "Verification code", "Never share your verification code. 1600 Amphitheatre Parkway", ""},
		{"1st is not st", "Verification code", "Your verification code 482913 (1st sign-in)", "482913"},
		{"cjk house number", "驗證碼", "請勿將驗證碼告訴他人。台北市中山路 1200 號", ""},
		{"us zip", "Verification code", "Never share your verification code. Mountain View, CA 94043", ""},
		{"by the way is fine", "Verification code", "Your verification code: 48213 by the way", "48213"},
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
