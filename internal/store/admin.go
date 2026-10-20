package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Admin struct {
	PasswordHash string
	TOTPSecret   string // "" = not yet bound
	TOTPPending  string
	TOTPLastStep int64
}

func (s *Store) GetAdmin(ctx context.Context) (Admin, bool, error) {
	var a Admin
	err := s.db.QueryRowContext(ctx,
		`SELECT password_hash, COALESCE(totp_secret, ''), COALESCE(totp_pending, ''), totp_last_step FROM admin WHERE id = 1`).
		Scan(&a.PasswordHash, &a.TOTPSecret, &a.TOTPPending, &a.TOTPLastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, false, nil
	}
	if err != nil {
		return Admin{}, false, err
	}
	return a, true, nil
}

// CreateAdminIfMissing creates the admin row; it reports whether it did.
func (s *Store) CreateAdminIfMissing(ctx context.Context, passwordHash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO admin (id, password_hash, updated_at) VALUES (1, ?, ?) ON CONFLICT(id) DO NOTHING`,
		passwordHash, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) SetAdminPassword(ctx context.Context, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin SET password_hash = ?, updated_at = ? WHERE id = 1`,
		passwordHash, time.Now().Unix())
	return err
}

// ResetAdmin sets the password, clears all TOTP state and deletes every session,
// creating the admin row when there is none.
func (s *Store) ResetAdmin(ctx context.Context, passwordHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO admin (id, password_hash, updated_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET password_hash = excluded.password_hash, totp_secret = NULL,
			totp_pending = NULL, totp_last_step = 0, updated_at = excluded.updated_at`,
		passwordHash, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetTOTPPending(ctx context.Context, secret string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin SET totp_pending = ?, updated_at = ? WHERE id = 1`,
		secret, time.Now().Unix())
	return err
}

// ConfirmTOTP stores the confirmed secret, clears the pending one and records the
// time step used for the confirmation code.
func (s *Store) ConfirmTOTP(ctx context.Context, secret string, step int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin SET totp_secret = ?, totp_pending = NULL, totp_last_step = ?, updated_at = ? WHERE id = 1`,
		secret, step, time.Now().Unix())
	return err
}

// UseTOTPStep records step as used and reports true only when it is newer than
// the last used step (replay protection).
func (s *Store) UseTOTPStep(ctx context.Context, step int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE admin SET totp_last_step = ?, updated_at = ? WHERE id = 1 AND totp_last_step < ?`,
		step, time.Now().Unix(), step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
