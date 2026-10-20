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

// nextBackoff doubles cur, capped at max.
func nextBackoff(cur, max time.Duration) time.Duration {
	return min(cur*2, max)
}

// Run keeps a session alive with exponential backoff until ctx is cancelled or
// the mailbox needs an interactive OAuth login.
func (w *Watcher) Run(ctx context.Context) {
	key := w.Key()
	backoff := w.cfg.MinBackoff
	var downSince time.Time
	alertedDown, alertedAuth := false, false
	alertedCatchUp := false // the down alert was about a failing catch-up, not the connection

	for {
		err := w.session(ctx, func() {
			w.setStatus(StatusConnected)
			w.log.Info("已連線並開始監看")
			backoff = w.cfg.MinBackoff
			downSince = time.Time{}
			if alertedDown {
				text := fmt.Sprintf("信箱 %s 已恢復連線", key)
				if alertedCatchUp {
					text = fmt.Sprintf("信箱 %s 已完成補抓，恢復監看", key)
				}
				w.cfg.Alert.Recovered("disconnected:"+key, text)
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
			w.cfg.Alert.Alert("needs_login:"+name, fmt.Sprintf("信箱 %s 的 Microsoft 授權已失效，請到 Web 管理介面的信箱頁重新授權（或執行 relay login %s）", name, name))
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
		wait := jitter(backoff)
		// An auth failure is already covered by the auth:<key> alert.
		if ae == nil && !alertedDown {
			if w.now().Sub(downSince) >= w.cfg.AlertAfter {
				// The key stays disconnected:<key> so that Recovered pairs with either text.
				var ce *catchUpError
				alertedCatchUp = errors.As(err, &ce)
				what := "無法連線"
				if alertedCatchUp {
					what = "無法完成補抓"
				}
				w.cfg.Alert.Alert("disconnected:"+key, fmt.Sprintf("信箱 %s 已超過 %s %s：%v", key, w.cfg.AlertAfter, what, err))
				alertedDown = true
			} else if rem := w.cfg.AlertAfter - w.now().Sub(downSince); rem < wait {
				// Wake at the alert deadline instead of sleeping past it.
				wait = rem
			}
		}
		w.log.Warn("連線中斷，稍後重試", "err", err, "retry_in", wait.Round(time.Millisecond).String())
		backoff = nextBackoff(backoff, w.cfg.MaxBackoff)
		select {
		case <-ctx.Done():
			w.setStatus(StatusStopped)
			return
		case <-time.After(wait):
		}
	}
}
