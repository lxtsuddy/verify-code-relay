package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"verify-code-relay/internal/dispatcher"
	"verify-code-relay/internal/store"
)

// TestRunSmoke boots the full service with an unreachable IMAP host and checks that
// startup, /healthz (503 while not connected) and graceful shutdown work.
func TestRunSmoke(t *testing.T) {
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	t.Setenv("RELAY_TELEGRAM_API", tg.URL)
	keepUmask(t)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	httpAddr := ln.Addr().String()
	ln.Close()
	p := writeConfig(t, "http_listen: \""+httpAddr+"\"\n")
	// point the mailbox at a closed port so the watcher keeps reconnecting
	cfg, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(cfg), "host: imap.gmail.com", "host: 127.0.0.1\n    port: 1", 1)), 0o600)

	done := make(chan int, 1)
	proc, _ := os.FindProcess(os.Getpid())
	go func() { done <- realMain([]string{"--config", p, "run"}, io.Discard, io.Discard) }()

	var code int
	for i := 0; i < 100; i++ {
		resp, err := http.Get("http://" + httpAddr + "/healthz")
		if err == nil {
			code = resp.StatusCode
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d, want 503 while IMAP is unreachable", code)
	}
	// run 會收緊 umask；資料庫與 -wal／-shm 檔（服務執行中才存在）都不可讓其他人讀取
	if m := umaskNow(); m != 0o077 {
		t.Errorf("umask while running = %#o, want 077", m)
	}
	dataDir := filepath.Join(filepath.Dir(p), "data")
	if _, err := os.Stat(filepath.Join(dataDir, "relay.db")); err != nil {
		t.Fatalf("db not created: %v", err)
	}
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s mode %v, want no group/other bits", e.Name(), fi.Mode().Perm())
		}
	}
	proc.Signal(os.Interrupt)
	select {
	case c := <-done:
		if c != 0 {
			t.Fatalf("exit code %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not shut down")
	}
}

// TestRunFailsFastOnHTTPBindError: a port already in use must stop startup with exit 1.
func TestRunFailsFastOnHTTPBindError(t *testing.T) {
	keepUmask(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	p := writeConfig(t, "http_listen: \""+occupied.Addr().String()+"\"\n")
	cfg, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(cfg), "host: imap.gmail.com", "host: 127.0.0.1\n    port: 1", 1)), 0o600)

	type result struct {
		code   int
		stderr string
	}
	done := make(chan result, 1)
	go func() {
		var errb strings.Builder
		c := realMain([]string{"--config", p, "run"}, io.Discard, &errb)
		done <- result{c, errb.String()}
	}()
	select {
	case r := <-done:
		if r.code != 1 || r.stderr == "" {
			t.Fatalf("code=%d stderr=%q, want 1 with an error message", r.code, r.stderr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not fail fast on HTTP bind error")
		// (the leaked run goroutine is acceptable: the test has already failed)
	}
}

// slowPruner blocks until shutdown, then takes a while to return.
type slowPruner struct{ returned atomic.Bool }

func (p *slowPruner) PruneLog(ctx context.Context, _ time.Time) (int64, error) {
	<-ctx.Done()
	time.Sleep(100 * time.Millisecond)
	p.returned.Store(true)
	return 0, ctx.Err()
}

// TestStartWaitsForPruneLoop: wait must not return while pruneLoop may still use the
// store (cmdRun closes it right after).
func TestStartWaitsForPruneLoop(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.DiscardHandler)
	p := &slowPruner{}
	s := &service{st: p, log: log, disp: dispatcher.New(dispatcher.Options{Store: st, Log: log})}
	ctx, cancel := context.WithCancel(context.Background())
	wait := s.start(ctx)
	cancel()
	wait()
	if !p.returned.Load() {
		t.Fatal("wait returned before pruneLoop stopped")
	}
}

type fakePruner struct {
	n      int64
	err    error
	before chan time.Time
}

func (p *fakePruner) PruneLog(_ context.Context, before time.Time) (int64, error) {
	p.before <- before
	return p.n, p.err
}

type recLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *recLog) Info(msg string, _ ...any) { l.add("INFO " + msg) }
func (l *recLog) Warn(msg string, _ ...any) { l.add("WARN " + msg) }
func (l *recLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, s)
}

func TestPruneLoop(t *testing.T) {
	for _, tc := range []struct {
		name      string
		n         int64
		err       error
		cancelled bool // ctx already cancelled: errors are expected and not logged
		want      string
	}{
		{name: "pruned", n: 3, want: "INFO 已清理舊轉發紀錄"},
		{name: "nothing", want: ""},
		{name: "error", err: errors.New("disk I/O error"), want: "WARN 清理轉發紀錄失敗"},
		{name: "error after cancel", err: context.Canceled, cancelled: true, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePruner{n: tc.n, err: tc.err, before: make(chan time.Time, 1)}
			log := &recLog{}
			ctx, cancel := context.WithCancel(context.Background())
			if tc.cancelled {
				cancel()
			}
			done := make(chan struct{})
			go func() { pruneLoop(ctx, p, log); close(done) }()
			before := <-p.before
			cancel()
			<-done
			if d := time.Since(before) - 30*24*time.Hour; d < 0 || d > time.Minute {
				t.Errorf("before = %v, want about 30 days ago", before)
			}
			got := strings.Join(log.lines, "\n")
			if got != tc.want {
				t.Errorf("log = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestShutdownHTTPLogsError: a shutdown that times out on an active request is logged.
func TestShutdownHTTPLogsError(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer close(release)
	go func() {
		if resp, err := http.Get("http://" + ln.Addr().String()); err == nil {
			resp.Body.Close()
		}
	}()
	<-started

	var buf bytes.Buffer
	shutdownHTTP(srv, 50*time.Millisecond, slog.New(slog.NewJSONHandler(&buf, nil)))
	if !strings.Contains(buf.String(), "關閉 HTTP 服務失敗") || !strings.Contains(buf.String(), "deadline") {
		t.Fatalf("log = %q", buf.String())
	}
}
