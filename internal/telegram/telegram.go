// Package telegram is a minimal Bot API client: sendMessage, getMe and getUpdates.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client // nil uses a shared client with a 30-second timeout
}

var defaultHTTP = &http.Client{Timeout: 30 * time.Second}

func New() *Client {
	return &Client{BaseURL: "https://api.telegram.org", HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type SendRequest struct {
	Token       string
	ChatID      int64
	Text        string
	HTML        bool
	ReplyMarkup string
}

type ErrorKind int

const (
	Transient ErrorKind = iota
	RateLimited
	BadRequest
	Forbidden
	// Unconfirmed: HTTP 200 but the response could not be read or understood. The message was
	// most likely delivered, so it must not be retried (a retry would send a duplicate).
	Unconfirmed
)

var kindNames = [...]string{"transient", "rate_limited", "bad_request", "forbidden", "unconfirmed"}

func (k ErrorKind) String() string {
	if k < 0 || int(k) >= len(kindNames) {
		return "unknown"
	}
	return kindNames[k]
}

type SendError struct {
	Kind        ErrorKind
	RetryAfter  time.Duration
	Description string
}

func (e *SendError) Error() string {
	return fmt.Sprintf("telegram %s: %s", e.Kind, e.Description)
}

type payload struct {
	ChatID             int64           `json:"chat_id"`
	Text               string          `json:"text"`
	ParseMode          string          `json:"parse_mode,omitempty"`
	LinkPreviewOptions map[string]bool `json:"link_preview_options"`
	ReplyMarkup        json.RawMessage `json:"reply_markup,omitempty"`
}

type apiResponse struct {
	OK          *bool  `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// Send sends one message. A token that is not ValidToken is refused before any request
// (Forbidden: retrying cannot help).
func (c *Client) Send(ctx context.Context, req SendRequest) error {
	if !ValidToken(req.Token) {
		return &SendError{Kind: Forbidden, Description: errTokenShape}
	}
	p := payload{ChatID: req.ChatID, Text: req.Text, LinkPreviewOptions: map[string]bool{"is_disabled": true}}
	if req.HTML {
		p.ParseMode = "HTML"
	}
	if req.ReplyMarkup != "" {
		p.ReplyMarkup = json.RawMessage(req.ReplyMarkup)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return &SendError{Kind: BadRequest, Description: err.Error()}
	}
	redact := func(s string) string { return strings.ReplaceAll(s, req.Token, "<token>") }
	url := strings.TrimRight(c.BaseURL, "/") + "/bot" + req.Token + "/sendMessage"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &SendError{Kind: Transient, Description: redact(err.Error())}
	}
	hreq.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return &SendError{Kind: Transient, Description: redact(transportError(err))}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var ar apiResponse
	parseErr := json.Unmarshal(raw, &ar)
	if resp.StatusCode == http.StatusOK {
		switch {
		case readErr != nil:
			return &SendError{Kind: Unconfirmed, Description: redact("HTTP 200, reading response: " + readErr.Error())}
		case parseErr != nil:
			return &SendError{Kind: Unconfirmed, Description: redact("HTTP 200, parsing response: " + parseErr.Error())}
		case ar.OK == nil:
			return &SendError{Kind: Unconfirmed, Description: `HTTP 200, response has no "ok" field`}
		case *ar.OK:
			return nil
		}
	}
	desc := ar.Description
	if desc == "" {
		desc = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	desc = redact(desc)
	code := ar.ErrorCode
	if code == 0 {
		code = resp.StatusCode
	}
	switch {
	case code == http.StatusTooManyRequests:
		retry := time.Duration(ar.Parameters.RetryAfter) * time.Second
		if retry <= 0 {
			retry = 5 * time.Second
		}
		return &SendError{Kind: RateLimited, RetryAfter: retry, Description: desc}
	case code == http.StatusBadRequest:
		return &SendError{Kind: BadRequest, Description: desc}
	case code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound:
		return &SendError{Kind: Forbidden, Description: desc}
	default:
		return &SendError{Kind: Transient, Description: desc}
	}
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// PlainText converts Telegram HTML to plain text (used when Telegram rejects our HTML).
func PlainText(htmlText string) string {
	return html.UnescapeString(tagRe.ReplaceAllString(htmlText, ""))
}

// PlainTextWithLinks is PlainText for the plain-text retry, which sends no buttons: each
// URL button of markup (an inline keyboard) not already in the text is appended as
// "label：URL" on its own line, so the link is not lost.
func PlainTextWithLinks(htmlText, markup string) string {
	text := PlainText(htmlText)
	var kb struct {
		InlineKeyboard [][]struct {
			Text string `json:"text"`
			URL  string `json:"url"`
		} `json:"inline_keyboard"`
	}
	if markup == "" || json.Unmarshal([]byte(markup), &kb) != nil {
		return text
	}
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			if b.URL != "" && !strings.Contains(text, b.URL) {
				text += "\n" + b.Text + "：" + b.URL
			}
		}
	}
	return text
}
