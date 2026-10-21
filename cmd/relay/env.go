package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/lxtsuddy/verify-code-relay/internal/runtime"
	"github.com/lxtsuddy/verify-code-relay/internal/store"
	"github.com/lxtsuddy/verify-code-relay/internal/websec"
)

// Process-level settings come from the environment: the database location must be known
// before the database (which holds everything else) can be opened.

// dataDir is RELAY_DATA_DIR, default "/data".
func dataDir() string {
	if v := os.Getenv("RELAY_DATA_DIR"); v != "" {
		return v
	}
	return "/data"
}

// listenAddr is the web admin address RELAY_LISTEN, default ":8080".
func listenAddr() string {
	if v := os.Getenv("RELAY_LISTEN"); v != "" {
		return v
	}
	return ":8080"
}

// healthAddr is the health server address RELAY_HEALTH_LISTEN: ":8081" when unset,
// "" (health server disabled) when set to the empty string.
func healthAddr() string {
	if v, ok := os.LookupEnv("RELAY_HEALTH_LISTEN"); ok {
		return v
	}
	return ":8081"
}

// openDB creates the data directory if needed and opens dataDir/relay.db. It tightens the
// umask first so SQLite's -wal/-shm side files are as private as the database itself.
func openDB() (*store.Store, error) {
	restrictUmask()
	dir := dataDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("建立資料目錄：%w", err)
	}
	return store.Open(filepath.Join(dir, "relay.db"))
}

// loadFromDB opens the store, loads and builds the stored config.
func loadFromDB(ctx context.Context) (*store.Store, *runtime.Built, error) {
	st, err := openDB()
	if err != nil {
		return nil, nil, err
	}
	b, err := runtime.LoadBuilt(ctx, st)
	if err != nil {
		st.Close()
		var invalid *runtime.InvalidConfigError
		if errors.As(err, &invalid) {
			return nil, nil, fmt.Errorf("資料庫中的設定無效（請在 Web 上修正）：\n%w", err)
		}
		return nil, nil, fmt.Errorf("無法讀取資料庫中的設定：%w", err)
	}
	return st, b, nil
}

// ensureAdmin creates the admin from RELAY_ADMIN_PASSWORD when none exists.
func ensureAdmin(ctx context.Context, st *store.Store, log *slog.Logger) error {
	_, ok, err := st.GetAdmin(ctx)
	if err != nil {
		return fmt.Errorf("讀取管理員：%w", err)
	}
	if ok {
		return nil
	}
	pw := os.Getenv("RELAY_ADMIN_PASSWORD")
	if utf8.RuneCountInString(pw) < websec.MinPasswordLen {
		log.Warn("尚未設定管理員：請設定 RELAY_ADMIN_PASSWORD（至少 12 個字元）後重新啟動")
		return nil
	}
	hash, err := websec.HashPassword(pw)
	if err != nil {
		return fmt.Errorf("產生密碼雜湊：%w", err)
	}
	created, err := st.CreateAdminIfMissing(ctx, hash)
	if err != nil {
		return fmt.Errorf("建立管理員：%w", err)
	}
	if created {
		log.Info("已建立管理員")
	}
	return nil
}
