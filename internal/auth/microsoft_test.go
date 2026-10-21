package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/lxtsuddy/verify-code-relay/internal/store"
)

type memTokens struct {
	mu      sync.Mutex
	m       map[string]store.OAuthToken
	saveErr error // returned by SaveOAuthToken when set
}

func (s *memTokens) GetOAuthToken(_ context.Context, mb string) (store.OAuthToken, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[mb]
	return t, ok, nil
}

func (s *memTokens) SaveOAuthToken(_ context.Context, mb string, t store.OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.m[mb] = t
	return nil
}

func (s *memTokens) setSaveErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveErr = err
}

func (s *memTokens) get(mb string) store.OAuthToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[mb]
}

type tokenServer struct {
	*httptest.Server
	mu    sync.Mutex
	forms []url.Values
	reply func(form url.Values) (int, string)
}

// calls returns the number of requests the server has received.
func (ts *tokenServer) calls() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.forms)
}

// form returns the i-th request's form; a negative i counts from the end.
func (ts *tokenServer) form(i int) url.Values {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if i < 0 {
		i += len(ts.forms)
	}
	return ts.forms[i]
}

func newTokenServer(t *testing.T, reply func(url.Values) (int, string)) *tokenServer {
	ts := &tokenServer{reply: reply}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		r.PostForm.Set("_path", r.URL.Path)
		ts.mu.Lock()
		ts.forms = append(ts.forms, r.PostForm)
		ts.mu.Unlock()
		code, body := ts.reply(r.PostForm)
		if code == dropConn {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

// dropConn as a reply status makes the test server close the connection
// without answering, which the client sees as a network error.
const dropConn = -1

var now0 = time.Unix(1_800_000_000, 0)

// fakeClock replaces Microsoft's clock and its wait between device-code polls,
// so polling advances time instantly and the requested waits can be checked.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	slept []time.Duration
}

func useFakeClock(m *Microsoft) *fakeClock {
	c := &fakeClock{t: now0}
	m.Now = c.now
	m.sleep = c.sleep
	return c
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	c.slept = append(c.slept, d)
	return nil
}

func (c *fakeClock) waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.slept)
}

func newMS(ts *tokenServer, toks *memTokens) *Microsoft {
	return &Microsoft{Mailbox: "outlook", Username: "me@outlook.com", ClientID: "cid", Tenant: "consumers",
		Store: toks, HTTP: ts.Client(), Authority: ts.URL, Now: func() time.Time { return now0 },
		Log: slog.New(slog.DiscardHandler)}
}

func TestAccessTokenFreshNoRefresh(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 500, "" })
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0.Add(time.Hour)}}}
	tok, err := newMS(ts, toks).AccessToken(context.Background())
	if err != nil || tok != "a" || ts.calls() != 0 {
		t.Fatalf("tok=%q err=%v calls=%d", tok, err, ts.calls())
	}
}

func TestAccessTokenRefreshRotates(t *testing.T) {
	ts := newTokenServer(t, func(f url.Values) (int, string) {
		return 200, `{"access_token":"a2","refresh_token":"r2","expires_in":3600,"token_type":"Bearer"}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0.Add(4 * time.Minute)}}}
	tok, err := newMS(ts, toks).AccessToken(context.Background())
	if err != nil || tok != "a2" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	f := ts.form(0)
	if f.Get("_path") != "/consumers/oauth2/v2.0/token" || f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "r" ||
		f.Get("client_id") != "cid" || f.Get("scope") != Scope {
		t.Errorf("form = %v", f)
	}
	saved := toks.m["outlook"]
	if saved.RefreshToken != "r2" || saved.AccessToken != "a2" || !saved.ExpiresAt.Equal(now0.Add(time.Hour)) {
		t.Errorf("saved = %+v", saved)
	}
}

func TestAccessTokenRefreshKeepsOldRefreshTokenWhenNotRotated(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 200, `{"access_token":"a2","expires_in":3600}` })
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0}}}
	if _, err := newMS(ts, toks).AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if toks.m["outlook"].RefreshToken != "r" {
		t.Error("refresh token lost")
	}
}

func TestAccessTokenInvalidGrantNeedsLogin(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) {
		return 400, `{"error":"invalid_grant","error_description":"AADSTS70000: expired"}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", ExpiresAt: now0}}}
	_, err := newMS(ts, toks).AccessToken(context.Background())
	if !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("err = %v", err)
	}
}

func TestAccessTokenMissingNeedsLogin(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 500, "" })
	_, err := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}}).AccessToken(context.Background())
	if !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("err = %v", err)
	}
}

func TestAccessTokenServerErrorIsNotNeedsLogin(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 503, `{"error":"temporarily_unavailable"}` })
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", ExpiresAt: now0}}}
	_, err := newMS(ts, toks).AccessToken(context.Background())
	if err == nil || errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("want transient error, got %v", err)
	}
}

func TestAccessTokenInteractionRequiredNeedsLogin(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) {
		return 400, `{"error":"interaction_required","error_description":"AADSTS50076: MFA required"}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "SECRET-REFRESH", AccessToken: "SECRET-ACCESS", ExpiresAt: now0}}}
	_, err := newMS(ts, toks).AccessToken(context.Background())
	if !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("err = %v", err)
	}
	assertNoSecrets(t, err)
}

// A failed refresh does not stop the mailbox while the access token is still
// valid: it is used until it expires.
func TestAccessTokenRefreshFailureKeepsValidToken(t *testing.T) {
	for _, reply := range []string{"503", "drop"} {
		ts := newTokenServer(t, pollReplies(reply))
		toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0.Add(3 * time.Minute)}}}
		tok, err := newMS(ts, toks).AccessToken(context.Background())
		if err != nil || tok != "a" || ts.calls() != 1 {
			t.Errorf("%s: tok=%q err=%v calls=%d, want the still-valid token after one refresh attempt", reply, tok, err, ts.calls())
		}
	}
}

// Once the access token has expired, or when the grant itself is rejected,
// a failed refresh is an error.
func TestAccessTokenRefreshFailureWithoutValidToken(t *testing.T) {
	for _, tc := range []struct {
		reply   string
		expires time.Time
		login   bool
	}{
		{"503", now0, false},
		{"drop", now0.Add(-time.Minute), false},
		{`{"error":"invalid_grant"}`, now0.Add(3 * time.Minute), true},
	} {
		ts := newTokenServer(t, pollReplies(tc.reply))
		toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "SECRET-REFRESH", AccessToken: "SECRET-ACCESS", ExpiresAt: tc.expires}}}
		_, err := newMS(ts, toks).AccessToken(context.Background())
		if err == nil || errors.Is(err, ErrNeedsLogin) != tc.login {
			t.Errorf("%s: err = %v, want needs-login %v", tc.reply, err, tc.login)
		}
		assertNoSecrets(t, err)
	}
}

// rotatingTokens answers the n-th refresh with access token a<n+1> and
// refresh token r<n+1>.
func rotatingTokens() func(url.Values) (int, string) {
	var n atomic.Int32
	return func(url.Values) (int, string) {
		i := n.Add(1) + 1
		return 200, fmt.Sprintf(`{"access_token":"a%d","refresh_token":"r%d","expires_in":3600}`, i, i)
	}
}

// A rotated refresh token that cannot be saved stays in memory: it is used,
// saving is retried, and the next refresh sends it rather than the old one.
func TestAccessTokenSaveFailureKeepsRotatedToken(t *testing.T) {
	ts := newTokenServer(t, rotatingTokens())
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r1", AccessToken: "a1", ExpiresAt: now0}},
		saveErr: errors.New("disk I/O error")}
	m := newMS(ts, toks)
	clk := useFakeClock(m)
	ctx := context.Background()

	tok, err := m.AccessToken(ctx)
	if err != nil || tok != "a2" {
		t.Fatalf("tok=%q err=%v, want the refreshed token despite the save failure", tok, err)
	}
	// Still unsaved: the in-memory token is used without another refresh.
	if tok, err := m.AccessToken(ctx); err != nil || tok != "a2" || ts.calls() != 1 {
		t.Fatalf("tok=%q err=%v calls=%d", tok, err, ts.calls())
	}
	// After expiry the refresh must use the rotated refresh token.
	clk.sleep(ctx, time.Hour)
	if tok, err := m.AccessToken(ctx); err != nil || tok != "a3" || ts.form(-1).Get("refresh_token") != "r2" {
		t.Fatalf("tok=%q err=%v refresh_token=%q, want r2", tok, err, ts.form(-1).Get("refresh_token"))
	}
	// Once the store works again the pending token is saved.
	toks.setSaveErr(nil)
	if tok, err := m.AccessToken(ctx); err != nil || tok != "a3" || ts.calls() != 2 {
		t.Fatalf("tok=%q err=%v calls=%d", tok, err, ts.calls())
	}
	if got := toks.get("outlook"); got.RefreshToken != "r3" || got.AccessToken != "a3" {
		t.Errorf("saved = %+v, want r3/a3", got)
	}
}

// A rejected access token that is only in memory is expired there too.
func TestExpireUnsavedToken(t *testing.T) {
	ts := newTokenServer(t, rotatingTokens())
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r1", ExpiresAt: now0}},
		saveErr: errors.New("disk I/O error")}
	m := newMS(ts, toks)
	ctx := context.Background()
	if tok, err := m.AccessToken(ctx); err != nil || tok != "a2" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if err := m.expire(ctx, "a2"); err != nil {
		t.Fatal(err)
	}
	if tok, err := m.AccessToken(ctx); err != nil || tok != "a3" || ts.form(-1).Get("refresh_token") != "r2" {
		t.Fatalf("tok=%q err=%v refresh_token=%q, want a3 refreshed with r2", tok, err, ts.form(-1).Get("refresh_token"))
	}
}

// Folders of one mailbox share a *Microsoft: concurrent sessions must not
// refresh (and rotate) the same refresh token twice.
func TestAccessTokenConcurrentRefreshOnce(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) {
		time.Sleep(50 * time.Millisecond)
		return 200, `{"access_token":"a2","refresh_token":"r2","expires_in":3600}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", ExpiresAt: now0}}}
	m := newMS(ts, toks)
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if tok, err := m.AccessToken(context.Background()); err != nil || tok != "a2" {
				t.Errorf("tok=%q err=%v", tok, err)
			}
		})
	}
	wg.Wait()
	if ts.calls() != 1 {
		t.Errorf("calls = %d, want a single refresh", ts.calls())
	}
}

// A caller waiting for another's refresh gives up when its context ends.
func TestAccessTokenWaitHonoursContext(t *testing.T) {
	release := make(chan struct{})
	ts := newTokenServer(t, func(url.Values) (int, string) {
		<-release
		return 200, `{"access_token":"a2","refresh_token":"r2","expires_in":3600}`
	})
	defer close(release)
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", ExpiresAt: now0}}}
	m := newMS(ts, toks)
	go m.AccessToken(context.Background())
	for ts.calls() == 0 {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.AccessToken(ctx); !errors.Is(err, context.DeadlineExceeded) || ts.calls() != 1 {
		t.Fatalf("err = %v calls = %d, want context.DeadlineExceeded without a second refresh", err, ts.calls())
	}
}

func TestDeviceLogin(t *testing.T) {
	polls := 0
	ts := newTokenServer(t, func(f url.Values) (int, string) {
		if f.Get("_path") == "/consumers/oauth2/v2.0/devicecode" {
			return 200, `{"device_code":"DC","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":0,"message":"go to url"}`
		}
		polls++
		if polls < 3 {
			return 400, `{"error":"authorization_pending"}`
		}
		return 200, `{"access_token":"a","refresh_token":"r","expires_in":3600}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{}}
	m := newMS(ts, toks)
	clk := useFakeClock(m)
	dc, err := m.StartDeviceLogin(context.Background())
	if err != nil || dc.UserCode != "ABCD-EFGH" || dc.Message != "go to url" {
		t.Fatalf("dc=%+v err=%v", dc, err)
	}
	if ts.form(0).Get("scope") != Scope || ts.form(0).Get("client_id") != "cid" {
		t.Errorf("devicecode form = %v", ts.form(0))
	}
	if err := m.PollDeviceLogin(context.Background(), dc); err != nil {
		t.Fatal(err)
	}
	last := ts.form(-1)
	if last.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || last.Get("device_code") != "DC" {
		t.Errorf("poll form = %v", last)
	}
	if toks.m["outlook"].RefreshToken != "r" {
		t.Errorf("token not saved: %+v", toks.m)
	}
	// interval 0 must not turn into a busy loop: wait at least 5 seconds between polls.
	if got, want := clk.waits(), []time.Duration{5 * time.Second, 5 * time.Second}; !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}
}

func TestDeviceLoginDeclined(t *testing.T) {
	ts := newTokenServer(t, func(f url.Values) (int, string) { return 400, `{"error":"authorization_declined"}` })
	m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
	err := m.PollDeviceLogin(context.Background(), &DeviceCode{DeviceCode: "DC", ExpiresIn: 900})
	if err == nil {
		t.Fatal("want error")
	}
}

// pollReplies answers requests with the given bodies in order, repeating the
// last one. "drop" closes the connection; a "503" prefix answers HTTP 503 with
// the rest as body; a body with an access token is HTTP 200, any other 400.
func pollReplies(replies ...string) func(url.Values) (int, string) {
	var mu sync.Mutex
	n := 0
	return func(url.Values) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		r := replies[min(n, len(replies)-1)]
		n++
		switch {
		case r == "drop":
			return dropConn, ""
		case strings.HasPrefix(r, "503"):
			return 503, strings.TrimPrefix(r, "503")
		case strings.Contains(r, "access_token"):
			return 200, r
		}
		return 400, r
	}
}

const (
	pending  = `{"error":"authorization_pending"}`
	slowDown = `{"error":"slow_down"}`
	granted  = `{"access_token":"SECRET-ACCESS","refresh_token":"SECRET-REFRESH","expires_in":3600}`
)

// newSecretDC returns a device code whose value must never appear in an error.
func newSecretDC(expiresIn int) *DeviceCode {
	return &DeviceCode{DeviceCode: "SECRET-DC", ExpiresIn: expiresIn, Interval: 5}
}

// assertNoSecrets fails if err's text contains any token used by these tests;
// auth errors end up in logs and in Telegram alerts.
func assertNoSecrets(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range []string{"SECRET-DC", "SECRET-ACCESS", "SECRET-REFRESH"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error leaks %s: %v", s, err)
		}
	}
}

func TestDeviceLoginSlowDown(t *testing.T) {
	ts := newTokenServer(t, pollReplies(pending, slowDown, pending, granted))
	m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
	clk := useFakeClock(m)
	if err := m.PollDeviceLogin(context.Background(), newSecretDC(900)); err != nil {
		t.Fatal(err)
	}
	if got, want := clk.waits(), []time.Duration{5 * time.Second, 10 * time.Second, 10 * time.Second}; !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}
}

func TestDeviceLoginTimeout(t *testing.T) {
	ts := newTokenServer(t, pollReplies(pending))
	m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
	useFakeClock(m)
	err := m.PollDeviceLogin(context.Background(), newSecretDC(12))
	if err == nil || !strings.Contains(err.Error(), "逾時") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	assertNoSecrets(t, err)
	// Polls at 0s, 5s, 10s and 15s; the code expired at 12s.
	if ts.calls() != 4 {
		t.Errorf("calls = %d, want 4", ts.calls())
	}
}

// Server errors and dropped connections are transient: polling goes on.
func TestDeviceLoginRetriesTransientErrors(t *testing.T) {
	ts := newTokenServer(t, pollReplies(pending, "503", "drop", `503{"error":"temporarily_unavailable"}`, granted))
	toks := &memTokens{m: map[string]store.OAuthToken{}}
	m := newMS(ts, toks)
	clk := useFakeClock(m)
	if err := m.PollDeviceLogin(context.Background(), newSecretDC(900)); err != nil {
		t.Fatal(err)
	}
	if ts.calls() != 5 || len(clk.waits()) != 4 {
		t.Errorf("calls = %d waits = %v", ts.calls(), clk.waits())
	}
	if toks.m["outlook"].RefreshToken != "SECRET-REFRESH" {
		t.Errorf("token not saved: %+v", toks.m)
	}
}

// When the code expires while the server keeps failing, the error says why.
func TestDeviceLoginTimeoutReportsLastTransientError(t *testing.T) {
	for _, reply := range []string{"503", "drop"} {
		ts := newTokenServer(t, pollReplies(reply))
		m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
		useFakeClock(m)
		err := m.PollDeviceLogin(context.Background(), newSecretDC(12))
		if err == nil || !strings.Contains(err.Error(), "逾時") {
			t.Fatalf("%s: err = %v, want a timeout", reply, err)
		}
		if reply == "503" && !strings.Contains(err.Error(), "HTTP 503") {
			t.Errorf("err = %v, want it to name HTTP 503", err)
		}
		if reply == "drop" && !strings.Contains(err.Error(), "EOF") {
			t.Errorf("err = %v, want it to include the network error", err)
		}
		assertNoSecrets(t, err)
	}
}

func TestDeviceLoginUnexpectedResponseNamesStatus(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
	}{{404, "<html>not found</html>"}, {200, `{}`}, {400, `{"error":"expired_token","error_description":"code expired"}`}} {
		ts := newTokenServer(t, func(url.Values) (int, string) { return tc.code, tc.body })
		m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
		useFakeClock(m)
		err := m.PollDeviceLogin(context.Background(), newSecretDC(900))
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", tc.code)) {
			t.Errorf("%d %s: err = %v, want it to name the HTTP status", tc.code, tc.body, err)
		}
		assertNoSecrets(t, err)
		if ts.calls() != 1 {
			t.Errorf("%d %s: calls = %d, want no retry", tc.code, tc.body, ts.calls())
		}
	}
}

// Cancelling the context (Ctrl-C during relay login) stops polling at once,
// also while waiting between polls on the real timer.
func TestDeviceLoginContextCancel(t *testing.T) {
	ts := newTokenServer(t, pollReplies(pending))
	m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	err := m.PollDeviceLogin(ctx, newSecretDC(900))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("returned after %v", d)
	}
}

// A context cancelled while a request is in flight is not retried as a network error.
func TestDeviceLoginContextCancelDuringRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ts := newTokenServer(t, func(url.Values) (int, string) { cancel(); return dropConn, "" })
	m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
	useFakeClock(m)
	err := m.PollDeviceLogin(ctx, newSecretDC(900))
	if !errors.Is(err, context.Canceled) || ts.calls() != 1 {
		t.Fatalf("err = %v calls = %d, want context.Canceled after one request", err, ts.calls())
	}
}

// A cached access token that the IMAP server rejects (e.g. revoked) must not be
// reused until its expiry: the next session refreshes it.
func TestAuthenticateRejectedCachedTokenIsRefreshedNextTime(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) {
		return 200, `{"access_token":"a2","refresh_token":"r2","expires_in":3600}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0.Add(time.Hour)}}}
	m := newMS(ts, toks)
	c, err := imapclient.DialInsecure(memServer(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = m.Authenticate(context.Background(), c)
	var ie *imap.Error
	if !errors.As(err, &ie) {
		t.Fatalf("want *imap.Error from the server, got %T %v", err, err)
	}
	if ts.calls() != 0 {
		t.Fatalf("token endpoint called %d times; the cached token should have been tried first", ts.calls())
	}
	if toks.m["outlook"].RefreshToken != "r" {
		t.Fatalf("refresh token lost: %+v", toks.m["outlook"])
	}
	tok, err := m.AccessToken(context.Background())
	if err != nil || tok != "a2" {
		t.Fatalf("tok=%q err=%v, want a refreshed token", tok, err)
	}
	if ts.calls() != 1 || ts.form(0).Get("grant_type") != "refresh_token" || ts.form(0).Get("refresh_token") != "r" {
		t.Fatalf("calls = %d, want one refresh_token request", ts.calls())
	}
}

// PollInterval overrides the server's interval and the 5-second floor (web tests use it).
func TestDeviceLoginPollIntervalOverride(t *testing.T) {
	ts := newTokenServer(t, pollReplies(pending, slowDown, granted))
	m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
	clk := useFakeClock(m)
	m.PollInterval = 10 * time.Millisecond
	if err := m.PollDeviceLogin(context.Background(), newSecretDC(900)); err != nil {
		t.Fatal(err)
	}
	if got, want := clk.waits(), []time.Duration{10 * time.Millisecond, 5*time.Second + 10*time.Millisecond}; !slices.Equal(got, want) {
		t.Errorf("waits = %v, want %v", got, want)
	}
}

// Expiry and a declined sign-in are recognisable, so the web page can explain them.
func TestDeviceLoginErrorKinds(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  error
	}{
		{`{"error":"expired_token","error_description":"code expired"}`, ErrDeviceCodeExpired},
		{`{"error":"authorization_declined"}`, ErrLoginDeclined},
	} {
		ts := newTokenServer(t, pollReplies(tc.reply))
		m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
		useFakeClock(m)
		err := m.PollDeviceLogin(context.Background(), newSecretDC(900))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.reply, err, tc.want)
		}
		assertNoSecrets(t, err)
	}
	ts := newTokenServer(t, pollReplies(pending))
	m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
	useFakeClock(m)
	if err := m.PollDeviceLogin(context.Background(), newSecretDC(12)); !errors.Is(err, ErrDeviceCodeExpired) || !strings.Contains(err.Error(), "relay login") {
		t.Errorf("timeout err = %v", err)
	}
}
