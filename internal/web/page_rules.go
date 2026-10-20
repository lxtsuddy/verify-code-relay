package web

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"strings"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/runtime"
)

// Rules page: the ordered custom rules and the extra recognition keywords, saved together.
// Each rule is one row of parallel form arrays (rule_name, rule_from, rule_subject,
// rule_code_regex, rule_link_regex, rule_ignore "0"/"1"), in the order shown; rows with
// every field blank are dropped before anything else, so error paths count kept rows.
// Error keys are the config paths: "rules[i].field", "rules[i]" (the row as a whole) and
// "recognize.extra_code_keywords"/"recognize.extra_link_keywords"; the rest go to "".
//
// Templates page: the global templates (tpl_code, tpl_link, tpl_both; empty = built-in).
// Error keys: the field names; the rest go to "".

// maxTemplateLen bounds a template's source (saved or previewed).
const maxTemplateLen = 8 << 10

const (
	msgTemplateTooLong = "範本太長：上限 8 KiB"
	msgRuleNoName      = "請輸入規則名稱"
	msgRuleNoMatch     = "至少需要寄件者或主旨其中一項，規則才知道要套用在哪些信上"
	msgRuleNoAction    = "請填寫驗證碼或連結的正規表示式，或把處理方式改為「略過」"
)

var rulePathRe = regexp.MustCompile(`^rules\[\d+\](\.(name|from|subject|code_regex|link_regex|ignore))?$`)

type rulesData struct {
	Rules        []config.Rule
	NewRule      config.Rule // the blank row the page adds from
	CodeKeywords string      // one per line
	LinkKeywords string
}

func (s *Server) getRules(w http.ResponseWriter, r *http.Request) {
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderRules(w, r, http.StatusOK, nil, rulesData{
		Rules:        raw.Rules,
		CodeKeywords: strings.Join(raw.Recognize.ExtraCodeKeywords, "\n"),
		LinkKeywords: strings.Join(raw.Recognize.ExtraLinkKeywords, "\n"),
	})
}

func (s *Server) renderRules(w http.ResponseWriter, r *http.Request, status int, errs FieldErrors, d rulesData) {
	s.render(w, r, status, "rules", Page{Title: "辨識規則", Nav: "rules", Errors: errs, Data: d})
}

// readRules reads the rule rows of the posted form, dropping blank ones.
func readRules(r *http.Request) []config.Rule {
	col := func(k string) []string { return r.PostForm["rule_"+k] }
	names, from, subj, code, link, ign := col("name"), col("from"), col("subject"), col("code_regex"), col("link_regex"), col("ignore")
	n := max(len(names), len(from), len(subj), len(code), len(link), len(ign))
	at := func(v []string, i int) string {
		if i < len(v) {
			return strings.TrimSpace(v[i])
		}
		return ""
	}
	var out []config.Rule
	for i := range n {
		rule := config.Rule{
			Name: at(names, i), From: at(from, i), Subject: at(subj, i),
			CodeRegex: at(code, i), LinkRegex: at(link, i), Ignore: at(ign, i) == "1",
		}
		if rule == (config.Rule{}) {
			continue
		}
		out = append(out, rule)
	}
	return out
}

// readKeywords splits a textarea into trimmed, non-empty, distinct lines (nil when none).
func readKeywords(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(v, "\r\n", "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" && !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	return out
}

// checkRules finds the problems the form can name better than config.Validate.
func checkRules(rules []config.Rule) FieldErrors {
	fe := FieldErrors{}
	for i, rule := range rules {
		base := fmt.Sprintf("rules[%d]", i)
		if rule.Name == "" {
			fe.add(base+".name", msgRuleNoName)
		}
		if rule.From == "" && rule.Subject == "" {
			fe.add(base, msgRuleNoMatch)
		}
		if rule.CodeRegex == "" && rule.LinkRegex == "" && !rule.Ignore {
			fe.add(base, msgRuleNoAction)
		}
	}
	return fe
}

// rulesErrors merges the form's own findings (local) with Build's (built): a path the
// form already explained keeps the form's message; a regex reported by both
// config.Validate and the recognizer keeps only Validate's 「正規表示式錯誤」. Paths outside
// the page go, as "path: message", under "".
func rulesErrors(local, built FieldErrors) FieldErrors {
	out := FieldErrors{}
	rest := FieldErrors{}
	for k, v := range local {
		out[k] = v
	}
	for k, v := range built {
		switch {
		case local[k] != "":
		case rulePathRe.MatchString(k) || strings.HasPrefix(k, "recognize."):
			out[k] = preferValidate(v)
		default:
			rest[k] = v
		}
	}
	if len(rest) > 0 {
		out.add("", rest.Error())
	}
	return out
}

func preferValidate(msg string) string {
	lines := strings.Split(msg, "\n")
	var keep []string
	for _, l := range lines {
		if strings.HasPrefix(l, "正規表示式錯誤") {
			keep = append(keep, l)
		}
	}
	if len(keep) == 0 {
		return msg
	}
	return strings.Join(keep, "\n")
}

func (s *Server) postRules(w http.ResponseWriter, r *http.Request) {
	d := rulesData{
		Rules:        readRules(r),
		CodeKeywords: strings.Join(readKeywords(r.PostFormValue("extra_code_keywords")), "\n"),
		LinkKeywords: strings.Join(readKeywords(r.PostFormValue("extra_link_keywords")), "\n"),
	}
	opts := config.RecognizeOptions{
		ExtraCodeKeywords: readKeywords(d.CodeKeywords),
		ExtraLinkKeywords: readKeywords(d.LinkKeywords),
	}
	// stored values are compared as the form would read them back (an imported rule or
	// keyword with outer spaces is not a change)
	apply := func(raw *config.Config) bool {
		changed := !reflect.DeepEqual(normRules(raw.Rules), d.Rules) ||
			!reflect.DeepEqual(readKeywords(strings.Join(raw.Recognize.ExtraCodeKeywords, "\n")), opts.ExtraCodeKeywords) ||
			!reflect.DeepEqual(readKeywords(strings.Join(raw.Recognize.ExtraLinkKeywords, "\n")), opts.ExtraLinkKeywords)
		raw.Rules, raw.Recognize = d.Rules, opts
		return changed
	}
	back := func(fe FieldErrors) { s.renderRules(w, r, http.StatusUnprocessableEntity, fe, d) }

	// report everything at once: the form's own checks plus a trial build of the change
	local := checkRules(d.Rules)
	trial, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	apply(trial)
	var built FieldErrors
	if _, err := runtime.Build(trial); err != nil {
		built = fieldErrors(err)
	}
	if fe := rulesErrors(local, built); len(fe) > 0 {
		back(fe)
		return
	}

	fe, err := s.mutate(r, "修改辨識規則", func(raw *config.Config) error {
		if !apply(raw) {
			return errNoChange
		}
		return nil
	})
	switch {
	case errors.Is(err, errNoChange):
		s.redirect(w, r, "/rules", &Flash{Kind: "info", Text: msgNoChange})
	case err != nil:
		s.serverError(w, r, err)
	case len(fe) > 0:
		back(rulesErrors(nil, fe))
	default:
		s.redirect(w, r, "/rules", &Flash{Kind: "success", Text: "已儲存辨識規則並套用"})
	}
}

// normRules is rules as readRules would read them back from the form (nil when none).
func normRules(rules []config.Rule) []config.Rule {
	var out []config.Rule
	for _, r := range rules {
		r = config.Rule{
			Name: strings.TrimSpace(r.Name), From: strings.TrimSpace(r.From), Subject: strings.TrimSpace(r.Subject),
			CodeRegex: strings.TrimSpace(r.CodeRegex), LinkRegex: strings.TrimSpace(r.LinkRegex), Ignore: r.Ignore,
		}
		if r != (config.Rule{}) {
			out = append(out, r)
		}
	}
	return out
}

// ---------- templates ----------

// tplKind is one of the three global templates on the page.
type tplKind struct {
	Key     string // code | link | both
	Field   string // form field (and error key)
	ID      string // textarea id
	Label   string
	Desc    string
	Value   string // what the editor holds
	Builtin string // the built-in template it falls back to
	Err     string
}

type templatesData struct {
	Kinds []tplKind
	Vars  []tplVar
}

// tplVar is a variable the editor offers, with the preview's sample value.
type tplVar struct{ Name, Label, Sample string }

var tplVars = []tplVar{
	{".Code", "驗證碼", sampleCode},
	{".URL", "認證連結", sampleURL},
	{".Service", "服務名稱", "GitHub"},
	{".FromName", "寄件者名稱", "GitHub"},
	{".FromAddr", "寄件者地址", "noreply@github.com"},
	{".To", "收件者", "me@example.com"},
	{".Subject", "主旨", "[GitHub] Please verify your device"},
	{".Mailbox", "信箱顯示名稱", "個人 Gmail"},
	{".Date", "收信時間", "time.Time"},
	{".Rule", "命中的規則", "builtin"},
}

func newTemplatesData(t config.Templates) templatesData {
	b := render.Builtin()
	return templatesData{
		Vars: tplVars,
		Kinds: []tplKind{
			{Key: "code", Field: "tpl_code", ID: "tpl-code", Label: "驗證碼", Desc: "信裡只找到驗證碼時使用。", Value: t.Code, Builtin: b.Code},
			{Key: "link", Field: "tpl_link", ID: "tpl-link", Label: "登入連結", Desc: "信裡只找到認證或登入連結時使用。", Value: t.Link, Builtin: b.Link},
			{Key: "both", Field: "tpl_both", ID: "tpl-both", Label: "驗證碼＋連結", Desc: "同一封信同時有驗證碼與連結時使用。", Value: t.Both, Builtin: b.Both},
		},
	}
}

func (s *Server) getTemplates(w http.ResponseWriter, r *http.Request) {
	raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderTemplates(w, r, http.StatusOK, nil, raw.Templates)
}

func (s *Server) renderTemplates(w http.ResponseWriter, r *http.Request, status int, errs FieldErrors, t config.Templates) {
	d := newTemplatesData(t)
	for i := range d.Kinds {
		d.Kinds[i].Err = errs[d.Kinds[i].Field]
	}
	s.render(w, r, status, "templates", Page{Title: "訊息範本", Nav: "rules", Errors: errs, Data: d})
}

// readTemplate reads a template field: browser CRLF line ends back to LF, outer space trimmed.
func readTemplate(r *http.Request, k string) string { return normTemplate(r.PostFormValue(k)) }

// normTemplate is a template as the form reads it back (also used to compare stored ones).
func normTemplate(v string) string { return strings.TrimSpace(strings.ReplaceAll(v, "\r\n", "\n")) }

func (s *Server) postTemplates(w http.ResponseWriter, r *http.Request) {
	t := config.Templates{Code: readTemplate(r, "tpl_code"), Link: readTemplate(r, "tpl_link"), Both: readTemplate(r, "tpl_both")}
	back := func(fe FieldErrors) { s.renderTemplates(w, r, http.StatusUnprocessableEntity, fe, t) }
	local := FieldErrors{}
	for field, v := range map[string]string{"tpl_code": t.Code, "tpl_link": t.Link, "tpl_both": t.Both} {
		if len(v) > maxTemplateLen {
			local[field] = msgTemplateTooLong
		}
	}
	if len(local) > 0 {
		back(local)
		return
	}
	fe, err := s.mutate(r, "修改訊息範本", func(raw *config.Config) error {
		if normTemplate(raw.Templates.Code) == t.Code && normTemplate(raw.Templates.Link) == t.Link && normTemplate(raw.Templates.Both) == t.Both {
			return errNoChange
		}
		raw.Templates = t
		return nil
	})
	switch {
	case errors.Is(err, errNoChange):
		s.redirect(w, r, "/templates", &Flash{Kind: "info", Text: msgNoChange})
	case err != nil:
		s.serverError(w, r, err)
	case len(fe) > 0:
		back(remap(fe, "", map[string]string{"templates.code": "tpl_code", "templates.link": "tpl_link", "templates.both": "tpl_both"}))
	default:
		s.redirect(w, r, "/templates", &Flash{Kind: "success", Text: "已儲存訊息範本並套用"})
	}
}
