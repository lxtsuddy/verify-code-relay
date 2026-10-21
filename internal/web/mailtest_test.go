package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/runtime"
)

const recoveryMail = "From: Google <noreply@google.com>\r\nTo: bar@outlook.com\r\nSubject: Use 123456 to set up recovery email\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nUse this code to finish:\r\n123456\r\n"

type mailTestAnswer struct {
	OK      bool
	Error   string
	Errors  map[string]string
	Message struct {
		From, Subject, Text string
		Warnings            []string
		Links               []struct{ URL, Text string }
	}
	Trace   []string
	Result  struct{ Kind, Code, URL, Service, Rule string }
	Preview *struct {
		HTML    string
		Buttons []previewButton
		Length  int
		Max     int
	}
}

// postMultipart posts fields and, when eml is not nil, an "eml" file part, with the
// X-CSRF-Token header.
func (e *env) postMultipart(path string, fields url.Values, eml []byte) (*http.Response, string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, vs := range fields {
		for _, v := range vs {
			if err := mw.WriteField(k, v); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	if eml != nil {
		fw, err := mw.CreateFormFile("eml", "mail.eml")
		if err != nil {
			e.t.Fatal(err)
		}
		fw.Write(eml)
	}
	mw.Close()
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	return e.do(req)
}

func (e *env) mailTest(fields url.Values, eml []byte) mailTestAnswer {
	e.t.Helper()
	resp, body := e.postMultipart("/api/rules/test", fields, eml)
	var a mailTestAnswer
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &a) != nil {
		e.t.Fatalf("= %d %s", resp.StatusCode, body)
	}
	return a
}

func TestMailTestRecognisesCode(t *testing.T) {
	var logs syncBuf
	e := newEnv(t, nil, func(o *Options) { o.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	e.login()
	a := e.mailTest(nil, []byte(recoveryMail))
	if !a.OK || a.Result.Kind != "code" || a.Result.Code != "123456" || a.Result.Service != "Google" {
		t.Fatalf("answer = %+v", a)
	}
	if a.Preview == nil || !strings.Contains(a.Preview.HTML, "123456") || !strings.Contains(a.Preview.HTML, "（測試）") {
		t.Fatalf("preview = %+v", a.Preview)
	}
	if len(a.Trace) == 0 || a.Message.Subject != "Use 123456 to set up recovery email" || !strings.Contains(a.Message.From, "noreply@google.com") {
		t.Fatalf("trace %q message %+v", a.Trace, a.Message)
	}
	if strings.Contains(logs.String(), "123456") {
		t.Fatalf("the code reached the log:\n%s", logs.String())
	}
}

func TestMailTestUsesUnsavedRules(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	a := e.mailTest(url.Values{
		"rule_name": {"g"}, "rule_from": {"*@google.com"}, "rule_subject": {""},
		"rule_code_regex": {""}, "rule_link_regex": {""}, "rule_ignore": {"1"},
	}, []byte(recoveryMail))
	if !a.OK || a.Result.Kind != "ignored" || a.Result.Rule != "custom:g" || a.Preview != nil {
		t.Fatalf("answer = %+v", a)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if len(raw.Rules) != 0 {
		t.Fatalf("the test saved rules: %+v", raw.Rules)
	}
	mail := "From: a@shop.test\r\nSubject: hi\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n驗證號碼 482913\r\n"
	if a := e.mailTest(nil, []byte(mail)); a.Result.Kind != "none" {
		t.Fatalf("without the keyword: %+v", a.Result)
	}
	if a := e.mailTest(url.Values{"extra_code_keywords": {"驗證號碼"}}, []byte(mail)); a.Result.Code != "482913" {
		t.Fatalf("with the keyword: %+v", a.Result)
	}
}

func TestMailTestRuleErrors(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	a := e.mailTest(url.Values{
		"rule_name": {"bad"}, "rule_from": {"*@x.test"}, "rule_subject": {""},
		"rule_code_regex": {"("}, "rule_link_regex": {""}, "rule_ignore": {"0"},
	}, []byte(recoveryMail))
	if a.OK || !strings.Contains(a.Errors["rules[0].code_regex"], "正規表示式錯誤") {
		t.Fatalf("answer = %+v", a)
	}
}

func TestMailTestInputs(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	pasted := "\n" + strings.ReplaceAll(recoveryMail, "\r\n", "\n")
	if a := e.mailTest(url.Values{"eml_text": {pasted}}, nil); !a.OK || a.Result.Code != "123456" {
		t.Fatalf("pasted: %+v", a)
	}
	if a := e.mailTest(nil, nil); a.OK || !strings.Contains(a.Error, "請上傳") {
		t.Fatalf("empty: %+v", a)
	}
	big := append([]byte(recoveryMail), bytes.Repeat([]byte("x"), 5<<20)...)
	if a := e.mailTest(nil, big); a.OK || !strings.Contains(a.Error, "5 MiB") {
		t.Fatalf("too big: %+v", a.Error)
	}
	// Over the 6 MiB body cap the server stops reading and closes the connection, which a
	// real client may see as a write error: serve this one in memory instead.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("eml", "big.eml")
	fw.Write(bytes.Repeat([]byte("x"), 7<<20))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, e.ts.URL+"/api/rules/test", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", e.csrf)
	req.Header.Set("Origin", e.ts.URL)
	req.Header.Set("Cookie", sessionCookie+"="+e.sessionCookie())
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "5 MiB") {
		t.Fatalf("over the body cap: %d %s", rec.Code, rec.Body.String())
	}
	if a := e.mailTest(url.Values{"mailbox": {"gmail"}}, []byte(recoveryMail)); a.Preview == nil || !strings.Contains(a.Preview.HTML, "個人 Gmail") {
		t.Fatalf("mailbox preview: %+v", a.Preview)
	}
	if a := e.mailTest(url.Values{"mailbox": {"nope"}}, []byte(recoveryMail)); a.OK || !strings.Contains(a.Error, "nope") {
		t.Fatalf("unknown mailbox: %+v", a)
	}
}

func TestMailTestNeedsLoginAndSameOrigin(t *testing.T) {
	e := newEnv(t, nil)
	if resp, _ := e.postMultipart("/api/rules/test", nil, []byte(recoveryMail)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logged out = %d", resp.StatusCode)
	}
	e.login()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("eml_text", recoveryMail)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/rules/test", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", e.csrf)
	req.Header.Set("Origin", "https://evil.test")
	if resp, _ := e.do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin = %d", resp.StatusCode)
	}
}

func TestRulesPageListsMailboxes(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	_, body := e.get("/rules")
	if !strings.Contains(body, `value="gmail"`) || !strings.Contains(body, "個人 Gmail") {
		t.Fatalf("rules page lacks the mailbox option")
	}
}

// The page checks the size before sending, with the server's wording and limit.
func TestMailTestScriptChecksSize(t *testing.T) {
	js, err := staticFS.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{msgMailTooBig, fmt.Sprintf("MT_MAX_BYTES = %d", pipeline.MaxMessageSize)} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
}

// The answer names the link's text from the mail, and the preview's button uses it.
func TestMailTestLinkText(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	mail := "From: Example <no-reply@example.com>\r\nSubject: Confirm your email\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" +
		`<p>Please confirm your email address.</p><a href="https://example.com/verify?token=Zx8_kq2LmN0pQr7sTuVwXy">Confirm email</a>` + "\r\n"
	resp, body := e.postMultipart("/api/rules/test", nil, []byte(mail))
	var a struct {
		OK     bool
		Result struct {
			Kind     string
			LinkText string `json:"link_text"`
		}
		Preview *struct {
			HTML    string
			Buttons []previewButton
		}
	}
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &a) != nil || !a.OK {
		t.Fatalf("= %d %s", resp.StatusCode, body)
	}
	if a.Result.Kind != "link" || a.Result.LinkText != "Confirm email" || a.Preview == nil ||
		len(a.Preview.Buttons) != 1 || a.Preview.Buttons[0].Text != "🔗 Confirm email" || !strings.Contains(a.Preview.HTML, "example.com") {
		t.Fatalf("answer = %+v %+v", a.Result, a.Preview)
	}
}
