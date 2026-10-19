// Package dispatcher drains the SQLite outbox to Telegram.
package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"verify-code-relay/internal/store"
	"verify-code-relay/internal/telegram"
)

type Store interface {
	DueOutbox(ctx context.Context, now time.Time, limit int) ([]store.OutboxItem, error)
	DeleteExpired(ctx context.Context, now time.Time) ([]store.OutboxItem, error)
	DeleteOutbox(ctx context.Context, id int64) error
	RescheduleOutbox(ctx context.Context, id int64, attempts int, next time.Time, plainFallback bool) error
	SetLogResult(ctx context.Context, logID int64, result string) error
}

type Sender interface {
	Send(ctx context.Context, req telegram.SendRequest) error
}

type Options struct {
	Store  Store
	Sender Sender
	Tokens map[string]string
	Alert  func(key, text string)
	Log    *slog.Logger
	Now    func() time.Time
}

type chatKey struct {
	bot  string
	chat int64
}

type Dispatcher struct {
	o        Options
	wake     chan struct{}
	lastSent map[chatKey]time.Time
}

const (
	perChatGap   = time.Second
	batchLimit   = 50
	sendTimeout  = 10 * time.Second
	backoffStart = 2 * time.Second
	backoffMax   = time.Minute
)

func New(o Options) *Dispatcher {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return &Dispatcher{o: o, wake: make(chan struct{}, 1), lastSent: map[chatKey]time.Time{}}
}

func (d *Dispatcher) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		if err := d.RunOnce(ctx); err != nil && ctx.Err() == nil {
			d.o.Log.Error("dispatcher", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-t.C:
		}
	}
}

func (d *Dispatcher) RunOnce(ctx context.Context) error {
	// Store calls use a context detached from shutdown so a cancelled run can still record results.
	sctx := context.WithoutCancel(ctx)
	now := d.o.Now()
	expired, err := d.o.Store.DeleteExpired(sctx, now)
	if err != nil {
		return err
	}
	for _, it := range expired {
		d.o.Log.Warn("訊息逾時未送出，已丟棄", "target", it.Target, "log_id", it.LogID)
		d.setResult(sctx, it, "expired")
	}
	due, err := d.o.Store.DueOutbox(sctx, now, batchLimit)
	if err != nil {
		return err
	}
	for _, it := range due {
		if ctx.Err() != nil {
			return nil
		}
		key := chatKey{it.Bot, it.ChatID}
		if last, ok := d.lastSent[key]; ok && now.Sub(last) < perChatGap {
			continue
		}
		d.send(sctx, it, now)
		d.lastSent[key] = d.o.Now()
	}
	return nil
}

func (d *Dispatcher) send(ctx context.Context, it store.OutboxItem, now time.Time) {
	token, ok := d.o.Tokens[it.Bot]
	if !ok {
		d.o.Log.Error("找不到 Bot，丟棄訊息", "bot", it.Bot, "target", it.Target)
		d.fail(ctx, it, fmt.Sprintf("找不到 Bot %q", it.Bot), false)
		return
	}
	req := telegram.SendRequest{Token: token, ChatID: it.ChatID, Text: it.Text, HTML: true, ReplyMarkup: it.ReplyMarkup}
	if it.PlainFallback {
		req = telegram.SendRequest{Token: token, ChatID: it.ChatID, Text: telegram.PlainText(it.Text)}
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	err := d.o.Sender.Send(sendCtx, req)
	cancel()
	if err == nil {
		d.o.Store.DeleteOutbox(ctx, it.ID)
		d.setResult(ctx, it, "sent")
		d.o.Log.Info("已轉發", "target", it.Target, "log_id", it.LogID)
		return
	}
	var se *telegram.SendError
	if !errors.As(err, &se) {
		se = &telegram.SendError{Kind: telegram.Transient, Description: err.Error()}
	}
	attempts := it.Attempts + 1
	switch se.Kind {
	case telegram.RateLimited:
		d.o.Store.RescheduleOutbox(ctx, it.ID, attempts, now.Add(se.RetryAfter), it.PlainFallback)
	case telegram.BadRequest:
		if !it.PlainFallback {
			d.o.Log.Warn("Telegram 拒絕 HTML 格式，改用純文字重送", "target", it.Target, "err", se.Description)
			d.o.Store.RescheduleOutbox(ctx, it.ID, attempts, now, true)
			return
		}
		d.fail(ctx, it, se.Description, true)
	case telegram.Forbidden:
		d.fail(ctx, it, se.Description, true)
	default:
		delay := backoffStart << min(it.Attempts, 10)
		if delay > backoffMax {
			delay = backoffMax
		}
		d.o.Log.Warn("發送失敗，稍後重試", "target", it.Target, "err", se.Description, "retry_in", delay)
		d.o.Store.RescheduleOutbox(ctx, it.ID, attempts, now.Add(delay), it.PlainFallback)
	}
}

func (d *Dispatcher) fail(ctx context.Context, it store.OutboxItem, reason string, alert bool) {
	d.o.Store.DeleteOutbox(ctx, it.ID)
	d.setResult(ctx, it, "failed")
	d.o.Log.Error("訊息發送失敗，已放棄", "target", it.Target, "log_id", it.LogID, "err", reason)
	if alert && it.LogID != 0 && d.o.Alert != nil {
		d.o.Alert("send_failed:"+it.Target, fmt.Sprintf("發送到目標 %s 失敗：%s", it.Target, reason))
	}
}

func (d *Dispatcher) setResult(ctx context.Context, it store.OutboxItem, result string) {
	if it.LogID != 0 {
		if err := d.o.Store.SetLogResult(ctx, it.LogID, result); err != nil {
			d.o.Log.Error("更新轉發紀錄失敗", "err", err)
		}
	}
}
