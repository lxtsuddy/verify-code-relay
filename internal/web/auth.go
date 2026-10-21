package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/lxtsuddy/verify-code-relay/internal/websec"
)

// loginData never says whether TOTP is bound: the form is the same either way, so a
// visitor cannot learn the binding state.
type loginData struct {
	HasAdmin bool
}

func (s *Server) getLogin(w http.ResponseWriter, r *http.Request) {
	if si := sessionFrom(r.Context()); si != nil {
		if si.Pending {
			http.Redirect(w, r, "/setup/totp", http.StatusSeeOther)
		} else {
			http.Redirect(w, r, "/", http.StatusSeeOther)
		}
		return
	}
	s.renderLogin(w, r, http.StatusOK, "")
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, msg string) {
	_, ok, err := s.o.Store.GetAdmin(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := Page{Title: "登入", Data: loginData{HasAdmin: ok}}
	if msg != "" {
		p.Errors = FieldErrors{"": msg}
	}
	s.render(w, r, status, "login", p)
}

func (s *Server) postLogin(w http.ResponseWriter, r *http.Request) {
	ip := websec.ClientIP(r, s.o.TrustedProxies)
	if !s.limiter.Allow() {
		s.renderLogin(w, r, http.StatusTooManyRequests, "嘗試太頻繁，請稍後再試")
		return
	}
	admin, ok, err := s.o.Store.GetAdmin(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		s.renderLogin(w, r, http.StatusServiceUnavailable, "尚未設定管理員：請設定 RELAY_ADMIN_PASSWORD 後重新啟動服務")
		return
	}
	pwOK, err := websec.VerifyPassword(admin.PasswordHash, r.PostFormValue("password"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if admin.TOTPSecret == "" { // first login: password only, then forced TOTP binding
		if pwOK {
			if _, err := s.startSession(w, r, ip); err != nil {
				s.serverError(w, r, err)
				return
			}
			s.o.Log.Info("Web 首次登入，等待綁定兩步驟驗證", "ip", ip)
			s.notifyLogin(ip, r.UserAgent(), true)
			http.Redirect(w, r, "/setup/totp", http.StatusSeeOther)
			return
		}
	} else {
		step, codeOK := websec.VerifyTOTP(admin.TOTPSecret, normalizeCode(r.PostFormValue("code")), s.now())
		if pwOK && codeOK {
			used, err := s.o.Store.UseTOTPStep(r.Context(), step)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			if used {
				if _, err := s.startSession(w, r, ip); err != nil {
					s.serverError(w, r, err)
					return
				}
				s.o.Log.Info("Web 登入成功", "ip", ip)
				s.notifyLogin(ip, r.UserAgent(), false)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
		}
		if pwOK && !codeOK {
			s.o.Runtime.Alert("web_login_totp", fmt.Sprintf("Web 後台登入：密碼正確但驗證碼錯誤（IP %s，%s）。若不是你本人，密碼可能已外洩，請立即修改密碼。", ip, truncate(r.UserAgent(), 120)))
		}
	}
	s.recordFailure(ip)
	s.o.Log.Warn("Web 登入失敗", "ip", ip)
	s.renderLogin(w, r, http.StatusUnauthorized, "登入失敗")
}

// normalizeCode drops the spaces and dashes people type or paste inside a code.
func normalizeCode(c string) string {
	return strings.NewReplacer(" ", "", "-", "", "\u00a0", "").Replace(strings.TrimSpace(c))
}

func (s *Server) postLogout(w http.ResponseWriter, r *http.Request) {
	si := sessionFrom(r.Context())
	if err := s.o.Store.DeleteSession(r.Context(), si.TokenHash); err != nil {
		s.serverError(w, r, err)
		return
	}
	clearSessionCookie(w)
	s.redirect(w, r, "/login", &Flash{Kind: "info", Text: "已登出"})
}

type totpData struct {
	Rebind bool     // replacing a bound secret (started from the settings page)
	Groups []string // the pending secret in groups of four, for typing by hand
	Secret string   // the pending secret, for the copy button
}

// getTOTPSetup shows the binding page. The pending secret is created here on the first
// visit; re-binding a bound admin is started from the settings page instead.
func (s *Server) getTOTPSetup(w http.ResponseWriter, r *http.Request) {
	admin, ok, err := s.o.Store.GetAdmin(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		s.needLogin(w, r)
		return
	}
	if admin.TOTPPending == "" {
		if admin.TOTPSecret != "" {
			http.Redirect(w, r, "/settings", http.StatusSeeOther)
			return
		}
		admin.TOTPPending = websec.NewTOTPSecret()
		if err := s.o.Store.SetTOTPPending(r.Context(), admin.TOTPPending); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.renderTOTPSetup(w, r, http.StatusOK, admin.TOTPPending, admin.TOTPSecret != "", "")
}

func (s *Server) renderTOTPSetup(w http.ResponseWriter, r *http.Request, status int, pending string, rebind bool, msg string) {
	var groups []string
	for i := 0; i < len(pending); i += 4 {
		groups = append(groups, pending[i:min(i+4, len(pending))])
	}
	p := Page{Title: "綁定兩步驟驗證", Data: totpData{Rebind: rebind, Groups: groups, Secret: pending}}
	if msg != "" {
		p.Errors = FieldErrors{"code": msg}
	}
	s.render(w, r, status, "totp_setup", p)
}

func (s *Server) postTOTPSetup(w http.ResponseWriter, r *http.Request) {
	admin, ok, err := s.o.Store.GetAdmin(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok || admin.TOTPPending == "" {
		http.Redirect(w, r, "/setup/totp", http.StatusSeeOther)
		return
	}
	step, codeOK := websec.VerifyTOTP(admin.TOTPPending, normalizeCode(r.PostFormValue("code")), s.now())
	if !codeOK {
		s.renderTOTPSetup(w, r, http.StatusUnprocessableEntity, admin.TOTPPending, admin.TOTPSecret != "", "驗證碼不正確，請確認手機時間")
		return
	}
	first := admin.TOTPSecret == ""
	ctx := r.Context()
	// Every session is "pending" until the first binding and becomes fully privileged the
	// moment it happens, so on first binding only this session may survive: drop the others
	// before binding, and again afterwards in case one was created meanwhile.
	if first {
		if err := s.o.Store.DeleteOtherSessions(ctx, sessionFrom(ctx).TokenHash); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if err := s.o.Store.ConfirmTOTP(ctx, admin.TOTPPending, step); err != nil {
		s.serverError(w, r, err)
		return
	}
	ip := websec.ClientIP(r, s.o.TrustedProxies)
	// the session just gained full access: give it a new token
	newHash, err := s.startSession(w, r, ip)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// The pending secret is not tied to a session, so any signed-in session could have
	// confirmed it; only the one that did keeps going, after both first binding and rebind.
	if err := s.o.Store.DeleteOtherSessions(ctx, newHash); err != nil {
		s.serverError(w, r, err)
		return
	}
	if first {
		s.o.Log.Info("Web 後台已完成 TOTP 綁定", "ip", ip)
		s.o.Runtime.Notify("Web 後台已完成 TOTP 綁定")
	} else {
		s.o.Log.Info("Web 後台已重新綁定 TOTP，其他 session 已登出", "ip", ip)
		s.o.Runtime.Notify(fmt.Sprintf("Web 後台已重新綁定 TOTP（IP %s）。舊的驗證器已失效，其他裝置的登入已全部登出。", ip))
	}
	s.redirect(w, r, "/", &Flash{Kind: "success", Text: "兩步驟驗證已啟用"})
}

// getTOTPQR serves the pending secret as a QR code.
func (s *Server) getTOTPQR(w http.ResponseWriter, r *http.Request) {
	admin, ok, err := s.o.Store.GetAdmin(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok || admin.TOTPPending == "" {
		http.NotFound(w, r)
		return
	}
	png, err := websec.QRPNG(websec.TOTPURI(admin.TOTPPending, "admin", "verify-code-relay"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(png)
}
