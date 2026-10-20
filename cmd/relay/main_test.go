package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func writeConfig(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	cfg := `bots: { main: { token: "123:abc" } }
targets: { me: { bot: main, chat_id: 42 } }
mailboxes:
  gmail:
    display_name: 個人 Gmail
    host: imap.gmail.com
    username: me@gmail.com
    auth: { type: password, password: pw }
    targets: [me]
` + extra
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// importConfig points RELAY_DATA_DIR at a fresh directory and imports the sample config
// (plus extra) into its database, as an operator would before using the CLI.
func importConfig(t *testing.T, extra string) {
	t.Helper()
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	p := writeConfig(t, extra)
	if code, stdout, stderr := run("config", "import", p); code != 0 {
		t.Fatalf("import: code=%d out=%q err=%q", code, stdout, stderr)
	}
}

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := realMain(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsage(t *testing.T) {
	code, _, stderr := run()
	if code != 2 || !strings.Contains(stderr, "用法") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	code, _, _ = run("bogus")
	if code != 2 {
		t.Fatalf("unknown command code=%d", code)
	}
}

func TestConfigValidate(t *testing.T) {
	p := writeConfig(t, "")
	code, stdout, stderr := run("--config", p, "config", "validate")
	if code != 0 || !strings.Contains(stdout, "設定檔正確") || !strings.Contains(stdout, "信箱 1") {
		t.Fatalf("code=%d out=%q err=%q", code, stdout, stderr)
	}
	bad := writeConfig(t, "admin: nobody\n")
	code, _, stderr = run("config", "validate", "--config", bad)
	if code != 1 || !strings.Contains(stderr, `admin: 找不到目標 "nobody"`) {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
	badTpl := writeConfig(t, "templates: { code: \"{{.Nope}}\" }\n")
	code, _, stderr = run("--config", badTpl, "config", "validate")
	if code != 1 || !strings.Contains(stderr, "templates.code") {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
}

func TestTestCommandPrintsTraceAndMessage(t *testing.T) {
	importConfig(t, "")
	code, stdout, stderr := run("test", "--eml", "../../testdata/eml/github-device-en.eml", "--mailbox", "gmail")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
	for _, want := range []string{"關鍵字", "482913", "<code>482913</code>", "個人 Gmail", "copy_text"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	code, stdout, _ = run("test", "--eml", "../../testdata/eml/newsletter-en.eml")
	if code != 0 || !strings.Contains(stdout, "不轉發") {
		t.Fatalf("code=%d out=%q", code, stdout)
	}
}

func TestTestCommandWithoutMailbox(t *testing.T) {
	importConfig(t, "")
	code, stdout, stderr := run("test", "--eml", "../../testdata/eml/github-device-en.eml")
	if code != 0 || !strings.Contains(stdout, "<code>482913</code>") || !strings.Contains(stdout, "（測試）") ||
		strings.Contains(stdout, "個人 Gmail") {
		t.Fatalf("code=%d out=%q err=%q", code, stdout, stderr)
	}
}

func TestTestCommandUnknownNames(t *testing.T) {
	importConfig(t, "")
	eml := "../../testdata/eml/github-device-en.eml"
	code, _, stderr := run("test", "--eml", eml, "--mailbox", "nope")
	if code != 1 || !strings.Contains(stderr, `找不到信箱 "nope"`) {
		t.Fatalf("--mailbox: code=%d err=%q", code, stderr)
	}
	code, _, stderr = run("test", "--eml", eml, "--send", "nobody")
	if code != 1 || !strings.Contains(stderr, `找不到目標 "nobody"`) {
		t.Fatalf("--send: code=%d err=%q", code, stderr)
	}
	code, _, stderr = run("test")
	if code != 2 || !strings.Contains(stderr, "用法") {
		t.Fatalf("no --eml: code=%d err=%q", code, stderr)
	}
}

func TestTestCommandSend(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("RELAY_TELEGRAM_API", srv.URL)
	importConfig(t, "")
	code, stdout, stderr := run("test", "--eml", "../../testdata/eml/github-device-en.eml",
		"--mailbox", "gmail", "--send", "me")
	if code != 0 || !strings.Contains(stdout, "已送出到目標 me") {
		t.Fatalf("code=%d out=%q err=%q", code, stdout, stderr)
	}
	text, _ := got["text"].(string)
	if got["chat_id"] != float64(42) || got["parse_mode"] != "HTML" || !strings.Contains(text, "<code>482913</code>") ||
		!strings.Contains(text, "個人 Gmail") || got["reply_markup"] == nil {
		t.Fatalf("sent %v", got)
	}
}

func TestSendTestUsesTelegram(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("RELAY_TELEGRAM_API", srv.URL) // test hook, see app.go
	importConfig(t, "")
	code, stdout, stderr := run("send-test", "me")
	if code != 0 || !strings.Contains(stdout, "已送出") || got["chat_id"].(float64) != 42 {
		t.Fatalf("code=%d out=%q err=%q got=%v", code, stdout, stderr, got)
	}
	code, _, _ = run("send-test", "nobody")
	if code != 1 {
		t.Fatalf("unknown target code=%d", code)
	}
}

func TestLoginRejectsPasswordMailbox(t *testing.T) {
	importConfig(t, "")
	code, _, stderr := run("login", "gmail")
	if code != 1 || !strings.Contains(stderr, "microsoft_oauth") {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
	code, _, stderr = run("login", "nope")
	if code != 1 || !strings.Contains(stderr, `找不到信箱 "nope"`) {
		t.Fatalf("unknown mailbox: code=%d err=%q", code, stderr)
	}
}

// CLI commands that read the config use the database: with nothing imported there are
// no targets, and the error says how to add one.
func TestSendTestWithEmptyDatabase(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	code, _, stderr := run("send-test", "me")
	if code != 1 || !strings.Contains(stderr, `找不到目標 "me"`) || !strings.Contains(stderr, "config import") {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
}

func TestHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	code, _, _ := run("healthcheck", "--url", srv.URL+"/healthz")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	code, _, _ = run("healthcheck", "--url", srv.URL+"/nope")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
}

// 未指定 --url 時依 RELAY_HEALTH_LISTEN 推導 /readyz 網址；只有埠號（":8081"）時連到 127.0.0.1。
func TestHealthcheckURLFromEnv(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().(*net.TCPAddr)

	for _, listen := range []string{addr.String(), fmt.Sprintf(":%d", addr.Port), fmt.Sprintf("0.0.0.0:%d", addr.Port)} {
		t.Setenv("RELAY_HEALTH_LISTEN", listen)
		if code, _, stderr := run("healthcheck"); code != 0 {
			t.Errorf("RELAY_HEALTH_LISTEN %q: code=%d err=%q", listen, code, stderr)
		}
	}
	status.Store(http.StatusServiceUnavailable)
	t.Setenv("RELAY_HEALTH_LISTEN", addr.String())
	if code, _, stderr := run("healthcheck"); code != 1 || !strings.Contains(stderr, "503") {
		t.Errorf("503: code=%d err=%q", code, stderr)
	}

	t.Setenv("RELAY_HEALTH_LISTEN", "")
	if code, _, stderr := run("healthcheck"); code != 1 || !strings.Contains(stderr, "RELAY_HEALTH_LISTEN 未啟用") {
		t.Errorf("disabled: code=%d err=%q", code, stderr)
	}
}

func TestHealthURL(t *testing.T) {
	for listen, want := range map[string]string{
		"127.0.0.1:9": "http://127.0.0.1:9/readyz",
		":8081":       "http://127.0.0.1:8081/readyz",
		"0.0.0.0:1":   "http://127.0.0.1:1/readyz",
		"[::]:2":      "http://[::1]:2/readyz",
		"[::1]:3":     "http://[::1]:3/readyz",
		"health:4":    "http://health:4/readyz",
	} {
		if got := healthURL(listen); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", listen, got, want)
		}
	}
}

func TestEnvDefaults(t *testing.T) {
	for _, k := range []string{"RELAY_DATA_DIR", "RELAY_LISTEN", "RELAY_HEALTH_LISTEN"} {
		t.Setenv(k, "x") // registers restoration; then unset for the test
		os.Unsetenv(k)
	}
	if dataDir() != "/data" || listenAddr() != ":8080" || healthAddr() != ":8081" {
		t.Fatalf("defaults = %q %q %q", dataDir(), listenAddr(), healthAddr())
	}
	t.Setenv("RELAY_DATA_DIR", "/tmp/x")
	t.Setenv("RELAY_LISTEN", "127.0.0.1:1")
	t.Setenv("RELAY_HEALTH_LISTEN", "")
	if dataDir() != "/tmp/x" || listenAddr() != "127.0.0.1:1" || healthAddr() != "" {
		t.Fatalf("from env = %q %q %q", dataDir(), listenAddr(), healthAddr())
	}
}

func TestGlobalConfigEqualsForm(t *testing.T) {
	p := writeConfig(t, "")
	for _, flagName := range []string{"--config=", "-config="} {
		code, stdout, stderr := run(flagName+p, "config", "validate")
		if code != 0 || !strings.Contains(stdout, "設定檔正確") {
			t.Fatalf("%s: code=%d out=%q err=%q", flagName, code, stdout, stderr)
		}
	}
	// 其他指令改從資料庫讀設定，明確指定 --config 時報錯而不是默默忽略
	for _, cmd := range []string{"run", "send-test", "login", "test", "healthcheck", "admin"} {
		if code, _, stderr := run("--config", p, cmd); code != 2 || !strings.Contains(stderr, "--config") {
			t.Errorf("%s: code=%d err=%q", cmd, code, stderr)
		}
	}
}

// RELAY_CONFIG（舊部署仍會設定）不影響不使用設定檔的指令。
func TestRelayConfigEnvIgnoredByOtherCommands(t *testing.T) {
	importConfig(t, "")
	t.Setenv("RELAY_CONFIG", "/nonexistent.yaml")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer srv.Close()
	t.Setenv("RELAY_TELEGRAM_API", srv.URL)
	if code, _, stderr := run("send-test", "me"); code != 0 {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
}

// config validate：位置參數優先於 --config，--config 優先於 RELAY_CONFIG。
func TestConfigValidateSources(t *testing.T) {
	good := writeConfig(t, "")
	bad := writeConfig(t, "admin: nobody\n")
	t.Setenv("RELAY_CONFIG", bad)
	if code, _, _ := run("config", "validate"); code != 1 {
		t.Errorf("RELAY_CONFIG: code=%d, want 1", code)
	}
	if code, _, stderr := run("config", "validate", "--config", good); code != 0 {
		t.Errorf("--config over RELAY_CONFIG: code=%d err=%q", code, stderr)
	}
	if code, _, stderr := run("config", "validate", good, "--config", bad); code != 0 {
		t.Errorf("positional over --config: code=%d err=%q", code, stderr)
	}
	if code, _, stderr := run("config", "validate", good, bad); code != 2 || !strings.Contains(stderr, "用法") {
		t.Errorf("two files: code=%d err=%q", code, stderr)
	}
}

// 位置參數只能有一個；「--」之後的一律視為位置參數。
func TestSendTestPositionals(t *testing.T) {
	importConfig(t, "")
	for _, args := range [][]string{
		{"send-test", "me", "extra"},
		{"send-test", "me", "--", "--config"},
		{"send-test"},
	} {
		if code, _, stderr := run(args...); code != 2 || !strings.Contains(stderr, "用法") {
			t.Errorf("%v: code=%d err=%q", args, code, stderr)
		}
	}
	code, _, stderr := run("send-test", "--", "nobody")
	if code != 1 || !strings.Contains(stderr, `找不到目標 "nobody"`) {
		t.Fatalf("send-test -- nobody: code=%d err=%q", code, stderr)
	}
}
