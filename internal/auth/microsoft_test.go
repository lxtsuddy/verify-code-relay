package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/store"
)

type memTokens struct {
	mu sync.Mutex
	m  map[string]store.OAuthToken
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
	s.m[mb] = t
	return nil
}

type tokenServer struct {
	*httptest.Server
	forms []url.Values
	reply func(form url.Values) (int, string)
}

func newTokenServer(t *testing.T, reply func(url.Values) (int, string)) *tokenServer {
	ts := &tokenServer{reply: reply}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		r.PostForm.Set("_path", r.URL.Path)
		ts.forms = append(ts.forms, r.PostForm)
		code, body := ts.reply(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

var now0 = time.Unix(1_800_000_000, 0)

func newMS(ts *tokenServer, toks *memTokens) *Microsoft {
	return &Microsoft{Mailbox: "outlook", Username: "me@outlook.com", ClientID: "cid", Tenant: "consumers",
		Store: toks, HTTP: ts.Client(), Authority: ts.URL, Now: func() time.Time { return now0 }}
}

func TestAccessTokenFreshNoRefresh(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 500, "" })
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0.Add(time.Hour)}}}
	tok, err := newMS(ts, toks).AccessToken(context.Background())
	if err != nil || tok != "a" || len(ts.forms) != 0 {
		t.Fatalf("tok=%q err=%v calls=%d", tok, err, len(ts.forms))
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
	f := ts.forms[0]
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
	dc, err := m.StartDeviceLogin(context.Background())
	if err != nil || dc.UserCode != "ABCD-EFGH" || dc.Message != "go to url" {
		t.Fatalf("dc=%+v err=%v", dc, err)
	}
	if ts.forms[0].Get("scope") != Scope || ts.forms[0].Get("client_id") != "cid" {
		t.Errorf("devicecode form = %v", ts.forms[0])
	}
	if err := m.PollDeviceLogin(context.Background(), dc); err != nil {
		t.Fatal(err)
	}
	last := ts.forms[len(ts.forms)-1]
	if last.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || last.Get("device_code") != "DC" {
		t.Errorf("poll form = %v", last)
	}
	if toks.m["outlook"].RefreshToken != "r" {
		t.Errorf("token not saved: %+v", toks.m)
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
	if len(ts.forms) != 0 {
		t.Fatalf("token endpoint called %d times; the cached token should have been tried first", len(ts.forms))
	}
	if toks.m["outlook"].RefreshToken != "r" {
		t.Fatalf("refresh token lost: %+v", toks.m["outlook"])
	}
	tok, err := m.AccessToken(context.Background())
	if err != nil || tok != "a2" {
		t.Fatalf("tok=%q err=%v, want a refreshed token", tok, err)
	}
	if len(ts.forms) != 1 || ts.forms[0].Get("grant_type") != "refresh_token" || ts.forms[0].Get("refresh_token") != "r" {
		t.Fatalf("forms = %v", ts.forms)
	}
}
