package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/watcher"
)

type nopIMAPLogger struct{}

func (nopIMAPLogger) Printf(string, ...any) {}

// imapDial records the hosts the web server asked to dial.
type imapDial struct {
	addr  string
	hosts chan string
}

// startIMAP runs an in-memory IMAP server with user u / pw and folders INBOX and Spam.
func startIMAP(t *testing.T) *imapDial {
	t.Helper()
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("u", "pw")
	for _, f := range []string{"INBOX", "Spam"} {
		if err := u.Create(f, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
		Logger:       nopIMAPLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &imapDial{addr: ln.Addr().String(), hosts: make(chan string, 16)}
}

// factory dials the in-memory server whatever host was asked for (and records the host).
func (d *imapDial) factory(host string, _ int) watcher.DialFunc {
	return func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
		select {
		case d.hosts <- host:
		default:
		}
		var nd net.Dialer
		conn, err := nd.DialContext(ctx, "tcp", d.addr)
		if err != nil {
			return nil, err
		}
		return imapclient.New(conn, opts), nil
	}
}

// newEnvWithIMAP is newEnv whose web server (not the runtime) dials an in-memory IMAP
// server: user u / pw, folders INBOX and Spam. cfg may be nil.
func newEnvWithIMAP(t *testing.T, cfg ...func(c *config.Config)) *env {
	t.Helper()
	var mod func(c *config.Config)
	if len(cfg) > 0 {
		mod = cfg[0]
	}
	return newEnvWithIMAPOpts(t, mod)
}

// newEnvWithIMAPOpts is newEnvWithIMAP with further Options adjustments.
func newEnvWithIMAPOpts(t *testing.T, cfg func(c *config.Config), opts ...func(o *Options)) *env {
	t.Helper()
	d := startIMAP(t)
	e := newEnv(t, cfg, append([]func(o *Options){func(o *Options) { o.Dial = d.factory }}, opts...)...)
	e.imap = d
	return e
}

func TestCreateEditDeleteMailbox(t *testing.T) {
	e := newEnv(t, nil) // base config: bot main, target me; no mailboxes
	e.login()
	form := url.Values{"name": {"work"}, "display_name": {"工作信箱"}, "host": {"imap.example.com"}, "port": {""},
		"username": {"u@example.com"}, "auth_type": {"password"}, "password": {"SECRET-PW-1"},
		"route_folder": {"INBOX", "Spam", ""}, "route_target": {"me", "me", ""}, "max_age": {"5m"}}
	if resp, body := e.postForm("/mailboxes/new", form); resp.StatusCode != 303 {
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	m := raw.Mailboxes["work"]
	if m == nil || m.Port != 0 || len(m.Routes) != 2 || m.MaxAge.Duration != 5*time.Minute || m.Auth.Password != "SECRET-PW-1" {
		t.Fatalf("stored = %+v", m)
	}
	if len(e.rt.Snapshot().Watchers) != 2 {
		t.Fatal("new mailbox not applied")
	}
	if _, body := e.get("/mailboxes/work"); strings.Contains(body, "SECRET-PW-1") {
		t.Fatal("edit page leaks the password")
	}
	form.Del("name")
	form.Set("password", "")
	form.Set("display_name", "Work")
	if resp, _ := e.postForm("/mailboxes/work", form); resp.StatusCode != 303 {
		t.Fatalf("edit = %d", resp.StatusCode)
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if raw.Mailboxes["work"].Auth.Password != "SECRET-PW-1" || raw.Mailboxes["work"].DisplayName != "Work" {
		t.Fatal("blank password must keep the stored one")
	}
	e.st.SaveOAuthToken(context.Background(), "work", store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now()})
	if resp, _ := e.postForm("/mailboxes/work/delete", nil); resp.StatusCode != 303 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if _, ok, _ := e.st.GetOAuthToken(context.Background(), "work"); ok {
		t.Error("OAuth token not deleted with the mailbox")
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if _, ok := raw.Mailboxes["work"]; ok {
		t.Error("mailbox not deleted")
	}
}

func TestMailboxValidationErrorsShowOnFields(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	resp, body := e.postForm("/mailboxes/new", url.Values{"name": {"x"}, "host": {""}, "username": {"u"}, "auth_type": {"password"}, "password": {"p"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "不可為空") || !strings.Contains(body, "至少需要一條轉發路由") {
		t.Fatalf("= %d", resp.StatusCode)
	}
	// the messages sit under their fields, not in the form-level callout
	if strings.Contains(body, "mailboxes.x.host") || !strings.Contains(body, `id="f-host" name="host"`) {
		t.Error("host error shown as a raw path")
	}
	if !strings.Contains(body, `aria-invalid="true"`) {
		t.Error("no field marked invalid")
	}
	// a submitted password is never sent back
	if strings.Contains(body, `value="p"`) {
		t.Error("password echoed into the form")
	}
}

func TestMailboxFormRejects(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	base := url.Values{"host": {"imap.example.com"}, "username": {"u"}, "auth_type": {"password"}, "password": {"p"},
		"route_folder": {"INBOX"}, "route_target": {"me"}}
	with := func(kv ...string) url.Values {
		f := url.Values{}
		for k, v := range base {
			f[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			f.Set(kv[i], kv[i+1])
		}
		return f
	}
	cases := []struct {
		form url.Values
		want string
	}{
		{with("name", "gmail"), "名稱已存在"},
		{with("name", "new"), "保留"},
		{with("name", "Bad Name"), msgNameRule},
		{with("name", "a", "port", "99999"), "1 到 65535"},
		{with("name", "a", "max_age", "5"), "缺少單位"},
		{with("name", "a", "route_target", "nobody"), "找不到目標"},
	}
	for _, c := range cases {
		resp, body := e.postForm("/mailboxes/new", c.form)
		if resp.StatusCode != 422 || !strings.Contains(body, c.want) {
			t.Errorf("%v = %d, want 422 with %q", c.form, resp.StatusCode, c.want)
		}
	}
	// a duplicate route: reported under its row
	f := with("name", "a")
	f["route_folder"] = []string{"INBOX", "INBOX"}
	f["route_target"] = []string{"me", "me"}
	if resp, body := e.postForm("/mailboxes/new", f); resp.StatusCode != 422 || !strings.Contains(body, "與第 1 條路由重複") ||
		!strings.Contains(body, `id="f-route-err-1"`) || strings.Contains(body, `id="f-route-err-0"`) {
		t.Errorf("duplicate route = %d", resp.StatusCode)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if len(raw.Mailboxes) != 1 {
		t.Fatalf("rejected forms stored mailboxes: %v", sortedNames(raw.Mailboxes))
	}
}

func TestMailboxAuthSwitchClearsOtherFields(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	form := url.Values{"display_name": {"x"}, "host": {"outlook.office365.com"}, "username": {"me@outlook.com"},
		"auth_type": {"microsoft_oauth"}, "password": {""}, "client_id": {"cid"}, "tenant": {"consumers"}, "route_folder": {"INBOX"}, "route_target": {"me"}}
	if resp, body := e.postForm("/mailboxes/gmail", form); resp.StatusCode != 303 {
		t.Fatalf("switch to oauth = %d %s", resp.StatusCode, body)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if a := raw.Mailboxes["gmail"].Auth; a.Password != "" || a.ClientID != "cid" || a.Tenant != "consumers" {
		t.Fatalf("auth = %+v", a)
	}
	// back to password: client_id/tenant are dropped and a password is required again
	form.Set("auth_type", "password")
	if resp, body := e.postForm("/mailboxes/gmail", form); resp.StatusCode != 422 || !strings.Contains(body, "不可為空") {
		t.Fatalf("password without a stored one = %d", resp.StatusCode)
	}
	form.Set("password", "new-pw")
	if resp, _ := e.postForm("/mailboxes/gmail", form); resp.StatusCode != 303 {
		t.Fatalf("switch back = %d", resp.StatusCode)
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if a := raw.Mailboxes["gmail"].Auth; a.Password != "new-pw" || a.ClientID != "" || a.Tenant != "" {
		t.Fatalf("auth = %+v", a)
	}
}

func TestMailboxPages(t *testing.T) {
	e := newEnv(t, withSecrets)
	e.login()
	resp, body := e.get("/mailboxes")
	if resp.StatusCode != 200 || !strings.Contains(body, "個人 Gmail") || !strings.Contains(body, "me@gmail.com") || !strings.Contains(body, "尚未授權") {
		t.Fatalf("list = %d", resp.StatusCode)
	}
	if strings.Contains(body, "SECRET-PW-XYZ") {
		t.Fatal("list leaks the password")
	}
	_, body = e.get("/mailboxes/gmail")
	if !strings.Contains(body, "已設定，留空表示不變") || strings.Contains(body, "SECRET-PW-XYZ") {
		t.Fatal("edit page: secret state wrong or leaked")
	}
	_, body = e.get("/mailboxes/outlook")
	if !strings.Contains(body, `data-oauth-start="outlook"`) || !strings.Contains(body, "尚未授權") {
		t.Fatal("outlook page lacks the authorise element / state")
	}
	e.st.SaveOAuthToken(context.Background(), "outlook", store.OAuthToken{RefreshToken: "REFRESH-SECRET", AccessToken: "ACCESS-SECRET", ExpiresAt: time.Now().Add(time.Hour)})
	_, body = e.get("/mailboxes/outlook")
	if !strings.Contains(body, "已授權") || strings.Contains(body, "REFRESH-SECRET") || strings.Contains(body, "ACCESS-SECRET") {
		t.Fatal("authorised state wrong or token leaked")
	}
	if resp, _ := e.get("/mailboxes/nope"); resp.StatusCode != 404 {
		t.Errorf("unknown mailbox = %d", resp.StatusCode)
	}
	if resp, body := e.get("/mailboxes/new"); resp.StatusCode != 200 || !strings.Contains(body, `name="preset"`) {
		t.Errorf("new form = %d", resp.StatusCode)
	}
	if resp, _ := e.postForm("/mailboxes/nope/delete", nil); resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "找不到") {
		t.Errorf("delete unknown = %d %q", resp.StatusCode, e.flash(resp))
	}
}

func TestMailboxEmptyState(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.Admin = ""; c.Targets = map[string]config.Target{} })
	e.login()
	_, body := e.get("/mailboxes")
	if !strings.Contains(body, "先新增 Bot 與目標，再新增信箱") {
		t.Fatal("empty state without targets lacks the guidance")
	}
}

func TestProbeEndpoint(t *testing.T) {
	e := newEnvWithIMAP(t) // in-memory IMAP server; env.Dial points at it; user u / pw, folders INBOX, Spam
	e.login()
	_, body := e.postJSON("/api/mailboxes/probe", map[string]string{"host": "imap.test", "port": "993", "username": "u", "auth_type": "password", "password": "pw"})
	if !strings.Contains(body, `"ok":true`) || !strings.Contains(body, "Spam") {
		t.Fatalf("probe = %s", body)
	}
	_, body = e.postJSON("/api/mailboxes/probe", map[string]string{"host": "imap.test", "port": "993", "username": "u", "auth_type": "password", "password": "bad"})
	if !strings.Contains(body, `"ok":false`) || strings.Contains(body, "bad") {
		t.Fatalf("bad password probe = %s", body)
	}
}

func TestProbeUsesStoredPasswordOnlyForTheSameServer(t *testing.T) {
	e := newEnvWithIMAP(t, func(c *config.Config) {
		c.Mailboxes["box"] = &config.Mailbox{Host: "imap.test", Username: "u", Auth: config.Auth{Type: "password", Password: "pw"},
			Targets: []string{"me"}}
	})
	e.login()
	_, body := e.postJSON("/api/mailboxes/probe", map[string]string{"name": "box", "host": "imap.test", "port": "", "username": "u", "auth_type": "password"})
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("probe with stored password = %s", body)
	}
	<-e.imap.hosts
	// another host (or account) never receives the stored password
	_, body = e.postJSON("/api/mailboxes/probe", map[string]string{"name": "box", "host": "evil.example", "port": "993", "username": "u", "auth_type": "password"})
	if !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "請輸入密碼") {
		t.Fatalf("probe of another host = %s", body)
	}
	select {
	case h := <-e.imap.hosts:
		t.Fatalf("dialled %s with the stored password", h)
	default:
	}
}

func TestProbeMicrosoftNeedsAuthorisation(t *testing.T) {
	e := newEnvWithIMAP(t, withOutlookMailbox)
	e.login()
	for _, name := range []string{"outlook", ""} {
		_, body := e.postJSON("/api/mailboxes/probe", map[string]string{"name": name, "host": "outlook.office365.com", "username": "me@outlook.com",
			"auth_type": "microsoft_oauth", "client_id": "cid", "tenant": "consumers"})
		if !strings.Contains(body, "請先完成 Microsoft 授權") {
			t.Errorf("probe %q = %s", name, body)
		}
	}
}

func TestProbeRejectsMissingFields(t *testing.T) {
	e := newEnvWithIMAP(t)
	e.login()
	_, body := e.postJSON("/api/mailboxes/probe", map[string]string{"host": "", "username": "u", "auth_type": "password", "password": "pw"})
	if !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "主機") {
		t.Fatalf("probe without host = %s", body)
	}
}

func TestProbeFailureNeverEchoesThePassword(t *testing.T) {
	ctx := context.Background()
	echo := &watcher.AuthError{Err: &imap.Error{Type: imap.StatusResponseTypeNo, Text: "bad login for hunter2-secret"}}
	if msg := probeFailure(ctx, "h", 993, echo, "hunter2-secret"); strings.Contains(msg, "hunter2") || !strings.Contains(msg, "登入失敗") {
		t.Errorf("auth failure = %q", msg)
	}
	plain := &watcher.AuthError{Err: &imap.Error{Type: imap.StatusResponseTypeNo, Text: "[AUTHENTICATIONFAILED] Invalid credentials"}}
	if msg := probeFailure(ctx, "h", 993, plain, "pw"); !strings.Contains(msg, "Invalid credentials") {
		t.Errorf("server text dropped without reason: %q", msg)
	}
	if msg := probeFailure(ctx, "h", 993, errors.New("連線：dial hunter2-secret"), "hunter2-secret"); strings.Contains(msg, "hunter2") {
		t.Errorf("connect failure = %q", msg)
	}
	listErr := fmt.Errorf("%w：%w", watcher.ErrListFolders, errors.New("LIST is broken"))
	if msg := probeFailure(ctx, "h", 993, listErr, ""); !strings.HasPrefix(msg, "已登入，但無法列出資料夾") || !strings.Contains(msg, "LIST is broken") {
		t.Errorf("list failure = %q", msg)
	}
	if msg := probeFailure(ctx, "h", 993, errors.New("認證：更新 OAuth token 失敗：HTTP 400 unauthorized_client"), ""); !strings.HasPrefix(msg, "登入過程失敗") {
		t.Errorf("auth-step failure = %q", msg)
	}
	expired, cancel := context.WithTimeout(ctx, 0)
	defer cancel()
	<-expired.Done()
	if msg := probeFailure(expired, "h", 993, context.DeadlineExceeded, ""); !strings.Contains(msg, "20 秒") {
		t.Errorf("timeout = %q", msg)
	}
}

func TestMailboxNoOpEditStoresNothing(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	before, _, _ := e.st.LatestConfigVersion(context.Background())
	// the gmail sample exactly as the edit page would post it back (password left blank)
	form := url.Values{"display_name": {"個人 Gmail"}, "host": {"imap.gmail.com"}, "port": {""}, "username": {"me@gmail.com"},
		"auth_type": {"password"}, "password": {""}, "route_folder": {"INBOX"}, "route_target": {"me"}}
	resp, _ := e.postForm("/mailboxes/gmail", form)
	if resp.StatusCode != 303 || e.flash(resp) != msgNoChange {
		t.Fatalf("no-op edit = %d %q", resp.StatusCode, e.flash(resp))
	}
	after, _, _ := e.st.LatestConfigVersion(context.Background())
	if after.ID != before.ID {
		t.Fatalf("a no-op edit stored version %d (was %d)", after.ID, before.ID)
	}
	// the routes are in the order the user put them: reordering them is a change
	e2 := newEnv(t, func(c *config.Config) {
		withSample(c)
		c.Targets["family"] = config.Target{Bot: "main", ChatID: 7}
		c.Mailboxes["gmail"].Routes = []config.Route{{Folder: "INBOX", Target: "me"}, {Folder: "Spam", Target: "family"}}
	})
	e2.login()
	form["route_folder"] = []string{"Spam", "INBOX"}
	form["route_target"] = []string{"family", "me"}
	if resp, _ := e2.postForm("/mailboxes/gmail", form); resp.StatusCode != 303 || e2.flash(resp) == msgNoChange {
		t.Fatalf("reordered edit = %d %q", resp.StatusCode, e2.flash(resp))
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e2.st)
	if got := raw.Mailboxes["gmail"].Routes; len(got) != 2 || got[0].Folder != "Spam" {
		t.Fatalf("stored routes = %v", got)
	}
}

func TestProbeWithoutNameNeverUsesStoredCredentials(t *testing.T) {
	e := newEnvWithIMAP(t, func(c *config.Config) {
		c.Mailboxes["box"] = &config.Mailbox{Host: "imap.test", Username: "u", Auth: config.Auth{Type: "password", Password: "pw"},
			Targets: []string{"me"}}
	})
	e.login()
	_, body := e.postJSON("/api/mailboxes/probe", map[string]string{"host": "imap.test", "username": "u", "auth_type": "password"})
	if !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "請輸入密碼") {
		t.Fatalf("probe without name = %s", body)
	}
	select {
	case h := <-e.imap.hosts:
		t.Fatalf("dialled %s without a password", h)
	default:
	}
}

func TestProbeMicrosoftUsesOnlyStoredSettings(t *testing.T) {
	e := newEnvWithIMAP(t, withOutlookMailbox)
	e.login()
	ctx := context.Background()
	e.st.SaveOAuthToken(ctx, "outlook", store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)})
	same := map[string]string{"name": "outlook", "host": "outlook.office365.com", "username": "me@outlook.com",
		"auth_type": "microsoft_oauth", "client_id": "00000000-0000-0000-0000-000000000001", "tenant": "consumers"}
	for field, v := range map[string]string{"client_id": "attacker-app", "tenant": "organizations", "host": "evil.example", "username": "other@outlook.com"} {
		in := map[string]string{}
		for k, x := range same {
			in[k] = x
		}
		in[field] = v
		_, body := e.postJSON("/api/mailboxes/probe", in)
		if !strings.Contains(body, `"ok":false`) || !strings.Contains(body, "與已儲存的不同") {
			t.Errorf("probe with another %s = %s", field, body)
		}
	}
	select {
	case h := <-e.imap.hosts:
		t.Fatalf("dialled %s with differing settings", h)
	default:
	}
	// the stored settings go ahead (the in-memory server refuses XOAUTH2: a login failure)
	_, body := e.postJSON("/api/mailboxes/probe", same)
	if !strings.Contains(body, "登入失敗") {
		t.Fatalf("same-server probe = %s", body)
	}
	if h := <-e.imap.hosts; h != "outlook.office365.com" {
		t.Fatalf("dialled %s", h)
	}
}

// A token refresh by the connection test that ends after a save changed the server (and
// deleted the token) must not write the token back.
func TestProbeRefreshDoesNotRestoreDroppedToken(t *testing.T) {
	ms := newFakeMicrosoft(t)
	started, release := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock) // before the fake server closes
	ms.setToken(func(int) (int, string) {
		startOnce.Do(func() { close(started) })
		<-release
		return 200, fakeGrant
	})
	e := newEnvWithIMAPOpts(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	ctx := context.Background()
	// expired: the probe has to refresh it
	e.st.SaveOAuthToken(ctx, "outlook", store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(-time.Hour)})

	b, _ := json.Marshal(map[string]string{"name": "outlook", "host": "outlook.office365.com", "username": "me@outlook.com",
		"auth_type": "microsoft_oauth", "client_id": "00000000-0000-0000-0000-000000000001", "tenant": "consumers"})
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/api/mailboxes/probe", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	req.Header.Set("Origin", e.ts.URL)
	done := make(chan string, 1)
	go func() {
		resp, err := e.c.Do(req)
		if err != nil {
			done <- "request failed: " + err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		done <- string(body)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe never refreshed the token")
	}

	form := url.Values{"display_name": {"Outlook"}, "host": {"imap-mail.outlook.com"}, "username": {"me@outlook.com"},
		"auth_type": {"microsoft_oauth"}, "client_id": {"00000000-0000-0000-0000-000000000001"}, "tenant": {"consumers"},
		"route_folder": {"INBOX"}, "route_target": {"me"}}
	if resp, body := e.postForm("/mailboxes/outlook", form); resp.StatusCode != 303 {
		t.Fatalf("host change = %d %s", resp.StatusCode, body)
	}
	if _, ok, _ := e.st.GetOAuthToken(ctx, "outlook"); ok {
		t.Fatal("token kept after the host changed")
	}
	unblock()
	select {
	case got := <-done:
		if !strings.Contains(got, `"ok":false`) { // the in-memory server refuses XOAUTH2
			t.Fatalf("probe = %s", got)
		}
	case <-time.After(probeTimeout + 5*time.Second):
		t.Fatal("the probe did not finish")
	}
	if tok, ok, _ := e.st.GetOAuthToken(ctx, "outlook"); ok {
		t.Fatalf("the probe wrote the dropped token back: %+v", tok)
	}
}

// Without a concurrent change, a token the connection test refreshes is stored: the
// rotated refresh token must not be lost.
func TestProbeRefreshStoresRotatedToken(t *testing.T) {
	ms := newFakeMicrosoft(t)
	ms.setToken(func(int) (int, string) { return 200, fakeGrant })
	e := newEnvWithIMAPOpts(t, withOutlookMailbox, withFakeMicrosoft(ms))
	e.login()
	ctx := context.Background()
	e.st.SaveOAuthToken(ctx, "outlook", store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(-time.Hour)})
	_, body := e.postJSON("/api/mailboxes/probe", map[string]string{"name": "outlook", "host": "outlook.office365.com", "username": "me@outlook.com",
		"auth_type": "microsoft_oauth", "client_id": "00000000-0000-0000-0000-000000000001", "tenant": "consumers"})
	if !strings.Contains(body, `"ok":false`) { // the in-memory server refuses XOAUTH2
		t.Fatalf("probe = %s", body)
	}
	tok, ok, err := e.st.GetOAuthToken(ctx, "outlook")
	if err != nil || !ok || tok.AccessToken != "SECRET-ACCESS" || tok.RefreshToken != "SECRET-REFRESH" || !tok.ExpiresAt.After(time.Now()) {
		t.Fatalf("stored token = %+v %v %v, want the refreshed one", tok, ok, err)
	}
}

// A mailbox that switches to Microsoft authorisation never keeps a token left over from
// earlier (for example from a deleted mailbox of the same name whose token was not cleared).
func TestMailboxBecomingMicrosoftDropsLeftoverToken(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	ctx := context.Background()
	e.st.SaveOAuthToken(ctx, "gmail", store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)})
	form := url.Values{"display_name": {"個人 Gmail"}, "host": {"outlook.office365.com"}, "username": {"me@outlook.com"},
		"auth_type": {"microsoft_oauth"}, "client_id": {"00000000-0000-0000-0000-000000000001"}, "tenant": {"consumers"},
		"route_folder": {"INBOX"}, "route_target": {"me"}}
	resp, body := e.postForm("/mailboxes/gmail", form)
	if resp.StatusCode != 303 {
		t.Fatalf("switch to Microsoft = %d %s", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/mailboxes/gmail" || !strings.Contains(e.flash(resp), "下一步：在信箱頁按「授權」") {
		t.Errorf("switch to Microsoft: location %q, flash %q", loc, e.flash(resp))
	}
	if _, ok, _ := e.st.GetOAuthToken(ctx, "gmail"); ok {
		t.Fatal("leftover token kept after the mailbox switched to Microsoft")
	}
}

func TestMailboxEditDropsStaleOAuthToken(t *testing.T) {
	e := newEnv(t, withOutlookMailbox)
	e.login()
	ctx := context.Background()
	tok := store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}
	form := url.Values{"display_name": {"Outlook"}, "host": {"outlook.office365.com"}, "username": {"me@outlook.com"},
		"auth_type": {"microsoft_oauth"}, "client_id": {"00000000-0000-0000-0000-000000000001"}, "tenant": {"consumers"},
		"route_folder": {"INBOX"}, "route_target": {"me"}}
	hasToken := func() bool { _, ok, _ := e.st.GetOAuthToken(ctx, "outlook"); return ok }
	edit := func(kv ...string) {
		t.Helper()
		for i := 0; i < len(kv); i += 2 {
			form.Set(kv[i], kv[i+1])
		}
		if resp, body := e.postForm("/mailboxes/outlook", form); resp.StatusCode != 303 {
			t.Fatalf("edit %v = %d %s", kv, resp.StatusCode, body)
		}
	}
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	edit("display_name", "Work") // unrelated change: kept
	if !hasToken() {
		t.Fatal("token dropped by a display name change")
	}
	edit("tenant", "") // consumers → common: another tenant
	if hasToken() {
		t.Fatal("token kept after the tenant changed")
	}
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	edit("username", "other@outlook.com")
	if hasToken() {
		t.Fatal("token kept after the account changed")
	}
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	edit("client_id", "00000000-0000-0000-0000-000000000002")
	if hasToken() {
		t.Fatal("token kept after client_id changed")
	}
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	edit("auth_type", "password", "password", "pw")
	if hasToken() {
		t.Fatal("token kept after switching to a password")
	}
}

// A blank password keeps the stored one only for the same host, port and account: the
// stored password must never be sent to another server.
func TestMailboxServerChangeNeedsPassword(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	base := url.Values{"display_name": {"個人 Gmail"}, "host": {"imap.gmail.com"}, "port": {""}, "username": {"me@gmail.com"},
		"auth_type": {"password"}, "password": {""}, "route_folder": {"INBOX"}, "route_target": {"me"}}
	for field, v := range map[string]string{"host": "imap.evil.example", "port": "994", "username": "other@gmail.com"} {
		form := url.Values{}
		for k, x := range base {
			form[k] = append([]string(nil), x...)
		}
		form.Set(field, v)
		resp, body := e.postForm("/mailboxes/gmail", form)
		if resp.StatusCode != 422 || !strings.Contains(body, "主機或帳號已變更，請重新輸入密碼") {
			t.Errorf("%s changed with a blank password = %d", field, resp.StatusCode)
		}
		raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
		if m := raw.Mailboxes["gmail"]; m.Host != "imap.gmail.com" || m.Port != 0 || m.Username != "me@gmail.com" {
			t.Fatalf("%s: stored %+v", field, m)
		}
		form.Set("password", "NEW-PW")
		if resp, body := e.postForm("/mailboxes/gmail", form); resp.StatusCode != 303 {
			t.Fatalf("%s changed with a new password = %d %s", field, resp.StatusCode, body)
		}
		raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
		if raw.Mailboxes["gmail"].Auth.Password != "NEW-PW" {
			t.Fatalf("%s: new password not stored", field)
		}
		// back to the original server, with its password
		base.Set("password", "p")
		if resp, body := e.postForm("/mailboxes/gmail", base); resp.StatusCode != 303 {
			t.Fatalf("restore %s = %d %s", field, resp.StatusCode, body)
		}
		base.Set("password", "")
	}
	// the default port written out (993) and another letter case of the host are the same server
	form := url.Values{}
	for k, x := range base {
		form[k] = append([]string(nil), x...)
	}
	form.Set("port", "993")
	form.Set("host", "IMAP.Gmail.com")
	if resp, body := e.postForm("/mailboxes/gmail", form); resp.StatusCode != 303 {
		t.Fatalf("same server = %d %s", resp.StatusCode, body)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if raw.Mailboxes["gmail"].Auth.Password != "p" {
		t.Fatal("blank password on the same server must keep the stored one")
	}
}

func TestMailboxServerChangeDropsOAuthToken(t *testing.T) {
	e := newEnv(t, withOutlookMailbox)
	e.login()
	ctx := context.Background()
	tok := store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}
	hasToken := func() bool { _, ok, _ := e.st.GetOAuthToken(ctx, "outlook"); return ok }
	for field, v := range map[string]string{"host": "imap.evil.example", "port": "994"} {
		form := url.Values{"display_name": {"Outlook"}, "host": {"outlook.office365.com"}, "username": {"me@outlook.com"},
			"auth_type": {"microsoft_oauth"}, "client_id": {"00000000-0000-0000-0000-000000000001"}, "tenant": {"consumers"},
			"route_folder": {"INBOX"}, "route_target": {"me"}}
		e.st.SaveOAuthToken(ctx, "outlook", tok)
		form.Set(field, v)
		resp, body := e.postForm("/mailboxes/outlook", form)
		if resp.StatusCode != 303 {
			t.Fatalf("%s change = %d %s", field, resp.StatusCode, body)
		}
		if hasToken() {
			t.Errorf("token kept after the %s changed", field)
		}
		if !strings.Contains(e.flash(resp), "請重新授權") {
			t.Errorf("%s change flash = %q", field, e.flash(resp))
		}
		raw, _, _ := runtime.LoadConfig(ctx, e.st)
		raw.Mailboxes["outlook"].Host, raw.Mailboxes["outlook"].Port = "outlook.office365.com", 0
		saveRaw(e, raw)
	}
}

// The connection test compares the effective app: a mailbox using the microsoft section
// tests with blank overrides (or ones equal to the global values), and a differing override
// is refused.
func TestProbeMicrosoftUsesEffectiveApp(t *testing.T) {
	e := newEnvWithIMAP(t, withGlobalMicrosoftApp)
	e.login()
	ctx := context.Background()
	e.st.SaveOAuthToken(ctx, "outlook", store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)})
	probe := func(clientID, tenant string) string {
		_, body := e.postJSON("/api/mailboxes/probe", map[string]string{"name": "outlook", "host": "outlook.office365.com", "username": "me@outlook.com",
			"auth_type": "microsoft_oauth", "client_id": clientID, "tenant": tenant})
		return body
	}
	for _, in := range [][2]string{{"", ""}, {"global-app", ""}, {"", "organizations"}} {
		// a fresh token each time: the refused XOAUTH2 login marks the stored one as failed
		e.st.SaveOAuthToken(ctx, "outlook", store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)})
		if body := probe(in[0], in[1]); !strings.Contains(body, "登入失敗") { // the in-memory server refuses XOAUTH2
			t.Errorf("probe %v = %s, want a login attempt", in, body)
		}
		<-e.imap.hosts
	}
	for _, in := range [][2]string{{"other-app", ""}, {"", "consumers"}} {
		if body := probe(in[0], in[1]); !strings.Contains(body, "與已儲存的不同") {
			t.Errorf("probe %v = %s, want refused", in, body)
		}
	}
}

// The mailbox form takes client_id and tenant as optional overrides of the microsoft section.
func TestMailboxFormMicrosoftAppDefaults(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	const needsGlobal = "請先到系統設定填寫 Microsoft 應用程式"
	_, body := e.get("/mailboxes/new")
	if !strings.Contains(body, needsGlobal) || !strings.Contains(body, `href="/settings#microsoft"`) {
		t.Error("no pointer to the settings page while no global app is set")
	}
	if strings.Contains(body, `name="tenant" type="text" value="consumers"`) {
		t.Error("a new mailbox must not carry a tenant override by default")
	}
	outlook := url.Values{"name": {"o"}, "host": {"outlook.office365.com"}, "username": {"me@outlook.com"},
		"auth_type": {"microsoft_oauth"}, "client_id": {""}, "tenant": {""}, "route_folder": {"INBOX"}, "route_target": {"me"}}
	resp, body := e.postForm("/mailboxes/new", outlook)
	if resp.StatusCode != 422 || !strings.Contains(body, "未設定：請在系統設定填寫 Microsoft 應用程式，或在信箱進階設定覆寫") {
		t.Fatalf("without any client_id = %d", resp.StatusCode)
	}
	if !strings.Contains(body, `<details class="card advanced" open>`) {
		t.Error("advanced section not opened for the client_id error")
	}

	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	raw.Microsoft = config.Microsoft{ClientID: "global-app", Tenant: "consumers"}
	saveRaw(e, raw)
	_, body = e.get("/mailboxes/new")
	if strings.Contains(body, needsGlobal) {
		t.Error("pointer to the settings page although a global app is set")
	}
	if !strings.Contains(body, `placeholder="global-app"`) || !strings.Contains(body, `placeholder="consumers"`) {
		t.Error("overrides do not show the global values as placeholders")
	}
	if resp, body := e.postForm("/mailboxes/new", outlook); resp.StatusCode != 303 {
		t.Fatalf("create with the global app = %d %s", resp.StatusCode, body)
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if a := raw.Mailboxes["o"].Auth; a != (config.Auth{Type: "microsoft_oauth"}) {
		t.Errorf("stored auth = %+v, want no overrides", a)
	}
}

// A stored mailbox with its own client_id (every Microsoft mailbox before the microsoft
// section existed) keeps it as an override and is not told to set a global app.
func TestMailboxFormKeepsLegacyOverride(t *testing.T) {
	e := newEnv(t, withOutlookMailbox)
	e.login()
	_, body := e.get("/mailboxes/outlook")
	if strings.Contains(body, "請先到系統設定填寫 Microsoft 應用程式") {
		t.Error("legacy mailbox told to set a global app")
	}
	if !strings.Contains(body, `value="00000000-0000-0000-0000-000000000001"`) || !strings.Contains(body, `name="tenant" type="text" value="consumers"`) {
		t.Error("legacy overrides not shown")
	}
	if resp, _ := e.postForm("/mailboxes/outlook", outlookForm("display_name", "Outlook")); e.flash(resp) != msgNoChange {
		t.Errorf("saving the legacy mailbox unchanged = %q", e.flash(resp))
	}
}

// Without a global client_id the override shows no sample value as if it were a default.
func TestMailboxOverridePlaceholderBlankWithoutGlobal(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	if _, body := e.get("/mailboxes/new"); strings.Contains(body, `placeholder="00000000-0000-0000-0000-000000000000"`) {
		t.Error("client_id override shows a sample UUID placeholder")
	}
}

// A long folder name is clipped in the list; its full name is the title.
func TestMailboxListFolderTitle(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	_, body := e.get("/mailboxes")
	if !strings.Contains(body, `<span class="mb-folder-name mono" title="INBOX">INBOX</span>`) {
		t.Error("folder name without title")
	}
}

// The routes card: a new mailbox starts with INBOX → the only target; an edit shows the
// stored rows; both carry blank spare rows for use without script; problems sit on their row.
func TestMailboxRoutesCard(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	_, body := e.get("/mailboxes/new")
	if !strings.Contains(body, `name="route_folder" value="INBOX"`) || !strings.Contains(body, `<option value="me" selected>`) {
		t.Error("new mailbox does not start with INBOX → me")
	}
	if n := strings.Count(body, "route-row-spare"); n != spareRoutes {
		t.Errorf("spare rows = %d", n)
	}
	if !strings.Contains(body, `<template id="tpl-route-row">`) || !strings.Contains(body, `<datalist id="f-folder-options"`) {
		t.Error("no row template or folder suggestions")
	}
	_, body = e.get("/mailboxes/gmail")
	if !strings.Contains(body, `id="f-route-folder-0" name="route_folder" value="INBOX"`) {
		t.Error("edit page lacks the stored route")
	}

	base := url.Values{"name": {"x"}, "host": {"imap.example.com"}, "username": {"u"}, "auth_type": {"password"}, "password": {"p"}}
	resp, body := e.postForm("/mailboxes/new", base)
	if resp.StatusCode != 422 || !strings.Contains(body, "至少需要一條轉發路由") || !strings.Contains(body, `aria-describedby="f-routes-err"`) {
		t.Errorf("no routes = %d", resp.StatusCode)
	}
	f := url.Values{}
	for k, v := range base {
		f[k] = v
	}
	f["route_folder"] = []string{"INBOX", ""}
	f["route_target"] = []string{"me", "me"}
	_, body = e.postForm("/mailboxes/new", f)
	if !strings.Contains(body, `id="f-route-folder-1" name="route_folder" value="" list="f-folder-options" placeholder="資料夾，例如 INBOX" autocomplete="off" spellcheck="false" aria-invalid="true"`) ||
		!strings.Contains(body, `id="f-route-err-1"`) {
		t.Error("blank folder not marked on its row")
	}
}

// The list shows each folder's own targets after its state.
func TestMailboxListShowsTargetsPerFolder(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		withSample(c)
		c.Targets["fam"] = config.Target{Bot: "main", ChatID: 7}
		c.Mailboxes["gmail"].Routes = []config.Route{{Folder: "INBOX", Target: "me"}, {Folder: "Spam", Target: "fam"}}
	})
	e.login()
	_, body := e.get("/mailboxes")
	spam := regexp.MustCompile(`(?s)title="Spam">Spam</span>.*?</li>`).FindString(body)
	if spam == "" || !strings.Contains(spam, `<span class="route-chat mono">fam</span>`) || strings.Contains(spam, ">me<") {
		t.Errorf("Spam row = %q", spam)
	}
	inbox := regexp.MustCompile(`(?s)title="INBOX">INBOX</span>.*?</li>`).FindString(body)
	if !strings.Contains(inbox, `<span class="route-chat mono">me</span>`) || strings.Contains(inbox, ">fam<") {
		t.Errorf("INBOX row = %q", inbox)
	}
	// the overview's card lists both folders
	_, body = e.get("/")
	if !strings.Contains(body, `data-folder="Spam"`) {
		t.Error("overview card lacks the Spam folder")
	}
}
