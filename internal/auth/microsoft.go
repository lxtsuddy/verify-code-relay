package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/store"
)

const Scope = "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"

const refreshMargin = 5 * time.Minute

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
	tok, ok, err := m.Store.GetOAuthToken(ctx, m.Mailbox)
	if err != nil {
		return "", err
	}
	if !ok || tok.RefreshToken == "" {
		return "", fmt.Errorf("信箱 %s 尚未授權：%w", m.Mailbox, ErrNeedsLogin)
	}
	if tok.AccessToken != "" && tok.ExpiresAt.Sub(m.now()) > refreshMargin {
		return tok.AccessToken, nil
	}
	var tr tokenResponse
	status, err := m.post(ctx, "token", url.Values{
		"client_id":     {m.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"scope":         {Scope},
	}, &tr)
	if err != nil {
		return "", fmt.Errorf("更新 OAuth token：%w", err)
	}
	if tr.Error == "invalid_grant" || tr.Error == "interaction_required" {
		return "", fmt.Errorf("信箱 %s 的授權已失效（%s）：%w", m.Mailbox, tr.Error, ErrNeedsLogin)
	}
	if status != http.StatusOK || tr.AccessToken == "" {
		return "", fmt.Errorf("更新 OAuth token 失敗：HTTP %d %s", status, tr.Error)
	}
	if err := m.save(ctx, tr, tok.RefreshToken); err != nil {
		return "", err
	}
	return tr.AccessToken, nil
}

func (m *Microsoft) Authenticate(ctx context.Context, c *imapclient.Client) error {
	tok, err := m.AccessToken(ctx)
	if err != nil {
		return err
	}
	return c.Authenticate(NewXOAUTH2(m.Username, tok))
}

func (m *Microsoft) StartDeviceLogin(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	status, err := m.post(ctx, "devicecode", url.Values{"client_id": {m.ClientID}, "scope": {Scope}}, &dc)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || dc.DeviceCode == "" {
		return nil, fmt.Errorf("取得 device code 失敗：HTTP %d（請確認 client_id、tenant 與「允許公開用戶端流程」設定）", status)
	}
	return &dc, nil
}

func (m *Microsoft) PollDeviceLogin(ctx context.Context, dc *DeviceCode) error {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := m.now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for {
		var tr tokenResponse
		status, err := m.post(ctx, "token", url.Values{
			"client_id":   {m.ClientID},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {dc.DeviceCode},
		}, &tr)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusOK && tr.AccessToken != "":
			if tr.RefreshToken == "" {
				return fmt.Errorf("沒有取得 refresh token（scope 需要包含 offline_access）")
			}
			return m.save(ctx, tr, "")
		case tr.Error == "authorization_pending":
		case tr.Error == "slow_down":
			interval += 5 * time.Second
		default:
			return fmt.Errorf("授權失敗：%s %s", tr.Error, tr.ErrorDescription)
		}
		if m.now().After(deadline) {
			return fmt.Errorf("授權逾時，請重新執行 relay login")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
