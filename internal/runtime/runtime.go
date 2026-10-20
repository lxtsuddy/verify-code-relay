// Package runtime owns the running relay service (watchers, dispatcher, alerter and log
// pruner) and can rebuild it in-process when the configuration changes.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"verify-code-relay/internal/alert"
	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/config"
	"verify-code-relay/internal/dispatcher"
	"verify-code-relay/internal/health"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/recognize"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/watcher"
)

// DialFactory returns the IMAP dialer for a mailbox host.
type DialFactory func(host string, port int) watcher.DialFunc

// TLSDial dials host:port with implicit TLS.
func TLSDial(host string, port int) watcher.DialFunc {
	return watcher.TLSDialer(net.JoinHostPort(host, strconv.Itoa(port)))
}

// Built is a validated, defaulted config with its compiled recognizer and renderer.
type Built struct {
	Config     *config.Config // defaulted
	Recognizer *recognize.Chain
	Renderer   *render.Renderer
}

// Build applies defaults to raw (without modifying it) and runs Validate, recognize.New and
// render.New, joining all problems into one error whose lines are "path: message".
func Build(raw *config.Config) (*Built, error) {
	cfg := raw.Defaulted()
	var errs []error
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	rec, err := recognize.New(cfg.Rules, cfg.Recognize)
	if err != nil {
		errs = append(errs, err)
	}
	rnd, err := render.New(cfg)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &Built{Config: cfg, Recognizer: rec, Renderer: rnd}, nil
}

type Options struct {
	Store  *store.Store
	Log    *slog.Logger
	Level  *slog.LevelVar // set from Config.LogLevel on each Apply; may be nil
	Sender dispatcher.Sender
	Dial   DialFactory
}

// stopWarnEvery is how often Apply and Stop log while the old service is still stopping.
const stopWarnEvery = 30 * time.Second

// Runtime runs at most one service at a time. Its methods are safe for concurrent use.
type Runtime struct {
	opts      Options
	applyMu   sync.Mutex // serialises Apply/Stop
	mu        sync.RWMutex
	cur       *service
	applying  bool
	appliedAt time.Time
}

// New returns a Runtime with nothing running; call Apply to start a service.
func New(o Options) *Runtime {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Dial == nil {
		o.Dial = TLSDial
	}
	return &Runtime{opts: o}
}

// Apply stops the current service (if any), waits for it to finish, then starts one for b.
// Calls are serialised. The old service is fully stopped first so that two dispatchers
// never send from the same outbox.
func (r *Runtime) Apply(b *Built) {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.mu.Lock()
	r.applying = true
	r.mu.Unlock()

	r.stopCurrent()

	if r.opts.Level != nil {
		var l slog.Level
		if err := l.UnmarshalText([]byte(b.Config.LogLevel)); err == nil {
			r.opts.Level.Set(l)
		}
	}
	s := r.newService(b)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wait = s.start(ctx)

	r.mu.Lock()
	r.cur = s
	r.applying = false
	r.appliedAt = time.Now()
	r.mu.Unlock()
	r.opts.Log.Info("設定已套用", "watchers", len(s.watchers))
}

// Reload loads the latest stored config, builds it and applies it.
func (r *Runtime) Reload() error {
	raw, _, err := LoadConfig(context.Background(), r.opts.Store)
	if err != nil {
		return err
	}
	b, err := Build(raw)
	if err != nil {
		return fmt.Errorf("設定有誤：%w", err)
	}
	r.Apply(b)
	return nil
}

// Stop stops the current service and waits for it. It may be called more than once,
// and Apply may start a new service afterwards.
func (r *Runtime) Stop() {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.stopCurrent()
	r.mu.Lock()
	r.cur = nil
	r.mu.Unlock()
}

// stopCurrent cancels the current service and waits for it, warning every stopWarnEvery.
// r.applyMu must be held. r.mu is not held while waiting, so Alert, Notify, Snapshot and
// Sources keep working (on the stopping service) in the meantime.
func (r *Runtime) stopCurrent() {
	r.mu.RLock()
	old := r.cur
	r.mu.RUnlock()
	if old == nil {
		return
	}
	old.cancel()
	done := make(chan struct{})
	go func() {
		old.wait()
		close(done)
	}()
	t := time.NewTicker(stopWarnEvery)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			r.opts.Log.Warn("等待舊服務停止中")
		}
	}
}

func (r *Runtime) current() *service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cur
}

// Current returns the applied config; nil before the first Apply and after Stop.
func (r *Runtime) Current() *Built {
	if s := r.current(); s != nil {
		return s.built
	}
	return nil
}

type WatcherStatus struct {
	Mailbox     string
	DisplayName string
	Folder      string
	Status      watcher.Status
}

type Snapshot struct {
	Applying  bool
	AppliedAt time.Time
	Watchers  []WatcherStatus // sorted by mailbox, then folder order in config
}

func (r *Runtime) Snapshot() Snapshot {
	r.mu.RLock()
	snap := Snapshot{Applying: r.applying, AppliedAt: r.appliedAt}
	s := r.cur
	r.mu.RUnlock()
	if s == nil {
		return snap
	}
	for _, w := range s.watchers {
		snap.Watchers = append(snap.Watchers, WatcherStatus{Mailbox: w.mailbox, DisplayName: w.displayName,
			Folder: w.folder, Status: w.w.Status()})
	}
	return snap
}

// Sources returns the health sources of the current service's watchers.
func (r *Runtime) Sources() []health.Source {
	s := r.current()
	if s == nil {
		return nil
	}
	out := make([]health.Source, len(s.watchers))
	for i, w := range s.watchers {
		out[i] = healthAdapter{w.w}
	}
	return out
}

// Alert raises a deduplicated alert through the current alerter. Without a running
// service it is only logged. key and text must not contain secrets.
func (r *Runtime) Alert(key, text string) {
	s := r.current()
	if s == nil {
		r.opts.Log.Warn("告警（服務未啟動）", "key", key, "text", text)
		return
	}
	s.alerter.Alert(key, text)
}

// Notify queues text for the admin target straight away, without deduplication. Without
// an admin target (or a running service) it is only logged. text must not contain secrets.
func (r *Runtime) Notify(text string) {
	s := r.current()
	if s == nil {
		r.opts.Log.Info("通知（服務未啟動）", "text", text)
		return
	}
	if s.admin == nil {
		r.opts.Log.Info("通知（未設定 admin 目標）", "text", text)
		return
	}
	now := time.Now()
	_, err := r.opts.Store.Enqueue(context.Background(), store.OutboxItem{
		Target: s.admin.Name, Bot: s.admin.Bot, ChatID: s.admin.ChatID, Text: "🔔 " + html.EscapeString(text),
		CreatedAt: now, ExpiresAt: now.Add(s.built.Config.Defaults.AlertTTL.Duration), NextAttemptAt: now,
	})
	if err != nil {
		r.opts.Log.Error("寫入通知失敗", "err", err, "text", text)
		return
	}
	s.disp.Wake()
}

type healthAdapter struct{ w *watcher.Watcher }

func (h healthAdapter) Key() string          { return h.w.Key() }
func (h healthAdapter) StatusString() string { return string(h.w.Status()) }

// runningWatcher is a watcher with the config names it was built from.
type runningWatcher struct {
	w           *watcher.Watcher
	mailbox     string
	displayName string
	folder      string
}

// service is one running instance of a Built config: the dispatcher, alerter, log
// pruner and one watcher per mailbox folder.
type service struct {
	built    *Built
	cancel   context.CancelFunc
	wait     func()
	watchers []runningWatcher
	alerter  *alert.Alerter
	disp     *dispatcher.Dispatcher
	admin    *alert.Target
	pruner   pruner
	log      *slog.Logger
}

func (r *Runtime) newService(b *Built) *service {
	cfg, st, log := b.Config, r.opts.Store, r.opts.Log
	tokens := map[string]string{}
	for name, bot := range cfg.Bots {
		tokens[name] = bot.Token
	}
	var alerter *alert.Alerter
	disp := dispatcher.New(dispatcher.Options{Store: st, Sender: r.opts.Sender, Tokens: tokens, Log: log,
		Alert: func(key, text string) { alerter.Alert(key, text) }})
	var admin *alert.Target
	if cfg.Admin != "" {
		t := cfg.Targets[cfg.Admin]
		admin = &alert.Target{Name: cfg.Admin, Bot: t.Bot, ChatID: t.ChatID}
	}
	alerter = alert.New(st, admin, cfg.Defaults.AlertTTL.Duration, disp.Wake, log)

	proc := &pipeline.Processor{Store: st, Recognizer: b.Recognizer, Renderer: b.Renderer, Notify: disp.Wake, Log: log}

	s := &service{built: b, alerter: alerter, disp: disp, admin: admin, pruner: st, log: log}
	for _, name := range cfg.SortedMailboxNames() {
		m := cfg.Mailboxes[name]
		var targets []pipeline.Target
		for _, tn := range m.Targets {
			t := cfg.Targets[tn]
			targets = append(targets, pipeline.Target{Name: tn, Bot: t.Bot, ChatID: t.ChatID})
		}
		var authn auth.Authenticator = auth.Password{Username: m.Username, Password: m.Auth.Password}
		if m.Auth.Type == "microsoft_oauth" {
			authn = &auth.Microsoft{Mailbox: name, Username: m.Username, ClientID: m.Auth.ClientID, Tenant: m.Auth.Tenant, Store: st, Log: log}
		}
		mb := pipeline.Mailbox{Name: name, DisplayName: m.DisplayName, Username: m.Username, Targets: targets,
			MaxAge: m.MaxAge.Duration, TTL: m.TTL.Duration}
		for _, folder := range m.Folders {
			w := watcher.New(watcher.Config{
				Mailbox: mb, Folder: folder,
				Dial:      r.opts.Dial(m.Host, m.Port),
				Auth:      authn,
				Processor: proc, State: st, Alert: alerter, Log: log,
				PollInterval: m.PollInterval.Duration, AlertAfter: cfg.Defaults.AlertAfter.Duration,
			})
			s.watchers = append(s.watchers, runningWatcher{w: w, mailbox: name, displayName: m.DisplayName, folder: folder})
		}
	}
	return s
}

// start runs the watchers, the dispatcher and the log pruner until ctx is
// cancelled; wait blocks until all of them have stopped.
func (s *service) start(ctx context.Context) (wait func()) {
	var wg sync.WaitGroup
	for _, w := range s.watchers {
		wg.Go(func() { w.w.Run(ctx) })
	}
	wg.Go(func() { s.disp.Run(ctx) })
	wg.Go(func() { pruneLoop(ctx, s.pruner, s.log) })
	return wg.Wait
}

type pruner interface {
	PruneLog(ctx context.Context, before time.Time) (int64, error)
}

func pruneLoop(ctx context.Context, st pruner, log interface {
	Info(string, ...any)
	Warn(string, ...any)
}) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		n, err := st.PruneLog(ctx, time.Now().Add(-30*24*time.Hour))
		switch {
		case err != nil:
			if ctx.Err() == nil {
				log.Warn("清理轉發紀錄失敗", "err", err)
			}
		case n > 0:
			log.Info("已清理舊轉發紀錄", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
