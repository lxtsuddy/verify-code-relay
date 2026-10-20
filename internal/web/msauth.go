package web

import (
	"context"
	"errors"
	"net/http"
	"sync"
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
// still has the account, client_id, tenant, host and port the flow was started for (flowStore), so a
// mailbox that was deleted or changed meanwhile never receives it. Every stored config
// change (mailbox edit or delete, version restore, …) also cancels the pending flows it
// made stale (cancelStaleFlows, called by mutateAs), and a new flow is only registered after
// checking the stored account under saveMu, so no change can slip in between.
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
	Warning         string // done, but applying the new token failed
	cancel          context.CancelFunc

	acct                   msAccount     // the settings the token is for
	life                   time.Duration // how long the code is valid
	finishedAt, reportedAt time.Time
}

// msAccount is what a Microsoft token belongs to (runtime.MSAccount).
type msAccount = runtime.MSAccount

func accountOf(m *config.Mailbox) msAccount { return runtime.MSAccountOf(m) }

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

// probeStore is the token store of a connection test. A token the test refreshes (or
// marks expired) is only saved while the stored mailbox still uses Microsoft with the
// account the test started with; otherwise it is dropped. Like flowStore it holds saveMu,
// so a change stored before the save fails the check and one stored after it deletes the
// token again: a test never writes back a token that a change has just deleted.
type probeStore struct {
	s    *Server
	name string
	acct msAccount
}

func (ps probeStore) GetOAuthToken(ctx context.Context, mailbox string) (store.OAuthToken, bool, error) {
	return ps.s.o.Store.GetOAuthToken(ctx, mailbox)
}

func (ps probeStore) SaveOAuthToken(ctx context.Context, mailbox string, t store.OAuthToken) error {
	s := ps.s
	ctx = context.WithoutCancel(ctx) // a rotated refresh token is not lost to the test's deadline
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	raw, _, err := runtime.LoadConfig(ctx, s.o.Store)
	if err != nil {
		return err
	}
	if m := raw.Mailboxes[ps.name]; m == nil || m.Auth.Type != authMicrosoft || accountOf(m) != ps.acct {
		s.o.Log.Info("連線測試期間信箱設定已變更，不儲存測試時更新的 Microsoft 授權", "mailbox", ps.name)
		return nil
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

// cancelStaleFlows stops and forgets every pending flow whose mailbox raw (the config just
// stored) no longer has, or has with another account or without Microsoft authorisation.
// mutateAs calls it with saveMu held, before any token is deleted.
func (s *Server) cancelStaleFlows(raw *config.Config) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	for name, f := range s.flows {
		if f.State != statePending {
			continue
		}
		if m := raw.Mailboxes[name]; m == nil || m.Auth.Type != authMicrosoft || accountOf(m) != f.acct {
			f.cancel()
			delete(s.flows, name)
			s.o.Log.Info("信箱設定已變更，取消 Microsoft 授權流程", "mailbox", name)
		}
	}
}

// startLock serialises the starts of one mailbox's flow (n counts holders and waiters).
type startLock struct {
	mu sync.Mutex
	n  int
}

// lockStart waits until no other start for mailbox name is running; call the returned
// function to release. Starts for other mailboxes are not affected.
func (s *Server) lockStart(name string) (unlock func()) {
	s.flowMu.Lock()
	l := s.startLocks[name]
	if l == nil {
		l = &startLock{}
		s.startLocks[name] = l
	}
	l.n++
	s.flowMu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		s.flowMu.Lock()
		if l.n--; l.n == 0 {
			delete(s.startLocks, name)
		}
		s.flowMu.Unlock()
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

// storedMailbox returns the stored mailbox name, answering 404 when there is none.
func (s *Server) storedMailbox(w http.ResponseWriter, r *http.Request, name string) (*config.Mailbox, bool) {
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

// microsoftMailbox is storedMailbox that also answers 400 unless the mailbox uses Microsoft
// authorisation.
func (s *Server) microsoftMailbox(w http.ResponseWriter, r *http.Request, name string) (*config.Mailbox, bool) {
	m, ok := s.storedMailbox(w, r, name)
	if ok && m.Auth.Type != authMicrosoft {
		s.fail(w, r, http.StatusBadRequest, "信箱 "+name+" 使用密碼登入，不需要 Microsoft 授權")
		return nil, false
	}
	return m, ok
}

func (s *Server) flowAnswer(f *deviceFlow) map[string]any {
	left := max(int(f.ExpiresAt.Sub(s.now())/time.Second), 0)
	return map[string]any{
		"ok": true, "state": f.State, "user_code": f.UserCode, "verification_uri": f.VerificationURI,
		"expires_at": f.ExpiresAt.UTC().Format(time.RFC3339), "expires_in": left, "lifetime": int(f.life / time.Second),
		"warning": f.Warning,
	}
}

// postOAuthStart answers {"ok":true,"user_code","verification_uri","expires_at",
// "expires_in","lifetime","state","warning"}, reusing a pending flow of the same account.
func (s *Server) postOAuthStart(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	unlock := s.lockStart(name) // one start per mailbox at a time; status keeps answering
	defer unlock()
	m, ok := s.microsoftMailbox(w, r, name) // after waiting: the mailbox may have changed
	if !ok {
		return
	}
	acct := accountOf(m)

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

	// register under saveMu after checking the stored account again: a change stored while
	// Microsoft answered could not cancel a flow that was not registered yet
	s.saveMu.Lock()
	raw, _, err := runtime.LoadConfig(context.WithoutCancel(r.Context()), s.o.Store)
	if err != nil {
		s.saveMu.Unlock()
		stop()
		s.serverError(w, r, err)
		return
	}
	if cur := raw.Mailboxes[name]; cur == nil || cur.Auth.Type != authMicrosoft || accountOf(cur) != acct {
		s.saveMu.Unlock()
		stop()
		s.fail(w, r, http.StatusConflict, "信箱 "+name+" 的設定剛被修改，請重新整理頁面後再授權")
		return
	}
	s.flowMu.Lock()
	if s.closed {
		s.flowMu.Unlock()
		s.saveMu.Unlock()
		stop()
		s.fail(w, r, http.StatusServiceUnavailable, "服務正在關閉，請稍後再試")
		return
	}
	s.flows[name] = f
	s.flowWG.Add(1)
	ans := s.flowAnswer(f)
	s.flowMu.Unlock()
	s.saveMu.Unlock()
	go s.runFlow(pollCtx, name, f, ms, dc)
	s.o.Log.Info("已開始 Microsoft 授權，等待使用者登入", "mailbox", name)
	s.json(w, http.StatusOK, ans)
}

// runFlow polls until the user signs in, the code expires or the flow is cancelled.
func (s *Server) runFlow(ctx context.Context, name string, f *deviceFlow, ms *auth.Microsoft, dc *auth.DeviceCode) {
	defer s.flowWG.Done()
	defer f.cancel()
	err := ms.PollDeviceLogin(ctx, dc)
	var reloadErr error
	if err == nil {
		s.o.Log.Info("Microsoft 授權已完成，重新套用設定", "mailbox", name)
		// before reporting done, so the page can say whether the new token is in use
		if reloadErr = s.o.Runtime.Reload(); reloadErr != nil {
			s.o.Log.Error("授權完成後重新套用設定失敗", "mailbox", name, "err", reloadErr)
		}
	}
	s.flowMu.Lock()
	if s.flows[name] != f { // cancelled: the mailbox changed or the server is closing
		s.flowMu.Unlock()
		s.o.Log.Info("Microsoft 授權流程已取消", "mailbox", name)
		return
	}
	f.finishedAt = s.now()
	switch {
	case err != nil:
		f.State, f.Error = stateError, flowError(err)
	case reloadErr != nil:
		f.State, f.Warning = stateDone, msgReloadFailed
	default:
		f.State = stateDone
	}
	s.flowMu.Unlock()
	switch {
	case err != nil:
		s.o.Log.Warn("Microsoft 授權沒有完成", "mailbox", name, "err", err)
	case reloadErr != nil:
		s.o.Runtime.Notify("信箱 " + name + " 的 Microsoft 授權已完成，但" + msgReloadFailed)
	default:
		s.o.Runtime.Notify("信箱 " + name + " 的 Microsoft 授權已完成")
	}
}

// msgReloadFailed explains a token that was stored but not applied. The reason (which
// quotes config values) only goes to the log.
const msgReloadFailed = "重新套用設定失敗，信箱還沒有用新的授權連線。請到「系統設定」檢查設定後再儲存一次，或重新啟動服務；詳情請看服務日誌。"

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

// getOAuthStatus answers {"state","error","warning"}; state "none" means there is no flow (never
// started, cancelled, forgotten after a restart, or reported over a minute ago).
func (s *Server) getOAuthStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.storedMailbox(w, r, name); !ok {
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
	ans := map[string]any{"ok": true, "state": f.State, "error": f.Error, "warning": f.Warning}
	s.flowMu.Unlock()
	s.json(w, http.StatusOK, ans)
}
