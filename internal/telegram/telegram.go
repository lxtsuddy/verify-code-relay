// Package telegram is a minimal Bot API client: sendMessage only.
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
	HTTP    *http.Client
}

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
)

func (k ErrorKind) String() string {
	return [...]string{"transient", "rate_limited", "bad_request", "forbidden"}[k]
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
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) Send(ctx context.Context, req SendRequest) error {
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
	resp, err := c.HTTP.Do(hreq)
	if err != nil {
		return &SendError{Kind: Transient, Description: redact(err.Error())}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var ar apiResponse
	_ = json.Unmarshal(raw, &ar)
	if resp.StatusCode == http.StatusOK && ar.OK {
		return nil
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
