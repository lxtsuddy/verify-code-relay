package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/lxtsuddy/verify-code-relay/internal/config"
	"github.com/lxtsuddy/verify-code-relay/internal/runtime"
	"github.com/lxtsuddy/verify-code-relay/internal/telegram"
)

// newFlags returns a FlagSet for a subcommand that writes its errors to stderr.
func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// newConfigFlags is newFlags plus --config (YAML file path) for config import/validate.
func newConfigFlags(name string, configPath *string, stderr io.Writer) *flag.FlagSet {
	fs := newFlags(name, stderr)
	fs.StringVar(configPath, "config", *configPath, "設定檔路徑")
	return fs
}

// parseInterspersed parses flags that may appear before or after positional
// arguments (flag.Parse stops at the first positional) and returns the
// positionals. Everything after a "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		pos, args = append(pos, rest[0]), rest[1:]
	}
}

// loadApp loads the YAML config (expanding ${ENV}) and builds it with runtime.Build, so
// all problems are reported together.
func loadApp(path string) (*runtime.Built, error) {
	raw, err := config.LoadRaw(path)
	if err != nil {
		return nil, err
	}
	return runtime.Build(raw)
}

// newLogger returns a JSON logger whose level is read from lv on every record.
func newLogger(lv *slog.LevelVar, w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lv}))
}

func telegramClient() *telegram.Client {
	c := telegram.New()
	if u := os.Getenv("RELAY_TELEGRAM_API"); u != "" { // test hook / self-hosted Bot API server
		c.BaseURL = u
	}
	return c
}

// notFound reports a missing mailbox or target in the stored config.
func notFound(kind, name string) error {
	return fmt.Errorf("找不到%s %q（設定存在資料庫中：請在 Web 上新增，或用 relay config import 匯入）", kind, name)
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "錯誤：%v\n", err)
	return 1
}
