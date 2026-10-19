package main

import (
	"strings"
	"testing"
)

func TestExampleConfigValidates(t *testing.T) {
	for k, v := range map[string]string{
		"TG_BOT_MAIN": "1:a", "TG_BOT_WORK": "2:b", "GMAIL_APP_PASSWORD": "x",
	} {
		t.Setenv(k, v)
	}
	code, stdout, stderr := run("--config", "../../config.example.yaml", "config", "validate")
	if code != 0 || !strings.Contains(stdout, "設定檔正確") {
		t.Fatalf("code=%d out=%q err=%q", code, stdout, stderr)
	}
}
