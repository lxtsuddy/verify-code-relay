package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type OAuthToken struct {
	RefreshToken string
	AccessToken  string
	ExpiresAt    time.Time
}

func (s *Store) GetOAuthToken(ctx context.Context, mailbox string) (OAuthToken, bool, error) {
	var t OAuthToken
	var exp int64
	err := s.db.QueryRowContext(ctx,
		`SELECT refresh_token, access_token, expires_at FROM oauth_tokens WHERE mailbox = ?`, mailbox).
		Scan(&t.RefreshToken, &t.AccessToken, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthToken{}, false, nil
	}
	if err != nil {
		return OAuthToken{}, false, err
	}
	t.ExpiresAt = fromUnix(exp)
	return t, true, nil
}

func (s *Store) SaveOAuthToken(ctx context.Context, mailbox string, t OAuthToken) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_tokens (mailbox, refresh_token, access_token, expires_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (mailbox) DO UPDATE SET refresh_token = excluded.refresh_token,
			access_token = excluded.access_token, expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
		mailbox, t.RefreshToken, t.AccessToken, unix(t.ExpiresAt), time.Now().Unix())
	return err
}
