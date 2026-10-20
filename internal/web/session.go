package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"verify-code-relay/internal/store"
	"verify-code-relay/internal/websec"
)

const sessionCookie = "__Host-relay_session"

type sessionInfo struct {
	TokenHash string
	CSRF      string
	Pending   bool // the admin has not bound TOTP yet (admin.TOTPSecret == "")
}

type ctxKey struct{}

// sessionFrom returns the request's session; nil = not signed in.
func sessionFrom(ctx context.Context) *sessionInfo {
	si, _ := ctx.Value(ctxKey{}).(*sessionInfo)
	return si
}

// loadSession puts the valid session named by the cookie (if any) into the context.
// Expired sessions are deleted; LastSeen is refreshed at most once a minute.
func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		hash := websec.HashToken(c.Value)
		se, ok, err := s.o.Store.GetSession(ctx, hash)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !ok {
			clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		now := s.now()
		if now.Sub(se.LastSeen) > sessionIdle || now.Sub(se.CreatedAt) > sessionLifetime {
			if err := s.o.Store.DeleteSession(ctx, hash); err != nil {
				s.serverError(w, r, err)
				return
			}
			clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		admin, ok, err := s.o.Store.GetAdmin(ctx)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !ok { // admin removed: no session can be valid
			next.ServeHTTP(w, r)
			return
		}
		if now.Sub(se.LastSeen) > touchEvery {
			if err := s.o.Store.TouchSession(ctx, hash, now); err != nil {
				s.o.Log.Warn("更新 session 時間失敗", "err", err)
			}
		}
		si := &sessionInfo{TokenHash: hash, CSRF: se.CSRF, Pending: admin.TOTPSecret == ""}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxKey{}, si)))
	})
}

// startSession replaces the request's session (if any) with a fresh one and sets its
// cookie, so a token planted before login is never promoted (session fixation). It
// returns the new session's token hash.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, ip string) (string, error) {
	ctx := r.Context()
	if old := sessionFrom(ctx); old != nil {
		if err := s.o.Store.DeleteSession(ctx, old.TokenHash); err != nil {
			return "", err
		}
	} else if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.o.Store.DeleteSession(ctx, websec.HashToken(c.Value)); err != nil {
			return "", err
		}
	}
	token, now := websec.RandomToken(), s.now()
	hash := websec.HashToken(token)
	err := s.o.Store.CreateSession(ctx, store.Session{
		TokenHash: hash, CSRF: websec.RandomToken(),
		CreatedAt: now, LastSeen: now, IP: ip, UserAgent: truncate(r.UserAgent(), 200),
	})
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionLifetime / time.Second),
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	return hash, nil
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
}

// truncate shortens s to at most n runes, marking the cut with "…".
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
