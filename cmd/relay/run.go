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

	"verify-code-relay/internal/health"
	"verify-code-relay/internal/runtime"
)

func cmdRun(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("run", &configPath, stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	restrictUmask()
	b, err := loadApp(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "設定檔有誤（%s）：\n%v\n", configPath, err)
		return 1
	}
	cfg := b.Config
	level := new(slog.LevelVar) // runtime.Apply sets it from log_level
	log := newLogger(level, stdout)
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

	rt := runtime.New(runtime.Options{Store: st, Log: log, Level: level, Sender: telegramClient(), Dial: runtime.TLSDial})
	rt.Apply(b)

	var httpSrv *http.Server
	if httpLn != nil {
		httpSrv = &http.Server{Handler: health.Handler(rt.Sources, st.Ping), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := httpSrv.Serve(httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("HTTP 服務失敗", "err", err)
			}
		}()
	}
	log.Info("服務已啟動", "watchers", len(rt.Snapshot().Watchers), "http", cfg.HTTPListen)

	<-ctx.Done()
	stop() // restore default signal handling: a second signal kills a hung shutdown
	log.Info("收到停止訊號，正在關閉")
	rt.Stop()
	if httpSrv != nil {
		shutdownHTTP(httpSrv, 5*time.Second, log)
	}
	log.Info("已停止")
	return 0
}

func shutdownHTTP(srv *http.Server, timeout time.Duration, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("關閉 HTTP 服務失敗", "err", err)
	}
}
