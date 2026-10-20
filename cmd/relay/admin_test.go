package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"verify-code-relay/internal/websec"
)

func TestAdminResetFromStdin(t *testing.T) {
	keepUmask(t)
	dir := t.TempDir()
	t.Setenv("RELAY_DATA_DIR", dir)
	defer func() { stdinReader = os.Stdin }()
	var out, errb bytes.Buffer
	stdinReader = strings.NewReader("short\n")
	if code := realMain([]string{"admin", "reset"}, &out, &errb); code == 0 {
		t.Fatal("short password accepted")
	}
	stdinReader = strings.NewReader("a-long-enough-password\n")
	if code := realMain([]string{"admin", "reset"}, &out, &errb); code != 0 {
		t.Fatalf("reset = %d: %s", code, errb.String())
	}
	if strings.Contains(out.String()+errb.String(), "a-long-enough-password") {
		t.Error("password echoed")
	}
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, ok, _ := st.GetAdmin(context.Background())
	if !ok || a.TOTPSecret != "" {
		t.Fatalf("admin = %+v %v", a, ok)
	}
	if ok, _ := websec.VerifyPassword(a.PasswordHash, "a-long-enough-password"); !ok {
		t.Error("password not set")
	}
}

// A reset clears a bound TOTP secret; CRLF line endings and a missing final newline are fine.
func TestAdminResetClearsTOTP(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	defer func() { stdinReader = os.Stdin }()
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st.CreateAdminIfMissing(ctx, "x")
	st.ConfirmTOTP(ctx, "SECRET", 1)
	st.Close()

	for _, in := range []string{"another-long-password\r\n", "another-long-password"} {
		stdinReader = strings.NewReader(in)
		if code, _, stderr := run("admin", "reset"); code != 0 {
			t.Fatalf("%q: code=%d err=%q", in, code, stderr)
		}
		st, _ := openDB()
		a, _, _ := st.GetAdmin(ctx)
		st.Close()
		if a.TOTPSecret != "" {
			t.Errorf("%q: TOTP not cleared", in)
		}
		if ok, _ := websec.VerifyPassword(a.PasswordHash, "another-long-password"); !ok {
			t.Errorf("%q: password not set", in)
		}
	}
	stdinReader = strings.NewReader("")
	if code, _, _ := run("admin", "reset"); code == 0 {
		t.Error("empty input accepted")
	}
	if code, _, stderr := run("admin"); code != 2 || !strings.Contains(stderr, "用法") {
		t.Errorf("admin without subcommand: code=%d err=%q", code, stderr)
	}
}

func TestEnsureAdmin(t *testing.T) {
	keepUmask(t)
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	st, err := openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	for _, pw := range []string{"", "short-pw"} {
		buf.Reset()
		t.Setenv("RELAY_ADMIN_PASSWORD", pw)
		if err := ensureAdmin(ctx, st, log); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := st.GetAdmin(ctx); ok {
			t.Fatalf("%q: admin created", pw)
		}
		if !strings.Contains(buf.String(), "尚未設定管理員") || (pw != "" && strings.Contains(buf.String(), pw)) {
			t.Errorf("%q: log = %s", pw, buf.String())
		}
	}

	buf.Reset()
	t.Setenv("RELAY_ADMIN_PASSWORD", "初始密碼-十二個字元以上") // counted in characters, not bytes
	if err := ensureAdmin(ctx, st, log); err != nil {
		t.Fatal(err)
	}
	a, ok, _ := st.GetAdmin(ctx)
	if !ok || !strings.Contains(buf.String(), "已建立管理員") || strings.Contains(buf.String(), "初始密碼") {
		t.Fatalf("admin %v, log = %s", ok, buf.String())
	}
	if ok, _ := websec.VerifyPassword(a.PasswordHash, "初始密碼-十二個字元以上"); !ok {
		t.Fatal("wrong password stored")
	}

	// 已有管理員：不再讀取變數
	buf.Reset()
	t.Setenv("RELAY_ADMIN_PASSWORD", "a-different-password")
	if err := ensureAdmin(ctx, st, log); err != nil {
		t.Fatal(err)
	}
	b, _, _ := st.GetAdmin(ctx)
	if b.PasswordHash != a.PasswordHash || buf.Len() != 0 {
		t.Fatalf("existing admin changed or logged: %s", buf.String())
	}

	// 12 個位元組但不足 12 個字元
	t.Setenv("RELAY_DATA_DIR", t.TempDir())
	st2, _ := openDB()
	defer st2.Close()
	t.Setenv("RELAY_ADMIN_PASSWORD", "密碼密碼") // 12 bytes, 4 characters
	ensureAdmin(ctx, st2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, ok, _ := st2.GetAdmin(ctx); ok {
		t.Fatal("4-character password accepted")
	}
}
