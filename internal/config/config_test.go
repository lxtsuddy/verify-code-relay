package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleYAML = `
data_dir: /data
bots:
  main: { token: "${TG}" }
targets:
  me:
    bot: main
    chat_id: ${CHAT}   # block style: '{' '}' are not allowed in flow-style plain scalars
admin: me
mailboxes:
  gmail:
    host: imap.gmail.com
    username: me@gmail.com
    auth: { type: password, password: "${PW}" }
    targets: [me]
  outlook:
    display_name: Outlook
    host: outlook.office365.com
    port: 1993
    username: me@outlook.com
    auth: { type: microsoft_oauth, client_id: abc }
    folders: [INBOX, Junk]
    targets: [me]
    max_age: 5m
    templates:
      code: "X {{.Code}}"
rules:
  - name: gh
    from: "*@github.com"
    code_regex: 'code: (\d{6})'
recognize:
  extra_code_keywords: ["認証コード"]
`

func TestParse(t *testing.T) {
	env := lookupFrom(map[string]string{"TG": "123:abc", "CHAT": "-1001", "PW": `p"w#: x`})
	c, err := Parse([]byte(sampleYAML), env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Bots["main"].Token != "123:abc" || c.Targets["me"].ChatID != -1001 {
		t.Fatalf("bots/targets wrong: %+v %+v", c.Bots, c.Targets)
	}
	g := c.Mailboxes["gmail"]
	if g.Name != "gmail" || g.DisplayName != "gmail" || g.Port != 993 {
		t.Errorf("gmail defaults wrong: %+v", g)
	}
	if g.Auth.Password != `p"w#: x` {
		t.Errorf("password = %q", g.Auth.Password)
	}
	if len(g.Folders) != 1 || g.Folders[0] != "INBOX" {
		t.Errorf("folders = %v", g.Folders)
	}
	if g.MaxAge.Duration != 10*time.Minute || g.TTL.Duration != 15*time.Minute || g.PollInterval.Duration != 30*time.Second {
		t.Errorf("gmail effective durations wrong: %+v", g)
	}
	o := c.Mailboxes["outlook"]
	if o.Port != 1993 || o.MaxAge.Duration != 5*time.Minute || o.Auth.Tenant != "" {
		t.Errorf("outlook wrong: %+v", o)
	}
	if id, tenant := c.MicrosoftApp(o); id != "abc" || tenant != "common" {
		t.Errorf("outlook effective app = %q %q, want abc common", id, tenant)
	}
	if o.Templates.Code != "X {{.Code}}" {
		t.Errorf("template override lost")
	}
	if c.Defaults.AlertTTL.Duration != 24*time.Hour || c.Defaults.AlertAfter.Duration != 10*time.Minute {
		t.Errorf("defaults wrong: %+v", c.Defaults)
	}
	if c.LogLevel != "info" {
		t.Errorf("log level default = %q", c.LogLevel)
	}
	if len(c.Rules) != 1 || c.Rules[0].CodeRegex != `code: (\d{6})` {
		t.Errorf("rules = %+v", c.Rules)
	}
	if got := c.SortedMailboxNames(); strings.Join(got, ",") != "gmail,outlook" {
		t.Errorf("sorted names = %v", got)
	}
}

func TestParseMissingEnvHasPath(t *testing.T) {
	_, err := Parse([]byte(sampleYAML), lookupFrom(map[string]string{"TG": "x", "CHAT": "1"}))
	if err == nil || !strings.Contains(err.Error(), "mailboxes.gmail.auth.password") || !strings.Contains(err.Error(), "PW") {
		t.Fatalf("want path+var in error, got %v", err)
	}
}

func TestParseBadDuration(t *testing.T) {
	_, err := Parse([]byte("defaults: { ttl: banana }"), lookupFrom(nil))
	if err == nil {
		t.Fatal("want duration error")
	}
}

func TestParseUnknownField(t *testing.T) {
	_, err := Parse([]byte("data_dirr: /x"), lookupFrom(nil))
	if err == nil || !strings.Contains(err.Error(), "data_dirr") {
		t.Fatalf("want unknown field error, got %v", err)
	}
}

func TestLoadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("data_dir: ./d\n"), 0o600)
	c, err := Load(p)
	if err != nil || c.DataDir != "./d" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}

func TestParseBadDurationLineNumber(t *testing.T) {
	// The bad value is on line 5; a re-encode of the tree would report a different line.
	_, err := Parse([]byte("\n\n# comment\ndefaults:\n  ttl: banana\n"), lookupFrom(nil))
	if err == nil || !strings.Contains(err.Error(), "第 5 行") {
		t.Fatalf("want error mentioning 第 5 行, got %v", err)
	}
}

func TestParseUnquotedSecret(t *testing.T) {
	const y = `
mailboxes:
  m:
    host: h
    username: u
    auth:
      type: password
      password: ${PW}
`
	c, err := Parse([]byte(y), lookupFrom(map[string]string{"PW": `p"w#: x`}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Mailboxes["m"].Auth.Password; got != `p"w#: x` {
		t.Fatalf("password = %q, want %q", got, `p"w#: x`)
	}
}

// An unquoted ${X} whose value looks like YAML null must stay a string,
// while numeric values (chat_id) are still re-resolved.
func TestParseExpandedNullStaysString(t *testing.T) {
	const y = `
bots:
  main:
    token: ${TG}
  alt:
    token: ${TILDE}
targets:
  me:
    bot: main
    chat_id: ${CHAT}
mailboxes:
  m:
    host: h
    username: u
    auth:
      type: password
      password: ${PW}
`
	c, err := Parse([]byte(y), lookupFrom(map[string]string{"TG": "null", "TILDE": "~", "CHAT": "-1001", "PW": "Null"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Bots["main"].Token; got != "null" {
		t.Errorf("token = %q, want %q", got, "null")
	}
	if got := c.Bots["alt"].Token; got != "~" {
		t.Errorf("token = %q, want %q", got, "~")
	}
	if got := c.Mailboxes["m"].Auth.Password; got != "Null" {
		t.Errorf("password = %q, want %q", got, "Null")
	}
	if got := c.Targets["me"].ChatID; got != -1001 {
		t.Errorf("chat_id = %d, want -1001", got)
	}
}

// "$$" in the YAML document yields a literal "$", quoted or not.
func TestParseDollarDollar(t *testing.T) {
	const y = `
bots:
  a: { token: "p$$w" }
  b:
    token: x$${NOT_EXPANDED}
`
	c, err := Parse([]byte(y), lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Bots["a"].Token; got != "p$w" {
		t.Errorf("a token = %q, want %q", got, "p$w")
	}
	if got := c.Bots["b"].Token; got != "x${NOT_EXPANDED}" {
		t.Errorf("b token = %q, want %q", got, "x${NOT_EXPANDED}")
	}
}

// Set distinguishes an explicit 0s from an omitted value; only omitted values get defaults.
func TestParseDurationSet(t *testing.T) {
	c, err := Parse([]byte(`
defaults: { ttl: 0s }
mailboxes:
  m: { max_age: 0s }
`), lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if d := c.Defaults.TTL; !d.Set || d.Duration != 0 {
		t.Errorf("defaults.ttl = %+v, want explicit 0", d)
	}
	if d := c.Defaults.MaxAge; d.Set || d.Duration != 10*time.Minute {
		t.Errorf("defaults.max_age = %+v, want unset default 10m", d)
	}
	m := c.Mailboxes["m"]
	if !m.MaxAge.Set || m.MaxAge.Duration != 0 {
		t.Errorf("mailbox max_age = %+v, want explicit 0", m.MaxAge)
	}
	if m.PollInterval.Duration != 30*time.Second {
		t.Errorf("mailbox poll_interval = %v, want default 30s", m.PollInterval.Duration)
	}
}
