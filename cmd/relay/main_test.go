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
	cfg := `data_dir: ` + filepath.Join(dir, "data") + `
bots: { main: { token: "123:abc" } }
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
	p := writeConfig(t, "")
	code, stdout, stderr := run("--config", p, "test", "--eml", "../../testdata/eml/github-device-en.eml", "--mailbox", "gmail")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
	for _, want := range []string{"關鍵字", "482913", "<code>482913</code>", "個人 Gmail", "copy_text"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	code, stdout, _ = run("--config", p, "test", "--eml", "../../testdata/eml/newsletter-en.eml")
	if code != 0 || !strings.Contains(stdout, "不轉發") {
		t.Fatalf("code=%d out=%q", code, stdout)
	}
}

func TestTestCommandWithoutMailbox(t *testing.T) {
	p := writeConfig(t, "")
	code, stdout, stderr := run("--config", p, "test", "--eml", "../../testdata/eml/github-device-en.eml")
	if code != 0 || !strings.Contains(stdout, "<code>482913</code>") || !strings.Contains(stdout, "（測試）") ||
		strings.Contains(stdout, "個人 Gmail") {
		t.Fatalf("code=%d out=%q err=%q", code, stdout, stderr)
	}
}

func TestTestCommandUnknownNames(t *testing.T) {
	p := writeConfig(t, "")
	eml := "../../testdata/eml/github-device-en.eml"
	code, _, stderr := run("--config", p, "test", "--eml", eml, "--mailbox", "nope")
	if code != 1 || !strings.Contains(stderr, `找不到信箱 "nope"`) {
		t.Fatalf("--mailbox: code=%d err=%q", code, stderr)
	}
	code, _, stderr = run("--config", p, "test", "--eml", eml, "--send", "nobody")
	if code != 1 || !strings.Contains(stderr, `找不到目標 "nobody"`) {
		t.Fatalf("--send: code=%d err=%q", code, stderr)
	}
	code, _, stderr = run("--config", p, "test")
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
	p := writeConfig(t, "")
	code, stdout, stderr := run("--config", p, "test", "--eml", "../../testdata/eml/github-device-en.eml",
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
	p := writeConfig(t, "")
	code, stdout, stderr := run("--config", p, "send-test", "me")
	if code != 0 || !strings.Contains(stdout, "已送出") || got["chat_id"].(float64) != 42 {
		t.Fatalf("code=%d out=%q err=%q got=%v", code, stdout, stderr, got)
	}
	code, _, _ = run("--config", p, "send-test", "nobody")
	if code != 1 {
		t.Fatalf("unknown target code=%d", code)
	}
}

func TestLoginRejectsPasswordMailbox(t *testing.T) {
	keepUmask(t)
	p := writeConfig(t, "")
	code, _, stderr := run("--config", p, "login", "gmail")
	if code != 1 || !strings.Contains(stderr, "microsoft_oauth") {
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

// 未指定 --url 時依 http_listen 推導網址；只有埠號（":8080"）時連到 127.0.0.1。
func TestHealthcheckURLFromConfig(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	addr := srv.Listener.Addr().(*net.TCPAddr)

	for _, listen := range []string{addr.String(), fmt.Sprintf(":%d", addr.Port)} {
		p := writeConfig(t, "http_listen: \""+listen+"\"\n")
		if code, _, stderr := run("--config", p, "healthcheck"); code != 0 {
			t.Errorf("http_listen %q: code=%d err=%q", listen, code, stderr)
		}
	}
	status.Store(http.StatusServiceUnavailable)
	p := writeConfig(t, "http_listen: \""+addr.String()+"\"\n")
	if code, _, stderr := run("--config", p, "healthcheck"); code != 1 || !strings.Contains(stderr, "503") {
		t.Errorf("503: code=%d err=%q", code, stderr)
	}

	p = writeConfig(t, "")
	if code, _, stderr := run("--config", p, "healthcheck"); code != 1 || !strings.Contains(stderr, "http_listen 未設定") {
		t.Errorf("no http_listen: code=%d err=%q", code, stderr)
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
}

// 參數放在位置參數之後也要能解析（relay login outlook --config x）。
func TestFlagsAfterPositional(t *testing.T) {
	keepUmask(t)
	p := writeConfig(t, "")
	code, _, stderr := run("login", "gmail", "--config", p)
	if code != 1 || !strings.Contains(stderr, "microsoft_oauth") {
		t.Fatalf("login: code=%d err=%q", code, stderr)
	}

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("RELAY_TELEGRAM_API", srv.URL)
	code, stdout, stderr := run("send-test", "me", "-config="+p)
	if code != 0 || !strings.Contains(stdout, "已送出") || got["chat_id"] != float64(42) {
		t.Fatalf("send-test: code=%d out=%q err=%q got=%v", code, stdout, stderr, got)
	}

	// 位置參數仍只能有一個；「--」之後的一律視為位置參數
	for _, args := range [][]string{
		{"--config", p, "send-test", "me", "extra"},
		{"--config", p, "send-test", "me", "--", "--config"},
	} {
		if code, _, stderr := run(args...); code != 2 || !strings.Contains(stderr, "用法") {
			t.Errorf("%v: code=%d err=%q", args, code, stderr)
		}
	}
	code, _, stderr = run("--config", p, "send-test", "--", "nobody")
	if code != 1 || !strings.Contains(stderr, `找不到目標 "nobody"`) {
		t.Fatalf("send-test -- nobody: code=%d err=%q", code, stderr)
	}
}
