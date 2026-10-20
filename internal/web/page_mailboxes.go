package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/watcher"
)

// Mailbox pages: the list, one form page for creating and editing (the name is fixed once
// created), delete and the connection test. Stale OAuth tokens (another account or server,
// or a deleted mailbox) are dropped by saveConfig.
//
// Form error keys (FieldErrors) are the form field names: "name", "display_name", "host",
// "port", "username", "auth_type", "password", "client_id", "tenant", "folders" (every
// folders[i] problem), "targets" (targets and targets[i]), "max_age", "ttl",
// "poll_interval", "tpl_code", "tpl_link", "tpl_both"; "" holds the rest as "path: message".

// probeTimeout bounds a connection test (connect, log in, list folders).
const probeTimeout = 20 * time.Second

const (
	authPassword  = "password"
	authMicrosoft = "microsoft_oauth"

	msgMailboxGone  = "找不到信箱 %s，可能已被刪除"
	msgNeedsOAuth   = "請先完成 Microsoft 授權"
	msgReservedName = "「new」是保留字，請換一個名稱"
	// a blank password field on another server: the stored password stays with its server
	msgRetypePassword = "主機或帳號已變更，請重新輸入密碼"
)

// mailboxPreset is a service type card: it only fills in the form (in app.js); the
// choice itself is not stored.
type mailboxPreset struct {
	Key, Label, Host, Auth, Note, Glyph string
}

var mailboxPresets = []mailboxPreset{
	{"gmail", "Gmail", "imap.gmail.com", authPassword, "應用程式密碼", "G"},
	{"icloud", "iCloud", "imap.mail.me.com", authPassword, "App 專用密碼", "i"},
	{"outlook", "Outlook", "outlook.office365.com", authMicrosoft, "Microsoft 授權", "O"},
	{"custom", "自訂", "", authPassword, "任何 IMAP 信箱", "@"},
}

// glyphFor is the initial engraved on a mailbox's stamp.
func glyphFor(host string) string {
	k := presetFor(host)
	for _, p := range mailboxPresets {
		if p.Key == k {
			return p.Glyph
		}
	}
	return "@"
}

// presetFor guesses the service card of a stored mailbox from its host.
func presetFor(host string) string {
	for _, p := range mailboxPresets {
		if p.Host != "" && strings.EqualFold(p.Host, host) {
			return p.Key
		}
	}
	return "custom"
}

// mailboxForm holds the form values as typed (never the password).
type mailboxForm struct {
	Name, DisplayName, Preset, Host, Port, Username string
	AuthType, ClientID, Tenant                      string
	Folders, Targets                                []string
	MaxAge, TTL, PollInterval                       string
	TplCode, TplLink, TplBoth                       string
}

func formFromMailbox(name string, m *config.Mailbox) mailboxForm {
	f := mailboxForm{
		Name: name, DisplayName: m.DisplayName, Preset: presetFor(m.Host), Host: m.Host, Username: m.Username,
		AuthType: m.Auth.Type, ClientID: m.Auth.ClientID, Tenant: m.Auth.Tenant,
		Folders: append([]string(nil), m.Folders...), Targets: append([]string(nil), m.Targets...),
		MaxAge: durationValue(m.MaxAge), TTL: durationValue(m.TTL), PollInterval: durationValue(m.PollInterval),
		TplCode: m.Templates.Code, TplLink: m.Templates.Link, TplBoth: m.Templates.Both,
	}
	if m.Port != 0 {
		f.Port = strconv.Itoa(m.Port)
	}
	if f.AuthType == "" {
		f.AuthType = authPassword
	}
	return f
}

// readMailboxForm reads the posted form. Empty folder rows are dropped; template text
// gets the browser's CRLF line ends turned back into LF.
func readMailboxForm(r *http.Request) mailboxForm {
	v := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	tpl := func(k string) string { return strings.TrimSpace(strings.ReplaceAll(r.PostFormValue(k), "\r\n", "\n")) }
	f := mailboxForm{
		Name: v("name"), DisplayName: v("display_name"), Preset: v("preset"), Host: v("host"), Port: v("port"),
		Username: v("username"), AuthType: v("auth_type"), ClientID: v("client_id"), Tenant: v("tenant"),
		MaxAge: v("max_age"), TTL: v("ttl"), PollInterval: v("poll_interval"),
		TplCode: tpl("tpl_code"), TplLink: tpl("tpl_link"), TplBoth: tpl("tpl_both"),
	}
	for _, x := range r.PostForm["folders"] {
		if x = strings.TrimSpace(x); x != "" {
			f.Folders = append(f.Folders, x)
		}
	}
	for _, x := range r.PostForm["targets"] {
		if x = strings.TrimSpace(x); x != "" {
			f.Targets = append(f.Targets, x)
		}
	}
	if f.AuthType == "" {
		f.AuthType = authPassword
	}
	return f
}

// parsePort reads the port field: "" = 0 (the default 993).
func parsePort(v string) (int, string) {
	if v == "" {
		return 0, ""
	}
	p, err := strconv.Atoi(v)
	if err != nil || p < 1 || p > 65535 {
		return 0, "請輸入 1 到 65535 的數字（留空為 993）"
	}
	return p, ""
}

// mailbox builds the config entry from the form. old (nil when creating) supplies the
// stored password when the password field is left blank, but only while host, port and
// username stay the same (sameServer): a stored password is never sent to another server.
// Switching to Microsoft drops the password; switching to a password drops client_id and
// tenant.
func (f mailboxForm) mailbox(old *config.Mailbox, password string) (*config.Mailbox, FieldErrors) {
	fe := FieldErrors{}
	m := &config.Mailbox{
		Name: f.Name, DisplayName: f.DisplayName, Host: f.Host, Username: f.Username,
		Folders: f.Folders, Targets: f.Targets,
		Templates: config.Templates{Code: f.TplCode, Link: f.TplLink, Both: f.TplBoth},
	}
	var msg string
	if m.Port, msg = parsePort(f.Port); msg != "" {
		fe["port"] = msg
	}
	switch f.AuthType {
	case authMicrosoft:
		m.Auth = config.Auth{Type: authMicrosoft, ClientID: f.ClientID, Tenant: f.Tenant}
	case authPassword:
		m.Auth = config.Auth{Type: authPassword, Password: password}
		if password == "" && old != nil && old.Auth.Type == authPassword && old.Auth.Password != "" {
			if fe["port"] == "" && sameServer(old, m.Host, effectivePort(m.Port), m.Username) {
				m.Auth.Password = old.Auth.Password
			} else if fe["port"] == "" {
				fe["password"] = msgRetypePassword
			}
		}
	default:
		fe["auth_type"] = "請選擇認證方式"
	}
	for _, d := range []struct {
		key string
		val string
		out *config.Duration
	}{{"max_age", f.MaxAge, &m.MaxAge}, {"ttl", f.TTL, &m.TTL}, {"poll_interval", f.PollInterval, &m.PollInterval}} {
		v, err := parseDuration(d.val)
		if err != nil {
			fe[d.key] = err.Error()
		}
		*d.out = v
	}
	return m, fe
}

// remapMailbox moves Build's problems about mailbox key to form field keys; problems
// elsewhere in the config go, as "path: message", under "".
func remapMailbox(fe FieldErrors, key string) FieldErrors {
	base := "mailboxes." + key
	fields := map[string]string{
		"": "name", ".host": "host", ".port": "port", ".username": "username",
		".auth.type": "auth_type", ".auth.password": "password", ".auth.client_id": "client_id", ".auth.tenant": "tenant",
		".targets": "targets", ".max_age": "max_age", ".ttl": "ttl", ".poll_interval": "poll_interval",
		".templates.code": "tpl_code", ".templates.link": "tpl_link", ".templates.both": "tpl_both",
	}
	out := FieldErrors{}
	rest := FieldErrors{}
	for _, k := range sortedNames(fe) { // stable order when several messages join
		v := fe[k]
		sub, ok := strings.CutPrefix(k, base)
		if ok && (sub == "" || sub[0] == '.') {
			switch {
			case strings.HasPrefix(sub, ".folders["):
				out.add("folders", v)
				continue
			case strings.HasPrefix(sub, ".targets["):
				out.add("targets", v)
				continue
			}
			if to, ok := fields[sub]; ok {
				out.add(to, v)
				continue
			}
		}
		rest[k] = v
	}
	if len(rest) > 0 {
		out.add("", rest.Error())
	}
	return out
}

func checkMailboxName(name string, taken bool) string {
	if name == "new" {
		return msgReservedName
	}
	return checkName(name, taken)
}

// ---------- list ----------

type folderRow struct {
	Name, Label, Class string
	Live               bool
}

type mailboxRow struct {
	Name, DisplayName, Username, Host, Glyph string
	Port                                     int
	Microsoft, NeedsAuth                     bool // NeedsAuth: no token stored
	NeedsLogin                               bool // a folder's watcher reports needs_login
	Folders                                  []folderRow
	Targets                                  []string
}

type mailboxesData struct {
	Mailboxes  []mailboxRow
	HasTargets bool
	HasBots    bool
	Applying   bool
}

// statusBadge describes a watcher status as a badge ("" = no watcher reports it yet).
func statusBadge(st watcher.Status) (label, class string, live bool) {
	label = statusLabels[st]
	switch st {
	case watcher.StatusConnected:
		return label, "badge-ok", false
	case watcher.StatusConnecting:
		return label, "badge-info", true
	case watcher.StatusReconnecting:
		return label, "badge-warn", true
	case watcher.StatusAuthFailed, watcher.StatusNeedsLogin:
		return label, "badge-err", false
	case watcher.StatusStopped:
		return label, "badge-idle", false
	}
	return "套用中", "badge-idle", true
}

func (s *Server) getMailboxes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw, _, err := runtime.LoadConfig(ctx, s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	snap := s.o.Runtime.Snapshot()
	status := map[[2]string]watcher.Status{}
	for _, ws := range snap.Watchers {
		status[[2]string{ws.Mailbox, ws.Folder}] = ws.Status
	}
	d := mailboxesData{HasTargets: len(raw.Targets) > 0, HasBots: len(raw.Bots) > 0, Applying: snap.Applying}
	for _, name := range sortedNames(raw.Mailboxes) {
		m := raw.Mailboxes[name]
		if m == nil {
			continue
		}
		row := mailboxRow{Name: name, DisplayName: m.DisplayName, Username: m.Username, Host: m.Host, Port: m.Port, Glyph: glyphFor(m.Host),
			Microsoft: m.Auth.Type == authMicrosoft, Targets: m.Targets}
		if row.Port == 0 {
			row.Port = 993
		}
		if row.DisplayName == "" {
			row.DisplayName = name
		}
		if row.Microsoft {
			_, ok, err := s.o.Store.GetOAuthToken(ctx, name)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			row.NeedsAuth = !ok
		}
		folders := m.Folders
		if len(folders) == 0 {
			folders = []string{"INBOX"}
		}
		for _, f := range folders {
			st, ok := status[[2]string{name, f}]
			fr := folderRow{Name: f}
			if ok {
				fr.Label, fr.Class, fr.Live = statusBadge(st)
			} else {
				fr.Label, fr.Class, fr.Live = statusBadge("")
			}
			row.Folders = append(row.Folders, fr)
			row.NeedsLogin = row.NeedsLogin || (ok && st == watcher.StatusNeedsLogin)
		}
		d.Mailboxes = append(d.Mailboxes, row)
	}
	s.render(w, r, http.StatusOK, "mailboxes", Page{Title: "信箱", Nav: "mailboxes", Data: d})
}

// ---------- form ----------

type routeOption struct {
	Name, Bot string
	ChatID    int64
	Checked   bool
}

type mailboxFormData struct {
	New             bool
	Name            string // the mailbox being edited ("" when creating)
	F               mailboxForm
	Presets         []mailboxPreset
	Targets         []routeOption
	HasPassword     bool                                        // a password is stored (edit, password auth)
	PasswordDropped bool                                        // a typed password is never sent back: say so
	StoredMicrosoft bool                                        // the stored mailbox uses Microsoft (authorisation applies)
	Authorized      bool                                        // an OAuth token is stored
	NeedsLogin      bool                                        // authorised, but a watcher reports the grant no longer works
	Stored          struct{ Username, ClientID, Tenant string } // what an authorisation is for (Microsoft)
	Defaults        struct{ MaxAge, TTL, PollInterval string }
	AdvancedOpen    bool
	AdvancedSet     int // advanced fields with a value
}

func (s *Server) getMailboxNew(w http.ResponseWriter, r *http.Request) {
	f := mailboxForm{Preset: "gmail", Host: "imap.gmail.com", AuthType: authPassword, Tenant: "consumers", Folders: []string{"INBOX"}}
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(raw.Targets) == 1 { // the only choice: preselect it
		for name := range raw.Targets {
			f.Targets = []string{name}
		}
	}
	s.renderMailboxForm(w, r, http.StatusOK, raw, "", f, nil, false)
}

func (s *Server) getMailboxEdit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	m := raw.Mailboxes[name]
	if m == nil {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf(msgMailboxGone, name)+"。")
		return
	}
	s.renderMailboxForm(w, r, http.StatusOK, raw, name, formFromMailbox(name, m), nil, false)
}

// renderMailboxForm shows the form for f; name is the edited mailbox ("" = new).
func (s *Server) renderMailboxForm(w http.ResponseWriter, r *http.Request, status int, raw *config.Config, name string, f mailboxForm, errs FieldErrors, dropped bool) {
	d := mailboxFormData{New: name == "", Name: name, F: f, Presets: mailboxPresets, PasswordDropped: dropped}
	if d.F.Preset == "" {
		d.F.Preset = presetFor(f.Host)
	}
	checked := map[string]bool{}
	for _, t := range f.Targets {
		checked[t] = true
	}
	for _, t := range sortedNames(raw.Targets) {
		tg := raw.Targets[t]
		d.Targets = append(d.Targets, routeOption{Name: t, Bot: tg.Bot, ChatID: tg.ChatID, Checked: checked[t]})
	}
	if old := raw.Mailboxes[name]; name != "" && old != nil {
		d.HasPassword = old.Auth.Type == authPassword && old.Auth.Password != ""
		d.StoredMicrosoft = old.Auth.Type == authMicrosoft
		if d.StoredMicrosoft {
			d.Stored.Username, d.Stored.ClientID, d.Stored.Tenant = old.Username, old.Auth.ClientID, old.Auth.Tenant
			_, ok, err := s.o.Store.GetOAuthToken(r.Context(), name)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			d.Authorized = ok
			for _, ws := range s.o.Runtime.Snapshot().Watchers {
				if ws.Mailbox == name && ws.Status == watcher.StatusNeedsLogin {
					d.NeedsLogin = ok
				}
			}
		}
	}
	def := raw.Defaulted().Defaults
	d.Defaults.MaxAge, d.Defaults.TTL, d.Defaults.PollInterval = shortDuration(def.MaxAge.Duration), shortDuration(def.TTL.Duration), shortDuration(def.PollInterval.Duration)
	for _, k := range []string{"max_age", "ttl", "poll_interval", "tpl_code", "tpl_link", "tpl_both"} {
		if errs[k] != "" {
			d.AdvancedOpen = true
		}
	}
	for _, v := range []string{f.MaxAge, f.TTL, f.PollInterval, f.TplCode, f.TplLink, f.TplBoth} {
		if v != "" {
			d.AdvancedSet++
		}
	}
	title := "新增信箱"
	if name != "" {
		title = "編輯信箱 " + name
	}
	s.render(w, r, status, "mailbox_form", Page{Title: title, Nav: "mailboxes", Errors: errs, Data: d})
}

func (s *Server) postMailboxNew(w http.ResponseWriter, r *http.Request) { s.saveMailbox(w, r, "") }

func (s *Server) postMailboxEdit(w http.ResponseWriter, r *http.Request) {
	s.saveMailbox(w, r, r.PathValue("name"))
}

// saveMailbox creates (name == "") or updates mailbox name from the posted form. Every
// problem is collected first (local checks plus a trial Build of the whole config), so the
// form comes back once with all of them; then mutate stores and applies the change.
func (s *Server) saveMailbox(w http.ResponseWriter, r *http.Request, name string) {
	f := readMailboxForm(r)
	password := strings.TrimSpace(r.PostFormValue("password"))
	isNew := name == ""
	key := name
	if isNew {
		key = f.Name
	} else {
		f.Name = name
	}
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if raw.Mailboxes == nil {
		raw.Mailboxes = map[string]*config.Mailbox{}
	}
	old := raw.Mailboxes[name]
	if !isNew && old == nil {
		s.redirect(w, r, "/mailboxes", &Flash{Kind: "error", Text: fmt.Sprintf(msgMailboxGone, name)})
		return
	}
	back := func(fe FieldErrors) {
		cur, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.renderMailboxForm(w, r, http.StatusUnprocessableEntity, cur, name, f, fe, password != "")
	}

	fe := FieldErrors{}
	trialKey := key
	if isNew {
		_, taken := raw.Mailboxes[f.Name]
		if msg := checkMailboxName(f.Name, taken); msg != "" {
			fe["name"] = msg
			trialKey = "form-trial-mailbox" // still check every other field under a valid key
			for raw.Mailboxes[trialKey] != nil {
				trialKey += "-x"
			}
		}
	}
	m, local := f.mailbox(old, password)
	for k, v := range local {
		fe.add(k, v)
	}
	// fields that failed locally were left unset, so the trial reports everything else
	raw.Mailboxes[trialKey] = m
	if _, err := runtime.Build(raw); err != nil {
		for k, v := range remapMailbox(fieldErrors(err), trialKey) {
			if local[k] != "" || (k == "name" && trialKey != key) {
				continue
			}
			fe.add(k, v)
		}
	}
	if len(fe) > 0 {
		back(fe)
		return
	}

	summary := "修改信箱 " + key
	if isNew {
		summary = "新增信箱 " + key
	}
	res, fe, err := s.saveConfig(r, "web", summary, func(raw *config.Config) error {
		if raw.Mailboxes == nil {
			raw.Mailboxes = map[string]*config.Mailbox{}
		}
		cur := raw.Mailboxes[key]
		if isNew && cur != nil {
			return FieldErrors{"name": msgNameTaken}
		}
		if !isNew && cur == nil {
			return FieldErrors{"": fmt.Sprintf(msgMailboxGone, key)}
		}
		next, local := f.mailbox(cur, password)
		if len(local) > 0 {
			return local
		}
		if !isNew {
			// checkbox and probe order is not a change: keep the stored order of the same set
			next.Folders = keepOrder(cur.Folders, next.Folders)
			next.Targets = keepOrder(cur.Targets, next.Targets)
		}
		if !isNew && reflect.DeepEqual(cur, next) {
			return errNoChange
		}
		raw.Mailboxes[key] = next
		return nil
	})
	switch {
	case errors.Is(err, errNoChange):
		s.redirect(w, r, "/mailboxes", &Flash{Kind: "info", Text: msgNoChange})
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	case len(fe) > 0:
		back(remapMailbox(fe, key))
		return
	}
	done := "已儲存信箱 " + key + " 並套用"
	switch {
	case isNew:
		done = "已新增信箱 " + key + " 並套用"
		if f.AuthType == authMicrosoft {
			done += "。下一步：在信箱頁按「授權」登入 Microsoft 帳號。"
			s.redirect(w, r, "/mailboxes/"+key, &Flash{Kind: "success", Text: done})
			return
		}
	case res.failed(key):
		done += "，但舊的 Microsoft 授權沒有清除成功，詳情請看服務日誌"
	case res.dropped(key) && f.AuthType == authMicrosoft:
		done += "。主機、帳號或應用程式已變更，舊的 Microsoft 授權已清除，請重新授權。"
		s.redirect(w, r, "/mailboxes/"+key, &Flash{Kind: "success", Text: done})
		return
	case res.dropped(key):
		done += "，並清除了它的 Microsoft 授權"
	}
	s.redirect(w, r, "/mailboxes", &Flash{Kind: "success", Text: done})
}

// keepOrder returns old when next holds the same items in another order, else next.
func keepOrder(old, next []string) []string {
	if len(old) != len(next) {
		return next
	}
	count := map[string]int{}
	for _, x := range old {
		count[x]++
	}
	for _, x := range next {
		if count[x]--; count[x] < 0 {
			return next
		}
	}
	return old
}

// effectiveTenant is the tenant a Microsoft login uses (config defaults blank to common).
func effectiveTenant(t string) string {
	if t == "" {
		return "common"
	}
	return t
}

// effectivePort is the port a mailbox connects to (config defaults 0 to 993).
func effectivePort(p int) int {
	if p == 0 {
		return 993
	}
	return p
}

// tokenInvalidated reports whether a stored OAuth token of mailbox cur no longer fits next:
// it stopped using Microsoft, or the account, client_id, tenant, host or port changed.
func tokenInvalidated(cur, next *config.Mailbox) bool {
	if cur == nil || cur.Auth.Type != authMicrosoft {
		return false
	}
	return next.Auth.Type != authMicrosoft || accountOf(next) != accountOf(cur)
}

func (s *Server) postMailboxDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	res, fe, err := s.saveConfig(r, "web", "刪除信箱 "+name, func(raw *config.Config) error {
		if raw.Mailboxes[name] == nil {
			return FieldErrors{"": fmt.Sprintf(msgMailboxGone, name)}
		}
		delete(raw.Mailboxes, name)
		return nil
	})
	switch {
	case err != nil:
		s.serverError(w, r, err)
		return
	case len(fe) > 0:
		s.redirect(w, r, "/mailboxes", &Flash{Kind: "error", Text: truncate(fe.Error(), 400)})
		return
	}
	if res.failed(name) { // saveConfig dropped the token of the removed mailbox
		s.redirect(w, r, "/mailboxes", &Flash{Kind: "info", Text: "已刪除信箱 " + name + "，但它的 Microsoft 授權沒有清除成功，詳情請看服務日誌"})
		return
	}
	s.redirect(w, r, "/mailboxes", &Flash{Kind: "success", Text: "已刪除信箱 " + name})
}

// ---------- connection test ----------

type probeRequest struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Username string `json:"username"`
	AuthType string `json:"auth_type"`
	Password string `json:"password"`
	ClientID string `json:"client_id"`
	Tenant   string `json:"tenant"`
}

// sameServer reports whether m talks to host:port as username, so that its stored
// credentials may be used for a test with these values (and never sent anywhere else).
func sameServer(m *config.Mailbox, host string, port int, username string) bool {
	return strings.EqualFold(m.Host, host) && effectivePort(m.Port) == port && m.Username == username
}

// postAPIProbe logs in with the form's current values and lists the server's folders:
// {"ok":true,"folders":[…]} or {"ok":false,"error":"…"}.
func (s *Server) postAPIProbe(w http.ResponseWriter, r *http.Request) {
	var in probeRequest
	if !s.readJSON(w, r, &in) {
		return
	}
	in.Name, in.Host, in.Port, in.Username = strings.TrimSpace(in.Name), strings.TrimSpace(in.Host), strings.TrimSpace(in.Port), strings.TrimSpace(in.Username)
	in.Password, in.ClientID, in.Tenant = strings.TrimSpace(in.Password), strings.TrimSpace(in.ClientID), strings.TrimSpace(in.Tenant)
	bad := func(msg string) { s.json(w, http.StatusOK, map[string]any{"ok": false, "error": msg}) }
	port, msg := parsePort(in.Port)
	switch {
	case in.Host == "":
		bad("請輸入 IMAP 主機")
		return
	case msg != "":
		bad("埠號：" + msg)
		return
	case in.Username == "":
		bad("請輸入帳號")
		return
	}
	if port == 0 {
		port = 993
	}
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var stored *config.Mailbox
	if in.Name != "" {
		stored = raw.Mailboxes[in.Name]
	}

	var authn auth.Authenticator
	switch in.AuthType {
	case authMicrosoft:
		if stored == nil || stored.Auth.Type != authMicrosoft {
			bad(msgNeedsOAuth + "：先儲存信箱，再按「授權」登入 Microsoft 帳號")
			return
		}
		if _, ok, err := s.o.Store.GetOAuthToken(r.Context(), in.Name); err != nil {
			s.serverError(w, r, err)
			return
		} else if !ok {
			bad(msgNeedsOAuth + "：按「授權」登入 Microsoft 帳號後再測試")
			return
		}
		if !sameServer(stored, in.Host, port, in.Username) {
			bad("主機、埠號或帳號與已儲存的不同：授權只會用在原本的伺服器，請先儲存再測試")
			return
		}
		if in.ClientID != stored.Auth.ClientID || effectiveTenant(in.Tenant) != effectiveTenant(stored.Auth.Tenant) {
			bad("應用程式識別碼或 tenant 與已儲存的不同：授權屬於原本的應用程式，請先儲存（需要重新授權）再測試")
			return
		}
		// only the stored settings ever drive the stored token
		authn = &auth.Microsoft{Mailbox: in.Name, Username: stored.Username, ClientID: stored.Auth.ClientID, Tenant: stored.Auth.Tenant,
			Store: s.o.Store, Authority: s.o.MSAuthority, Log: s.o.Log}
	case authPassword:
		pw := in.Password
		if pw == "" {
			switch {
			case stored == nil || stored.Auth.Type != authPassword || stored.Auth.Password == "":
				bad("請輸入密碼")
				return
			case !sameServer(stored, in.Host, port, in.Username):
				bad("主機、埠號或帳號與已儲存的不同：已儲存的密碼只會送往原本的伺服器，請輸入密碼再測試")
				return
			}
			pw = stored.Auth.Password
		}
		in.Password = pw // for scrubbing below
		authn = auth.Password{Username: in.Username, Password: pw}
	default:
		bad("請選擇認證方式")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()
	folders, err := watcher.Probe(ctx, s.o.Dial(in.Host, port), authn)
	if err != nil {
		text := probeFailure(ctx, in.Host, port, err, in.Password)
		s.o.Log.Info("信箱連線測試失敗", "mailbox", in.Name, "host", in.Host, "port", port, "err", text)
		bad(text)
		return
	}
	s.o.Log.Info("信箱連線測試成功", "mailbox", in.Name, "host", in.Host, "port", port, "folders", len(folders))
	if folders == nil {
		folders = []string{}
	}
	s.json(w, http.StatusOK, map[string]any{"ok": true, "folders": folders})
}

// probeFailure explains a failed connection test. Details from the server or the network
// are left out whenever they contain the password.
func probeFailure(ctx context.Context, host string, port int, err error, password string) string {
	detail := func(s string) string {
		if s == "" || (password != "" && strings.Contains(s, password)) {
			return ""
		}
		return "（" + truncate(s, 200) + "）"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("逾時：%d 秒內沒有完成連線與登入。請確認主機 %s、埠號 %d 正確，且伺服器支援 SSL/TLS（不是 STARTTLS）。", int(probeTimeout/time.Second), host, port)
	}
	var ae *watcher.AuthError
	if errors.As(err, &ae) {
		if errors.Is(err, auth.ErrNeedsLogin) {
			return "Microsoft 授權已失效，請重新授權" + detail(err.Error())
		}
		var ie *imap.Error
		text := ""
		if errors.As(err, &ie) {
			text = ie.Text
		}
		return "登入失敗：伺服器拒絕了帳號或密碼" + detail(text) + "。Gmail、iCloud 必須使用應用程式密碼，不是平常登入的密碼。"
	}
	if errors.Is(err, watcher.ErrListFolders) {
		return "已登入，但無法列出資料夾" + detail(strings.TrimPrefix(err.Error(), watcher.ErrListFolders.Error()+"："))
	}
	if rest, ok := strings.CutPrefix(err.Error(), "認證："); ok { // e.g. a failed OAuth token refresh
		return "登入過程失敗" + detail(rest)
	}
	return fmt.Sprintf("無法連線到 %s:%d", host, port) + detail(strings.TrimPrefix(err.Error(), "連線："))
}
