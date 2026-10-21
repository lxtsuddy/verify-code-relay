package main

import (
	"context"
	"fmt"
	"html"
	"io"

	"github.com/lxtsuddy/verify-code-relay/internal/telegram"
)

func cmdSendTest(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("send-test", stderr)
	pos, err := parseInterspersed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprintln(stderr, "用法：relay send-test <目標>")
		return 2
	}
	name := pos[0]
	st, b, err := loadFromDB(context.Background())
	if err != nil {
		return fail(stderr, err)
	}
	st.Close()
	cfg := b.Config
	t, ok := cfg.Targets[name]
	if !ok {
		return fail(stderr, notFound("目標", name))
	}
	text := fmt.Sprintf("✅ verify-code-relay 測試訊息\n目標 <b>%s</b>（Bot %s）設定正確。", html.EscapeString(name), html.EscapeString(t.Bot))
	err = telegramClient().Send(context.Background(), telegram.SendRequest{Token: cfg.Bots[t.Bot].Token, ChatID: t.ChatID, Text: text, HTML: true})
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "已送出測試訊息到目標 %s\n", name)
	return 0
}
