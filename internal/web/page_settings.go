package web

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
	"verify-code-relay/internal/websec"
)

// durationField is one of the five defaults on the settings form.
type durationField struct {
	Name    string // form field
	Path    string // config path, the FieldErrors key
	Label   string
	Hint    string
	Default string // built-in value used when the field is empty
	Value   string
}

type targetOption struct {
	Name   string
	Bot    string
	ChatID int64
}

type logLevelOption struct{ Value, Label, Hint string }

type ipInfo struct {
	Client    string // what the server uses in logs and notifications
	Peer      string // the connection's source address
	PeerProxy bool   // Peer is in RELAY_TRUSTED_PROXIES
	Forwarded string // X-Forwarded-For as received ("" if none)
	Trusted   int    // number of RELAY_TRUSTED_PROXIES prefixes
}

type settingsData struct {
	Durations []durationField
	Admin     string
	Targets   []targetOption
	LogLevel  string
	LogLevels []logLevelOption
	IP        ipInfo
	Version   int64 // current config version (0 = none)
	MinPwLen  int
}

var logLevels = []logLevelOption{
	{"debug", "除錯", "記錄每封信的處理細節，排查問題時使用"},
	{"info", "一般", "記錄轉發、連線狀態與登入（建議）"},
	{"warn", "警告", "只記錄需要注意的事件"},
	{"error", "錯誤", "只記錄失敗"},
}

// generalPaths are the config paths the general form edits.
var generalPaths = []string{"defaults.max_age", "defaults.ttl", "defaults.alert_ttl", "defaults.poll_interval", "defaults.alert_after", "admin", "log_level"}

// newSettingsData describes the form for raw (the stored config, without defaults).
func (s *Server) newSettingsData(r *http.Request, raw *config.Config, version int64) settingsData {
	def := config.Empty().Defaulted().Defaults // built-in values (Set is false: they were not given)
	d := settingsData{
		Durations: []durationField{
			{"max_age", "defaults.max_age", "郵件時效", "收到超過這段時間的信不轉發，避免服務重啟後補送過期的驗證碼。", shortDuration(def.MaxAge.Duration), durationValue(raw.Defaults.MaxAge)},
			{"ttl", "defaults.ttl", "待發訊息保留", "Telegram 暫時送不出去時，訊息最多等這麼久，逾時就丟棄。", shortDuration(def.TTL.Duration), durationValue(raw.Defaults.TTL)},
			{"alert_ttl", "defaults.alert_ttl", "告警保留", "告警送不出去時在佇列中保留的時間。", shortDuration(def.AlertTTL.Duration), durationValue(raw.Defaults.AlertTTL)},
			{"poll_interval", "defaults.poll_interval", "輪詢間隔", "信箱伺服器不支援 IDLE 即時通知時，每隔多久檢查一次新信。", shortDuration(def.PollInterval.Duration), durationValue(raw.Defaults.PollInterval)},
			{"alert_after", "defaults.alert_after", "斷線告警門檻", "信箱連續斷線超過這段時間才發告警，避免短暫斷線也通知。", shortDuration(def.AlertAfter.Duration), durationValue(raw.Defaults.AlertAfter)},
		},
		Admin:     raw.Admin,
		LogLevel:  raw.LogLevel,
		LogLevels: logLevels,
		IP:        s.ipInfo(r),
		Version:   version,
		MinPwLen:  websec.MinPasswordLen,
	}
	if d.LogLevel == "" {
		d.LogLevel = "info"
	}
	for name, t := range raw.Targets {
		d.Targets = append(d.Targets, targetOption{Name: name, Bot: t.Bot, ChatID: t.ChatID})
	}
	sort.Slice(d.Targets, func(i, j int) bool { return d.Targets[i].Name < d.Targets[j].Name })
	return d
}

func (s *Server) ipInfo(r *http.Request) ipInfo {
	info := ipInfo{
		Client:    websec.ClientIP(r, s.o.TrustedProxies),
		Peer:      r.RemoteAddr,
		Forwarded: truncate(strings.Join(r.Header.Values("X-Forwarded-For"), ", "), 200),
		Trusted:   len(s.o.TrustedProxies),
	}
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		info.Peer = h
	}
	if a, err := netip.ParseAddr(info.Peer); err == nil {
		a = a.Unmap().WithZone("")
		info.Peer = a.String()
		for _, p := range s.o.TrustedProxies {
			if p.Contains(a) {
				info.PeerProxy = true
			}
		}
	}
	return info
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, http.StatusOK, nil, nil)
}

// renderSettings shows the settings page for the stored config; form (may be nil) replaces
// the general form's values with what was submitted.
func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, errs FieldErrors, form func(d *settingsData)) {
	raw, id, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := s.newSettingsData(r, raw, id)
	if form != nil {
		form(&d)
	}
	s.render(w, r, status, "settings", Page{Title: "系統設定", Nav: "settings", Errors: errs, Data: d})
}

func (s *Server) postSettingsGeneral(w http.ResponseWriter, r *http.Request) {
	vals := map[string]string{}
	durs := map[string]config.Duration{}
	perr := FieldErrors{}
	for _, f := range []struct{ name, path string }{
		{"max_age", "defaults.max_age"}, {"ttl", "defaults.ttl"}, {"alert_ttl", "defaults.alert_ttl"},
		{"poll_interval", "defaults.poll_interval"}, {"alert_after", "defaults.alert_after"},
	} {
		v := strings.TrimSpace(r.PostFormValue(f.name))
		vals[f.name] = v
		d, err := parseDuration(v)
		if err != nil {
			perr[f.path] = err.Error()
		}
		durs[f.name] = d
	}
	admin := strings.TrimSpace(r.PostFormValue("admin"))
	level := strings.TrimSpace(r.PostFormValue("log_level"))
	submitted := func(d *settingsData) {
		for i := range d.Durations {
			d.Durations[i].Value = vals[d.Durations[i].Name]
		}
		d.Admin, d.LogLevel = admin, level
	}
	apply := func(raw *config.Config) {
		raw.Defaults.MaxAge = durs["max_age"]
		raw.Defaults.TTL = durs["ttl"]
		raw.Defaults.AlertTTL = durs["alert_ttl"]
		raw.Defaults.PollInterval = durs["poll_interval"]
		raw.Defaults.AlertAfter = durs["alert_after"]
		raw.Admin = admin
		raw.LogLevel = level
	}
	fe := perr
	if len(perr) > 0 {
		// also report the other fields' problems now rather than after the next try
		if raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store); err == nil {
			apply(raw)
			if _, err := runtime.Build(raw); err != nil {
				for k, v := range fieldErrors(err) {
					if _, ok := fe[k]; !ok {
						fe[k] = v
					}
				}
			}
		}
	} else {
		var err error
		fe, err = s.mutate(r, "修改系統設定", func(raw *config.Config) error {
			apply(raw)
			return nil
		})
		if err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if len(fe) > 0 {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, fe.only(generalPaths...), submitted)
		return
	}
	s.redirect(w, r, "/settings", &Flash{Kind: "success", Text: "已儲存並套用"})
}

// checkCredentials verifies the admin's current password (when password is true) and TOTP
// code, consuming the code's time step. It reports false for any mismatch or a replayed
// code; the caller shows one message for all of them.
func (s *Server) checkCredentials(r *http.Request, password bool, current, code string) (bool, error) {
	admin, ok, err := s.o.Store.GetAdmin(r.Context())
	if err != nil || !ok {
		return false, err
	}
	if password {
		pwOK, err := websec.VerifyPassword(admin.PasswordHash, current)
		if err != nil || !pwOK {
			return false, err
		}
	}
	step, codeOK := websec.VerifyTOTP(admin.TOTPSecret, normalizeCode(code), s.now())
	if !codeOK {
		return false, nil
	}
	return s.o.Store.UseTOTPStep(r.Context(), step)
}

func (s *Server) postSettingsPassword(w http.ResponseWriter, r *http.Request) {
	ip := websec.ClientIP(r, s.o.TrustedProxies)
	next, confirm := r.PostFormValue("new"), r.PostFormValue("confirm")
	fail := func(status int, key, msg string) {
		s.renderSettings(w, r, status, FieldErrors{key: msg}, nil)
	}
	// the cheap checks first, so a typo does not use up the code
	if n := len([]rune(next)); n < websec.MinPasswordLen {
		fail(http.StatusUnprocessableEntity, "password.new", fmt.Sprintf("至少需要 %d 個字元（目前 %d 個）", websec.MinPasswordLen, n))
		return
	}
	if next != confirm {
		fail(http.StatusUnprocessableEntity, "password.confirm", "兩次輸入的新密碼不一致")
		return
	}
	if !s.limiter.Allow() {
		fail(http.StatusTooManyRequests, "password", "嘗試太頻繁，請稍後再試")
		return
	}
	ok, err := s.checkCredentials(r, true, r.PostFormValue("current"), r.PostFormValue("code"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		s.o.Log.Warn("修改密碼失敗：目前密碼或驗證碼不正確", "ip", ip)
		s.recordFailure(ip)
		fail(http.StatusUnprocessableEntity, "password", "目前密碼或驗證碼不正確（驗證碼每組只能用一次）")
		return
	}
	hash, err := websec.HashPassword(next)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ctx := r.Context()
	if err := s.o.Store.SetAdminPassword(ctx, hash); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.o.Store.DeleteOtherSessions(ctx, sessionFrom(ctx).TokenHash); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.o.Log.Info("Web 後台密碼已修改，其他 session 已登出", "ip", ip)
	s.o.Runtime.Notify(fmt.Sprintf("Web 後台密碼已修改（IP %s）。其他裝置的登入已全部登出。", ip))
	s.redirect(w, r, "/settings#security", &Flash{Kind: "success", Text: "密碼已修改，其他裝置的登入已全部登出"})
}

func (s *Server) postSettingsTOTP(w http.ResponseWriter, r *http.Request) {
	ip := websec.ClientIP(r, s.o.TrustedProxies)
	if !s.limiter.Allow() {
		s.renderSettings(w, r, http.StatusTooManyRequests, FieldErrors{"totp.code": "嘗試太頻繁，請稍後再試"}, nil)
		return
	}
	// the current authenticator must agree before a new secret is even created
	ok, err := s.checkCredentials(r, false, "", r.PostFormValue("code"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		s.o.Log.Warn("重新綁定 TOTP 失敗：驗證碼不正確", "ip", ip)
		s.recordFailure(ip)
		s.renderSettings(w, r, http.StatusUnprocessableEntity, FieldErrors{"totp.code": "驗證碼不正確或已用過，請輸入驗證器上目前的驗證碼"}, nil)
		return
	}
	if err := s.o.Store.SetTOTPPending(r.Context(), websec.NewTOTPSecret()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.o.Log.Info("開始重新綁定 TOTP", "ip", ip)
	http.Redirect(w, r, "/setup/totp", http.StatusSeeOther)
}

// versionRow is one line of the version history.
type versionRow struct {
	ID          int64
	At          time.Time
	Ago         string
	Source      string
	SourceLabel string
	IP          string
	Summary     string
	Current     bool
}

var sourceLabels = map[string]string{"web": "網頁", "import": "匯入", "restore": "還原", "bootstrap": "初始設定"}

type versionsData struct {
	Versions []versionRow
	Keep     int
}

func (s *Server) getVersions(w http.ResponseWriter, r *http.Request) {
	list, err := s.o.Store.ListConfigVersions(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	now := s.now()
	d := versionsData{Keep: store.KeepConfigVersions}
	for i, v := range list {
		label := sourceLabels[v.Source]
		if label == "" {
			label = v.Source
		}
		d.Versions = append(d.Versions, versionRow{
			ID: v.ID, At: v.CreatedAt, Ago: ago(now, v.CreatedAt), Source: v.Source, SourceLabel: label,
			IP: v.IP, Summary: v.Summary, Current: i == 0,
		})
	}
	s.render(w, r, http.StatusOK, "versions", Page{Title: "設定版本歷史", Nav: "settings", Data: d})
}

func (s *Server) postRestore(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.notFound(w, r)
		return
	}
	v, ok, err := s.o.Store.GetConfigVersion(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !ok {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf("找不到版本 #%d，可能已超過保留的 %d 個版本而被刪除。", id, store.KeepConfigVersions))
		return
	}
	c, err := config.Decode([]byte(v.YAML))
	if err != nil {
		s.o.Log.Error("設定版本無法解析", "version", id, "err", err)
		s.redirect(w, r, "/settings/versions", &Flash{Kind: "error", Text: fmt.Sprintf("版本 #%d 的內容無法解析，無法還原。", id)})
		return
	}
	res, fe, err := s.saveConfig(r, "restore", fmt.Sprintf("還原版本 #%d", id), func(raw *config.Config) error {
		*raw = *c
		return nil
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(fe) > 0 {
		s.redirect(w, r, "/settings/versions", &Flash{Kind: "error", Text: truncate(fmt.Sprintf("版本 #%d 無法通過目前的檢查，沒有還原：\n%s", id, fe.Error()), 400)})
		return
	}
	text := fmt.Sprintf("已還原版本 #%d 並套用", id)
	if len(res.Dropped) > 0 {
		text += "，並清除了信箱 " + strings.Join(res.Dropped, "、") + " 已不適用的 Microsoft 授權"
	}
	if len(res.Failed) > 0 {
		text += "，但信箱 " + strings.Join(res.Failed, "、") + " 的舊 Microsoft 授權沒有清除成功，詳情請看服務日誌"
		s.redirect(w, r, "/settings/versions", &Flash{Kind: "info", Text: text})
		return
	}
	s.redirect(w, r, "/settings/versions", &Flash{Kind: "success", Text: text})
}

// ago describes how long before now t was, e.g. "3 分鐘前".
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "剛剛"
	case d < time.Hour:
		return fmt.Sprintf("%d 分鐘前", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小時前", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d/(24*time.Hour)))
	}
	return t.Format("2006-01-02")
}
