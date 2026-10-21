package web

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lxtsuddy/verify-code-relay/internal/config"
	"github.com/lxtsuddy/verify-code-relay/internal/runtime"
	"github.com/lxtsuddy/verify-code-relay/internal/store"
	"github.com/lxtsuddy/verify-code-relay/internal/websec"
)

// regexpStripTags returns the text content of an HTML/SVG fragment.
func regexpStripTags(s string) string { return tagRe.ReplaceAllString(s, "") }

var tagRe = regexp.MustCompile(`<[^>]*>`)

// storeVersion is a config version written straight to the store (bypassing validation).
func storeVersion(yaml string) store.ConfigVersion {
	return store.ConfigVersion{CreatedAt: time.Now(), Source: "test", Summary: "直接寫入", YAML: yaml}
}

func TestSettingsSaveAndRestore(t *testing.T) {
	e := newEnv(t, withSample) // withSample adds bot main, target me, mailbox gmail
	e.login()
	resp, body := e.postForm("/settings/general", url.Values{"max_age": {"5m"}, "ttl": {""}, "alert_ttl": {""}, "poll_interval": {""}, "alert_after": {""}, "admin": {"me"}, "log_level": {"debug"}})
	if resp.StatusCode != 303 {
		t.Fatalf("save = %d %s", resp.StatusCode, body)
	}
	if e.flash(resp) != "已儲存並套用" {
		t.Errorf("flash = %q", e.flash(resp))
	}
	raw, id, _ := runtime.LoadConfig(context.Background(), e.st)
	if raw.Defaults.MaxAge.Duration != 5*time.Minute || raw.Defaults.TTL.Set || raw.LogLevel != "debug" || raw.Admin != "me" {
		t.Fatalf("raw = %+v", raw.Defaults)
	}
	if v, _, _ := e.st.LatestConfigVersion(context.Background()); v.Source != "web" || v.Summary != "修改系統設定" {
		t.Errorf("version meta = %+v", v)
	}
	resp, body = e.postForm("/settings/general", url.Values{"max_age": {"0s"}, "log_level": {"info"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "必須大於 0") {
		t.Fatalf("invalid save = %d", resp.StatusCode)
	}
	if _, id2, _ := runtime.LoadConfig(context.Background(), e.st); id2 != id {
		t.Fatal("invalid save stored a version")
	}
	resp, _ = e.postForm(fmt.Sprintf("/settings/versions/%d/restore", id-1), nil)
	if resp.StatusCode != 303 {
		t.Fatalf("restore = %d", resp.StatusCode)
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if raw.Defaults.MaxAge.Set {
		t.Error("restore did not bring back the previous version")
	}
	v, _, _ := e.st.LatestConfigVersion(context.Background())
	if v.Source != "restore" || v.IP == "" {
		t.Errorf("version meta = %+v", v)
	}
	if v.Summary != fmt.Sprintf("還原版本 #%d", id-1) {
		t.Errorf("restore summary = %q", v.Summary)
	}
	if cur := e.rt.Current(); cur.Config.LogLevel != "info" {
		t.Error("restored version not applied")
	}
}

func TestSettingsFormShowsRawValuesAndErrors(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Defaults.TTL = config.Duration{Duration: 20 * time.Minute, Set: true}
	})
	e.login()
	_, body := e.get("/settings")
	// only explicitly set values are filled in; the others show the built-in default as a placeholder
	if !strings.Contains(body, `name="ttl" type="text" value="20m"`) {
		t.Error("explicit ttl not shown")
	}
	if !strings.Contains(body, `name="max_age" type="text" value=""`) || !strings.Contains(body, `placeholder="10m"`) {
		t.Error("unset max_age must be empty with the default as placeholder")
	}
	if !strings.Contains(body, `value="me" selected`) {
		t.Error("admin target not selected")
	}

	// a bare number is a parse error on its field; the submitted values are kept
	resp, body := e.postForm("/settings/general", url.Values{"max_age": {"10"}, "ttl": {"3m"}, "alert_ttl": {"abc"}, "alert_after": {"0s"}, "admin": {""}, "log_level": {"warn"}})
	if resp.StatusCode != 422 {
		t.Fatalf("bad durations = %d", resp.StatusCode)
	}
	if strings.Count(body, "請輸入例如 10m、30s、1h") != 2 || !strings.Contains(body, `value="10"`) || !strings.Contains(body, `value="3m"`) {
		t.Error("parse errors or submitted values missing")
	}
	if !strings.Contains(body, "必須大於 0") {
		t.Error("validation problems of the other fields must be reported together with parse errors")
	}
	if !strings.Contains(body, `value="warn" checked`) {
		t.Error("submitted log level not kept")
	}

	// an unknown admin target and log level are reported on their fields
	resp, body = e.postForm("/settings/general", url.Values{"admin": {"ghost"}, "log_level": {"loud"}})
	if resp.StatusCode != 422 || !strings.Contains(body, `找不到目標 &#34;ghost&#34;`) || !strings.Contains(body, "必須是 debug、info、warn 或 error") {
		t.Errorf("reference errors = %d", resp.StatusCode)
	}
	if strings.Contains(body, "SECRET-TOKEN") {
		t.Error("secret on the settings page")
	}
}

func TestSettingsAdminCanBeUnset(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	if resp, _ := e.postForm("/settings/general", url.Values{"admin": {""}, "log_level": {"info"}}); resp.StatusCode != 303 {
		t.Fatalf("unset admin = %d", resp.StatusCode)
	}
	if raw, _, _ := runtime.LoadConfig(context.Background(), e.st); raw.Admin != "" {
		t.Error("admin not cleared")
	}
}

func TestSettingsShowsUnrelatedProblems(t *testing.T) {
	// a stored config that has become invalid elsewhere must still say why a save fails
	e := newEnv(t, nil)
	e.login()
	bad := `log_level: info
targets:
  me: {bot: gone, chat_id: 42}
`
	if _, err := e.st.SaveConfigVersion(context.Background(), storeVersion(bad)); err != nil {
		t.Fatal(err)
	}
	resp, body := e.postForm("/settings/general", url.Values{"log_level": {"info"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "targets.me.bot: 找不到 Bot") {
		t.Fatalf("unrelated problem not shown: %d", resp.StatusCode)
	}
}

func TestChangePasswordLogsOutOthers(t *testing.T) {
	e := newEnv(t, nil)
	other := e.clone() // second client
	other.login()
	e.login()
	e.root().now = e.root().now.Add(30 * time.Second) // login used this step's code
	resp, _ := e.postForm("/settings/password", url.Values{"current": {testPassword}, "code": {e.code()}, "new": {"another-long-password"}, "confirm": {"another-long-password"}})
	if resp.StatusCode != 303 {
		t.Fatalf("change = %d", resp.StatusCode)
	}
	if resp, _ := other.get("/"); resp.StatusCode != 303 {
		t.Error("other session survived the password change")
	}
	if resp, _ := e.get("/"); resp.StatusCode != 200 {
		t.Error("own session must survive")
	}
	a, _, _ := e.st.GetAdmin(context.Background())
	if ok, _ := websec.VerifyPassword(a.PasswordHash, "another-long-password"); !ok {
		t.Error("password not changed")
	}
	if !e.notified("Web 後台密碼已修改") {
		t.Error("password change not notified")
	}
}

func TestChangePasswordRejections(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	next := func() string { e.root().now = e.root().now.Add(30 * time.Second); return e.code() }
	cases := []struct {
		name string
		form url.Values
		want string
	}{
		{"short", url.Values{"current": {testPassword}, "code": {next()}, "new": {"short-pw"}, "confirm": {"short-pw"}}, "至少需要 12 個字元"},
		{"mismatch", url.Values{"current": {testPassword}, "code": {next()}, "new": {"another-long-password"}, "confirm": {"another-long-passwort"}}, "兩次輸入的新密碼不一致"},
		{"wrong current", url.Values{"current": {"not-the-password"}, "code": {next()}, "new": {"another-long-password"}, "confirm": {"another-long-password"}}, "目前密碼或驗證碼不正確"},
		{"wrong code", url.Values{"current": {testPassword}, "code": {"000000"}, "new": {"another-long-password"}, "confirm": {"another-long-password"}}, "目前密碼或驗證碼不正確"},
	}
	for _, c := range cases {
		resp, body := e.postForm("/settings/password", c.form)
		if resp.StatusCode != 422 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d, want 422 with %q", c.name, resp.StatusCode, c.want)
		}
		if strings.Contains(body, "another-long-password") || strings.Contains(body, testPassword) {
			t.Errorf("%s: a password was echoed back", c.name)
		}
	}
	// a code already used (here: by a successful change) cannot be used again
	code := next()
	form := url.Values{"current": {testPassword}, "code": {code}, "new": {"another-long-password"}, "confirm": {"another-long-password"}}
	if resp, _ := e.postForm("/settings/password", form); resp.StatusCode != 303 {
		t.Fatalf("change = %d", resp.StatusCode)
	}
	form.Set("current", "another-long-password")
	if resp, body := e.postForm("/settings/password", form); resp.StatusCode != 422 || !strings.Contains(body, "目前密碼或驗證碼不正確") {
		t.Errorf("replayed code = %d", resp.StatusCode)
	}
	a, _, _ := e.st.GetAdmin(context.Background())
	if ok, _ := websec.VerifyPassword(a.PasswordHash, "another-long-password"); !ok {
		t.Error("password should be the one from the successful change")
	}
}

func TestRebindTOTPNeedsCurrentCode(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	e.root().now = e.root().now.Add(30 * time.Second)
	resp, body := e.postForm("/settings/totp", url.Values{"code": {"000000"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "驗證碼不正確") {
		t.Fatalf("wrong code = %d", resp.StatusCode)
	}
	if a, _, _ := e.st.GetAdmin(context.Background()); a.TOTPPending != "" {
		t.Fatal("pending secret created without the current code")
	}
	resp, _ = e.postForm("/settings/totp", url.Values{"code": {e.code()}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/setup/totp" {
		t.Fatalf("rebind start = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	a, _, _ := e.st.GetAdmin(context.Background())
	if a.TOTPPending == "" || a.TOTPSecret != e.secret {
		t.Fatal("pending secret not created, or the old one replaced too early")
	}
	// the same code cannot start it again
	if resp, _ := e.postForm("/settings/totp", url.Values{"code": {e.code()}}); resp.StatusCode != 422 {
		t.Errorf("replayed code = %d", resp.StatusCode)
	}
	// until confirmed, the old authenticator still signs in
	other := e.clone()
	other.login()
	_, page := e.get("/setup/totp")
	if !strings.Contains(page, "重新綁定") || !strings.Contains(page, a.TOTPPending[:4]) {
		t.Error("rebind page")
	}
}

func TestSettingsShowsClientIP(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	_, body := e.get("/settings")
	if !strings.Contains(body, "127.0.0.1") {
		t.Error("detected client IP not shown")
	}
}

func TestVersionsPage(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	e.postForm("/settings/general", url.Values{"log_level": {"debug"}})
	resp, body := e.get("/settings/versions")
	if resp.StatusCode != 200 {
		t.Fatalf("versions = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "修改系統設定") || !strings.Contains(body, "測試設定") || !strings.Contains(body, "目前") {
		t.Error("versions list incomplete")
	}
	if strings.Count(body, "/restore\"") != 1 {
		t.Errorf("restore buttons = %d, want 1 (not on the current version)", strings.Count(body, "/restore\""))
	}
	if strings.Contains(body, "SECRET-TOKEN") {
		t.Error("secret on the versions page")
	}
	if resp, _ := e.postForm("/settings/versions/9999/restore", nil); resp.StatusCode != 404 {
		t.Errorf("unknown version = %d", resp.StatusCode)
	}
	if resp, _ := e.postForm("/settings/versions/abc/restore", nil); resp.StatusCode != 404 {
		t.Errorf("bad id = %d", resp.StatusCode)
	}
}

func TestRestoreInvalidVersionIsRejected(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	id, err := e.st.SaveConfigVersion(context.Background(), storeVersion("log_level: loud\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, latest, _ := runtime.LoadConfig(context.Background(), e.st)
	resp, _ := e.postForm(fmt.Sprintf("/settings/versions/%d/restore", id), nil)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/settings/versions" || !strings.Contains(e.flash(resp), "log_level") {
		t.Fatalf("restore invalid = %d %s %q", resp.StatusCode, resp.Header.Get("Location"), e.flash(resp))
	}
	if _, now, _ := runtime.LoadConfig(context.Background(), e.st); now != latest {
		t.Error("invalid restore stored a version")
	}
}

func TestRebindEndsOtherSessions(t *testing.T) {
	e := newEnv(t, nil)
	other := e.clone()
	other.login()
	e.login()
	e.root().now = e.root().now.Add(30 * time.Second)
	if resp, _ := e.postForm("/settings/totp", url.Values{"code": {e.code()}}); resp.StatusCode != 303 {
		t.Fatalf("rebind start = %d", resp.StatusCode)
	}
	_, page := e.get("/setup/totp")
	e.csrf = extractCSRF(page)
	a, _, _ := e.st.GetAdmin(context.Background())
	e.root().now = e.root().now.Add(30 * time.Second)
	code, _ := websec.TOTPCode(a.TOTPPending, websec.TOTPStep(e.root().now))
	if resp, _ := e.postForm("/setup/totp", url.Values{"code": {code}}); resp.StatusCode != 303 {
		t.Fatalf("rebind confirm = %d", resp.StatusCode)
	}
	if resp, _ := other.get("/"); resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
		t.Errorf("other session survived the rebind: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := e.get("/"); resp.StatusCode != 200 {
		t.Errorf("rebinding session lost: %d", resp.StatusCode)
	}
	if !e.notified("其他裝置的登入已全部登出") {
		t.Error("rebind notification must say other sessions were ended")
	}
}

func (s *Server) failureCount() int {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return len(s.failures)
}

func TestSettingsCredentialFailuresCount(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	before := e.srv.failureCount()
	e.root().now = e.root().now.Add(30 * time.Second)
	e.postForm("/settings/password", url.Values{"current": {"not-the-password"}, "code": {e.code()}, "new": {"another-long-password"}, "confirm": {"another-long-password"}})
	e.postForm("/settings/totp", url.Values{"code": {"000000"}})
	// typos in the new password are not credential failures
	e.postForm("/settings/password", url.Values{"current": {testPassword}, "code": {e.code()}, "new": {"short"}, "confirm": {"short"}})
	if n := e.srv.failureCount() - before; n != 2 {
		t.Fatalf("recorded failures = %d, want 2", n)
	}
}

func TestVersionTimesAreMachineReadable(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	_, body := e.get("/settings/versions")
	v, _, _ := e.st.LatestConfigVersion(context.Background())
	want := fmt.Sprintf(`<time datetime="%s" data-localtime>%s UTC</time>`, v.CreatedAt.UTC().Format(time.RFC3339), v.CreatedAt.UTC().Format("2006-01-02 15:04"))
	if !strings.Contains(body, want) {
		t.Errorf("version time markup missing, want %s", want)
	}
}

func TestPostmarkHasNoNumbers(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	_, body := e.get("/settings")
	i := strings.Index(body, `<svg class="postmark"`)
	if i < 0 {
		t.Fatal("postmark missing")
	}
	svg := body[i:]
	svg = svg[:strings.Index(svg, "</svg>")]
	if strings.ContainsAny(regexpStripTags(svg), "0123456789") {
		t.Errorf("the postmark must not show numbers that could be read as an IP or time: %s", regexpStripTags(svg))
	}
}

// Restoring a version decides about OAuth tokens like any other save: a token issued for
// another account or server is dropped, one for the same account is kept.
func TestRestoreDropsStaleOAuthToken(t *testing.T) {
	e := newEnv(t, withOutlookMailbox) // version 1
	ctx := context.Background()
	tok := store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}
	hasToken := func() bool { _, ok, _ := e.st.GetOAuthToken(ctx, "outlook"); return ok }
	raw, _, _ := loadRaw(e)
	raw.Mailboxes["outlook"].DisplayName = "只改名稱"
	saveRaw(e, raw) // version 2: display name only
	raw.Mailboxes["outlook"].Host = "imap.evil.example"
	saveRaw(e, raw) // version 3: another host
	raw.Mailboxes["outlook"].Host = "outlook.office365.com"
	raw.Mailboxes["outlook"].Username = "other@outlook.com"
	saveRaw(e, raw) // version 4: another account
	delete(raw.Mailboxes, "outlook")
	saveRaw(e, raw) // version 5: no outlook
	e.login()
	restore := func(id int) {
		t.Helper()
		resp, _ := e.postForm(fmt.Sprintf("/settings/versions/%d/restore", id), nil)
		if resp.StatusCode != 303 || !strings.HasPrefix(e.flash(resp), fmt.Sprintf("已還原版本 #%d", id)) {
			t.Fatalf("restore %d = %d %q", id, resp.StatusCode, e.flash(resp))
		}
	}
	restore(1)
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	restore(2)
	if !hasToken() {
		t.Fatal("token dropped by restoring a version that only renames the mailbox")
	}
	resp, _ := e.postForm("/settings/versions/3/restore", nil)
	if hasToken() {
		t.Fatal("token kept after restoring a version with another host")
	}
	if f := e.flash(resp); !strings.Contains(f, "清除了信箱 outlook") {
		t.Errorf("restore flash = %q", f)
	}
	restore(2)
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	restore(4)
	if hasToken() {
		t.Fatal("token kept after restoring a version with another account")
	}
	restore(2)
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	restore(5)
	if hasToken() {
		t.Fatal("token kept after restoring a version without the mailbox")
	}
}

// The change-password and re-bind forms show the zh-Hant inline errors, not the browser's
// own validation bubbles (in the browser's language).
func TestSecurityFormsAreNovalidate(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	_, body := e.get("/settings")
	for _, action := range []string{"/settings/password#security", "/settings/totp#security"} {
		m := regexp.MustCompile(`<form [^>]*action="` + regexp.QuoteMeta(action) + `"[^>]*>`).FindString(body)
		if m == "" || !strings.Contains(m, " novalidate") {
			t.Errorf("form %s = %q, want novalidate", action, m)
		}
	}
}

// withOwnAppAndTeam is withOutlookMailbox ("outlook" with its own client_id and tenant
// consumers) plus "team", a Microsoft mailbox that uses the microsoft section.
func withOwnAppAndTeam(c *config.Config) {
	withSample(c)
	withOutlookMailbox(c)
	c.Microsoft = config.Microsoft{ClientID: "global-app"}
	c.Mailboxes["team"] = &config.Mailbox{Host: "outlook.office365.com", Username: "team@outlook.com",
		Auth: config.Auth{Type: "microsoft_oauth"}, Targets: []string{"me"}}
}

func TestSettingsMicrosoftApp(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { withSample(c); withOutlookMailbox(c) })
	e.login()
	ctx := context.Background()
	_, body := e.get("/settings")
	if !strings.Contains(body, `id="microsoft"`) || !strings.Contains(body, "Microsoft 應用程式") || !strings.Contains(body, "允許公用用戶端流程") {
		t.Fatal("settings page has no Microsoft application card")
	}
	// nothing relies on the default tenant yet: suggest consumers
	if !strings.Contains(body, `name="ms_tenant" type="text" value="consumers"`) {
		t.Error("tenant not pre-filled with consumers")
	}
	tok := store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	resp, body := e.postForm("/settings/microsoft", url.Values{"ms_client_id": {" global-app "}, "ms_tenant": {"consumers"}})
	if resp.StatusCode != 303 || e.flash(resp) != "已儲存 Microsoft 應用程式設定並套用" {
		t.Fatalf("save = %d %q %s", resp.StatusCode, e.flash(resp), body)
	}
	raw, _, _ := runtime.LoadConfig(ctx, e.st)
	if raw.Microsoft != (config.Microsoft{ClientID: "global-app", Tenant: "consumers"}) {
		t.Fatalf("microsoft = %+v", raw.Microsoft)
	}
	if v, _, _ := e.st.LatestConfigVersion(ctx); v.Summary != "修改 Microsoft 應用程式設定" {
		t.Errorf("summary = %q", v.Summary)
	}
	if _, ok, _ := e.st.GetOAuthToken(ctx, "outlook"); !ok {
		t.Error("a mailbox with its own app lost its token")
	}
	if resp, _ := e.postForm("/settings/microsoft", url.Values{"ms_client_id": {"global-app"}, "ms_tenant": {"consumers"}}); e.flash(resp) != msgNoChange {
		t.Errorf("unchanged save flash = %q", e.flash(resp))
	}
}

// The confirmation for a change lists exactly the authorised mailboxes whose token the
// save then drops, and the flash says which were cleared.
func TestSettingsMicrosoftAppChangeClearsTokens(t *testing.T) {
	e := newEnv(t, withOwnAppAndTeam)
	e.login()
	ctx := context.Background()
	tok := store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}
	e.st.SaveOAuthToken(ctx, "outlook", tok)
	e.st.SaveOAuthToken(ctx, "team", tok)
	_, body := e.get("/settings")
	if !strings.Contains(body, `data-confirm-client="將清除 1 個信箱的 Microsoft 授權，需要重新授權：team"`) {
		t.Errorf("client_id confirmation missing:\n%s", confirmAttrs(body))
	}
	// outlook keeps its own tenant; team uses the global (blank: common)
	if !strings.Contains(body, `data-confirm-tenant="將清除 1 個信箱的 Microsoft 授權，需要重新授權：team"`) {
		t.Errorf("tenant confirmation missing:\n%s", confirmAttrs(body))
	}
	if !strings.Contains(body, `data-stored-client_id="global-app"`) || !strings.Contains(body, `data-stored-tenant=""`) {
		t.Error("stored values missing")
	}
	if strings.Contains(body, `name="ms_tenant" type="text" value="consumers"`) {
		t.Error("tenant pre-filled although team relies on the default tenant")
	}
	resp, body := e.postForm("/settings/microsoft", url.Values{"ms_client_id": {"global-app-2"}, "ms_tenant": {""}})
	if resp.StatusCode != 303 {
		t.Fatalf("save = %d %s", resp.StatusCode, body)
	}
	if f := e.flash(resp); !strings.Contains(f, "清除了信箱 team 的 Microsoft 授權") || strings.Contains(f, "outlook") {
		t.Errorf("flash = %q", f)
	}
	if _, ok, _ := e.st.GetOAuthToken(ctx, "team"); ok {
		t.Error("token of a mailbox using the old global app kept")
	}
	if _, ok, _ := e.st.GetOAuthToken(ctx, "outlook"); !ok {
		t.Error("a mailbox with its own app lost its token")
	}
	// nothing authorised uses it any more: no confirmation
	if _, body = e.get("/settings"); strings.Contains(body, "data-confirm-client=\"將清除") {
		t.Errorf("confirmation without authorised mailboxes:\n%s", confirmAttrs(body))
	}
}

func TestSettingsMicrosoftAppCannotBeClearedWhileUsed(t *testing.T) {
	e := newEnv(t, withOwnAppAndTeam)
	e.login()
	resp, body := e.postForm("/settings/microsoft", url.Values{"ms_client_id": {""}, "ms_tenant": {""}})
	if resp.StatusCode != 422 {
		t.Fatalf("clear = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "信箱 team 沒有自己的應用程式識別碼") {
		t.Errorf("no reason on the field:\n%s", regexpStripTags(body))
	}
	if raw, _, _ := runtime.LoadConfig(context.Background(), e.st); raw.Microsoft.ClientID != "global-app" {
		t.Error("cleared although a mailbox needs it")
	}
}

func confirmAttrs(body string) string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "data-confirm-") || strings.Contains(line, "data-stored-") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return strings.Join(out, "\n")
}

// Saving the same effective values (a blank tenant is common) stores nothing.
func TestSettingsMicrosoftAppNoOpIsEffective(t *testing.T) {
	e := newEnv(t, withOwnAppAndTeam) // global client_id global-app, tenant blank
	e.login()
	_, before, _ := runtime.LoadConfig(context.Background(), e.st)
	resp, _ := e.postForm("/settings/microsoft", url.Values{"ms_client_id": {"global-app"}, "ms_tenant": {"common"}})
	if e.flash(resp) != msgNoChange {
		t.Errorf("blank → common flash = %q", e.flash(resp))
	}
	if _, after, _ := runtime.LoadConfig(context.Background(), e.st); after != before {
		t.Error("a save without an effective change stored a version")
	}
}

// The card groups the Microsoft mailboxes by what they take from it.
func TestSettingsMicrosoftAppGroupsByDependence(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		withOwnAppAndTeam(c) // team: both global; outlook: own client_id and tenant
		c.Mailboxes["half"] = &config.Mailbox{Host: "outlook.office365.com", Username: "h@outlook.com",
			Auth: config.Auth{Type: "microsoft_oauth", ClientID: "own-app"}, Targets: []string{"me"}}
		c.Mailboxes["tonly"] = &config.Mailbox{Host: "outlook.office365.com", Username: "t@outlook.com",
			Auth: config.Auth{Type: "microsoft_oauth", Tenant: "organizations"}, Targets: []string{"me"}}
	})
	e.login()
	_, body := e.get("/settings")
	text := regexpStripTags(body)
	for _, want := range []string{"沿用這裡的設定team", "只沿用預設 tenanthalf", "只沿用識別碼tonly", "使用自己的應用程式outlook"} {
		if !strings.Contains(strings.Join(strings.Fields(text), ""), strings.ReplaceAll(want, " ", "")) {
			t.Errorf("card does not list %q", want)
		}
	}
}

// Without a global client_id, mailboxes with their own client_id but a blank tenant still
// follow the default tenant: changing it is accepted, asks first and clears their tokens.
func TestSettingsMicrosoftDefaultTenantWithoutGlobalClient(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Mailboxes["alpha"] = &config.Mailbox{Host: "outlook.office365.com", Username: "a@outlook.com",
			Auth: config.Auth{Type: "microsoft_oauth", ClientID: "own-a"}, Targets: []string{"me"}}
	})
	e.login()
	ctx := context.Background()
	e.st.SaveOAuthToken(ctx, "alpha", store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)})
	if _, body := e.get("/settings"); !strings.Contains(body, `data-confirm-tenant="將清除 1 個信箱的 Microsoft 授權，需要重新授權：alpha"`) {
		t.Errorf("tenant confirmation missing:\n%s", confirmAttrs(body))
	}
	resp, _ := e.postForm("/settings/microsoft", url.Values{"ms_client_id": {""}, "ms_tenant": {"consumers"}})
	if resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "清除了信箱 alpha") {
		t.Fatalf("save = %d %q", resp.StatusCode, e.flash(resp))
	}
	if _, ok, _ := e.st.GetOAuthToken(ctx, "alpha"); ok {
		t.Error("token kept although the default tenant changed")
	}
}

// A version from before routes (folders and targets) restores, forwards as it did, and is
// stored in the new shape.
func TestRestoreVersionWithOldFoldersAndTargets(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	old := `bots: { main: { token: "1:SECRET-TOKEN" } }
targets: { me: { bot: main, chat_id: 42 }, fam: { bot: main, chat_id: 7 } }
admin: me
mailboxes:
  gmail:
    host: imap.gmail.com
    username: me@gmail.com
    auth: { type: password, password: p }
    folders: [INBOX, Spam]
    targets: [me, fam]
`
	id, err := e.st.SaveConfigVersion(context.Background(), storeVersion(old))
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := e.postForm(fmt.Sprintf("/settings/versions/%d/restore", id), nil); resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "已還原") {
		t.Fatalf("restore = %d %q", resp.StatusCode, e.flash(resp))
	}
	latest, _, _ := e.st.LatestConfigVersion(context.Background())
	if !strings.Contains(latest.YAML, "routes:") || strings.Contains(latest.YAML, "folders:") {
		t.Errorf("restored version not in the routes shape:\n%s", latest.YAML)
	}
	if n := len(e.rt.Snapshot().Watchers); n != 2 {
		t.Errorf("watchers = %d, want INBOX and Spam", n)
	}
	if _, body := e.get("/mailboxes/gmail"); strings.Count(body, `name="route_folder" value="Spam"`) != 2 {
		t.Error("edit page lacks Spam → me and Spam → fam")
	}
}
