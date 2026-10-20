package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/telegram"
)

// Telegram page: bots (write-only tokens) and targets (bot + chat_id). Names are the
// config keys and cannot change after creation.
//
// Form error keys (FieldErrors): the add-bot form uses "bot.name", "bot.token" and "bot"
// (form level); the add-target form "target.name", "target.bot", "target.chat_id" and
// "target"; the rotate form of bot N "rotate.N.token" and "rotate.N"; the edit form of
// target N "edit.N.bot", "edit.N.chat_id" and "edit.N". Errors[""] stays unused so the
// layout never shows a form's problem behind its dialog.

// telegramTimeout bounds every Bot API call made for the page.
const telegramTimeout = 15 * time.Second

// nameRe repeats config's name rule, so the name field gets the message instead of the top.
var nameRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

const (
	msgNameRule   = "名稱只能包含小寫英文字母、數字、_ 與 -"
	msgNameTaken  = "名稱已存在"
	msgNoChange   = "沒有變更"
	msgTokenShape = "格式不正確：應為 @BotFather 提供的「數字:英數字」，例如 123456789:AAH4x…"
)

var errNoChange = errors.New(msgNoChange)

type botRow struct {
	Name    string
	Targets []string // targets using the bot
}

type targetRow struct {
	Name      string
	Bot       string
	ChatID    int64
	Mailboxes []string // mailboxes routing to the target
	Admin     bool     // the notification target (settings)
	// edit form values: the stored ones, or what was submitted when sent back with errors
	FormBot, FormChatID string
}

type targetForm struct{ Name, Bot, ChatID string }

type telegramData struct {
	Bots     []botRow
	Targets  []targetRow
	BotNames []string
	NewBot   struct {
		Name         string
		TokenDropped bool // a token was submitted: it is never sent back, so say so
	}
	NewTarget targetForm
	Open      string // id of the dialog to open on load (a form sent back with errors)
}

func (s *Server) getTelegram(w http.ResponseWriter, r *http.Request) {
	s.renderTelegram(w, r, http.StatusOK, nil, nil)
}

// renderTelegram shows the page for the stored config; form (may be nil) puts submitted
// values back and names the dialog to open.
func (s *Server) renderTelegram(w http.ResponseWriter, r *http.Request, status int, errs FieldErrors, form func(d *telegramData)) {
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := newTelegramData(raw)
	if form != nil {
		form(&d)
	}
	s.render(w, r, status, "telegram", Page{Title: "Telegram", Nav: "telegram", Errors: errs, Data: d})
}

func newTelegramData(raw *config.Config) telegramData {
	var d telegramData
	for _, name := range sortedNames(raw.Bots) {
		row := botRow{Name: name}
		for _, t := range sortedNames(raw.Targets) {
			if raw.Targets[t].Bot == name {
				row.Targets = append(row.Targets, t)
			}
		}
		d.Bots = append(d.Bots, row)
		d.BotNames = append(d.BotNames, name)
	}
	for _, name := range sortedNames(raw.Targets) {
		t := raw.Targets[name]
		d.Targets = append(d.Targets, targetRow{
			Name: name, Bot: t.Bot, ChatID: t.ChatID, Mailboxes: mailboxesUsing(raw, name), Admin: raw.Admin == name,
			FormBot: t.Bot, FormChatID: strconv.FormatInt(t.ChatID, 10),
		})
	}
	if len(d.BotNames) > 0 {
		d.NewTarget.Bot = d.BotNames[0]
	}
	return d
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mailboxesUsing(raw *config.Config, target string) []string {
	var out []string
	for _, name := range sortedNames(raw.Mailboxes) {
		if m := raw.Mailboxes[name]; m != nil {
			for _, t := range m.Targets {
				if t == target {
					out = append(out, name)
					break
				}
			}
		}
	}
	return out
}

// remap moves Build's problems at the given config paths to form keys; all others go,
// as "path: message", under the form-level key.
func remap(fe FieldErrors, formKey string, paths map[string]string) FieldErrors {
	out := FieldErrors{}
	rest := FieldErrors{}
	for k, v := range fe {
		if to, ok := paths[k]; ok {
			out.add(to, v)
		} else {
			rest[k] = v
		}
	}
	if len(rest) > 0 {
		out.add(formKey, rest.Error())
	}
	return out
}

func checkName(name string, taken bool) string {
	switch {
	case name == "":
		return "請輸入名稱"
	case !nameRe.MatchString(name):
		return msgNameRule
	case taken:
		return msgNameTaken
	}
	return ""
}

// checkToken describes what is wrong with a token typed into a form ("" if nothing).
func checkToken(token string) string {
	switch {
	case token == "":
		return "請輸入 Bot token"
	case !telegram.ValidToken(token):
		return msgTokenShape
	}
	return ""
}

// verifyToken asks Telegram about token. rejected is set when Telegram refused the token;
// any other failure only leaves warn set (the token may still be fine).
func (s *Server) verifyToken(ctx context.Context, token string) (info telegram.BotInfo, rejected, warn string) {
	ctx, cancel := context.WithTimeout(ctx, telegramTimeout)
	defer cancel()
	info, err := s.o.Telegram.GetMe(ctx, token)
	if err == nil {
		return info, "", ""
	}
	var ae *telegram.APIError
	if errors.As(err, &ae) && ae.Rejected() {
		return info, fmt.Sprintf("Telegram 不接受這個 token（%s）：請確認是 @BotFather 提供的完整 token", ae.Description), ""
	}
	return info, "", err.Error()
}

// savedText is the flash after a bot's token was stored: what is e.g. "已新增 Bot x".
func savedText(what string, info telegram.BotInfo, warn string) *Flash {
	switch {
	case warn != "":
		return &Flash{Kind: "info", Text: what + " 並套用，但目前無法向 Telegram 驗證 token：" + warn}
	case info.Username != "":
		return &Flash{Kind: "success", Text: fmt.Sprintf("%s（@%s）並套用", what, info.Username)}
	}
	return &Flash{Kind: "success", Text: what + " 並套用"}
}

func (s *Server) postBotAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	token := strings.TrimSpace(r.PostFormValue("token"))
	back := func(status int, fe FieldErrors) {
		s.renderTelegram(w, r, status, fe, func(d *telegramData) {
			d.NewBot.Name, d.NewBot.TokenDropped = name, token != ""
			d.Open = "dlg-bot-add"
		})
	}
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	_, taken := raw.Bots[name]
	fe := FieldErrors{}
	if msg := checkName(name, taken); msg != "" {
		fe["bot.name"] = msg
	}
	if msg := checkToken(token); msg != "" {
		fe["bot.token"] = msg
	}
	if len(fe) > 0 {
		back(http.StatusUnprocessableEntity, fe)
		return
	}
	info, rejected, warn := s.verifyToken(r.Context(), token)
	if rejected != "" {
		back(http.StatusUnprocessableEntity, FieldErrors{"bot.token": rejected})
		return
	}
	fe, err = s.mutate(r, "新增 Bot "+name, func(raw *config.Config) error {
		if _, ok := raw.Bots[name]; ok {
			return FieldErrors{"bot.name": msgNameTaken}
		}
		raw.Bots[name] = config.Bot{Token: token}
		return nil
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(fe) > 0 {
		back(http.StatusUnprocessableEntity, remap(fe, "bot", map[string]string{
			"bot.name": "bot.name", "bots." + name: "bot.name", "bots." + name + ".token": "bot.token",
		}))
		return
	}
	s.redirect(w, r, "/telegram", savedText("已新增 Bot "+name, info, warn))
}

func (s *Server) postBotRotate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	token := strings.TrimSpace(r.PostFormValue("token"))
	key := "rotate." + name
	back := func(status int, fe FieldErrors) {
		s.renderTelegram(w, r, status, fe, func(d *telegramData) { d.Open = "dlg-bot-" + name })
	}
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	old, ok := raw.Bots[name]
	if !ok {
		s.redirect(w, r, "/telegram", &Flash{Kind: "error", Text: "找不到 Bot " + name + "，可能已被刪除"})
		return
	}
	if token == "" || token == old.Token {
		s.redirect(w, r, "/telegram", &Flash{Kind: "info", Text: msgNoChange})
		return
	}
	if msg := checkToken(token); msg != "" {
		back(http.StatusUnprocessableEntity, FieldErrors{key + ".token": msg})
		return
	}
	info, rejected, warn := s.verifyToken(r.Context(), token)
	if rejected != "" {
		back(http.StatusUnprocessableEntity, FieldErrors{key + ".token": rejected})
		return
	}
	fe, err := s.mutate(r, "更換 Bot "+name+" 的 token", func(raw *config.Config) error {
		b, ok := raw.Bots[name]
		if !ok {
			return FieldErrors{key: "找不到 Bot " + name + "，可能已被刪除"}
		}
		if b.Token == token {
			return errNoChange
		}
		raw.Bots[name] = config.Bot{Token: token}
		return nil
	})
	switch {
	case errors.Is(err, errNoChange):
		s.redirect(w, r, "/telegram", &Flash{Kind: "info", Text: msgNoChange})
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	case len(fe) > 0:
		back(http.StatusUnprocessableEntity, remap(fe, key, map[string]string{key: key, "bots." + name + ".token": key + ".token"}))
		return
	}
	s.redirect(w, r, "/telegram", savedText("已更換 Bot "+name+" 的 token", info, warn))
}

// joinNames lists names as "a、b".
func joinNames(names []string) string { return strings.Join(names, "、") }

func (s *Server) postBotDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	fe, err := s.mutate(r, "刪除 Bot "+name, func(raw *config.Config) error {
		if _, ok := raw.Bots[name]; !ok {
			return FieldErrors{"": "找不到 Bot " + name + "，可能已被刪除"}
		}
		var users []string
		for _, t := range sortedNames(raw.Targets) {
			if raw.Targets[t].Bot == name {
				users = append(users, t)
			}
		}
		if len(users) > 0 {
			return FieldErrors{"": fmt.Sprintf("Bot %s 仍被目標 %s 使用", name, joinNames(users))}
		}
		delete(raw.Bots, name)
		return nil
	})
	s.afterDelete(w, r, fe, err, "已刪除 Bot "+name)
}

func (s *Server) afterDelete(w http.ResponseWriter, r *http.Request, fe FieldErrors, err error, done string) {
	switch {
	case err != nil:
		s.serverError(w, r, err)
	case len(fe) > 0:
		s.redirect(w, r, "/telegram", &Flash{Kind: "error", Text: truncate(fe.Error(), 400)})
	default:
		s.redirect(w, r, "/telegram", &Flash{Kind: "success", Text: done})
	}
}

// parseChatID reads the chat_id field; msg describes the problem ("" if none).
func parseChatID(v string) (id int64, msg string) {
	if v == "" {
		return 0, "請輸入 chat_id"
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, "chat_id 必須是整數（群組與頻道是負數，例如 -1001234567890）"
	}
	if id == 0 {
		return 0, "不可為 0"
	}
	return id, ""
}

func (s *Server) postTargetAdd(w http.ResponseWriter, r *http.Request) {
	f := targetForm{
		Name:   strings.TrimSpace(r.PostFormValue("name")),
		Bot:    strings.TrimSpace(r.PostFormValue("bot")),
		ChatID: strings.TrimSpace(r.PostFormValue("chat_id")),
	}
	back := func(status int, fe FieldErrors) {
		s.renderTelegram(w, r, status, fe, func(d *telegramData) {
			d.NewTarget = f
			d.Open = "dlg-target-add"
		})
	}
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	_, taken := raw.Targets[f.Name]
	fe := FieldErrors{}
	if msg := checkName(f.Name, taken); msg != "" {
		fe["target.name"] = msg
	}
	if _, ok := raw.Bots[f.Bot]; !ok {
		fe["target.bot"] = fmt.Sprintf("找不到 Bot %q", f.Bot)
	}
	id, msg := parseChatID(f.ChatID)
	if msg != "" {
		fe["target.chat_id"] = msg
	}
	if len(fe) > 0 {
		back(http.StatusUnprocessableEntity, fe)
		return
	}
	fe, err = s.mutate(r, "新增目標 "+f.Name, func(raw *config.Config) error {
		if _, ok := raw.Targets[f.Name]; ok {
			return FieldErrors{"target.name": msgNameTaken}
		}
		raw.Targets[f.Name] = config.Target{Bot: f.Bot, ChatID: id}
		return nil
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(fe) > 0 {
		p := "targets." + f.Name
		back(http.StatusUnprocessableEntity, remap(fe, "target", map[string]string{
			"target.name": "target.name", p: "target.name", p + ".bot": "target.bot", p + ".chat_id": "target.chat_id",
		}))
		return
	}
	s.redirect(w, r, "/telegram", &Flash{Kind: "success", Text: "已新增目標 " + f.Name + " 並套用。可以按「發送測試」確認收得到。"})
}

func (s *Server) postTargetEdit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	bot := strings.TrimSpace(r.PostFormValue("bot"))
	chat := strings.TrimSpace(r.PostFormValue("chat_id"))
	key := "edit." + name
	back := func(status int, fe FieldErrors) {
		s.renderTelegram(w, r, status, fe, func(d *telegramData) {
			for i := range d.Targets {
				if d.Targets[i].Name == name {
					d.Targets[i].FormBot, d.Targets[i].FormChatID = bot, chat
				}
			}
			d.Open = "dlg-target-" + name
		})
	}
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if _, ok := raw.Targets[name]; !ok {
		s.redirect(w, r, "/telegram", &Flash{Kind: "error", Text: "找不到目標 " + name + "，可能已被刪除"})
		return
	}
	fe := FieldErrors{}
	if _, ok := raw.Bots[bot]; !ok {
		fe[key+".bot"] = fmt.Sprintf("找不到 Bot %q", bot)
	}
	id, msg := parseChatID(chat)
	if msg != "" {
		fe[key+".chat_id"] = msg
	}
	if len(fe) > 0 {
		back(http.StatusUnprocessableEntity, fe)
		return
	}
	fe, err = s.mutate(r, "修改目標 "+name, func(raw *config.Config) error {
		t, ok := raw.Targets[name]
		if !ok {
			return FieldErrors{key: "找不到目標 " + name + "，可能已被刪除"}
		}
		next := config.Target{Bot: bot, ChatID: id}
		if t == next {
			return errNoChange
		}
		raw.Targets[name] = next
		return nil
	})
	switch {
	case errors.Is(err, errNoChange):
		s.redirect(w, r, "/telegram", &Flash{Kind: "info", Text: msgNoChange})
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	case len(fe) > 0:
		p := "targets." + name
		back(http.StatusUnprocessableEntity, remap(fe, key, map[string]string{
			key: key, p + ".bot": key + ".bot", p + ".chat_id": key + ".chat_id",
		}))
		return
	}
	s.redirect(w, r, "/telegram", &Flash{Kind: "success", Text: "已儲存目標 " + name + " 並套用"})
}

func (s *Server) postTargetDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	fe, err := s.mutate(r, "刪除目標 "+name, func(raw *config.Config) error {
		if _, ok := raw.Targets[name]; !ok {
			return FieldErrors{"": "找不到目標 " + name + "，可能已被刪除"}
		}
		var users []string
		if m := mailboxesUsing(raw, name); len(m) > 0 {
			users = append(users, "信箱 "+joinNames(m))
		}
		if raw.Admin == name {
			users = append(users, "通知對象（系統設定）")
		}
		if len(users) > 0 {
			return FieldErrors{"": fmt.Sprintf("目標 %s 仍被%s使用", name, strings.Join(users, "及"))}
		}
		delete(raw.Targets, name)
		return nil
	})
	s.afterDelete(w, r, fe, err, "已刪除目標 "+name)
}

// testMessage is sent by the 「發送測試」 button.
func testMessage(target string) string {
	return "✅ verify-code-relay 測試訊息（目標 " + target + "）"
}

func (s *Server) postTargetTest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	t, ok := raw.Targets[name]
	if !ok {
		s.redirect(w, r, "/telegram", &Flash{Kind: "error", Text: "找不到目標 " + name + "，可能已被刪除"})
		return
	}
	bot, ok := raw.Bots[t.Bot]
	if !ok {
		s.redirect(w, r, "/telegram", &Flash{Kind: "error", Text: fmt.Sprintf("目標 %s 使用的 Bot %s 不存在", name, t.Bot)})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), telegramTimeout)
	defer cancel()
	err = s.o.Telegram.Send(ctx, telegram.SendRequest{Token: bot.Token, ChatID: t.ChatID, Text: testMessage(name)})
	if err != nil {
		s.o.Log.Warn("測試訊息發送失敗", "target", name, "err", err)
		s.redirect(w, r, "/telegram", &Flash{Kind: "error", Text: truncate(sendFailure(name, err), 400)})
		return
	}
	s.o.Log.Info("已發送測試訊息", "target", name)
	s.redirect(w, r, "/telegram", &Flash{Kind: "success", Text: fmt.Sprintf("已送出測試訊息到目標 %s（chat %d），請到 Telegram 確認有收到", name, t.ChatID)})
}

// sendFailure explains a failed test send with a hint for the usual causes.
func sendFailure(target string, err error) string {
	msg := fmt.Sprintf("目標 %s 的測試訊息送不出去：", target)
	var se *telegram.SendError
	if !errors.As(err, &se) {
		return msg + err.Error()
	}
	msg += se.Description
	switch se.Kind {
	case telegram.Forbidden:
		msg += "。Bot token 可能失效，或對方封鎖了 Bot、尚未對 Bot 傳過訊息、Bot 不在該群組中。"
	case telegram.BadRequest:
		msg += "。chat_id 可能不正確：可用「讀取最近對話」重新選擇。"
	case telegram.RateLimited:
		msg += "。Telegram 要求稍後再送，請過一會兒再試。"
	case telegram.Unconfirmed:
		msg = fmt.Sprintf("目標 %s 的測試訊息可能已送出，但無法確認：%s", target, se.Description)
	}
	return msg
}

// readJSON decodes the request body into v; on failure it answers 400 and returns false.
func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		s.fail(w, r, http.StatusBadRequest, "請求內容不是有效的 JSON")
		return false
	}
	return true
}

// storedToken returns the token of bot name, answering 404 when there is none.
func (s *Server) storedToken(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return "", false
	}
	b, ok := raw.Bots[name]
	if !ok {
		s.fail(w, r, http.StatusNotFound, "找不到 Bot "+name+"，可能已被刪除；請重新整理頁面")
		return "", false
	}
	return b.Token, true
}

// postAPIGetMe answers {"ok":true,"username","first_name"} or {"ok":false,"error"} for a
// stored bot ({"bot":name}) or a token typed into a form ({"token":…}).
func (s *Server) postAPIGetMe(w http.ResponseWriter, r *http.Request) {
	var in struct{ Bot, Token string }
	if !s.readJSON(w, r, &in) {
		return
	}
	token := strings.TrimSpace(in.Token)
	if in.Bot != "" {
		var ok bool
		if token, ok = s.storedToken(w, r, in.Bot); !ok {
			return
		}
	} else if msg := checkToken(token); msg != "" {
		s.json(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
	info, rejected, warn := s.verifyToken(r.Context(), token)
	switch {
	case rejected != "":
		s.json(w, http.StatusOK, map[string]any{"ok": false, "error": rejected})
	case warn != "":
		s.json(w, http.StatusOK, map[string]any{"ok": false, "error": "無法向 Telegram 驗證：" + warn})
	default:
		s.json(w, http.StatusOK, map[string]any{"ok": true, "username": info.Username, "first_name": info.FirstName})
	}
}

type chatJSON struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username,omitempty"`
}

// postAPIChats lists the chats bot {"bot":name} saw recently (getUpdates).
func (s *Server) postAPIChats(w http.ResponseWriter, r *http.Request) {
	var in struct{ Bot string }
	if !s.readJSON(w, r, &in) {
		return
	}
	token, ok := s.storedToken(w, r, in.Bot)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), telegramTimeout)
	defer cancel()
	chats, err := s.o.Telegram.RecentChats(ctx, token)
	if err != nil {
		s.json(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]chatJSON, 0, len(chats))
	for _, c := range chats {
		title := c.Title
		if title == "" {
			title = c.Name
		}
		out = append(out, chatJSON{ID: c.ID, Type: c.Type, Title: title, Username: c.Username})
	}
	s.json(w, http.StatusOK, map[string]any{"ok": true, "chats": out})
}
