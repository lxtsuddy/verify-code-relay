package web

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
)

func TestTelegramPagesNeverShowTokens(t *testing.T) {
	e := newEnv(t, withSample) // bot token "1:SECRET-TOKEN"
	e.login()
	for _, p := range []string{"/telegram"} {
		if _, body := e.get(p); strings.Contains(body, "SECRET-TOKEN") {
			t.Errorf("%s leaks the bot token", p)
		}
	}
	// nor the JSON answers, even when Telegram's error would echo it
	e.tg.setGetMe(401, `{"ok":false,"error_code":401,"description":"Unauthorized for bot1:SECRET-TOKEN"}`)
	if _, body := e.postJSON("/api/telegram/getme", map[string]string{"bot": "main"}); strings.Contains(body, "SECRET-TOKEN") || !strings.Contains(body, `"ok":false`) {
		t.Errorf("getme leaks the token or succeeded: %s", body)
	}
	e.tg.setUpdates(400, `{"ok":false,"error_code":400,"description":"bad bot1:SECRET-TOKEN"}`)
	if _, body := e.postJSON("/api/telegram/chats", map[string]string{"bot": "main"}); strings.Contains(body, "SECRET-TOKEN") {
		t.Errorf("chats leaks the token: %s", body)
	}
	// a rejected new token is not echoed back into the form
	resp, body := e.postForm("/telegram/bots", url.Values{"name": {"other"}, "token": {"5:ANOTHER-SECRET"}})
	if resp.StatusCode != 422 || strings.Contains(body, "ANOTHER-SECRET") {
		t.Errorf("rejected token = %d, echoed: %v", resp.StatusCode, strings.Contains(body, "ANOTHER-SECRET"))
	}
}

func TestAddBotAndTarget(t *testing.T) {
	e := newEnv(t, nil) // base config already has bot main / target me
	e.login()
	if resp, body := e.postForm("/telegram/bots", url.Values{"name": {"alerts"}, "token": {"9:tok"}}); resp.StatusCode != 303 {
		t.Fatalf("add bot = %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.postForm("/telegram/bots", url.Values{"name": {"alerts"}, "token": {"9:tok"}}); resp.StatusCode != 422 {
		t.Fatalf("duplicate bot = %d", resp.StatusCode)
	}
	if resp, _ := e.postForm("/telegram/targets", url.Values{"name": {"ops"}, "bot": {"alerts"}, "chat_id": {"77"}}); resp.StatusCode != 303 {
		t.Fatalf("add target = %d", resp.StatusCode)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if raw.Targets["ops"].ChatID != 77 || raw.Bots["alerts"].Token != "9:tok" {
		t.Fatalf("raw = %+v", raw)
	}
	// blank token on edit keeps the old one
	e.postForm("/telegram/bots/alerts", url.Values{"token": {""}})
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if raw.Bots["alerts"].Token != "9:tok" {
		t.Fatal("blank token replaced the stored one")
	}
	resp, body := e.postForm("/telegram/bots/alerts/delete", nil)
	if resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "ops") {
		t.Fatalf("delete referenced bot = %d %s", resp.StatusCode, body)
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if _, ok := raw.Bots["alerts"]; !ok {
		t.Fatal("referenced bot was deleted")
	}
}

func TestSendTestToTarget(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	resp, _ := e.postForm("/telegram/targets/me/test", nil)
	if resp.StatusCode != 303 || !e.waitSent("測試訊息") {
		t.Fatalf("test send = %d", resp.StatusCode)
	}
	if !e.waitSent("✅ verify-code-relay 測試訊息（目標 me）") || !strings.Contains(e.flash(resp), "已送出") {
		t.Fatalf("text or flash wrong: %q", e.flash(resp))
	}
	var found bool
	for _, m := range e.tg.messages() {
		if strings.Contains(m.Text, "測試訊息") {
			found = m.ChatID == 42 && m.Token == "1:SECRET-TOKEN"
		}
	}
	if !found {
		t.Fatal("test message not sent with target me's bot and chat")
	}
	resp, _ = e.postForm("/telegram/targets/nope/test", nil)
	if resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "找不到目標 nope") {
		t.Fatalf("unknown target = %d %q", resp.StatusCode, e.flash(resp))
	}
}

func versionCount(t *testing.T, e *env) int {
	t.Helper()
	list, err := e.st.ListConfigVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

func TestAddBotValidation(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	for _, c := range []struct {
		name, token, want string
	}{
		{"", "9:tok", "請輸入名稱"},
		{"My Bot", "9:tok", "名稱只能包含小寫英文字母、數字、_ 與 -"},
		{"main", "9:tok", "名稱已存在"},
		{"alerts", "", "請輸入 Bot token"},
		{"alerts", "not a token", "格式不正確"},
	} {
		resp, body := e.postForm("/telegram/bots", url.Values{"name": {c.name}, "token": {c.token}})
		if resp.StatusCode != 422 || !strings.Contains(body, c.want) {
			t.Errorf("name %q token %q = %d, want %q", c.name, c.token, resp.StatusCode, c.want)
		}
		if c.name != "" && !strings.Contains(body, `value="`+c.name+`"`) {
			t.Errorf("name %q not kept in the form", c.name)
		}
	}
	// Telegram refuses the token: rejected, nothing stored
	e.tg.setGetMe(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	resp, body := e.postForm("/telegram/bots", url.Values{"name": {"alerts"}, "token": {"9:tok"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "Unauthorized") {
		t.Fatalf("rejected token = %d", resp.StatusCode)
	}
	if n := versionCount(t, e); n != 1 {
		t.Fatalf("versions = %d, nothing should be stored", n)
	}
	// Telegram unreachable or broken: stored anyway, with a warning
	e.tg.setGetMe(502, `bad gateway`)
	resp, _ = e.postForm("/telegram/bots", url.Values{"name": {"alerts"}, "token": {"9:tok"}})
	if resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "無法向 Telegram 驗證") {
		t.Fatalf("unverified add = %d %q", resp.StatusCode, e.flash(resp))
	}
	// success names the bot's username
	e.tg.setGetMe(200, `{"ok":true,"result":{"id":5,"username":"family_bot","first_name":"F"}}`)
	resp, _ = e.postForm("/telegram/bots", url.Values{"name": {"family"}, "token": {"6:abc"}})
	if resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "@family_bot") {
		t.Fatalf("add = %d %q", resp.StatusCode, e.flash(resp))
	}
}

func TestRotateBotToken(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	before := versionCount(t, e)
	for _, tok := range []string{"", "1:SECRET-TOKEN"} { // blank, and the same as stored
		resp, _ := e.postForm("/telegram/bots/main", url.Values{"token": {tok}})
		if resp.StatusCode != 303 || e.flash(resp) != "沒有變更" {
			t.Fatalf("token %q = %d %q", tok, resp.StatusCode, e.flash(resp))
		}
	}
	if versionCount(t, e) != before {
		t.Fatal("a no-op rotation stored a version")
	}
	resp, _ := e.postForm("/telegram/bots/main", url.Values{"token": {"1:NEW-TOKEN"}})
	if resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "@relay_test_bot") {
		t.Fatalf("rotate = %d %q", resp.StatusCode, e.flash(resp))
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if raw.Bots["main"].Token != "1:NEW-TOKEN" {
		t.Fatal("token not replaced")
	}
	e.tg.setGetMe(401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	resp, body := e.postForm("/telegram/bots/main", url.Values{"token": {"1:BAD-TOKEN"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "Unauthorized") || strings.Contains(body, "BAD-TOKEN") || !strings.Contains(body, `id="dlg-bot-main" aria-labelledby="dlg-bot-main-title" data-open>`) {
		t.Fatalf("rejected rotate = %d", resp.StatusCode)
	}
	if resp, _ := e.postForm("/telegram/bots/nope", url.Values{"token": {"1:x"}}); resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "找不到 Bot nope") {
		t.Fatalf("unknown bot = %d", resp.StatusCode)
	}
}

func TestDeleteBotAndTarget(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	// target me: used by mailbox gmail and as the admin target
	resp, _ := e.postForm("/telegram/targets/me/delete", nil)
	if f := e.flash(resp); resp.StatusCode != 303 || f != "目標 me 仍被信箱 gmail 及通知對象（系統設定）使用" {
		t.Fatalf("delete used target = %d %q", resp.StatusCode, f)
	}
	if _, page := e.get("/telegram"); !strings.Contains(page, "它仍被信箱 gmail 及系統通知使用") {
		t.Error("target delete confirm text")
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	raw.Admin = ""
	saveRaw(e, raw)
	resp, _ = e.postForm("/telegram/targets/me/delete", nil)
	if f := e.flash(resp); f != "目標 me 仍被信箱 gmail 使用" {
		t.Fatalf("delete target used by a mailbox = %q", f)
	}
	if _, page := e.get("/telegram"); !strings.Contains(page, "它仍被信箱 gmail 使用") {
		t.Error("target delete confirm text (mailbox only)")
	}
	resp, _ = e.postForm("/telegram/bots/main/delete", nil)
	if f := e.flash(resp); resp.StatusCode != 303 || f != "Bot main 仍被目標 me 使用" {
		t.Fatalf("delete used bot = %d %q", resp.StatusCode, f)
	}
	// unused ones go
	e.postForm("/telegram/bots", url.Values{"name": {"spare"}, "token": {"7:x"}})
	e.postForm("/telegram/targets", url.Values{"name": {"spare-chat"}, "bot": {"spare"}, "chat_id": {"-100123"}})
	if resp, _ := e.postForm("/telegram/targets/spare-chat/delete", nil); resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "已刪除") {
		t.Fatalf("delete target = %d %q", resp.StatusCode, e.flash(resp))
	}
	if resp, _ := e.postForm("/telegram/bots/spare/delete", nil); resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "已刪除") {
		t.Fatalf("delete bot = %d %q", resp.StatusCode, e.flash(resp))
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if _, ok := raw.Bots["spare"]; ok {
		t.Fatal("bot not deleted")
	}
	if _, ok := raw.Targets["spare-chat"]; ok {
		t.Fatal("target not deleted")
	}
	if resp, _ := e.postForm("/telegram/targets/spare-chat/delete", nil); resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "找不到") {
		t.Fatalf("delete missing target = %d", resp.StatusCode)
	}
}

func TestTargetValidationAndEdit(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	for _, c := range []struct {
		form url.Values
		want string
	}{
		{url.Values{"name": {"me"}, "bot": {"main"}, "chat_id": {"1"}}, "名稱已存在"},
		{url.Values{"name": {"Ops!"}, "bot": {"main"}, "chat_id": {"1"}}, "名稱只能包含"},
		{url.Values{"name": {"ops"}, "bot": {"nope"}, "chat_id": {"1"}}, "找不到 Bot"},
		{url.Values{"name": {"ops"}, "bot": {"main"}, "chat_id": {""}}, "請輸入 chat_id"},
		{url.Values{"name": {"ops"}, "bot": {"main"}, "chat_id": {"abc"}}, "必須是整數"},
		{url.Values{"name": {"ops"}, "bot": {"main"}, "chat_id": {"0"}}, "不可為 0"},
	} {
		resp, body := e.postForm("/telegram/targets", c.form)
		if resp.StatusCode != 422 || !strings.Contains(body, c.want) {
			t.Errorf("%v = %d, want %q", c.form, resp.StatusCode, c.want)
		}
	}
	// negative ids (groups, channels) and surrounding spaces are fine
	if resp, _ := e.postForm("/telegram/targets", url.Values{"name": {"ops"}, "bot": {"main"}, "chat_id": {" -1001234567890 "}}); resp.StatusCode != 303 {
		t.Fatalf("negative chat id = %d", resp.StatusCode)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if raw.Targets["ops"].ChatID != -1001234567890 {
		t.Fatalf("chat id = %d", raw.Targets["ops"].ChatID)
	}

	before := versionCount(t, e)
	resp, _ := e.postForm("/telegram/targets/ops", url.Values{"bot": {"main"}, "chat_id": {"-1001234567890"}})
	if resp.StatusCode != 303 || e.flash(resp) != "沒有變更" || versionCount(t, e) != before {
		t.Fatalf("no-op edit = %d %q", resp.StatusCode, e.flash(resp))
	}
	resp, body := e.postForm("/telegram/targets/ops", url.Values{"bot": {"main"}, "chat_id": {"x1"}})
	if resp.StatusCode != 422 || !strings.Contains(body, "必須是整數") || !strings.Contains(body, `value="x1"`) {
		t.Fatalf("bad edit = %d", resp.StatusCode)
	}
	resp, _ = e.postForm("/telegram/targets/ops", url.Values{"bot": {"main"}, "chat_id": {"55"}})
	if resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "已儲存") {
		t.Fatalf("edit = %d %q", resp.StatusCode, e.flash(resp))
	}
	raw, _, _ = runtime.LoadConfig(context.Background(), e.st)
	if raw.Targets["ops"].ChatID != 55 {
		t.Fatal("edit not stored")
	}
	if resp, _ := e.postForm("/telegram/targets/nope", url.Values{"bot": {"main"}, "chat_id": {"5"}}); resp.StatusCode != 303 || !strings.Contains(e.flash(resp), "找不到目標 nope") {
		t.Fatalf("edit unknown target = %d", resp.StatusCode)
	}
}

func TestTelegramPageLists(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		withSample(c)
		c.Bots["family"] = config.Bot{Token: "2:x"}
		c.Targets["kids"] = config.Target{Bot: "family", ChatID: -100222333}
	})
	e.login()
	resp, body := e.get("/telegram")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /telegram = %d", resp.StatusCode)
	}
	text := regexpStripTags(body)
	for _, want := range []string{"main", "family", "kids", "-100222333", "gmail", "通知對象", `data-getme-bot="main"`, "讀取最近對話", "先對 Bot 傳一則訊息，或把 Bot 加入群組後再讀取"} {
		if !strings.Contains(body, want) && !strings.Contains(text, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// empty state
	e2 := newEnv(t, func(c *config.Config) {
		delete(c.Bots, "main")
		delete(c.Targets, "me")
		c.Admin = ""
	})
	e2.login()
	_, body = e2.get("/telegram")
	if !strings.Contains(body, "還沒有 Bot") {
		t.Error("no empty state for bots")
	}
}

func TestTelegramAPI(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	decode := func(body string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatalf("not JSON: %s", body)
		}
		return m
	}
	_, body := e.postJSON("/api/telegram/getme", map[string]string{"bot": "main"})
	if m := decode(body); m["ok"] != true || m["username"] != "relay_test_bot" {
		t.Fatalf("getme bot = %s", body)
	}
	_, body = e.postJSON("/api/telegram/getme", map[string]string{"token": "3:fresh"})
	if m := decode(body); m["ok"] != true || m["username"] != "relay_test_bot" {
		t.Fatalf("getme token = %s", body)
	}
	_, body = e.postJSON("/api/telegram/getme", map[string]string{"token": "no token"})
	if m := decode(body); m["ok"] != false || !strings.Contains(m["error"].(string), "格式不正確") {
		t.Fatalf("getme bad token = %s", body)
	}
	resp, body := e.postJSON("/api/telegram/getme", map[string]string{"bot": "nope"})
	if m := decode(body); resp.StatusCode != 404 || m["ok"] != false {
		t.Fatalf("getme unknown bot = %d %s", resp.StatusCode, body)
	}

	e.tg.setUpdates(200, `{"ok":true,"result":[
		{"update_id":1,"message":{"chat":{"id":42,"type":"private","first_name":"Amy","username":"amy"}}},
		{"update_id":2,"my_chat_member":{"chat":{"id":-1001,"type":"supergroup","title":"Family"}}}]}`)
	_, body = e.postJSON("/api/telegram/chats", map[string]string{"bot": "main"})
	var got struct {
		OK    bool `json:"ok"`
		Chats []struct {
			ID       int64  `json:"id"`
			Type     string `json:"type"`
			Title    string `json:"title"`
			Username string `json:"username"`
		} `json:"chats"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || !got.OK || len(got.Chats) != 2 {
		t.Fatalf("chats = %s", body)
	}
	if c := got.Chats[0]; c.ID != -1001 || c.Type != "supergroup" || c.Title != "Family" {
		t.Errorf("first chat = %+v", c)
	}
	if c := got.Chats[1]; c.ID != 42 || c.Title != "Amy" || c.Username != "amy" {
		t.Errorf("private chat = %+v", c)
	}
	e.tg.setUpdates(409, `{"ok":false,"error_code":409,"description":"Conflict: can't use getUpdates method while webhook is active"}`)
	_, body = e.postJSON("/api/telegram/chats", map[string]string{"bot": "main"})
	if m := decode(body); m["ok"] != false || !strings.Contains(m["error"].(string), "webhook") {
		t.Fatalf("chats webhook = %s", body)
	}
	resp, _ = e.postJSON("/api/telegram/chats", map[string]string{"bot": "nope"})
	if resp.StatusCode != 404 {
		t.Fatalf("chats unknown bot = %d", resp.StatusCode)
	}
}
