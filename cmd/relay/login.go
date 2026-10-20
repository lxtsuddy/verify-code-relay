package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/config"
	"verify-code-relay/internal/store"
)

func cmdLogin(configPath string, args []string, stdout, stderr io.Writer) int {
	restrictUmask() // the db holds OAuth tokens
	fs := newFlags("login", &configPath, stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法：relay login <信箱>")
		return 2
	}
	name := fs.Arg(0)
	cfg, err := config.Load(configPath)
	if err != nil {
		return fail(stderr, err)
	}
	if err := cfg.Validate(); err != nil {
		return fail(stderr, err)
	}
	m, ok := cfg.Mailboxes[name]
	if !ok {
		return fail(stderr, fmt.Errorf("找不到信箱 %q", name))
	}
	if m.Auth.Type != "microsoft_oauth" {
		return fail(stderr, fmt.Errorf("信箱 %s 的 auth.type 是 %q，只有 microsoft_oauth 需要 login", name, m.Auth.Type))
	}
	st, err := openStore(cfg)
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()
	ms := &auth.Microsoft{Mailbox: name, Username: m.Username, ClientID: m.Auth.ClientID, Tenant: m.Auth.Tenant, Store: st,
		Log: slog.New(slog.NewTextHandler(stderr, nil))}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dc, err := ms.StartDeviceLogin(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "請在瀏覽器開啟 %s 並輸入代碼：%s\n（%s）\n等待授權中…\n", dc.VerificationURI, dc.UserCode, dc.Message)
	if err := ms.PollDeviceLogin(ctx, dc); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "授權成功，信箱 %s 的 token 已儲存。若服務正在執行，請重新啟動以生效。\n", name)
	return 0
}

func openStore(cfg *config.Config) (*store.Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("建立資料目錄：%w", err)
	}
	return store.Open(filepath.Join(cfg.DataDir, "relay.db"))
}
