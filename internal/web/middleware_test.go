package web

import (
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSecurityHeaders(t *testing.T) {
	e := newEnv(t, nil)
	resp, _ := e.get("/login")
	h := resp.Header
	if !strings.Contains(h.Get("Content-Security-Policy"), "script-src 'self'") || h.Get("X-Frame-Options") != "DENY" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", h)
	}
}

func TestPostRequiresCSRFAndOrigin(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	good := e.csrf
	e.csrf = "wrong"
	if resp, _ := e.postForm("/logout", nil); resp.StatusCode != 403 {
		t.Errorf("bad csrf = %d", resp.StatusCode)
	}
	e.csrf = good
	req, _ := http.NewRequest("POST", e.ts.URL+"/logout", strings.NewReader("csrf="+good))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := e.c.Do(req)
	if resp.StatusCode != 403 {
		t.Errorf("foreign origin = %d", resp.StatusCode)
	}
}

func TestCookieFlags(t *testing.T) {
	e := newEnv(t, nil)
	resp, _ := e.postForm("/login", url.Values{"password": {testPassword}, "code": {e.code()}})
	var c *http.Cookie
	for _, k := range resp.Cookies() {
		if k.Name == "__Host-relay_session" {
			c = k
		}
	}
	if c == nil {
		t.Fatal("no session cookie")
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Errorf("cookie = %+v", c)
	}
}

func TestNoInlineScriptsOrStyles(t *testing.T) {
	// The CSP forbids inline code; every template must comply.
	err := fs.WalkDir(templateFS, "templates", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(templateFS, p)
		s := string(b)
		for _, bad := range []string{"<style", " style=", "<script>", "onclick=", "onchange=", "onsubmit=", "oninput=", "javascript:"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s contains %q", p, bad)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOriginRules(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	post := func(set func(h http.Header)) int {
		req, _ := http.NewRequest("POST", e.ts.URL+"/logout", strings.NewReader("csrf="+url.QueryEscape(e.csrf)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		set(req.Header)
		resp, err := e.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := post(func(h http.Header) { h.Set("Origin", "null") }); c != 403 {
		t.Errorf("Origin null = %d", c)
	}
	if c := post(func(http.Header) {}); c != 403 {
		t.Errorf("no Origin or Referer = %d", c)
	}
	if c := post(func(h http.Header) { h.Set("Referer", "https://evil.example/x") }); c != 403 {
		t.Errorf("foreign Referer = %d", c)
	}
	if c := post(func(h http.Header) { h.Set("Origin", e.ts.URL); h.Set("Sec-Fetch-Site", "cross-site") }); c != 403 {
		t.Errorf("Sec-Fetch-Site cross-site = %d", c)
	}
	if c := post(func(h http.Header) { h.Set("Origin", "https://evil.example"); h.Set("Sec-Fetch-Site", "same-origin") }); c != 403 {
		t.Errorf("foreign Origin with Sec-Fetch-Site same-origin = %d", c)
	}
	if c := post(func(h http.Header) { h.Set("Origin", "null"); h.Set("Sec-Fetch-Site", "same-origin") }); c != 403 {
		t.Errorf("Origin null with Sec-Fetch-Site same-origin = %d", c)
	}
	if c := post(func(h http.Header) { h.Set("Referer", e.ts.URL+"/settings") }); c != 303 {
		t.Errorf("same-origin Referer = %d", c)
	}
}

// TestOriginScheme: https origins only, except plain http on a loopback host
// (kubectl port-forward).
func TestOriginScheme(t *testing.T) {
	e := newEnv(t, nil)
	post := func(host, origin string) int {
		e.login()
		req, _ := http.NewRequest("POST", e.ts.URL+"/logout", strings.NewReader("csrf="+url.QueryEscape(e.csrf)))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = host
		req.Header.Set("Origin", origin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		// the cookie jar keys on req.Host, so send the session cookie by hand
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: e.sessionCookie()})
		resp, err := (&http.Client{Transport: e.c.Transport, CheckRedirect: e.c.CheckRedirect}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		host, origin string
		want         int
	}{
		{"relay.example.com", "https://relay.example.com", 303},
		{"relay.example.com", "https://RELAY.example.com", 303},
		{"relay.example.com", "http://relay.example.com", 403},
		{"relay.example.com", "https://relay.example.com:8443", 403},
		{"localhost:8080", "http://localhost:8080", 303},
		{"127.0.0.1:8080", "http://127.0.0.1:8080", 303},
		{"[::1]:8080", "http://[::1]:8080", 303},
		{"localhost:8080", "ftp://localhost:8080", 403},
	} {
		if got := post(c.host, c.origin); got != c.want {
			t.Errorf("Host %s, Origin %s = %d, want %d", c.host, c.origin, got, c.want)
		}
	}
}

func TestStaticFiles(t *testing.T) {
	e := newEnv(t, nil)
	for _, p := range []string{"/static/app.css", "/static/app.js", "/static/icon.svg"} {
		resp, body := e.get(p)
		if resp.StatusCode != 200 || body == "" || resp.Header.Get("Cache-Control") != "public, max-age=3600" ||
			resp.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("GET %s = %d cache %q", p, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
	if resp, _ := e.get("/static/"); resp.StatusCode != 404 {
		t.Errorf("directory listing = %d", resp.StatusCode)
	}
	if resp, _ := e.get("/static/nope.css"); resp.StatusCode != 404 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("missing static file = %d cache %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	_, page := e.get("/login")
	if !strings.Contains(page, `href="/static/app.css?v=`) || !strings.Contains(page, `src="/static/app.js?v=`) {
		t.Error("pages must link the versioned assets")
	}
}

func TestErrorsAnswerJSONToScripts(t *testing.T) {
	e := newEnv(t, nil)
	resp, body := e.postJSON("/api/anything", nil)
	if resp.StatusCode != 404 || !strings.Contains(resp.Header.Get("Content-Type"), "application/json") || !strings.Contains(body, `"ok":false`) {
		t.Errorf("unknown API = %d %s", resp.StatusCode, body)
	}
	resp, body = e.get("/no-such-page")
	if resp.StatusCode != 404 || !strings.Contains(body, "找不到這個頁面") {
		t.Errorf("unknown page = %d", resp.StatusCode)
	}
}
