package render

import "verify-code-relay/internal/config"

// The subject and URL parts collapse when the field is empty (missing in the mail, or
// removed by Render to fit the length limit), so no dangling "·" or "🔗" remains.
const (
	defaultCode = `🔐 <b>{{.Service}}</b> 驗證碼
<code>{{.Code}}</code>
📧 {{.Mailbox}}{{if .Subject}} · {{.Subject}}{{end}}`

	defaultLink = `🔗 <b>{{.Service}}</b> 認證連結
{{- with .URL}}
{{.}}{{end}}
📧 {{.Mailbox}}{{if .Subject}} · {{.Subject}}{{end}}`

	defaultBoth = `🔐 <b>{{.Service}}</b> 驗證碼
<code>{{.Code}}</code>
{{- with .URL}}
🔗 {{.}}{{end}}
📧 {{.Mailbox}}{{if .Subject}} · {{.Subject}}{{end}}`
)

// Builtin returns the built-in templates, used where neither the mailbox nor the global
// templates set one (the admin's template editor starts from them).
func Builtin() config.Templates {
	return config.Templates{Code: defaultCode, Link: defaultLink, Both: defaultBoth}
}
