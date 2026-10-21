package render

import "verify-code-relay/internal/config"

// The URL itself is not printed: the button opens it, labelled with the link's text from
// the mail, and the host line shows where it goes. Lines whose fields are empty (missing
// in the mail, or the subject removed by Render to fit the length limit) disappear.
const (
	headerLines = `
{{- if .FromAddr}}
👤 寄件者：{{if .FromName}}{{.FromName}}（{{.FromAddr}}）{{else}}{{.FromAddr}}{{end}}
{{- else if .FromName}}
👤 寄件者：{{.FromName}}{{end}}
{{- if .To}}
📥 收件者：{{.To}}{{if .Mailbox}}（{{.Mailbox}}）{{end}}
{{- else if .Mailbox}}
📥 收件信箱：{{.Mailbox}}{{end}}
{{- if .Subject}}
📝 主旨：{{.Subject}}{{end}}`

	defaultCode = `🔐 <b>{{.Service}}</b> 驗證碼
<code>{{.Code}}</code>` + headerLines

	defaultLink = `🔗 <b>{{.Service}}</b> 認證連結
{{- with .Host}}
🌐 {{.}}{{end}}` + headerLines

	defaultBoth = `🔐 <b>{{.Service}}</b> 驗證碼
<code>{{.Code}}</code>
{{- with .Host}}
🌐 {{.}}{{end}}` + headerLines
)

// Builtin returns the built-in templates, used where neither the mailbox nor the global
// templates set one (the admin's template editor starts from them).
func Builtin() config.Templates {
	return config.Templates{Code: defaultCode, Link: defaultLink, Both: defaultBoth}
}
