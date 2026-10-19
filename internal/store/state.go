package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type MailboxState struct {
	UIDValidity uint32
	LastUID     uint32
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) GetMailboxState(ctx context.Context, mailbox, folder string) (MailboxState, bool, error) {
	var st MailboxState
	err := s.db.QueryRowContext(ctx,
		`SELECT uid_validity, last_uid FROM mailbox_state WHERE mailbox = ? AND folder = ?`,
		mailbox, folder).Scan(&st.UIDValidity, &st.LastUID)
	if errors.Is(err, sql.ErrNoRows) {
		return MailboxState{}, false, nil
	}
	if err != nil {
		return MailboxState{}, false, err
	}
	return st, true, nil
}

func (s *Store) SetMailboxState(ctx context.Context, mailbox, folder string, st MailboxState) error {
	return setState(ctx, s.db, mailbox, folder, st)
}

func setState(ctx context.Context, x execer, mailbox, folder string, st MailboxState) error {
	_, err := x.ExecContext(ctx, `
		INSERT INTO mailbox_state (mailbox, folder, uid_validity, last_uid, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (mailbox, folder) DO UPDATE SET uid_validity = excluded.uid_validity,
			last_uid = excluded.last_uid, updated_at = excluded.updated_at`,
		mailbox, folder, st.UIDValidity, st.LastUID, time.Now().Unix())
	return err
}
