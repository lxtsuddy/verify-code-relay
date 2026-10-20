package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/telegram"
	"verify-code-relay/internal/web"
	"verify-code-relay/internal/websec"
)

// webClient drives the admin over HTTP the way a browser does: cookies, the CSRF token
// from <meta name="csrf-token"> and an Origin header on every request.
type webClient struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

var e2eCSRFRe = regexp.MustCompile(`<meta name="csrf-token" content="([^"]*)">`)

func (w *webClient) do(method, path, contentType string, body io.Reader) (*http.Response, string) {
	w.t.Helper()
	req, err := http.NewRequest(method, w.base+path, body)
	if err != nil {
		w.t.Fatal(err)
	}
	req.Header.Set("Origin", w.base)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := w.c.Do(req)
	if err != nil {
		w.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	return resp, string(b)
}

// get fetches path and remembers the page's CSRF token, if it has one.
func (w *webClient) get(path string) (*http.Response, string) {
	w.t.Helper()
	resp, body := w.do(http.MethodGet, path, "", nil)
	if m := e2eCSRFRe.FindStringSubmatch(body); m != nil {
		w.csrf = html.UnescapeString(m[1])
	}
	return resp, body
}

func (w *webClient) post(path string, form url.Values) (*http.Response, string) {
	w.t.Helper()
	f := url.Values{}
	for k, v := range form {
		f[k] = v
	}
	if w.csrf != "" {
		f.Set("csrf", w.csrf)
	}
	return w.do(http.MethodPost, path, "application/x-www-form-urlencoded", strings.NewReader(f.Encode()))
}

// mustRedirect posts form and fails unless the answer is 303 to location.
func (w *webClient) mustRedirect(path string, form url.Values, location string) {
	w.t.Helper()
	resp, body := w.post(path, form)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != location {
		w.t.Fatalf("POST %s = %d → %q, want 303 → %q\n%s", path, resp.StatusCode, resp.Header.Get("Location"), location, body)
	}
}

// apiStatus is the part of /api/status the test reads.
type apiStatus struct {
	Pending  int `json:"pending"` // messages still in the outbox
	Watchers []struct {
		Mailbox string `json:"mailbox"`
		Status  string `json:"status"`
	} `json:"watchers"`
}

func (w *webClient) status() apiStatus {
	w.t.Helper()
	resp, body := w.do(http.MethodGet, "/api/status", "", nil)
	if resp.StatusCode != http.StatusOK {
		w.t.Fatalf("GET /api/status = %d\n%s", resp.StatusCode, body)
	}
	var st apiStatus
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		w.t.Fatalf("/api/status: %v\n%s", err, body)
	}
	return st
}

// watcherStatus returns the status of mailbox's watcher from /api/status ("" if absent).
func (w *webClient) watcherStatus(mailbox string) string {
	w.t.Helper()
	for _, ws := range w.status().Watchers {
		if ws.Mailbox == mailbox {
			return ws.Status
		}
	}
	return ""
}

// verificationMail is a minimal message carrying code.
func verificationMail(subject, code string) []byte {
	return []byte(fmt.Sprintf("From: \"Example\" <noreply@example.com>\nTo: %s\nSubject: %s\nDate: %s\nMessage-ID: <%s@example.com>\nMIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\nYour verification code is %s\n",
		memUser, subject, time.Now().Format(time.RFC1123Z), code, code))
}

// TestWebEndToEnd starts from an empty database, signs in through the web admin, binds
// TOTP, creates a bot, a target and a mailbox with forms, receives a forwarded code,
// then re-routes the mailbox and checks the next code follows the new route.
func TestWebEndToEnd(t *testing.T) {
	ctx := context.Background()
	imapSrv := startMemIMAP(t)
	tg := startFakeTelegram(t)

	// 1. assemble the service the way `relay run` does
	const password = "e2e-admin-password"
	t.Setenv("RELAY_ADMIN_PASSWORD", password)
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) // registered first: runs last
	log := slog.New(slog.DiscardHandler)
	if err := ensureAdmin(ctx, st, log); err != nil {
		t.Fatal(err)
	}
	tc := &telegram.Client{BaseURL: tg.URL, HTTP: tg.Client()}
	dial := imapSrv.dial(t)
	rt := runtime.New(runtime.Options{Store: st, Log: log, Level: new(slog.LevelVar), Sender: tc, Dial: dial})
	t.Cleanup(rt.Stop)
	b, err := runtime.Build(config.Empty())
	if err != nil {
		t.Fatal(err)
	}
	rt.Apply(b)
	admin := web.New(web.Options{Store: st, Runtime: rt, Log: log, Telegram: tc, Dial: dial})
	t.Cleanup(admin.Close)
	ts := httptest.NewTLSServer(admin.Handler())
	t.Cleanup(ts.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	w := &webClient{t: t, base: ts.URL, c: &http.Client{
		Transport:     ts.Client().Transport,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       30 * time.Second,
	}}

	// 2. first login is password only, then TOTP binding is forced
	if resp, _ := w.get("/"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("GET / signed out = %d → %q, want 303 → /login", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := w.get("/login"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d", resp.StatusCode)
	}
	w.mustRedirect("/login", url.Values{"password": {password}}, "/setup/totp")
	if resp, _ := w.get("/"); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/setup/totp" {
		t.Fatalf("GET / before binding = %d → %q, want 303 → /setup/totp", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := w.get("/setup/totp"); resp.StatusCode != http.StatusOK || w.csrf == "" {
		t.Fatalf("GET /setup/totp = %d (csrf %q)\n%s", resp.StatusCode, w.csrf, body)
	}
	a, ok, err := st.GetAdmin(ctx)
	if err != nil || !ok || a.TOTPPending == "" {
		t.Fatalf("admin after visiting the binding page: ok=%v pending=%q err=%v", ok, a.TOTPPending, err)
	}
	code, err := websec.TOTPCode(a.TOTPPending, websec.TOTPStep(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	w.mustRedirect("/setup/totp", url.Values{"code": {code}}, "/")
	// binding issued a new session (and CSRF token)
	if resp, body := w.get("/"); resp.StatusCode != http.StatusOK || w.csrf == "" {
		t.Fatalf("GET / after binding = %d\n%s", resp.StatusCode, body)
	}

	// 3. bot, target and mailbox through the forms
	w.mustRedirect("/telegram/bots", url.Values{"name": {"main"}, "token": {"1:abc"}}, "/telegram")
	w.mustRedirect("/telegram/targets", url.Values{"name": {"me"}, "bot": {"main"}, "chat_id": {"42"}}, "/telegram")
	if resp, _ := w.get("/mailboxes/new"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /mailboxes/new = %d", resp.StatusCode)
	}
	mailboxForm := func(targets ...string) url.Values {
		return url.Values{
			"display_name": {"記憶體信箱"},
			"host":         {"127.0.0.1"},
			"port":         {strconv.Itoa(imapSrv.addr.Port)},
			"username":     {memUser},
			"auth_type":    {"password"},
			"folders":      {"INBOX"},
			"targets":      targets,
		}
	}
	f := mailboxForm("me")
	f.Set("name", "mem")
	f.Set("password", memPass)
	dials := imapSrv.dials()
	w.mustRedirect("/mailboxes/new", f, "/mailboxes")
	if resp, body := w.get("/mailboxes"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "記憶體信箱") {
		t.Fatalf("GET /mailboxes = %d, mailbox missing\n%s", resp.StatusCode, body)
	}

	// 4. the watcher connects and the first code reaches chat 42
	// imapmemserver announces new mail only to a client that is already idling
	waitIdle := func(what string, dials int) {
		t.Helper()
		waitFor(t, what, func() bool { return w.watcherStatus("mem") == "connected" && imapSrv.idlingSince(dials) })
	}
	waitIdle("watcher mem connected and idling", dials)
	imapSrv.appendMail(t, verificationMail("Your code", "482913"))
	withCode := func(code string) []sentMessage {
		var out []sentMessage
		for _, m := range tg.messages() {
			if strings.Contains(m.Text, code) {
				out = append(out, m)
			}
		}
		return out
	}
	waitFor(t, "first code forwarded", func() bool { return len(withCode("482913")) >= 1 })

	// 5. a second target, and the mailbox re-routed to it only
	// (every save restarts the watchers: wait for the new one each time)
	w.get("/telegram")
	dials = imapSrv.dials()
	w.mustRedirect("/telegram/targets", url.Values{"name": {"you"}, "bot": {"main"}, "chat_id": {"43"}}, "/telegram")
	waitIdle("watcher idling after adding you", dials)
	if resp, _ := w.get("/mailboxes/mem"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /mailboxes/mem = %d", resp.StatusCode)
	}
	dials = imapSrv.dials()
	w.mustRedirect("/mailboxes/mem", mailboxForm("you"), "/mailboxes") // blank password keeps the stored one
	waitIdle("re-routed watcher idling", dials)
	imapSrv.appendMail(t, verificationMail("Your second code", "551177"))
	// A mail's log row and its outbox items (one per target) are stored together, so once
	// the second mail is logged and the outbox is empty, every delivery has been made.
	waitFor(t, "second mail logged and outbox drained", func() bool {
		_, total, err := st.ListLog(ctx, store.LogQuery{})
		if err != nil {
			t.Fatal(err)
		}
		return total >= 2 && w.status().Pending == 0
	})

	first, second := withCode("482913"), withCode("551177")
	if len(first) != 1 || first[0].ChatID != 42 {
		t.Errorf("first code went to %+v, want once to chat 42", first)
	}
	if len(second) != 1 || second[0].ChatID != 43 {
		t.Errorf("second code went to %+v, want once to chat 43 only", second)
	}
	for _, m := range append(first, second...) {
		if m.Path != "/bot1:abc/sendMessage" {
			t.Errorf("sendMessage path = %q", m.Path)
		}
	}

	versions, err := st.ListConfigVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) < 5 {
		t.Errorf("config versions = %d, want at least 5 (bot, me, mailbox, you, re-route)", len(versions))
	}
	rows, total, err := st.ListLog(ctx, store.LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("forward_log rows = %d, want 2: %+v", total, rows)
	}
	if resp, body := w.get("/logs"); resp.StatusCode != http.StatusOK || !strings.Contains(body, "Your second code") {
		t.Errorf("GET /logs = %d, second mail missing\n%s", resp.StatusCode, body)
	}
}
