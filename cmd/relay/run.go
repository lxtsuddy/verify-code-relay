package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"verify-code-relay/internal/alert"
	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/dispatcher"
	"verify-code-relay/internal/health"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/watcher"
)

type healthAdapter struct{ w *watcher.Watcher }

func (h healthAdapter) Key() string          { return h.w.Key() }
func (h healthAdapter) StatusString() string { return string(h.w.Status()) }

func cmdRun(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("run", &configPath, stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	restrictUmask()
	a, err := loadApp(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "設定檔有誤（%s）：\n%v\n", configPath, err)
		return 1
	}
	cfg := a.cfg
	log := newLogger(cfg.LogLevel, stdout)
	st, err := openStore(cfg)
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Bind synchronously so a port conflict or bad address fails startup instead of
	// silently running without /healthz.
	var httpLn net.Listener
	if cfg.HTTPListen != "" {
		httpLn, err = net.Listen("tcp", cfg.HTTPListen)
		if err != nil {
			return fail(stderr, fmt.Errorf("http_listen %q：%w", cfg.HTTPListen, err))
		}
		defer httpLn.Close()
	}

	svc := newService(a, st, log, telegramClient(), tlsDial)
	wait := svc.start(ctx)

	var httpSrv *http.Server
	if httpLn != nil {
		httpSrv = &http.Server{Handler: health.Handler(svc.sources), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := httpSrv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("HTTP 服務失敗", "err", err)
			}
		}()
	}
	log.Info("服務已啟動", "watchers", len(svc.watchers), "http", cfg.HTTPListen)

	<-ctx.Done()
	stop() // restore default signal handling: a second signal kills a hung shutdown
	log.Info("收到停止訊號，正在關閉")
	wait()
	if httpSrv != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		httpSrv.Shutdown(sctx)
		cancel()
	}
	log.Info("已停止")
	return 0
}

// dialFactory returns the IMAP dialer for a mailbox host.
type dialFactory func(host string, port int) watcher.DialFunc

func tlsDial(host string, port int) watcher.DialFunc {
	return watcher.TLSDialer(net.JoinHostPort(host, strconv.Itoa(port)))
}

// service wires the store, recognizer, renderer, pipeline, dispatcher, alerter and
// one watcher per mailbox folder. Process concerns (flags, signals, HTTP) stay in cmdRun.
type service struct {
	st       *store.Store
	log      *slog.Logger
	disp     *dispatcher.Dispatcher
	watchers []*watcher.Watcher
	sources  []health.Source
}

func newService(a *app, st *store.Store, log *slog.Logger, sender dispatcher.Sender, dial dialFactory) *service {
	cfg := a.cfg
	tokens := map[string]string{}
	for name, b := range cfg.Bots {
		tokens[name] = b.Token
	}
	var alerter *alert.Alerter
	disp := dispatcher.New(dispatcher.Options{Store: st, Sender: sender, Tokens: tokens, Log: log,
		Alert: func(key, text string) { alerter.Alert(key, text) }})
	var admin *alert.Target
	if cfg.Admin != "" {
		t := cfg.Targets[cfg.Admin]
		admin = &alert.Target{Name: cfg.Admin, Bot: t.Bot, ChatID: t.ChatID}
	}
	alerter = alert.New(st, admin, cfg.Defaults.AlertTTL.Duration, disp.Wake, log)

	proc := &pipeline.Processor{Store: st, Recognizer: a.recognizer, Renderer: a.renderer, Notify: disp.Wake, Log: log}

	s := &service{st: st, log: log, disp: disp}
	for _, name := range cfg.SortedMailboxNames() {
		m := cfg.Mailboxes[name]
		var targets []pipeline.Target
		for _, tn := range m.Targets {
			t := cfg.Targets[tn]
			targets = append(targets, pipeline.Target{Name: tn, Bot: t.Bot, ChatID: t.ChatID})
		}
		var authn auth.Authenticator = auth.Password{Username: m.Username, Password: m.Auth.Password}
		if m.Auth.Type == "microsoft_oauth" {
			authn = &auth.Microsoft{Mailbox: name, Username: m.Username, ClientID: m.Auth.ClientID, Tenant: m.Auth.Tenant, Store: st}
		}
		mb := pipeline.Mailbox{Name: name, DisplayName: m.DisplayName, Username: m.Username, Targets: targets,
			MaxAge: m.MaxAge.Duration, TTL: m.TTL.Duration}
		for _, folder := range m.Folders {
			w := watcher.New(watcher.Config{
				Mailbox: mb, Folder: folder,
				Dial:      dial(m.Host, m.Port),
				Auth:      authn,
				Processor: proc, State: st, Alert: alerter, Log: log,
				PollInterval: m.PollInterval.Duration, AlertAfter: cfg.Defaults.AlertAfter.Duration,
			})
			s.watchers = append(s.watchers, w)
			s.sources = append(s.sources, healthAdapter{w})
		}
	}
	return s
}

// start runs the watchers, the dispatcher and the log pruner until ctx is
// cancelled; wait blocks until the watchers and the dispatcher have stopped.
func (s *service) start(ctx context.Context) (wait func()) {
	var wg sync.WaitGroup
	for _, w := range s.watchers {
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(ctx) }()
	}
	dispDone := make(chan struct{})
	go func() { s.disp.Run(ctx); close(dispDone) }()
	go pruneLoop(ctx, s.st, s.log)
	return func() {
		wg.Wait()
		<-dispDone
	}
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
