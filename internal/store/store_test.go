package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var ctx = context.Background()

func mustEnqueue(t *testing.T, s *Store, it OutboxItem) int64 {
	t.Helper()
	id, err := s.Enqueue(ctx, it)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustDue(t *testing.T, s *Store, now time.Time) []OutboxItem {
	t.Helper()
	due, err := s.DueOutbox(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	return due
}

func countRows(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenPermissionsAndReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v", fi.Mode().Perm())
	}
	s2, err := Open(p) // migrations must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenUsesWALAndSchemaVersion(t *testing.T) {
	s := openTest(t)
	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var v int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Errorf("user_version = %d, want %d", v, len(migrations))
	}
}

func TestWALFilesArePrivate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// A write while the store is open guarantees both side files exist.
	if err := s.SetMailboxState(ctx, "g", "INBOX", MailboxState{1, 1}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		fi, err := os.Stat(p + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s perm = %v, want 0600", suffix, fi.Mode().Perm())
		}
	}
}

func TestOpenPathWithURISpecialChars(t *testing.T) {
	for _, name := range []string{"q?x", "h#x", "p%41x", "p%x", "s p&a=b"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, name)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(dir, "relay.db")
			s, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.SetMailboxState(ctx, "g", "INBOX", MailboxState{1, 7}); err != nil {
				t.Fatal(err)
			}
			// SQLite must have opened exactly p, not a mangled name elsewhere.
			if _, err := os.Stat(p + "-wal"); err != nil {
				t.Errorf("SQLite did not open %q: %v", p, err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s2, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer s2.Close()
			st, ok, err := s2.GetMailboxState(ctx, "g", "INBOX")
			if err != nil || !ok || st.LastUID != 7 {
				t.Fatalf("reopen: got %+v %v %v", st, ok, err)
			}
			ents, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			if len(ents) != 1 {
				t.Errorf("stray files next to data dir: %v", ents)
			}
		})
	}
}

func TestOpenRelativePath(t *testing.T) {
	t.Chdir(t.TempDir())
	s, err := Open("relay.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SetMailboxState(ctx, "g", "INBOX", MailboxState{1, 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("relay.db-wal"); err != nil {
		t.Fatalf("database not opened at the relative path: %v", err)
	}
}

func TestMailboxState(t *testing.T) {
	s := openTest(t)
	if _, ok, err := s.GetMailboxState(ctx, "g", "INBOX"); ok || err != nil {
		t.Fatalf("want missing, got ok=%v err=%v", ok, err)
	}
	if err := s.SetMailboxState(ctx, "g", "INBOX", MailboxState{UIDValidity: 7, LastUID: 41}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMailboxState(ctx, "g", "INBOX", MailboxState{UIDValidity: 7, LastUID: 42}); err != nil {
		t.Fatal(err)
	}
	st, ok, err := s.GetMailboxState(ctx, "g", "INBOX")
	if !ok || err != nil || st != (MailboxState{7, 42}) {
		t.Fatalf("got %+v %v %v", st, ok, err)
	}
}

func item(target string, chat int64, now time.Time) OutboxItem {
	return OutboxItem{Target: target, Bot: "main", ChatID: chat, Text: "hi", ReplyMarkup: `{"inline_keyboard":[]}`,
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), NextAttemptAt: now}
}

func TestRecordMessageAtomicAndOutbox(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	e := LogEntry{At: now, Mailbox: "g", Folder: "INBOX", UID: 5, FromAddr: "a@b.c", Subject: "s", Kind: "code", Rule: "builtin:code", Result: "pending"}
	logID, err := s.RecordMessage(ctx, e, []OutboxItem{item("me", 1, now), item("fam", 2, now)}, MailboxState{UIDValidity: 3, LastUID: 5})
	if err != nil || logID == 0 {
		t.Fatalf("RecordMessage = %d, %v", logID, err)
	}
	st, ok, err := s.GetMailboxState(ctx, "g", "INBOX")
	if err != nil || !ok || st.LastUID != 5 {
		t.Errorf("state not advanced: %+v %v %v", st, ok, err)
	}
	due := mustDue(t, s, now)
	if len(due) != 2 {
		t.Fatalf("due = %+v", due)
	}
	if due[0].LogID != logID || due[0].ReplyMarkup == "" || !due[0].ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Errorf("item fields wrong: %+v", due[0])
	}
	if err := s.SetLogResult(ctx, logID, "sent"); err != nil {
		t.Fatal(err)
	}
}

func TestDueOutboxHeadPerChat(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	id1 := mustEnqueue(t, s, item("me", 1, now))
	id2 := mustEnqueue(t, s, item("me", 1, now))
	// head not due yet -> nothing for chat 1 (keeps per-chat order)
	if err := s.RescheduleOutbox(ctx, id1, 1, now.Add(10*time.Second), false); err != nil {
		t.Fatal(err)
	}
	if due := mustDue(t, s, now); len(due) != 0 {
		t.Fatalf("want none due, got %+v", due)
	}
	due := mustDue(t, s, now.Add(10*time.Second))
	if len(due) != 1 || due[0].ID != id1 || due[0].Attempts != 1 {
		t.Fatalf("want head id1, got %+v", due)
	}
	if err := s.DeleteOutbox(ctx, id1); err != nil {
		t.Fatal(err)
	}
	due = mustDue(t, s, now)
	if len(due) != 1 || due[0].ID != id2 {
		t.Fatalf("want id2, got %+v", due)
	}
	if err := s.RescheduleOutbox(ctx, id2, 2, now, true); err != nil {
		t.Fatal(err)
	}
	due = mustDue(t, s, now)
	if len(due) != 1 || !due[0].PlainFallback {
		t.Errorf("plain fallback flag not persisted: %+v", due)
	}
}

func TestDeleteExpired(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	old := item("me", 1, now.Add(-time.Hour))
	old.ExpiresAt = now.Add(-time.Minute)
	mustEnqueue(t, s, old)
	mustEnqueue(t, s, item("me", 1, now))
	gone, err := s.DeleteExpired(ctx, now)
	if err != nil || len(gone) != 1 {
		t.Fatalf("DeleteExpired = %+v, %v", gone, err)
	}
	if due := mustDue(t, s, now); len(due) != 1 {
		t.Fatalf("remaining = %+v", due)
	}
}

func TestPruneLog(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	for i, at := range []time.Time{now.Add(-31 * 24 * time.Hour), now} {
		uid := uint32(i + 1)
		e := LogEntry{At: at, Mailbox: "g", Folder: "INBOX", UID: uid, Kind: "none", Result: "not_forwarded"}
		if _, err := s.RecordMessage(ctx, e, nil, MailboxState{1, uid}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneLog(ctx, now.Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneLog = %d, %v", n, err)
	}
	if n := countRows(t, s, "forward_log"); n != 1 {
		t.Fatalf("forward_log rows = %d, want 1", n)
	}
}

func TestOAuthToken(t *testing.T) {
	s := openTest(t)
	if _, ok, err := s.GetOAuthToken(ctx, "o"); ok || err != nil {
		t.Fatalf("want missing, got ok=%v err=%v", ok, err)
	}
	tok := OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Unix(1_800_000_000, 0)}
	if err := s.SaveOAuthToken(ctx, "o", tok); err != nil {
		t.Fatal(err)
	}
	tok.RefreshToken = "r2"
	if err := s.SaveOAuthToken(ctx, "o", tok); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetOAuthToken(ctx, "o")
	if !ok || err != nil || got.RefreshToken != "r2" || !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Fatalf("got %+v %v %v", got, ok, err)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(p)
	if err == nil {
		s2.Close()
		t.Fatal("want error for newer schema version")
	}
	if !strings.Contains(err.Error(), "99") {
		t.Fatalf("error should mention version 99, got: %v", err)
	}
}

func TestRecordMessageRollsBackOnFailure(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	if _, err := s.db.Exec(`DROP TABLE outbox`); err != nil {
		t.Fatal(err)
	}
	e := LogEntry{At: now, Mailbox: "g", Folder: "INBOX", UID: 9, Kind: "code", Result: "pending"}
	if _, err := s.RecordMessage(ctx, e, []OutboxItem{item("me", 1, now)}, MailboxState{UIDValidity: 3, LastUID: 9}); err == nil {
		t.Fatal("want error when outbox insert fails")
	}
	if _, ok, err := s.GetMailboxState(ctx, "g", "INBOX"); ok || err != nil {
		t.Fatalf("mailbox state must not persist: ok=%v err=%v", ok, err)
	}
	if n := countRows(t, s, "forward_log"); n != 0 {
		t.Fatalf("forward_log rows = %d, want 0", n)
	}
}

func TestRecordMessageRollsBackWhenLastStepFails(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	// setState is RecordMessage's final write; everything before it must be undone.
	if _, err := s.db.Exec(`DROP TABLE mailbox_state`); err != nil {
		t.Fatal(err)
	}
	e := LogEntry{At: now, Mailbox: "g", Folder: "INBOX", UID: 9, Kind: "code", Result: "pending"}
	items := []OutboxItem{item("me", 1, now), item("fam", 2, now)}
	if _, err := s.RecordMessage(ctx, e, items, MailboxState{UIDValidity: 3, LastUID: 9}); err == nil {
		t.Fatal("want error when mailbox state update fails")
	}
	if n := countRows(t, s, "forward_log"); n != 0 {
		t.Errorf("forward_log rows = %d, want 0", n)
	}
	if n := countRows(t, s, "outbox"); n != 0 {
		t.Errorf("outbox rows = %d, want 0", n)
	}
}
