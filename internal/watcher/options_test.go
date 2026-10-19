package watcher

import "testing"

// ENVELOPE subjects (logged for skipped mail) arrive as RFC 2047 encoded words;
// legacy charsets need go-message's charset reader. imapmemserver decodes and
// re-encodes subjects as UTF-8 itself, so this is checked on the options.
func TestClientOptionsDecodeLegacyCharsets(t *testing.T) {
	opts := clientOptions(make(chan struct{}, 1))
	if opts.UnilateralDataHandler == nil || opts.UnilateralDataHandler.Mailbox == nil {
		t.Fatal("mailbox update handler missing")
	}
	if opts.WordDecoder == nil {
		t.Fatal("WordDecoder not set")
	}
	cases := map[string]string{
		"=?big5?B?oWm9ZKjSu8im5qFquvS49LvIpua1bqRKxefD0r1Y?=":                "【範例銀行】網路銀行登入驗證碼",
		"=?gb2312?B?tcfCvNHp1qTC6w==?=":                                      "登录验证码",
		"=?iso-2022-jp?B?GyRCJW0lMCUkJXNHJz5aJTMhPCVJJE4kKkNOJGkkOxsoQg==?=": "ログイン認証コードのお知らせ",
	}
	for in, want := range cases {
		got, err := opts.WordDecoder.DecodeHeader(in)
		if err != nil || got != want {
			t.Errorf("DecodeHeader(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
}
