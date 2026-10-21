package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/lxtsuddy/verify-code-relay/internal/websec"
)

// stdinReader is where admin reset reads the new password; tests replace it.
var stdinReader io.Reader = os.Stdin

func cmdAdmin(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "reset" {
		fmt.Fprintln(stderr, "用法：relay admin reset")
		return 2
	}
	pw, err := readNewPassword(stderr)
	if err != nil {
		return fail(stderr, err)
	}
	if n := utf8.RuneCountInString(pw); n < websec.MinPasswordLen {
		return fail(stderr, fmt.Errorf("密碼至少需要 %d 個字元（目前 %d 個）", websec.MinPasswordLen, n))
	}
	hash, err := websec.HashPassword(pw)
	if err != nil {
		return fail(stderr, err)
	}
	st, err := openDB()
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()
	if err := st.ResetAdmin(context.Background(), hash); err != nil {
		return fail(stderr, fmt.Errorf("重設管理員：%w", err))
	}
	fmt.Fprintln(stdout, "已重設管理員密碼並清除 TOTP，所有登入中的工作階段已登出。請登入 Web 並重新綁定 TOTP。")
	return 0
}

// readNewPassword reads the password without echo (asking twice) when stdin is a
// terminal, otherwise one line, so it can be piped in (kubectl exec -i).
func readNewPassword(prompt io.Writer) (string, error) {
	if f, ok := stdinReader.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(prompt, "新密碼（至少 %d 個字元）：", websec.MinPasswordLen)
		a, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("讀取密碼：%w", err)
		}
		fmt.Fprint(prompt, "再輸入一次：")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("讀取密碼：%w", err)
		}
		if string(a) != string(b) {
			return "", errors.New("兩次輸入的密碼不一致")
		}
		return string(a), nil
	}
	line, err := bufio.NewReader(stdinReader).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("讀取密碼：%w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
