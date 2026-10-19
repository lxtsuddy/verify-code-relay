package store

import (
	"context"
	"database/sql"
	"time"
)

type LogEntry struct {
	At       time.Time
	Mailbox  string
	Folder   string
	UID      uint32
	FromAddr string
	Subject  string
	Kind     string
	Rule     string
	Result   string
}

type OutboxItem struct {
	ID            int64
	LogID         int64 // 0 means alert (stored as NULL)
	Target        string
	Bot           string
	ChatID        int64
	Text          string
	ReplyMarkup   string
	PlainFallback bool
	CreatedAt     time.Time
	ExpiresAt     time.Time
	NextAttemptAt time.Time
	Attempts      int
}

const outboxCols = `id, COALESCE(log_id, 0), target, bot, chat_id, text, COALESCE(reply_markup, ''),
	plain_fallback, created_at, expires_at, next_attempt_at, attempts`

func (s *Store) RecordMessage(ctx context.Context, e LogEntry, items []OutboxItem, st MailboxState) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO forward_log (at, mailbox, folder, uid, from_addr, subject, kind, rule, result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		unix(e.At), e.Mailbox, e.Folder, e.UID, e.FromAddr, e.Subject, e.Kind, nullStr(e.Rule), e.Result)
	if err != nil {
		return 0, err
	}
	logID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		it.LogID = logID
		if _, err := insertOutbox(ctx, tx, it); err != nil {
			return 0, err
		}
	}
	if err := setState(ctx, tx, e.Mailbox, e.Folder, st); err != nil {
		return 0, err
	}
	return logID, tx.Commit()
}

func (s *Store) Enqueue(ctx context.Context, it OutboxItem) (int64, error) {
	return insertOutbox(ctx, s.db, it)
}

func insertOutbox(ctx context.Context, x execer, it OutboxItem) (int64, error) {
	var logID any
	if it.LogID != 0 {
		logID = it.LogID
	}
	res, err := x.ExecContext(ctx, `
		INSERT INTO outbox (log_id, target, bot, chat_id, text, reply_markup, plain_fallback,
			created_at, expires_at, attempts, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		logID, it.Target, it.Bot, it.ChatID, it.Text, nullStr(it.ReplyMarkup), it.PlainFallback,
		unix(it.CreatedAt), unix(it.ExpiresAt), it.Attempts, unix(it.NextAttemptAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DueOutbox returns, for each (bot, chat_id), its oldest item if that item is due.
// Later items of the same chat wait behind the head to preserve ordering.
func (s *Store) DueOutbox(ctx context.Context, now time.Time, limit int) ([]OutboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+outboxCols+` FROM outbox
		WHERE id IN (SELECT MIN(id) FROM outbox GROUP BY bot, chat_id) AND next_attempt_at <= ?
		ORDER BY id LIMIT ?`, unix(now), limit)
	if err != nil {
		return nil, err
	}
	return scanOutbox(rows)
}

func (s *Store) DeleteExpired(ctx context.Context, now time.Time) ([]OutboxItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+outboxCols+` FROM outbox WHERE expires_at < ? ORDER BY id`, unix(now))
	if err != nil {
		return nil, err
	}
	items, err := scanOutbox(rows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE expires_at < ?`, unix(now)); err != nil {
		return nil, err
	}
	return items, tx.Commit()
}

func (s *Store) DeleteOutbox(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id)
	return err
}

func (s *Store) RescheduleOutbox(ctx context.Context, id int64, attempts int, next time.Time, plainFallback bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE outbox SET attempts = ?, next_attempt_at = ?, plain_fallback = ? WHERE id = ?`,
		attempts, unix(next), plainFallback, id)
	return err
}

func (s *Store) SetLogResult(ctx context.Context, logID int64, result string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE forward_log SET result = ? WHERE id = ?`, result, logID)
	return err
}

func (s *Store) PruneLog(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM forward_log WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanOutbox(rows *sql.Rows) ([]OutboxItem, error) {
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		var created, expires, next int64
		if err := rows.Scan(&it.ID, &it.LogID, &it.Target, &it.Bot, &it.ChatID, &it.Text, &it.ReplyMarkup,
			&it.PlainFallback, &created, &expires, &next, &it.Attempts); err != nil {
			return nil, err
		}
		it.CreatedAt, it.ExpiresAt, it.NextAttemptAt = fromUnix(created), fromUnix(expires), fromUnix(next)
		out = append(out, it)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
