package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeAPI(t *testing.T, status int, body string, seen *string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client()}
}

func TestGetMeOK(t *testing.T) {
	var seen string
	c := fakeAPI(t, 200, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"my_bot","first_name":"My"}}`, &seen)
	info, err := c.GetMe(context.Background(), "123:SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if info != (BotInfo{ID: 1, Username: "my_bot", FirstName: "My"}) {
		t.Fatalf("info = %+v", info)
	}
	if seen != "GET /bot123:SECRET/getMe?" {
		t.Fatalf("request = %q", seen)
	}
}

func TestGetMeUnauthorizedHidesToken(t *testing.T) {
	c := fakeAPI(t, 401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, nil)
	_, err := c.GetMe(context.Background(), "123:SECRET")
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.Code != 401 || !ae.Rejected() {
		t.Fatalf("not a rejected APIError: %#v", err)
	}
	if want := "Telegram getMe 失敗：Unauthorized（HTTP 401）"; err.Error() != want {
		t.Fatalf("message = %q, want %q", err.Error(), want)
	}
}

func TestErrorsNeverContainToken(t *testing.T) {
	// a description echoing the token (Telegram does not, but a proxy might)
	c := fakeAPI(t, 400, `{"ok":false,"error_code":400,"description":"bad bot123:SECRET"}`, nil)
	if _, err := c.GetMe(context.Background(), "123:SECRET"); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
	// transport errors embed the request URL, token included
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c = &Client{BaseURL: srv.URL}
	for _, call := range []func() error{
		func() error { _, err := c.GetMe(context.Background(), "123:SECRET"); return err },
		func() error { _, err := c.RecentChats(context.Background(), "123:SECRET"); return err },
	} {
		err := call()
		if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "Telegram") {
			t.Fatalf("err = %v", err)
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Rejected() {
			t.Fatal("a transport error is not a rejection of the token")
		}
	}
	// a 200 that is not JSON
	c = fakeAPI(t, 200, `<html>`, nil)
	if _, err := c.GetMe(context.Background(), "123:SECRET"); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestRecentChats(t *testing.T) {
	var seen string
	c := fakeAPI(t, 200, `{"ok":true,"result":[
		{"update_id":1,"message":{"message_id":1,"chat":{"id":42,"type":"private","first_name":"Amy","last_name":"Lin","username":"amy"},"text":"hi"}},
		{"update_id":2,"my_chat_member":{"chat":{"id":-1001,"type":"supergroup","title":"Family"},"new_chat_member":{"status":"member"}}},
		{"update_id":3,"message":{"message_id":2,"chat":{"id":42,"type":"private","first_name":"Amy","last_name":"Lin","username":"amy"},"text":"again"}},
		{"update_id":4,"channel_post":{"message_id":3,"chat":{"id":-1002,"type":"channel","title":"Alerts","username":"alerts_ch"}}},
		{"update_id":5,"callback_query":{"id":"x"}}
	]}`, &seen)
	chats, err := c.RecentChats(context.Background(), "123:SECRET")
	if err != nil {
		t.Fatal(err)
	}
	want := []Chat{
		{ID: -1002, Type: "channel", Title: "Alerts", Username: "alerts_ch"},
		{ID: 42, Type: "private", Username: "amy", Name: "Amy Lin"},
		{ID: -1001, Type: "supergroup", Title: "Family"},
	}
	if len(chats) != len(want) {
		t.Fatalf("chats = %+v", chats)
	}
	for i := range want {
		if chats[i] != want[i] {
			t.Errorf("chats[%d] = %+v, want %+v", i, chats[i], want[i])
		}
	}
	// timeout 0 and nothing else: no offset (nothing is confirmed), no allowed_updates (Telegram
	// would remember it for the bot)
	if seen != "GET /bot123:SECRET/getUpdates?timeout=0" {
		t.Fatalf("request = %q", seen)
	}
}

func TestRecentChatsEditedMessage(t *testing.T) {
	c := fakeAPI(t, 200, `{"ok":true,"result":[{"update_id":1,"edited_message":{"chat":{"id":7,"type":"group","title":"G"}}}]}`, nil)
	chats, err := c.RecentChats(context.Background(), "1:x")
	if err != nil || len(chats) != 1 || chats[0].ID != 7 || chats[0].Title != "G" {
		t.Fatalf("chats = %+v, err = %v", chats, err)
	}
}

func TestRecentChatsWebhook(t *testing.T) {
	c := fakeAPI(t, 409, `{"ok":false,"error_code":409,"description":"Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first"}`, nil)
	_, err := c.RecentChats(context.Background(), "123:SECRET")
	if err == nil || err.Error() != "此 Bot 設定了 webhook，無法讀取最近對話；請先移除 webhook" {
		t.Fatalf("err = %v", err)
	}
	c = fakeAPI(t, 409, `{"ok":false,"error_code":409,"description":"Conflict: terminated by other getUpdates request; make sure that only one bot instance is running"}`, nil)
	_, err = c.RecentChats(context.Background(), "123:SECRET")
	if err == nil || strings.Contains(err.Error(), "webhook") || !strings.Contains(err.Error(), "getUpdates") {
		t.Fatalf("err = %v", err)
	}
}
