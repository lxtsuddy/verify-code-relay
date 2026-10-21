// Package runtime owns the running relay service (watchers, dispatcher, alerter and log
// pruner) and can rebuild it in-process when the configuration changes.
package runtime

import (
	"context"
	"errors"
	"fmt"
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
	stopped   bool // Stop was called: Apply and Reload start nothing any more (under applyMu)
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
	r.ApplyWith(b, nil)
}

// ApplyWith is Apply that calls beforeStart (when not nil) after the old service has
// fully stopped and before the new one starts: nothing of either service runs meanwhile,
// so e.g. a stored OAuth token deleted there can neither be written back by an old
// watcher refreshing it nor be read by a new one.
func (r *Runtime) ApplyWith(b *Built, beforeStart func()) {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.applyLocked(b, beforeStart)
}

// applyLocked does the work of ApplyWith; r.applyMu must be held. After Stop it only runs
// beforeStart: a config change racing the shutdown must not start new watchers.
func (r *Runtime) applyLocked(b *Built, beforeStart func()) {
	if r.stopped {
		if beforeStart != nil {
			beforeStart()
		}
		r.opts.Log.Info("服務正在關閉，不套用新設定（下次啟動時會載入）")
		return
	}
	r.mu.Lock()
	r.applying = true
	r.mu.Unlock()

	r.stopCurrent()
	if beforeStart != nil {
		beforeStart()
	}

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

// Reload loads the latest stored config, builds it and applies it. The config is loaded
// while holding the apply lock, so a Reload queued behind another Apply still applies the
// newest stored version rather than the one current when Reload was called.
func (r *Runtime) Reload() error {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	if r.stopped {
		r.opts.Log.Info("服務正在關閉，不重新載入設定")
		return nil
	}
	raw, _, err := LoadConfig(context.Background(), r.opts.Store)
	if err != nil {
		return err
	}
	b, err := Build(raw)
	if err != nil {
		return fmt.Errorf("設定有誤：%w", err)
	}
	r.applyLocked(b, nil)
	return nil
}

// Stop stops the current service and waits for it. It is final: Apply, ApplyWith and
// Reload start nothing afterwards (they only log). It may be called more than once.
func (r *Runtime) Stop() {
	r.applyMu.Lock()
	defer r.applyMu.Unlock()
	r.stopped = true
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

// Notify queues text for the admin target straight away, without deduplication, truncated
// to Telegram's length limit. Without an admin target (or a running service) it is only
// logged. text must not contain secrets.
func (r *Runtime) Notify(text string) {
	s := r.current()
	if s == nil {
		r.opts.Log.Info("通知（服務未啟動）", "text", text)
		return
	}
	s.alerter.Notify(text)
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

	s := &service{built: b, alerter: alerter, disp: disp, pruner: st, log: log}
	for _, name := range cfg.SortedMailboxNames() {
		m := cfg.Mailboxes[name]
		var authn auth.Authenticator = auth.Password{Username: m.Username, Password: m.Auth.Password}
		if m.Auth.Type == "microsoft_oauth" {
			clientID, tenant := cfg.MicrosoftApp(m)
			authn = &auth.Microsoft{Mailbox: name, Username: m.Username, ClientID: clientID, Tenant: tenant, Store: st, Log: log}
		}
		for _, folder := range m.WatchedFolders() {
			w := watcher.New(watcher.Config{
				Mailbox: mailboxFor(cfg, name, folder), Folder: folder,
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

// mailboxFor is mailbox name as the watcher of folder sees it: mail found there goes to
// the targets the routes give that folder.
func mailboxFor(cfg *config.Config, name, folder string) pipeline.Mailbox {
	m := cfg.Mailboxes[name]
	var targets []pipeline.Target
	for _, tn := range m.TargetsOf(folder) {
		t := cfg.Targets[tn]
		targets = append(targets, pipeline.Target{Name: tn, Bot: t.Bot, ChatID: t.ChatID})
	}
	return pipeline.Mailbox{Name: name, DisplayName: m.DisplayName, Username: m.Username, Targets: targets,
		MaxAge: m.MaxAge.Duration, TTL: m.TTL.Duration}
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
