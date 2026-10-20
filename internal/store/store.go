// Package store persists relay state in SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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
	dsn, err := fileURI(path)
	if err != nil {
		return nil, err
	}
	// busy_timeout goes first so it is already in effect for the pragmas after it.
	// It does not cover two processes switching the same brand-new file to WAL at
	// the same instant: SQLite may then return SQLITE_BUSY without waiting.
	db, err := sql.Open("sqlite", dsn+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
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

// fileURI turns a filesystem path into an SQLite "file:" URI. The path is made
// absolute (so a leading "//" or a relative name is never read as a URI authority)
// and percent-encoded, so '?', '#' and '%' in data_dir are taken literally instead
// of starting the query, the fragment or an escape.
func fileURI(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("資料庫路徑：%w", err)
	}
	u := url.URL{Path: filepath.ToSlash(abs)}
	return "file:" + u.EscapedPath(), nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate reads user_version and applies the missing migrations in one IMMEDIATE
// transaction. Taking the write lock before the read means another process
// opening the same database (e.g. `relay login` beside `relay run`) waits on
// busy_timeout and then sees the new version, instead of both reading the old
// version and the second one re-running CREATE TABLE. database/sql's BeginTx only
// issues a deferred BEGIN, so the transaction is driven by hand on a dedicated
// connection; nothing here may touch s.db, whose only connection this holds.
func (s *Store) migrate(ctx context.Context) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("資料庫遷移：%w", err)
	}
	defer func() {
		if err != nil {
			conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("資料庫版本 %d 高於程式支援的 %d，請更新程式", version, len(migrations))
	}
	for i := version; i < len(migrations); i++ {
		if _, err := conn.ExecContext(ctx, migrations[i]); err != nil {
			return fmt.Errorf("資料庫遷移 %d：%w", i+1, err)
		}
	}
	if version < len(migrations) {
		if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, len(migrations))); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func unix(t time.Time) int64 { return t.Unix() }

func fromUnix(v int64) time.Time { return time.Unix(v, 0) }
