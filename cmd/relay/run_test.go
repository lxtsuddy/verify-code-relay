package main

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	// 存活與就緒不受 IMAP 連線影響：資料庫可用即就緒
	for _, path := range []string{"/livez", "/readyz"} {
		resp, err := http.Get("http://" + httpAddr + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, resp.StatusCode)
		}
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
