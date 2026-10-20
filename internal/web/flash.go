package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// flashCookie carries a Flash across one redirect. The __Host- prefix keeps other hosts
// (sub-domains) from planting one; the text is always HTML-escaped when shown.
const flashCookie = "__Host-relay_flash"

var flashKinds = map[string]bool{"success": true, "error": true, "info": true}

func (s *Server) setFlash(w http.ResponseWriter, f Flash) {
	if !flashKinds[f.Kind] {
		f.Kind = "info"
	}
	b, _ := json.Marshal(f)
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: base64.RawURLEncoding.EncodeToString(b),
		Path: "/", MaxAge: 60, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// takeFlash returns the pending flash (nil if none) and clears it.
func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) *Flash {
	c, err := r.Cookie(flashCookie)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	return decodeFlash(c.Value)
}

func decodeFlash(v string) *Flash {
	b, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return nil
	}
	var f Flash
	if json.Unmarshal(b, &f) != nil || !flashKinds[f.Kind] || f.Text == "" {
		return nil
	}
	if r := []rune(f.Text); len(r) > 500 {
		f.Text = string(r[:500])
	}
	return &f
}
