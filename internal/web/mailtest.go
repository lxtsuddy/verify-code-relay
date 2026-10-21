package web

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/tglen"
)

// Mail test: POST /api/rules/test (multipart/form-data) runs one message — an "eml" file
// or pasted "eml_text" — through the rules and keywords posted with it (the rules page
// as currently filled in, saved or not), optionally rendered for "mailbox", and answers
//
//	{"ok":true,"message":{"from","subject","warnings","text","links":[{"url","text"}]},
//	 "trace":[…],"result":{"kind","code","url","service","rule"},
//	 "preview":{"html","buttons","length","max","unsupported"}}
//
// or {"ok":false,"error":"…","errors":{path:message}}. The message stays in memory: the
// body cap (maxMailTestBody) is below the ParseMultipartForm memory limit
// (mailTestMemory), so no part spills to a temporary file. Nothing is stored or logged —
// the trace holds codes and links.

const (
	mailTestPath    = "/api/rules/test"
	maxMailTestBody = 6 << 20
	mailTestMemory  = 32 << 20 // must stay above maxMailTestBody
	mailTestText    = 500      // runes of the body shown

	msgMailTooBig  = "信件超過 5 MiB，實際運作時也會略過不辨識"
	msgMailMissing = "請上傳 .eml 檔，或貼上信件原始碼"
)

func (s *Server) postAPIRulesTest(w http.ResponseWriter, r *http.Request) {
	bad := func(msg string) { s.json(w, http.StatusOK, map[string]any{"ok": false, "error": msg}) }
	if err := r.ParseMultipartForm(mailTestMemory); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			bad(msgMailTooBig)
			return
		}
		s.fail(w, r, http.StatusBadRequest, "請求內容不是有效的表單")
		return
	}
	raw, err := mailTestSource(r)
	switch {
	case err != nil:
		s.fail(w, r, http.StatusBadRequest, "無法讀取上傳的檔案")
		return
	case len(raw) == 0:
		bad(msgMailMissing)
		return
	case len(raw) > pipeline.MaxMessageSize:
		bad(msgMailTooBig)
		return
	}

	rules := readRules(r)
	trial, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	trial.Rules = rules
	trial.Recognize = config.RecognizeOptions{
		ExtraCodeKeywords: readKeywords(r.PostFormValue("extra_code_keywords")),
		ExtraLinkKeywords: readKeywords(r.PostFormValue("extra_link_keywords")),
	}
	b, err := runtime.Build(trial)
	var built FieldErrors
	if err != nil {
		built = fieldErrors(err)
	}
	if fe := rulesErrors(checkRules(rules), built); len(fe) > 0 {
		s.json(w, http.StatusOK, map[string]any{"ok": false, "error": "規則有錯誤，請先修正再測試", "errors": fe})
		return
	}

	mb := pipeline.Mailbox{DisplayName: "（測試）"}
	if name := r.PostFormValue("mailbox"); name != "" {
		m, ok := b.Config.Mailboxes[name]
		if !ok {
			bad("找不到信箱 " + name + "，可能已被刪除；請重新整理頁面")
			return
		}
		mb = pipeline.Mailbox{Name: name, DisplayName: m.DisplayName, Username: m.Username}
	}
	t, err := pipeline.RunTrial(raw, b.Recognizer, b.Renderer, mb)
	if t == nil {
		bad(err.Error())
		return
	}
	s.json(w, http.StatusOK, mailTestReply(t, err))
}

// mailTestSource returns the uploaded file, else the pasted source with its line ends
// made CRLF (a browser textarea gives LF or CRLF; leading blank lines are dropped).
func mailTestSource(r *http.Request) ([]byte, error) {
	if f, _, err := r.FormFile("eml"); err == nil {
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, pipeline.MaxMessageSize+1))
	} else if !errors.Is(err, http.ErrMissingFile) {
		return nil, err
	}
	src := strings.TrimLeft(strings.ReplaceAll(r.PostFormValue("eml_text"), "\r\n", "\n"), "\n\r\t ")
	if src == "" {
		return nil, nil
	}
	return []byte(strings.ReplaceAll(src, "\n", "\r\n")), nil
}

func mailTestReply(t *pipeline.Trial, renderErr error) map[string]any {
	type link struct {
		URL  string `json:"url"`
		Text string `json:"text"`
	}
	links := []link{}
	for _, l := range t.Msg.Links {
		links = append(links, link{l.URL, l.Text})
	}
	from := t.Msg.From.Addr
	if t.Msg.From.Name != "" {
		from = t.Msg.From.Name + " <" + t.Msg.From.Addr + ">"
	}
	result := map[string]any{"kind": t.Kind}
	if t.Result != nil {
		result["code"], result["url"], result["service"], result["rule"] = t.Result.Code, t.Result.URL, t.Result.Service, t.Result.Rule
	}
	out := map[string]any{
		"ok": true,
		"message": map[string]any{
			"from": from, "subject": t.Msg.Subject, "warnings": append([]string{}, t.Msg.Warnings...),
			"text": truncate(t.Msg.Text, mailTestText), "links": links,
		},
		"trace":  append([]string{}, t.Trace.Steps...),
		"result": result,
	}
	switch {
	case t.Output != nil:
		safe, unsupported := sanitize(t.Output.Text)
		out["preview"] = map[string]any{"html": safe, "buttons": previewButtons(t.Output.ReplyMarkup),
			"length": tglen.Len(t.Output.Text), "max": tglen.Max, "unsupported": unsupported}
	case renderErr != nil:
		out["preview_error"] = renderErr.Error()
	}
	return out
}
