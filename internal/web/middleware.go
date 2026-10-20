package web

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"verify-code-relay/internal/websec"
)

const csp = "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'"

// maxBody caps request bodies; no form or JSON call in the admin comes near it.
const maxBody = 1 << 20

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin") // no-referrer would make browsers send "Origin: null" on our own form posts
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cache-Control", "no-store") // the static handler overrides this
		next.ServeHTTP(w, r)
	})
}

func safeMethod(r *http.Request) bool {
	return r.Method == http.MethodGet || r.Method == http.MethodHead
}

// checkOrigin rejects every non-GET/HEAD request that a browser did not send from this
// site:
//   - Sec-Fetch-Site, when present, must be "same-origin";
//   - Origin (or, without one, Referer) must name this host with scheme https — or http
//     when the host is loopback (kubectl port-forward). "Origin: null", a foreign origin or
//     neither header is rejected.
//
// Referrer-Policy same-origin makes browsers send a real Origin on our own form posts.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if safeMethod(r) {
			next.ServeHTTP(w, r)
			return
		}
		site := r.Header.Get("Sec-Fetch-Site")
		src := r.Header.Get("Origin")
		if src == "" {
			src = r.Header.Get("Referer")
		}
		if (site != "" && site != "same-origin") || !sameOrigin(src, r.Host) {
			s.o.Log.Warn("拒絕跨來源請求", "method", r.Method, "path", r.URL.Path, "origin", truncate(src, 100),
				"sec_fetch_site", truncate(site, 30), "ip", websec.ClientIP(r, s.o.TrustedProxies))
			s.fail(w, r, http.StatusForbidden, "請求來源不符。請從管理介面本身操作；若持續發生，請重新整理頁面。")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether the URL src is this site: its host equals host (compared
// case-insensitively) and its scheme is https, or http for a loopback host.
func sameOrigin(src, host string) bool {
	if src == "" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, host) {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		return isLoopback(u.Hostname())
	}
	return false
}

func isLoopback(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.IsLoopback()
}

// public routes need nothing.
func (s *Server) public(h http.HandlerFunc) http.HandlerFunc { return h }

// pendingOK routes need a session, which may still be waiting for TOTP binding.
func (s *Server) pendingOK(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		si := sessionFrom(r.Context())
		if si == nil {
			s.needLogin(w, r)
			return
		}
		if !safeMethod(r) && !s.validCSRF(r, si) {
			s.fail(w, r, http.StatusForbidden, "頁面已過期（安全驗證碼不符）。請重新整理頁面後再操作一次。")
			return
		}
		h(w, r)
	}
}

// authed routes need a session of an admin with TOTP bound.
func (s *Server) authed(h http.HandlerFunc) http.HandlerFunc {
	return s.pendingOK(func(w http.ResponseWriter, r *http.Request) {
		if sessionFrom(r.Context()).Pending {
			if safeMethod(r) && !wantsJSON(r) {
				http.Redirect(w, r, "/setup/totp", http.StatusSeeOther)
				return
			}
			s.fail(w, r, http.StatusForbidden, "請先完成兩步驟驗證綁定。")
			return
		}
		h(w, r)
	})
}

func (s *Server) needLogin(w http.ResponseWriter, r *http.Request) {
	if safeMethod(r) && !wantsJSON(r) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.fail(w, r, http.StatusUnauthorized, "尚未登入或登入已逾時，請重新登入。")
}

// validCSRF checks the form field "csrf" or the X-CSRF-Token header.
func (s *Server) validCSRF(r *http.Request, si *sessionInfo) bool {
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" {
		tok = r.PostFormValue("csrf")
	}
	return tok != "" && websec.Equal(tok, si.CSRF)
}
