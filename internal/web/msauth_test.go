package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lxtsuddy/verify-code-relay/internal/config"
	"github.com/lxtsuddy/verify-code-relay/internal/runtime"
	"github.com/lxtsuddy/verify-code-relay/internal/store"
)

// fakeMicrosoft answers the device-code and token endpoints of any tenant. By default the
// token endpoint says authorization_pending once, then grants a token.
type fakeMicrosoft struct {
	*httptest.Server
	mu       sync.Mutex
	devCalls int
	polls    int
	tenants  []string
	clients  []string                  // client_id of every request
	devBody  string                    // devicecode reply (200)
	devGate  map[string]chan struct{}  // by tenant: devicecode answers once the channel is closed
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
	f.clients = append(f.clients, r.PostForm.Get("client_id"))
	var status int
	var body string
	var token func(int) (int, string)
	switch parts[3] {
	case "devicecode":
		f.devCalls++
		gate := f.devGate[parts[0]]
		body = f.devBody
		f.mu.Unlock()
		if gate != nil {
			<-gate
		}
		io.WriteString(w, body)
		return
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

// seen returns the tenants and client_ids of the requests so far.
func (f *fakeMicrosoft) seen() (tenants, clients []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tenants...), append([]string(nil), f.clients...)
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

// outlookForm is the edit form of withOutlookMailbox with fields changed by kv.
func outlookForm(kv ...string) url.Values {
	f := url.Values{"host": {"outlook.office365.com"}, "username": {"me@outlook.com"},
		"auth_type": {"microsoft_oauth"}, "client_id": {"00000000-0000-0000-0000-000000000001"}, "tenant": {"consumers"},
		"route_folder": {"INBOX"}, "route_target": {"me"}}
	for i := 0; i+1 < len(kv); i += 2 {
		f.Set(kv[i], kv[i+1])
	}
	return f
}

// blockedGrant makes the token endpoint wait for release before granting; polled
// receives a value at the first poll.
func blockedGrant(ms *fakeMicrosoft) (polled <-chan struct{}, release chan struct{}) {
	p := make(chan struct{}, 1)
	release = make(chan struct{})
	ms.setToken(func(int) (int, string) {
		select {
		case p <- struct{}{}:
		default:
		}
		<-release // the user finishes signing in only when the test says so
		return 200, fakeGrant
	})
	return p, release
}

// Deleting the mailbox or changing its account or authentication while a flow waits
// cancels the flow, and a token granted afterwards is never stored.
func TestOAuthFlowCancelledByMailboxChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(e *env) *http.Response
	}{
		{"delete", func(e *env) *http.Response { r, _ := e.postForm("/mailboxes/outlook/delete", nil); return r }},
		{"username", func(e *env) *http.Response {
			r, _ := e.postForm("/mailboxes/outlook", outlookForm("username", "other@outlook.com"))
			return r
		}},
		{"tenant", func(e *env) *http.Response {
			r, _ := e.postForm("/mailboxes/outlook", outlookForm("tenant", "organizations"))
			return r
		}},
		{"password", func(e *env) *http.Response {
			r, _ := e.postForm("/mailboxes/outlook", outlookForm("auth_type", "password", "password", "app-password", "client_id", "", "tenant", ""))
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms := newFakeMicrosoft(t)
			polled, release := blockedGrant(ms)
			e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
			e.login()
			e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
			<-polled
			if resp := tc.change(e); resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("change = %d", resp.StatusCode)
			}
			close(release)
			if tc.name == "delete" {
				if resp, _ := e.get("/api/mailboxes/outlook/oauth/status"); resp.StatusCode != 404 {
					t.Errorf("status after delete = %d", resp.StatusCode)
				}
			} else {
				e.waitOAuthState("outlook", "none")
			}
			e.srv.Close() // waits for the flow goroutine: any (wrong) save has happened by now
			if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); ok {
				t.Error("token stored for a changed or deleted mailbox")
			}
			for _, m := range e.tg.messages() {
				if strings.Contains(m.Text, "授權已完成") {
					t.Error("a cancelled flow was reported as completed")
				}
			}
		})
	}
}

// Restoring a version with another client_id cancels the waiting flow.
func TestOAuthFlowCancelledByRestore(t *testing.T) {
	ms := newFakeMicrosoft(t)
	polled, release := blockedGrant(ms)
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms)) // version 1: client_id …001
	raw, _, err := loadRaw(e)
	if err != nil {
		t.Fatal(err)
	}
	raw.Mailboxes["outlook"].Auth.ClientID = "00000000-0000-0000-0000-000000000002"
	saveRaw(e, raw) // version 2, the current one
	e.login()
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	<-polled
	if resp, _ := e.postForm("/settings/versions/1/restore", nil); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("restore = %d", resp.StatusCode)
	}
	close(release)
	e.waitOAuthState("outlook", "none")
	e.srv.Close()
	if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); ok {
		t.Error("token stored after the restore changed the client_id")
	}
}

// Concurrent starts for one mailbox share one flow; a start for another mailbox does not
// wait for them.
func TestOAuthStartsArePerMailbox(t *testing.T) {
	ms := newFakeMicrosoft(t)
	ms.setToken(func(int) (int, string) { return 400, `{"error":"authorization_pending"}` })
	gate := make(chan struct{})
	ms.mu.Lock()
	ms.devGate = map[string]chan struct{}{"consumers": gate}
	ms.mu.Unlock()
	opened := false
	defer func() {
		if !opened {
			close(gate)
		}
	}()
	e := newEnv(t, func(c *config.Config) {
		withOutlookMailbox(c)
		m := *c.Mailboxes["outlook"]
		m.Username, m.Auth.Tenant = "me@contoso.com", "organizations"
		c.Mailboxes["work"] = &m
	}, withFakeMicrosoft(ms))
	e.login()
	answers := make(chan string, 2)
	for range 2 {
		go func() {
			_, b := e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
			answers <- b
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for ms.deviceCodeCalls() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("no devicecode request")
		}
		time.Sleep(5 * time.Millisecond)
	}
	other := make(chan *http.Response, 1)
	go func() {
		r, _ := e.postJSON("/api/mailboxes/work/oauth/start", nil)
		other <- r
	}()
	select {
	case r := <-other:
		if r.StatusCode != 200 {
			t.Errorf("work start = %d", r.StatusCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a start for another mailbox waited for outlook's")
	}
	opened = true
	close(gate)
	for range 2 {
		if b := <-answers; !strings.Contains(b, "ABCD-EFGH") {
			t.Errorf("outlook start = %s", b)
		}
	}
	if n := ms.deviceCodeCalls(); n != 2 {
		t.Errorf("devicecode calls = %d, want one per mailbox", n)
	}
}

// When applying the config fails after a successful sign-in, the token is kept and both
// the page and the notification say that it is not in use yet.
func TestOAuthReloadFailureIsReported(t *testing.T) {
	ms := newFakeMicrosoft(t)
	polled, release := blockedGrant(ms)
	e := newEnv(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	<-polled
	// a stored config that does not build (written around the validation)
	raw, _, err := loadRaw(e)
	if err != nil {
		t.Fatal(err)
	}
	raw.Targets["ghost"] = config.Target{Bot: "missing", ChatID: 1}
	data, err := config.Encode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.SaveConfigVersion(context.Background(), store.ConfigVersion{CreatedAt: e.now, Source: "test", YAML: string(data)}); err != nil {
		t.Fatal(err)
	}
	close(release)
	st := e.waitOAuthState("outlook", "done")
	if w, _ := st["warning"].(string); !strings.Contains(w, "重新套用設定失敗") {
		t.Errorf("warning = %q", w)
	}
	if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); !ok {
		t.Error("token not kept")
	}
	if !e.notified("重新套用設定失敗") {
		t.Error("the failed reload was not notified")
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
		"route_folder": {"INBOX"}, "route_target": {"me"}})
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
		"route_folder": {"INBOX"}, "route_target": {"me"}})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("= %d", resp.StatusCode)
	}
	if !strings.Contains(page, `data-stored-tenant="consumers"`) || !strings.Contains(page, `value="organizations"`) {
		t.Error("the sent-back form must keep the stored tenant on the authorisation box")
	}
}

// withGlobalMicrosoftApp sets the microsoft section and makes mailbox "outlook" use it.
func withGlobalMicrosoftApp(c *config.Config) {
	withOutlookMailbox(c)
	c.Microsoft = config.Microsoft{ClientID: "global-app", Tenant: "organizations"}
	c.Mailboxes["outlook"].Auth = config.Auth{Type: "microsoft_oauth"}
}

// A mailbox without its own client_id and tenant signs in with the microsoft section.
func TestDeviceFlowUsesGlobalMicrosoftApp(t *testing.T) {
	ms := newFakeMicrosoft(t)
	e := newEnv(t, withGlobalMicrosoftApp, withFakeMicrosoft(ms))
	e.login()
	if _, body := e.postJSON("/api/mailboxes/outlook/oauth/start", nil); !strings.Contains(body, "ABCD-EFGH") {
		t.Fatalf("start = %s", body)
	}
	e.waitOAuthState("outlook", "done")
	tenants, clients := ms.seen()
	for i := range tenants {
		if tenants[i] != "organizations" || clients[i] != "global-app" {
			t.Fatalf("request %d went to tenant %q with client_id %q, want the global app", i, tenants[i], clients[i])
		}
	}
	if _, ok, _ := e.st.GetOAuthToken(context.Background(), "outlook"); !ok {
		t.Fatal("token not stored")
	}
}

// A mailbox's own client_id wins over the microsoft section.
func TestDeviceFlowUsesMailboxOverride(t *testing.T) {
	ms := newFakeMicrosoft(t)
	e := newEnv(t, func(c *config.Config) {
		withGlobalMicrosoftApp(c)
		c.Mailboxes["outlook"].Auth.ClientID = "own-app"
	}, withFakeMicrosoft(ms))
	e.login()
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	e.waitOAuthState("outlook", "done")
	tenants, clients := ms.seen()
	if len(clients) == 0 || clients[0] != "own-app" || tenants[0] != "organizations" {
		t.Fatalf("tenants %v clients %v, want own-app on the global tenant", tenants, clients)
	}
}

// Changing the microsoft section while a flow of a mailbox that uses it is pending cancels
// the flow (its token would be for the old app) and leaves other mailboxes' flows alone.
func TestGlobalAppChangeCancelsFlowsAndDropsTokens(t *testing.T) {
	ms := newFakeMicrosoft(t)
	polled, release := blockedGrant(ms)
	defer close(release)
	e := newEnv(t, func(c *config.Config) {
		withGlobalMicrosoftApp(c)
		c.Mailboxes["own"] = &config.Mailbox{Host: "outlook.office365.com", Username: "own@outlook.com",
			Auth: config.Auth{Type: "microsoft_oauth", ClientID: "own-app", Tenant: "consumers"}, Targets: []string{"me"}}
		c.Mailboxes["team"] = &config.Mailbox{Host: "outlook.office365.com", Username: "team@outlook.com",
			Auth: config.Auth{Type: "microsoft_oauth"}, Targets: []string{"me"}}
	}, withFakeMicrosoft(ms))
	e.login()
	ctx := context.Background()
	for _, name := range []string{"own", "team"} {
		e.st.SaveOAuthToken(ctx, name, store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)})
	}
	e.postJSON("/api/mailboxes/outlook/oauth/start", nil)
	<-polled
	req, _ := http.NewRequest("POST", "/", nil)
	res, fe, err := e.srv.saveConfig(req.WithContext(ctx), "web", "test", func(raw *config.Config) error {
		raw.Microsoft.ClientID = "global-app-2"
		return nil
	})
	if err != nil || len(fe) > 0 {
		t.Fatalf("save: %v %v", err, fe)
	}
	if !slices.Equal(res.Dropped, []string{"team"}) {
		t.Errorf("dropped = %v, want only team", res.Dropped)
	}
	if st := e.waitOAuthState("outlook", "none"); st["state"] != "none" {
		t.Fatalf("flow of a mailbox using the global app survived: %v", st)
	}
	if _, ok, _ := e.st.GetOAuthToken(ctx, "own"); !ok {
		t.Fatal("a mailbox with its own app lost its token")
	}
}
