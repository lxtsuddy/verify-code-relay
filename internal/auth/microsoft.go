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
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/lxtsuddy/verify-code-relay/internal/store"
)

const Scope = "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"

const refreshMargin = 5 * time.Minute

// minPollInterval is the shortest wait between device-code polls, so that a
// missing or zero interval in the response does not turn into a busy loop.
const minPollInterval = 5 * time.Second

var (
	// ErrDeviceCodeExpired: the device code expired before the sign-in was finished.
	ErrDeviceCodeExpired = errors.New("授權逾時")
	// ErrLoginDeclined: the user declined the sign-in on Microsoft's page.
	ErrLoginDeclined = errors.New("授權被拒絕")
)

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
	// PollInterval is the wait between device-code polls; 0 means the server's interval,
	// at least minPollInterval. Tests set it to poll quickly.
	PollInterval time.Duration

	// sleep waits between device-code polls; tests replace it to skip the wait.
	sleep func(ctx context.Context, d time.Duration) error

	once    sync.Once
	lock    chan struct{}     // see acquire
	unsaved *store.OAuthToken // a refreshed token the store failed to save
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

// newToken builds the token to store from a token response, keeping the old
// refresh token when the server did not rotate it.
func (m *Microsoft) newToken(tr tokenResponse, oldRefresh string) store.OAuthToken {
	refresh := tr.RefreshToken
	if refresh == "" {
		refresh = oldRefresh
	}
	return store.OAuthToken{
		RefreshToken: refresh, AccessToken: tr.AccessToken,
		ExpiresAt: m.now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}
}

// acquire takes the lock that serialises reading, refreshing and saving the
// token: the folders of one mailbox share a Microsoft, and two concurrent
// refreshes would each rotate the refresh token.
func (m *Microsoft) acquire(ctx context.Context) error {
	m.once.Do(func() { m.lock = make(chan struct{}, 1) })
	select {
	case m.lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Microsoft) release() { <-m.lock }

// load returns the current token. A refreshed token that the store failed to
// save takes precedence over the store, and saving it is retried. Callers hold
// the lock.
func (m *Microsoft) load(ctx context.Context) (store.OAuthToken, bool, error) {
	if m.unsaved == nil {
		return m.Store.GetOAuthToken(ctx, m.Mailbox)
	}
	tok := *m.unsaved
	if err := m.Store.SaveOAuthToken(ctx, m.Mailbox, tok); err != nil {
		m.log().Error("仍無法儲存更新後的 OAuth token，繼續使用記憶體中的 token", "mailbox", m.Mailbox, "err", err)
	} else {
		m.unsaved = nil
	}
	return tok, true, nil
}

func (m *Microsoft) AccessToken(ctx context.Context) (string, error) {
	tok, _, err := m.accessToken(ctx)
	return tok, err
}

// accessToken returns a usable access token; cached reports whether it came from
// the store rather than a refresh just now. When a refresh fails for a reason
// other than a revoked grant, an access token that has not yet expired is
// still returned.
func (m *Microsoft) accessToken(ctx context.Context) (token string, cached bool, err error) {
	if err := m.acquire(ctx); err != nil {
		return "", false, err
	}
	defer m.release()
	tok, ok, err := m.load(ctx)
	if err != nil {
		return "", false, err
	}
	if !ok || tok.RefreshToken == "" {
		return "", false, fmt.Errorf("信箱 %s 尚未授權：%w", m.Mailbox, ErrNeedsLogin)
	}
	if tok.AccessToken != "" && tok.ExpiresAt.Sub(m.now()) > refreshMargin {
		return tok.AccessToken, true, nil
	}
	tr, err := m.refresh(ctx, tok.RefreshToken)
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, ErrNeedsLogin) && tok.AccessToken != "" && tok.ExpiresAt.After(m.now()) {
			m.log().Warn("更新 OAuth token 失敗，繼續使用尚未過期的 access token", "mailbox", m.Mailbox, "err", err)
			return tok.AccessToken, true, nil
		}
		return "", false, err
	}
	nt := m.newToken(tr, tok.RefreshToken)
	if err := m.Store.SaveOAuthToken(ctx, m.Mailbox, nt); err != nil {
		// The old refresh token may no longer work once rotated: keep the new
		// one in memory rather than losing it.
		m.unsaved = &nt
		m.log().Error("儲存更新後的 OAuth token 失敗，暫時保留在記憶體中", "mailbox", m.Mailbox, "err", err)
	}
	return tr.AccessToken, false, nil
}

// refresh redeems a refresh token at the token endpoint.
func (m *Microsoft) refresh(ctx context.Context, refreshToken string) (tokenResponse, error) {
	var tr tokenResponse
	status, err := m.post(ctx, "token", url.Values{
		"client_id":     {m.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"scope":         {Scope},
	}, &tr)
	if err != nil {
		return tr, fmt.Errorf("更新 OAuth token：%w", err)
	}
	if tr.Error == "invalid_grant" || tr.Error == "interaction_required" {
		return tr, fmt.Errorf("信箱 %s 的授權已失效（%s）：%w", m.Mailbox, tr.Error, ErrNeedsLogin)
	}
	if status != http.StatusOK || tr.AccessToken == "" {
		return tr, fmt.Errorf("更新 OAuth token 失敗：HTTP %d %s", status, tr.Error)
	}
	return tr, nil
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

// expire marks the current access token as expired, keeping the refresh token.
// A token that another session has already replaced is left alone.
func (m *Microsoft) expire(ctx context.Context, access string) error {
	if err := m.acquire(ctx); err != nil {
		return err
	}
	defer m.release()
	if m.unsaved != nil {
		if m.unsaved.AccessToken == access {
			m.unsaved.ExpiresAt = m.now()
		}
		return nil
	}
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
	if m.PollInterval > 0 {
		interval = m.PollInterval
	}
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
			return m.Store.SaveOAuthToken(ctx, m.Mailbox, m.newToken(tr, ""))
		case tr.Error == "authorization_pending":
			lastErr = nil
		case tr.Error == "slow_down":
			lastErr = nil
			interval += 5 * time.Second
		case tr.Error == "expired_token":
			return fmt.Errorf("%w：%s", ErrDeviceCodeExpired, describe(status, tr))
		case tr.Error == "authorization_declined":
			return fmt.Errorf("%w：%s", ErrLoginDeclined, describe(status, tr))
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
				return fmt.Errorf("%w，請重新執行 relay login（最後一次錯誤：%w）", ErrDeviceCodeExpired, lastErr)
			}
			return fmt.Errorf("%w，請重新執行 relay login", ErrDeviceCodeExpired)
		}
		if err := m.wait(ctx, interval); err != nil {
			return err
		}
	}
}
