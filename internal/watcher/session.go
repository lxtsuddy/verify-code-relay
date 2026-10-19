package watcher

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/store"
)

var errDead = errors.New("IMAP 伺服器無回應")

// await runs f and gives up after d, closing the client so that f's pending
// command is released. IDLE has no read deadline in imapclient, so every
// command we issue goes through await.
func (w *Watcher) await(c *imapclient.Client, d time.Duration, f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		c.Close()
		return errDead
	}
}

// session runs one connection until it fails or ctx is cancelled. connected is
// called once the folder is selected and the first catch-up has succeeded, so a
// message that keeps failing does not reset the reconnect backoff.
func (w *Watcher) session(ctx context.Context, connected func()) error {
	sig := make(chan struct{}, 1)
	opts := &imapclient.Options{UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		// Runs on the client's read goroutine: only signal, never call client methods here.
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				select {
				case sig <- struct{}{}:
				default:
				}
			}
		},
	}}
	c, err := w.cfg.Dial(ctx, opts)
	if err != nil {
		return fmt.Errorf("連線：%w", err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	if err := w.await(c, w.cfg.FetchTimeout, func() error { return w.cfg.Auth.Authenticate(ctx, c) }); err != nil {
		var ie *imap.Error
		if errors.As(err, &ie) || errors.Is(err, auth.ErrNeedsLogin) {
			return &AuthError{Err: err}
		}
		return fmt.Errorf("認證：%w", err)
	}

	mb, folder := w.cfg.Mailbox, w.cfg.Folder
	var sd *imap.SelectData
	if err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		sd, err = c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
		return err
	}); err != nil {
		return fmt.Errorf("選取資料夾 %s：%w", folder, err)
	}
	uidNext, err := w.uidNext(c, sd)
	if err != nil {
		return err
	}
	st, ok, err := w.cfg.State.GetMailboxState(ctx, mb.Name, folder)
	if err != nil {
		return err
	}
	if !ok || st.UIDValidity != sd.UIDValidity {
		st = store.MailboxState{UIDValidity: sd.UIDValidity, LastUID: uidNext - 1}
		if err := w.cfg.State.SetMailboxState(ctx, mb.Name, folder, st); err != nil {
			return err
		}
		w.log.Info("初始化信箱狀態，既有信件不轉發", "uidvalidity", st.UIDValidity, "last_uid", st.LastUID)
	}
	if err := w.drain(ctx, c, &st); err != nil {
		return err
	}
	connected()

	caps := c.Caps()
	if caps == nil {
		return fmt.Errorf("取得伺服器能力失敗")
	}
	useIdle := !w.forcePoll && (caps.Has(imap.CapIdle) || caps.Has(imap.CapIMAP4rev2))
	if !useIdle {
		w.log.Info("伺服器不支援 IDLE，改用輪詢", "interval", w.cfg.PollInterval)
	}
	for {
		if useIdle {
			if err := w.idleOnce(ctx, c, sig); err != nil {
				return err
			}
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c.Closed():
				return errors.New("連線已關閉")
			case <-time.After(w.cfg.PollInterval):
			}
			if err := w.await(c, w.cfg.CmdTimeout, func() error { return c.Noop().Wait() }); err != nil {
				return fmt.Errorf("NOOP：%w", err)
			}
		}
		if err := w.drain(ctx, c, &st); err != nil {
			return err
		}
	}
}

// drain repeats catchUp until a pass finds nothing new. It guards against
// servers that hold back untagged updates outside IDLE: mail that arrives while
// we are fetching must be picked up here before we go back to IDLE.
func (w *Watcher) drain(ctx context.Context, c *imapclient.Client, st *store.MailboxState) error {
	for {
		n, err := w.catchUp(ctx, c, st)
		if err != nil || n == 0 {
			return err
		}
	}
}

func (w *Watcher) uidNext(c *imapclient.Client, sd *imap.SelectData) (uint32, error) {
	if sd.UIDNext != 0 {
		return uint32(sd.UIDNext), nil
	}
	var status *imap.StatusData
	err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		status, err = c.Status(w.cfg.Folder, &imap.StatusOptions{UIDNext: true}).Wait()
		return err
	})
	if errors.Is(err, errDead) {
		return 0, err
	}
	if err == nil && status.UIDNext != 0 {
		return uint32(status.UIDNext), nil
	}
	var sr *imap.SearchData
	if err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		sr, err = c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
		return err
	}); err != nil {
		return 0, fmt.Errorf("取得 UIDNEXT：%w", err)
	}
	var max imap.UID
	for _, u := range sr.AllUIDs() {
		if u > max {
			max = u
		}
	}
	return uint32(max) + 1, nil
}

// idleOnce idles until new mail, the liveness cycle, cancellation or disconnect.
func (w *Watcher) idleOnce(ctx context.Context, c *imapclient.Client, sig <-chan struct{}) error {
	var idle *imapclient.IdleCommand
	if err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		idle, err = c.Idle()
		return err
	}); err != nil {
		return fmt.Errorf("IDLE：%w", err)
	}
	cycle := time.NewTimer(w.cfg.IdleCycle)
	defer cycle.Stop()
	cycled := false
	select {
	case <-sig:
	case <-cycle.C:
		cycled = true
	case <-ctx.Done():
	case <-c.Closed():
	}
	if err := w.await(c, w.cfg.CmdTimeout, func() error {
		if err := idle.Close(); err != nil {
			return err
		}
		return idle.Wait()
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("結束 IDLE：%w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if cycled {
		if err := w.await(c, w.cfg.CmdTimeout, func() error { return c.Noop().Wait() }); err != nil {
			return fmt.Errorf("NOOP：%w", err)
		}
	}
	return nil
}

func toEnvelope(b *imapclient.FetchMessageBuffer) pipeline.Envelope {
	env := pipeline.Envelope{UID: uint32(b.UID), InternalDate: b.InternalDate, Size: b.RFC822Size}
	if b.Envelope != nil {
		env.Subject = b.Envelope.Subject
		if len(b.Envelope.From) > 0 {
			env.From = b.Envelope.From[0].Addr()
		}
	}
	return env
}

// catchChunk is how many messages' metadata catchUp fetches per command.
const catchChunk = 50

// catchUp handles every message with UID > st.LastUID and returns how many it
// handled. The pending UIDs are listed with UID SEARCH and their metadata is
// fetched in chunks, so a long outage cannot exceed a single command timeout.
func (w *Watcher) catchUp(ctx context.Context, c *imapclient.Client, st *store.MailboxState) (int, error) {
	var set imap.UIDSet
	set.AddRange(imap.UID(st.LastUID+1), 0) // LastUID+1:*
	var sr *imap.SearchData
	if err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		sr, err = c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
		return err
	}); err != nil {
		return 0, fmt.Errorf("搜尋新信：%w", err)
	}
	var uids []imap.UID
	for _, u := range sr.AllUIDs() {
		if uint32(u) > st.LastUID { // servers return the last message for n:* when n > max
			uids = append(uids, u)
		}
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	n := 0
	for len(uids) > 0 {
		k := min(catchChunk, len(uids))
		var chunk imap.UIDSet
		for _, u := range uids[:k] {
			chunk.AddNum(u)
		}
		uids = uids[k:]
		var bufs []*imapclient.FetchMessageBuffer
		if err := w.await(c, w.cfg.FetchTimeout, func() (err error) {
			bufs, err = c.Fetch(chunk, &imap.FetchOptions{UID: true, InternalDate: true, RFC822Size: true, Envelope: true}).Collect()
			return err
		}); err != nil {
			return n, fmt.Errorf("抓取新信清單：%w", err)
		}
		sort.Slice(bufs, func(i, j int) bool { return bufs[i].UID < bufs[j].UID })
		got, err := w.handle(ctx, c, st, bufs)
		n += got
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// handle processes one chunk of fetched metadata in UID order.
func (w *Watcher) handle(ctx context.Context, c *imapclient.Client, st *store.MailboxState, bufs []*imapclient.FetchMessageBuffer) (int, error) {
	mb, folder := w.cfg.Mailbox, w.cfg.Folder
	n := 0
	for _, b := range bufs {
		uid := uint32(b.UID)
		if uid <= st.LastUID {
			continue
		}
		n++
		env := toEnvelope(b)
		if kind := w.cfg.Processor.Decide(mb, env); kind != "" {
			if err := w.cfg.Processor.RecordSkipped(ctx, mb, folder, st.UIDValidity, env, kind); err != nil {
				return n, err
			}
			st.LastUID = uid
			continue
		}
		var body []*imapclient.FetchMessageBuffer
		if err := w.await(c, w.cfg.FetchTimeout, func() (err error) {
			body, err = c.Fetch(imap.UIDSetNum(b.UID), &imap.FetchOptions{
				UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}},
			}).Collect()
			return err
		}); err != nil {
			return n, fmt.Errorf("抓取信件 UID %d：%w", uid, err)
		}
		if len(body) == 0 || len(body[0].BodySection) == 0 {
			// expunged between the two fetches
			if err := w.cfg.State.SetMailboxState(ctx, mb.Name, folder, store.MailboxState{UIDValidity: st.UIDValidity, LastUID: uid}); err != nil {
				return n, err
			}
			st.LastUID = uid
			continue
		}
		if err := w.cfg.Processor.Process(ctx, mb, folder, st.UIDValidity, env, body[0].BodySection[0].Bytes); err != nil {
			return n, fmt.Errorf("處理信件 UID %d：%w", uid, err)
		}
		st.LastUID = uid
	}
	return n, nil
}
