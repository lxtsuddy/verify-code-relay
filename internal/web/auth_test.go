package web

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/websec"
)

// authedPosts are the POST routes that need a signed-in admin with TOTP bound.
var authedPosts = []string{
	"/settings/general", "/settings/password", "/settings/totp", "/settings/versions/1/restore",
	"/telegram/bots", "/telegram/bots/main", "/telegram/bots/main/delete",
	"/telegram/targets", "/telegram/targets/me", "/telegram/targets/me/delete", "/telegram/targets/me/test",
	"/api/telegram/getme", "/api/telegram/chats",
	"/mailboxes/new", "/mailboxes/gmail", "/mailboxes/gmail/delete", "/api/mailboxes/probe",
	"/api/mailboxes/gmail/oauth/start",
	"/rules", "/templates", "/api/templates/preview", "/api/rules/test",
}

func TestRoutesRequireLogin(t *testing.T) {
	e := newEnv(t, nil)
	for _, path := range []string{"/", "/mailboxes", "/mailboxes/new", "/mailboxes/gmail", "/telegram", "/rules", "/templates", "/logs", "/settings", "/settings/versions", "/api/status", "/api/mailboxes/gmail/oauth/status"} {
		resp, _ := e.get(path)
		if resp.StatusCode != http.StatusSeeOther && resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without login = %d", path, resp.StatusCode)
		}
		if resp.StatusCode == http.StatusSeeOther && resp.Header.Get("Location") != "/login" {
			t.Errorf("GET %s redirects to %s", path, resp.Header.Get("Location"))
		}
	}
	// POST-only routes answer 401 without a session (and 403 without CSRF, see middleware_test)
	for _, path := range append([]string{"/logout", "/setup/totp"}, authedPosts...) {
		if resp, _ := e.postForm(path, nil); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("POST %s without login = %d", path, resp.StatusCode)
		}
	}
	// and are refused to a session still waiting for TOTP binding
	p := newEnvNoTOTP(t)
	p.postForm("/login", url.Values{"password": {testPassword}})
	_, page := p.get("/setup/totp")
	p.csrf = extractCSRF(page)
	for _, path := range []string{"/", "/logs", "/settings", "/settings/versions", "/telegram", "/mailboxes", "/mailboxes/new", "/rules", "/templates"} {
		if resp, _ := p.get(path); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/setup/totp" {
			t.Errorf("GET %s while pending = %d %s", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if resp, _ := p.get("/api/status"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET /api/status while pending = %d", resp.StatusCode)
	}
	for _, path := range authedPosts {
		if resp, _ := p.postForm(path, nil); resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s while pending = %d", path, resp.StatusCode)
		}
	}
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t, nil)
	resp, body := e.postForm("/login", url.Values{"password": {"wrong-password-123"}, "code": {e.code()}})
	if resp.StatusCode != 401 || !strings.Contains(body, "登入失敗") {
		t.Fatalf("bad password: %d", resp.StatusCode)
	}
	resp, body = e.postForm("/login", url.Values{"password": {testPassword}, "code": {"000000"}})
	if resp.StatusCode != 401 || !strings.Contains(body, "登入失敗") {
		t.Fatalf("bad code: %d", resp.StatusCode)
	}
	if !e.alertQueued("密碼正確但驗證碼錯誤") {
		t.Error("password-ok/code-bad must alert the admin")
	}
	e.login()
	if !e.notified("登入成功") {
		t.Error("successful login must notify")
	}
	resp, _ = e.get("/")
	if resp.StatusCode != 200 {
		t.Fatalf("after login GET / = %d", resp.StatusCode)
	}
}

func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	e := newEnv(t, nil)
	code := e.code()
	e.postForm("/login", url.Values{"password": {testPassword}, "code": {code}})
	e.logoutAndClearCookies()
	resp, _ := e.postForm("/login", url.Values{"password": {testPassword}, "code": {code}})
	if resp.StatusCode != 401 {
		t.Fatalf("replayed code = %d", resp.StatusCode)
	}
}

func TestLoginRateLimited(t *testing.T) {
	e := newEnv(t, nil)
	var last int
	for i := 0; i < 7; i++ {
		resp, _ := e.postForm("/login", url.Values{"password": {"x"}})
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("7th attempt = %d", last)
	}
}

func TestFirstLoginForcesTOTPSetup(t *testing.T) {
	e := newEnvNoTOTP(t) // admin with password only
	resp, _ := e.postForm("/login", url.Values{"password": {testPassword}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/setup/totp" {
		t.Fatalf("first login = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, _ = e.get("/")
	if resp.Header.Get("Location") != "/setup/totp" {
		t.Fatalf("pending session reached / : %d", resp.StatusCode)
	}
	_, page := e.get("/setup/totp")
	e.csrf = extractCSRF(page)
	a, _, _ := e.st.GetAdmin(context.Background())
	code, _ := websec.TOTPCode(a.TOTPPending, websec.TOTPStep(e.now))
	resp, _ = e.postForm("/setup/totp", url.Values{"code": {code}})
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Fatalf("confirm = %d", resp.StatusCode)
	}
	if a, _, _ = e.st.GetAdmin(context.Background()); a.TOTPSecret == "" {
		t.Fatal("TOTP not bound")
	}
}

func TestSessionRotatesOnLogin(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	first := e.sessionCookie()
	e.now = e.now.Add(time.Minute)
	e.login()
	if e.sessionCookie() == first {
		t.Fatal("session token not rotated")
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	e.now = e.now.Add(31 * time.Minute)
	resp, _ := e.get("/")
	if resp.StatusCode != 303 {
		t.Fatalf("idle session still valid: %d", resp.StatusCode)
	}
}

func TestSessionAbsoluteLifetime(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	for i := 0; i < 30; i++ { // active every 29 minutes: idle never expires it
		e.now = e.now.Add(29 * time.Minute)
		resp, _ := e.get("/")
		if resp.StatusCode == http.StatusSeeOther {
			if elapsed := time.Duration(i+1) * 29 * time.Minute; elapsed < 12*time.Hour {
				t.Fatalf("session ended after %v", elapsed)
			}
			return
		}
	}
	t.Fatal("session outlived 12 hours")
}

// TestLoginPageHidesBindingState: the login form looks the same whether or not TOTP is
// bound, so a visitor cannot tell.
func TestLoginPageHidesBindingState(t *testing.T) {
	e := newEnv(t, nil)
	n := newEnvNoTOTP(t)
	_, bound := e.get("/login")
	_, unbound := n.get("/login")
	if !strings.Contains(bound, `name="code"`) || bound != unbound {
		t.Error("login page differs between bound and unbound TOTP")
	}
	e.login()
	if resp, _ := e.get("/login"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Errorf("signed-in GET /login = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestFirstLoginNotifiesAndBindingEndsOtherSessions(t *testing.T) {
	e := newEnvNoTOTP(t)
	other := e.clone()
	// a password-only session elsewhere (the code field is sent but ignored while unbound)
	if resp, _ := other.postForm("/login", url.Values{"password": {testPassword}, "code": {""}}); resp.StatusCode != 303 {
		t.Fatalf("other first login = %d", resp.StatusCode)
	}
	if !e.notified("兩步驟驗證尚未綁定") {
		t.Error("first (password-only) login must notify")
	}
	e.postForm("/login", url.Values{"password": {testPassword}})
	_, page := e.get("/setup/totp")
	e.csrf = extractCSRF(page)
	a, _, _ := e.st.GetAdmin(context.Background())
	code, _ := websec.TOTPCode(a.TOTPPending, websec.TOTPStep(e.now))
	if resp, _ := e.postForm("/setup/totp", url.Values{"code": {code}}); resp.StatusCode != 303 {
		t.Fatalf("bind = %d", resp.StatusCode)
	}
	if resp, _ := other.get("/"); resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
		t.Fatalf("pending session elsewhere survived binding: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := e.get("/"); resp.StatusCode != 200 {
		t.Fatalf("binding session lost: %d", resp.StatusCode)
	}
}

func TestNormalizeCode(t *testing.T) {
	for in, want := range map[string]string{"123456": "123456", " 123 456 ": "123456", "123-456": "123456", "123\u00a0456": "123456"} {
		if got := normalizeCode(in); got != want {
			t.Errorf("normalizeCode(%q) = %q", in, got)
		}
	}
}

func TestLogoutEndsSession(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	resp, _ := e.postForm("/logout", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" || e.flash(resp) != "已登出" {
		t.Fatalf("logout = %d %s %q", resp.StatusCode, resp.Header.Get("Location"), e.flash(resp))
	}
	_, body := e.get("/login")
	if !strings.Contains(body, "已登出") {
		t.Error("flash not shown on the next page")
	}
	if _, body = e.get("/login"); strings.Contains(body, "已登出") {
		t.Error("flash shown twice")
	}
	if resp, _ := e.get("/"); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("GET / after logout = %d", resp.StatusCode)
	}
}

func TestTOTPSetupPageAndQR(t *testing.T) {
	e := newEnvNoTOTP(t)
	if resp, _ := e.get("/setup/totp.png"); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("QR without login = %d", resp.StatusCode)
	}
	e.postForm("/login", url.Values{"password": {testPassword}})
	_, page := e.get("/setup/totp")
	a, _, _ := e.st.GetAdmin(context.Background())
	if a.TOTPPending == "" || !strings.Contains(page, a.TOTPPending[:4]) || !strings.Contains(page, `src="/setup/totp.png"`) {
		t.Fatal("setup page must show the pending key and the QR code")
	}
	if _, again := e.get("/setup/totp"); !strings.Contains(again, a.TOTPPending[:4]) {
		t.Fatal("reloading the page must keep the same pending key")
	}
	if b, _, _ := e.st.GetAdmin(context.Background()); b.TOTPPending != a.TOTPPending {
		t.Fatal("pending key changed on reload")
	}
	resp, body := e.get("/setup/totp.png")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || !strings.HasPrefix(body, "\x89PNG") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("QR = %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
	}
	e.csrf = extractCSRF(page)
	resp, body = e.postForm("/setup/totp", url.Values{"code": {"123456"}})
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, "驗證碼不正確") {
		t.Fatalf("wrong code = %d", resp.StatusCode)
	}
	e.csrf = "bad"
	code, _ := websec.TOTPCode(a.TOTPPending, websec.TOTPStep(e.now))
	if resp, _ := e.postForm("/setup/totp", url.Values{"code": {code}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("setup without csrf = %d", resp.StatusCode)
	}
	e.csrf = extractCSRF(page)
	before := e.sessionCookie()
	resp, _ = e.postForm("/setup/totp", url.Values{"code": {code[:3] + " " + code[3:]}})
	if resp.StatusCode != http.StatusSeeOther || e.flash(resp) != "兩步驟驗證已啟用" {
		t.Fatalf("confirm = %d %q", resp.StatusCode, e.flash(resp))
	}
	if e.sessionCookie() == before {
		t.Error("session not renewed after binding")
	}
	if !e.notified("完成 TOTP 綁定") {
		t.Error("binding not notified")
	}
	if resp, _ := e.get("/"); resp.StatusCode != 200 {
		t.Fatalf("GET / after binding = %d", resp.StatusCode)
	}
	if resp, _ := e.get("/setup/totp"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings" {
		t.Fatalf("bound admin without a pending key: /setup/totp = %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := e.get("/setup/totp.png"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("QR without a pending key = %d", resp.StatusCode)
	}
}

func TestTOTPRebind(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	next := websec.NewTOTPSecret()
	if err := e.st.SetTOTPPending(context.Background(), next); err != nil { // as the settings page does
		t.Fatal(err)
	}
	_, page := e.get("/setup/totp")
	if !strings.Contains(page, "重新綁定") || !strings.Contains(page, next[:4]) {
		t.Fatal("rebind page")
	}
	e.now = e.now.Add(30 * time.Second)
	code, _ := websec.TOTPCode(next, websec.TOTPStep(e.now))
	e.csrf = extractCSRF(page)
	if resp, _ := e.postForm("/setup/totp", url.Values{"code": {code}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("rebind = %d", resp.StatusCode)
	}
	if a, _, _ := e.st.GetAdmin(context.Background()); a.TOTPSecret != next || a.TOTPPending != "" {
		t.Fatal("new secret not bound")
	}
	if !e.notified("重新綁定 TOTP") {
		t.Error("rebind not notified")
	}
}

func TestFailureSummaryAndNoSecretsInLogs(t *testing.T) {
	var logs syncBuf
	e := newEnv(t, nil, func(o *Options) { o.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	for i := 0; i < 11; i++ {
		e.now = e.now.Add(2 * time.Second) // keep the limiter from refusing
		if resp, _ := e.postForm("/login", url.Values{"password": {"SECRET-WRONG-PW"}, "code": {"987654"}}); resp.StatusCode != 401 {
			t.Fatalf("attempt %d = %d", i, resp.StatusCode)
		}
	}
	if !e.notified("過去一小時有 11 次登入失敗") {
		t.Error("failure summary not sent")
	}
	e.now = e.now.Add(2 * time.Second)
	e.postForm("/login", url.Values{"password": {"SECRET-WRONG-PW"}})
	// the outbox delivers each chat in order: once this marker arrives, any second summary
	// (queued synchronously by the request above) would have arrived too
	e.rt.Notify("marker")
	if !e.notified("marker") {
		t.Fatal("marker not delivered")
	}
	n := 0
	for _, m := range e.tg.messages() {
		if strings.Contains(m.Text, "次登入失敗") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("summaries = %d, want 1 per hour", n)
	}
	e.login()
	e.notified("登入成功")
	for _, secret := range []string{"SECRET-WRONG-PW", "987654", testPassword, e.secret} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("log contains %q", secret)
		}
		for _, m := range e.tg.messages() {
			if strings.Contains(m.Text, secret) {
				t.Errorf("notification contains %q", secret)
			}
		}
	}
}
