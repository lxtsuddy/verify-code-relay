package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
)

// fakeMicrosoft answers the device-code and token endpoints of any tenant. By default the
// token endpoint says authorization_pending once, then grants a token.
type fakeMicrosoft struct {
	*httptest.Server
	mu       sync.Mutex
	devCalls int
	polls    int
	tenants  []string
	devBody  string                    // devicecode reply (200)
	token    func(n int) (int, string) // n = 1 for the first poll
}

const fakeGrant = `{"access_token":"SECRET-ACCESS","refresh_token":"SECRET-REFRESH","expires_in":3600}`

func newFakeMicrosoft(t *testing.T) *fakeMicrosoft {
	t.Helper()
	f := &fakeMicrosoft{
		devBody: `{"device_code":"SECRET-DC","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":0}`,
		token: func(n int) (int, string) {
			if n == 1 {
				return 400, `{"error":"authorization_pending"}`
			}
			return 200, fakeGrant
		},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeMicrosoft) serve(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[1] != "oauth2" || parts[2] != "v2.0" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	f.tenants = append(f.tenants, parts[0])
	var status int
	var body string
	var token func(int) (int, string)
	switch parts[3] {
	case "devicecode":
		f.devCalls++
		status, body = 200, f.devBody
	case "token":
		f.polls++
		token = f.token
		n := f.polls
		f.mu.Unlock()
		status, body = token(n) // may block: called without the lock
		w.WriteHeader(status)
		io.WriteString(w, body)
		return
	default:
		status, body = 404, `{}`
	}
	f.mu.Unlock()
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func (f *fakeMicrosoft) deviceCodeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.devCalls
}

func (f *fakeMicrosoft) setToken(fn func(n int) (int, string)) {
	f.mu.Lock()
	f.token = fn
	f.mu.Unlock()
}

// withFakeMicrosoft points the server at ms and polls every 10 ms.
func withFakeMicrosoft(ms *fakeMicrosoft) func(o *Options) {
	return func(o *Options) {
		o.MSAuthority = ms.URL
		o.MSPollInterval = 10 * time.Millisecond
	}
}

// waitOAuthState polls the status endpoint until it reports want (up to 20 s) and
// returns the decoded answer.
func (e *env) waitOAuthState(name, want string) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, st := e.get("/api/mailboxes/" + name + "/oauth/status")
		var m map[string]any
		if err := json.Unmarshal([]byte(st), &m); err != nil {
			e.t.Fatalf("status = %s", st)
		}
		if m["state"] == want {
			return m
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("status = %s, want state %s", st, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMicrosoftDeviceFlow(t *testing.T) {
	ms := newFakeMicrosoft(t) // pending once, then success
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	_, body := e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	if !strings.Contains(body, "ABCD-EFGH") {
		t.Fatalf("start = %s", body)
	}
	_, again := e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	if ms.deviceCodeCalls() != 1 || !strings.Contains(again, "ABCD-EFGH") {
		t.Fatal("second start must reuse the pending flow")
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, st := e.get("/api/mailboxes/outlook/oauth/status")
		if strings.Contains(st, `"done"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %s", st)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); !ok {
		t.Fatal("token not stored")
	}
	if !e.notified("授權已完成") {
		t.Error("completion not notified")
	}
}

func TestOAuthStartRejectsPasswordMailbox(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	if resp, _ := e.postJSON("/api/mailboxes/gmail/oauth/start", nil); resp.StatusCode != 400 {
		t.Fatalf("= %d", resp.StatusCode)
	}
}

// The start answer carries what the browser shows and nothing secret; the stored
// settings (tenant) drive the request.
func TestOAuthStartAnswer(t *testing.T) {
	ms := newFakeMicrosoft(t)
	ms.setToken(func(int) (int, string) { return 400, `{"error":"authorization_pending"}` })
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	resp, body := e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("start = %d %s", resp.StatusCode, body)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got["user_code"] != "ABCD-EFGH" || got["verification_uri"] != "https://microsoft.com/devicelogin" || got["state"] != "pending" {
		t.Errorf("start = %s", body)
	}
	if n, _ := got["expires_in"].(float64); n < 890 || n > 900 {
		t.Errorf("expires_in = %v", got["expires_in"])
	}
	if at, err := time.Parse(time.RFC3339, got["expires_at"].(string)); err != nil || at.Sub(e.now) < 899*time.Second {
		t.Errorf("expires_at = %v (%v)", got["expires_at"], err)
	}
	if strings.Contains(body, "SECRET-DC") {
		t.Errorf("start leaks the device code: %s", body)
	}
	ms.mu.Lock()
	tenant := ms.tenants[0]
	ms.mu.Unlock()
	if tenant != "consumers" {
		t.Errorf("tenant = %s", tenant)
	}
	_, st := e.get("/api/mailboxes/outlook/oauth/status")
	if !strings.Contains(st, `"pending"`) || strings.Contains(st, "SECRET") {
		t.Errorf("status = %s", st)
	}
}

func TestOAuthUnknownMailboxAndNoFlow(t *testing.T) {
	e := newEnv(t, withOutlookMailbox)
	e.login()
	if resp, _ := e.postJSON("/api/mailboxes/nope/oauth/start", nil); resp.StatusCode != 404 {
		t.Errorf("start unknown = %d", resp.StatusCode)
	}
	if resp, _ := e.get("/api/mailboxes/nope/oauth/status"); resp.StatusCode != 404 {
		t.Errorf("status unknown = %d", resp.StatusCode)
	}
	resp, st := e.get("/api/mailboxes/outlook/oauth/status")
	if resp.StatusCode != 200 || !strings.Contains(st, `"none"`) {
		t.Errorf("status without a flow = %d %s", resp.StatusCode, st)
	}
}

// A failed devicecode request answers ok:false with the reason, and no flow is kept.
func TestOAuthStartFailure(t *testing.T) {
	ms := newFakeMicrosoft(t)
	ms.devBody = `{"error":"unauthorized_client"}`
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	resp, body := e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "允許公開用戶端流程") {
		t.Fatalf("start = %d %s", resp.StatusCode, body)
	}
	if _, st := e.get("/api/mailboxes/outlook/oauth/status"); !strings.Contains(st, `"none"`) {
		t.Errorf("status = %s", st)
	}
}

// Declined and expired sign-ins end in state error with an explanation; a new start
// then begins a new flow. Finished flows are dropped a minute after being reported.
func TestOAuthFlowErrorsAndRetry(t *testing.T) {
	ms := newFakeMicrosoft(t)
	ms.setToken(func(int) (int, string) { return 400, `{"error":"authorization_declined"}` })
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	st := e.waitOAuthState("outlook", "error")
	if msg, _ := st["error"].(string); !strings.Contains(msg, "拒絕") {
		t.Errorf("declined error = %q", msg)
	}

	ms.setToken(func(int) (int, string) { return 400, `{"error":"expired_token"}` })
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	if ms.deviceCodeCalls() != 2 {
		t.Fatalf("a start after an error must begin a new flow (calls = %d)", ms.deviceCodeCalls())
	}
	st = e.waitOAuthState("outlook", "error")
	if msg, _ := st["error"].(string); !strings.Contains(msg, "過期") || strings.Contains(msg, "relay login") {
		t.Errorf("expired error = %q", msg)
	}
	// still there within the minute, gone after it
	e.now = e.now.Add(50 * time.Second)
	e.waitOAuthState("outlook", "error")
	e.now = e.now.Add(61 * time.Second)
	e.waitOAuthState("outlook", "none")
	if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); ok {
		t.Error("a failed flow stored a token")
	}
}

// Deleting the mailbox or changing its account while a flow waits cancels the flow, and
// a token granted afterwards is never stored.
func TestOAuthFlowCancelledByMailboxChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(e *env)
	}{
		{"delete", func(e *env) { e.postForm("/mailboxes/outlook/delete", nil) }},
		{"username", func(e *env) {
			e.postForm("/mailboxes/outlook", url.Values{"host": {"outlook.office365.com"}, "username": {"other@outlook.com"},
				"auth_type": {"microsoft_oauth"}, "client_id": {"00000000-0000-0000-0000-000000000001"}, "tenant": {"consumers"},
				"folders": {"INBOX"}, "targets": {"me"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newFakeMicrosoft(t)
			release := make(chan struct{})
			polled := make(chan struct{}, 1)
			ms.setToken(func(n int) (int, string) {
				select {
				case polled <- struct{}{}:
				default:
				}
				<-release // the user finishes signing in only after the change
				return 200, fakeGrant
			})
			e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
			e.login()
			e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
			<-polled
			tc.change(e)
			close(release)
			if tc.name == "delete" {
				if resp, _ := e.get("/api/mailboxes/outlook/oauth/status"); resp.StatusCode != 404 {
					t.Errorf("status after delete = %d", resp.StatusCode)
				}
			} else {
				e.waitOAuthState("outlook", "none")
			}
			time.Sleep(200 * time.Millisecond) // let a (wrong) save happen
			if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); ok {
				t.Error("token stored for a changed or deleted mailbox")
			}
			for _, m := range e.tg.messages() { // after the pause above: no need to wait 5 s
				if strings.Contains(m.Text, "授權已完成") {
					t.Error("a cancelled flow was reported as completed")
				}
			}
		})
	}
}

// The save guard also refuses a token when the settings changed some other way (e.g. a
// version restore), without the flow having been cancelled.
func TestOAuthFlowGuardChecksStoredSettings(t *testing.T) {
	ms := newFakeMicrosoft(t)
	release := make(chan struct{})
	polled := make(chan struct{}, 1)
	ms.setToken(func(n int) (int, string) {
		select {
		case polled <- struct{}{}:
		default:
		}
		<-release
		return 200, fakeGrant
	})
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	<-polled
	// change the client_id behind the web's back
	raw, _, err := loadRaw(e)
	if err != nil {
		t.Fatal(err)
	}
	raw.Mailboxes["outlook"].Auth.ClientID = "00000000-0000-0000-0000-000000000002"
	saveRaw(e, raw)
	close(release)
	st := e.waitOAuthState("outlook", "error")
	if msg, _ := st["error"].(string); !strings.Contains(msg, "設定已變更") {
		t.Errorf("error = %q", msg)
	}
	if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); ok {
		t.Error("token stored for other settings")
	}
}

// A display-name edit leaves a waiting flow alone.
func TestOAuthFlowSurvivesUnrelatedEdit(t *testing.T) {
	ms := newFakeMicrosoft(t)
	release := make(chan struct{})
	polled := make(chan struct{}, 1)
	ms.setToken(func(n int) (int, string) {
		select {
		case polled <- struct{}{}:
		default:
		}
		<-release
		return 200, fakeGrant
	})
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	<-polled
	e.postForm("/mailboxes/outlook", url.Values{"display_name": {"工作信箱"}, "host": {"outlook.office365.com"}, "username": {"me@outlook.com"},
		"auth_type": {"microsoft_oauth"}, "client_id": {"00000000-0000-0000-0000-000000000001"}, "tenant": {"consumers"},
		"folders": {"INBOX"}, "targets": {"me"}})
	close(release)
	e.waitOAuthState("outlook", "done")
	if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); !ok {
		t.Error("token not stored")
	}
}

// The list offers authorisation for Microsoft mailboxes without a token.
func TestMailboxListOffersAuthorisation(t *testing.T) {
	e := newEnv(t, withSecrets)
	e.login()
	_, page := e.get("/mailboxes")
	if !strings.Contains(page, `data-oauth-start="outlook"`) || strings.Contains(page, `data-oauth-start="gmail"`) {
		t.Errorf("list authorise buttons wrong")
	}
	if !strings.Contains(page, `id="dlg-oauth"`) {
		t.Error("list page lacks the authorisation dialog")
	}
	if err := e.st.SaveOAuthToken(context.Background(), "outlook", store.OAuthToken{RefreshToken: "SECRET-REFRESH", AccessToken: "SECRET-ACCESS", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	_, page = e.get("/mailboxes")
	if strings.Contains(page, `data-oauth-start="outlook"`) {
		t.Error("an authorised mailbox that is not failing needs no button")
	}
	if strings.Contains(page, "SECRET") {
		t.Error("list leaks a token")
	}
	_, page = e.get("/mailboxes/outlook")
	if !strings.Contains(page, `data-oauth-start="outlook"`) || !strings.Contains(page, `id="dlg-oauth"`) || !strings.Contains(page, `data-oauth-state="outlook"`) {
		t.Error("edit page lacks the authorise button or dialog")
	}
}

func loadRaw(e *env) (*config.Config, int64, error) {
	return runtime.LoadConfig(context.Background(), e.st)
}

// saveRaw stores raw as a new version without going through the web handlers.
func saveRaw(e *env, raw *config.Config) {
	e.t.Helper()
	if _, _, err := runtime.SaveConfig(context.Background(), e.st, raw, runtime.Meta{Source: "test", Summary: "測試"}, e.now); err != nil {
		e.t.Fatal(err)
	}
}

// Neither the device code nor the tokens reach the logs, whatever the outcome.
func TestOAuthFlowLogsNoSecrets(t *testing.T) {
	var buf syncBuf
	ms := newFakeMicrosoft(t)
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms), func(o *Options) {
		o.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	e.login()
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	e.waitOAuthState("outlook", "done")
	ms.setToken(func(int) (int, string) {
		return 400, `{"error":"invalid_grant","error_description":"bad"}`
	})
	e.now = e.now.Add(2 * time.Minute)
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	e.waitOAuthState("outlook", "error")
	out := buf.String()
	if !strings.Contains(out, "Microsoft 授權已完成") || !strings.Contains(out, "Microsoft 授權沒有完成") {
		t.Errorf("log lacks the outcomes:\n%s", out)
	}
	for _, s := range []string{"SECRET-DC", "SECRET-ACCESS", "SECRET-REFRESH"} {
		if strings.Contains(out, s) {
			t.Errorf("log leaks %s", s)
		}
	}
}

// A form sent back with errors keeps the stored account on the authorisation box, so the
// page can tell unsaved account changes apart from the stored settings.
func TestEditPageKeepsStoredAccountForAuthorisation(t *testing.T) {
	e := newEnv(t, withOutlookMailbox)
	e.login()
	resp, page := e.postForm("/mailboxes/outlook", url.Values{"host": {"outlook.office365.com"}, "port": {"99999"}, "username": {"me@outlook.com"},
		"auth_type": {"microsoft_oauth"}, "client_id": {"00000000-0000-0000-0000-000000000001"}, "tenant": {"organizations"},
		"folders": {"INBOX"}, "targets": {"me"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("= %d", resp.StatusCode)
	}
	if !strings.Contains(page, `data-stored-tenant="consumers"`) || !strings.Contains(page, `value="organizations"`) {
		t.Error("the sent-back form must keep the stored tenant on the authorisation box")
	}
}
