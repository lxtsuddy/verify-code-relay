package render

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
