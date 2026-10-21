package recognize

import (
	"strings"
	"testing"

	"github.com/lxtsuddy/verify-code-relay/internal/mailparse"
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
		{"pin is", "Notice", "Your PIN is 4821", "4821"},
		{"one-time pin", "Notice", "Your one-time PIN: 4821", "4821"},
		{"pin boundary", "Spinning class", "Room 4821", ""},
		{"bare pin is not a keyword", "Card services", "If you forgot your PIN, call 0800 123 456.", ""},
		{"bare pin near a branch number", "Card services", "Never share your PIN with anyone. Branch 4821", ""},
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
		{"word cut at the street window", "Notice", "Your verification code is 482913. It expires in 10 minutes. Stay safe!", "482913"},
		{"street word ending the text", "Notice", "Your verification code is 482913, 1600 Long Name Ave", ""},
		{"the code is no keyword", "Notice", "Use the code SAVE20 at checkout", ""},
		{"promo on the line", "Notice", "Use this code SAVE20 at checkout", ""},
		{"promo on the line above", "Notice", "Get 20% off your next order!\n\nUse this code: 482913", ""},
		{"coupon zh", "通知", "優惠券\n您的驗證碼 482913", ""},
		{"this code above the code line", "Notice", "Use this code to finish:\n123456", "123456"},
		{"promo in the subject does not reach the body", "50% off everything", "Your verification code is 482913", "482913"},
		{"by the way is fine", "Verification code", "Your verification code: 48213 by the way", "48213"},
		{"subject echo", "Use 123456 to set up recovery email", "Finish setting up:\n123456\nExpires in 24 hours.", "123456"},
		{"subject echo needs own line", "Use 123456 to sign in", "Your number 123456 was used today.", ""},
		{"subject echo order word", "您的訂單已出貨：20261021", "出貨通知\n貨運單號\n20261021\n", ""},
		{"subject echo invoice in subject", "Your invoice is ready: 482913", "Details\n482913\n", ""},
		{"subject echo shipment in subject", "Use 482913 to track your shipment", "x\n482913\n", ""},
		{"subject echo orders", "Your orders 48213 shipped", "Package\n48213\n", ""},
		{"subject echo year", "Annual report 2026", "Highlights\n2026\n", ""},
		{"subject echo excluded in subject", "Payment $2500 received", "Total\n2500\n", ""},
		{"subject echo excluded in body", "Ref 2500 update", "Total NT$\n2500\n", ""},
		{"subject echo booking zh", "您的訂房 48213765 已確認", "訂房資訊\n訂房編號\n48213765\n入住日期 2026/11/02", ""},
		{"subject echo case", "Case 4821937: we have replied", "Case number\n4821937\nStatus: open", ""},
		{"subject echo reservation", "Reservation 482913 confirmed", "Details\n482913\n", ""},
		{"subject echo payment", "Payment 482913 received", "Details\n482913\n", ""},
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

func TestSubjectEchoTrace(t *testing.T) {
	tr := &Trace{}
	got := newCodeFinder(nil).find(&mailparse.Message{Subject: "Use 123456 now", Text: "x\n123456\n"}, tr)
	if got != "123456" || !strings.Contains(strings.Join(tr.Steps, "\n"), "主旨的 123456 也獨佔內文一行，視為關鍵字") {
		t.Fatalf("got %q trace %q", got, tr.Steps)
	}
}

func TestCodeFinderEmphasisAndAlpha(t *testing.T) {
	cases := []struct {
		name, subject, text string
		emph                []mailparse.Span
		want                string
	}{
		{"emphasis wins", "Notice", "Your verification code: 111111 or 222222", []mailparse.Span{{Start: 34, End: 40}}, "222222"},
		{"no emphasis, nearest wins", "Notice", "Your verification code: 111111 or 222222", nil, "111111"},
		{"alpha own line", "Sign in", "Your code is:\nQWERTY\nIt expires soon.", nil, "QWERTY"},
		{"alpha emphasised inline", "Sign in", "Enter this code: QWERTY to continue", []mailparse.Span{{Start: 17, End: 23}}, "QWERTY"},
		{"alpha inline without emphasis", "Sign in", "Your code is QWERTY and more", nil, ""},
		{"alpha not right after keyword", "Sign in", "Verification code below.\nHELLO\n", nil, ""},
		{"alpha after 為", "登入", "您的驗證碼為\nABCDEF", nil, "ABCDEF"},
		{"alpha inside a heading", "Notice", "YOUR CODE IS READY", []mailparse.Span{{Start: 0, End: 18}}, ""},
		{"alpha inside a heading after the code", "Notice", "USE THE CODE SUMMER", []mailparse.Span{{Start: 0, End: 19}}, ""},
		{"alpha is the whole emphasis", "Sign in", "Your code is QWERTY now", []mailparse.Span{{Start: 13, End: 19}}, "QWERTY"},
		{"digits inside a heading keep the bonus", "Notice", "Your verification code 111111 or 222222", []mailparse.Span{{Start: 23, End: 39}}, "111111"},
		{"emphasis past scan limit is ignored", "Notice", "Your verification code: 111111", []mailparse.Span{{Start: 1 << 20, End: 1<<20 + 6}}, "111111"},
	}
	f := newCodeFinder(nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Trace{}
			if got := f.find(&mailparse.Message{Subject: c.subject, Text: c.text, Emphasis: c.emph}, tr); got != c.want {
				t.Errorf("got %q want %q\ntrace:\n%v", got, c.want, tr.Steps)
			}
		})
	}
}

// HTML as mail sends it: headings and bold text that merely contain a word, and promo codes.
func TestCodeFinderHTML(t *testing.T) {
	cases := []struct{ name, html, want string }{
		{"heading containing a word", "<h1>YOUR CODE IS READY</h1><p>Sign in to see it.</p>", ""},
		{"heading after the code", "<h1>USE THE CODE SUMMER</h1>", ""},
		{"promo in bold", "<p>Use the code <b>SAVE20</b> at checkout</p>", ""},
		{"bold letters after a keyword", "<p>Your code is <b>QWERTY</b></p>", "QWERTY"},
	}
	f := newCodeFinder(nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := "From: a@example.com\r\nSubject: Notice\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + c.html
			msg, err := mailparse.Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			tr := &Trace{}
			if got := f.find(msg, tr); got != c.want {
				t.Errorf("got %q want %q\ntrace:\n%v", got, c.want, tr.Steps)
			}
		})
	}
}
