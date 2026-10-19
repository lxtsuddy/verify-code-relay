// Package store persists relay state in SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

var migrations = []string{
	`CREATE TABLE mailbox_state (
		mailbox      TEXT NOT NULL,
		folder       TEXT NOT NULL,
		uid_validity INTEGER NOT NULL,
		last_uid     INTEGER NOT NULL,
		updated_at   INTEGER NOT NULL,
		PRIMARY KEY (mailbox, folder)
	);
	CREATE TABLE oauth_tokens (
		mailbox       TEXT PRIMARY KEY,
		refresh_token TEXT NOT NULL,
		access_token  TEXT NOT NULL,
		expires_at    INTEGER NOT NULL,
		updated_at    INTEGER NOT NULL
	);
	CREATE TABLE outbox (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		log_id          INTEGER,
		target          TEXT NOT NULL,
		bot             TEXT NOT NULL,
		chat_id         INTEGER NOT NULL,
		text            TEXT NOT NULL,
		reply_markup    TEXT,
		plain_fallback  INTEGER NOT NULL DEFAULT 0,
		created_at      INTEGER NOT NULL,
		expires_at      INTEGER NOT NULL,
		attempts        INTEGER NOT NULL DEFAULT 0,
		next_attempt_at INTEGER NOT NULL
	);
	CREATE INDEX outbox_chat ON outbox (bot, chat_id, id);
	CREATE TABLE forward_log (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		at         INTEGER NOT NULL,
		mailbox    TEXT NOT NULL,
		folder     TEXT NOT NULL,
		uid        INTEGER NOT NULL,
		from_addr  TEXT NOT NULL,
		subject    TEXT NOT NULL,
		kind       TEXT NOT NULL,
		rule       TEXT,
		result     TEXT NOT NULL
	);
	CREATE INDEX forward_log_at ON forward_log (at);`,
}

// Open opens (creating if needed, mode 0600) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("建立資料庫檔案：%w", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; avoids SQLITE_BUSY between our own goroutines
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("資料庫版本 %d 高於程式支援的 %d，請更新程式", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("資料庫遷移 %d：%w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func unix(t time.Time) int64 { return t.Unix() }

func fromUnix(v int64) time.Time { return time.Unix(v, 0) }
