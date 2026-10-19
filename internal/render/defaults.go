package render

const (
	defaultCode = `🔐 <b>{{.Service}}</b> 驗證碼
<code>{{.Code}}</code>
📧 {{.Mailbox}} · {{.Subject}}`

	defaultLink = `🔗 <b>{{.Service}}</b> 認證連結
{{.URL}}
📧 {{.Mailbox}} · {{.Subject}}`

	defaultBoth = `🔐 <b>{{.Service}}</b> 驗證碼
<code>{{.Code}}</code>
🔗 {{.URL}}
📧 {{.Mailbox}} · {{.Subject}}`
)
