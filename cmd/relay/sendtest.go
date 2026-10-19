package main

import (
	"context"
	"fmt"
	"html"
	"io"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/telegram"
)

func cmdSendTest(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("send-test", &configPath, stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法：relay send-test <目標>")
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
	t, ok := cfg.Targets[name]
	if !ok {
		return fail(stderr, fmt.Errorf("找不到目標 %q", name))
	}
	text := fmt.Sprintf("✅ verify-code-relay 測試訊息\n目標 <b>%s</b>（Bot %s）設定正確。", html.EscapeString(name), html.EscapeString(t.Bot))
	err = telegramClient().Send(context.Background(), telegram.SendRequest{Token: cfg.Bots[t.Bot].Token, ChatID: t.ChatID, Text: text, HTML: true})
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "已送出測試訊息到目標 %s\n", name)
	return 0
}
