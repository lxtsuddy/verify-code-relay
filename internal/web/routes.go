package web

import (
	"io/fs"
	"net/http"
	"strings"
)

// routes registers every route. Wrap each handler in s.public, s.pendingOK or s.authed,
// and add new paths to TestRoutesRequireLogin.
func (s *Server) routes() {
	m := s.mux
	m.Handle("GET /static/", s.static())

	m.HandleFunc("GET /login", s.public(s.getLogin))
	m.HandleFunc("POST /login", s.public(s.postLogin))
	m.HandleFunc("POST /logout", s.pendingOK(s.postLogout))
	m.HandleFunc("GET /setup/totp", s.pendingOK(s.getTOTPSetup))
	m.HandleFunc("POST /setup/totp", s.pendingOK(s.postTOTPSetup))
	m.HandleFunc("GET /setup/totp.png", s.pendingOK(s.getTOTPQR))

	m.HandleFunc("GET /{$}", s.authed(s.getHome))

	m.HandleFunc("GET /settings", s.authed(s.getSettings))
	m.HandleFunc("POST /settings/general", s.authed(s.postSettingsGeneral))
	m.HandleFunc("POST /settings/password", s.authed(s.postSettingsPassword))
	m.HandleFunc("POST /settings/totp", s.authed(s.postSettingsTOTP))
	m.HandleFunc("GET /settings/versions", s.authed(s.getVersions))
	m.HandleFunc("POST /settings/versions/{id}/restore", s.authed(s.postRestore))

	m.HandleFunc("/", s.public(s.notFound))
}

// static serves the embedded files (no directory listings), cacheable for an hour; page
// templates add a content hash to the URLs, so a new build is picked up at once.
func (s *Server) static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/static/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		if fi, err := fs.Stat(sub, name); err != nil || fi.IsDir() {
			s.notFound(w, r) // keeps no-store: a missing file must not be cached
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		files.ServeHTTP(w, r)
	})
}

// getHome is a placeholder overview until the dashboard (Task 12) replaces it.
func (s *Server) getHome(w http.ResponseWriter, r *http.Request) {
	snap := s.o.Runtime.Snapshot()
	cfg := s.o.Runtime.Current()
	d := homeData{Watchers: len(snap.Watchers), Applying: snap.Applying}
	if cfg != nil {
		d.Mailboxes, d.Bots, d.Targets = len(cfg.Config.Mailboxes), len(cfg.Config.Bots), len(cfg.Config.Targets)
	}
	s.render(w, r, http.StatusOK, "home", Page{Title: "總覽", Nav: "dashboard", Data: d})
}

type homeData struct {
	Mailboxes, Bots, Targets, Watchers int
	Applying                           bool
}
