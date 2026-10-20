package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/config"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/store"
)

// Microsoft device-code authorisation in the browser. One flow per mailbox lives in
// memory (a restart forgets it): start asks Microsoft for a user code and polls for the
// token in the background; the page polls status every 3 seconds.
//
// The token is only stored while the flow is still registered and the stored mailbox
// still has the account, client_id and tenant the flow was started for (flowStore), so a
// mailbox that was deleted or changed meanwhile never receives it. Deleting a mailbox or
// changing those settings also cancels its flow (cancelFlow).
//
// The device code never leaves the goroutine that polls with it; the user code and the
// verification URI are meant to be shown.

const (
	statePending = "pending"
	stateDone    = "done"
	stateError   = "error"

	flowStartTimeout = 30 * time.Second
	flowKeepReported = time.Minute      // a finished flow, after the page has seen the result
	flowKeepFinished = 15 * time.Minute // a finished flow nobody asked about
	defaultCodeLife  = 15 * time.Minute // when Microsoft does not say
)

type deviceFlow struct {
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	State           string // pending | done | error
	Error           string
	cancel          context.CancelFunc

	acct                   msAccount     // the settings the token is for
	life                   time.Duration // how long the code is valid
	finishedAt, reportedAt time.Time
}

// msAccount is what a Microsoft token belongs to.
type msAccount struct{ Username, ClientID, Tenant string }

func accountOf(m *config.Mailbox) msAccount {
	return msAccount{m.Username, m.Auth.ClientID, effectiveTenant(m.Auth.Tenant)}
}

var errFlowStale = errors.New("信箱設定已變更或已刪除，這次授權作廢，請重新授權")

// flowStore saves the token of one flow only while that flow is current (see above).
type flowStore struct {
	s    *Server
	name string
	f    *deviceFlow
}

func (fs flowStore) GetOAuthToken(ctx context.Context, mailbox string) (store.OAuthToken, bool, error) {
	return fs.s.o.Store.GetOAuthToken(ctx, mailbox)
}

// SaveOAuthToken holds saveMu, so no config change can slip in between the check and the
// save; a change stored before it has cancelled the flow or fails the check, and one stored
// after it deletes the token again.
func (fs flowStore) SaveOAuthToken(ctx context.Context, mailbox string, t store.OAuthToken) error {
	s := fs.s
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.flowMu.Lock()
	current := s.flows[fs.name] == fs.f && fs.f.State == statePending
	s.flowMu.Unlock()
	if !current {
		return errFlowStale
	}
	raw, _, err := runtime.LoadConfig(ctx, s.o.Store)
	if err != nil {
		return err
	}
	m := raw.Mailboxes[fs.name]
	if m == nil || m.Auth.Type != authMicrosoft || accountOf(m) != fs.f.acct {
		return errFlowStale
	}
	return s.o.Store.SaveOAuthToken(ctx, mailbox, t)
}

// pruneFlows drops finished flows that have been kept long enough; s.flowMu is held.
func (s *Server) pruneFlows(now time.Time) {
	for name, f := range s.flows {
		if f.State == statePending {
			continue
		}
		if (!f.reportedAt.IsZero() && now.Sub(f.reportedAt) >= flowKeepReported) || now.Sub(f.finishedAt) >= flowKeepFinished {
			delete(s.flows, name)
		}
	}
}

// cancelFlow stops and forgets the flow of mailbox name, if any. Call it after storing a
// config change that deletes the mailbox or changes its account (before deleting the
// token).
func (s *Server) cancelFlow(name string) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	if f := s.flows[name]; f != nil {
		f.cancel()
		delete(s.flows, name)
	}
}

// Close cancels every authorisation flow and waits for them to end; later starts are
// refused. Call it before stopping the runtime.
func (s *Server) Close() {
	s.flowMu.Lock()
	s.closed = true
	for name, f := range s.flows {
		f.cancel()
		delete(s.flows, name)
	}
	s.flowMu.Unlock()
	s.flowWG.Wait()
}

// microsoftMailbox returns the stored mailbox name, answering 404 when there is none.
func (s *Server) microsoftMailbox(w http.ResponseWriter, r *http.Request, name string) (*config.Mailbox, bool) {
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	m := raw.Mailboxes[name]
	if m == nil {
		s.fail(w, r, http.StatusNotFound, "找不到信箱 "+name+"，可能已被刪除；請重新整理頁面")
		return nil, false
	}
	return m, true
}

func (s *Server) flowAnswer(f *deviceFlow) map[string]any {
	left := max(int(f.ExpiresAt.Sub(s.now())/time.Second), 0)
	return map[string]any{
		"ok": true, "state": f.State, "user_code": f.UserCode, "verification_uri": f.VerificationURI,
		"expires_at": f.ExpiresAt.UTC().Format(time.RFC3339), "expires_in": left, "lifetime": int(f.life / time.Second),
	}
}

// postOAuthStart answers {"ok":true,"user_code","verification_uri","expires_at",
// "expires_in","state"}, reusing a pending flow of the same account.
func (s *Server) postOAuthStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, ok := s.microsoftMailbox(w, r, name)
	if !ok {
		return
	}
	if m.Auth.Type != authMicrosoft {
		s.fail(w, r, http.StatusBadRequest, "信箱 "+name+" 使用密碼登入，不需要 Microsoft 授權")
		return
	}
	acct := accountOf(m)

	s.flowStartMu.Lock() // one devicecode request at a time; status keeps answering
	defer s.flowStartMu.Unlock()
	s.flowMu.Lock()
	if s.closed {
		s.flowMu.Unlock()
		s.fail(w, r, http.StatusServiceUnavailable, "服務正在關閉，請稍後再試")
		return
	}
	s.pruneFlows(s.now())
	if f := s.flows[name]; f != nil && f.State == statePending {
		if f.acct == acct {
			ans := s.flowAnswer(f)
			s.flowMu.Unlock()
			s.json(w, http.StatusOK, ans)
			return
		}
		f.cancel() // started for settings that have changed since
		delete(s.flows, name)
	}
	s.flowMu.Unlock()

	f := &deviceFlow{State: statePending, acct: acct}
	ms := &auth.Microsoft{Mailbox: name, Username: m.Username, ClientID: m.Auth.ClientID, Tenant: m.Auth.Tenant,
		Store: flowStore{s, name, f}, Authority: s.o.MSAuthority, Log: s.o.Log, PollInterval: s.o.MSPollInterval}
	ctx, cancel := context.WithTimeout(r.Context(), flowStartTimeout)
	dc, err := ms.StartDeviceLogin(ctx)
	cancel()
	if err != nil {
		s.o.Log.Warn("無法開始 Microsoft 授權", "mailbox", name, "err", err)
		s.json(w, http.StatusBadGateway, map[string]any{"ok": false, "error": "無法向 Microsoft 取得登入代碼：" + err.Error()})
		return
	}
	life := time.Duration(dc.ExpiresIn) * time.Second
	if life <= 0 {
		life = defaultCodeLife
		dc.ExpiresIn = int(life / time.Second)
	}
	f.UserCode, f.VerificationURI, f.ExpiresAt, f.life = dc.UserCode, dc.VerificationURI, s.now().Add(life), life
	pollCtx, stop := context.WithTimeout(context.Background(), life)
	f.cancel = stop

	s.flowMu.Lock()
	if s.closed {
		s.flowMu.Unlock()
		stop()
		s.fail(w, r, http.StatusServiceUnavailable, "服務正在關閉，請稍後再試")
		return
	}
	s.flows[name] = f
	s.flowWG.Add(1)
	ans := s.flowAnswer(f)
	s.flowMu.Unlock()
	go s.runFlow(pollCtx, name, f, ms, dc)
	s.o.Log.Info("已開始 Microsoft 授權，等待使用者登入", "mailbox", name)
	s.json(w, http.StatusOK, ans)
}

// runFlow polls until the user signs in, the code expires or the flow is cancelled.
func (s *Server) runFlow(ctx context.Context, name string, f *deviceFlow, ms *auth.Microsoft, dc *auth.DeviceCode) {
	defer s.flowWG.Done()
	defer f.cancel()
	err := ms.PollDeviceLogin(ctx, dc)
	s.flowMu.Lock()
	if s.flows[name] != f { // cancelled: the mailbox changed or the server is closing
		s.flowMu.Unlock()
		s.o.Log.Info("Microsoft 授權流程已取消", "mailbox", name)
		return
	}
	f.finishedAt = s.now()
	if err == nil {
		f.State = stateDone
	} else {
		f.State, f.Error = stateError, flowError(err)
	}
	s.flowMu.Unlock()
	if err != nil {
		s.o.Log.Warn("Microsoft 授權沒有完成", "mailbox", name, "err", err)
		return
	}
	s.o.Log.Info("Microsoft 授權已完成", "mailbox", name)
	if err := s.o.Runtime.Reload(); err != nil {
		s.o.Log.Error("授權完成後重新套用設定失敗", "mailbox", name, "err", err)
	}
	s.o.Runtime.Notify("信箱 " + name + " 的 Microsoft 授權已完成")
}

// flowError explains why a flow ended without a token.
func flowError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, auth.ErrDeviceCodeExpired):
		return "代碼已過期：時限內沒有完成 Microsoft 登入。請按「重試」取得新的代碼。"
	case errors.Is(err, auth.ErrLoginDeclined):
		return "Microsoft 登入頁上拒絕了授權。要讓服務讀取這個信箱，請按「重試」並同意存取權限。"
	case errors.Is(err, errFlowStale):
		return errFlowStale.Error()
	}
	return err.Error()
}

// getOAuthStatus answers {"state","error"}; state "none" means there is no flow (never
// started, cancelled, forgotten after a restart, or reported over a minute ago).
func (s *Server) getOAuthStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.microsoftMailbox(w, r, name); !ok {
		return
	}
	now := s.now()
	s.flowMu.Lock()
	s.pruneFlows(now)
	f := s.flows[name]
	if f == nil {
		s.flowMu.Unlock()
		s.json(w, http.StatusOK, map[string]any{"ok": true, "state": "none", "error": ""})
		return
	}
	if f.State != statePending && f.reportedAt.IsZero() {
		f.reportedAt = now
	}
	ans := map[string]any{"ok": true, "state": f.State, "error": f.Error}
	s.flowMu.Unlock()
	s.json(w, http.StatusOK, ans)
}
