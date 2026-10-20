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
	"syscall"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/health"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/web"
	"verify-code-relay/internal/websec"
)

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("run", stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "用法：relay run")
		return 2
	}
	proxies, err := websec.ParsePrefixes(os.Getenv("RELAY_TRUSTED_PROXIES"))
	if err != nil {
		return fail(stderr, fmt.Errorf("RELAY_TRUSTED_PROXIES 格式不正確：%w（應為以逗號分隔的 CIDR 或 IP，例如 10.42.0.0/16,192.168.1.5）", err))
	}
	restrictUmask()
	level := new(slog.LevelVar) // runtime.Apply sets it from log_level
	log := newLogger(level, stdout)

	st, err := openDB()
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()
	if err := ensureAdmin(context.Background(), st, log); err != nil {
		return fail(stderr, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Bind synchronously so a port conflict or bad address fails startup instead of
	// silently running without the web UI or health checks.
	webAddr, hAddr := listenAddr(), healthAddr()
	webLn, err := net.Listen("tcp", webAddr)
	if err != nil {
		return fail(stderr, fmt.Errorf("RELAY_LISTEN %q：%w", webAddr, err))
	}
	defer webLn.Close()
	var healthLn net.Listener
	if hAddr != "" {
		healthLn, err = net.Listen("tcp", hAddr)
		if err != nil {
			return fail(stderr, fmt.Errorf("RELAY_HEALTH_LISTEN %q：%w", hAddr, err))
		}
		defer healthLn.Close()
	}

	rt := runtime.New(runtime.Options{Store: st, Log: log, Level: level, Sender: telegramClient(), Dial: runtime.TLSDial})
	b, err := buildStored(context.Background(), st)
	if err != nil {
		log.Error("資料庫中的設定無效，改以空設定啟動，請到 Web 修正", "err", err)
		if b, err = runtime.Build(config.Empty()); err != nil {
			return fail(stderr, fmt.Errorf("空設定：%w", err)) // cannot happen: the empty config is valid
		}
	}
	rt.Apply(b)

	admin := web.New(web.Options{Store: st, Runtime: rt, Log: log, Telegram: telegramClient(), TrustedProxies: proxies, Dial: runtime.TLSDial})
	go admin.PruneLoop(ctx)
	webSrv := serveHTTP(webLn, admin.Handler(), "Web 服務失敗", log)
	var healthSrv *http.Server
	if healthLn != nil {
		healthSrv = serveHTTP(healthLn, health.Handler(rt.Sources, st.Ping), "健康檢查服務失敗", log)
	}
	log.Info("服務已啟動", "watchers", len(rt.Snapshot().Watchers), "web", webAddr, "health", hAddr)

	<-ctx.Done()
	stop() // restore default signal handling: a second signal kills a hung shutdown
	log.Info("收到停止訊號，正在關閉")
	rt.Stop()
	shutdownHTTP(webSrv, 5*time.Second, log)
	if healthSrv != nil {
		shutdownHTTP(healthSrv, 5*time.Second, log)
	}
	log.Info("已停止")
	return 0
}

// buildStored loads and builds the latest stored config.
func buildStored(ctx context.Context, st *store.Store) (*runtime.Built, error) {
	raw, _, err := runtime.LoadConfig(ctx, st)
	if err != nil {
		return nil, err
	}
	return runtime.Build(raw)
}

func serveHTTP(ln net.Listener, h http.Handler, failMsg string, log *slog.Logger) *http.Server {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(failMsg, "err", err)
		}
	}()
	return srv
}

func shutdownHTTP(srv *http.Server, timeout time.Duration, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("關閉 HTTP 服務失敗", "err", err)
	}
}
