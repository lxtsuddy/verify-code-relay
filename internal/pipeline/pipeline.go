// Package pipeline turns one fetched message into forward_log + outbox rows.
package pipeline

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"verify-code-relay/internal/mailparse"
	"verify-code-relay/internal/recognize"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/store"
)

const MaxMessageSize = 5 << 20

type Store interface {
	RecordMessage(ctx context.Context, e store.LogEntry, items []store.OutboxItem, st store.MailboxState) (int64, error)
}

type Renderer interface {
	Render(mailbox string, d render.Data) (render.Rendered, error)
}

type Target struct {
	Name   string
	Bot    string
	ChatID int64
}

type Mailbox struct {
	Name        string
	DisplayName string
	Username    string
	Targets     []Target
	MaxAge      time.Duration
	TTL         time.Duration
}

type Envelope struct {
	UID          uint32
	InternalDate time.Time
	Size         int64
	From         string
	Subject      string
}

type Processor struct {
	Store      Store
	Recognizer recognize.Recognizer
	Renderer   Renderer
	Notify     func()
	Log        *slog.Logger
	Now        func() time.Time
}

func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Processor) Decide(mb Mailbox, env Envelope) string {
	if env.Size > MaxMessageSize {
		return "skipped_size"
	}
	if !env.InternalDate.IsZero() && p.now().Sub(env.InternalDate) > mb.MaxAge {
		return "skipped_age"
	}
	return ""
}

func (p *Processor) RecordSkipped(ctx context.Context, mb Mailbox, folder string, uidValidity uint32, env Envelope, kind string) error {
	_, err := p.Store.RecordMessage(ctx, store.LogEntry{
		At: p.now(), Mailbox: mb.Name, Folder: folder, UID: env.UID, FromAddr: env.From, Subject: env.Subject,
		Kind: kind, Result: "not_forwarded",
	}, nil, store.MailboxState{UIDValidity: uidValidity, LastUID: env.UID})
	if err == nil {
		p.Log.Info("略過信件", "mailbox", mb.Name, "folder", folder, "uid", env.UID, "from", env.From,
			"subject", env.Subject, "kind", kind)
	}
	return err
}

// redact removes a verification code from text that is stored or logged.
func redact(s, code string) string {
	if code == "" {
		return s
	}
	return strings.ReplaceAll(s, code, "******")
}

func (p *Processor) Process(ctx context.Context, mb Mailbox, folder string, uidValidity uint32, env Envelope, raw []byte) error {
	now := p.now()
	entry := store.LogEntry{At: now, Mailbox: mb.Name, Folder: folder, UID: env.UID, FromAddr: env.From,
		Subject: env.Subject, Kind: "none", Result: "not_forwarded"}
	st := store.MailboxState{UIDValidity: uidValidity, LastUID: env.UID}
	logAttrs := func(e store.LogEntry) []any {
		return []any{"mailbox", mb.Name, "folder", folder, "uid", env.UID, "from", e.FromAddr, "subject", e.Subject, "kind", e.Kind, "rule", e.Rule}
	}

	msg, err := mailparse.Parse(raw)
	if err != nil {
		p.Log.Warn("無法解析信件", append(logAttrs(entry), "err", err)...)
		_, err = p.Store.RecordMessage(ctx, entry, nil, st)
		return err
	}
	if msg.Date.IsZero() {
		msg.Date = env.InternalDate
	}
	if msg.From.Addr != "" {
		entry.FromAddr = msg.From.Addr
	}
	if msg.Subject != "" {
		entry.Subject = msg.Subject
	}

	res, ignore, _ := p.Recognizer.Recognize(msg) // the trace may contain codes: never log it
	switch {
	case ignore:
		entry.Kind = "ignored"
		if res != nil {
			entry.Rule = res.Rule
		}
	case res == nil:
		entry.Kind = "none"
	}
	if ignore || res == nil {
		_, err := p.Store.RecordMessage(ctx, entry, nil, st)
		if err == nil {
			p.Log.Info("信件不符合轉發條件", logAttrs(entry)...)
		}
		return err
	}

	entry.Rule = res.Rule
	// Codes must never be stored or logged; the Telegram text below keeps the real subject.
	entry.Subject = redact(entry.Subject, res.Code)
	switch {
	case res.Code != "" && res.URL != "":
		entry.Kind = "both"
	case res.Code != "":
		entry.Kind = "code"
	default:
		entry.Kind = "link"
	}
	out, err := p.Renderer.Render(mb.Name, render.Data{
		Code: res.Code, URL: res.URL, Service: res.Service, FromName: msg.From.Name, FromAddr: msg.From.Addr,
		To: Recipient(msg.To, mb.Username), Subject: msg.Subject, Mailbox: mb.DisplayName, Rule: res.Rule, Date: msg.Date,
	})
	if err != nil {
		entry.Result = "failed"
		p.Log.Error("套用範本失敗", append(logAttrs(entry), "err", err)...)
		_, err = p.Store.RecordMessage(ctx, entry, nil, st)
		return err
	}
	entry.Result = "pending"
	items := make([]store.OutboxItem, 0, len(mb.Targets))
	for _, t := range mb.Targets {
		items = append(items, store.OutboxItem{Target: t.Name, Bot: t.Bot, ChatID: t.ChatID, Text: out.Text,
			ReplyMarkup: out.ReplyMarkup, CreatedAt: now, ExpiresAt: now.Add(mb.TTL), NextAttemptAt: now})
	}
	if _, err := p.Store.RecordMessage(ctx, entry, items, st); err != nil {
		return err
	}
	p.Log.Info("辨識成功，已排入發送佇列", append(logAttrs(entry), "targets", len(items))...)
	if p.Notify != nil {
		p.Notify()
	}
	return nil
}
