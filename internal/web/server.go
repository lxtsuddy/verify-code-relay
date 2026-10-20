// Package web is the admin UI: a server-rendered site exposed to the internet behind TLS,
// with a single administrator who signs in with a password and a TOTP code.
package web

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/telegram"
	"verify-code-relay/internal/websec"
)

type Options struct {
	Store          *store.Store
	Runtime        *runtime.Runtime
	Log            *slog.Logger
	Telegram       *telegram.Client
	TrustedProxies []netip.Prefix
	Dial           runtime.DialFactory
	MSAuthority    string           // "" = the real Microsoft endpoint
	MSPollInterval time.Duration    // wait between device-code polls; 0 = Microsoft's interval (at least 5 s)
	Now            func() time.Time // nil = time.Now
}

type Server struct {
	o       Options
	pages   map[string]*template.Template
	assetV  string // content hash appended to static URLs
	limiter *websec.Limiter
	mux     *http.ServeMux

	saveMu sync.Mutex // serialises config changes (see mutate)

	// Microsoft authorisation flows (msauth.go)
	flowMu     sync.Mutex
	flows      map[string]*deviceFlow // by mailbox
	closed     bool                   // Close was called: no new flows
	startLocks map[string]*startLock  // serialise starts per mailbox
	flowWG     sync.WaitGroup         // running flow goroutines

	failMu      sync.Mutex
	failures    []time.Time // failed logins within the last hour
	lastSummary time.Time   // last failure summary notification
}

const (
	sessionIdle     = 30 * time.Minute
	sessionLifetime = 12 * time.Hour
	touchEvery      = time.Minute
	pruneEvery      = 10 * time.Minute
)

func New(o Options) *Server {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Dial == nil {
		o.Dial = runtime.TLSDial
	}
	if o.Telegram == nil {
		o.Telegram = telegram.New()
	}
	s := &Server{o: o, mux: http.NewServeMux(), flows: map[string]*deviceFlow{}, startLocks: map[string]*startLock{}}
	s.limiter = websec.NewLimiter(1, 5, s.now)
	s.assetV = assetVersion()
	s.pages = s.parsePages()
	s.routes()
	return s
}

func (s *Server) now() time.Time { return s.o.Now() }

// Handler returns the full middleware chain around the routes.
func (s *Server) Handler() http.Handler {
	return s.securityHeaders(s.loadSession(s.checkOrigin(s.mux)))
}

// PruneLoop deletes expired sessions now and every 10 minutes until ctx is done.
func (s *Server) PruneLoop(ctx context.Context) {
	t := time.NewTicker(pruneEvery)
	defer t.Stop()
	for {
		now := s.now()
		if n, err := s.o.Store.PruneSessions(ctx, now.Add(-sessionIdle), now.Add(-sessionLifetime)); err != nil {
			if ctx.Err() == nil {
				s.o.Log.Warn("清除過期 session 失敗", "err", err)
			}
		} else if n > 0 {
			s.o.Log.Debug("已清除過期 session", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
