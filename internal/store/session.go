package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Session struct {
	TokenHash string
	CSRF      string
	CreatedAt time.Time
	LastSeen  time.Time
	IP        string
	UserAgent string
}

func (s *Store) CreateSession(ctx context.Context, se Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, csrf, created_at, last_seen, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?)`,
		se.TokenHash, se.CSRF, unix(se.CreatedAt), unix(se.LastSeen), nullStr(se.IP), nullStr(se.UserAgent))
	return err
}

func (s *Store) GetSession(ctx context.Context, tokenHash string) (Session, bool, error) {
	se := Session{TokenHash: tokenHash}
	var created, seen int64
	err := s.db.QueryRowContext(ctx,
		`SELECT csrf, created_at, last_seen, COALESCE(ip, ''), COALESCE(user_agent, '') FROM sessions WHERE token_hash = ?`, tokenHash).
		Scan(&se.CSRF, &created, &seen, &se.IP, &se.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, err
	}
	se.CreatedAt, se.LastSeen = fromUnix(created), fromUnix(seen)
	return se, true, nil
}

func (s *Store) TouchSession(ctx context.Context, tokenHash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE token_hash = ?`, unix(at), tokenHash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteOtherSessions(ctx context.Context, keepTokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash <> ?`, keepTokenHash)
	return err
}

// PruneSessions deletes sessions idle since before idleBefore or created before
// createdBefore, and returns how many were removed.
func (s *Store) PruneSessions(ctx context.Context, idleBefore, createdBefore time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE last_seen < ? OR created_at < ?`,
		unix(idleBefore), unix(createdBefore))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
