package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/watcher"
)

// The overview: system health, one card per mailbox with its folders' states, the
// pending outbox, the latest forwards and, on a fresh install, the setup steps. app.js
// refreshes the live parts every 10 seconds from GET /api/status; every label, class and
// health decision comes from here, so the page and the JSON never disagree.

// recentLimit is the number of forward_log rows on the overview: the latest forwarded
// mail, or with "?recent=all" the latest checked mail of any kind.
const recentLimit = 10

// statusLabels are the watcher states in words (the overview, the mailbox list, the JSON).
var statusLabels = map[watcher.Status]string{
	watcher.StatusConnecting:   "連線中",
	watcher.StatusConnected:    "已連線",
	watcher.StatusReconnecting: "重新連線中",
	watcher.StatusAuthFailed:   "認證失敗",
	watcher.StatusNeedsLogin:   "需要授權",
	watcher.StatusStopped:      "已停止",
}

// troubled reports whether st means the folder cannot be watched right now. stopped is
// not: a watcher only stops when its service is being replaced or shut down.
func troubled(st watcher.Status) bool {
	switch st {
	case watcher.StatusReconnecting, watcher.StatusAuthFailed, watcher.StatusNeedsLogin:
		return true
	}
	return false
}

// dashFolder is one watched folder on a mailbox card.
type dashFolder struct {
	Name   string
	Status string // watcher status; "" when no watcher runs for it (yet)
	Known  bool   // a watcher reports this folder
	Label  string
	Class  string
	Live   bool
}

// dashMailbox is one status card.
type dashMailbox struct {
	Name, DisplayName, Username, Glyph string
	Microsoft                          bool
	NeedsAuth                          bool // Microsoft without a stored token
	Folders                            []dashFolder
	State                              string // ok | busy | bad
	Action                             string // oauth | edit | ""
	Note                               string // what is going on, for a card that is not ok
}

// finish fills in the folder badges and the card's state, action and note:
//   - bad: some folder is reconnecting, failed to authenticate or needs authorisation;
//     needs_login on a Microsoft mailbox offers 重新授權, auth_failed links to the
//     mailbox form;
//   - busy: no folder is in trouble but one is still connecting, stopped (a new service is
//     taking over) or has no watcher yet;
//   - ok: every folder is connected.
func (m *dashMailbox) finish() {
	var bad, busy, needsLogin, authFailed, reconnecting, stopped bool
	for i := range m.Folders {
		f := &m.Folders[i]
		st := watcher.Status(f.Status)
		if !f.Known {
			st = ""
		}
		f.Label, f.Class, f.Live = statusBadge(st)
		switch {
		case troubled(st):
			bad = true
		case st != watcher.StatusConnected:
			busy = true
		}
		needsLogin = needsLogin || st == watcher.StatusNeedsLogin
		authFailed = authFailed || st == watcher.StatusAuthFailed
		reconnecting = reconnecting || st == watcher.StatusReconnecting
		stopped = stopped || st == watcher.StatusStopped
	}
	m.State, m.Action, m.Note = "ok", "", ""
	switch {
	case bad:
		m.State = "bad"
	case busy:
		m.State = "busy"
	}
	switch {
	case needsLogin && m.Microsoft:
		m.Action, m.Note = "oauth", "Microsoft 授權已失效，重新授權後才會恢復監看。"
	case needsLogin:
		m.Action, m.Note = "edit", "這個信箱需要重新登入，請檢查信箱設定。"
	case authFailed:
		m.Action, m.Note = "edit", "伺服器拒絕了帳號或密碼，請到信箱設定更新後再儲存。"
	case reconnecting:
		m.Note = "連線中斷，正在自動重試；持續失敗會發出 Telegram 告警。"
	case stopped:
		m.Note = "監看已停止，新的設定套用後會重新啟動。"
	}
}

// dashHealth is the overall verdict at the top of the overview.
type dashHealth struct {
	State     string `json:"state"` // ok | info | warn | err | idle
	Word      string `json:"word"`  // the postmark's middle text
	Title     string `json:"title"`
	Text      string `json:"text"`
	Mailboxes int    `json:"mailboxes"`
	OK        int    `json:"ok"`
	Bad       int    `json:"bad"`
	Busy      int    `json:"busy"`
}

func health(cards []dashMailbox, applying bool) dashHealth {
	h := dashHealth{Mailboxes: len(cards)}
	for _, c := range cards {
		switch c.State {
		case "ok":
			h.OK++
		case "bad":
			h.Bad++
		default:
			h.Busy++
		}
	}
	switch {
	case applying:
		h.State, h.Word, h.Title, h.Text = "warn", "套用中", "正在套用新的設定", "信箱連線會重新建立，狀態稍後更新。"
	case h.Mailboxes == 0:
		h.State, h.Word, h.Title, h.Text = "idle", "待設定", "還沒有要監看的信箱", "照下方步驟完成設定，收到的驗證碼就會轉到 Telegram。"
	case h.Bad > 0:
		h.State, h.Word, h.Title = "err", "需處理", fmt.Sprintf("%d 個信箱需要處理", h.Bad)
		h.Text = "有信箱目前沒有在監看，這段期間收到的驗證碼不會轉發。請看下方標成紅色的信箱。"
	case h.Busy > 0:
		h.State, h.Word, h.Title, h.Text = "info", "連線中", "信箱連線中", "剛啟動或剛套用設定，連上後就會開始監看。"
	default:
		h.State, h.Word, h.Title, h.Text = "ok", "正常", "所有信箱都在監看中", "收到驗證碼或登入連結就會轉到 Telegram。"
	}
	return h
}

// logView is one forward_log row as shown (and as sent in the JSON).
type logView struct {
	At           time.Time
	Mailbox      string // the config key
	MailboxLabel string // the display name; the key when unset or the mailbox is gone (see labelLogs)
	Folder       string
	From         string
	Subject      string
	Kind         string
	KindLabel    string
	Hit          bool // something was recognised (code, link or both)
	Rule         string
	Result       string
	ResultLabel  string
	ResultClass  string
	Live         bool
}

var kindLabels = map[string]string{
	"code": "驗證碼", "link": "連結", "both": "驗證碼＋連結", "none": "未辨識",
	"ignored": "規則略過", "skipped_age": "過舊", "skipped_size": "過大",
}

// resultOrder orders the result filter on the log page.
var resultOrder = []string{"pending", "sent", "failed", "expired", "not_forwarded"}

var resultLabels = map[string]string{
	"pending": "待發送", "sent": "已送出", "failed": "失敗", "expired": "已過期", "not_forwarded": "未轉發",
}

var resultBadges = map[string]string{
	"pending": "badge-info", "sent": "badge-ok", "failed": "badge-err", "expired": "badge-warn", "not_forwarded": "badge-idle",
}

func viewLog(r store.LogRow) logView {
	v := logView{At: r.At, Mailbox: r.Mailbox, MailboxLabel: r.Mailbox, Folder: r.Folder, From: r.FromAddr, Subject: r.Subject,
		Kind: r.Kind, Rule: r.Rule, Result: r.Result}
	v.KindLabel = kindLabels[r.Kind]
	if v.KindLabel == "" {
		v.KindLabel = r.Kind
	}
	v.Hit = r.Kind == "code" || r.Kind == "link" || r.Kind == "both"
	v.ResultLabel, v.ResultClass = resultLabels[r.Result], resultBadges[r.Result]
	if v.ResultLabel == "" {
		v.ResultLabel, v.ResultClass = r.Result, "badge-idle"
	}
	v.Live = r.Result == "pending"
	return v
}

// labelLogs names each row's mailbox by its display name in raw; a mailbox deleted since
// (or without a display name) keeps its key.
func labelLogs(rows []logView, raw *config.Config) {
	for i := range rows {
		if m := raw.Mailboxes[rows[i].Mailbox]; m != nil && m.DisplayName != "" {
			rows[i].MailboxLabel = m.DisplayName
		}
	}
}

// dashboard is everything the overview shows, read in one go.
type dashboard struct {
	Applying   bool
	AppliedAt  time.Time
	Pending    int
	Cards      []dashMailbox
	Health     dashHealth
	Recent     []logView
	RecentAll  bool // Recent lists every checked mail, not only forwarded mail
	Watchers   []runtime.WatcherStatus
	microsoft  map[string]bool
	HasBots    bool
	HasTargets bool
}

func (s *Server) loadDashboard(ctx context.Context, withTokens, recentAll bool) (*dashboard, error) {
	raw, _, err := runtime.LoadConfig(ctx, s.o.Store)
	if err != nil {
		return nil, err
	}
	snap := s.o.Runtime.Snapshot()
	d := &dashboard{Applying: snap.Applying, AppliedAt: snap.AppliedAt, Watchers: snap.Watchers, microsoft: map[string]bool{},
		HasBots: len(raw.Bots) > 0, HasTargets: len(raw.Targets) > 0, RecentAll: recentAll}
	if d.Pending, err = s.o.Store.CountOutbox(ctx); err != nil {
		return nil, err
	}
	lq := store.LogQuery{Limit: recentLimit}
	if !recentAll {
		lq.Kinds = logKindFilters["hit"]
	}
	rows, _, err := s.o.Store.ListLog(ctx, lq)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		d.Recent = append(d.Recent, viewLog(r))
	}
	labelLogs(d.Recent, raw)
	status := map[[2]string]watcher.Status{}
	for _, ws := range snap.Watchers {
		status[[2]string{ws.Mailbox, ws.Folder}] = ws.Status
	}
	for _, name := range sortedNames(raw.Mailboxes) {
		m := raw.Mailboxes[name]
		if m == nil {
			continue
		}
		d.microsoft[name] = m.Auth.Type == authMicrosoft
		d.Cards = append(d.Cards, dashCard(name, m, status))
		if withTokens && d.microsoft[name] {
			_, ok, err := s.o.Store.GetOAuthToken(ctx, name)
			if err != nil {
				return nil, err
			}
			d.Cards[len(d.Cards)-1].NeedsAuth = !ok
		}
	}
	d.Health = health(d.Cards, d.Applying)
	return d, nil
}

func dashCard(name string, m *config.Mailbox, status map[[2]string]watcher.Status) dashMailbox {
	c := dashMailbox{Name: name, DisplayName: m.DisplayName, Username: m.Username, Glyph: glyphFor(m.Host),
		Microsoft: m.Auth.Type == authMicrosoft}
	if c.DisplayName == "" {
		c.DisplayName = name
	}
	for _, f := range m.WatchedFolders() {
		st, ok := status[[2]string{name, f}]
		c.Folders = append(c.Folders, dashFolder{Name: f, Status: string(st), Known: ok})
	}
	c.finish()
	return c
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	d, err := s.loadDashboard(r.Context(), true, r.URL.Query().Get("recent") == "all")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "dashboard", Page{Title: "總覽", Nav: "dashboard", Data: d})
}

// ---------- GET /api/status ----------

type apiWatcher struct {
	Mailbox     string `json:"mailbox"`
	DisplayName string `json:"display_name"`
	Folder      string `json:"folder"`
	Status      string `json:"status"`
	Label       string `json:"label"`
	Class       string `json:"class"`
	Live        bool   `json:"live"`
	NeedsLogin  bool   `json:"needs_login"`
	OAuth       bool   `json:"oauth"`
}

// apiCard is a mailbox card's verdict (see dashMailbox.finish).
type apiCard struct {
	Mailbox string      `json:"mailbox"`
	State   string      `json:"state"`
	Action  string      `json:"action"`
	Note    string      `json:"note"`
	Folders []apiFolder `json:"folders"`
}

type apiFolder struct {
	Folder string `json:"folder"`
	Label  string `json:"label"`
	Class  string `json:"class"`
	Live   bool   `json:"live"`
}

type apiLog struct {
	At           string `json:"at"`
	Mailbox      string `json:"mailbox"`
	MailboxLabel string `json:"mailbox_label"`
	Folder       string `json:"folder"`
	From         string `json:"from"`
	Subject      string `json:"subject"`
	Kind         string `json:"kind"`
	KindLabel    string `json:"kind_label"`
	Hit          bool   `json:"hit"`
	Result       string `json:"result"`
	ResultLabel  string `json:"result_label"`
	ResultClass  string `json:"result_class"`
	Live         bool   `json:"live"`
}

type apiStatus struct {
	Applying  bool         `json:"applying"`
	AppliedAt string       `json:"applied_at"` // RFC3339 UTC, "" before the first apply
	Pending   int          `json:"pending"`
	Watchers  []apiWatcher `json:"watchers"`
	Recent    []apiLog     `json:"recent"`
	Mailboxes []apiCard    `json:"mailboxes"`
	Health    dashHealth   `json:"health"`
}

func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// getAPIStatus is polled by the overview. It does not count as activity for the
// session's idle timeout (see loadSession).
func (s *Server) getAPIStatus(w http.ResponseWriter, r *http.Request) {
	d, err := s.loadDashboard(r.Context(), false, r.URL.Query().Get("recent") == "all")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := apiStatus{Applying: d.Applying, AppliedAt: rfc3339(d.AppliedAt), Pending: d.Pending, Health: d.Health,
		Watchers: []apiWatcher{}, Recent: []apiLog{}, Mailboxes: []apiCard{}}
	for _, ws := range d.Watchers {
		label, class, live := statusBadge(ws.Status)
		out.Watchers = append(out.Watchers, apiWatcher{Mailbox: ws.Mailbox, DisplayName: ws.DisplayName, Folder: ws.Folder,
			Status: string(ws.Status), Label: label, Class: class, Live: live,
			NeedsLogin: ws.Status == watcher.StatusNeedsLogin, OAuth: d.microsoft[ws.Mailbox]})
	}
	for _, v := range d.Recent {
		out.Recent = append(out.Recent, apiLog{At: rfc3339(v.At), Mailbox: v.Mailbox, MailboxLabel: v.MailboxLabel, Folder: v.Folder, From: v.From,
			Subject: v.Subject, Kind: v.Kind, KindLabel: v.KindLabel, Hit: v.Hit,
			Result: v.Result, ResultLabel: v.ResultLabel, ResultClass: v.ResultClass, Live: v.Live})
	}
	for _, c := range d.Cards {
		ac := apiCard{Mailbox: c.Name, State: c.State, Action: c.Action, Note: c.Note, Folders: []apiFolder{}}
		for _, f := range c.Folders {
			ac.Folders = append(ac.Folders, apiFolder{Folder: f.Name, Label: f.Label, Class: f.Class, Live: f.Live})
		}
		out.Mailboxes = append(out.Mailboxes, ac)
	}
	s.json(w, http.StatusOK, out)
}
