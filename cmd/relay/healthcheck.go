package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

func cmdHealthcheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("healthcheck", stderr)
	url := fs.String("url", "", "健康檢查網址（預設依 RELAY_HEALTH_LISTEN 的 /readyz）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *url == "" {
		addr := healthAddr()
		if addr == "" {
			return fail(stderr, errors.New("RELAY_HEALTH_LISTEN 未啟用"))
		}
		*url = healthURL(addr)
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

// healthURL turns a listen address into the local /readyz URL. A missing or unspecified
// host (":8081", "0.0.0.0:8081", "[::]:8081") becomes the matching loopback address.
func healthURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen + "/readyz"
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/readyz"
}
