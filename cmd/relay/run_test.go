package main

import (
	"io"
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
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), "data", "relay.db")); err != nil {
		t.Fatalf("db not created: %v", err)
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
