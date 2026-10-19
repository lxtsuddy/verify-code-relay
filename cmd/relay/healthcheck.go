package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"verify-code-relay/internal/config"
)

func cmdHealthcheck(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("healthcheck", &configPath, stderr)
	url := fs.String("url", "", "健康檢查網址（預設依設定檔 http_listen）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *url == "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return fail(stderr, err)
		}
		if cfg.HTTPListen == "" {
			return fail(stderr, fmt.Errorf("http_listen 未設定"))
		}
		addr := cfg.HTTPListen
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		*url = "http://" + addr + "/healthz"
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(*url)
	if err != nil {
		return fail(stderr, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "不健康：HTTP %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
