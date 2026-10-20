package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// tokenRe is the shape of a token from @BotFather: "<bot id>:<secret>". Only such tokens
// are put into request URLs: they are the same escaped and unescaped, so the token can
// always be redacted from errors (which may quote the URL).
var tokenRe = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

// errTokenShape is the description of every call refused because of the token's shape.
const errTokenShape = "Bot token 格式不正確"

// ValidToken reports whether token has the shape of a Bot API token.
func ValidToken(token string) bool { return tokenRe.MatchString(token) }

// BotInfo is what getMe tells about a bot.
type BotInfo struct {
	ID        int64
	Username  string
	FirstName string
}

// Chat is a chat the bot has seen recently (see RecentChats).
type Chat struct {
	ID       int64
	Type     string // private | group | supergroup | channel
	Title    string // groups and channels
	Username string
	Name     string // private chats: first + last name
}

// APIError is a failed Bot API call (other than sendMessage, which returns *SendError).
// Its text never contains the token.
type APIError struct {
	Method      string
	Status      int    // HTTP status; 0 when no response was received
	Code        int    // Telegram's error_code; 0 when the response was not a Bot API error
	Description string // redacted
	msg         string // replaces the standard text when set
}

func (e *APIError) Error() string {
	switch {
	case e.msg != "":
		return e.msg
	case e.Status == 0:
		return fmt.Sprintf("Telegram %s 失敗：%s", e.Method, e.Description)
	}
	return fmt.Sprintf("Telegram %s 失敗：%s（HTTP %d）", e.Method, e.Description, e.Status)
}

// Rejected reports whether Telegram itself refused the token (401 Unauthorized, or 404 for
// a token that is not even well-formed), as opposed to a network or server problem.
func (e *APIError) Rejected() bool {
	return e.Code == http.StatusUnauthorized || e.Code == http.StatusNotFound
}

// GetMe validates token. Errors never contain the token.
func (c *Client) GetMe(ctx context.Context, token string) (BotInfo, error) {
	var r struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	}
	if err := c.call(ctx, token, "getMe", nil, &r); err != nil {
		return BotInfo{}, err
	}
	return BotInfo{ID: r.ID, Username: r.Username, FirstName: r.FirstName}, nil
}

type apiChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type chatHolder struct {
	Chat          *apiChat `json:"chat"`
	NewChatMember *struct {
		Status string `json:"status"`
	} `json:"new_chat_member"` // my_chat_member only
}

// RecentChats calls getUpdates (timeout 0) and returns the distinct chats seen in
// message, edited_message, channel_post and my_chat_member updates, newest first.
// A bot with a webhook gets a 409 from Telegram; the error says to remove the webhook.
// Chats whose latest my_chat_member update shows the bot "left" or "kicked" are left out.
//
// No offset is sent, so the updates stay unconfirmed, and no allowed_updates, which
// Telegram would remember for the bot. Without an offset Telegram answers with the OLDEST
// pending updates, at most 100, and keeps updates for 24 hours only: a bot that received
// more than 100 updates in a day may not show its newest chats.
func (c *Client) RecentChats(ctx context.Context, token string) ([]Chat, error) {
	var updates []struct {
		Message           *chatHolder `json:"message"`
		EditedMessage     *chatHolder `json:"edited_message"`
		ChannelPost       *chatHolder `json:"channel_post"`
		EditedChannelPost *chatHolder `json:"edited_channel_post"`
		MyChatMember      *chatHolder `json:"my_chat_member"`
	}
	err := c.call(ctx, token, "getUpdates", url.Values{"timeout": {"0"}}, &updates)
	if err != nil {
		if ae, ok := err.(*APIError); ok && ae.Status == http.StatusConflict {
			if strings.Contains(ae.Description, "other getUpdates") {
				ae.msg = "另一個程式正在以 getUpdates 接收此 Bot 的訊息，無法同時讀取最近對話；請稍後再試或停止該程式"
			} else {
				ae.msg = "此 Bot 設定了 webhook，無法讀取最近對話；請先移除 webhook"
			}
		}
		return nil, err
	}
	seen := map[int64]bool{}
	member := map[int64]string{} // the bot's status in the chat's latest my_chat_member update
	var out []Chat
	for i := len(updates) - 1; i >= 0; i-- {
		u := updates[i]
		if h := u.MyChatMember; h != nil && h.Chat != nil && h.NewChatMember != nil {
			if _, ok := member[h.Chat.ID]; !ok {
				member[h.Chat.ID] = h.NewChatMember.Status
			}
		}
		for _, h := range []*chatHolder{u.Message, u.EditedMessage, u.ChannelPost, u.EditedChannelPost, u.MyChatMember} {
			if h == nil || h.Chat == nil || h.Chat.ID == 0 || seen[h.Chat.ID] {
				continue
			}
			seen[h.Chat.ID] = true
			ch := h.Chat
			out = append(out, Chat{
				ID: ch.ID, Type: ch.Type, Title: ch.Title, Username: ch.Username,
				Name: strings.TrimSpace(ch.FirstName + " " + ch.LastName),
			})
		}
	}
	kept := out[:0]
	for _, c := range out {
		if st := member[c.ID]; st != "left" && st != "kicked" {
			kept = append(kept, c)
		}
	}
	return kept, nil
}

// call does GET BaseURL/bot<token>/<method>?params and decodes the "result" into result.
// Every error is an *APIError without the token; a token that is not ValidToken is
// refused before any request.
func (c *Client) call(ctx context.Context, token, method string, params url.Values, result any) error {
	if !ValidToken(token) {
		return &APIError{Method: method, Description: errTokenShape, msg: errTokenShape}
	}
	redact := func(s string) string {
		if token == "" {
			return s
		}
		s = strings.ReplaceAll(s, token, "<token>")
		return strings.ReplaceAll(s, url.PathEscape(token), "<token>")
	}
	fail := func(status, code int, desc string) error {
		return &APIError{Method: method, Status: status, Code: code, Description: redact(desc)}
	}
	u := strings.TrimRight(c.BaseURL, "/") + "/bot" + token + "/" + method
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fail(0, 0, "無法建立請求："+err.Error())
	}
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fail(0, 0, "無法連線："+err.Error())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fail(resp.StatusCode, 0, "讀取回應失敗："+err.Error())
	}
	var ar struct {
		OK          *bool           `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &ar); err != nil || ar.OK == nil {
		return fail(resp.StatusCode, 0, "回應不是 Bot API 的格式")
	}
	if !*ar.OK || resp.StatusCode != http.StatusOK {
		desc := ar.Description
		if desc == "" {
			desc = http.StatusText(resp.StatusCode)
		}
		return fail(resp.StatusCode, ar.ErrorCode, desc)
	}
	if err := json.Unmarshal(ar.Result, result); err != nil {
		return fail(resp.StatusCode, 0, "無法解讀回應內容："+err.Error())
	}
	return nil
}
