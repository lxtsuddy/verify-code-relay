package pipeline

import (
	"fmt"

	"github.com/lxtsuddy/verify-code-relay/internal/mailparse"
	"github.com/lxtsuddy/verify-code-relay/internal/recognize"
	"github.com/lxtsuddy/verify-code-relay/internal/render"
)

// Trial is what one message would do, worked out without storing or sending anything;
// `relay test` and the admin's mail test show it. The trace holds codes and links: it
// must never be logged.
type Trial struct {
	Msg    *mailparse.Message
	Result *recognize.Result // nil when nothing matched
	Kind   string            // code | link | both | ignored | none
	Trace  *recognize.Trace
	Output *render.Rendered // the Telegram message; nil unless Kind is code, link or both
}

// RunTrial parses raw and runs it through rec and rnd as Process would for mailbox mb
// (only its Name, DisplayName and Username are used). A parse error returns no Trial; a
// template error returns the Trial so far.
func RunTrial(raw []byte, rec recognize.Recognizer, rnd Renderer, mb Mailbox) (*Trial, error) {
	msg, err := mailparse.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("解析信件：%w", err)
	}
	res, ignore, tr := rec.Recognize(msg)
	t := &Trial{Msg: msg, Result: res, Kind: kindOf(res, ignore), Trace: tr}
	if t.Kind == "ignored" || t.Kind == "none" {
		return t, nil
	}
	out, err := rnd.Render(mb.Name, renderData(msg, res, mb))
	if err != nil {
		return t, fmt.Errorf("套用範本：%w", err)
	}
	t.Output = &out
	return t, nil
}

// kindOf names what a recognition outcome forwards.
func kindOf(res *recognize.Result, ignore bool) string {
	switch {
	case ignore:
		return "ignored"
	case res == nil:
		return "none"
	case res.Code != "" && res.URL != "":
		return "both"
	case res.Code != "":
		return "code"
	}
	return "link"
}

// renderData is what the templates see for msg, recognised as res, in mailbox mb.
func renderData(msg *mailparse.Message, res *recognize.Result, mb Mailbox) render.Data {
	return render.Data{
		Code: res.Code, URL: res.URL, LinkText: res.LinkText, Service: res.Service, FromName: msg.From.Name, FromAddr: msg.From.Addr,
		To: Recipient(msg.To, mb.Username), Subject: msg.Subject, Mailbox: mb.DisplayName, Rule: res.Rule, Date: msg.Date,
	}
}
