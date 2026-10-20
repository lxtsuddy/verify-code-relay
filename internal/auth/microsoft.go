package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/store"
)

const Scope = "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"

const refreshMargin = 5 * time.Minute

// minPollInterval is the shortest wait between device-code polls, so that a
// missing or zero interval in the response does not turn into a busy loop.
const minPollInterval = 5 * time.Second

type TokenStore interface {
	GetOAuthToken(ctx context.Context, mailbox string) (store.OAuthToken, bool, error)
	SaveOAuthToken(ctx context.Context, mailbox string, t store.OAuthToken) error
}

type Microsoft struct {
	Mailbox   string
	Username  string
	ClientID  string
	Tenant    string
	Store     TokenStore
	HTTP      *http.Client
	Authority string
	Now       func() time.Time
	// Log receives failures that are retried or worked around; nil means slog.Default().
	Log *slog.Logger

	// sleep waits between device-code polls; tests replace it to skip the wait.
	sleep func(ctx context.Context, d time.Duration) error
}

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Message         string `json:"message"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (m *Microsoft) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Microsoft) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

func (m *Microsoft) wait(ctx context.Context, d time.Duration) error {
	if m.sleep != nil {
		return m.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (m *Microsoft) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (m *Microsoft) endpoint(name string) string {
	authority := m.Authority
	if authority == "" {
		authority = "https://login.microsoftonline.com"
	}
	return strings.TrimRight(authority, "/") + "/" + url.PathEscape(m.Tenant) + "/oauth2/v2.0/" + name
}

func (m *Microsoft) post(ctx context.Context, name string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint(name), strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, out); err != nil && resp.StatusCode == http.StatusOK {
		return resp.StatusCode, fmt.Errorf("解析 %s 回應：%w", name, err)
	}
	return resp.StatusCode, nil
}

func (m *Microsoft) save(ctx context.Context, tr tokenResponse, oldRefresh string) error {
	refresh := tr.RefreshToken
	if refresh == "" {
		refresh = oldRefresh
	}
	return m.Store.SaveOAuthToken(ctx, m.Mailbox, store.OAuthToken{
		RefreshToken: refresh, AccessToken: tr.AccessToken,
		ExpiresAt: m.now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	})
}

func (m *Microsoft) AccessToken(ctx context.Context) (string, error) {
	tok, _, err := m.accessToken(ctx)
	return tok, err
}

// accessToken returns a usable access token; cached reports whether it came from
// the store rather than a refresh just now.
func (m *Microsoft) accessToken(ctx context.Context) (token string, cached bool, err error) {
	tok, ok, err := m.Store.GetOAuthToken(ctx, m.Mailbox)
	if err != nil {
		return "", false, err
	}
	if !ok || tok.RefreshToken == "" {
		return "", false, fmt.Errorf("信箱 %s 尚未授權：%w", m.Mailbox, ErrNeedsLogin)
	}
	if tok.AccessToken != "" && tok.ExpiresAt.Sub(m.now()) > refreshMargin {
		return tok.AccessToken, true, nil
	}
	var tr tokenResponse
	status, err := m.post(ctx, "token", url.Values{
		"client_id":     {m.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"scope":         {Scope},
	}, &tr)
	if err != nil {
		return "", false, fmt.Errorf("更新 OAuth token：%w", err)
	}
	if tr.Error == "invalid_grant" || tr.Error == "interaction_required" {
		return "", false, fmt.Errorf("信箱 %s 的授權已失效（%s）：%w", m.Mailbox, tr.Error, ErrNeedsLogin)
	}
	if status != http.StatusOK || tr.AccessToken == "" {
		return "", false, fmt.Errorf("更新 OAuth token 失敗：HTTP %d %s", status, tr.Error)
	}
	if err := m.save(ctx, tr, tok.RefreshToken); err != nil {
		return "", false, err
	}
	return tr.AccessToken, false, nil
}

// Authenticate logs in with XOAUTH2. When the server rejects a cached access
// token (for example because it was revoked), the token is marked expired so
// that the next session refreshes it instead of retrying it until its expiry.
func (m *Microsoft) Authenticate(ctx context.Context, c *imapclient.Client) error {
	tok, cached, err := m.accessToken(ctx)
	if err != nil {
		return err
	}
	err = c.Authenticate(NewXOAUTH2(m.Username, tok))
	var ie *imap.Error
	if err != nil && cached && errors.As(err, &ie) {
		if ierr := m.expire(ctx, tok); ierr != nil {
			return errors.Join(err, ierr)
		}
	}
	return err
}

// expire marks the stored access token as expired, keeping the refresh token.
// A token that another session has already replaced is left alone.
func (m *Microsoft) expire(ctx context.Context, access string) error {
	cur, ok, err := m.Store.GetOAuthToken(ctx, m.Mailbox)
	if err != nil || !ok || cur.AccessToken != access {
		return err
	}
	cur.ExpiresAt = m.now()
	return m.Store.SaveOAuthToken(ctx, m.Mailbox, cur)
}

func (m *Microsoft) StartDeviceLogin(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	status, err := m.post(ctx, "devicecode", url.Values{"client_id": {m.ClientID}, "scope": {Scope}}, &dc)
	if err != nil {
		return nil, fmt.Errorf("取得 device code：%w", err)
	}
	if status != http.StatusOK || dc.DeviceCode == "" {
		return nil, fmt.Errorf("取得 device code 失敗：HTTP %d（請確認 client_id、tenant 與「允許公開用戶端流程」設定）", status)
	}
	return &dc, nil
}

// describe formats a token endpoint error response for an error message.
func describe(status int, tr tokenResponse) string {
	s := fmt.Sprintf("HTTP %d", status)
	if tr.Error != "" {
		s += " " + tr.Error
	}
	if tr.ErrorDescription != "" {
		s += "（" + tr.ErrorDescription + "）"
	}
	return s
}

// PollDeviceLogin waits for the user to finish the device-code sign-in and
// saves the tokens. Server errors and network failures are retried until the
// code expires; the timeout error then includes the last failure.
func (m *Microsoft) PollDeviceLogin(ctx context.Context, dc *DeviceCode) error {
	interval := max(time.Duration(dc.Interval)*time.Second, minPollInterval)
	deadline := m.now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	var lastErr error
	for {
		var tr tokenResponse
		status, err := m.post(ctx, "token", url.Values{
			"client_id":   {m.ClientID},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {dc.DeviceCode},
		}, &tr)
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case err != nil:
			lastErr = err
		case status == http.StatusOK && tr.AccessToken != "":
			if tr.RefreshToken == "" {
				return fmt.Errorf("沒有取得 refresh token（scope 需要包含 offline_access）")
			}
			return m.save(ctx, tr, "")
		case tr.Error == "authorization_pending":
			lastErr = nil
		case tr.Error == "slow_down":
			lastErr = nil
			interval += 5 * time.Second
		case status >= http.StatusInternalServerError || status == http.StatusTooManyRequests:
			lastErr = errors.New(describe(status, tr))
		default:
			return fmt.Errorf("授權失敗：%s", describe(status, tr))
		}
		if lastErr != nil {
			m.log().Warn("查詢授權狀態失敗，稍後重試", "err", lastErr)
		}
		if m.now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("授權逾時，請重新執行 relay login（最後一次錯誤：%w）", lastErr)
			}
			return fmt.Errorf("授權逾時，請重新執行 relay login")
		}
		if err := m.wait(ctx, interval); err != nil {
			return err
		}
	}
}
