package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/store"
)

func TestStatusAPI(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	resp, body := e.get("/api/status")
	if resp.StatusCode != 200 || !strings.Contains(body, `"folder":"INBOX"`) || !strings.Contains(body, `"label":"連線中"`) {
		t.Fatalf("= %d %s", resp.StatusCode, body)
	}
}

func TestStatusAPIShape(t *testing.T) {
	e := newEnv(t, withSecrets)
	e.login()
	ctx := context.Background()
	e.st.RecordMessage(ctx, store.LogEntry{At: e.now, Mailbox: "gmail", Folder: "INBOX", UID: 7, FromAddr: "no-reply@example.com",
		Subject: "****** 是你的驗證碼", Kind: "code", Result: "pending"},
		[]store.OutboxItem{{Target: "me", Bot: "main", ChatID: 42, Text: "x", CreatedAt: e.now, ExpiresAt: e.now.Add(time.Hour), NextAttemptAt: e.now.Add(time.Hour)}},
		store.MailboxState{UIDValidity: 1, LastUID: 7})
	_, body := e.get("/api/status")
	var got struct {
		Applying  bool   `json:"applying"`
		AppliedAt string `json:"applied_at"`
		Pending   int    `json:"pending"`
		Watchers  []struct {
			Mailbox     string `json:"mailbox"`
			DisplayName string `json:"display_name"`
			Folder      string `json:"folder"`
			Status      string `json:"status"`
			Label       string `json:"label"`
			NeedsLogin  bool   `json:"needs_login"`
			OAuth       bool   `json:"oauth"`
		} `json:"watchers"`
		Recent []struct {
			At, Mailbox, From, Subject, Kind, Result string
		} `json:"recent"`
		Health struct {
			Mailboxes int    `json:"mailboxes"`
			State     string `json:"state"`
		} `json:"health"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if _, err := time.Parse(time.RFC3339, got.AppliedAt); err != nil {
		t.Errorf("applied_at = %q", got.AppliedAt)
	}
	if got.Pending != 1 || len(got.Watchers) != 2 || len(got.Recent) != 1 || got.Health.Mailboxes != 2 {
		t.Fatalf("pending %d watchers %d recent %d health %+v", got.Pending, len(got.Watchers), len(got.Recent), got.Health)
	}
	for _, w := range got.Watchers {
		if w.OAuth != (w.Mailbox == "outlook") {
			t.Errorf("oauth of %s = %v", w.Mailbox, w.OAuth)
		}
	}
	r := got.Recent[0]
	if r.Mailbox != "gmail" || r.From != "no-reply@example.com" || r.Kind != "code" || r.Result != "pending" {
		t.Errorf("recent = %+v", r)
	}
	if at, err := time.Parse(time.RFC3339, r.At); err != nil || !at.Equal(e.now) {
		t.Errorf("recent at = %q", r.At)
	}
	if !strings.Contains(body, `"result_label":"待發送"`) || !strings.Contains(body, `"kind_label":"驗證碼"`) {
		t.Errorf("labels missing: %s", body)
	}
}

func TestStatusAPIEmptyListsAreArrays(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	resp, body := e.get("/api/status")
	if resp.StatusCode != 200 || !strings.Contains(body, `"watchers":[]`) || !strings.Contains(body, `"recent":[]`) || !strings.Contains(body, `"pending":0`) {
		t.Fatalf("= %d %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", resp.Header)
	}
}

func TestStatusPollDoesNotKeepSessionAlive(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	for i := 1; i <= 3; i++ {
		e.now = e.now.Add(10 * time.Minute)
		if resp, _ := e.get("/api/status"); resp.StatusCode != http.StatusOK {
			t.Fatalf("poll %d = %d", i, resp.StatusCode)
		}
	}
	e.now = e.now.Add(10 * time.Minute) // 40 minutes without a page view
	if resp, body := e.get("/api/status"); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, `"ok":false`) {
		t.Fatalf("poll after idle = %d %s", resp.StatusCode, body)
	}
	// a page view does count as activity
	e.login()
	e.now = e.now.Add(20 * time.Minute)
	e.get("/logs")
	e.now = e.now.Add(20 * time.Minute)
	if resp, _ := e.get("/api/status"); resp.StatusCode != http.StatusOK {
		t.Fatalf("poll 20 min after a page view = %d", resp.StatusCode)
	}
}

func TestDashboardPage(t *testing.T) {
	e := newEnv(t, withSecrets)
	e.login()
	resp, body := e.get("/")
	if resp.StatusCode != 200 {
		t.Fatalf("= %d", resp.StatusCode)
	}
	for _, want := range []string{`data-mb="gmail"`, `data-mb="outlook"`, "個人 Gmail", `data-folder="INBOX"`, "連線中",
		`id="dlg-oauth"`, "data-status-board", "最近轉發", "還沒有轉發紀錄"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	if strings.Contains(body, "data-first-run") {
		t.Error("first-run guide shown although mailboxes exist")
	}
}

func TestDashboardFirstRun(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		delete(c.Targets, "me")
		delete(c.Bots, "main")
		c.Admin = ""
	})
	e.login()
	_, body := e.get("/")
	for _, want := range []string{"data-first-run", "新增 Bot", "新增目標", "新增信箱", `href="/telegram"`} {
		if !strings.Contains(body, want) {
			t.Errorf("first-run page lacks %q", want)
		}
	}
}

func TestDashboardActions(t *testing.T) {
	for _, c := range []struct{ mailbox, status, action string }{
		{"outlook", "needs_login", "oauth"},
		{"gmail", "auth_failed", "edit"},
		{"gmail", "reconnecting", ""},
	} {
		row := dashMailbox{Name: c.mailbox, Microsoft: c.mailbox == "outlook"}
		row.Folders = []dashFolder{{Name: "INBOX", Status: c.status, Known: true}}
		row.finish()
		if row.Action != c.action || row.State != "bad" || row.Note == "" {
			t.Errorf("%s/%s: action %q state %q note %q", c.mailbox, c.status, row.Action, row.State, row.Note)
		}
	}
	// the page renders the action for a failing mailbox
	e := newEnv(t, withSecrets)
	e.login()
	_, body := e.get("/")
	if !strings.Contains(body, "data-mb-action") {
		t.Error("no action slot on the cards")
	}
}

func TestLogsPageFiltersAndRedacts(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	e.st.RecordMessage(context.Background(), store.LogEntry{At: time.Now(), Mailbox: "gmail", Folder: "INBOX", UID: 1, FromAddr: "a@b", Subject: "****** is your code", Kind: "code", Result: "sent"}, nil, store.MailboxState{UIDValidity: 1, LastUID: 1})
	_, body := e.get("/logs?result=sent")
	if !strings.Contains(body, "****** is your code") || !strings.Contains(body, "已送出") {
		t.Fatal("log row missing")
	}
	_, body = e.get("/logs?result=failed")
	if strings.Contains(body, "is your code") {
		t.Fatal("filter ignored")
	}
}

func TestLogsPagination(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	ctx := context.Background()
	for i := 1; i <= 120; i++ {
		res := "sent"
		if i%3 == 0 {
			res = "not_forwarded"
		}
		if _, err := e.st.RecordMessage(ctx, store.LogEntry{At: e.now.Add(time.Duration(i) * time.Second), Mailbox: "gmail", Folder: "INBOX",
			UID: uint32(i), FromAddr: "a@b", Subject: fmt.Sprintf("主旨-%03d", i), Kind: "none", Result: res}, nil,
			store.MailboxState{UIDValidity: 1, LastUID: uint32(i)}); err != nil {
			t.Fatal(err)
		}
	}
	_, body := e.get("/logs")
	if !strings.Contains(body, "主旨-120") || !strings.Contains(body, "主旨-071") || strings.Contains(body, "主旨-070") {
		t.Error("page 1 should hold the newest 50")
	}
	if !strings.Contains(body, "第 1 / 3 頁") || !strings.Contains(body, "共 120 筆") || !strings.Contains(body, `href="/logs?page=2"`) {
		t.Error("page 1 navigation wrong")
	}
	_, body = e.get("/logs?page=3")
	if !strings.Contains(body, "主旨-001") || strings.Contains(body, "主旨-021") || !strings.Contains(body, `href="/logs?page=2"`) {
		t.Error("page 3 wrong")
	}
	// out-of-range and junk pages are clamped, never an error
	for q, want := range map[string]string{"page=0": "第 1 / 3 頁", "page=-4": "第 1 / 3 頁", "page=abc": "第 1 / 3 頁",
		"page=99999999999999999999": "第 3 / 3 頁", "page=4": "第 3 / 3 頁", "page=999999999": "第 3 / 3 頁"} {
		resp, body := e.get("/logs?" + q)
		if resp.StatusCode != 200 || !strings.Contains(body, want) {
			t.Errorf("%s = %d, want %q", q, resp.StatusCode, want)
		}
	}
	// filters combine with pages and stay in the links
	_, body = e.get("/logs?result=not_forwarded&mailbox=gmail")
	if !strings.Contains(body, "共 40 筆") || !strings.Contains(body, "未轉發") || strings.Contains(body, "主旨-119") {
		t.Error("result filter wrong")
	}
	_, body = e.get("/logs?result=sent&page=2")
	if !strings.Contains(body, `href="/logs?result=sent"`) || !strings.Contains(body, "第 2 / 2 頁") {
		t.Error("filter not kept in page links")
	}
	// unknown values: an empty list, not an error; a bad result filter is ignored
	resp, body := e.get("/logs?mailbox=ghost")
	if resp.StatusCode != 200 || !strings.Contains(body, "沒有符合條件的紀錄") || !strings.Contains(body, `value="ghost" selected`) {
		t.Errorf("unknown mailbox = %d", resp.StatusCode)
	}
	_, body = e.get("/logs?result=bogus")
	if !strings.Contains(body, "共 120 筆") {
		t.Error("an unknown result filter should be ignored")
	}
}

func TestLogsEmpty(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	resp, body := e.get("/logs")
	if resp.StatusCode != 200 || !strings.Contains(body, "還沒有轉發紀錄") {
		t.Fatalf("= %d", resp.StatusCode)
	}
}

func TestNoPageLeaksSecrets(t *testing.T) {
	e := newEnv(t, withSecrets) // bot token "1:SECRET-TOKEN-XYZ", mailbox password "SECRET-PW-XYZ", outlook mailbox
	e.login()
	pages := []string{"/", "/api/status", "/mailboxes", "/mailboxes/gmail", "/mailboxes/outlook", "/mailboxes/new", "/telegram",
		"/rules", "/templates", "/logs", "/settings", "/settings/versions"}
	for _, p := range pages {
		resp, body := e.get(p)
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d", p, resp.StatusCode)
		}
		a, _, _ := e.st.GetAdmin(context.Background())
		for _, secret := range []string{"SECRET-TOKEN-XYZ", "SECRET-PW-XYZ", a.TOTPSecret, a.PasswordHash} {
			if strings.Contains(body, secret) {
				t.Errorf("GET %s leaks a secret", p)
			}
		}
	}
}

// Logs and the overview's recent forwards name a mailbox by its display name, with the
// key as a secondary label; a mailbox deleted since keeps its key.
func TestLogRowsShowDisplayName(t *testing.T) {
	e := newEnv(t, withSample) // gmail: 個人 Gmail
	e.login()
	ctx := context.Background()
	for i, mb := range []string{"gmail", "gone"} {
		e.st.RecordMessage(ctx, store.LogEntry{At: e.now.Add(time.Duration(i) * time.Second), Mailbox: mb, Folder: "[Gmail]/Spam", UID: uint32(i + 1),
			FromAddr: "a@b", Subject: "s" + mb, Kind: "none", Result: "not_forwarded"}, nil, store.MailboxState{UIDValidity: 1, LastUID: uint32(i + 1)})
	}
	for _, path := range []string{"/logs", "/"} {
		_, body := e.get(path)
		if !strings.Contains(body, `<span class="log-mb">個人 Gmail<span class="log-key mono">gmail</span></span>`) {
			t.Errorf("%s: display name row missing", path)
		}
		if !strings.Contains(body, `<span class="log-mb">gone</span>`) {
			t.Errorf("%s: deleted mailbox row missing", path)
		}
	}
	_, body := e.get("/api/status")
	if !strings.Contains(body, `"mailbox":"gmail","mailbox_label":"個人 Gmail"`) || !strings.Contains(body, `"mailbox":"gone","mailbox_label":"gone"`) {
		t.Errorf("status recent = %s", body)
	}
}

// The log page promises only what the pipeline does: recognised codes are masked.
func TestLogsPageWording(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	_, body := e.get("/logs")
	if strings.Contains(body, "主旨裡的驗證碼已遮蔽") || !strings.Contains(body, "已辨識的驗證碼會在主旨中遮蔽") {
		t.Error("log page wording")
	}
}
