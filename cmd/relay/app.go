package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/recognize"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/telegram"
)

// newFlags returns a FlagSet that also accepts --config after the subcommand.
func newFlags(name string, configPath *string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(configPath, "config", *configPath, "設定檔路徑")
	return fs
}

type app struct {
	cfg        *config.Config
	recognizer *recognize.Chain
	renderer   *render.Renderer
}

// loadApp loads + validates config and compiles rules and templates. All problems are
// reported together.
func loadApp(path string) (*app, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	var errs []error
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	rec, err := recognize.New(cfg.Rules, cfg.Recognize)
	if err != nil {
		errs = append(errs, err)
	}
	rnd, err := render.New(cfg)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &app{cfg: cfg, recognizer: rec, renderer: rnd}, nil
}

func newLogger(level string, w io.Writer) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l}))
}

func telegramClient() *telegram.Client {
	c := telegram.New()
	if u := os.Getenv("RELAY_TELEGRAM_API"); u != "" { // test hook / self-hosted Bot API server
		c.BaseURL = u
	}
	return c
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "錯誤：%v\n", err)
	return 1
}
