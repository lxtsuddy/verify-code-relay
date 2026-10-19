package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendOKRequestShape(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	err := c.Send(context.Background(), SendRequest{Token: "123:SECRET", ChatID: -100, Text: "<b>x</b>", HTML: true,
		ReplyMarkup: `{"inline_keyboard":[[{"text":"t","url":"https://e.com"}]]}`})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:SECRET/sendMessage" {
		t.Errorf("path = %s", path)
	}
	if got["chat_id"].(float64) != -100 || got["parse_mode"] != "HTML" || got["text"] != "<b>x</b>" {
		t.Errorf("body = %v", got)
	}
	if got["link_preview_options"].(map[string]any)["is_disabled"] != true {
		t.Errorf("preview not disabled: %v", got)
	}
	if _, ok := got["reply_markup"].(map[string]any)["inline_keyboard"]; !ok {
		t.Errorf("markup not embedded as JSON object: %v", got["reply_markup"])
	}
}

func TestSendPlainOmitsParseModeAndMarkup(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	if err := c.Send(context.Background(), SendRequest{Token: "t", ChatID: 1, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["parse_mode"]; ok {
		t.Errorf("parse_mode present: %v", got)
	}
	if _, ok := got["reply_markup"]; ok {
		t.Errorf("reply_markup present: %v", got)
	}
}

func TestSendErrorKinds(t *testing.T) {
	cases := []struct {
		status int
		body   string
		kind   ErrorKind
		retry  time.Duration
	}{
		{429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`, RateLimited, 7 * time.Second},
		{400, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`, BadRequest, 0},
		{403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, Forbidden, 0},
		{401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, Forbidden, 0},
		{404, `{"ok":false,"error_code":404,"description":"Not Found"}`, Forbidden, 0},
		{502, `bad gateway`, Transient, 0},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
		err := c.Send(context.Background(), SendRequest{Token: "123:SECRET", ChatID: 1, Text: "x"})
		srv.Close()
		var se *SendError
		if !errors.As(err, &se) || se.Kind != tc.kind || se.RetryAfter != tc.retry {
			t.Errorf("status %d: got %#v", tc.status, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("token leaked: %v", err)
		}
	}
}

func TestSendNetworkErrorRedactsToken(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Second}}
	err := c.Send(context.Background(), SendRequest{Token: "123:SECRET", ChatID: 1, Text: "x"})
	var se *SendError
	if !errors.As(err, &se) || se.Kind != Transient {
		t.Fatalf("got %#v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("token leaked: %v", err)
	}
}

func TestPlainText(t *testing.T) {
	got := PlainText("🔐 <b>GitHub</b>\n<code>1 &lt; 2 &amp;&amp; 3</code>")
	if got != "🔐 GitHub\n1 < 2 && 3" {
		t.Errorf("got %q", got)
	}
}
