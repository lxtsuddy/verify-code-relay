package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
