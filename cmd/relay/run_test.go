package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
)

// freePort returns a 127.0.0.1 address that was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// syncBuffer is a bytes.Buffer safe to write from run's goroutine and read from the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// startRun starts `relay run` with a fresh RELAY_DATA_DIR (unless already set), free
// ports and the given admin password, waits until /readyz is 200 and returns the
// health and web addresses, the log, and a stop function that signals shutdown and
// returns the exit code.
func startRun(t *testing.T, adminPassword string) (healthAddr, webAddr string, log *syncBuffer, stop func() int) {
	t.Helper()
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(tg.Close)
	t.Setenv("RELAY_TELEGRAM_API", tg.URL)
	keepUmask(t)
	if os.Getenv("RELAY_DATA_DIR") == "" {
		t.Setenv("RELAY_DATA_DIR", t.TempDir())
	}
	healthAddr, webAddr = freePort(t), freePort(t)
	t.Setenv("RELAY_LISTEN", webAddr)
	t.Setenv("RELAY_HEALTH_LISTEN", healthAddr)
	t.Setenv("RELAY_ADMIN_PASSWORD", adminPassword)

	log = &syncBuffer{}
	done := make(chan int, 1)
	go func() { done <- realMain([]string{"run"}, log, log) }()
	ready := false
	for i := 0; i < 100 && !ready; i++ {
		resp, err := http.Get("http://" + healthAddr + "/readyz")
		if err == nil {
			ready = resp.StatusCode == http.StatusOK
			resp.Body.Close()
		}
		if !ready {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !ready {
		t.Fatalf("/readyz never returned 200; log:\n%s", log.String())
	}
	stopped := false
	stop = func() int {
		stopped = true
		select {
		case c := <-done: // already exited: signalling now would kill the test binary
			return c
		default:
		}
		proc, _ := os.FindProcess(os.Getpid())
		proc.Signal(os.Interrupt)
		select {
		case c := <-done:
			return c
		case <-time.After(10 * time.Second):
			t.Fatal("did not shut down")
			return -1
		}
	}
	t.Cleanup(func() {
		if !stopped {
			stop()
		}
	})
	return healthAddr, webAddr, log, stop
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestRunSmoke boots the full service from an empty database and checks that the admin
// is created from RELAY_ADMIN_PASSWORD, the health endpoints answer on
// RELAY_HEALTH_LISTEN, the web placeholder on RELAY_LISTEN, and shutdown is graceful.
func TestRunSmoke(t *testing.T) {
	t.Setenv("RELAY_DATA_DIR", t.TempDir()) // never the developer's real data dir
	healthAddr, webAddr, log, stop := startRun(t, "a-long-enough-password")

	// 沒有信箱：全部健康
	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		if c := getStatus(t, "http://"+healthAddr+path); c != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, c)
		}
	}
	// the admin UI is up: / redirects (followed by http.Get) to the login page
	if resp, err := http.Get("http://" + webAddr + "/"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/login" {
			t.Errorf("web / = %d at %s, want the login page", resp.StatusCode, resp.Request.URL.Path)
		}
	}
	// run 會收緊 umask；資料庫與 -wal／-shm 檔（服務執行中才存在）都不可讓其他人讀取
	if m := umaskNow(); m != 0o077 {
		t.Errorf("umask while running = %#o, want 077", m)
	}
	dataDir := os.Getenv("RELAY_DATA_DIR")
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
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := st.GetAdmin(context.Background())
	st.Close()
	if err != nil || !ok {
		t.Fatalf("admin not created: %v", err)
	}
	if c := stop(); c != 0 {
		t.Fatalf("exit code %d", c)
	}
	if strings.Contains(log.String(), "a-long-enough-password") || !strings.Contains(log.String(), "已建立管理員") {
		t.Errorf("log = %s", log.String())
	}
}

// TestRunWithImportedConfig: run uses the stored config; an unreachable mailbox makes
// /healthz 503 while /livez and /readyz stay 200.
func TestRunWithImportedConfig(t *testing.T) {
	importConfig(t, "")
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	// point the mailbox at a closed port so the watcher keeps reconnecting
	raw, _, err := runtime.LoadConfig(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	raw.Mailboxes["gmail"].Host, raw.Mailboxes["gmail"].Port = "127.0.0.1", 1
	if _, _, err := runtime.SaveConfig(context.Background(), st, raw, runtime.Meta{Source: "test", Summary: "x"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	healthAddr, _, log, stop := startRun(t, "")
	if c := getStatus(t, "http://"+healthAddr+"/healthz"); c != http.StatusServiceUnavailable {
		t.Errorf("/healthz = %d, want 503 while IMAP is unreachable", c)
	}
	if c := getStatus(t, "http://"+healthAddr+"/livez"); c != http.StatusOK {
		t.Errorf("/livez = %d", c)
	}
	if c := stop(); c != 0 {
		t.Fatalf("exit code %d", c)
	}
	if !strings.Contains(log.String(), "尚未設定管理員") || !strings.Contains(log.String(), `"watchers":1`) {
		t.Errorf("log = %s", log.String())
	}
}

// TestRunFallsBackOnInvalidStoredConfig: a stored version that no longer builds must not
// keep the service (and so the web UI) down: run starts with an empty config instead.
func TestRunFallsBackOnInvalidStoredConfig(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.SaveConfigVersion(context.Background(), store.ConfigVersion{CreatedAt: time.Now(), Source: "test", Summary: "bad",
		YAML: "targets: { me: { bot: ghost, chat_id: 5 } }\n"})
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	healthAddr, _, log, stop := startRun(t, "a-long-enough-password")
	if c := getStatus(t, "http://"+healthAddr+"/healthz"); c != http.StatusOK {
		t.Errorf("/healthz = %d", c)
	}
	if c := stop(); c != 0 {
		t.Fatalf("exit code %d", c)
	}
	if !strings.Contains(log.String(), "資料庫中的設定無效") || !strings.Contains(log.String(), "targets.me.bot") {
		t.Errorf("log = %s", log.String())
	}
}

// TestRunWithoutHealthListener: RELAY_HEALTH_LISTEN="" disables the health server.
func TestRunWithoutHealthListener(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	webAddr := freePort(t)
	t.Setenv("RELAY_LISTEN", webAddr)
	t.Setenv("RELAY_HEALTH_LISTEN", "")
	done := make(chan int, 1)
	go func() { done <- realMain([]string{"run"}, io.Discard, io.Discard) }()
	up := false
	for i := 0; i < 100 && !up; i++ {
		if resp, err := http.Get("http://" + webAddr + "/"); err == nil {
			resp.Body.Close()
			up = true
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !up {
		t.Fatal("web server not up")
	}
	proc, _ := os.FindProcess(os.Getpid())
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

// TestRunFailsFastOnBindError: a port already in use (web or health) must stop startup with exit 1.
func TestRunFailsFastOnBindError(t *testing.T) {
	keepUmask(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	for _, env := range []string{"RELAY_LISTEN", "RELAY_HEALTH_LISTEN"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("RELAY_DATA_DIR", t.TempDir())
			t.Setenv("RELAY_LISTEN", freePort(t))
			t.Setenv("RELAY_HEALTH_LISTEN", freePort(t))
			t.Setenv(env, occupied.Addr().String())
			type result struct {
				code   int
				stderr string
			}
			done := make(chan result, 1)
			go func() {
				var errb strings.Builder
				c := realMain([]string{"run"}, io.Discard, &errb)
				done <- result{c, errb.String()}
			}()
			select {
			case r := <-done:
				if r.code != 1 || !strings.Contains(r.stderr, env) {
					t.Fatalf("code=%d stderr=%q, want 1 naming %s", r.code, r.stderr, env)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("run did not fail fast on bind error")
				// (the leaked run goroutine is acceptable: the test has already failed)
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

// TestRunRejectsBadTrustedProxies: a typo in RELAY_TRUSTED_PROXIES fails startup clearly.
func TestRunRejectsBadTrustedProxies(t *testing.T) {
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	t.Setenv("RELAY_TRUSTED_PROXIES", "10.42.0.0/16, not-an-ip")
	var out, errb bytes.Buffer
	if c := realMain([]string{"run"}, &out, &errb); c != 1 {
		t.Fatalf("exit code %d", c)
	}
	if !strings.Contains(errb.String(), "RELAY_TRUSTED_PROXIES") || !strings.Contains(errb.String(), "not-an-ip") {
		t.Errorf("stderr = %s", errb.String())
	}
}
