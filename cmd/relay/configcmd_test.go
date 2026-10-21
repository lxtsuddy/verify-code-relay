package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/store"
)

func TestConfigImportExport(t *testing.T) {
	keepUmask(t)
	dir := t.TempDir()
	t.Setenv("RELAY_DATA_DIR", dir)
	t.Setenv("TG", "1:secret")
	yml := filepath.Join(dir, "c.yaml")
	os.WriteFile(yml, []byte(`data_dir: /elsewhere
http_listen: ":9"
bots: { main: { token: "${TG}" } }
targets: { me: { bot: main, chat_id: 5 } }
`), 0o600)
	var out, errb bytes.Buffer
	if code := realMain([]string{"config", "import", yml}, &out, &errb); code != 0 {
		t.Fatalf("import = %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String()+errb.String(), "data_dir") {
		t.Error("expected a notice that data_dir/http_listen are ignored")
	}
	if !strings.Contains(out.String(), "已匯入為版本 #1") || strings.Contains(out.String()+errb.String(), "1:secret") {
		t.Errorf("import output = %q / %q", out.String(), errb.String())
	}
	out.Reset()
	if code := realMain([]string{"config", "export"}, &out, &errb); code != 0 {
		t.Fatalf("export = %d: %s", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "token: 1:secret") || strings.Contains(got, "data_dir") || strings.Contains(got, "http_listen") {
		t.Errorf("export =\n%s", got)
	}

	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, ok, err := st.LatestConfigVersion(context.Background())
	if err != nil || !ok || v.Source != "import" || v.Summary != "從 c.yaml 匯入" {
		t.Fatalf("version = %+v %v %v", v, ok, err)
	}
}

func TestConfigImportRejectsInvalid(t *testing.T) {
	keepUmask(t)
	dir := t.TempDir()
	t.Setenv("RELAY_DATA_DIR", dir)
	yml := filepath.Join(dir, "c.yaml")
	os.WriteFile(yml, []byte("targets: { me: { bot: ghost, chat_id: 5 } }\n"), 0o600)
	var out, errb bytes.Buffer
	if code := realMain([]string{"config", "import", yml}, &out, &errb); code == 0 {
		t.Fatal("invalid config imported")
	}
	if !strings.Contains(errb.String(), "targets.me.bot") {
		t.Errorf("stderr = %s", errb.String())
	}
}

func TestConfigImportUsage(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	t.Setenv("RELAY_CONFIG", "")
	if code, _, stderr := run("config", "import"); code != 2 || !strings.Contains(stderr, "用法") {
		t.Errorf("no file: code=%d err=%q", code, stderr)
	}
	if code, _, stderr := run("config", "import", "/nonexistent.yaml"); code != 1 || !strings.Contains(stderr, "讀取設定檔") {
		t.Errorf("missing file: code=%d err=%q", code, stderr)
	}
	// --config／RELAY_CONFIG 也可指定匯入的檔案
	p := writeConfig(t, "")
	if code, stdout, stderr := run("config", "import", "--config", p); code != 0 || !strings.Contains(stdout, "#1") {
		t.Errorf("--config: code=%d out=%q err=%q", code, stdout, stderr)
	}
	t.Setenv("RELAY_CONFIG", p)
	if code, stdout, stderr := run("config", "import"); code != 0 || !strings.Contains(stdout, "#2") {
		t.Errorf("RELAY_CONFIG: code=%d out=%q err=%q", code, stdout, stderr)
	}
	if code, _, stderr := run("config", "bogus"); code != 2 || !strings.Contains(stderr, "用法") {
		t.Errorf("unknown subcommand: code=%d err=%q", code, stderr)
	}
}

func TestConfigExportEmpty(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	code, stdout, stderr := run("config", "export")
	if code != 0 || !strings.Contains(stderr, "尚無設定") || strings.TrimSpace(stdout) != "{}" {
		t.Fatalf("code=%d out=%q err=%q", code, stdout, stderr)
	}
}

// importYAML writes body to a file and imports it, failing the test on a non-zero exit.
func importYAML(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run("config", "import", p)
	if code != 0 {
		t.Fatalf("import = %d: %s", code, stderr)
	}
	return stdout
}

const importBase = `bots: { main: { token: "1:x" } }
targets: { me: { bot: main, chat_id: 5 } }
`

func msBox(host, user string) string {
	return "{ host: " + host + ", username: " + user + `, auth: { type: microsoft_oauth, client_id: "00000000-0000-0000-0000-000000000001", tenant: consumers }, targets: [me] }`
}

// An import drops the OAuth tokens it made stale, like a save on the Web: a removed
// mailbox, another host or account, and a mailbox that just became Microsoft (any token
// stored for it is left over from earlier).
func TestConfigImportDropsStaleOAuthTokens(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	importYAML(t, importBase+`mailboxes:
  outlook: `+msBox("outlook.office365.com", "a@outlook.com")+`
  gone: `+msBox("outlook.office365.com", "b@outlook.com")+`
  same: `+msBox("outlook.office365.com", "c@outlook.com")+`
  pw: { host: imap.gmail.com, username: d@gmail.com, auth: { type: password, password: p }, targets: [me] }
`)
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	tok := store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}
	for _, name := range []string{"outlook", "gone", "same", "pw"} {
		if err := st.SaveOAuthToken(ctx, name, tok); err != nil {
			t.Fatal(err)
		}
	}
	out := importYAML(t, importBase+`mailboxes:
  outlook: `+msBox("imap-mail.outlook.com", "a@outlook.com")+`
  same: { display_name: Same, host: OUTLOOK.office365.com, port: 993, username: c@outlook.com, auth: { type: microsoft_oauth, client_id: "00000000-0000-0000-0000-000000000001", tenant: consumers }, targets: [me] }
  pw: `+msBox("outlook.office365.com", "d@outlook.com")+`
`)
	for _, name := range []string{"outlook", "gone", "pw"} {
		if _, ok, _ := st.GetOAuthToken(ctx, name); ok {
			t.Errorf("token of %s kept", name)
		}
		if !strings.Contains(out, "信箱 "+name+" 的 Microsoft 授權已清除，請重新授權") {
			t.Errorf("no notice for %s in %q", name, out)
		}
	}
	if _, ok, _ := st.GetOAuthToken(ctx, "same"); !ok {
		t.Error("token of an unchanged account dropped")
	}
	if strings.Contains(out, "信箱 same") {
		t.Errorf("notice for an unchanged account: %q", out)
	}
	// nothing stored: nothing to tell
	out = importYAML(t, importBase)
	if strings.Contains(out, "信箱 outlook") || !strings.Contains(out, "信箱 same 的 Microsoft 授權已清除") {
		t.Errorf("removing every mailbox = %q", out)
	}
}

// A rejected import keeps every token.
func TestConfigImportFailureKeepsOAuthTokens(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	importYAML(t, importBase+"mailboxes:\n  outlook: "+msBox("outlook.office365.com", "a@outlook.com")+"\n")
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	st.SaveOAuthToken(ctx, "outlook", store.OAuthToken{RefreshToken: "r"})
	p := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(p, []byte("targets: { me: { bot: ghost, chat_id: 5 } }\n"), 0o600)
	if code, _, _ := run("config", "import", p); code == 0 {
		t.Fatal("invalid config imported")
	}
	if _, ok, _ := st.GetOAuthToken(ctx, "outlook"); !ok {
		t.Fatal("a rejected import dropped a token")
	}
}

// When the stored config cannot be read, nothing tells which account a token was for: every
// imported Microsoft mailbox loses its token.
func TestConfigImportOverUnreadableConfigDropsTokens(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.SaveConfigVersion(ctx, store.ConfigVersion{CreatedAt: time.Now(), Source: "test", YAML: "mailboxes: ["}); err != nil {
		t.Fatal(err)
	}
	st.SaveOAuthToken(ctx, "outlook", store.OAuthToken{RefreshToken: "r"})
	out := importYAML(t, importBase+"mailboxes:\n  outlook: "+msBox("outlook.office365.com", "a@outlook.com")+"\n")
	if _, ok, _ := st.GetOAuthToken(ctx, "outlook"); ok {
		t.Fatal("token kept although the previous account is unknown")
	}
	if !strings.Contains(out, "信箱 outlook 的 Microsoft 授權已清除，請重新授權") {
		t.Errorf("out = %q", out)
	}
}

// The microsoft section is imported like the rest; changing its client_id drops the tokens
// of exactly the mailboxes that use it, and re-importing an old per-mailbox YAML unchanged
// drops nothing.
func TestConfigImportGlobalMicrosoftApp(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	legacy := importBase + "mailboxes:\n  own: " + msBox("outlook.office365.com", "a@outlook.com") + "\n"
	importYAML(t, legacy)
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	tok := store.OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now().Add(time.Hour)}
	st.SaveOAuthToken(ctx, "own", tok)
	if out := importYAML(t, legacy); strings.Contains(out, "已清除") {
		t.Fatalf("re-importing the same per-mailbox YAML dropped a token: %q", out)
	}
	shared := "{ host: outlook.office365.com, username: b@outlook.com, auth: { type: microsoft_oauth }, targets: [me] }"
	importYAML(t, importBase+"microsoft: { client_id: app-1, tenant: consumers }\nmailboxes:\n  own: "+msBox("outlook.office365.com", "a@outlook.com")+"\n  shared: "+shared+"\n")
	st.SaveOAuthToken(ctx, "shared", tok)
	out := importYAML(t, importBase+"microsoft: { client_id: app-2, tenant: consumers }\nmailboxes:\n  own: "+msBox("outlook.office365.com", "a@outlook.com")+"\n  shared: "+shared+"\n")
	if _, ok, _ := st.GetOAuthToken(ctx, "shared"); ok || !strings.Contains(out, "信箱 shared 的 Microsoft 授權已清除") {
		t.Errorf("global client_id change kept the token of a mailbox using it: %q", out)
	}
	if _, ok, _ := st.GetOAuthToken(ctx, "own"); !ok || strings.Contains(out, "信箱 own") {
		t.Errorf("a mailbox with its own client_id lost its token: %q", out)
	}
	code, stdout, _ := run("config", "export")
	if code != 0 || !strings.Contains(stdout, "microsoft:\n  client_id: app-2\n  tenant: consumers\n") {
		t.Errorf("export = %d %q", code, stdout)
	}
}
