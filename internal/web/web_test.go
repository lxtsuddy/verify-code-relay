package web

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/telegram"
	"verify-code-relay/internal/watcher"
	"verify-code-relay/internal/websec"

	"github.com/emersion/go-imap/v2/imapclient"
)

// Shared test harness for every web handler test (Tasks 6–12).

const testPassword = "a-long-enough-password"

var (
	testHashOnce sync.Once
	testHash     string
)

// testPasswordHash hashes testPassword once per test binary: argon2 under -race is slow.
func testPasswordHash(t *testing.T) string {
	t.Helper()
	testHashOnce.Do(func() {
		h, err := websec.HashPassword(testPassword)
		if err != nil {
			panic(err)
		}
		testHash = h
	})
	return testHash
}

// testClock is the server's clock. Tests move env.now; every request publishes env.now
// here first, so handlers never read the test's field directly (no data race).
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// sentMessage is one sendMessage call received by the fake Telegram.
type sentMessage struct {
	Token  string
	ChatID int64
	Text   string
}

// fakeTelegram answers sendMessage (recorded), getMe and getUpdates (configurable).
type fakeTelegram struct {
	*httptest.Server
	mu      sync.Mutex
	sent    []sentMessage
	me      fakeReply
	updates fakeReply
}

type fakeReply struct {
	status int
	body   string
}

var tgPathRe = regexp.MustCompile(`^/bot([^/]+)/([A-Za-z]+)$`)

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{
		me:      fakeReply{200, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Relay","username":"relay_test_bot"}}`},
		updates: fakeReply{200, `{"ok":true,"result":[]}`},
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTelegram) serve(w http.ResponseWriter, r *http.Request) {
	m := tgPathRe.FindStringSubmatch(r.URL.Path)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch m[2] {
	case "sendMessage":
		var p struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: bad json"}`)
			return
		}
		f.sent = append(f.sent, sentMessage{Token: m[1], ChatID: p.ChatID, Text: p.Text})
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	case "getMe":
		w.WriteHeader(f.me.status)
		io.WriteString(w, f.me.body)
	case "getUpdates":
		w.WriteHeader(f.updates.status)
		io.WriteString(w, f.updates.body)
	default:
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
	}
}

// setGetMe and setUpdates change what getMe / getUpdates answer.
func (f *fakeTelegram) setGetMe(status int, body string) {
	f.mu.Lock()
	f.me = fakeReply{status, body}
	f.mu.Unlock()
}

func (f *fakeTelegram) setUpdates(status int, body string) {
	f.mu.Lock()
	f.updates = fakeReply{status, body}
	f.mu.Unlock()
}

// messages returns everything sent so far.
func (f *fakeTelegram) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

// blockedDial never connects until ctx is cancelled, so watchers stay "connecting".
func blockedDial(string, int) watcher.DialFunc {
	return func(ctx context.Context, _ *imapclient.Options) (*imapclient.Client, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

type env struct {
	t      *testing.T
	st     *store.Store
	rt     *runtime.Runtime
	srv    *Server
	ts     *httptest.Server // TLS
	c      *http.Client     // with cookie jar
	csrf   string
	now    time.Time // the server's clock; see root()
	tg     *fakeTelegram
	secret string    // TOTP secret of the test admin ("" for newEnvNoTOTP)
	imap   *imapDial // newEnvWithIMAP's in-memory IMAP server (nil otherwise)

	clock *testClock
	base  *env // set on clones: the env that owns the clock
}

// newEnv starts a TLS test server with an admin (password testPassword, TOTP bound) and a
// stored config. The base config always has bot "main" (token "1:SECRET-TOKEN"), target
// "me" (chat 42) and admin "me", so notifications and alerts reach the fake Telegram;
// cfg (may be nil) modifies it further, e.g. withSample adds mailbox "gmail".
// opts (optional) adjust web.Options before the server is created.
func newEnv(t *testing.T, cfg func(c *config.Config), opts ...func(o *Options)) *env {
	return startEnv(t, true, cfg, opts...)
}

// newEnvNoTOTP is newEnv with an admin that has a password but no TOTP bound yet.
func newEnvNoTOTP(t *testing.T) *env {
	return startEnv(t, false, nil)
}

func startEnv(t *testing.T, bindTOTP bool, cfg func(c *config.Config), opts ...func(o *Options)) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) // registered first: runs after rt.Stop
	if _, err := st.CreateAdminIfMissing(ctx, testPasswordHash(t)); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, now: time.Now().Truncate(time.Second), clock: &testClock{}}
	e.clock.set(e.now)
	if bindTOTP {
		e.secret = websec.NewTOTPSecret()
		// last step far in the past so the first login's code is accepted
		if err := st.ConfirmTOTP(ctx, e.secret, 0); err != nil {
			t.Fatal(err)
		}
	}

	e.tg = newFakeTelegram(t)
	tc := &telegram.Client{BaseURL: e.tg.URL}
	log := slog.New(slog.DiscardHandler)
	e.rt = runtime.New(runtime.Options{Store: st, Log: log, Level: new(slog.LevelVar), Sender: tc, Dial: blockedDial})
	t.Cleanup(e.rt.Stop)

	c := config.Empty()
	c.Bots["main"] = config.Bot{Token: "1:SECRET-TOKEN"}
	c.Targets["me"] = config.Target{Bot: "main", ChatID: 42}
	c.Admin = "me"
	if cfg != nil {
		cfg(c)
	}
	b, _, err := runtime.SaveConfig(ctx, st, c, runtime.Meta{Source: "test", Summary: "測試設定"}, e.now)
	if err != nil {
		t.Fatalf("test config invalid: %v", err)
	}
	e.rt.Apply(b)

	o := Options{Store: st, Runtime: e.rt, Log: log, Telegram: tc, Dial: blockedDial, Now: e.clock.now}
	for _, f := range opts {
		f(&o)
	}
	e.srv = New(o)
	t.Cleanup(e.srv.Close) // runs before rt.Stop and st.Close
	e.ts = httptest.NewTLSServer(e.srv.Handler())
	t.Cleanup(e.ts.Close)
	e.c = e.newClient()
	return e
}

func (e *env) newClient() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return &http.Client{
		Transport:     e.ts.Client().Transport,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       30 * time.Second,
	}
}

// root returns the env that owns the clock (itself unless this is a clone).
func (e *env) root() *env {
	if e.base != nil {
		return e.base
	}
	return e
}

// clone returns a second client (own cookie jar, logged out) on the same server, sharing
// the clock: clones read and advance the original env's now.
func (e *env) clone() *env {
	r := e.root()
	c := &env{t: r.t, st: r.st, rt: r.rt, srv: r.srv, ts: r.ts, tg: r.tg, secret: r.secret, clock: r.clock, base: r}
	c.c = c.newClient()
	return c
}

// code returns the TOTP code for the current (root) time.
func (e *env) code() string {
	e.t.Helper()
	code, err := websec.TOTPCode(e.root().secret, websec.TOTPStep(e.root().now))
	if err != nil {
		e.t.Fatal(err)
	}
	return code
}

func (e *env) do(req *http.Request) (*http.Response, string) {
	e.t.Helper()
	e.clock.set(e.root().now)
	if req.Header.Get("Origin") == "" {
		req.Header.Set("Origin", e.ts.URL)
	}
	resp, err := e.c.Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp, string(b)
}

// get sends a GET with Origin set to the server URL.
func (e *env) get(path string) (*http.Response, string) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	return e.do(req)
}

// postForm posts form (may be nil) plus the recorded csrf token.
func (e *env) postForm(path string, form url.Values) (*http.Response, string) {
	e.t.Helper()
	f := url.Values{}
	for k, v := range form {
		f[k] = append([]string(nil), v...)
	}
	if e.csrf != "" {
		f.Set("csrf", e.csrf)
	}
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, strings.NewReader(f.Encode()))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req)
}

// postJSON posts body (nil = empty object) as JSON with the X-CSRF-Token header.
func (e *env) postJSON(path string, body any) (*http.Response, string) {
	e.t.Helper()
	if body == nil {
		body = map[string]any{}
	}
	b, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, bytes.NewReader(b))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	return e.do(req)
}

// login logs in with password and the current TOTP code and records the CSRF token.
// It first moves the shared clock 30 seconds on, so the code is never a replay and the
// login limiter has refilled.
func (e *env) login() {
	e.t.Helper()
	r := e.root()
	r.now = r.now.Add(30 * time.Second)
	resp, body := e.postForm("/login", url.Values{"password": {testPassword}, "code": {e.code()}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		e.t.Fatalf("login = %d %s\n%s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	resp, body = e.get("/")
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("GET / after login = %d", resp.StatusCode)
	}
	if e.csrf = extractCSRF(body); e.csrf == "" {
		e.t.Fatal("no csrf token on the page after login")
	}
}

var csrfRe = regexp.MustCompile(`<meta name="csrf-token" content="([^"]*)">`)

// extractCSRF returns the token from the page's <meta name="csrf-token">.
func extractCSRF(page string) string {
	if m := csrfRe.FindStringSubmatch(page); m != nil {
		return html.UnescapeString(m[1])
	}
	return ""
}

// sessionCookie returns the session cookie value in the client's jar ("" if none).
func (e *env) sessionCookie() string {
	u, _ := url.Parse(e.ts.URL)
	for _, c := range e.c.Jar.Cookies(u) {
		if c.Name == sessionCookie {
			return c.Value
		}
	}
	return ""
}

// logoutAndClearCookies logs out the current session (if any) and empties the cookie jar.
func (e *env) logoutAndClearCookies() {
	e.t.Helper()
	if e.sessionCookie() != "" {
		resp, body := e.get("/")
		if resp.StatusCode == http.StatusSeeOther && resp.Header.Get("Location") == "/setup/totp" {
			_, body = e.get("/setup/totp")
		}
		if tok := extractCSRF(body); tok != "" {
			e.csrf = tok
			e.postForm("/logout", nil)
		}
	}
	e.csrf = ""
	e.c = e.newClient()
}

// flash returns the text of the flash message set by resp ("" if none).
func (e *env) flash(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == flashCookie && c.MaxAge >= 0 && c.Value != "" {
			if f := decodeFlash(c.Value); f != nil {
				return f.Text
			}
		}
	}
	return ""
}

// waitSent polls the fake Telegram (up to 5 s) for a message containing substr.
func (e *env) waitSent(substr string) bool {
	return e.waitMessage(func(text string) bool { return strings.Contains(text, substr) })
}

// notified reports whether a notification (Runtime.Notify, "🔔 ") containing substr
// reached the admin target within 5 s.
func (e *env) notified(substr string) bool {
	return e.waitMessage(func(text string) bool {
		return strings.HasPrefix(text, "🔔") && strings.Contains(text, substr)
	})
}

// alertQueued reports whether an alert (Runtime.Alert, "⚠️ ") containing substr reached the
// admin target within 5 s.
func (e *env) alertQueued(substr string) bool {
	return e.waitMessage(func(text string) bool {
		return strings.HasPrefix(text, "⚠️") && strings.Contains(text, substr)
	})
}

func (e *env) waitMessage(match func(string) bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, m := range e.tg.messages() {
			if match(html.UnescapeString(m.Text)) {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Config modifiers for newEnv.

// withSample adds mailbox "gmail" (password "p", folder INBOX, target me).
func withSample(c *config.Config) {
	c.Mailboxes["gmail"] = &config.Mailbox{
		DisplayName: "個人 Gmail", Host: "imap.gmail.com", Username: "me@gmail.com",
		Auth:    config.Auth{Type: "password", Password: "p"},
		Folders: []string{"INBOX"}, Targets: []string{"me"},
	}
}

// withOutlookMailbox adds mailbox "outlook" using Microsoft OAuth (not yet authorised).
func withOutlookMailbox(c *config.Config) {
	c.Mailboxes["outlook"] = &config.Mailbox{
		DisplayName: "Outlook", Host: "outlook.office365.com", Username: "me@outlook.com",
		Auth:    config.Auth{Type: "microsoft_oauth", ClientID: "00000000-0000-0000-0000-000000000001", Tenant: "consumers"},
		Folders: []string{"INBOX"}, Targets: []string{"me"},
	}
}

// withSecrets is withSample plus withOutlookMailbox, with recognisable secrets: bot token
// "1:SECRET-TOKEN-XYZ" and gmail password "SECRET-PW-XYZ".
func withSecrets(c *config.Config) {
	withSample(c)
	withOutlookMailbox(c)
	c.Bots["main"] = config.Bot{Token: "1:SECRET-TOKEN-XYZ"}
	c.Mailboxes["gmail"].Auth.Password = "SECRET-PW-XYZ"
}

func TestHarnessHelpers(t *testing.T) {
	e := newEnv(t, withSecrets)
	if n := len(e.rt.Snapshot().Watchers); n != 2 {
		t.Fatalf("watchers = %d, want gmail and outlook", n)
	}
	other := e.clone()
	other.login()
	if other.sessionCookie() == "" || e.sessionCookie() != "" {
		t.Fatal("clone must have its own cookie jar")
	}
	if e.root().now != other.root().now {
		t.Fatal("clone must share the clock")
	}
	e.rt.Notify("harness ping")
	if !e.waitSent("harness ping") || !e.notified("harness ping") {
		t.Fatal("fake Telegram did not receive the notification")
	}
	if e.alertQueued("harness ping") {
		t.Fatal("a notification is not an alert")
	}
	other.logoutAndClearCookies()
	if other.sessionCookie() != "" {
		t.Fatal("cookies not cleared")
	}
}

// syncBuf is a bytes.Buffer safe for concurrent writes (a test log sink).
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
