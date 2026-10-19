package watcher

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"verify-code-relay/internal/auth"
)

func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// Run keeps a session alive with exponential backoff until ctx is cancelled or
// the mailbox needs an interactive OAuth login.
func (w *Watcher) Run(ctx context.Context) {
	key := w.Key()
	backoff := w.cfg.MinBackoff
	var downSince time.Time
	alertedDown, alertedAuth := false, false

	for {
		err := w.session(ctx, func() {
			w.setStatus(StatusConnected)
			w.log.Info("已連線並開始監看")
			backoff = w.cfg.MinBackoff
			downSince = time.Time{}
			if alertedDown {
				w.cfg.Alert.Recovered("disconnected:"+key, fmt.Sprintf("信箱 %s 已恢復連線", key))
				alertedDown = false
			}
			if alertedAuth {
				w.cfg.Alert.Recovered("auth:"+key, fmt.Sprintf("信箱 %s 認證已恢復", key))
				alertedAuth = false
			}
		})
		if ctx.Err() != nil {
			w.setStatus(StatusStopped)
			return
		}
		var ae *AuthError
		switch {
		case errors.Is(err, auth.ErrNeedsLogin):
			w.setStatus(StatusNeedsLogin)
			name := w.cfg.Mailbox.Name
			w.log.Error("OAuth 授權失效，停止監看", "err", err)
			w.cfg.Alert.Alert("needs_login:"+name, fmt.Sprintf("信箱 %s 的 Microsoft 授權已失效，請執行：relay login %s", name, name))
			return
		case errors.As(err, &ae):
			w.setStatus(StatusAuthFailed)
			if !alertedAuth {
				w.cfg.Alert.Alert("auth:"+key, fmt.Sprintf("信箱 %s 認證失敗：%v", key, ae.Err))
				alertedAuth = true
			}
		default:
			w.setStatus(StatusReconnecting)
		}
		if downSince.IsZero() {
			downSince = w.now()
		}
		if !alertedDown && w.now().Sub(downSince) >= w.cfg.AlertAfter {
			w.cfg.Alert.Alert("disconnected:"+key, fmt.Sprintf("信箱 %s 已超過 %s 無法連線：%v", key, w.cfg.AlertAfter, err))
			alertedDown = true
		}
		wait := jitter(backoff)
		w.log.Warn("連線中斷，稍後重試", "err", err, "retry_in", wait.Round(time.Millisecond))
		backoff = min(backoff*2, w.cfg.MaxBackoff)
		select {
		case <-ctx.Done():
			w.setStatus(StatusStopped)
			return
		case <-time.After(wait):
		}
	}
}
