package store

import (
	"context"
	"os"
	"path/filepath"
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

func TestOpenPermissionsAndReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
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
	s2.Close()
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
	st, _, _ := s.GetMailboxState(ctx, "g", "INBOX")
	if st.LastUID != 5 {
		t.Errorf("state not advanced: %+v", st)
	}
	due, err := s.DueOutbox(ctx, now, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %+v, %v", due, err)
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
	id1, _ := s.Enqueue(ctx, item("me", 1, now))
	id2, _ := s.Enqueue(ctx, item("me", 1, now))
	// head not due yet -> nothing for chat 1 (keeps per-chat order)
	if err := s.RescheduleOutbox(ctx, id1, 1, now.Add(10*time.Second), false); err != nil {
		t.Fatal(err)
	}
	due, _ := s.DueOutbox(ctx, now, 10)
	if len(due) != 0 {
		t.Fatalf("want none due, got %+v", due)
	}
	due, _ = s.DueOutbox(ctx, now.Add(10*time.Second), 10)
	if len(due) != 1 || due[0].ID != id1 || due[0].Attempts != 1 {
		t.Fatalf("want head id1, got %+v", due)
	}
	s.DeleteOutbox(ctx, id1)
	due, _ = s.DueOutbox(ctx, now, 10)
	if len(due) != 1 || due[0].ID != id2 {
		t.Fatalf("want id2, got %+v", due)
	}
	if err := s.RescheduleOutbox(ctx, id2, 2, now, true); err != nil {
		t.Fatal(err)
	}
	due, _ = s.DueOutbox(ctx, now, 10)
	if !due[0].PlainFallback {
		t.Error("plain fallback flag not persisted")
	}
}

func TestDeleteExpired(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	old := item("me", 1, now.Add(-time.Hour))
	old.ExpiresAt = now.Add(-time.Minute)
	s.Enqueue(ctx, old)
	s.Enqueue(ctx, item("me", 1, now))
	gone, err := s.DeleteExpired(ctx, now)
	if err != nil || len(gone) != 1 {
		t.Fatalf("DeleteExpired = %+v, %v", gone, err)
	}
	due, _ := s.DueOutbox(ctx, now, 10)
	if len(due) != 1 {
		t.Fatalf("remaining = %+v", due)
	}
}

func TestPruneLog(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	s.RecordMessage(ctx, LogEntry{At: now.Add(-31 * 24 * time.Hour), Mailbox: "g", Folder: "INBOX", UID: 1, Kind: "none", Result: "not_forwarded"}, nil, MailboxState{1, 1})
	s.RecordMessage(ctx, LogEntry{At: now, Mailbox: "g", Folder: "INBOX", UID: 2, Kind: "none", Result: "not_forwarded"}, nil, MailboxState{1, 2})
	n, err := s.PruneLog(ctx, now.Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneLog = %d, %v", n, err)
	}
}

func TestOAuthToken(t *testing.T) {
	s := openTest(t)
	if _, ok, _ := s.GetOAuthToken(ctx, "o"); ok {
		t.Fatal("want missing")
	}
	tok := OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Unix(1_800_000_000, 0)}
	if err := s.SaveOAuthToken(ctx, "o", tok); err != nil {
		t.Fatal(err)
	}
	tok.RefreshToken = "r2"
	s.SaveOAuthToken(ctx, "o", tok)
	got, ok, err := s.GetOAuthToken(ctx, "o")
	if !ok || err != nil || got.RefreshToken != "r2" || !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Fatalf("got %+v %v %v", got, ok, err)
	}
}
