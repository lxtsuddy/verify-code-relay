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

	m.HandleFunc("GET /{$}", s.authed(s.getDashboard))
	m.HandleFunc("GET /api/status", s.authed(s.getAPIStatus))
	m.HandleFunc("GET /logs", s.authed(s.getLogs))

	m.HandleFunc("GET /settings", s.authed(s.getSettings))
	m.HandleFunc("POST /settings/general", s.authed(s.postSettingsGeneral))
	m.HandleFunc("POST /settings/password", s.authed(s.postSettingsPassword))
	m.HandleFunc("POST /settings/totp", s.authed(s.postSettingsTOTP))
	m.HandleFunc("GET /settings/versions", s.authed(s.getVersions))
	m.HandleFunc("POST /settings/versions/{id}/restore", s.authed(s.postRestore))

	m.HandleFunc("GET /mailboxes", s.authed(s.getMailboxes))
	m.HandleFunc("GET /mailboxes/new", s.authed(s.getMailboxNew))
	m.HandleFunc("POST /mailboxes/new", s.authed(s.postMailboxNew))
	m.HandleFunc("GET /mailboxes/{name}", s.authed(s.getMailboxEdit))
	m.HandleFunc("POST /mailboxes/{name}", s.authed(s.postMailboxEdit))
	m.HandleFunc("POST /mailboxes/{name}/delete", s.authed(s.postMailboxDelete))
	m.HandleFunc("POST /api/mailboxes/probe", s.authed(s.postAPIProbe))
	m.HandleFunc("POST /api/mailboxes/{name}/oauth/start", s.authed(s.postOAuthStart))
	m.HandleFunc("GET /api/mailboxes/{name}/oauth/status", s.authed(s.getOAuthStatus))

	m.HandleFunc("GET /telegram", s.authed(s.getTelegram))
	m.HandleFunc("POST /telegram/bots", s.authed(s.postBotAdd))
	m.HandleFunc("POST /telegram/bots/{name}", s.authed(s.postBotRotate))
	m.HandleFunc("POST /telegram/bots/{name}/delete", s.authed(s.postBotDelete))
	m.HandleFunc("POST /telegram/targets", s.authed(s.postTargetAdd))
	m.HandleFunc("POST /telegram/targets/{name}", s.authed(s.postTargetEdit))
	m.HandleFunc("POST /telegram/targets/{name}/delete", s.authed(s.postTargetDelete))
	m.HandleFunc("POST /telegram/targets/{name}/test", s.authed(s.postTargetTest))
	m.HandleFunc("POST /api/telegram/getme", s.authed(s.postAPIGetMe))
	m.HandleFunc("POST /api/telegram/chats", s.authed(s.postAPIChats))

	m.HandleFunc("GET /rules", s.authed(s.getRules))
	m.HandleFunc("POST /rules", s.authed(s.postRules))
	m.HandleFunc("GET /templates", s.authed(s.getTemplates))
	m.HandleFunc("POST /templates", s.authed(s.postTemplates))
	m.HandleFunc("POST /api/templates/preview", s.authed(s.postAPITemplatePreview))

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
