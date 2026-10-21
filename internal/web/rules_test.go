package web

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/lxtsuddy/verify-code-relay/internal/config"
	"github.com/lxtsuddy/verify-code-relay/internal/render"
	"github.com/lxtsuddy/verify-code-relay/internal/runtime"
)

func TestRulesRoundTrip(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	form := url.Values{
		"rule_name": {"gh", "promo"}, "rule_from": {"*@github.com", "*@news.example.com"}, "rule_subject": {"", ""},
		"rule_code_regex": {`code:\s*(\d{6})`, ""}, "rule_link_regex": {"", ""}, "rule_ignore": {"0", "1"},
		"extra_code_keywords": {"驗證號碼\n\n認證碼"}, "extra_link_keywords": {""},
	}
	if resp, body := e.postForm("/rules", form); resp.StatusCode != 303 {
		t.Fatalf("= %d %s", resp.StatusCode, body)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if len(raw.Rules) != 2 || raw.Rules[1].Name != "promo" || !raw.Rules[1].Ignore || raw.Rules[0].CodeRegex != `code:\s*(\d{6})` {
		t.Fatalf("rules = %+v", raw.Rules)
	}
	if got := raw.Recognize.ExtraCodeKeywords; len(got) != 2 || got[1] != "認證碼" {
		t.Fatalf("keywords = %q", got)
	}
	form.Set("rule_code_regex", "(")
	form["rule_code_regex"] = []string{"(", ""}
	resp, body := e.postForm("/rules", form)
	if resp.StatusCode != 422 || !strings.Contains(body, "正規表示式錯誤") {
		t.Fatalf("bad regex = %d", resp.StatusCode)
	}
}

func TestTemplatePreview(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	_, body := e.postJSON("/api/templates/preview", map[string]string{"kind": "code", "template": "<b>{{.Service}}</b> <code>{{.Code}}</code><script>x</script>"})
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, "834291") || strings.Contains(body, "<script>") {
		t.Fatalf("preview = %s", body)
	}
	// encoding/json escapes "<", so the check above cannot see tags: look at the decoded HTML
	var a previewAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil || a.HTML != "<b>GitHub</b> <code>834291</code>x" || strings.Contains(a.HTML, "script") {
		t.Fatalf("html = %q (%v)", a.HTML, err)
	}
	_, body = e.postJSON("/api/templates/preview", map[string]string{"kind": "code", "template": "{{.Nope}}"})
	if !strings.Contains(body, `"ok":false`) {
		t.Fatalf("bad template = %s", body)
	}
}

func TestSanitizeTelegramHTML(t *testing.T) {
	got := sanitizeTelegramHTML(`<b>a</b><img src=x onerror=alert(1)><a href="javascript:x">l</a><a href="https://ok">k</a>` + "\nz")
	if strings.Contains(got, "img") || strings.Contains(got, "javascript") || !strings.Contains(got, `<a href="https://ok">k</a>`) || !strings.Contains(got, "<br>") {
		t.Fatalf("= %s", got)
	}
}

func TestSanitizeTelegramHTMLDetails(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// attributes other than a's href are dropped
		{`<a href="https://x.test/?a=1&amp;b=2" onclick="evil()">k</a>`, `<a href="https://x.test/?a=1&amp;b=2">k</a>`},
		{`<b class="x" style="color:red">t</b>`, `<b>t</b>`},
		{`<a href=" HTTPS://X.test ">k</a>`, `<a href="HTTPS://X.test">k</a>`},
		{`<a href="tg://user?id=1">u</a>`, `<a href="tg://user?id=1">u</a>`},
		{`<a href="data:text/html,x">u</a><a>v</a>`, `<a>u</a><a>v</a>`},
		// unknown tags go, their text stays (escaped); script content is plain text
		{`<div>x</div><span class="tg-spoiler">y</span>`, `xy`},
		{`<script>alert("1")</script>`, `alert(&#34;1&#34;)`},
		{`1 &lt; 2 &amp; <i>3</i>`, `1 &lt; 2 &amp; <i>3</i>`},
		// balanced: missing ends are closed, stray ends dropped, crossed ends close inner tags
		{`<b>open`, `<b>open</b>`},
		{`x</b></i>y`, `xy`},
		{`<b><i>a</b>c</i>`, `<b><i>a</i></b>c`},
		{`<pre><code class="language-go">x</code></pre>`, `<pre><code>x</code></pre>`},
		{"a\r\nb\nc", "a<br>b<br>c"},
		{`<!-- c --><br/>t`, `t`},
	} {
		if got := sanitizeTelegramHTML(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRulesForm(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	resp, body := e.get("/rules")
	if resp.StatusCode != 200 || !strings.Contains(body, "內建辨識") {
		t.Fatalf("GET /rules = %d", resp.StatusCode)
	}
	before := versionCount(t, e)

	// row-level and field errors land on their rows; blank rows are dropped first
	form := url.Values{
		"rule_name": {"", "ok", "", "bad"}, "rule_from": {"", "*@a.test", "", ""}, "rule_subject": {"", "", "", ""},
		"rule_code_regex": {"", `(\d+)`, "", "("}, "rule_link_regex": {"", "", "", ""}, "rule_ignore": {"0", "0", "0", "0"},
	}
	resp, body = e.postForm("/rules", form)
	if resp.StatusCode != 422 {
		t.Fatalf("= %d", resp.StatusCode)
	}
	if !invalidField(body, "rule-1-code_regex") || invalidField(body, "rule-0-code_regex") {
		t.Errorf("regex error not on the second kept row:\n%s", body)
	}
	if !strings.Contains(body, "至少需要寄件者或主旨") {
		t.Errorf("row error missing")
	}
	if n := strings.Count(body, "正規表示式錯誤"); n != 1 {
		t.Errorf("regex error shown %d times", n)
	}
	if strings.Count(body, `name="rule_name"`) != 2+1 { // two rows plus the new-row template
		t.Errorf("blank rows not dropped: %d", strings.Count(body, `name="rule_name"`))
	}
	if versionCount(t, e) != before {
		t.Fatal("invalid rules stored")
	}

	// missing name
	resp, body = e.postForm("/rules", url.Values{"rule_name": {""}, "rule_from": {"*@a.test"}, "rule_ignore": {"1"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "請輸入規則名稱") || !invalidField(body, "rule-0-name") {
		t.Fatalf("missing name = %d", resp.StatusCode)
	}

	// save, then the same again is no change
	ok := url.Values{
		"rule_name": {"a"}, "rule_from": {"*@a.test"}, "rule_subject": {""}, "rule_code_regex": {""},
		"rule_link_regex": {`^https://a\.test/`}, "rule_ignore": {"0"},
		"extra_code_keywords": {" 認證碼 \r\n認證碼\r\n"}, "extra_link_keywords": {"啟用帳號"},
	}
	if resp, _ := e.postForm("/rules", ok); resp.StatusCode != 303 || e.flash(resp) != "已儲存辨識規則並套用" {
		t.Fatalf("save = %d %q", resp.StatusCode, e.flash(resp))
	}
	v, _, _ := e.st.LatestConfigVersion(context.Background())
	if v.Summary != "修改辨識規則" {
		t.Errorf("summary = %q", v.Summary)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if got := raw.Recognize.ExtraCodeKeywords; len(got) != 1 || got[0] != "認證碼" {
		t.Errorf("code keywords = %q", got)
	}
	if got := raw.Recognize.ExtraLinkKeywords; len(got) != 1 || got[0] != "啟用帳號" {
		t.Errorf("link keywords = %q", got)
	}
	n := versionCount(t, e)
	if resp, _ := e.postForm("/rules", ok); resp.StatusCode != 303 || e.flash(resp) != "沒有變更" || versionCount(t, e) != n {
		t.Fatalf("no-op save = %d %q", resp.StatusCode, e.flash(resp))
	}
	_, body = e.get("/rules")
	if !strings.Contains(body, `^https://a\.test/`) || !strings.Contains(body, "啟用帳號") {
		t.Error("stored rule not shown")
	}

	// clearing everything stores no rules
	if resp, _ := e.postForm("/rules", url.Values{"extra_code_keywords": {""}}); resp.StatusCode != 303 {
		t.Fatalf("clear = %d", resp.StatusCode)
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if len(raw.Rules) != 0 || len(raw.Recognize.ExtraCodeKeywords) != 0 || len(raw.Recognize.ExtraLinkKeywords) != 0 {
		t.Fatalf("not cleared: %+v %+v", raw.Rules, raw.Recognize)
	}
}

// invalidField reports whether the element with id is marked aria-invalid.
func invalidField(body, id string) bool {
	return regexp.MustCompile(`id="` + regexp.QuoteMeta(id) + `"[^>]*aria-invalid="true"`).MatchString(body)
}

func TestImportedSpacingIsNoChange(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Rules = []config.Rule{{Name: "a", From: "*@a.test ", CodeRegex: "(\\d+)\n"}}
		c.Recognize.ExtraCodeKeywords = []string{" 認證碼"}
		c.Templates.Code = "{{.Code}}\n"
	})
	e.login()
	n := versionCount(t, e)
	resp, _ := e.postForm("/rules", url.Values{
		"rule_name": {"a"}, "rule_from": {"*@a.test"}, "rule_subject": {""}, "rule_code_regex": {`(\d+)`},
		"rule_link_regex": {""}, "rule_ignore": {"0"}, "extra_code_keywords": {"認證碼"}, "extra_link_keywords": {""},
	})
	if resp.StatusCode != 303 || e.flash(resp) != "沒有變更" {
		t.Errorf("rules = %d %q", resp.StatusCode, e.flash(resp))
	}
	resp, _ = e.postForm("/templates", url.Values{"tpl_code": {"{{.Code}}"}, "tpl_link": {""}, "tpl_both": {""}})
	if resp.StatusCode != 303 || e.flash(resp) != "沒有變更" {
		t.Errorf("templates = %d %q", resp.StatusCode, e.flash(resp))
	}
	if versionCount(t, e) != n {
		t.Error("a version was stored")
	}
}

func TestTemplatesForm(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	resp, body := e.get("/templates")
	if resp.StatusCode != 200 || !strings.Contains(body, "認證連結") { // the built-in link template is offered
		t.Fatalf("GET /templates = %d", resp.StatusCode)
	}
	before := versionCount(t, e)
	resp, body = e.postForm("/templates", url.Values{"tpl_code": {"{{.Nope}}"}, "tpl_link": {""}, "tpl_both": {""}})
	if resp.StatusCode != 422 || !invalidField(body, "tpl-code") || invalidField(body, "tpl-link") {
		t.Fatalf("bad template = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "{{.Nope}}") {
		t.Error("submitted template not sent back")
	}
	resp, body = e.postForm("/templates", url.Values{"tpl_link": {strings.Repeat("x", maxTemplateLen+1)}})
	if resp.StatusCode != 422 || !strings.Contains(body, "範本太長") || !invalidField(body, "tpl-link") {
		t.Fatalf("long template = %d", resp.StatusCode)
	}
	if versionCount(t, e) != before {
		t.Fatal("invalid templates stored")
	}

	tpl := "🔐 {{.Service}}\r\n<code>{{.Code}}</code>\r\n"
	if resp, _ := e.postForm("/templates", url.Values{"tpl_code": {tpl}, "tpl_link": {""}, "tpl_both": {""}}); resp.StatusCode != 303 || e.flash(resp) != "已儲存訊息範本並套用" {
		t.Fatalf("save = %d %q", resp.StatusCode, e.flash(resp))
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if raw.Templates.Code != "🔐 {{.Service}}\n<code>{{.Code}}</code>" || raw.Templates.Link != "" {
		t.Fatalf("templates = %+v", raw.Templates)
	}
	v, _, _ := e.st.LatestConfigVersion(context.Background())
	if v.Summary != "修改訊息範本" {
		t.Errorf("summary = %q", v.Summary)
	}
	n := versionCount(t, e)
	if resp, _ := e.postForm("/templates", url.Values{"tpl_code": {tpl}}); resp.StatusCode != 303 || e.flash(resp) != "沒有變更" || versionCount(t, e) != n {
		t.Fatalf("no-op = %d %q", resp.StatusCode, e.flash(resp))
	}
	_, body = e.get("/templates")
	if !strings.Contains(body, "&lt;code&gt;{{.Code}}&lt;/code&gt;") {
		t.Error("stored template not in the editor")
	}
}

type previewAnswer struct {
	OK          bool     `json:"ok"`
	Error       string   `json:"error"`
	HTML        string   `json:"html"`
	Text        string   `json:"text"`
	Builtin     bool     `json:"builtin"`
	Length      int      `json:"length"`
	Max         int      `json:"max"`
	Unsupported []string `json:"unsupported"`
	Buttons     []struct {
		Text string `json:"text"`
		URL  string `json:"url"`
		Copy string `json:"copy"`
	} `json:"buttons"`
}

func preview(t *testing.T, e *env, req map[string]string) (int, previewAnswer) {
	t.Helper()
	resp, body := e.postJSON("/api/templates/preview", req)
	var a previewAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return resp.StatusCode, a
}

func TestTemplatePreviewKinds(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		withSecrets(c)
		c.Mailboxes["gmail"].DisplayName = "工作信箱"
		c.Templates.Link = "全域 {{.URL}}"
	})
	e.login()

	_, a := preview(t, e, map[string]string{"kind": "link", "template": ""})
	if !a.OK || !a.Builtin || !strings.Contains(a.Text, "GitHub</b> 認證連結") {
		t.Errorf("empty link = %+v", a)
	}
	if len(a.Buttons) != 1 || a.Buttons[0].URL != "https://github.com/login/device?code=834291" {
		t.Errorf("link buttons = %+v", a.Buttons)
	}

	_, a = preview(t, e, map[string]string{"kind": "both", "template": "{{.Code}} {{.URL}} {{.Mailbox}}"})
	if !a.OK || len(a.Buttons) != 2 || a.Buttons[0].Copy != "834291" || !strings.Contains(a.Text, "個人 Gmail") {
		t.Errorf("both = %+v", a)
	}
	if a.Length != len([]rune(a.Text)) || a.Max != 4096 {
		t.Errorf("length = %d/%d", a.Length, a.Max)
	}

	_, a = preview(t, e, map[string]string{"kind": "code", "template": "{{.Mailbox}} {{.Code}}", "mailbox": "gmail"})
	if !a.OK || a.Text != "工作信箱 834291" {
		t.Errorf("mailbox = %+v", a)
	}
	// for a mailbox, an empty template is the stored global one
	_, a = preview(t, e, map[string]string{"kind": "link", "template": "", "mailbox": "gmail"})
	if !a.OK || a.Text != "全域 https://github.com/login/device?code=834291" {
		t.Errorf("mailbox fallback = %+v", a)
	}
	_, a = preview(t, e, map[string]string{"kind": "code", "template": "x", "mailbox": "nope"})
	if a.OK || !strings.Contains(a.Error, "nope") {
		t.Errorf("unknown mailbox = %+v", a)
	}

	_, a = preview(t, e, map[string]string{"kind": "code", "template": "<div>{{.Code}}</div><tg-spoiler>x</tg-spoiler><p>y</p><div></div>"})
	if !a.OK || len(a.Unsupported) != 2 || a.Unsupported[0] != "div" || a.Unsupported[1] != "p" || a.HTML != "834291xy" {
		t.Errorf("unsupported = %+v", a)
	}

	_, a = preview(t, e, map[string]string{"kind": "code", "template": "{{if}}"})
	if a.OK || !strings.Contains(a.Error, "templates.code") {
		t.Errorf("parse error = %+v", a)
	}
	_, a = preview(t, e, map[string]string{"kind": "code", "template": strings.Repeat("x", maxTemplateLen+1)})
	if a.OK || !strings.Contains(a.Error, "範本太長") {
		t.Errorf("long = %+v", a)
	}
	if status, a := preview(t, e, map[string]string{"kind": "other", "template": "x"}); status != 400 || a.OK {
		t.Errorf("bad kind = %d %+v", status, a)
	}
	for _, kind := range []string{"code", "link", "both"} {
		_, body := e.postJSON("/api/templates/preview", map[string]string{"kind": kind, "template": "", "mailbox": "gmail"})
		if strings.Contains(body, "SECRET") {
			t.Fatalf("secret in preview: %s", body)
		}
	}
}

func TestBuiltinTemplatesCompile(t *testing.T) {
	b := render.Builtin()
	if b.Code == "" || b.Link == "" || b.Both == "" {
		t.Fatal("empty builtin")
	}
	if _, err := render.New(&config.Config{Templates: b}); err != nil {
		t.Fatal(err)
	}
}

func TestRulesPageHasMailTest(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	_, body := e.get("/rules")
	for _, want := range []string{`data-mailtest`, `name="eml"`, `name="eml_text"`, `name="mailbox"`, `value="gmail"`, `data-mt-out`, `尚未儲存也會套用`} {
		if !strings.Contains(body, want) {
			t.Errorf("rules page lacks %q", want)
		}
	}
	if strings.Contains(body, " style=") || strings.Contains(body, "<script>") {
		t.Errorf("inline style or script breaks the CSP")
	}
	// Without JS the form must POST to the API (rejected there for lacking the CSRF
	// header), never GET the page with the pasted mail in the URL.
	i := strings.Index(body, `id="mailtest"`)
	start, end := strings.LastIndex(body[:max(i, 0)], "<form"), strings.Index(body[max(i, 0):], ">")
	if i < 0 || start < 0 || end < 0 {
		t.Fatalf("no mail test form tag")
	}
	tag := body[start : i+end]
	for _, want := range []string{`method="post"`, `action="/api/rules/test"`, `enctype="multipart/form-data"`} {
		if !strings.Contains(tag, want) {
			t.Errorf("mail test form %s lacks %s", tag, want)
		}
	}
	if !strings.Contains(body, `data-mt-live`) || !strings.Contains(body, `role="status"`) {
		t.Errorf("rules page lacks the mail test status line")
	}
	// that no-JS post: same origin and logged in, but no CSRF token
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("eml_text", recoveryMail)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/rules/test", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", e.ts.URL)
	if resp, _ := e.do(req); resp.StatusCode != http.StatusForbidden {
		t.Errorf("no-JS mail test post = %d, want 403", resp.StatusCode)
	}
}

// The built-in link template previews with the host line and a button named after the link.
func TestTemplatePreviewLinkButton(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	_, body := e.postJSON("/api/templates/preview", map[string]string{"kind": "link", "template": ""})
	var a previewAnswer
	if err := json.Unmarshal([]byte(body), &a); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.HTML, "🌐 github.com") || strings.Contains(a.HTML, "login/device") {
		t.Errorf("html = %q", a.HTML)
	}
	if !strings.Contains(body, `"text":"🔗 驗證裝置"`) {
		t.Errorf("buttons = %s", body)
	}
}
