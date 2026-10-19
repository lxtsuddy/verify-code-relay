// Package watcher keeps one IMAP connection per (mailbox, folder) and feeds new
// mail to the pipeline.
package watcher

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/store"
)

type Status string

const (
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusReconnecting Status = "reconnecting"
	StatusAuthFailed   Status = "auth_failed"
	StatusNeedsLogin   Status = "needs_login"
	StatusStopped      Status = "stopped"
)

type DialFunc func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error)

// TLSDialer dials implicit TLS with TCP keepalive.
func TLSDialer(addr string) DialFunc {
	return func(_ context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
		o := *opts
		o.Dialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 60 * time.Second}
		o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		return imapclient.DialTLS(addr, &o)
	}
}

type Processor interface {
	Decide(mb pipeline.Mailbox, env pipeline.Envelope) string
	RecordSkipped(ctx context.Context, mb pipeline.Mailbox, folder string, uidValidity uint32, env pipeline.Envelope, kind string) error
	Process(ctx context.Context, mb pipeline.Mailbox, folder string, uidValidity uint32, env pipeline.Envelope, raw []byte) error
}

type StateStore interface {
	GetMailboxState(ctx context.Context, mailbox, folder string) (store.MailboxState, bool, error)
	SetMailboxState(ctx context.Context, mailbox, folder string, st store.MailboxState) error
}

type Alerter interface {
	Alert(key, text string)
	Recovered(key, text string)
}

type Config struct {
	Mailbox   pipeline.Mailbox
	Folder    string
	Dial      DialFunc
	Auth      auth.Authenticator
	Processor Processor
	State     StateStore
	Alert     Alerter
	Log       *slog.Logger

	PollInterval time.Duration
	AlertAfter   time.Duration

	IdleCycle    time.Duration
	CmdTimeout   time.Duration
	FetchTimeout time.Duration
	MinBackoff   time.Duration
	MaxBackoff   time.Duration
}

type AuthError struct{ Err error }

func (e *AuthError) Error() string { return "認證失敗：" + e.Err.Error() }
func (e *AuthError) Unwrap() error { return e.Err }

type Watcher struct {
	cfg    Config
	log    *slog.Logger
	status atomic.Value
	now    func() time.Time

	// forcePoll makes session poll with NOOP even if the server supports IDLE (test hook).
	forcePoll bool
}

func orDefault(v *time.Duration, def time.Duration) {
	if *v == 0 {
		*v = def
	}
}

func New(cfg Config) *Watcher {
	orDefault(&cfg.IdleCycle, 25*time.Minute)
	orDefault(&cfg.CmdTimeout, 30*time.Second)
	orDefault(&cfg.FetchTimeout, 2*time.Minute)
	orDefault(&cfg.MinBackoff, time.Second)
	orDefault(&cfg.MaxBackoff, 5*time.Minute)
	orDefault(&cfg.PollInterval, 30*time.Second)
	orDefault(&cfg.AlertAfter, 10*time.Minute)
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	w := &Watcher{cfg: cfg, now: time.Now}
	w.log = cfg.Log.With("mailbox", cfg.Mailbox.Name, "folder", cfg.Folder)
	w.status.Store(StatusConnecting)
	return w
}

func (w *Watcher) Key() string { return w.cfg.Mailbox.Name + "/" + w.cfg.Folder }

func (w *Watcher) Status() Status { return w.status.Load().(Status) }

func (w *Watcher) setStatus(s Status) { w.status.Store(s) }
