package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"verify-code-relay/internal/auth"
)

func cmdLogin(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("login", stderr)
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprintln(stderr, "用法：relay login <信箱>")
		return 2
	}
	name := pos[0]
	st, b, err := loadFromDB(context.Background())
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()
	m, ok := b.Config.Mailboxes[name]
	if !ok {
		return fail(stderr, notFound("信箱", name))
	}
	if m.Auth.Type != "microsoft_oauth" {
		return fail(stderr, fmt.Errorf("信箱 %s 的 auth.type 是 %q，只有 microsoft_oauth 需要 login", name, m.Auth.Type))
	}
	clientID, tenant := b.Config.MicrosoftApp(m)
	ms := &auth.Microsoft{Mailbox: name, Username: m.Username, ClientID: clientID, Tenant: tenant, Store: st,
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
	fmt.Fprintf(stdout, "授權成功，信箱 %s 的 token 已儲存。若服務正在執行，請重新啟動 Pod 以套用（在 Web 上授權則會自動套用）。\n", name)
	return 0
}
