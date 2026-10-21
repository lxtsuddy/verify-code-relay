package render

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lxtsuddy/verify-code-relay/internal/config"
	"github.com/lxtsuddy/verify-code-relay/internal/tglen"
)

func cfgWith(global config.Templates, perMailbox map[string]config.Templates) *config.Config {
	c := &config.Config{Templates: global, Mailboxes: map[string]*config.Mailbox{}}
	for name, t := range perMailbox {
		c.Mailboxes[name] = &config.Mailbox{Name: name, Templates: t}
	}
	return c
}

func mustNew(t *testing.T, cfg *config.Config) *Renderer {
	t.Helper()
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mustRender(t *testing.T, r *Renderer, mailbox string, d Data) Rendered {
	t.Helper()
	out, err := r.Render(mailbox, d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func checkFits(t *testing.T, text string) {
	t.Helper()
	if n := tglen.Len(text); n > tglen.Max {
		t.Fatalf("text is %d UTF-16 units, want <= %d", n, tglen.Max)
	}
}

var sample = Data{Code: "123456", URL: "https://example.com/v?a=1&b=2", Service: "GitHub", FromName: "GitHub",
	FromAddr: "noreply@github.com", To: "me@gmail.com", Subject: "Verify <device> & more", Mailbox: "個人 Gmail",
	Rule: "builtin:code", Date: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)}

// footer is the built-in templates' sender / recipient / subject lines for sample.
const footer = "👤 寄件者：GitHub（noreply@github.com）\n📥 收件者：me@gmail.com（個人 Gmail）\n📝 主旨：Verify &lt;device&gt; &amp; more"

// legacy is the old built-in layout, which printed the URL: custom templates still do,
// and Render's length fitting must keep handling them.
var legacy = config.Templates{
	Code: `🔐 <b>{{.Service}}</b> 驗證碼
<code>{{.Code}}</code>
📧 {{.Mailbox}}{{if .Subject}} · {{.Subject}}{{end}}`,
	Link: `🔗 <b>{{.Service}}</b> 認證連結
{{- with .URL}}
{{.}}{{end}}
📧 {{.Mailbox}}{{if .Subject}} · {{.Subject}}{{end}}`,
	Both: `🔐 <b>{{.Service}}</b> 驗證碼
<code>{{.Code}}</code>
{{- with .URL}}
🔗 {{.}}{{end}}
📧 {{.Mailbox}}{{if .Subject}} · {{.Subject}}{{end}}`,
}

func TestDefaultCodeTemplateEscapes(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	d := sample
	d.URL = ""
	out := mustRender(t, r, "", d)
	want := "🔐 <b>GitHub</b> 驗證碼\n<code>123456</code>\n" + footer
	if out.Text != want {
		t.Errorf("text:\n%q\nwant\n%q", out.Text, want)
	}
	var mk struct {
		InlineKeyboard [][]map[string]any `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(out.ReplyMarkup), &mk); err != nil {
		t.Fatal(err)
	}
	btn := mk.InlineKeyboard[0][0]
	if btn["text"] != "📋 複製驗證碼" || btn["copy_text"].(map[string]any)["text"] != "123456" || len(mk.InlineKeyboard[0]) != 1 {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestBothTemplateAndButtons(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	out := mustRender(t, r, "", sample)
	if want := "🔐 <b>GitHub</b> 驗證碼\n<code>123456</code>\n🌐 example.com\n" + footer; out.Text != want {
		t.Errorf("text:\n%q\nwant\n%q", out.Text, want)
	}
	if !strings.Contains(out.ReplyMarkup, `"copy_text"`) || !strings.Contains(out.ReplyMarkup, `"url":"https://example.com/v?a=1&b=2"`) {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestLinkOnly(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	d := sample
	d.Code = ""
	out := mustRender(t, r, "", d)
	if want := "🔗 <b>GitHub</b> 認證連結\n🌐 example.com\n" + footer; out.Text != want {
		t.Errorf("text:\n%q\nwant\n%q", out.Text, want)
	}
	if strings.Contains(out.ReplyMarkup, "copy_text") || !strings.Contains(out.ReplyMarkup, `"url"`) {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestNoCodeNoLinkIsError(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	d := sample
	d.Code, d.URL = "", ""
	if out, err := r.Render("", d); err == nil || !strings.Contains(err.Error(), "沒有驗證碼也沒有連結") || out != (Rendered{}) {
		t.Fatalf("want error, got %+v, %v", out, err)
	}
}

// Lines whose fields are empty disappear without leaving blank lines.
func TestEmptyHeaderFieldsLeaveNoLines(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	for _, tc := range []struct {
		d    Data
		want string
	}{
		{Data{Code: "123456", Service: "S", Mailbox: "M"}, "🔐 <b>S</b> 驗證碼\n<code>123456</code>\n📥 收件信箱：M"},
		{Data{URL: "https://x.y", Service: "S"}, "🔗 <b>S</b> 認證連結\n🌐 x.y"},
		{Data{Code: "123456", URL: "https://x.y", Service: "S", FromAddr: "a@x.y", To: "me@x.y"},
			"🔐 <b>S</b> 驗證碼\n<code>123456</code>\n🌐 x.y\n👤 寄件者：a@x.y\n📥 收件者：me@x.y"},
		{Data{Code: "1", Service: "S", FromName: "N"}, "🔐 <b>S</b> 驗證碼\n<code>1</code>\n👤 寄件者：N"},
	} {
		if out := mustRender(t, r, "", tc.d); out.Text != tc.want {
			t.Errorf("text:\n%q\nwant\n%q", out.Text, tc.want)
		}
	}
}

func TestTemplateEscapesHeaderFields(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{Code: "{{.Service}}|{{.FromName}}|{{.FromAddr}}|{{.To}}|{{.Rule}}"}, nil))
	d := sample
	d.URL = ""
	d.Service, d.FromName, d.FromAddr, d.To, d.Rule = "<S&>", `"N" <x>`, "a&b@x.y", "<me+a&b@x.y>", "r<1>"
	out := mustRender(t, r, "", d)
	want := "&lt;S&amp;&gt;|&#34;N&#34; &lt;x&gt;|a&amp;b@x.y|&lt;me&#43;a&amp;b@x.y&gt;|r&lt;1&gt;"
	if out.Text != want {
		t.Errorf("text:\n%q\nwant\n%q", out.Text, want)
	}
}

func TestLookupOrder(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{Code: "G {{.Code}}"}, map[string]config.Templates{"work": {Code: "W {{.Code}} {{.Date.Format \"2006\"}}"}}))
	d := sample
	d.URL = ""
	if out := mustRender(t, r, "work", d); out.Text != "W 123456 2026" {
		t.Errorf("mailbox override: %q", out.Text)
	}
	if out := mustRender(t, r, "other", d); out.Text != "G 123456" {
		t.Errorf("global override: %q", out.Text)
	}
	d.Code, d.URL = "", "https://x.y"
	if out := mustRender(t, r, "work", d); !strings.HasPrefix(out.Text, "🔗") {
		t.Errorf("builtin link fallback: %q", out.Text)
	}
}

func TestMailboxLinkAndBothOverrides(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{Link: "GL {{.URL}}", Both: "GB {{.Code}} {{.URL}}"},
		map[string]config.Templates{"work": {Link: "WL {{.URL}}", Both: "WB {{.Code}} {{.URL}}"}, "home": {}}))
	for _, tc := range []struct{ mailbox, code, want string }{
		{"work", "", "WL https://x.y"},
		{"work", "42", "WB 42 https://x.y"},
		{"home", "", "GL https://x.y"},
		{"home", "42", "GB 42 https://x.y"},
	} {
		d := sample
		d.Code, d.URL = tc.code, "https://x.y"
		if out := mustRender(t, r, tc.mailbox, d); out.Text != tc.want {
			t.Errorf("%s code=%q: %q, want %q", tc.mailbox, tc.code, out.Text, tc.want)
		}
	}
}

func TestNewReportsTemplateErrors(t *testing.T) {
	_, err := New(cfgWith(config.Templates{Link: "{{.Nope}}"}, nil))
	if err == nil || !strings.Contains(err.Error(), "templates.link") {
		t.Fatalf("want templates.link error, got %v", err)
	}
	_, err = New(cfgWith(config.Templates{}, map[string]config.Templates{"m": {Both: "{{"}}))
	if err == nil || !strings.Contains(err.Error(), "mailboxes.m.templates.both") {
		t.Fatalf("want mailbox path, got %v", err)
	}
}

// A branch that only runs when a field is empty must be checked at startup too.
func TestNewTrialRunsWithEmptyFields(t *testing.T) {
	_, err := New(cfgWith(config.Templates{Link: "{{if not .URL}}{{.Nope}}{{end}}x"}, nil))
	if err == nil || !strings.Contains(err.Error(), "templates.link") || !strings.Contains(err.Error(), "空值") {
		t.Fatalf("want templates.link empty-field error, got %v", err)
	}
	_, err = New(cfgWith(config.Templates{}, map[string]config.Templates{"m": {Code: "{{if not .Subject}}{{.Nope}}{{end}}"}}))
	if err == nil || !strings.Contains(err.Error(), "mailboxes.m.templates.code") {
		t.Fatalf("want mailbox path, got %v", err)
	}
}

// The fitting tests below call fitText directly with subjects longer than Render lets
// through (see TestRenderBoundsHeaderFields): a template may still show the subject many
// times, or a subject of '&' may escape to more than the limit.
func mustFit(t *testing.T, r *Renderer, d Data) Rendered {
	t.Helper()
	text, err := fitText(r.global[kindCode], d)
	if err != nil {
		t.Fatal(err)
	}
	return Rendered{Text: text}
}

func TestTruncation(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	d := sample
	d.URL = ""
	d.Subject = strings.Repeat("長", 5000)
	out := mustFit(t, r, d)
	checkFits(t, out.Text)
	if !strings.Contains(out.Text, "<code>123456</code>") || !strings.HasSuffix(out.Text, "長…") {
		t.Errorf("code must survive and subject end with …: %q", out.Text[len(out.Text)-20:])
	}
	if tglen.Len(out.Text) != tglen.Max {
		t.Errorf("subject should fill the message exactly: %d units", tglen.Len(out.Text))
	}
}

// Emoji outside the BMP count as two units; 3000 of them fit in 4096 runes but not in
// 4096 UTF-16 units.
func TestTruncationCountsUTF16(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	d := sample
	d.URL = ""
	d.Subject = strings.Repeat("😀", 3000)
	out := mustFit(t, r, d)
	checkFits(t, out.Text)
	if !strings.Contains(out.Text, "<code>123456</code>") || !strings.HasSuffix(out.Text, "😀…") {
		t.Errorf("code must survive and subject end with …: %q", out.Text[len(out.Text)-20:])
	}
	if n := tglen.Len(out.Text); n < tglen.Max-2 {
		t.Errorf("subject cut too short: %d units", n)
	}
}

// Each '&' becomes "&amp;" (5 units); the subject must still keep as much as fits, and
// never end in a partial entity.
func TestTruncationAmpersandSubject(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	d := sample
	d.URL = ""
	d.Subject = strings.Repeat("&", 2000)
	out := mustRender(t, r, "", d)
	checkFits(t, out.Text)
	if !strings.HasSuffix(out.Text, "&amp;…") {
		t.Fatalf("subject must end with a whole entity and …: %q", out.Text[len(out.Text)-20:])
	}
	if n := tglen.Len(out.Text); n+5 <= tglen.Max {
		t.Errorf("one more '&' would still fit: %d units", n)
	}
}

func TestHugeURLKeepsButtonOnly(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	d := sample
	d.Code = ""
	d.URL = "https://example.com/?" + strings.Repeat("a", 4100)
	out := mustRender(t, r, "", d)
	checkFits(t, out.Text)
	if strings.Contains(out.Text, "aaaa") {
		t.Errorf("text should drop URL: %q", out.Text)
	}
	if !strings.Contains(out.ReplyMarkup, `"url"`) {
		t.Errorf("button must keep URL")
	}
}

// The over-long URL goes first so the subject can stay whole.
func TestHugeURLDroppedBeforeSubject(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	for _, tc := range []struct{ code, want string }{
		{"", "🔗 <b>GitHub</b> 認證連結\n📧 個人 Gmail · Verify &lt;device&gt; &amp; more"},
		{"123456", "🔐 <b>GitHub</b> 驗證碼\n<code>123456</code>\n📧 個人 Gmail · Verify &lt;device&gt; &amp; more"},
	} {
		d := sample
		d.Code = tc.code
		d.URL = "https://example.com/?" + strings.Repeat("a", 4100)
		if out := mustRender(t, r, "", d); out.Text != tc.want {
			t.Errorf("URL must go and subject stay:\n%q\nwant\n%q", out.Text, tc.want)
		}
	}
}

// A URL whose escaped form is too long counts as over-long even if its raw form is not.
func TestURLMeasuredEscaped(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	d := sample
	d.Code = ""
	d.URL = "https://example.com/?" + strings.Repeat("a&", 1000) // 2021 raw, 6021 escaped
	out := mustRender(t, r, "", d)
	checkFits(t, out.Text)
	if strings.Contains(out.Text, "a&amp;a") || !strings.HasSuffix(out.Text, "Verify &lt;device&gt; &amp; more") {
		t.Errorf("URL must go and subject stay: %q", out.Text)
	}
}

// When the subject cannot fit at all it is dropped together with its separator.
func TestSubjectClearedLeavesNoSeparator(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	head, tail := "🔐 <b>GitHub</b> 驗證碼\n<code>", "</code>\n📧 個人 Gmail"
	d := sample
	d.URL = ""
	d.Code = strings.Repeat("A", tglen.Max-tglen.Len(head+tail))
	out := mustRender(t, r, "", d)
	if want := head + d.Code + tail; out.Text != want {
		t.Errorf("text ends %q, want %q", out.Text[len(out.Text)-30:], want[len(want)-30:])
	}
}

// If the subject is gone and the text is still too long, a URL of any length leaves
// the text (the button keeps it) before resorting to a hard cut.
func TestURLDroppedBeforeHardCut(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	d := sample
	d.Code = strings.Repeat("A", 2000)
	d.URL = "https://example.com/?" + strings.Repeat("b", 3000)
	out := mustRender(t, r, "", d)
	checkFits(t, out.Text)
	if strings.Contains(out.Text, "bbbb") || !strings.Contains(out.Text, "<code>"+d.Code+"</code>") {
		t.Errorf("URL must leave the text and code stay whole: %q", out.Text[len(out.Text)-40:])
	}
	if !strings.HasSuffix(out.Text, "Verify &lt;device&gt; &amp; more") {
		t.Errorf("subject should come back once the URL is gone: %q", out.Text[len(out.Text)-40:])
	}
	if !strings.Contains(out.ReplyMarkup, `"url"`) {
		t.Errorf("button must keep URL")
	}
}

func TestHardCut(t *testing.T) {
	r := mustNew(t, cfgWith(legacy, nil))
	for _, code := range []string{strings.Repeat("A", 5000), strings.Repeat("😀", 3000)} {
		d := sample
		d.URL = ""
		d.Code = code
		out := mustRender(t, r, "", d)
		checkFits(t, out.Text)
		if !strings.HasPrefix(out.Text, "🔐 <b>GitHub</b> 驗證碼\n<code>") || tglen.Len(out.Text) < tglen.Max-1 {
			t.Errorf("hard cut should keep the head and fill the limit: %d units", tglen.Len(out.Text))
		}
	}
}

func TestLongCodeHasNoCopyButton(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	d := sample
	d.URL = ""
	d.Code = strings.Repeat("A", 257)
	out := mustRender(t, r, "", d)
	if out.ReplyMarkup != "" {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

// Header fields come from the mail and can be as large as the message (5 MiB). Render cuts
// them before the template runs: the subject, sender and recipient to 1024 runes, the
// service and mailbox names to 256, so rendering stays fast whatever the template does.
func TestRenderBoundsHeaderFields(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{Code: "{{.Code}}|{{.Subject}}|{{.FromName}}|{{.FromAddr}}|{{.To}}|{{.Service}}|{{.Mailbox}}"}, nil))
	huge := strings.Repeat("長", 5<<20/3) // 5 MiB of UTF-8
	d := sample
	d.URL = ""
	d.Subject, d.FromName, d.FromAddr, d.To, d.Service, d.Mailbox = huge, huge, huge, huge, huge, huge
	start := time.Now()
	out := mustRender(t, r, "", d)
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("Render took %v", el)
	}
	checkFits(t, out.Text)
	parts := strings.Split(out.Text, "|")
	if len(parts) != 7 || parts[0] != "123456" {
		t.Fatalf("text = %.200q", out.Text)
	}
	for i, want := range []int{1024, 1024, 1024, 1024, 256, 256} {
		p := []rune(parts[i+1])
		if i == 0 && len(p) <= want && len(p) > 0 { // the other fields leave the subject less room
			continue
		}
		if len(p) != want || p[len(p)-1] != '…' {
			t.Errorf("field %d: %d runes ending %q, want %d ending …", i+1, len(p), string(p[len(p)-1]), want)
		}
	}
	// short fields and fields of exactly the limit are left alone
	d = sample
	d.URL = ""
	d.Subject = strings.Repeat("😀", 1024)
	out = mustRender(t, r, "", d)
	if parts := strings.Split(out.Text, "|"); parts[1] != d.Subject {
		t.Errorf("a subject of exactly 1024 runes was changed")
	}
}

func TestButtonLabel(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	for _, tc := range []struct{ text, url, want string }{
		{"重設密碼", "https://example.com/r", "🔗 重設密碼"},
		{"  Verify\n  email  ", "https://example.com/r", "🔗 Verify email"},
		{"", "https://Accounts.Example.com:8443/r", "🔗 開啟 accounts.example.com"},
		{strings.Repeat("長", 40), "https://example.com/r", "🔗 " + strings.Repeat("長", 31) + "…"},
		{"", "mailto:x", "🔗 開啟連結"},
	} {
		d := sample
		d.Code, d.URL, d.LinkText = "", tc.url, tc.text
		out := mustRender(t, r, "", d)
		var mk struct {
			InlineKeyboard [][]struct{ Text, URL string } `json:"inline_keyboard"`
		}
		if err := json.Unmarshal([]byte(out.ReplyMarkup), &mk); err != nil {
			t.Fatal(err)
		}
		if got := mk.InlineKeyboard[0][0]; got.Text != tc.want || got.URL != tc.url {
			t.Errorf("button = %+v, want %q", got, tc.want)
		}
	}
}

// A template may print the link text and host itself; Render derives the host from the URL.
func TestLinkTextAndHostFields(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{Link: "{{.LinkText}}|{{.Host}}"}, nil))
	d := sample
	d.Code, d.LinkText, d.URL = "", "Go <now>", "https://WWW.Example.com/x"
	if out := mustRender(t, r, "", d); out.Text != "Go &lt;now&gt;|www.example.com" {
		t.Errorf("text = %q", out.Text)
	}
}

// The host line must not hide a look-alike domain: a non-ASCII host also shows its
// ASCII (punycode) form; a trailing dot is dropped.
func TestHostShowsPunycode(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{Link: "{{.Host}}"}, nil))
	for url, want := range map[string]string{
		"https://аpple.com/x":         "аpple.com（xn--pple-43d.com）",
		"https://evil.com./x":         "evil.com",
		"https://github.com@evil.com": "evil.com",
		"https://xn--/x":              "xn--",
	} {
		d := sample
		d.Code, d.URL = "", url
		if out := mustRender(t, r, "", d); out.Text != want {
			t.Errorf("%s: host %q, want %q", url, out.Text, want)
		}
	}
}

func TestButtonLabelFallbacks(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	for _, tc := range []struct{ text, url, want string }{
		// a URL shown as the link text names nothing (and may differ from the real target)
		{"https://github.com/login/verify?token=abc", "https://evil.example/x", "🔗 開啟 evil.example"},
		{"www.github.com/login", "https://evil.example/x", "🔗 開啟 evil.example"},
		{"github.com/login/verify", "https://evil.example/x", "🔗 開啟 evil.example"},
		{"accounts.google.com", "https://evil.example/x", "🔗 開啟 evil.example"},
		// the button names a look-alike host by its ASCII form
		{"", "https://аpple.com/x", "🔗 開啟 xn--pple-43d.com"},
		// ordinary text with a dot stays
		{"Verify v2.0", "https://x.test/r", "🔗 Verify v2.0"},
		// invisible format characters (zero-width space, right-to-left override) are dropped
		{"Ver​ify‮", "https://x.test/r", "🔗 Verify"},
		// the host fallback is cut like any label
		{"", "https://" + strings.Repeat("a", 60) + ".test/", "🔗 開啟 " + strings.Repeat("a", 28) + "…"},
	} {
		d := sample
		d.Code, d.URL, d.LinkText = "", tc.url, tc.text
		out := mustRender(t, r, "", d)
		var mk struct {
			InlineKeyboard [][]struct{ Text string } `json:"inline_keyboard"`
		}
		if err := json.Unmarshal([]byte(out.ReplyMarkup), &mk); err != nil {
			t.Fatal(err)
		}
		if got := mk.InlineKeyboard[0][0].Text; got != tc.want {
			t.Errorf("label %q, want %q", got, tc.want)
		}
	}
}

// The new built-in layout still fits: a long subject is shortened and the rest kept.
func TestBuiltinFitsLongSubject(t *testing.T) {
	r := mustNew(t, cfgWith(config.Templates{}, nil))
	d := sample
	d.Subject = strings.Repeat("長", 5000)
	out := mustRender(t, r, "", d)
	checkFits(t, out.Text)
	if !strings.Contains(out.Text, "<code>123456</code>") || !strings.Contains(out.Text, "👤 寄件者：GitHub") || !strings.HasSuffix(out.Text, "…") {
		t.Errorf("text tail %q", out.Text[len(out.Text)-40:])
	}
}
