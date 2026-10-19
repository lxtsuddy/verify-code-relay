package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
