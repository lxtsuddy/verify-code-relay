package store

import (
	"testing"
	"time"
)

func TestConfigVersionsKeepNewest(t *testing.T) {
	s := openTest(t)
	if _, ok, err := s.LatestConfigVersion(ctx); ok || err != nil {
		t.Fatalf("empty db: ok=%v err=%v", ok, err)
	}
	var last int64
	for i := 0; i < KeepConfigVersions+3; i++ {
		id, err := s.SaveConfigVersion(ctx, ConfigVersion{CreatedAt: time.Unix(int64(i), 0), Source: "web", IP: "1.2.3.4", Summary: "s", YAML: "log_level: info\n"})
		if err != nil {
			t.Fatal(err)
		}
		last = id
	}
	v, ok, err := s.LatestConfigVersion(ctx)
	if !ok || err != nil || v.ID != last || v.YAML != "log_level: info\n" || v.IP != "1.2.3.4" {
		t.Fatalf("latest = %+v %v %v", v, ok, err)
	}
	list, err := s.ListConfigVersions(ctx)
	if err != nil || len(list) != KeepConfigVersions || list[0].ID != last || list[0].YAML != "" {
		t.Fatalf("list len=%d first=%+v err=%v", len(list), list[0], err)
	}
	if _, ok, _ := s.GetConfigVersion(ctx, 1); ok {
		t.Error("oldest version should have been pruned")
	}
}

func TestAdminLifecycle(t *testing.T) {
	s := openTest(t)
	if created, err := s.CreateAdminIfMissing(ctx, "h1"); !created || err != nil {
		t.Fatalf("create: %v %v", created, err)
	}
	if created, _ := s.CreateAdminIfMissing(ctx, "h2"); created {
		t.Fatal("second create must be a no-op")
	}
	if err := s.SetTOTPPending(ctx, "PENDING"); err != nil {
		t.Fatal(err)
	}
	if err := s.ConfirmTOTP(ctx, "SECRET", 100); err != nil {
		t.Fatal(err)
	}
	a, _, _ := s.GetAdmin(ctx)
	if a.PasswordHash != "h1" || a.TOTPSecret != "SECRET" || a.TOTPPending != "" || a.TOTPLastStep != 100 {
		t.Fatalf("admin = %+v", a)
	}
	if ok, _ := s.UseTOTPStep(ctx, 100); ok {
		t.Error("replayed step accepted")
	}
	if ok, _ := s.UseTOTPStep(ctx, 101); !ok {
		t.Error("new step rejected")
	}
	s.CreateSession(ctx, Session{TokenHash: "x", CSRF: "c", CreatedAt: time.Now(), LastSeen: time.Now()})
	if err := s.ResetAdmin(ctx, "h3"); err != nil {
		t.Fatal(err)
	}
	a, _, _ = s.GetAdmin(ctx)
	if a.PasswordHash != "h3" || a.TOTPSecret != "" || a.TOTPLastStep != 0 {
		t.Fatalf("after reset = %+v", a)
	}
	if _, ok, _ := s.GetSession(ctx, "x"); ok {
		t.Error("reset must delete sessions")
	}
}

func TestAdminAbsentAndResetCreates(t *testing.T) {
	s := openTest(t)
	if _, ok, err := s.GetAdmin(ctx); ok || err != nil {
		t.Fatalf("empty db: ok=%v err=%v", ok, err)
	}
	if ok, err := s.UseTOTPStep(ctx, 5); ok || err != nil {
		t.Fatalf("UseTOTPStep without admin: ok=%v err=%v", ok, err)
	}
	if err := s.ResetAdmin(ctx, "h"); err != nil {
		t.Fatal(err)
	}
	if a, ok, _ := s.GetAdmin(ctx); !ok || a.PasswordHash != "h" {
		t.Fatalf("reset must create admin: %+v %v", a, ok)
	}
	if err := s.SetAdminPassword(ctx, "h2"); err != nil {
		t.Fatal(err)
	}
	if a, _, _ := s.GetAdmin(ctx); a.PasswordHash != "h2" {
		t.Fatalf("admin = %+v", a)
	}
}

func TestSessions(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_000_000, 0)
	for _, h := range []string{"a", "b", "old"} {
		created := now
		if h == "old" {
			created = now.Add(-13 * time.Hour)
		}
		if err := s.CreateSession(ctx, Session{TokenHash: h, CSRF: "csrf-" + h, CreatedAt: created, LastSeen: created, IP: "ip", UserAgent: "ua"}); err != nil {
			t.Fatal(err)
		}
	}
	se, ok, err := s.GetSession(ctx, "a")
	if !ok || err != nil || se.CSRF != "csrf-a" || se.UserAgent != "ua" {
		t.Fatalf("get = %+v %v %v", se, ok, err)
	}
	s.TouchSession(ctx, "a", now.Add(time.Minute))
	if se, _, _ = s.GetSession(ctx, "a"); !se.LastSeen.Equal(now.Add(time.Minute)) {
		t.Errorf("touch: %v", se.LastSeen)
	}
	n, err := s.PruneSessions(ctx, now.Add(-30*time.Minute), now.Add(-12*time.Hour))
	if n != 1 || err != nil {
		t.Fatalf("prune = %d %v", n, err)
	}
	s.DeleteOtherSessions(ctx, "a")
	if _, ok, _ := s.GetSession(ctx, "b"); ok {
		t.Error("other session survived")
	}
	s.DeleteSession(ctx, "a")
	if _, ok, _ := s.GetSession(ctx, "a"); ok {
		t.Error("deleted session survived")
	}
}

func TestListLogAndCounts(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	for i, mb := range []string{"a", "b", "a"} {
		res := "sent"
		if i == 2 {
			res = "failed"
		}
		if _, err := s.RecordMessage(ctx, LogEntry{At: now.Add(time.Duration(i) * time.Second), Mailbox: mb, Folder: "INBOX", UID: uint32(i + 1),
			FromAddr: "x@y", Subject: "s", Kind: "code", Result: res}, nil, MailboxState{UIDValidity: 1, LastUID: uint32(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	rows, total, err := s.ListLog(ctx, LogQuery{Mailbox: "a", Limit: 1})
	if err != nil || total != 2 || len(rows) != 1 || rows[0].Result != "failed" {
		t.Fatalf("rows=%+v total=%d err=%v", rows, total, err)
	}
	if _, total, _ := s.ListLog(ctx, LogQuery{Result: "sent", Limit: 10}); total != 2 {
		t.Errorf("result filter total = %d", total)
	}
	if n, err := s.CountOutbox(ctx); n != 0 || err != nil {
		t.Errorf("outbox = %d %v", n, err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Error(err)
	}
}

func TestDeleteOAuthToken(t *testing.T) {
	s := openTest(t)
	s.SaveOAuthToken(ctx, "m", OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Now()})
	if err := s.DeleteOAuthToken(ctx, "m"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GetOAuthToken(ctx, "m"); ok {
		t.Error("token survived")
	}
}
