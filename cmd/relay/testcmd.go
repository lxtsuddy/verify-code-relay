package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"verify-code-relay/internal/mailparse"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/telegram"
)

func cmdTest(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("test", stderr)
	emlPath := fs.String("eml", "", ".eml 檔案路徑（必填）")
	mailbox := fs.String("mailbox", "", "使用哪個信箱的範本與名稱")
	sendTo := fs.String("send", "", "實際發送到這個目標")
	if err := fs.Parse(args); err != nil || *emlPath == "" {
		fmt.Fprintln(stderr, "用法：relay test --eml <檔案> [--mailbox <信箱>] [--send <目標>]")
		return 2
	}
	st, a, err := loadFromDB(context.Background())
	if err != nil {
		return fail(stderr, err)
	}
	st.Close()
	raw, err := os.ReadFile(*emlPath)
	if err != nil {
		return fail(stderr, err)
	}
	msg, err := mailparse.Parse(raw)
	if err != nil {
		return fail(stderr, fmt.Errorf("解析信件：%w", err))
	}
	displayName, username := "（測試）", ""
	if *mailbox != "" {
		m, ok := a.Config.Mailboxes[*mailbox]
		if !ok {
			return fail(stderr, notFound("信箱", *mailbox))
		}
		displayName, username = m.DisplayName, m.Username
	}

	fmt.Fprintf(stdout, "寄件者：%s <%s>\n主旨：%s\n", msg.From.Name, msg.From.Addr, msg.Subject)
	for _, w := range msg.Warnings {
		fmt.Fprintf(stdout, "警告：%s\n", w)
	}
	fmt.Fprintf(stdout, "── 內文（純文字，前 500 字）──\n%s\n", truncateRunes(msg.Text, 500))
	fmt.Fprintf(stdout, "── 連結 %d 個 ──\n", len(msg.Links))
	for _, l := range msg.Links {
		fmt.Fprintf(stdout, "  %s  [%s]\n", l.URL, l.Text)
	}
	res, ignore, tr := a.Recognizer.Recognize(msg)
	fmt.Fprintln(stdout, "── 辨識過程 ──")
	for _, s := range tr.Steps {
		fmt.Fprintln(stdout, "  "+s)
	}
	fmt.Fprintln(stdout, "── 結果 ──")
	switch {
	case ignore:
		rule := ""
		if res != nil {
			rule = res.Rule
		}
		fmt.Fprintf(stdout, "被規則 %s 明確略過，不轉發\n", rule)
		return 0
	case res == nil:
		fmt.Fprintln(stdout, "不符合任何規則，不轉發")
		return 0
	}
	fmt.Fprintf(stdout, "規則：%s\n驗證碼：%s\n連結：%s\n服務：%s\n", res.Rule, res.Code, res.URL, res.Service)

	out, err := a.Renderer.Render(*mailbox, render.Data{Code: res.Code, URL: res.URL, Service: res.Service,
		FromName: msg.From.Name, FromAddr: msg.From.Addr, To: pipeline.Recipient(msg.To, username),
		Subject: msg.Subject, Mailbox: displayName, Rule: res.Rule, Date: msg.Date})
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "── Telegram 訊息 ──\n%s\n── 按鈕 ──\n%s\n", out.Text, out.ReplyMarkup)

	if *sendTo != "" {
		t, ok := a.Config.Targets[*sendTo]
		if !ok {
			return fail(stderr, notFound("目標", *sendTo))
		}
		err := telegramClient().Send(context.Background(), telegram.SendRequest{Token: a.Config.Bots[t.Bot].Token,
			ChatID: t.ChatID, Text: out.Text, HTML: true, ReplyMarkup: out.ReplyMarkup})
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "已送出到目標 %s\n", *sendTo)
	}
	return 0
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
