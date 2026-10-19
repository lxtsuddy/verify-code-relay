# verify-code-relay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `relay`, a Go daemon that watches multiple IMAP mailboxes via IDLE, recognizes verification-code / verification-link emails with rules, renders them through templates and forwards them to Telegram chats through multiple bots.

**Architecture:** One goroutine per (mailbox, folder) keeps an IMAP IDLE session; new mail is parsed (`mailparse`), recognized (`recognize`), rendered (`render`) and written to a SQLite outbox in the same transaction that advances the mailbox UID watermark (`pipeline` + `store`). A `dispatcher` drains the outbox to the Telegram Bot API with per-chat pacing, retries, plain-text fallback and TTL expiry. Config is a YAML file; a small CLI wraps run / validate / login / test / send-test.

**Tech Stack:** Go 1.26 (toolchain auto-download from the installed 1.24 works), `github.com/emersion/go-imap/v2 v2.0.0-beta.8`, `github.com/emersion/go-message v0.18.2`, `github.com/emersion/go-sasl`, `golang.org/x/net/html`, `gopkg.in/yaml.v3`, `modernc.org/sqlite` (pure Go, no cgo), stdlib `log/slog`, `html/template`, `net/http`.

**Spec:** `docs/superpowers/specs/2026-10-19-verify-code-relay-design.md` — read it before starting any task; this plan argues from it.

## Global Constraints

- Module path: `verify-code-relay`; `go 1.26` in `go.mod`.
- No cgo: `CGO_ENABLED=0` must build. SQLite driver is `modernc.org/sqlite` (driver name `"sqlite"`).
- Mailboxes are strictly read-only: `SELECT` with `ReadOnly: true` (EXAMINE) and `BODY.PEEK[]` only. Never STORE/MOVE/EXPUNGE.
- Never log verification codes, full URLs, passwords, OAuth tokens, or bot tokens. Log mailbox, folder, UID, sender, subject, kind only.
- `forward_log` never stores codes or URLs.
- Defaults (spec §9): `max_age 10m`, `ttl 15m`, `alert_ttl 24h`, `poll_interval 30s`, `alert_after 10m`, IMAP port `993`, folders `[INBOX]`, max message size `5 MiB`.
- Names (bots/targets/mailboxes/rules) match `^[a-z0-9_-]+$`.
- Telegram: `sendMessage` only, `parse_mode=HTML`, link previews disabled, per-chat ≥1 s spacing, text ≤ 4096, `copy_text` button text 1–256 chars.
- IMAP liveness: never set a blanket read timeout during IDLE; cycle IDLE every 25 min (`Close` → `NOOP` → `Idle`) and treat >30 s without response as dead.
- Reconnect backoff: 1 s doubling to 5 min, ±20 % jitter, reset after a successful SELECT.
- User-facing messages (CLI output, config errors, alerts, default templates) are Traditional Chinese, as in the spec.
- Every task ends with `go vet ./... && go test ./...` green before committing.
- Commit messages end with the line `Co-Authored-By: Claude Code <noreply@anthropic.com>`.

## Verified library facts (go-imap v2.0.0-beta.8)

Executors: these were verified against the module source on 2026-10-19; rely on them.

- `imapclient.DialTLS(addr, *Options)`, `imapclient.DialInsecure(addr, *Options)`, `imapclient.New(net.Conn, *Options)`.
- `Options{UnilateralDataHandler *UnilateralDataHandler, Dialer *net.Dialer, TLSConfig *tls.Config}`. The handler runs on the client's read goroutine and **blocks the client** — only do a non-blocking channel send inside it.
- `UnilateralDataHandler{Mailbox func(*UnilateralDataMailbox)}`; `UnilateralDataMailbox.NumMessages *uint32` non-nil means EXISTS changed.
- `c.Idle() (*IdleCommand, error)`; `IdleCommand.Close()` then `IdleCommand.Wait()`. The library restarts IDLE itself every 28 min; it sets **no** read deadline while idle.
- `c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait() (*imap.SelectData, error)`; `SelectData.UIDValidity uint32`, `SelectData.UIDNext imap.UID` (0 if server omitted it).
- `c.Status(name, &imap.StatusOptions{UIDNext: true}).Wait()` → `*imap.StatusData{UIDNext imap.UID}`.
- `var s imap.UIDSet; s.AddRange(start, 0)` encodes `start:*`. `imap.UIDSetNum(uid)`.
- `c.Fetch(set, &imap.FetchOptions{UID, InternalDate, RFC822Size, Envelope bool, BodySection: []*imap.FetchItemBodySection{{Peek: true}}}).Collect() ([]*imapclient.FetchMessageBuffer, error)`; buffer fields `UID imap.UID`, `InternalDate time.Time`, `RFC822Size int64`, `Envelope *imap.Envelope`, `BodySection []FetchBodySectionBuffer{Bytes []byte}`.
- `imap.Envelope{Subject string, From []imap.Address}`; `(*imap.Address).Addr()`.
- `c.Noop().Wait()`, `c.Login(u, p).Wait()`, `c.Authenticate(sasl.Client) error`, `c.Caps().Has(imap.CapIdle)`, `c.Close()`, `c.Closed() <-chan struct{}`.
- `*imap.Error` (alias of `StatusResponse{Type, Code, Text}`); `imap.ResponseCodeAuthenticationFailed`.
- In-memory test server: `imapmemserver.New()`, `imapmemserver.NewUser(u, p)`, `user.Create("INBOX", nil)`, `mem.AddUser(user)`, `imapserver.New(&imapserver.Options{NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) { return mem.NewSession(), nil, nil }, Caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}}, InsecureAuth: true})`, `srv.Serve(listener)`. A message APPENDed from a second connection produces an unilateral EXISTS on an idling client (verified). `UID FETCH n:*` with n beyond max returns 0 messages on this server (real servers may return the last message — always filter `uid > last_uid`).
- `go-sasl` has **no** XOAUTH2 client; we implement `sasl.Client` ourselves (`Start() (mech string, ir []byte, err error)`, `Next(challenge []byte) ([]byte, error)`).

## File Structure

```
go.mod / go.sum
cmd/relay/
  main.go          # subcommand dispatch, usage
  app.go           # shared: load+validate config, logger, build recognizer/renderer
  run.go           # `relay run` wiring + graceful shutdown
  validate.go      # `relay config validate`
  testcmd.go       # `relay test --eml`
  sendtest.go      # `relay send-test`
  login.go         # `relay login`
  healthcheck.go   # hidden `relay healthcheck`
internal/config/   config.go (types, Load, defaults), env.go (${ENV} expansion on yaml.Node), validate.go
internal/store/    store.go (Open, migrations), state.go, outbox.go, oauth.go
internal/mailparse/ parse.go, html.go
internal/recognize/ recognize.go (types, Chain, New, service name), code.go, link.go, custom.go
internal/render/   render.go, defaults.go
internal/telegram/ telegram.go
internal/dispatcher/ dispatcher.go
internal/alert/    alert.go
internal/auth/     auth.go (interface, Password, xoauth2), microsoft.go
internal/pipeline/ pipeline.go (Processor: decide/skip/process), recipient.go
internal/watcher/  watcher.go (supervisor, backoff, status), session.go (one IMAP connection)
internal/health/   health.go
testdata/eml/      *.eml + *.expected.yaml corpus, gen_encoded.py
config.example.yaml, Dockerfile, docker-compose.yml, .env.example, .dockerignore, README.md
```

Deviation from spec §2.2 (intentional, approved scope): processing of a fetched message lives in `internal/pipeline` instead of inside `watcher`, so it can be unit-tested without IMAP. `recognize.Chain.Recognize(msg) (*Result, bool, *Trace)` matches the spec's `Recognizer` signature; internal stages receive the `*Trace` to append to.

Addition to spec §3.1 (found while verifying this plan): after every wake-up the watcher repeats catch-up until a pass finds no new UID (`drain`, Task 14) before re-entering IDLE. go-imap's `imapmemserver` — and possibly some real servers — only report EXISTS for changes that happen *during* IDLE, so mail arriving while we fetch would otherwise wait for the 25-minute cycle. Microsoft scope is `https://outlook.office.com/IMAP.AccessAsUser.All offline_access` (spec corrected).

---
### Task 1: Project scaffold + config loading (types, `${ENV}` expansion, defaults)

**Files:**
- Create: `go.mod`, `.gitignore`
- Create: `internal/config/config.go`, `internal/config/env.go`
- Test: `internal/config/config_test.go`, `internal/config/env_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (used by every later task):
  ```go
  package config
  type Duration struct{ time.Duration }            // YAML "10m"
  type Config struct {
      DataDir, LogLevel, HTTPListen string
      Bots      map[string]Bot
      Targets   map[string]Target
      Admin     string
      Defaults  Defaults
      Mailboxes map[string]*Mailbox
      Templates Templates
      Rules     []Rule
      Recognize RecognizeOptions
  }
  type Bot struct{ Token string }
  type Target struct{ Bot string; ChatID int64 }
  type Defaults struct{ MaxAge, TTL, AlertTTL, PollInterval, AlertAfter Duration }
  type Mailbox struct {
      Name, DisplayName, Host string; Port int; Username string
      Auth Auth; Folders, Targets []string
      MaxAge, TTL, PollInterval Duration   // effective values after Load
      Templates Templates
  }
  type Auth struct{ Type, Password, ClientID, Tenant string }   // Type: "password" | "microsoft_oauth"
  type Templates struct{ Code, Link, Both string }
  type Rule struct{ Name, From, Subject, CodeRegex, LinkRegex string; Ignore bool }
  type RecognizeOptions struct{ ExtraCodeKeywords, ExtraLinkKeywords []string }
  func Load(path string) (*Config, error)                      // read + expand env + decode + defaults (no validation)
  func Parse(data []byte, lookup func(string) (string, bool)) (*Config, error)
  func (c *Config) SortedMailboxNames() []string
  ```

- [ ] **Step 1: Scaffold the module**

```bash
cd /home/suddy/verify-code-relay
go mod init verify-code-relay
go mod edit -go=1.26
cat > .gitignore <<'EOF'
/relay
/data/
.env
config.yaml
*.db
*.db-*
EOF
```

- [ ] **Step 2: Write the failing env-expansion test** — `internal/config/env_test.go`

```go
package config

import (
	"strings"
	"testing"
)

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestExpandString(t *testing.T) {
	env := lookupFrom(map[string]string{"A": "x", "QUOTE": `p"a#ss: }`})
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"${A}", "x"},
		{"pre-${A}-post", "pre-x-post"},
		{"$$100", "$100"},
		{"cost $5", "cost $5"},
		{"${QUOTE}", `p"a#ss: }`},
	}
	for _, c := range cases {
		got, err := expandString(c.in, env)
		if err != nil || got != c.want {
			t.Errorf("expandString(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestExpandStringMissing(t *testing.T) {
	_, err := expandString("${NOPE}", lookupFrom(nil))
	if err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("want error mentioning NOPE, got %v", err)
	}
}

func TestExpandStringUnterminated(t *testing.T) {
	if _, err := expandString("${A", lookupFrom(nil)); err == nil {
		t.Fatal("want error for unterminated ${")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./internal/config/ -run TestExpand -v`
Expected: FAIL — `undefined: expandString`.

- [ ] **Step 4: Implement `internal/config/env.go`**

```go
package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// expandString replaces ${NAME} with the looked-up value. "$$" yields a literal "$";
// a "$" not followed by "{" or "$" is kept as is.
func expandString(s string, lookup func(string) (string, bool)) (string, error) {
	if !strings.Contains(s, "$") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			i++
		case '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				return "", fmt.Errorf("未結束的 ${：%q", s)
			}
			name := s[i+2 : i+2+end]
			v, ok := lookup(name)
			if !ok {
				return "", fmt.Errorf("環境變數 %s 未設定", name)
			}
			b.WriteString(v)
			i += 2 + end
		default:
			b.WriteByte('$')
		}
	}
	return b.String(), nil
}

// expandNode walks the YAML tree and expands ${NAME} inside every scalar value
// (map keys are left alone). Expansion happens after parsing, so secrets that
// contain YAML syntax characters cannot corrupt the document.
func expandNode(n *yaml.Node, path string, lookup func(string) (string, bool)) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := expandNode(c, path, lookup); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			p := key
			if path != "" {
				p = path + "." + key
			}
			if err := expandNode(n.Content[i+1], p, lookup); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if err := expandNode(c, fmt.Sprintf("%s[%d]", path, i), lookup); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		v, err := expandString(n.Value, lookup)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if v != n.Value {
			n.Value = v
			if n.Style == 0 {
				n.Tag = "" // let yaml re-resolve plain scalars (e.g. chat_id: ${CHAT})
			}
		}
	}
	return nil
}
```

- [ ] **Step 5: Run env tests — expect PASS**

Run: `go get gopkg.in/yaml.v3@v3.0.1 && go test ./internal/config/ -run TestExpand -v`
Expected: PASS.

- [ ] **Step 6: Write the failing Load/Parse test** — `internal/config/config_test.go`

```go
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
	if o.Port != 1993 || o.MaxAge.Duration != 5*time.Minute || o.Auth.Tenant != "common" {
		t.Errorf("outlook wrong: %+v", o)
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
```

- [ ] **Step 7: Run it to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL — `undefined: Parse`.

- [ ] **Step 8: Implement `internal/config/config.go`**

```go
// Package config loads and validates the relay YAML configuration.
package config

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("第 %d 行：無效的時間長度 %q", n.Line, n.Value)
	}
	d.Duration = v
	return nil
}

type Config struct {
	DataDir    string              `yaml:"data_dir"`
	LogLevel   string              `yaml:"log_level"`
	HTTPListen string              `yaml:"http_listen"`
	Bots       map[string]Bot      `yaml:"bots"`
	Targets    map[string]Target   `yaml:"targets"`
	Admin      string              `yaml:"admin"`
	Defaults   Defaults            `yaml:"defaults"`
	Mailboxes  map[string]*Mailbox `yaml:"mailboxes"`
	Templates  Templates           `yaml:"templates"`
	Rules      []Rule              `yaml:"rules"`
	Recognize  RecognizeOptions    `yaml:"recognize"`
}

type Bot struct {
	Token string `yaml:"token"`
}

type Target struct {
	Bot    string `yaml:"bot"`
	ChatID int64  `yaml:"chat_id"`
}

type Defaults struct {
	MaxAge       Duration `yaml:"max_age"`
	TTL          Duration `yaml:"ttl"`
	AlertTTL     Duration `yaml:"alert_ttl"`
	PollInterval Duration `yaml:"poll_interval"`
	AlertAfter   Duration `yaml:"alert_after"`
}

type Mailbox struct {
	Name         string    `yaml:"-"`
	DisplayName  string    `yaml:"display_name"`
	Host         string    `yaml:"host"`
	Port         int       `yaml:"port"`
	Username     string    `yaml:"username"`
	Auth         Auth      `yaml:"auth"`
	Folders      []string  `yaml:"folders"`
	Targets      []string  `yaml:"targets"`
	MaxAge       Duration  `yaml:"max_age"`
	TTL          Duration  `yaml:"ttl"`
	PollInterval Duration  `yaml:"poll_interval"`
	Templates    Templates `yaml:"templates"`
}

type Auth struct {
	Type     string `yaml:"type"`
	Password string `yaml:"password"`
	ClientID string `yaml:"client_id"`
	Tenant   string `yaml:"tenant"`
}

type Templates struct {
	Code string `yaml:"code"`
	Link string `yaml:"link"`
	Both string `yaml:"both"`
}

type Rule struct {
	Name      string `yaml:"name"`
	From      string `yaml:"from"`
	Subject   string `yaml:"subject"`
	CodeRegex string `yaml:"code_regex"`
	LinkRegex string `yaml:"link_regex"`
	Ignore    bool   `yaml:"ignore"`
}

type RecognizeOptions struct {
	ExtraCodeKeywords []string `yaml:"extra_code_keywords"`
	ExtraLinkKeywords []string `yaml:"extra_link_keywords"`
}

// Load reads the file at path, expands ${ENV} from the process environment and applies defaults.
// It does not validate; call Validate for that.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("讀取設定檔：%w", err)
	}
	return Parse(data, os.LookupEnv)
}

func Parse(data []byte, lookup func(string) (string, bool)) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("解析 YAML：%w", err)
	}
	if err := expandNode(&root, "", lookup); err != nil {
		return nil, err
	}
	var c Config
	if len(root.Content) > 0 {
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		if err := enc.Encode(&root); err != nil {
			return nil, err
		}
		dec := yaml.NewDecoder(&buf)
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			return nil, fmt.Errorf("解析設定：%w", err)
		}
	}
	c.applyDefaults()
	return &c, nil
}

func orDur(v *Duration, def time.Duration) {
	if v.Duration == 0 {
		v.Duration = def
	}
}

func (c *Config) applyDefaults() {
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	orDur(&c.Defaults.MaxAge, 10*time.Minute)
	orDur(&c.Defaults.TTL, 15*time.Minute)
	orDur(&c.Defaults.AlertTTL, 24*time.Hour)
	orDur(&c.Defaults.PollInterval, 30*time.Second)
	orDur(&c.Defaults.AlertAfter, 10*time.Minute)
	for name, m := range c.Mailboxes {
		if m == nil {
			m = &Mailbox{}
			c.Mailboxes[name] = m
		}
		m.Name = name
		if m.DisplayName == "" {
			m.DisplayName = name
		}
		if m.Port == 0 {
			m.Port = 993
		}
		if len(m.Folders) == 0 {
			m.Folders = []string{"INBOX"}
		}
		if m.Auth.Type == "microsoft_oauth" && m.Auth.Tenant == "" {
			m.Auth.Tenant = "common"
		}
		orDur(&m.MaxAge, c.Defaults.MaxAge.Duration)
		orDur(&m.TTL, c.Defaults.TTL.Duration)
		orDur(&m.PollInterval, c.Defaults.PollInterval.Duration)
	}
}

func (c *Config) SortedMailboxNames() []string {
	names := make([]string, 0, len(c.Mailboxes))
	for n := range c.Mailboxes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
```

Note: re-encoding the expanded `yaml.Node` and decoding with `KnownFields(true)` is how we get both env expansion and unknown-field errors; `yaml.Node.Decode` does not support `KnownFields`.

- [ ] **Step 9: Run tests — expect PASS**

Run: `go test ./internal/config/ -v && go vet ./...`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add go.mod go.sum .gitignore internal/config
git commit -m "feat(config): load YAML config with env expansion and defaults

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: Config validation

**Files:**
- Create: `internal/config/validate.go`
- Test: `internal/config/validate_test.go`

**Interfaces:**
- Consumes: Task 1 types.
- Produces: `func (c *Config) Validate() error` — returns `nil` or an error whose message lists every problem, one per line, each prefixed with its YAML path (e.g. `mailboxes.outlook.targets[1]: 找不到目標 "work_chanel"`). Template parsing is **not** done here (render.New does it, Task 7); regexes **are** compiled here.

- [ ] **Step 1: Write the failing test** — `internal/config/validate_test.go`

```go
package config

import (
	"strings"
	"testing"
)

func validConfig() *Config {
	c, err := Parse([]byte(`
bots: { main: { token: "t" } }
targets: { me: { bot: main, chat_id: 1 } }
admin: me
mailboxes:
  gmail:
    host: imap.gmail.com
    username: me@gmail.com
    auth: { type: password, password: pw }
    targets: [me]
rules:
  - { name: gh, from: "*@github.com", code_regex: '(\d{6})' }
`), lookupFrom(nil))
	if err != nil {
		panic(err)
	}
	return c
}

func TestValidateOK(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
		want   string
	}{
		{"unknown target", func(c *Config) { c.Mailboxes["gmail"].Targets = []string{"me", "work_chanel"} }, `mailboxes.gmail.targets[1]: 找不到目標 "work_chanel"`},
		{"no targets", func(c *Config) { c.Mailboxes["gmail"].Targets = nil }, "mailboxes.gmail.targets: 至少需要一個目標"},
		{"unknown bot", func(c *Config) { c.Targets["me"] = Target{Bot: "nope", ChatID: 1} }, `targets.me.bot: 找不到 Bot "nope"`},
		{"zero chat", func(c *Config) { c.Targets["me"] = Target{Bot: "main"} }, "targets.me.chat_id: 不可為 0"},
		{"empty token", func(c *Config) { c.Bots["main"] = Bot{} }, "bots.main.token: 不可為空"},
		{"bad admin", func(c *Config) { c.Admin = "x" }, `admin: 找不到目標 "x"`},
		{"bad name", func(c *Config) { c.Bots["Main!"] = Bot{Token: "t"} }, `bots.Main!: 名稱只能包含`},
		{"no host", func(c *Config) { c.Mailboxes["gmail"].Host = "" }, "mailboxes.gmail.host: 不可為空"},
		{"no username", func(c *Config) { c.Mailboxes["gmail"].Username = "" }, "mailboxes.gmail.username: 不可為空"},
		{"bad auth type", func(c *Config) { c.Mailboxes["gmail"].Auth.Type = "magic" }, `mailboxes.gmail.auth.type: 未知的認證類型 "magic"`},
		{"no password", func(c *Config) { c.Mailboxes["gmail"].Auth.Password = "" }, "mailboxes.gmail.auth.password: 不可為空"},
		{"oauth no client", func(c *Config) { c.Mailboxes["gmail"].Auth = Auth{Type: "microsoft_oauth"} }, "mailboxes.gmail.auth.client_id: 不可為空"},
		{"bad regex", func(c *Config) { c.Rules[0].CodeRegex = "(" }, "rules[0].code_regex: "},
		{"rule no matcher", func(c *Config) { c.Rules[0].From = "" }, "rules[0]: 至少需要 from 或 subject"},
		{"rule no action", func(c *Config) { c.Rules[0].CodeRegex = "" }, "rules[0]: 需要 code_regex、link_regex 或 ignore: true 其中一項"},
		{"dup rule", func(c *Config) { c.Rules = append(c.Rules, c.Rules[0]) }, `rules[1].name: 名稱 "gh" 重複`},
		{"bad subject regex", func(c *Config) { c.Rules[0].Subject = "[" }, "rules[0].subject: "},
		{"empty folder", func(c *Config) { c.Mailboxes["gmail"].Folders = []string{""} }, "mailboxes.gmail.folders[0]: 不可為空"},
		{"bad log level", func(c *Config) { c.LogLevel = "loud" }, `log_level: 必須是 debug、info、warn 或 error`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(c)
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q in error, got %v", tc.want, err)
			}
		})
	}
}

func TestValidateNoMailboxes(t *testing.T) {
	c := validConfig()
	c.Mailboxes = nil
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "mailboxes: 至少需要一個信箱") {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/config/ -run TestValidate -v`
Expected: FAIL — `c.Validate undefined`.

- [ ] **Step 3: Implement `internal/config/validate.go`**

```go
package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var nameRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

type problems []string

func (p *problems) add(path, format string, args ...any) {
	*p = append(*p, path+": "+fmt.Sprintf(format, args...))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate checks references, required fields, names and regexes. Templates are
// validated by render.New.
func (c *Config) Validate() error {
	var p problems
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		p.add("log_level", "必須是 debug、info、warn 或 error")
	}
	checkName := func(path, name string) {
		if !nameRe.MatchString(name) {
			p.add(path, "名稱只能包含小寫英文字母、數字、_ 與 -")
		}
	}
	for _, name := range sortedKeys(c.Bots) {
		checkName("bots."+name, name)
		if c.Bots[name].Token == "" {
			p.add("bots."+name+".token", "不可為空")
		}
	}
	for _, name := range sortedKeys(c.Targets) {
		t := c.Targets[name]
		checkName("targets."+name, name)
		if _, ok := c.Bots[t.Bot]; !ok {
			p.add("targets."+name+".bot", "找不到 Bot %q", t.Bot)
		}
		if t.ChatID == 0 {
			p.add("targets."+name+".chat_id", "不可為 0")
		}
	}
	if c.Admin != "" {
		if _, ok := c.Targets[c.Admin]; !ok {
			p.add("admin", "找不到目標 %q", c.Admin)
		}
	}
	if len(c.Mailboxes) == 0 {
		p.add("mailboxes", "至少需要一個信箱")
	}
	for _, name := range sortedKeys(c.Mailboxes) {
		m := c.Mailboxes[name]
		base := "mailboxes." + name
		checkName(base, name)
		if m.Host == "" {
			p.add(base+".host", "不可為空")
		}
		if m.Username == "" {
			p.add(base+".username", "不可為空")
		}
		switch m.Auth.Type {
		case "password":
			if m.Auth.Password == "" {
				p.add(base+".auth.password", "不可為空")
			}
		case "microsoft_oauth":
			if m.Auth.ClientID == "" {
				p.add(base+".auth.client_id", "不可為空")
			}
		default:
			p.add(base+".auth.type", "未知的認證類型 %q（可用：password、microsoft_oauth）", m.Auth.Type)
		}
		for i, f := range m.Folders {
			if f == "" {
				p.add(fmt.Sprintf("%s.folders[%d]", base, i), "不可為空")
			}
		}
		if len(m.Targets) == 0 {
			p.add(base+".targets", "至少需要一個目標")
		}
		for i, t := range m.Targets {
			if _, ok := c.Targets[t]; !ok {
				p.add(fmt.Sprintf("%s.targets[%d]", base, i), "找不到目標 %q", t)
			}
		}
	}
	seen := map[string]bool{}
	for i, r := range c.Rules {
		base := fmt.Sprintf("rules[%d]", i)
		checkName(base+".name", r.Name)
		if seen[r.Name] {
			p.add(base+".name", "名稱 %q 重複", r.Name)
		}
		seen[r.Name] = true
		if r.From == "" && r.Subject == "" {
			p.add(base, "至少需要 from 或 subject")
		}
		if r.CodeRegex == "" && r.LinkRegex == "" && !r.Ignore {
			p.add(base, "需要 code_regex、link_regex 或 ignore: true 其中一項")
		}
		for field, expr := range map[string]string{"subject": r.Subject, "code_regex": r.CodeRegex, "link_regex": r.LinkRegex} {
			if expr == "" {
				continue
			}
			if _, err := regexp.Compile(expr); err != nil {
				p.add(base+"."+field, "正規表示式錯誤：%v", err)
			}
		}
	}
	if len(p) == 0 {
		return nil
	}
	sort.Strings(p)
	return errors.New(strings.Join(p, "\n"))
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/config/ -v && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(config): validate references, auth fields, names and rule regexes

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 3: SQLite store (schema, mailbox state, outbox, forward log, OAuth tokens)

**Files:**
- Create: `internal/store/store.go`, `internal/store/state.go`, `internal/store/outbox.go`, `internal/store/oauth.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: nothing project-internal.
- Produces:
  ```go
  package store
  func Open(path string) (*Store, error)        // creates file 0600, WAL, migrations
  func (s *Store) Close() error
  type MailboxState struct{ UIDValidity, LastUID uint32 }
  func (s *Store) GetMailboxState(ctx context.Context, mailbox, folder string) (MailboxState, bool, error)
  func (s *Store) SetMailboxState(ctx context.Context, mailbox, folder string, st MailboxState) error
  type LogEntry struct{ At time.Time; Mailbox, Folder string; UID uint32; FromAddr, Subject, Kind, Rule, Result string }
  type OutboxItem struct {
      ID, LogID int64            // LogID 0 = alert (NULL in DB)
      Target, Bot string; ChatID int64
      Text, ReplyMarkup string   // ReplyMarkup: JSON or ""
      PlainFallback bool
      CreatedAt, ExpiresAt, NextAttemptAt time.Time
      Attempts int
  }
  // RecordMessage atomically inserts the log entry, the outbox items (LogID filled in) and
  // sets mailbox_state(mailbox, folder) = st. Returns the new log id.
  func (s *Store) RecordMessage(ctx context.Context, e LogEntry, items []OutboxItem, st MailboxState) (int64, error)
  func (s *Store) Enqueue(ctx context.Context, it OutboxItem) (int64, error)            // alerts
  func (s *Store) DueOutbox(ctx context.Context, now time.Time, limit int) ([]OutboxItem, error) // head item per (bot,chat_id) that is due, ordered by id
  func (s *Store) DeleteExpired(ctx context.Context, now time.Time) ([]OutboxItem, error)          // deletes and returns items with expires_at < now
  func (s *Store) DeleteOutbox(ctx context.Context, id int64) error
  func (s *Store) RescheduleOutbox(ctx context.Context, id int64, attempts int, next time.Time, plainFallback bool) error
  func (s *Store) SetLogResult(ctx context.Context, logID int64, result string) error
  func (s *Store) PruneLog(ctx context.Context, before time.Time) (int64, error)
  type OAuthToken struct{ RefreshToken, AccessToken string; ExpiresAt time.Time }
  func (s *Store) GetOAuthToken(ctx context.Context, mailbox string) (OAuthToken, bool, error)
  func (s *Store) SaveOAuthToken(ctx context.Context, mailbox string, t OAuthToken) error
  ```
  Log `Kind` values: `code|link|both|none|ignored|skipped_age|skipped_size`. `Result` values: `pending|sent|failed|expired|not_forwarded`.

- [ ] **Step 1: Write the failing test** — `internal/store/store_test.go`

```go
package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var ctx = context.Background()

func TestOpenPermissionsAndReopen(t *testing.T) {
	p := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v", fi.Mode().Perm())
	}
	s2, err := Open(p) // migrations must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestMailboxState(t *testing.T) {
	s := openTest(t)
	if _, ok, err := s.GetMailboxState(ctx, "g", "INBOX"); ok || err != nil {
		t.Fatalf("want missing, got ok=%v err=%v", ok, err)
	}
	if err := s.SetMailboxState(ctx, "g", "INBOX", MailboxState{UIDValidity: 7, LastUID: 41}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMailboxState(ctx, "g", "INBOX", MailboxState{UIDValidity: 7, LastUID: 42}); err != nil {
		t.Fatal(err)
	}
	st, ok, err := s.GetMailboxState(ctx, "g", "INBOX")
	if !ok || err != nil || st != (MailboxState{7, 42}) {
		t.Fatalf("got %+v %v %v", st, ok, err)
	}
}

func item(target string, chat int64, now time.Time) OutboxItem {
	return OutboxItem{Target: target, Bot: "main", ChatID: chat, Text: "hi", ReplyMarkup: `{"inline_keyboard":[]}`,
		CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), NextAttemptAt: now}
}

func TestRecordMessageAtomicAndOutbox(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	e := LogEntry{At: now, Mailbox: "g", Folder: "INBOX", UID: 5, FromAddr: "a@b.c", Subject: "s", Kind: "code", Rule: "builtin:code", Result: "pending"}
	logID, err := s.RecordMessage(ctx, e, []OutboxItem{item("me", 1, now), item("fam", 2, now)}, MailboxState{UIDValidity: 3, LastUID: 5})
	if err != nil || logID == 0 {
		t.Fatalf("RecordMessage = %d, %v", logID, err)
	}
	st, _, _ := s.GetMailboxState(ctx, "g", "INBOX")
	if st.LastUID != 5 {
		t.Errorf("state not advanced: %+v", st)
	}
	due, err := s.DueOutbox(ctx, now, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %+v, %v", due, err)
	}
	if due[0].LogID != logID || due[0].ReplyMarkup == "" || !due[0].ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Errorf("item fields wrong: %+v", due[0])
	}
	if err := s.SetLogResult(ctx, logID, "sent"); err != nil {
		t.Fatal(err)
	}
}

func TestDueOutboxHeadPerChat(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	id1, _ := s.Enqueue(ctx, item("me", 1, now))
	id2, _ := s.Enqueue(ctx, item("me", 1, now))
	// head not due yet -> nothing for chat 1 (keeps per-chat order)
	if err := s.RescheduleOutbox(ctx, id1, 1, now.Add(10*time.Second), false); err != nil {
		t.Fatal(err)
	}
	due, _ := s.DueOutbox(ctx, now, 10)
	if len(due) != 0 {
		t.Fatalf("want none due, got %+v", due)
	}
	due, _ = s.DueOutbox(ctx, now.Add(10*time.Second), 10)
	if len(due) != 1 || due[0].ID != id1 || due[0].Attempts != 1 {
		t.Fatalf("want head id1, got %+v", due)
	}
	s.DeleteOutbox(ctx, id1)
	due, _ = s.DueOutbox(ctx, now, 10)
	if len(due) != 1 || due[0].ID != id2 {
		t.Fatalf("want id2, got %+v", due)
	}
	if err := s.RescheduleOutbox(ctx, id2, 2, now, true); err != nil {
		t.Fatal(err)
	}
	due, _ = s.DueOutbox(ctx, now, 10)
	if !due[0].PlainFallback {
		t.Error("plain fallback flag not persisted")
	}
}

func TestDeleteExpired(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	old := item("me", 1, now.Add(-time.Hour))
	old.ExpiresAt = now.Add(-time.Minute)
	s.Enqueue(ctx, old)
	s.Enqueue(ctx, item("me", 1, now))
	gone, err := s.DeleteExpired(ctx, now)
	if err != nil || len(gone) != 1 {
		t.Fatalf("DeleteExpired = %+v, %v", gone, err)
	}
	due, _ := s.DueOutbox(ctx, now, 10)
	if len(due) != 1 {
		t.Fatalf("remaining = %+v", due)
	}
}

func TestPruneLog(t *testing.T) {
	s := openTest(t)
	now := time.Unix(1_800_000_000, 0)
	s.RecordMessage(ctx, LogEntry{At: now.Add(-31 * 24 * time.Hour), Mailbox: "g", Folder: "INBOX", UID: 1, Kind: "none", Result: "not_forwarded"}, nil, MailboxState{1, 1})
	s.RecordMessage(ctx, LogEntry{At: now, Mailbox: "g", Folder: "INBOX", UID: 2, Kind: "none", Result: "not_forwarded"}, nil, MailboxState{1, 2})
	n, err := s.PruneLog(ctx, now.Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneLog = %d, %v", n, err)
	}
}

func TestOAuthToken(t *testing.T) {
	s := openTest(t)
	if _, ok, _ := s.GetOAuthToken(ctx, "o"); ok {
		t.Fatal("want missing")
	}
	tok := OAuthToken{RefreshToken: "r", AccessToken: "a", ExpiresAt: time.Unix(1_800_000_000, 0)}
	if err := s.SaveOAuthToken(ctx, "o", tok); err != nil {
		t.Fatal(err)
	}
	tok.RefreshToken = "r2"
	s.SaveOAuthToken(ctx, "o", tok)
	got, ok, err := s.GetOAuthToken(ctx, "o")
	if !ok || err != nil || got.RefreshToken != "r2" || !got.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Fatalf("got %+v %v %v", got, ok, err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/store/ -v`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 3: Implement `internal/store/store.go`**

```go
// Package store persists relay state in SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

var migrations = []string{
	`CREATE TABLE mailbox_state (
		mailbox      TEXT NOT NULL,
		folder       TEXT NOT NULL,
		uid_validity INTEGER NOT NULL,
		last_uid     INTEGER NOT NULL,
		updated_at   INTEGER NOT NULL,
		PRIMARY KEY (mailbox, folder)
	);
	CREATE TABLE oauth_tokens (
		mailbox       TEXT PRIMARY KEY,
		refresh_token TEXT NOT NULL,
		access_token  TEXT NOT NULL,
		expires_at    INTEGER NOT NULL,
		updated_at    INTEGER NOT NULL
	);
	CREATE TABLE outbox (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		log_id          INTEGER,
		target          TEXT NOT NULL,
		bot             TEXT NOT NULL,
		chat_id         INTEGER NOT NULL,
		text            TEXT NOT NULL,
		reply_markup    TEXT,
		plain_fallback  INTEGER NOT NULL DEFAULT 0,
		created_at      INTEGER NOT NULL,
		expires_at      INTEGER NOT NULL,
		attempts        INTEGER NOT NULL DEFAULT 0,
		next_attempt_at INTEGER NOT NULL
	);
	CREATE INDEX outbox_chat ON outbox (bot, chat_id, id);
	CREATE TABLE forward_log (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		at         INTEGER NOT NULL,
		mailbox    TEXT NOT NULL,
		folder     TEXT NOT NULL,
		uid        INTEGER NOT NULL,
		from_addr  TEXT NOT NULL,
		subject    TEXT NOT NULL,
		kind       TEXT NOT NULL,
		rule       TEXT,
		result     TEXT NOT NULL
	);
	CREATE INDEX forward_log_at ON forward_log (at);`,
}

// Open opens (creating if needed, mode 0600) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("建立資料庫檔案：%w", err)
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; avoids SQLITE_BUSY between our own goroutines
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("資料庫遷移 %d：%w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func unix(t time.Time) int64 { return t.Unix() }

func fromUnix(v int64) time.Time { return time.Unix(v, 0) }
```

- [ ] **Step 4: Implement `internal/store/state.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type MailboxState struct {
	UIDValidity uint32
	LastUID     uint32
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Store) GetMailboxState(ctx context.Context, mailbox, folder string) (MailboxState, bool, error) {
	var st MailboxState
	err := s.db.QueryRowContext(ctx,
		`SELECT uid_validity, last_uid FROM mailbox_state WHERE mailbox = ? AND folder = ?`,
		mailbox, folder).Scan(&st.UIDValidity, &st.LastUID)
	if errors.Is(err, sql.ErrNoRows) {
		return MailboxState{}, false, nil
	}
	if err != nil {
		return MailboxState{}, false, err
	}
	return st, true, nil
}

func (s *Store) SetMailboxState(ctx context.Context, mailbox, folder string, st MailboxState) error {
	return setState(ctx, s.db, mailbox, folder, st)
}

func setState(ctx context.Context, x execer, mailbox, folder string, st MailboxState) error {
	_, err := x.ExecContext(ctx, `
		INSERT INTO mailbox_state (mailbox, folder, uid_validity, last_uid, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (mailbox, folder) DO UPDATE SET uid_validity = excluded.uid_validity,
			last_uid = excluded.last_uid, updated_at = excluded.updated_at`,
		mailbox, folder, st.UIDValidity, st.LastUID, time.Now().Unix())
	return err
}
```

- [ ] **Step 5: Implement `internal/store/outbox.go`**

```go
package store

import (
	"context"
	"database/sql"
	"time"
)

type LogEntry struct {
	At       time.Time
	Mailbox  string
	Folder   string
	UID      uint32
	FromAddr string
	Subject  string
	Kind     string
	Rule     string
	Result   string
}

type OutboxItem struct {
	ID            int64
	LogID         int64 // 0 means alert (stored as NULL)
	Target        string
	Bot           string
	ChatID        int64
	Text          string
	ReplyMarkup   string
	PlainFallback bool
	CreatedAt     time.Time
	ExpiresAt     time.Time
	NextAttemptAt time.Time
	Attempts      int
}

const outboxCols = `id, COALESCE(log_id, 0), target, bot, chat_id, text, COALESCE(reply_markup, ''),
	plain_fallback, created_at, expires_at, next_attempt_at, attempts`

func (s *Store) RecordMessage(ctx context.Context, e LogEntry, items []OutboxItem, st MailboxState) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO forward_log (at, mailbox, folder, uid, from_addr, subject, kind, rule, result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		unix(e.At), e.Mailbox, e.Folder, e.UID, e.FromAddr, e.Subject, e.Kind, nullStr(e.Rule), e.Result)
	if err != nil {
		return 0, err
	}
	logID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		it.LogID = logID
		if _, err := insertOutbox(ctx, tx, it); err != nil {
			return 0, err
		}
	}
	if err := setState(ctx, tx, e.Mailbox, e.Folder, st); err != nil {
		return 0, err
	}
	return logID, tx.Commit()
}

func (s *Store) Enqueue(ctx context.Context, it OutboxItem) (int64, error) {
	return insertOutbox(ctx, s.db, it)
}

func insertOutbox(ctx context.Context, x execer, it OutboxItem) (int64, error) {
	var logID any
	if it.LogID != 0 {
		logID = it.LogID
	}
	res, err := x.ExecContext(ctx, `
		INSERT INTO outbox (log_id, target, bot, chat_id, text, reply_markup, plain_fallback,
			created_at, expires_at, attempts, next_attempt_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		logID, it.Target, it.Bot, it.ChatID, it.Text, nullStr(it.ReplyMarkup), it.PlainFallback,
		unix(it.CreatedAt), unix(it.ExpiresAt), it.Attempts, unix(it.NextAttemptAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DueOutbox returns, for each (bot, chat_id), its oldest item if that item is due.
// Later items of the same chat wait behind the head to preserve ordering.
func (s *Store) DueOutbox(ctx context.Context, now time.Time, limit int) ([]OutboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+outboxCols+` FROM outbox
		WHERE id IN (SELECT MIN(id) FROM outbox GROUP BY bot, chat_id) AND next_attempt_at <= ?
		ORDER BY id LIMIT ?`, unix(now), limit)
	if err != nil {
		return nil, err
	}
	return scanOutbox(rows)
}

func (s *Store) DeleteExpired(ctx context.Context, now time.Time) ([]OutboxItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+outboxCols+` FROM outbox WHERE expires_at < ? ORDER BY id`, unix(now))
	if err != nil {
		return nil, err
	}
	items, err := scanOutbox(rows)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM outbox WHERE expires_at < ?`, unix(now)); err != nil {
		return nil, err
	}
	return items, tx.Commit()
}

func (s *Store) DeleteOutbox(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM outbox WHERE id = ?`, id)
	return err
}

func (s *Store) RescheduleOutbox(ctx context.Context, id int64, attempts int, next time.Time, plainFallback bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE outbox SET attempts = ?, next_attempt_at = ?, plain_fallback = ? WHERE id = ?`,
		attempts, unix(next), plainFallback, id)
	return err
}

func (s *Store) SetLogResult(ctx context.Context, logID int64, result string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE forward_log SET result = ? WHERE id = ?`, result, logID)
	return err
}

func (s *Store) PruneLog(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM forward_log WHERE at < ?`, unix(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanOutbox(rows *sql.Rows) ([]OutboxItem, error) {
	defer rows.Close()
	var out []OutboxItem
	for rows.Next() {
		var it OutboxItem
		var created, expires, next int64
		if err := rows.Scan(&it.ID, &it.LogID, &it.Target, &it.Bot, &it.ChatID, &it.Text, &it.ReplyMarkup,
			&it.PlainFallback, &created, &expires, &next, &it.Attempts); err != nil {
			return nil, err
		}
		it.CreatedAt, it.ExpiresAt, it.NextAttemptAt = fromUnix(created), fromUnix(expires), fromUnix(next)
		out = append(out, it)
	}
	return out, rows.Err()
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
```

- [ ] **Step 6: Implement `internal/store/oauth.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type OAuthToken struct {
	RefreshToken string
	AccessToken  string
	ExpiresAt    time.Time
}

func (s *Store) GetOAuthToken(ctx context.Context, mailbox string) (OAuthToken, bool, error) {
	var t OAuthToken
	var exp int64
	err := s.db.QueryRowContext(ctx,
		`SELECT refresh_token, access_token, expires_at FROM oauth_tokens WHERE mailbox = ?`, mailbox).
		Scan(&t.RefreshToken, &t.AccessToken, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthToken{}, false, nil
	}
	if err != nil {
		return OAuthToken{}, false, err
	}
	t.ExpiresAt = fromUnix(exp)
	return t, true, nil
}

func (s *Store) SaveOAuthToken(ctx context.Context, mailbox string, t OAuthToken) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO oauth_tokens (mailbox, refresh_token, access_token, expires_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (mailbox) DO UPDATE SET refresh_token = excluded.refresh_token,
			access_token = excluded.access_token, expires_at = excluded.expires_at, updated_at = excluded.updated_at`,
		mailbox, t.RefreshToken, t.AccessToken, unix(t.ExpiresAt), time.Now().Unix())
	return err
}
```

- [ ] **Step 7: Run tests — expect PASS**

Run: `go get modernc.org/sqlite@v1.60.1 && go test ./internal/store/ -v && go vet ./...`
Expected: PASS. If `DueOutbox` returns items in the wrong order, check the `MIN(id) ... GROUP BY bot, chat_id` subquery first.

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat(store): SQLite schema, mailbox state, outbox, forward log, OAuth tokens

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 4: Mail parsing (MIME, charsets, HTML→text, links)

**Files:**
- Create: `internal/mailparse/parse.go`, `internal/mailparse/html.go`
- Test: `internal/mailparse/parse_test.go`, `internal/mailparse/html_test.go`

**Interfaces:**
- Consumes: nothing project-internal.
- Produces:
  ```go
  package mailparse
  type Address struct{ Name, Addr string }
  type Link struct{ URL, Text string }
  type Message struct {
      From    Address
      To      []Address   // To + Cc + Delivered-To, de-duplicated by lower-cased Addr
      Subject string
      Date    time.Time   // zero if header missing/unparseable (caller falls back to INTERNALDATE)
      Text    string
      Links   []Link
  }
  func Parse(raw []byte) (*Message, error)
  func HTMLToText(src string) (text string, links []Link)
  func ExtractURLs(text string) []string
  ```

- [ ] **Step 1: Write the failing HTML test** — `internal/mailparse/html_test.go`

```go
package mailparse

import (
	"reflect"
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	src := `<html><head><title>T</title><style>p{color:red}</style></head><body>
<p>Hello&nbsp;there</p><div>Your code:<br><b>123456</b></div>
<script>var x = 999999;</script>
<div style="display: none">hidden 777777</div>
<img src="https://t.example.com/pixel.gif">
<table><tr><td>a</td><td>b</td></tr><tr><td>c</td></tr></table>
<a href="https://example.com/verify?t=abc">Verify   email</a>
<a href="mailto:x@y.z">mail</a>
</body></html>`
	text, links := HTMLToText(src)
	for _, bad := range []string{"999999", "777777", "color:red"} {
		if strings.Contains(text, bad) {
			t.Errorf("text contains %q:\n%s", bad, text)
		}
	}
	for _, want := range []string{"Hello there", "Your code:\n123456", "Verify email"} {
		if !strings.Contains(text, want) {
			t.Errorf("text missing %q:\n%s", want, text)
		}
	}
	wantLinks := []Link{{URL: "https://example.com/verify?t=abc", Text: "Verify email"}}
	if !reflect.DeepEqual(links, wantLinks) {
		t.Errorf("links = %+v", links)
	}
}

func TestExtractURLs(t *testing.T) {
	got := ExtractURLs("go to https://a.com/x?y=1. or (https://b.com/z), 或 https://c.com/中文，好")
	want := []string{"https://a.com/x?y=1", "https://b.com/z", "https://c.com/中文"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/mailparse/ -v`
Expected: FAIL — `undefined: HTMLToText`.

- [ ] **Step 3: Implement `internal/mailparse/html.go`**

```go
package mailparse

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var blockAtoms = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Tr: true, atom.Li: true, atom.Table: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Ul: true, atom.Ol: true, atom.Blockquote: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true, atom.Hr: true, atom.Center: true,
}

var skipAtoms = map[atom.Atom]bool{
	atom.Head: true, atom.Style: true, atom.Script: true, atom.Img: true, atom.Noscript: true, atom.Template: true,
}

func hidden(n *html.Node) bool {
	for _, a := range n.Attr {
		if a.Key == "style" {
			s := strings.ToLower(strings.ReplaceAll(a.Val, " ", ""))
			if strings.Contains(s, "display:none") {
				return true
			}
		}
		if a.Key == "hidden" {
			return true
		}
	}
	return false
}

// HTMLToText renders HTML to plain text (block elements become newlines) and
// returns the http(s) <a href> links with their visible text.
func HTMLToText(src string) (string, []Link) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", nil
	}
	var b strings.Builder
	var links []Link
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skipAtoms[n.DataAtom] || hidden(n) {
				return
			}
			if n.DataAtom == atom.Br {
				b.WriteByte('\n')
				return
			}
			if blockAtoms[n.DataAtom] {
				b.WriteByte('\n')
			}
			if n.DataAtom == atom.Td || n.DataAtom == atom.Th {
				b.WriteByte(' ')
			}
			if n.DataAtom == atom.A {
				start := b.Len()
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				href := strings.TrimSpace(attr(n, "href"))
				if isHTTP(href) {
					links = append(links, Link{URL: href, Text: collapseSpaces(b.String()[start:])})
				}
				return
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(strings.ReplaceAll(n.Data, "\u00a0", " "))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockAtoms[n.DataAtom] {
			b.WriteByte('\n')
		}
	}
	walk(doc)
	return normalizeText(b.String()), links
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func isHTTP(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

var spaceRun = regexp.MustCompile(`[ \t\r\f\v]+`)

func collapseSpaces(s string) string {
	return strings.TrimSpace(spaceRun.ReplaceAllString(strings.ReplaceAll(s, "\n", " "), " "))
}

// normalizeText collapses spaces inside lines, trims lines and keeps at most one blank line in a row.
func normalizeText(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := true
	for _, l := range lines {
		l = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
		if l == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, l)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

var urlRe = regexp.MustCompile(`(?i)https?://[^\s<>"'()\[\]{}（）「」，。、]+`)

// ExtractURLs finds bare http(s) URLs in text, trimming trailing punctuation.
func ExtractURLs(text string) []string {
	var out []string
	for _, u := range urlRe.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:!?")
		if u != "" {
			out = append(out, u)
		}
	}
	return out
}
```

- [ ] **Step 4: Run HTML tests — expect PASS**

Run: `go get golang.org/x/net@latest && go test ./internal/mailparse/ -v`
Expected: PASS.

- [ ] **Step 5: Write the failing Parse test** — `internal/mailparse/parse_test.go`

The Big5/GB2312 cases build their bytes at runtime with `golang.org/x/text` (already a transitive dependency of go-message's charset package).

```go
package mailparse

import (
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

func TestParseMultipartAlternative(t *testing.T) {
	raw := crlf(`From: "GitHub" <noreply@github.com>
To: me+gh@gmail.com
Cc: Other <other@x.com>, me+gh@gmail.com
Delivered-To: me@gmail.com
Subject: =?UTF-8?B?W0dpdEh1Yl0g6amX6K2J56K8?=
Date: Mon, 19 Oct 2026 08:00:00 +0800
MIME-Version: 1.0
Content-Type: multipart/alternative; boundary="b1"

--b1
Content-Type: text/plain; charset=utf-8
Content-Transfer-Encoding: quoted-printable

Your code is 123456=0Ahttps://github.com/plain-link
--b1
Content-Type: text/html; charset=utf-8

<p>Your code is <b>123456</b></p><a href="https://github.com/verify?x=1">Verify</a>
--b1--
`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.From != (Address{Name: "GitHub", Addr: "noreply@github.com"}) {
		t.Errorf("from = %+v", m.From)
	}
	if m.Subject != "[GitHub] 驗證碼" {
		t.Errorf("subject = %q", m.Subject)
	}
	if len(m.To) != 3 || m.To[0].Addr != "me+gh@gmail.com" || m.To[2].Addr != "me@gmail.com" {
		t.Errorf("to = %+v", m.To)
	}
	if m.Date.IsZero() || m.Date.UTC().Hour() != 0 {
		t.Errorf("date = %v", m.Date)
	}
	if !strings.Contains(m.Text, "Your code is 123456\nhttps://github.com/plain-link") {
		t.Errorf("text should come from text/plain: %q", m.Text)
	}
	urls := map[string]string{}
	for _, l := range m.Links {
		urls[l.URL] = l.Text
	}
	if urls["https://github.com/verify?x=1"] != "Verify" {
		t.Errorf("html link missing: %+v", m.Links)
	}
	if _, ok := urls["https://github.com/plain-link"]; !ok {
		t.Errorf("plain link missing: %+v", m.Links)
	}
}

func TestParseHTMLOnlyBig5(t *testing.T) {
	body, _ := traditionalchinese.Big5.NewEncoder().String(`<div>您的驗證碼是：<b>482913</b></div>`)
	subj, _ := traditionalchinese.Big5.NewEncoder().String("登入驗證碼")
	raw := crlf("From: =?big5?B?" + base64.StdEncoding.EncodeToString([]byte(subj)) + "?= <svc@example.com.tw>\n" +
		"To: me@example.com\nSubject: =?big5?B?" + base64.StdEncoding.EncodeToString([]byte(subj)) + "?=\n" +
		"Content-Type: text/html; charset=big5\nContent-Transfer-Encoding: base64\n\n" +
		base64.StdEncoding.EncodeToString([]byte(body)) + "\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "登入驗證碼" || m.From.Name != "登入驗證碼" {
		t.Errorf("subject/from = %q / %q", m.Subject, m.From.Name)
	}
	if !strings.Contains(m.Text, "您的驗證碼是：\n482913") && !strings.Contains(m.Text, "您的驗證碼是：482913") {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParseGB2312Plain(t *testing.T) {
	gb, _ := simplifiedchinese.GBK.NewEncoder().String("您的验证码为 735102，5分钟内有效。")
	raw := crlf("From: svc@example.cn\nTo: me@example.com\nSubject: code\n" +
		"Content-Type: text/plain; charset=gb2312\nContent-Transfer-Encoding: base64\n\n" +
		base64.StdEncoding.EncodeToString([]byte(gb)) + "\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "您的验证码为 735102") {
		t.Errorf("text = %q", m.Text)
	}
}

func TestParseIgnoresAttachmentsAndBadDate(t *testing.T) {
	raw := crlf(`From: a@b.c
Subject: s
Date: not a date
Content-Type: multipart/mixed; boundary="m"

--m
Content-Type: text/plain

body text
--m
Content-Type: text/plain
Content-Disposition: attachment; filename="a.txt"

attachment 999999
--m--
`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Text, "999999") || m.Text != "body text" {
		t.Errorf("text = %q", m.Text)
	}
	if !m.Date.IsZero() {
		t.Errorf("date should be zero: %v", m.Date)
	}
}

func TestParseUnknownCharsetStillReturnsMessage(t *testing.T) {
	raw := crlf("From: a@b.c\nSubject: s\nContent-Type: text/plain; charset=x-unknown\n\ncode 1234\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Text, "1234") {
		t.Errorf("text = %q", m.Text)
	}
}
```

- [ ] **Step 6: Run to verify failure**

Run: `go get github.com/emersion/go-message@v0.18.2 golang.org/x/text && go test ./internal/mailparse/ -run TestParse -v`
Expected: FAIL — `undefined: Parse`.

- [ ] **Step 7: Implement `internal/mailparse/parse.go`**

```go
// Package mailparse turns raw RFC 5322 messages into a normalized Message.
package mailparse

import (
	"bytes"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers Big5, GB2312, GBK, GB18030, Shift_JIS, ISO-2022-JP, ...
	"github.com/emersion/go-message/mail"
)

type Address struct {
	Name string
	Addr string
}

type Link struct {
	URL  string
	Text string
}

type Message struct {
	From    Address
	To      []Address
	Subject string
	Date    time.Time
	Text    string
	Links   []Link
}

const maxPartBytes = 5 << 20

func Parse(raw []byte) (*Message, error) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
		return nil, err
	}
	m := &Message{}
	h := r.Header
	if s, err := h.Subject(); err == nil {
		m.Subject = s
	} else {
		m.Subject = h.Get("Subject")
	}
	if d, err := h.Date(); err == nil {
		m.Date = d
	}
	if from, err := h.AddressList("From"); err == nil && len(from) > 0 {
		m.From = Address{Name: from[0].Name, Addr: from[0].Address}
	}
	seen := map[string]bool{}
	for _, key := range []string{"To", "Cc", "Delivered-To"} {
		list, _ := h.AddressList(key)
		for _, a := range list {
			k := strings.ToLower(a.Address)
			if a.Address == "" || seen[k] {
				continue
			}
			seen[k] = true
			m.To = append(m.To, Address{Name: a.Name, Addr: a.Address})
		}
	}

	var plain, htmlBody string
	var havePlain, haveHTML bool
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
			break // keep what we have; a broken trailing part must not lose the message
		}
		if p == nil {
			continue
		}
		ih, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue // attachment
		}
		if disp, _, _ := ih.ContentDisposition(); disp == "attachment" {
			continue
		}
		ct, _, _ := ih.ContentType()
		if ct == "" {
			ct = "text/plain"
		}
		body, _ := io.ReadAll(io.LimitReader(p.Body, maxPartBytes))
		switch {
		case ct == "text/plain" && !havePlain:
			plain, havePlain = string(body), true
		case ct == "text/html" && !haveHTML:
			htmlBody, haveHTML = string(body), true
		}
	}

	var htmlText string
	var htmlLinks []Link
	if haveHTML {
		htmlText, htmlLinks = HTMLToText(htmlBody)
	}
	if havePlain {
		m.Text = normalizeText(plain)
	} else {
		m.Text = htmlText
	}
	m.Links = mergeLinks(htmlLinks, ExtractURLs(m.Text))
	return m, nil
}

// mergeLinks de-duplicates by URL, preferring entries that have visible text.
func mergeLinks(htmlLinks []Link, bare []string) []Link {
	idx := map[string]int{}
	var out []Link
	add := func(l Link) {
		if i, ok := idx[l.URL]; ok {
			if out[i].Text == "" && l.Text != "" {
				out[i].Text = l.Text
			}
			return
		}
		idx[l.URL] = len(out)
		out = append(out, l)
	}
	for _, l := range htmlLinks {
		add(l)
	}
	for _, u := range bare {
		add(Link{URL: u})
	}
	return out
}
```

- [ ] **Step 8: Run tests — expect PASS**

Run: `go get github.com/emersion/go-message@v0.18.2 golang.org/x/text && go test ./internal/mailparse/ -v && go vet ./...`
Expected: PASS. If `TestParseUnknownCharsetStillReturnsMessage` fails because `NextPart` returns `p == nil` with an unknown-charset error, keep the part anyway: go-message returns a non-nil `*Part` alongside `IsUnknownCharset` errors in v0.18 — confirm by printing `err` and adjust the `p == nil` branch only if needed.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum internal/mailparse
git commit -m "feat(mailparse): MIME decoding with charsets, HTML to text and link extraction

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 5: Recognizer core types + built-in verification-code rule

**Files:**
- Create: `internal/recognize/recognize.go`, `internal/recognize/text.go`, `internal/recognize/code.go`
- Test: `internal/recognize/code_test.go`, `internal/recognize/recognize_test.go`

**Interfaces:**
- Consumes: `mailparse.Message`, `mailparse.Address`, `mailparse.Link` (Task 4).
- Produces:
  ```go
  package recognize
  type Result struct{ Code, URL, Service, Rule string }
  type Trace struct{ Steps []string }
  func (t *Trace) Addf(format string, args ...any)
  type Stage interface{ Run(msg *mailparse.Message, tr *Trace) (res *Result, ignore bool) }
  type Recognizer interface{ Recognize(msg *mailparse.Message) (res *Result, ignore bool, tr *Trace) }
  type Chain struct{ /* unexported */ }
  func NewChain(stages ...Stage) *Chain                    // *Chain implements Recognizer
  func ServiceName(from mailparse.Address) string
  func MainLabel(host string) string                       // "accounts.google.com" -> "google", "shop.example.com.tw" -> "example"
  // code.go
  func newCodeFinder(extraKeywords []string) *codeFinder
  func (f *codeFinder) find(msg *mailparse.Message, tr *Trace) string
  ```
  When a stage returns `ignore == true`, `Chain.Recognize` returns `(res, true, tr)` where `res` may carry `Rule` (the ignoring rule's name) for logging. When a stage returns a non-nil result, the chain fills `Service` if empty.

- [ ] **Step 1: Write the failing core test** — `internal/recognize/recognize_test.go`

```go
package recognize

import (
	"testing"

	"verify-code-relay/internal/mailparse"
)

type fakeStage struct {
	res    *Result
	ignore bool
	called *int
}

func (f fakeStage) Run(*mailparse.Message, *Trace) (*Result, bool) {
	*f.called++
	return f.res, f.ignore
}

func TestChainOrderAndService(t *testing.T) {
	var a, b int
	c := NewChain(fakeStage{called: &a}, fakeStage{res: &Result{Code: "1"}, called: &b})
	res, ignore, tr := c.Recognize(&mailparse.Message{From: mailparse.Address{Addr: "noreply@accounts.google.com"}})
	if ignore || res == nil || res.Service != "google" || a != 1 || b != 1 || tr == nil {
		t.Fatalf("res=%+v ignore=%v a=%d b=%d", res, ignore, a, b)
	}
}

func TestChainIgnoreStops(t *testing.T) {
	var a, b int
	c := NewChain(fakeStage{res: &Result{Rule: "custom:x"}, ignore: true, called: &a}, fakeStage{res: &Result{Code: "1"}, called: &b})
	res, ignore, _ := c.Recognize(&mailparse.Message{})
	if !ignore || res.Rule != "custom:x" || b != 0 {
		t.Fatalf("res=%+v ignore=%v b=%d", res, ignore, b)
	}
}

func TestServiceName(t *testing.T) {
	cases := map[mailparse.Address]string{
		{Name: "GitHub", Addr: "noreply@github.com"}: "GitHub",
		{Addr: "no-reply@accounts.google.com"}:       "google",
		{Addr: "service@mail.shop.example.com.tw"}:   "example",
		{Addr: "a@bbc.co.uk"}:                        "bbc",
		{Addr: "weird"}:                              "weird",
	}
	for in, want := range cases {
		if got := ServiceName(in); got != want {
			t.Errorf("ServiceName(%+v) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/recognize/ -v`
Expected: FAIL — `undefined: NewChain`.

- [ ] **Step 3: Implement `internal/recognize/recognize.go`**

```go
// Package recognize decides whether a message carries a verification code or
// a verification link, and extracts it.
package recognize

import (
	"fmt"
	"strings"

	"verify-code-relay/internal/mailparse"
)

type Result struct {
	Code    string
	URL     string
	Service string
	Rule    string
}

// Trace is a human-readable log of the decision, printed by `relay test`. It may
// contain codes and URLs, so it must never be written to the service log.
type Trace struct{ Steps []string }

func (t *Trace) Addf(format string, args ...any) {
	if t != nil {
		t.Steps = append(t.Steps, fmt.Sprintf(format, args...))
	}
}

type Stage interface {
	Run(msg *mailparse.Message, tr *Trace) (res *Result, ignore bool)
}

type Recognizer interface {
	Recognize(msg *mailparse.Message) (res *Result, ignore bool, tr *Trace)
}

type Chain struct{ stages []Stage }

func NewChain(stages ...Stage) *Chain { return &Chain{stages: stages} }

func (c *Chain) Recognize(msg *mailparse.Message) (*Result, bool, *Trace) {
	tr := &Trace{}
	for _, s := range c.stages {
		res, ignore := s.Run(msg, tr)
		if ignore {
			return res, true, tr
		}
		if res != nil {
			if res.Service == "" {
				res.Service = ServiceName(msg.From)
			}
			return res, false, tr
		}
	}
	tr.Addf("結果：不符合任何規則")
	return nil, false, tr
}

func ServiceName(from mailparse.Address) string {
	if n := strings.TrimSpace(from.Name); n != "" {
		return n
	}
	at := strings.LastIndexByte(from.Addr, '@')
	if at < 0 {
		return from.Addr
	}
	return MainLabel(from.Addr[at+1:])
}

var secondLevel = map[string]bool{"com": true, "co": true, "net": true, "org": true, "gov": true, "edu": true, "ac": true, "or": true, "ne": true, "go": true}

// MainLabel returns the registrable name of a host: the label before the public
// suffix, approximating two-part suffixes like com.tw / co.uk.
func MainLabel(host string) string {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	labels := strings.Split(host, ".")
	n := len(labels)
	switch {
	case n >= 3 && len(labels[n-1]) == 2 && secondLevel[labels[n-2]]:
		return labels[n-3]
	case n >= 2:
		return labels[n-2]
	default:
		return host
	}
}
```

- [ ] **Step 4: Run core tests — expect PASS**

Run: `go test ./internal/recognize/ -run 'TestChain|TestServiceName' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing code-rule test** — `internal/recognize/code_test.go`

```go
package recognize

import (
	"testing"

	"verify-code-relay/internal/mailparse"
)

func TestCodeFinder(t *testing.T) {
	f := newCodeFinder([]string{"認証コード"})
	cases := []struct {
		name, subject, text, want string
	}{
		{"github", "[GitHub] Please verify your device", "Verification code: 123456\nIf you did not ...", "123456"},
		{"subject code", "839201 is your Facebook confirmation code", "Someone tried to log in.", "839201"},
		{"zh-tw", "登入通知", "您的驗證碼為 482913，請於 5 分鐘內輸入。\n客服專線 0800-123-456", "482913"},
		{"zh-cn", "验证码", "【某服务】您的验证码是735102，10分钟内有效。", "735102"},
		{"ja extra keyword", "ログイン", "認証コード: 551204", "551204"},
		{"alnum", "Your login code", "Use this code: K7Q2ZP to sign in.", "K7Q2ZP"},
		{"split", "Security code", "Your security code is 123 456.", "123456"},
		{"own line beats earlier noise", "Sign-in code", "Your code is below.\nOrder #20261019\n\n839201\n", "839201"},
		{"no keyword", "Weekly digest", "Room 1234 has 5678 items.", ""},
		{"amount excluded", "Payment verification code", "Amount NT$ 2500 charged. Verification code: 9921", "9921"},
		{"year excluded", "Verification code", "Date: Oct 19, 2026\nThat's all.", ""},
		{"url excluded", "Verification code", "Visit https://ex.com/a/123456 now", ""},
		{"phone excluded", "OTP", "Call +1 415 555 0100 for help", ""},
		{"time excluded", "Login code", "Valid until 12:34 only", ""},
		{"too far", "Your verification code", longPad(300) + "123456", ""},
		{"word boundary", "Hotpot recipes", "Serves 4 people, 1234 calories", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Trace{}
			got := f.find(&mailparse.Message{Subject: c.subject, Text: c.text}, tr)
			if got != c.want {
				t.Errorf("got %q want %q\ntrace:\n%v", got, c.want, tr.Steps)
			}
		})
	}
}

func longPad(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = '.'
	}
	return string(b)
}
```

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/recognize/ -run TestCodeFinder -v`
Expected: FAIL — `undefined: newCodeFinder`.

- [ ] **Step 7: Implement `internal/recognize/text.go`** (shared rune helpers)

```go
package recognize

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

type span struct{ start, end int } // rune offsets, end exclusive

func lowerRunes(rs []rune) []rune {
	out := make([]rune, len(rs))
	for i, r := range rs {
		out[i] = unicode.ToLower(r)
	}
	return out
}

func isASCIILetter(r rune) bool { return r < utf8.RuneSelf && (r|0x20) >= 'a' && (r|0x20) <= 'z' }

// findKeywords returns every occurrence of every keyword in lower. Keywords that
// start/end with an ASCII letter must not touch another ASCII letter (so "otp"
// does not match "hotpot").
func findKeywords(lower []rune, keywords [][]rune) []span {
	var out []span
	for _, kw := range keywords {
		if len(kw) == 0 {
			continue
		}
		for i := 0; i+len(kw) <= len(lower); i++ {
			if !runesEqual(lower[i:i+len(kw)], kw) {
				continue
			}
			if isASCIILetter(kw[0]) && i > 0 && isASCIILetter(lower[i-1]) {
				continue
			}
			end := i + len(kw)
			if isASCIILetter(kw[len(kw)-1]) && end < len(lower) && isASCIILetter(lower[end]) {
				continue
			}
			out = append(out, span{i, end})
		}
	}
	return out
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func toRuneKeywords(words []string) [][]rune {
	out := make([][]rune, 0, len(words))
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" {
			out = append(out, []rune(w))
		}
	}
	return out
}

// byteToRune maps every byte offset of s (0..len(s)) to its rune offset.
func byteToRune(s string) []int {
	m := make([]int, len(s)+1)
	r := 0
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:]) // invalid bytes: size 1, same as []rune(s)
		for j := 0; j < size; j++ {
			m[i+j] = r
		}
		i += size
		r++
	}
	m[len(s)] = r
	return m
}

func regexpSpans(re *regexp.Regexp, s string, b2r []int) []span {
	var out []span
	for _, m := range re.FindAllStringIndex(s, -1) {
		out = append(out, span{b2r[m[0]], b2r[m[1]]})
	}
	return out
}

func overlaps(a span, list []span) bool {
	for _, b := range list {
		if a.start < b.end && b.start < a.end {
			return true
		}
	}
	return false
}

// lineAt returns the rune span of the line containing position pos.
func lineAt(rs []rune, pos int) span {
	s, e := pos, pos
	for s > 0 && rs[s-1] != '\n' {
		s--
	}
	for e < len(rs) && rs[e] != '\n' {
		e++
	}
	return span{s, e}
}
```

- [ ] **Step 8: Implement `internal/recognize/code.go`**

```go
package recognize

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"verify-code-relay/internal/mailparse"
)

var defaultCodeKeywords = []string{
	"驗證碼", "验证码", "校驗碼", "校验码", "動態碼", "动态码", "確認碼", "确认码", "安全碼", "安全码",
	"認證碼", "认证码", "一次性密碼", "一次性密码", "登入碼", "登录码",
	"verification code", "security code", "confirmation code", "authentication code", "login code",
	"sign-in code", "one-time code", "one-time password", "otp", "passcode", "2fa", "two-factor",
	"your code", "code is",
	"認証コード", "確認コード", "ワンタイムパスワード",
}

var (
	digitsRe   = regexp.MustCompile(`\b\d{4,8}\b`)
	splitRe    = regexp.MustCompile(`\b\d{3}[ -]\d{3}\b`)
	alnumRe    = regexp.MustCompile(`\b[A-Z0-9]{4,8}\b`)
	urlLikeRe  = regexp.MustCompile(`(?i)(https?://|www\.)\S+`)
	emailRe    = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
	monthRe    = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*\b`)
	dateNumRe  = regexp.MustCompile(`\d{4}[/-]\d{1,2}|\d{1,2}[/-]\d{1,2}[/-]\d{2,4}`)
	currencies = []string{"$", "¥", "￥", "€", "£", "twd", "usd", "rmb", "cny"}
	orderWords = []string{"訂單", "订单", "order", "invoice", "#"}
)

const (
	codeMaxDistance = 200
	codeThreshold   = 40.0
)

type codeFinder struct{ keywords [][]rune }

func newCodeFinder(extra []string) *codeFinder {
	return &codeFinder{keywords: toRuneKeywords(append(append([]string{}, defaultCodeKeywords...), extra...))}
}

type codeCand struct {
	span
	raw   string
	value string
}

func (f *codeFinder) find(msg *mailparse.Message, tr *Trace) string {
	full := msg.Subject + "\n" + msg.Text
	rs := []rune(full)
	lower := lowerRunes(rs)
	subjLen := len([]rune(msg.Subject))
	kws := findKeywords(lower, f.keywords)
	if len(kws) == 0 {
		tr.Addf("驗證碼：沒有找到驗證碼關鍵字")
		return ""
	}
	for _, k := range kws {
		tr.Addf("驗證碼：關鍵字 %q 位於 %d", string(rs[k.start:k.end]), k.start)
	}
	b2r := byteToRune(full)
	blocked := append(regexpSpans(urlLikeRe, full, b2r), regexpSpans(emailRe, full, b2r)...)

	var cands []codeCand
	for _, sp := range regexpSpans(digitsRe, full, b2r) {
		raw := string(rs[sp.start:sp.end])
		cands = append(cands, codeCand{sp, raw, raw})
	}
	for _, sp := range regexpSpans(splitRe, full, b2r) {
		raw := string(rs[sp.start:sp.end])
		cands = append(cands, codeCand{sp, raw, strings.NewReplacer(" ", "", "-", "").Replace(raw)})
	}
	for _, sp := range regexpSpans(alnumRe, full, b2r) {
		raw := string(rs[sp.start:sp.end])
		if strings.IndexFunc(raw, unicode.IsDigit) >= 0 && strings.IndexFunc(raw, unicode.IsUpper) >= 0 {
			cands = append(cands, codeCand{sp, raw, raw})
		}
	}
	sortCands(cands)

	best, bestScore := "", -1e9
	for _, c := range cands {
		if reason := excludeReason(c, rs, lower, blocked); reason != "" {
			tr.Addf("  候選 %q：排除（%s）", c.raw, reason)
			continue
		}
		d, ok := keywordDistance(c.span, kws)
		if !ok {
			tr.Addf("  候選 %q：離關鍵字超過 %d 字元", c.raw, codeMaxDistance)
			continue
		}
		score := 100 - float64(d)/2
		if c.start < subjLen {
			score += 30
		}
		if ln := lineAt(rs, c.start); strings.TrimSpace(string(rs[ln.start:ln.end])) == c.raw {
			score += 20
		}
		if len(c.value) == 6 && isAllDigits(c.value) {
			score += 10
		}
		tr.Addf("  候選 %q：分數 %.1f", c.raw, score)
		if score > bestScore {
			best, bestScore = c.value, score
		}
	}
	if best == "" || bestScore < codeThreshold {
		tr.Addf("驗證碼：沒有分數達到門檻 %.0f 的候選", codeThreshold)
		return ""
	}
	tr.Addf("驗證碼：選擇 %q（分數 %.1f）", best, bestScore)
	return best
}

func sortCands(c []codeCand) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].start < c[j-1].start; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

// keywordDistance returns the weighted distance to the nearest keyword; candidates
// before a keyword count double. ok is false when every keyword is >200 runes away.
func keywordDistance(c span, kws []span) (int, bool) {
	best, ok := 0, false
	for _, k := range kws {
		var raw, d int
		switch {
		case c.start >= k.end:
			raw = c.start - k.end
			d = raw
		case c.end <= k.start:
			raw = k.start - c.end
			d = 2 * raw
		default:
			raw, d = 0, 0
		}
		if raw > codeMaxDistance {
			continue
		}
		if !ok || d < best {
			best, ok = d, true
		}
	}
	return best, ok
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func isPhoneRune(r rune) bool {
	return (r >= '0' && r <= '9') || r == ' ' || r == '-' || r == '(' || r == ')'
}

func excludeReason(c codeCand, rs, lower []rune, blocked []span) string {
	if overlaps(c.span, blocked) {
		return "位於網址或 email 中"
	}
	before := strings.ReplaceAll(string(lower[max(0, c.start-5):c.start]), " ", "")
	for _, cur := range currencies {
		if strings.Contains(before, cur) {
			return "金額"
		}
	}
	after := c.end
	for after < len(rs) && rs[after] == ' ' {
		after++
	}
	if after < len(rs) && rs[after] == '元' {
		return "金額"
	}
	if c.start >= 2 && rs[c.start-1] == ':' && unicode.IsDigit(rs[c.start-2]) ||
		c.end+1 < len(rs) && rs[c.end] == ':' && unicode.IsDigit(rs[c.end+1]) {
		return "時間"
	}
	if len(c.value) == 4 && isAllDigits(c.value) {
		if y, _ := strconv.Atoi(c.value); y >= 1900 && y <= 2099 {
			ln := lineAt(lower, c.start)
			line := string(lower[ln.start:ln.end])
			if strings.Contains(line, "年") || monthRe.MatchString(line) || dateNumRe.MatchString(line) {
				return "日期中的年份"
			}
		}
	}
	l, r := c.start, c.end
	for l > 0 && isPhoneRune(rs[l-1]) {
		l--
	}
	for r < len(rs) && isPhoneRune(rs[r]) {
		r++
	}
	if l > 0 && rs[l-1] == '+' {
		return "電話號碼"
	}
	run := string(rs[l:r])
	digits := 0
	for _, ch := range run {
		if ch >= '0' && ch <= '9' {
			digits++
		}
	}
	if digits > 8 && strings.ContainsAny(run, "-(") {
		return "電話號碼"
	}
	window := string(lower[max(0, c.start-10):c.start])
	for _, w := range orderWords {
		if strings.Contains(window, w) {
			return "訂單編號"
		}
	}
	return ""
}
```

- [ ] **Step 9: Run tests — expect PASS**

Run: `go test ./internal/recognize/ -v && go vet ./...`
Expected: PASS. If a case fails, read the printed trace before changing anything. Allowed adjustments: weights/threshold in `code.go` (spec §5.2 says "初始值，以測試樣本調整"); never change a test's expected value to match the code.

- [ ] **Step 10: Commit**

```bash
git add internal/recognize
git commit -m "feat(recognize): chain, service name and built-in verification-code rule

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: Built-in link rule, custom rules, `recognize.New`

**Files:**
- Create: `internal/recognize/link.go`, `internal/recognize/custom.go`, `internal/recognize/builtin.go`
- Test: `internal/recognize/link_test.go`, `internal/recognize/custom_test.go`

**Interfaces:**
- Consumes: Task 5 (`Stage`, `Result`, `Trace`, `newCodeFinder`, `MainLabel`, rune helpers), `config.Rule`, `config.RecognizeOptions` (Task 1).
- Produces:
  ```go
  func newLinkFinder(extraKeywords []string) *linkFinder
  func (f *linkFinder) find(msg *mailparse.Message, tr *Trace) string
  type Builtin struct{ /* code + link finders */ }
  func NewBuiltin(opts config.RecognizeOptions) *Builtin       // Stage; Rule = "builtin:code" | "builtin:link" | "builtin:code+link"
  type Custom struct{ /* compiled rules */ }
  func NewCustom(rules []config.Rule) (*Custom, error)          // Stage; Rule = "custom:<name>"
  func New(rules []config.Rule, opts config.RecognizeOptions) (*Chain, error) // Custom then Builtin
  ```

- [ ] **Step 1: Write the failing link test** — `internal/recognize/link_test.go`

```go
package recognize

import (
	"testing"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

func configOpts(code, link []string) config.RecognizeOptions {
	return config.RecognizeOptions{ExtraCodeKeywords: code, ExtraLinkKeywords: link}
}

func TestLinkFinder(t *testing.T) {
	f := newLinkFinder(nil)
	from := mailparse.Address{Addr: "no-reply@accounts.example.com"}
	cases := []struct {
		name    string
		subject string
		text    string
		links   []mailparse.Link
		want    string
	}{
		{"anchor text", "Confirm your email", "Click below", []mailparse.Link{
			{URL: "https://example.com/help", Text: "Help center"},
			{URL: "https://click.example.com/ls/abc", Text: "Confirm email address"},
			{URL: "https://example.com/unsubscribe?u=1", Text: "Unsubscribe"},
		}, "https://click.example.com/ls/abc"},
		{"magic link url", "Sign in to Example", "Use the link", []mailparse.Link{
			{URL: "https://example.com/"},
			{URL: "https://example.com/auth/magic?token=Zx8_kq2LmN0pQr7sTuVwXy-1"},
		}, "https://example.com/auth/magic?token=Zx8_kq2LmN0pQr7sTuVwXy-1"},
		{"reset password zh", "重設密碼", "請點擊下方連結重設密碼", []mailparse.Link{
			{URL: "https://www.facebook.com/example", Text: "Facebook"},
			{URL: "https://example.com/account/reset?code=123", Text: "重設密碼"},
		}, "https://example.com/account/reset?code=123"},
		{"no keyword", "Newsletter", "Our news", []mailparse.Link{{URL: "https://example.com/verify", Text: "Verify"}}, ""},
		{"below threshold", "Please confirm", "text", []mailparse.Link{{URL: "https://example.com/blog/post", Text: "Read more"}}, ""},
		{"image excluded", "Verify", "x", []mailparse.Link{{URL: "https://example.com/verify.png", Text: "Verify"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Trace{}
			got := f.find(&mailparse.Message{From: from, Subject: c.subject, Text: c.text, Links: c.links}, tr)
			if got != c.want {
				t.Errorf("got %q want %q\ntrace: %v", got, c.want, tr.Steps)
			}
		})
	}
}

func TestBuiltinBoth(t *testing.T) {
	b := NewBuiltin(configOpts(nil, nil))
	msg := &mailparse.Message{
		From:    mailparse.Address{Addr: "noreply@example.com"},
		Subject: "Verify your email",
		Text:    "Your verification code is 482913 or click the link.",
		Links:   []mailparse.Link{{URL: "https://example.com/verify?t=abc", Text: "Verify email"}},
	}
	res, ignore := b.Run(msg, &Trace{})
	if ignore || res == nil || res.Code != "482913" || res.URL != "https://example.com/verify?t=abc" || res.Rule != "builtin:code+link" {
		t.Fatalf("res = %+v", res)
	}
}
```

- [ ] **Step 2: Write the failing custom-rule test** — `internal/recognize/custom_test.go`

```go
package recognize

import (
	"testing"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

func TestCustomRules(t *testing.T) {
	c, err := NewCustom([]config.Rule{
		{Name: "quiet", From: "*@news.example.com", Ignore: true},
		{Name: "gh", From: "*@GitHub.com", Subject: "(?i)verify", CodeRegex: `code:\s*(\d{6})`},
		{Name: "lnk", From: "noreply@example.org", LinkRegex: `https://example\.org/v\?t=\S+`},
		{Name: "nomatch", Subject: "zzz", CodeRegex: `(\d+)`},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		msg        mailparse.Message
		wantCode   string
		wantURL    string
		wantRule   string
		wantIgnore bool
		wantNil    bool
	}{
		{name: "ignore", msg: mailparse.Message{From: mailparse.Address{Addr: "promo@news.example.com"}}, wantIgnore: true, wantRule: "custom:quiet"},
		{name: "code", msg: mailparse.Message{From: mailparse.Address{Addr: "noreply@github.com"}, Subject: "Please verify", Text: "code: 123456"}, wantCode: "123456", wantRule: "custom:gh"},
		{name: "subject mismatch falls through", msg: mailparse.Message{From: mailparse.Address{Addr: "noreply@github.com"}, Subject: "Hello", Text: "code: 123456"}, wantNil: true},
		{name: "applies but no match falls through", msg: mailparse.Message{From: mailparse.Address{Addr: "noreply@github.com"}, Subject: "verify", Text: "nothing"}, wantNil: true},
		{name: "link", msg: mailparse.Message{From: mailparse.Address{Addr: "NoReply@Example.org"}, Links: []mailparse.Link{{URL: "https://x.com"}, {URL: "https://example.org/v?t=abc"}}}, wantURL: "https://example.org/v?t=abc", wantRule: "custom:lnk"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, ignore := c.Run(&tc.msg, &Trace{})
			if tc.wantNil {
				if res != nil || ignore {
					t.Fatalf("want fallthrough, got %+v %v", res, ignore)
				}
				return
			}
			if res == nil || ignore != tc.wantIgnore || res.Code != tc.wantCode || res.URL != tc.wantURL || res.Rule != tc.wantRule {
				t.Fatalf("got %+v ignore=%v", res, ignore)
			}
		})
	}
}

func TestNewChainCustomBeforeBuiltin(t *testing.T) {
	ch, err := New([]config.Rule{{Name: "x", From: "*@example.com", CodeRegex: `ID (\d{4})`}}, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, _, _ := ch.Recognize(&mailparse.Message{From: mailparse.Address{Addr: "a@example.com"}, Subject: "Your verification code", Text: "ID 4321 code 999999"})
	if res == nil || res.Code != "4321" || res.Rule != "custom:x" {
		t.Fatalf("res = %+v", res)
	}
}

func TestNewCustomBadRegex(t *testing.T) {
	if _, err := NewCustom([]config.Rule{{Name: "x", Subject: "(", Ignore: true}}); err == nil {
		t.Fatal("want error")
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/recognize/ -v`
Expected: FAIL — `undefined: newLinkFinder`, `NewCustom`, `NewBuiltin`, `New`.

- [ ] **Step 4: Implement `internal/recognize/link.go`**

```go
package recognize

import (
	"net/url"
	"regexp"
	"strings"

	"verify-code-relay/internal/mailparse"
)

var defaultLinkKeywords = []string{
	"verify", "verification", "confirm", "activate", "activation", "validate", "sign in", "log in",
	"login link", "magic link", "reset password", "reset your password", "password reset",
	"驗證", "验证", "確認", "确认", "啟用", "启用", "激活", "登入", "登录",
	"重設密碼", "重设密码", "重置密碼", "重置密码",
}

var (
	linkExcludeWords = []string{"unsubscribe", "退訂", "退订", "取消訂閱", "取消订阅", "preferences", "privacy",
		"隱私", "隐私", "terms", "條款", "条款", "help", "support", "說明", "说明"}
	socialHosts  = []string{"facebook.com", "twitter.com", "x.com", "instagram.com", "linkedin.com", "youtube.com"}
	urlHintWords = []string{"verify", "confirm", "activate", "validate", "magic", "reset", "token=", "code=", "login"}
	imageExtRe   = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|svg|bmp|ico)$`)
	tokenValueRe = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
)

const linkThreshold = 50

type linkFinder struct{ keywords []string }

func newLinkFinder(extra []string) *linkFinder {
	var kws []string
	for _, k := range append(append([]string{}, defaultLinkKeywords...), extra...) {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			kws = append(kws, k)
		}
	}
	return &linkFinder{keywords: kws}
}

func (f *linkFinder) containsKeyword(s string) (string, bool) {
	for _, k := range f.keywords {
		if strings.Contains(s, k) {
			return k, true
		}
	}
	return "", false
}

func (f *linkFinder) find(msg *mailparse.Message, tr *Trace) string {
	kw, ok := f.containsKeyword(strings.ToLower(msg.Subject + "\n" + msg.Text))
	if !ok {
		tr.Addf("連結：沒有找到連結關鍵字")
		return ""
	}
	tr.Addf("連結：關鍵字 %q", kw)
	senderLabel := ""
	if at := strings.LastIndexByte(msg.From.Addr, '@'); at >= 0 {
		senderLabel = MainLabel(msg.From.Addr[at+1:])
	}
	best, bestScore := "", -1
	for _, l := range msg.Links {
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		lu, lt := strings.ToLower(l.URL), strings.ToLower(l.Text)
		if reason := linkExcludeReason(u, lu, lt); reason != "" {
			tr.Addf("  連結 %s：排除（%s）", l.URL, reason)
			continue
		}
		score := 0
		if _, ok := f.containsKeyword(lt); ok {
			score += 50
		}
		for _, h := range urlHintWords {
			if strings.Contains(lu, h) {
				score += 30
				break
			}
		}
		for _, vals := range u.Query() {
			if len(vals) > 0 && tokenValueRe.MatchString(vals[0]) {
				score += 15
				break
			}
		}
		if senderLabel != "" && MainLabel(u.Hostname()) == senderLabel {
			score += 10
		}
		tr.Addf("  連結 %s（文字 %q）：分數 %d", l.URL, l.Text, score)
		if score > bestScore {
			best, bestScore = l.URL, score
		}
	}
	if best == "" || bestScore < linkThreshold {
		tr.Addf("連結：沒有分數達到門檻 %d 的連結", linkThreshold)
		return ""
	}
	tr.Addf("連結：選擇 %s（分數 %d）", best, bestScore)
	return best
}

func linkExcludeReason(u *url.URL, lu, lt string) string {
	for _, w := range linkExcludeWords {
		if strings.Contains(lu, w) || strings.Contains(lt, w) {
			return "含 " + w
		}
	}
	host := strings.ToLower(u.Hostname())
	for _, s := range socialHosts {
		if host == s || strings.HasSuffix(host, "."+s) {
			return "社群網站"
		}
	}
	if imageExtRe.MatchString(u.Path) {
		return "圖片"
	}
	return ""
}
```

- [ ] **Step 5: Implement `internal/recognize/builtin.go`**

```go
package recognize

import (
	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

type Builtin struct {
	code *codeFinder
	link *linkFinder
}

func NewBuiltin(opts config.RecognizeOptions) *Builtin {
	return &Builtin{code: newCodeFinder(opts.ExtraCodeKeywords), link: newLinkFinder(opts.ExtraLinkKeywords)}
}

func (b *Builtin) Run(msg *mailparse.Message, tr *Trace) (*Result, bool) {
	code := b.code.find(msg, tr)
	link := b.link.find(msg, tr)
	switch {
	case code != "" && link != "":
		return &Result{Code: code, URL: link, Rule: "builtin:code+link"}, false
	case code != "":
		return &Result{Code: code, Rule: "builtin:code"}, false
	case link != "":
		return &Result{URL: link, Rule: "builtin:link"}, false
	}
	return nil, false
}
```

- [ ] **Step 6: Implement `internal/recognize/custom.go`**

```go
package recognize

import (
	"fmt"
	"regexp"
	"strings"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

type customRule struct {
	name           string
	from, subject  *regexp.Regexp
	codeRe, linkRe *regexp.Regexp
	ignore         bool
}

type Custom struct{ rules []customRule }

// globToRegexp converts a case-insensitive address glob (* and ?) to an anchored regexp.
func globToRegexp(glob string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("(?i)^")
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

func NewCustom(rules []config.Rule) (*Custom, error) {
	c := &Custom{}
	for i, r := range rules {
		cr := customRule{name: r.Name, ignore: r.Ignore}
		var err error
		compile := func(field, expr string) *regexp.Regexp {
			if expr == "" || err != nil {
				return nil
			}
			var re *regexp.Regexp
			if re, err = regexp.Compile(expr); err != nil {
				err = fmt.Errorf("rules[%d].%s: %w", i, field, err)
			}
			return re
		}
		if r.From != "" {
			if cr.from, err = globToRegexp(r.From); err != nil {
				return nil, fmt.Errorf("rules[%d].from: %w", i, err)
			}
		}
		cr.subject = compile("subject", r.Subject)
		cr.codeRe = compile("code_regex", r.CodeRegex)
		cr.linkRe = compile("link_regex", r.LinkRegex)
		if err != nil {
			return nil, err
		}
		c.rules = append(c.rules, cr)
	}
	return c, nil
}

func (c *Custom) Run(msg *mailparse.Message, tr *Trace) (*Result, bool) {
	for _, r := range c.rules {
		if r.from != nil && !r.from.MatchString(msg.From.Addr) {
			continue
		}
		if r.subject != nil && !r.subject.MatchString(msg.Subject) {
			continue
		}
		rule := "custom:" + r.name
		if r.ignore {
			tr.Addf("自訂規則 %s：明確略過", r.name)
			return &Result{Rule: rule}, true
		}
		res := &Result{Rule: rule}
		if r.codeRe != nil {
			if m := r.codeRe.FindStringSubmatch(msg.Subject + "\n" + msg.Text); m != nil {
				res.Code = m[0]
				if len(m) > 1 {
					res.Code = m[1]
				}
			}
		}
		if r.linkRe != nil {
			for _, l := range msg.Links {
				if m := r.linkRe.FindString(l.URL); m != "" {
					res.URL = m
					break
				}
			}
		}
		if res.Code != "" || res.URL != "" {
			tr.Addf("自訂規則 %s：命中", r.name)
			return res, false
		}
		tr.Addf("自訂規則 %s：適用但沒有擷取到內容，繼續比對", r.name)
	}
	return nil, false
}

func New(rules []config.Rule, opts config.RecognizeOptions) (*Chain, error) {
	custom, err := NewCustom(rules)
	if err != nil {
		return nil, err
	}
	return NewChain(custom, NewBuiltin(opts)), nil
}
```

- [ ] **Step 7: Run tests — expect PASS**

Run: `gofmt -w internal/recognize && go test ./internal/recognize/ -v && go vet ./...`
Expected: PASS. Same tuning rule as Task 5 if a link case fails: read the trace; adjust weights in `link.go` only.

- [ ] **Step 8: Commit**

```bash
git add internal/recognize
git commit -m "feat(recognize): built-in link rule, custom rules and recognizer chain

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 7: Recognizer `.eml` corpus (end-to-end parse + recognize)

**Files:**
- Create: `testdata/eml/gen.py` (writes every fixture; encoded ones need real Big5/GB2312/ISO-2022-JP bytes)
- Create (generated): `testdata/eml/*.eml`, `testdata/eml/*.expected.yaml`
- Test: `internal/recognize/corpus_test.go`

**Interfaces:**
- Consumes: `mailparse.Parse` (Task 4), `recognize.New` (Task 6).
- Produces: the corpus convention used from now on — every `NAME.eml` has `NAME.expected.yaml` with exactly one of: `code:` and/or `url:`, `ignore: true`, or `none: true`. The corpus test runs with this fixed rule set: `[{name: promo, from: "*@news.example.com", ignore: true}]`.

- [ ] **Step 1: Write the generator** — `testdata/eml/gen.py`

```python
#!/usr/bin/env python3
"""Generates the recognizer test corpus. Re-run after editing: python3 testdata/eml/gen.py"""
import base64, os

HERE = os.path.dirname(os.path.abspath(__file__))

def b64(data: bytes) -> str:
    s = base64.b64encode(data).decode()
    return "\r\n".join(s[i:i + 76] for i in range(0, len(s), 76))

def word(text: str, charset: str) -> str:
    return "=?%s?B?%s?=" % (charset, base64.b64encode(text.encode(charset)).decode())

def write(name, headers, body_parts, expected):
    lines = [f"{k}: {v}" for k, v in headers]
    lines.append("MIME-Version: 1.0")
    if len(body_parts) == 1:
        ctype, charset, body = body_parts[0]
        lines += [f"Content-Type: {ctype}; charset={charset}", "Content-Transfer-Encoding: base64", "", b64(body.encode(charset))]
    else:
        lines += ['Content-Type: multipart/alternative; boundary="BOUNDARY"', ""]
        for ctype, charset, body in body_parts:
            lines += ["--BOUNDARY", f"Content-Type: {ctype}; charset={charset}", "Content-Transfer-Encoding: base64", "", b64(body.encode(charset))]
        lines.append("--BOUNDARY--")
    with open(os.path.join(HERE, name + ".eml"), "wb") as f:
        f.write(("\r\n".join(lines) + "\r\n").encode("ascii"))
    with open(os.path.join(HERE, name + ".expected.yaml"), "w", encoding="utf-8") as f:
        f.write(expected.strip() + "\n")

DATE = ("Date", "Mon, 19 Oct 2026 08:00:00 +0800")

write("github-device-en",
      [("From", '"GitHub" <noreply@github.com>'), ("To", "me@gmail.com"), ("Subject", "[GitHub] Please verify your device"), DATE],
      [("text/plain", "utf-8", "Hey me!\n\nA sign in attempt requires further verification because we did not recognize your device.\n\nVerification code: 482913\n\nIf you did not attempt to sign in to your account, your password may be compromised.\nVisit https://github.com/settings/security to create a new, strong password.\n\nThanks,\nThe GitHub Team\n"),
       ("text/html", "utf-8", "<p>Hey me!</p><p>Verification code: <strong>482913</strong></p><p><a href=\"https://github.com/settings/security\">Change password</a></p>")],
      'code: "482913"')

write("google-code-en",
      [("From", "Google <no-reply@accounts.google.com>"), ("To", "me@gmail.com"), ("Subject", "G-583920 is your Google verification code"), DATE],
      [("text/plain", "utf-8", "Google\nYour verification code is G-583920.\nDon't share it with anyone.\n© 2026 Google LLC, 1600 Amphitheatre Parkway, Mountain View, CA 94043\n")],
      'code: "583920"')

write("microsoft-security-code-en",
      [("From", "Microsoft account team <account-security-noreply@accountprotection.microsoft.com>"), ("To", "me@outlook.com"), ("Subject", "Microsoft account security code"), DATE],
      [("text/plain", "utf-8", "Microsoft account\nSecurity code\n\nPlease use the following security code for the Microsoft account me@outlook.com.\n\nSecurity code: 7291\n\nIf you don't recognize the Microsoft account me@outlook.com, you can click https://account.live.com/dp?ft=-Dj7a8sQ2 to remove your email address from that account.\n\nThanks,\nThe Microsoft account team\n")],
      'code: "7291"')

write("bank-zh-tw-big5",
      [("From", word("範例銀行", "big5") + " <notify@bank.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("【範例銀行】網路銀行登入驗證碼", "big5")), DATE],
      [("text/html", "big5", "<html><body><p>親愛的客戶您好：</p><p>您的驗證碼為 <b>604217</b>，有效時間 5 分鐘，請勿提供給他人。</p><p>本次交易金額 NT$ 1,280。</p><p>如有疑問請洽客服專線 0800-000-123。</p><p>範例銀行 敬上 2026年10月19日</p></body></html>")],
      'code: "604217"')

write("shop-zh-cn-gb2312",
      [("From", word("某商城", "gb2312") + " <service@shop.example.cn>"), ("To", "me@example.com"), ("Subject", word("登录验证码", "gb2312")), DATE],
      [("text/plain", "gb2312", "【某商城】验证码：917344，用于登录，5分钟内有效。如非本人操作，请忽略本邮件。\n您最近的订单号 20261019123 已发货。\n")],
      'code: "917344"')

write("ja-login-iso2022jp",
      [("From", "Example <no-reply@example.co.jp>"), ("To", "me@example.com"), ("Subject", word("ログイン認証コードのお知らせ", "iso-2022-jp")), DATE],
      [("text/plain", "iso-2022-jp", "いつもご利用ありがとうございます。\n認証コード：338120\nこのコードの有効期限は10分です。\n")],
      'code: "338120"')

write("slack-magic-link-en",
      [("From", "Slack <no-reply@slack.com>"), ("To", "me@gmail.com"), ("Subject", "Sign in to Example on Slack"), DATE],
      [("text/html", "utf-8", "<p>Click the button below to sign in to Slack. This link will expire in 24 hours.</p><a href=\"https://example.slack.com/z-app-1234-abcd/magic?t=xoxm7YbQ2LcP0aZ9eKfTqWv3\">Sign in to Slack</a><p><a href=\"https://slack.com/help\">Help</a> <a href=\"https://slack.com/unsubscribe?e=1\">Unsubscribe</a></p>")],
      'url: "https://example.slack.com/z-app-1234-abcd/magic?t=xoxm7YbQ2LcP0aZ9eKfTqWv3"')

write("verify-email-link-en",
      [("From", "Example <hello@mail.example.com>"), ("To", "me@gmail.com"), ("Subject", "Confirm your email address"), DATE],
      [("text/html", "utf-8", "<img src=\"https://t.example.com/open.gif\"><h1>Welcome!</h1><p>Please confirm your email address to finish signing up.</p><a href=\"https://click.mail.example.com/ls/click?upn=AbCdEf1234567890GhIjKl\">Confirm email</a><p>Follow us: <a href=\"https://twitter.com/example\">Twitter</a> <a href=\"https://www.facebook.com/example\">Facebook</a></p><p><a href=\"https://example.com/privacy\">Privacy</a> · <a href=\"https://click.mail.example.com/unsubscribe?u=9\">Unsubscribe</a></p>")],
      'url: "https://click.mail.example.com/ls/click?upn=AbCdEf1234567890GhIjKl"')

write("reset-password-en",
      [("From", "Acme <security@acme.example>"), ("To", "me@gmail.com"), ("Subject", "Reset your Acme password"), DATE],
      [("text/plain", "utf-8", "Someone requested a password reset for your Acme account.\n\nReset your password:\nhttps://acme.example/account/reset-password?token=q8Zp3LmN0aB7cD2eF5gH\n\nIf you didn't request this, you can ignore this email.\nNeed help? https://acme.example/support\n")],
      'url: "https://acme.example/account/reset-password?token=q8Zp3LmN0aB7cD2eF5gH"')

write("reset-password-zh",
      [("From", word("範例服務", "utf-8") + " <noreply@service.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("重設密碼通知", "utf-8")), DATE],
      [("text/html", "utf-8", "<p>您好，我們收到了重設密碼的請求。</p><p><a href=\"https://service.example.com.tw/password/reset?code=7f3a9c2e1b5d4f6a8c0e\">重設密碼</a></p><p>若非本人操作請忽略此信。<a href=\"https://service.example.com.tw/help\">說明中心</a></p>")],
      'url: "https://service.example.com.tw/password/reset?code=7f3a9c2e1b5d4f6a8c0e"')

write("code-and-link-en",
      [("From", "Example <noreply@example.com>"), ("To", "me+shop@gmail.com"), ("Subject", "Verify your email for Example"), DATE],
      [("text/plain", "utf-8", "Your verification code is 250391.\n\nOr verify your email by opening this link:\nhttps://example.com/verify?token=Hk29dLqP0zXv8MnB4cRt\n\nThis code expires in 10 minutes.\n")],
      'code: "250391"\nurl: "https://example.com/verify?token=Hk29dLqP0zXv8MnB4cRt"')

write("newsletter-en",
      [("From", "Example Weekly <weekly@example.com>"), ("To", "me@gmail.com"), ("Subject", "This week: 5 stories you missed"), DATE],
      [("text/html", "utf-8", "<h2>Top stories</h2><p>1. Our 2026 roadmap — <a href=\"https://example.com/blog/2026-roadmap\">Read more</a></p><p>2. Join 12000 developers at our conference on Oct 19, 2026.</p><p><a href=\"https://example.com/unsubscribe\">Unsubscribe</a></p>")],
      "none: true")

write("order-shipped-zh",
      [("From", word("範例購物", "utf-8") + " <order@shop.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("您的訂單已出貨", "utf-8")), DATE],
      [("text/html", "utf-8", "<p>親愛的會員您好，您的訂單 #A1234567 已於 2026年10月19日 出貨。</p><p>訂單金額 NT$ 2,580。請確認收貨後給予評價。</p><a href=\"https://shop.example.com.tw/order/A1234567\">查看訂單</a>")],
      "none: true")

write("invoice-en",
      [("From", "Billing <billing@saas.example>"), ("To", "me@gmail.com"), ("Subject", "Your invoice #20261019 is ready"), DATE],
      [("text/plain", "utf-8", "Invoice #20261019\nAmount due: $1,234.00 on 2026-11-01\nQuestions? Call +1 (800) 555-0199.\nView invoice: https://saas.example/invoices/20261019\n")],
      "none: true")

write("security-alert-en",
      [("From", "Google <no-reply@accounts.google.com>"), ("To", "me@gmail.com"), ("Subject", "Security alert"), DATE],
      [("text/html", "utf-8", "<p>We noticed a new sign in to your Google Account on a Windows device. If this was you, you don't need to do anything.</p><a href=\"https://accounts.google.com/AccountChooser?Email=me@gmail.com&continue=https://myaccount.google.com/alert/nt/1729300000000\">Check activity</a><p>You received this email to let you know about important changes. © 2026 Google LLC</p>")],
      "none: true")

write("ignored-promo",
      [("From", "Deals <promo@news.example.com>"), ("To", "me@gmail.com"), ("Subject", "Your code is inside: SAVE2026"), DATE],
      [("text/plain", "utf-8", "Use promo code SAVE2026 to get 20% off.\n")],
      "ignore: true")

print("ok")
```

- [ ] **Step 2: Generate fixtures**

Run: `python3 testdata/eml/gen.py && ls testdata/eml | wc -l`
Expected: `ok` and `33` (16 `.eml` + 16 `.expected.yaml` + `gen.py`).

- [ ] **Step 3: Write the corpus test** — `internal/recognize/corpus_test.go`

```go
package recognize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/mailparse"
)

type expectation struct {
	Code   string `yaml:"code"`
	URL    string `yaml:"url"`
	Ignore bool   `yaml:"ignore"`
	None   bool   `yaml:"none"`
}

func TestCorpus(t *testing.T) {
	chain, err := New([]config.Rule{{Name: "promo", From: "*@news.example.com", Ignore: true}}, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../../testdata/eml/*.eml")
	if len(files) < 16 {
		t.Fatalf("corpus too small: %d files", len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".eml")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			expRaw, err := os.ReadFile(strings.TrimSuffix(f, ".eml") + ".expected.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var exp expectation
			if err := yaml.Unmarshal(expRaw, &exp); err != nil {
				t.Fatal(err)
			}
			msg, err := mailparse.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			res, ignore, tr := chain.Recognize(msg)
			fail := func(format string, args ...any) {
				t.Errorf(format+"\nsubject=%q\ntext=%q\ntrace:\n  %s", append(args, msg.Subject, msg.Text, strings.Join(tr.Steps, "\n  "))...)
			}
			switch {
			case exp.Ignore:
				if !ignore {
					fail("want ignore, got %+v", res)
				}
			case exp.None:
				if res != nil || ignore {
					fail("want none, got %+v ignore=%v", res, ignore)
				}
			default:
				if res == nil || ignore || res.Code != exp.Code || res.URL != exp.URL {
					fail("want code=%q url=%q, got %+v ignore=%v", exp.Code, exp.URL, res, ignore)
				}
			}
		})
	}
}
```

- [ ] **Step 4: Run the corpus**

Run: `go test ./internal/recognize/ -run TestCorpus -v`
Expected: PASS. If a fixture fails, the output includes subject, text and trace. Diagnose in this order: (1) parsing problem (text/subject wrong) → fix `mailparse`; (2) a missing keyword → add it to the default list in `code.go`/`link.go`; (3) scoring → adjust weights/threshold. After any change, re-run **all** of `go test ./internal/recognize/ ./internal/mailparse/` — earlier unit tests must stay green. Never edit an `.expected.yaml` to match wrong output. If a fixture seems genuinely ambiguous, stop and ask the user (spec §5 weights are "初始值").

- [ ] **Step 5: Commit**

```bash
git add testdata/eml internal/recognize
git commit -m "test(recognize): add .eml corpus covering zh/en/ja codes, links and negatives

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 8: Template rendering (HTML escaping, lookup order, truncation, buttons)

**Files:**
- Create: `internal/render/render.go`, `internal/render/defaults.go`
- Test: `internal/render/render_test.go`

**Interfaces:**
- Consumes: `config.Config`, `config.Templates` (Task 1).
- Produces:
  ```go
  package render
  type Data struct {
      Code, URL, Service, FromName, FromAddr, To, Subject, Mailbox, Rule string
      Date time.Time
  }
  type Rendered struct {
      Text        string // Telegram HTML
      ReplyMarkup string // JSON {"inline_keyboard":[[...]]} or "" when no buttons
  }
  type Renderer struct{ /* unexported */ }
  func New(cfg *config.Config) (*Renderer, error)   // parses + trial-renders every template; error paths "templates.code: ..." / "mailboxes.x.templates.link: ..."
  func (r *Renderer) Render(mailbox string, d Data) (Rendered, error)  // mailbox "" = global templates
  const MaxTextRunes = 4096
  ```

- [ ] **Step 1: Write the failing test** — `internal/render/render_test.go`

```go
package render

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"verify-code-relay/internal/config"
)

func cfgWith(global config.Templates, perMailbox map[string]config.Templates) *config.Config {
	c := &config.Config{Templates: global, Mailboxes: map[string]*config.Mailbox{}}
	for name, t := range perMailbox {
		c.Mailboxes[name] = &config.Mailbox{Name: name, Templates: t}
	}
	return c
}

var sample = Data{Code: "123456", URL: "https://example.com/v?a=1&b=2", Service: "GitHub", FromName: "GitHub",
	FromAddr: "noreply@github.com", To: "me@gmail.com", Subject: "Verify <device> & more", Mailbox: "個人 Gmail",
	Rule: "builtin:code", Date: time.Date(2026, 10, 19, 8, 0, 0, 0, time.UTC)}

func TestDefaultCodeTemplateEscapes(t *testing.T) {
	r, err := New(cfgWith(config.Templates{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	d := sample
	d.URL = ""
	out, err := r.Render("", d)
	if err != nil {
		t.Fatal(err)
	}
	want := "🔐 <b>GitHub</b> 驗證碼\n<code>123456</code>\n📧 個人 Gmail · Verify &lt;device&gt; &amp; more"
	if out.Text != want {
		t.Errorf("text:\n%q\nwant\n%q", out.Text, want)
	}
	var mk struct {
		InlineKeyboard [][]map[string]any `json:"inline_keyboard"`
	}
	if err := json.Unmarshal([]byte(out.ReplyMarkup), &mk); err != nil {
		t.Fatal(err)
	}
	btn := mk.InlineKeyboard[0][0]
	if btn["text"] != "📋 複製驗證碼" || btn["copy_text"].(map[string]any)["text"] != "123456" || len(mk.InlineKeyboard[0]) != 1 {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestBothTemplateAndButtons(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	out, err := r.Render("", sample)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "🔗 https://example.com/v?a=1&amp;b=2") || !strings.Contains(out.Text, "<code>123456</code>") {
		t.Errorf("text = %q", out.Text)
	}
	if !strings.Contains(out.ReplyMarkup, `"copy_text"`) || !strings.Contains(out.ReplyMarkup, `"url":"https://example.com/v?a=1&b=2"`) {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestLinkOnly(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	d := sample
	d.Code = ""
	out, _ := r.Render("", d)
	if !strings.HasPrefix(out.Text, "🔗 <b>GitHub</b> 認證連結\nhttps://example.com/v?a=1&amp;b=2") {
		t.Errorf("text = %q", out.Text)
	}
	if strings.Contains(out.ReplyMarkup, "copy_text") || !strings.Contains(out.ReplyMarkup, `"url"`) {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}

func TestLookupOrder(t *testing.T) {
	r, err := New(cfgWith(config.Templates{Code: "G {{.Code}}"}, map[string]config.Templates{"work": {Code: "W {{.Code}} {{.Date.Format \"2006\"}}"}}))
	if err != nil {
		t.Fatal(err)
	}
	d := sample
	d.URL = ""
	if out, _ := r.Render("work", d); out.Text != "W 123456 2026" {
		t.Errorf("mailbox override: %q", out.Text)
	}
	if out, _ := r.Render("other", d); out.Text != "G 123456" {
		t.Errorf("global override: %q", out.Text)
	}
	d.Code, d.URL = "", "https://x.y"
	if out, _ := r.Render("work", d); !strings.HasPrefix(out.Text, "🔗") {
		t.Errorf("builtin link fallback: %q", out.Text)
	}
}

func TestNewReportsTemplateErrors(t *testing.T) {
	_, err := New(cfgWith(config.Templates{Link: "{{.Nope}}"}, nil))
	if err == nil || !strings.Contains(err.Error(), "templates.link") {
		t.Fatalf("want templates.link error, got %v", err)
	}
	_, err = New(cfgWith(config.Templates{}, map[string]config.Templates{"m": {Both: "{{"}}))
	if err == nil || !strings.Contains(err.Error(), "mailboxes.m.templates.both") {
		t.Fatalf("want mailbox path, got %v", err)
	}
}

func TestTruncation(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	d := sample
	d.URL = ""
	d.Subject = strings.Repeat("長", 5000)
	out, err := r.Render("", d)
	if err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(out.Text); n > MaxTextRunes {
		t.Fatalf("len = %d", n)
	}
	if !strings.Contains(out.Text, "<code>123456</code>") || !strings.HasSuffix(out.Text, "…") {
		t.Errorf("code must survive and subject end with …: %q", out.Text[len(out.Text)-20:])
	}
}

func TestHugeURLKeepsButtonOnly(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	d := sample
	d.Code = ""
	d.URL = "https://example.com/?" + strings.Repeat("a", 4100)
	out, err := r.Render("", d)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(out.Text) > MaxTextRunes || strings.Contains(out.Text, "aaaa") {
		t.Errorf("text should drop URL: %d runes", utf8.RuneCountInString(out.Text))
	}
	if !strings.Contains(out.ReplyMarkup, `"url"`) {
		t.Errorf("button must keep URL")
	}
}

func TestLongCodeHasNoCopyButton(t *testing.T) {
	r, _ := New(cfgWith(config.Templates{}, nil))
	d := sample
	d.URL = ""
	d.Code = strings.Repeat("A", 257)
	out, _ := r.Render("", d)
	if out.ReplyMarkup != "" {
		t.Errorf("markup = %s", out.ReplyMarkup)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/render/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement `internal/render/defaults.go`**

```go
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
```

- [ ] **Step 4: Implement `internal/render/render.go`**

```go
// Package render turns a recognition result into a Telegram HTML message.
package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"
	"unicode/utf8"

	"verify-code-relay/internal/config"
)

const (
	MaxTextRunes = 4096
	maxCopyRunes = 256
	maxInlineURL = 4000
	kindCode     = "code"
	kindLink     = "link"
	kindBoth     = "both"
)

type Data struct {
	Code     string
	URL      string
	Service  string
	FromName string
	FromAddr string
	To       string
	Subject  string
	Mailbox  string
	Rule     string
	Date     time.Time
}

type Rendered struct {
	Text        string
	ReplyMarkup string
}

type tset map[string]*template.Template // kind -> template

type Renderer struct {
	global  tset
	mailbox map[string]tset
}

var trialData = Data{Code: "123456", URL: "https://example.com/verify?token=abc", Service: "Example",
	FromName: "Example", FromAddr: "noreply@example.com", To: "me@example.com", Subject: "Subject",
	Mailbox: "Mailbox", Rule: "builtin:code", Date: time.Unix(0, 0)}

func compile(path, src string) (*template.Template, error) {
	t, err := template.New(path).Option("missingkey=error").Parse(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := t.Execute(&bytes.Buffer{}, trialData); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

func compileSet(prefix string, tp config.Templates, fallback tset) (tset, error) {
	out := tset{}
	var errs []error
	for kind, src := range map[string]string{kindCode: tp.Code, kindLink: tp.Link, kindBoth: tp.Both} {
		if src == "" {
			if fallback != nil {
				out[kind] = fallback[kind]
			}
			continue
		}
		t, err := compile(prefix+kind, src)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[kind] = t
	}
	return out, errors.Join(errs...)
}

func New(cfg *config.Config) (*Renderer, error) {
	builtin, err := compileSet("builtin.", config.Templates{Code: defaultCode, Link: defaultLink, Both: defaultBoth}, nil)
	if err != nil {
		return nil, err // programming error
	}
	r := &Renderer{mailbox: map[string]tset{}}
	var errs []error
	if r.global, err = compileSet("templates.", cfg.Templates, builtin); err != nil {
		errs = append(errs, err)
	}
	for _, name := range cfg.SortedMailboxNames() {
		set, err := compileSet("mailboxes."+name+".templates.", cfg.Mailboxes[name].Templates, r.global)
		if err != nil {
			errs = append(errs, err)
		}
		r.mailbox[name] = set
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Renderer) Render(mailbox string, d Data) (Rendered, error) {
	kind := kindCode
	switch {
	case d.Code != "" && d.URL != "":
		kind = kindBoth
	case d.Code == "" && d.URL != "":
		kind = kindLink
	case d.Code == "" && d.URL == "":
		return Rendered{}, errors.New("render: 沒有驗證碼也沒有連結")
	}
	set, ok := r.mailbox[mailbox]
	if !ok {
		set = r.global
	}
	t := set[kind]
	markup, err := buttons(d)
	if err != nil {
		return Rendered{}, err
	}

	text, err := exec(t, d)
	if err != nil {
		return Rendered{}, err
	}
	// 1) shrink the subject until it fits
	for utf8.RuneCountInString(text) > MaxTextRunes && d.Subject != "" {
		subj := []rune(strings.TrimSuffix(d.Subject, "…"))
		over := utf8.RuneCountInString(text) - MaxTextRunes
		keep := len(subj) - over - 1
		if keep < 0 {
			keep = 0
		}
		d.Subject = string(subj[:keep]) + "…"
		if keep == 0 {
			d.Subject = ""
		}
		if text, err = exec(t, d); err != nil {
			return Rendered{}, err
		}
	}
	// 2) a gigantic URL stays only in the button
	if utf8.RuneCountInString(text) > MaxTextRunes && utf8.RuneCountInString(d.URL) > maxInlineURL {
		d.URL = ""
		if text, err = exec(t, d); err != nil {
			return Rendered{}, err
		}
	}
	// 3) last resort: hard cut (may break HTML; the dispatcher's plain-text fallback handles that)
	if utf8.RuneCountInString(text) > MaxTextRunes {
		text = string([]rune(text)[:MaxTextRunes])
	}
	return Rendered{Text: text, ReplyMarkup: markup}, nil
}

func exec(t *template.Template, d Data) (string, error) {
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

type button struct {
	Text     string    `json:"text"`
	URL      string    `json:"url,omitempty"`
	CopyText *copyText `json:"copy_text,omitempty"`
}

type copyText struct {
	Text string `json:"text"`
}

func buttons(d Data) (string, error) {
	var row []button
	if n := utf8.RuneCountInString(d.Code); n >= 1 && n <= maxCopyRunes {
		row = append(row, button{Text: "📋 複製驗證碼", CopyText: &copyText{Text: d.Code}})
	}
	if d.URL != "" {
		row = append(row, button{Text: "🔗 開啟連結", URL: d.URL})
	}
	if len(row) == 0 {
		return "", nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep '&' in URLs readable; Telegram accepts either form
	if err := enc.Encode(map[string][][]button{"inline_keyboard": {row}}); err != nil {
		return "", err
	}
	return strings.TrimSpace(buf.String()), nil
}
```

Note on the subject-shrinking loop: when the template does not reference `.Subject` the loop would spin; the `keep == 0` branch sets `Subject = ""`, which terminates the loop on the next check.

- [ ] **Step 5: Run tests — expect PASS**

Run: `gofmt -w internal/render && go test ./internal/render/ -v && go vet ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/render
git commit -m "feat(render): html/template rendering with escaping, override lookup, truncation and buttons

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 9: Telegram Bot API client

**Files:**
- Create: `internal/telegram/telegram.go`
- Test: `internal/telegram/telegram_test.go`

**Interfaces:**
- Consumes: nothing project-internal.
- Produces:
  ```go
  package telegram
  type Client struct{ BaseURL string; HTTP *http.Client }
  func New() *Client                               // BaseURL "https://api.telegram.org", 30 s timeout
  type SendRequest struct {
      Token       string
      ChatID      int64
      Text        string
      HTML        bool   // parse_mode=HTML when true
      ReplyMarkup string // raw JSON, "" = none
  }
  type ErrorKind int
  const ( Transient ErrorKind = iota; RateLimited; BadRequest; Forbidden )
  type SendError struct{ Kind ErrorKind; RetryAfter time.Duration; Description string }
  func (e *SendError) Error() string
  func (c *Client) Send(ctx context.Context, req SendRequest) error   // nil or *SendError; token never appears in errors
  func PlainText(htmlText string) string                              // strip tags + unescape, for the 400 fallback
  ```

- [ ] **Step 1: Write the failing test** — `internal/telegram/telegram_test.go`

```go
package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSendOKRequestShape(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	err := c.Send(context.Background(), SendRequest{Token: "123:SECRET", ChatID: -100, Text: "<b>x</b>", HTML: true,
		ReplyMarkup: `{"inline_keyboard":[[{"text":"t","url":"https://e.com"}]]}`})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:SECRET/sendMessage" {
		t.Errorf("path = %s", path)
	}
	if got["chat_id"].(float64) != -100 || got["parse_mode"] != "HTML" || got["text"] != "<b>x</b>" {
		t.Errorf("body = %v", got)
	}
	if got["link_preview_options"].(map[string]any)["is_disabled"] != true {
		t.Errorf("preview not disabled: %v", got)
	}
	if _, ok := got["reply_markup"].(map[string]any)["inline_keyboard"]; !ok {
		t.Errorf("markup not embedded as JSON object: %v", got["reply_markup"])
	}
}

func TestSendPlainOmitsParseModeAndMarkup(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	if err := c.Send(context.Background(), SendRequest{Token: "t", ChatID: 1, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["parse_mode"]; ok {
		t.Errorf("parse_mode present: %v", got)
	}
	if _, ok := got["reply_markup"]; ok {
		t.Errorf("reply_markup present: %v", got)
	}
}

func TestSendErrorKinds(t *testing.T) {
	cases := []struct {
		status int
		body   string
		kind   ErrorKind
		retry  time.Duration
	}{
		{429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`, RateLimited, 7 * time.Second},
		{400, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`, BadRequest, 0},
		{403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, Forbidden, 0},
		{401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, Forbidden, 0},
		{404, `{"ok":false,"error_code":404,"description":"Not Found"}`, Forbidden, 0},
		{502, `bad gateway`, Transient, 0},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
		err := c.Send(context.Background(), SendRequest{Token: "123:SECRET", ChatID: 1, Text: "x"})
		srv.Close()
		var se *SendError
		if !errors.As(err, &se) || se.Kind != tc.kind || se.RetryAfter != tc.retry {
			t.Errorf("status %d: got %#v", tc.status, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("token leaked: %v", err)
		}
	}
}

func TestSendNetworkErrorRedactsToken(t *testing.T) {
	c := &Client{BaseURL: "http://127.0.0.1:1", HTTP: &http.Client{Timeout: time.Second}}
	err := c.Send(context.Background(), SendRequest{Token: "123:SECRET", ChatID: 1, Text: "x"})
	var se *SendError
	if !errors.As(err, &se) || se.Kind != Transient {
		t.Fatalf("got %#v", err)
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("token leaked: %v", err)
	}
}

func TestPlainText(t *testing.T) {
	got := PlainText("🔐 <b>GitHub</b>\n<code>1 &lt; 2 &amp;&amp; 3</code>")
	if got != "🔐 GitHub\n1 < 2 && 3" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/telegram/ -v`
Expected: FAIL — `undefined: Client`.

- [ ] **Step 3: Implement `internal/telegram/telegram.go`**

```go
// Package telegram is a minimal Bot API client: sendMessage only.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New() *Client {
	return &Client{BaseURL: "https://api.telegram.org", HTTP: &http.Client{Timeout: 30 * time.Second}}
}

type SendRequest struct {
	Token       string
	ChatID      int64
	Text        string
	HTML        bool
	ReplyMarkup string
}

type ErrorKind int

const (
	Transient ErrorKind = iota
	RateLimited
	BadRequest
	Forbidden
)

func (k ErrorKind) String() string {
	return [...]string{"transient", "rate_limited", "bad_request", "forbidden"}[k]
}

type SendError struct {
	Kind        ErrorKind
	RetryAfter  time.Duration
	Description string
}

func (e *SendError) Error() string {
	return fmt.Sprintf("telegram %s: %s", e.Kind, e.Description)
}

type payload struct {
	ChatID             int64           `json:"chat_id"`
	Text               string          `json:"text"`
	ParseMode          string          `json:"parse_mode,omitempty"`
	LinkPreviewOptions map[string]bool `json:"link_preview_options"`
	ReplyMarkup        json.RawMessage `json:"reply_markup,omitempty"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) Send(ctx context.Context, req SendRequest) error {
	p := payload{ChatID: req.ChatID, Text: req.Text, LinkPreviewOptions: map[string]bool{"is_disabled": true}}
	if req.HTML {
		p.ParseMode = "HTML"
	}
	if req.ReplyMarkup != "" {
		p.ReplyMarkup = json.RawMessage(req.ReplyMarkup)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return &SendError{Kind: BadRequest, Description: err.Error()}
	}
	redact := func(s string) string { return strings.ReplaceAll(s, req.Token, "<token>") }
	url := strings.TrimRight(c.BaseURL, "/") + "/bot" + req.Token + "/sendMessage"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return &SendError{Kind: Transient, Description: redact(err.Error())}
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(hreq)
	if err != nil {
		return &SendError{Kind: Transient, Description: redact(err.Error())}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var ar apiResponse
	_ = json.Unmarshal(raw, &ar)
	if resp.StatusCode == http.StatusOK && ar.OK {
		return nil
	}
	desc := ar.Description
	if desc == "" {
		desc = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	desc = redact(desc)
	code := ar.ErrorCode
	if code == 0 {
		code = resp.StatusCode
	}
	switch {
	case code == http.StatusTooManyRequests:
		retry := time.Duration(ar.Parameters.RetryAfter) * time.Second
		if retry <= 0 {
			retry = 5 * time.Second
		}
		return &SendError{Kind: RateLimited, RetryAfter: retry, Description: desc}
	case code == http.StatusBadRequest:
		return &SendError{Kind: BadRequest, Description: desc}
	case code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound:
		return &SendError{Kind: Forbidden, Description: desc}
	default:
		return &SendError{Kind: Transient, Description: desc}
	}
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// PlainText converts Telegram HTML to plain text (used when Telegram rejects our HTML).
func PlainText(htmlText string) string {
	return html.UnescapeString(tagRe.ReplaceAllString(htmlText, ""))
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/telegram/ -v && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/telegram
git commit -m "feat(telegram): sendMessage client with error classification and token redaction

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 10: Dispatcher (outbox → Telegram)

**Files:**
- Create: `internal/dispatcher/dispatcher.go`
- Test: `internal/dispatcher/dispatcher_test.go`

**Interfaces:**
- Consumes: `store.OutboxItem` + store methods (Task 3), `telegram.SendRequest`, `telegram.SendError`, `telegram.PlainText` (Task 9).
- Produces:
  ```go
  package dispatcher
  type Store interface {
      DueOutbox(ctx context.Context, now time.Time, limit int) ([]store.OutboxItem, error)
      DeleteExpired(ctx context.Context, now time.Time) ([]store.OutboxItem, error)
      DeleteOutbox(ctx context.Context, id int64) error
      RescheduleOutbox(ctx context.Context, id int64, attempts int, next time.Time, plainFallback bool) error
      SetLogResult(ctx context.Context, logID int64, result string) error
  }
  type Sender interface{ Send(ctx context.Context, req telegram.SendRequest) error }
  type Options struct {
      Store  Store
      Sender Sender
      Tokens map[string]string                // bot name -> token
      Alert  func(key, text string)           // may be nil
      Log    *slog.Logger
      Now    func() time.Time                 // nil = time.Now
  }
  func New(o Options) *Dispatcher
  func (d *Dispatcher) Wake()                  // non-blocking
  func (d *Dispatcher) RunOnce(ctx context.Context) error
  func (d *Dispatcher) Run(ctx context.Context) // until ctx done; finishes an in-flight send with its own 10 s timeout
  ```
  Alert items are outbox rows with `LogID == 0`; failures of alert items are only logged (never re-alerted).

- [ ] **Step 1: Write the failing test** — `internal/dispatcher/dispatcher_test.go`

```go
package dispatcher

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/store"
	"verify-code-relay/internal/telegram"
)

type fakeSender struct {
	errs []error // consumed in order; nil = success
	reqs []telegram.SendRequest
}

func (f *fakeSender) Send(_ context.Context, r telegram.SendRequest) error {
	f.reqs = append(f.reqs, r)
	if len(f.errs) == 0 {
		return nil
	}
	e := f.errs[0]
	f.errs = f.errs[1:]
	return e
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type alerts struct{ keys []string }

func (a *alerts) alert(key, _ string) { a.keys = append(a.keys, key) }

func setup(t *testing.T, errs ...error) (*Dispatcher, *store.Store, *fakeSender, *clock, *alerts) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := &fakeSender{errs: errs}
	ck := &clock{t: time.Unix(1_800_000_000, 0)}
	al := &alerts{}
	d := New(Options{Store: st, Sender: fs, Tokens: map[string]string{"main": "TOKEN"}, Alert: al.alert,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: ck.now})
	return d, st, fs, ck, al
}

var ctx = context.Background()

func record(t *testing.T, st *store.Store, now time.Time, chats ...int64) int64 {
	t.Helper()
	var items []store.OutboxItem
	for _, c := range chats {
		items = append(items, store.OutboxItem{Target: "t", Bot: "main", ChatID: c, Text: "<b>hi</b>",
			ReplyMarkup: `{"inline_keyboard":[]}`, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute), NextAttemptAt: now})
	}
	id, err := st.RecordMessage(ctx, store.LogEntry{At: now, Mailbox: "g", Folder: "INBOX", UID: 1, Kind: "code", Result: "pending"},
		items, store.MailboxState{UIDValidity: 1, LastUID: 1})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pending(t *testing.T, st *store.Store, now time.Time) []store.OutboxItem {
	items, err := st.DueOutbox(ctx, now.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestSendSuccess(t *testing.T) {
	d, st, fs, ck, _ := setup(t)
	record(t, st, ck.t, 1, 2)
	if err := d.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fs.reqs) != 2 || fs.reqs[0].Token != "TOKEN" || !fs.reqs[0].HTML || fs.reqs[0].ReplyMarkup == "" {
		t.Fatalf("reqs = %+v", fs.reqs)
	}
	if len(pending(t, st, ck.t)) != 0 {
		t.Error("outbox not drained")
	}
}

func TestPerChatSpacing(t *testing.T) {
	d, st, fs, ck, _ := setup(t)
	record(t, st, ck.t, 1)
	record(t, st, ck.t, 1)
	d.RunOnce(ctx)
	d.RunOnce(ctx) // same instant: second message to same chat must wait
	if len(fs.reqs) != 1 {
		t.Fatalf("sent %d, want 1", len(fs.reqs))
	}
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 2 {
		t.Fatalf("sent %d, want 2", len(fs.reqs))
	}
}

func TestRateLimited(t *testing.T) {
	d, st, fs, ck, _ := setup(t, &telegram.SendError{Kind: telegram.RateLimited, RetryAfter: 7 * time.Second})
	record(t, st, ck.t, 1)
	d.RunOnce(ctx)
	ck.t = ck.t.Add(6 * time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 1 {
		t.Fatalf("retried too early: %d", len(fs.reqs))
	}
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 2 || len(pending(t, st, ck.t)) != 0 {
		t.Fatalf("reqs=%d pending=%d", len(fs.reqs), len(pending(t, st, ck.t)))
	}
}

func TestTransientBackoff(t *testing.T) {
	d, st, fs, ck, _ := setup(t, &telegram.SendError{Kind: telegram.Transient}, &telegram.SendError{Kind: telegram.Transient})
	record(t, st, ck.t, 1)
	d.RunOnce(ctx) // fail #1 -> next in 2s
	ck.t = ck.t.Add(2 * time.Second)
	d.RunOnce(ctx) // fail #2 -> next in 4s
	ck.t = ck.t.Add(3 * time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 2 {
		t.Fatalf("reqs = %d", len(fs.reqs))
	}
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 3 {
		t.Fatalf("reqs = %d", len(fs.reqs))
	}
}

func TestBadRequestFallsBackToPlainThenFails(t *testing.T) {
	d, st, fs, ck, al := setup(t, &telegram.SendError{Kind: telegram.BadRequest}, &telegram.SendError{Kind: telegram.BadRequest})
	record(t, st, ck.t, 1)
	d.RunOnce(ctx)
	ck.t = ck.t.Add(time.Second)
	d.RunOnce(ctx)
	if len(fs.reqs) != 2 {
		t.Fatalf("reqs = %d", len(fs.reqs))
	}
	if fs.reqs[1].HTML || fs.reqs[1].ReplyMarkup != "" || fs.reqs[1].Text != "hi" {
		t.Errorf("fallback request = %+v", fs.reqs[1])
	}
	if len(pending(t, st, ck.t)) != 0 || len(al.keys) != 1 || !strings.HasPrefix(al.keys[0], "send_failed:") {
		t.Errorf("pending/alerts wrong: %v", al.keys)
	}
}

func TestForbiddenAlertsOnceButNotForAlertItems(t *testing.T) {
	d, st, _, ck, al := setup(t, &telegram.SendError{Kind: telegram.Forbidden}, &telegram.SendError{Kind: telegram.Forbidden})
	record(t, st, ck.t, 1)
	st.Enqueue(ctx, store.OutboxItem{Target: "admin", Bot: "main", ChatID: 9, Text: "alert", CreatedAt: ck.t,
		ExpiresAt: ck.t.Add(time.Hour), NextAttemptAt: ck.t})
	d.RunOnce(ctx)
	if len(al.keys) != 1 {
		t.Fatalf("alerts = %v (alert-item failure must not alert)", al.keys)
	}
}

func TestExpired(t *testing.T) {
	d, st, fs, ck, al := setup(t)
	record(t, st, ck.t, 1)
	ck.t = ck.t.Add(16 * time.Minute)
	d.RunOnce(ctx)
	if len(fs.reqs) != 0 || len(pending(t, st, ck.t)) != 0 || len(al.keys) != 0 {
		t.Fatalf("reqs=%d alerts=%v", len(fs.reqs), al.keys)
	}
}

func TestUnknownBotFailsItem(t *testing.T) {
	d, st, fs, ck, al := setup(t)
	st.Enqueue(ctx, store.OutboxItem{LogID: 0, Target: "x", Bot: "ghost", ChatID: 1, Text: "x", CreatedAt: ck.t,
		ExpiresAt: ck.t.Add(time.Hour), NextAttemptAt: ck.t})
	d.RunOnce(ctx)
	if len(fs.reqs) != 0 || len(pending(t, st, ck.t)) != 0 || len(al.keys) != 0 {
		t.Fatalf("reqs=%d alerts=%v", len(fs.reqs), al.keys)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/dispatcher/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement `internal/dispatcher/dispatcher.go`**

```go
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
```

Notes for the implementer: `lastSent` is only touched by the `Run` goroutine, so no mutex is needed. The `TestPerChatSpacing` test relies on `RunOnce` skipping a chat whose last send was <1 s ago; the skipped item stays the chat's head and is retried next tick.

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/dispatcher/ -v && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatcher
git commit -m "feat(dispatcher): drain outbox with per-chat pacing, retries, plain-text fallback and expiry

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 11: Alerts

**Files:**
- Create: `internal/alert/alert.go`
- Test: `internal/alert/alert_test.go`

**Interfaces:**
- Consumes: `store.OutboxItem`, `(*store.Store).Enqueue` (Task 3).
- Produces:
  ```go
  package alert
  type Enqueuer interface{ Enqueue(ctx context.Context, it store.OutboxItem) (int64, error) }
  type Target struct{ Name, Bot string; ChatID int64 }
  type Alerter struct{ /* unexported */ }
  func New(q Enqueuer, admin *Target, ttl time.Duration, wake func(), log *slog.Logger) *Alerter  // admin nil = log only
  func (a *Alerter) Alert(key, text string)       // dedup: same key at most once per hour
  func (a *Alerter) Recovered(key, text string)   // always sent; clears the key's dedup window
  // a.now is overridable in tests (same package)
  ```

- [ ] **Step 1: Write the failing test** — `internal/alert/alert_test.go`

```go
package alert

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/store"
)

type fakeQ struct{ items []store.OutboxItem }

func (f *fakeQ) Enqueue(_ context.Context, it store.OutboxItem) (int64, error) {
	f.items = append(f.items, it)
	return int64(len(f.items)), nil
}

func TestAlertDedupAndRecovered(t *testing.T) {
	q := &fakeQ{}
	woke := 0
	a := New(q, &Target{Name: "me", Bot: "main", ChatID: 42}, 24*time.Hour, func() { woke++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(1_800_000_000, 0)
	a.now = func() time.Time { return now }

	a.Alert("disconnected:g/INBOX", "斷線 <b>")
	a.Alert("disconnected:g/INBOX", "斷線")
	if len(q.items) != 1 || woke != 1 {
		t.Fatalf("dedup failed: %d items", len(q.items))
	}
	it := q.items[0]
	if it.LogID != 0 || it.Bot != "main" || it.ChatID != 42 || it.Target != "me" || !it.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Errorf("item = %+v", it)
	}
	if !strings.HasPrefix(it.Text, "⚠️ ") || !strings.Contains(it.Text, "&lt;b&gt;") {
		t.Errorf("text not prefixed/escaped: %q", it.Text)
	}
	a.Recovered("disconnected:g/INBOX", "已恢復")
	if len(q.items) != 2 || !strings.HasPrefix(q.items[1].Text, "✅ ") {
		t.Fatalf("recovered not sent: %+v", q.items)
	}
	a.Alert("disconnected:g/INBOX", "又斷線")
	if len(q.items) != 3 {
		t.Fatal("recovered must reset dedup")
	}
	now = now.Add(61 * time.Minute)
	a.Alert("other", "x")
	a.Alert("disconnected:g/INBOX", "還是斷線")
	if len(q.items) != 5 {
		t.Fatalf("after an hour the same key may alert again: %d", len(q.items))
	}
}

func TestNoAdminLogsOnly(t *testing.T) {
	q := &fakeQ{}
	a := New(q, nil, time.Hour, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.Alert("k", "x")
	a.Recovered("k", "y")
	if len(q.items) != 0 {
		t.Fatal("must not enqueue without admin")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/alert/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement `internal/alert/alert.go`**

```go
// Package alert sends system notifications to the optional admin target.
package alert

import (
	"context"
	"html"
	"log/slog"
	"sync"
	"time"

	"verify-code-relay/internal/store"
)

type Enqueuer interface {
	Enqueue(ctx context.Context, it store.OutboxItem) (int64, error)
}

type Target struct {
	Name   string
	Bot    string
	ChatID int64
}

const dedupWindow = time.Hour

type Alerter struct {
	q     Enqueuer
	admin *Target
	ttl   time.Duration
	wake  func()
	log   *slog.Logger
	now   func() time.Time

	mu   sync.Mutex
	last map[string]time.Time
}

func New(q Enqueuer, admin *Target, ttl time.Duration, wake func(), log *slog.Logger) *Alerter {
	if wake == nil {
		wake = func() {}
	}
	return &Alerter{q: q, admin: admin, ttl: ttl, wake: wake, log: log, now: time.Now, last: map[string]time.Time{}}
}

func (a *Alerter) Alert(key, text string) {
	a.mu.Lock()
	now := a.now()
	if t, ok := a.last[key]; ok && now.Sub(t) < dedupWindow {
		a.mu.Unlock()
		return
	}
	a.last[key] = now
	a.mu.Unlock()
	a.log.Warn("告警", "key", key, "text", text)
	a.enqueue("⚠️ " + html.EscapeString(text))
}

func (a *Alerter) Recovered(key, text string) {
	a.mu.Lock()
	delete(a.last, key)
	a.mu.Unlock()
	a.log.Info("已恢復", "key", key, "text", text)
	a.enqueue("✅ " + html.EscapeString(text))
}

func (a *Alerter) enqueue(text string) {
	if a.admin == nil {
		return
	}
	now := a.now()
	_, err := a.q.Enqueue(context.Background(), store.OutboxItem{
		Target: a.admin.Name, Bot: a.admin.Bot, ChatID: a.admin.ChatID, Text: text,
		CreatedAt: now, ExpiresAt: now.Add(a.ttl), NextAttemptAt: now,
	})
	if err != nil {
		a.log.Error("寫入告警失敗", "err", err)
		return
	}
	a.wake()
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/alert/ -v && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/alert
git commit -m "feat(alert): admin alerts with hourly de-duplication and recovery notices

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 12: IMAP authentication (password, XOAUTH2, Microsoft OAuth2 device code + refresh)

**Files:**
- Create: `internal/auth/auth.go`, `internal/auth/microsoft.go`
- Test: `internal/auth/auth_test.go`, `internal/auth/microsoft_test.go`

**Interfaces:**
- Consumes: `store.OAuthToken` (Task 3).
- Produces:
  ```go
  package auth
  type Authenticator interface{ Authenticate(ctx context.Context, c *imapclient.Client) error }
  type Password struct{ Username, Password string }                  // Authenticator
  var ErrNeedsLogin = errors.New("需要重新登入")
  type TokenStore interface {
      GetOAuthToken(ctx context.Context, mailbox string) (store.OAuthToken, bool, error)
      SaveOAuthToken(ctx context.Context, mailbox string, t store.OAuthToken) error
  }
  type Microsoft struct {
      Mailbox, Username, ClientID, Tenant string
      Store     TokenStore
      HTTP      *http.Client      // nil = 30 s default client
      Authority string            // "" = https://login.microsoftonline.com
      Now       func() time.Time  // nil = time.Now
  }                                                                    // Authenticator
  func (m *Microsoft) AccessToken(ctx context.Context) (string, error) // refreshes when <5 min left; ErrNeedsLogin on missing token / invalid_grant
  type DeviceCode struct{ DeviceCode, UserCode, VerificationURI, Message string; ExpiresIn, Interval int }
  func (m *Microsoft) StartDeviceLogin(ctx context.Context) (*DeviceCode, error)
  func (m *Microsoft) PollDeviceLogin(ctx context.Context, dc *DeviceCode) error  // blocks until authorized; saves token
  const Scope = "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"
  func NewXOAUTH2(username, token string) sasl.Client
  ```

- [ ] **Step 1: Write the failing XOAUTH2/password test** — `internal/auth/auth_test.go`

This test uses the in-memory IMAP server (see "Verified library facts" in the header). `imapmemserver` only supports LOGIN, which is enough to test `Password`; XOAUTH2 is unit-tested at the SASL level.

```go
package auth

import (
	"context"
	"net"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func memServer(t *testing.T) string {
	t.Helper()
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("me@example.com", "pw")
	u.Create("INBOX", nil)
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func TestPassword(t *testing.T) {
	addr := memServer(t)
	for _, tc := range []struct {
		pw   string
		want bool
	}{{"pw", true}, {"bad", false}} {
		c, err := imapclient.DialInsecure(addr, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = Password{Username: "me@example.com", Password: tc.pw}.Authenticate(context.Background(), c)
		if (err == nil) != tc.want {
			t.Errorf("pw %q: err = %v", tc.pw, err)
		}
		c.Close()
	}
}

func TestXOAUTH2InitialResponse(t *testing.T) {
	mech, ir, err := NewXOAUTH2("me@outlook.com", "TOKEN").Start()
	if err != nil || mech != "XOAUTH2" {
		t.Fatalf("mech=%q err=%v", mech, err)
	}
	if string(ir) != "user=me@outlook.com\x01auth=Bearer TOKEN\x01\x01" {
		t.Errorf("ir = %q", ir)
	}
	// On failure the server sends a JSON challenge; the client must answer with an empty response.
	resp, err := NewXOAUTH2("u", "t").Next([]byte(`{"status":"401"}`))
	if err != nil || len(resp) != 0 {
		t.Errorf("Next = %q, %v", resp, err)
	}
}
```

- [ ] **Step 2: Write the failing Microsoft test** — `internal/auth/microsoft_test.go`

```go
package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"verify-code-relay/internal/store"
)

type memTokens struct {
	mu sync.Mutex
	m  map[string]store.OAuthToken
}

func (s *memTokens) GetOAuthToken(_ context.Context, mb string) (store.OAuthToken, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.m[mb]
	return t, ok, nil
}

func (s *memTokens) SaveOAuthToken(_ context.Context, mb string, t store.OAuthToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[mb] = t
	return nil
}

type tokenServer struct {
	*httptest.Server
	forms []url.Values
	reply func(form url.Values) (int, string)
}

func newTokenServer(t *testing.T, reply func(url.Values) (int, string)) *tokenServer {
	ts := &tokenServer{reply: reply}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		r.PostForm.Set("_path", r.URL.Path)
		ts.forms = append(ts.forms, r.PostForm)
		code, body := ts.reply(r.PostForm)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts
}

var now0 = time.Unix(1_800_000_000, 0)

func newMS(ts *tokenServer, toks *memTokens) *Microsoft {
	return &Microsoft{Mailbox: "outlook", Username: "me@outlook.com", ClientID: "cid", Tenant: "consumers",
		Store: toks, HTTP: ts.Client(), Authority: ts.URL, Now: func() time.Time { return now0 }}
}

func TestAccessTokenFreshNoRefresh(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 500, "" })
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0.Add(time.Hour)}}}
	tok, err := newMS(ts, toks).AccessToken(context.Background())
	if err != nil || tok != "a" || len(ts.forms) != 0 {
		t.Fatalf("tok=%q err=%v calls=%d", tok, err, len(ts.forms))
	}
}

func TestAccessTokenRefreshRotates(t *testing.T) {
	ts := newTokenServer(t, func(f url.Values) (int, string) {
		return 200, `{"access_token":"a2","refresh_token":"r2","expires_in":3600,"token_type":"Bearer"}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0.Add(4 * time.Minute)}}}
	tok, err := newMS(ts, toks).AccessToken(context.Background())
	if err != nil || tok != "a2" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	f := ts.forms[0]
	if f.Get("_path") != "/consumers/oauth2/v2.0/token" || f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "r" ||
		f.Get("client_id") != "cid" || f.Get("scope") != Scope {
		t.Errorf("form = %v", f)
	}
	saved := toks.m["outlook"]
	if saved.RefreshToken != "r2" || saved.AccessToken != "a2" || !saved.ExpiresAt.Equal(now0.Add(time.Hour)) {
		t.Errorf("saved = %+v", saved)
	}
}

func TestAccessTokenRefreshKeepsOldRefreshTokenWhenNotRotated(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 200, `{"access_token":"a2","expires_in":3600}` })
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", AccessToken: "a", ExpiresAt: now0}}}
	if _, err := newMS(ts, toks).AccessToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if toks.m["outlook"].RefreshToken != "r" {
		t.Error("refresh token lost")
	}
}

func TestAccessTokenInvalidGrantNeedsLogin(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) {
		return 400, `{"error":"invalid_grant","error_description":"AADSTS70000: expired"}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", ExpiresAt: now0}}}
	_, err := newMS(ts, toks).AccessToken(context.Background())
	if !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("err = %v", err)
	}
}

func TestAccessTokenMissingNeedsLogin(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 500, "" })
	_, err := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}}).AccessToken(context.Background())
	if !errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("err = %v", err)
	}
}

func TestAccessTokenServerErrorIsNotNeedsLogin(t *testing.T) {
	ts := newTokenServer(t, func(url.Values) (int, string) { return 503, `{"error":"temporarily_unavailable"}` })
	toks := &memTokens{m: map[string]store.OAuthToken{"outlook": {RefreshToken: "r", ExpiresAt: now0}}}
	_, err := newMS(ts, toks).AccessToken(context.Background())
	if err == nil || errors.Is(err, ErrNeedsLogin) {
		t.Fatalf("want transient error, got %v", err)
	}
}

func TestDeviceLogin(t *testing.T) {
	polls := 0
	ts := newTokenServer(t, func(f url.Values) (int, string) {
		if f.Get("_path") == "/consumers/oauth2/v2.0/devicecode" {
			return 200, `{"device_code":"DC","user_code":"ABCD-EFGH","verification_uri":"https://microsoft.com/devicelogin","expires_in":900,"interval":0,"message":"go to url"}`
		}
		polls++
		if polls < 3 {
			return 400, `{"error":"authorization_pending"}`
		}
		return 200, `{"access_token":"a","refresh_token":"r","expires_in":3600}`
	})
	toks := &memTokens{m: map[string]store.OAuthToken{}}
	m := newMS(ts, toks)
	dc, err := m.StartDeviceLogin(context.Background())
	if err != nil || dc.UserCode != "ABCD-EFGH" || dc.Message != "go to url" {
		t.Fatalf("dc=%+v err=%v", dc, err)
	}
	if ts.forms[0].Get("scope") != Scope || ts.forms[0].Get("client_id") != "cid" {
		t.Errorf("devicecode form = %v", ts.forms[0])
	}
	if err := m.PollDeviceLogin(context.Background(), dc); err != nil {
		t.Fatal(err)
	}
	last := ts.forms[len(ts.forms)-1]
	if last.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || last.Get("device_code") != "DC" {
		t.Errorf("poll form = %v", last)
	}
	if toks.m["outlook"].RefreshToken != "r" {
		t.Errorf("token not saved: %+v", toks.m)
	}
}

func TestDeviceLoginDeclined(t *testing.T) {
	ts := newTokenServer(t, func(f url.Values) (int, string) { return 400, `{"error":"authorization_declined"}` })
	m := newMS(ts, &memTokens{m: map[string]store.OAuthToken{}})
	err := m.PollDeviceLogin(context.Background(), &DeviceCode{DeviceCode: "DC", ExpiresIn: 900})
	if err == nil {
		t.Fatal("want error")
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go get github.com/emersion/go-imap/v2@v2.0.0-beta.8 github.com/emersion/go-sasl@latest && go test ./internal/auth/ -v`
Expected: FAIL — `undefined: Password`, `NewXOAUTH2`, `Microsoft`.

- [ ] **Step 4: Implement `internal/auth/auth.go`**

```go
// Package auth authenticates IMAP connections.
package auth

import (
	"context"
	"errors"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
)

type Authenticator interface {
	Authenticate(ctx context.Context, c *imapclient.Client) error
}

var ErrNeedsLogin = errors.New("需要重新登入")

type Password struct {
	Username string
	Password string
}

func (p Password) Authenticate(_ context.Context, c *imapclient.Client) error {
	return c.Login(p.Username, p.Password).Wait()
}

type xoauth2 struct{ username, token string }

// NewXOAUTH2 returns a SASL XOAUTH2 client (go-sasl does not ship one).
func NewXOAUTH2(username, token string) sasl.Client { return &xoauth2{username, token} }

func (x *xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.username + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

// Next answers the server's JSON error challenge with an empty response so the
// server completes the exchange with a tagged NO.
func (x *xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }
```

- [ ] **Step 5: Implement `internal/auth/microsoft.go`**

```go
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/store"
)

const Scope = "https://outlook.office.com/IMAP.AccessAsUser.All offline_access"

const refreshMargin = 5 * time.Minute

type TokenStore interface {
	GetOAuthToken(ctx context.Context, mailbox string) (store.OAuthToken, bool, error)
	SaveOAuthToken(ctx context.Context, mailbox string, t store.OAuthToken) error
}

type Microsoft struct {
	Mailbox   string
	Username  string
	ClientID  string
	Tenant    string
	Store     TokenStore
	HTTP      *http.Client
	Authority string
	Now       func() time.Time
}

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Message         string `json:"message"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (m *Microsoft) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Microsoft) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (m *Microsoft) endpoint(name string) string {
	authority := m.Authority
	if authority == "" {
		authority = "https://login.microsoftonline.com"
	}
	return strings.TrimRight(authority, "/") + "/" + url.PathEscape(m.Tenant) + "/oauth2/v2.0/" + name
}

func (m *Microsoft) post(ctx context.Context, name string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint(name), strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := m.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(body, out); err != nil && resp.StatusCode == http.StatusOK {
		return resp.StatusCode, fmt.Errorf("解析 %s 回應：%w", name, err)
	}
	return resp.StatusCode, nil
}

func (m *Microsoft) save(ctx context.Context, tr tokenResponse, oldRefresh string) error {
	refresh := tr.RefreshToken
	if refresh == "" {
		refresh = oldRefresh
	}
	return m.Store.SaveOAuthToken(ctx, m.Mailbox, store.OAuthToken{
		RefreshToken: refresh, AccessToken: tr.AccessToken,
		ExpiresAt: m.now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	})
}

func (m *Microsoft) AccessToken(ctx context.Context) (string, error) {
	tok, ok, err := m.Store.GetOAuthToken(ctx, m.Mailbox)
	if err != nil {
		return "", err
	}
	if !ok || tok.RefreshToken == "" {
		return "", fmt.Errorf("信箱 %s 尚未授權：%w", m.Mailbox, ErrNeedsLogin)
	}
	if tok.AccessToken != "" && tok.ExpiresAt.Sub(m.now()) > refreshMargin {
		return tok.AccessToken, nil
	}
	var tr tokenResponse
	status, err := m.post(ctx, "token", url.Values{
		"client_id":     {m.ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {tok.RefreshToken},
		"scope":         {Scope},
	}, &tr)
	if err != nil {
		return "", fmt.Errorf("更新 OAuth token：%w", err)
	}
	if tr.Error == "invalid_grant" || tr.Error == "interaction_required" {
		return "", fmt.Errorf("信箱 %s 的授權已失效（%s）：%w", m.Mailbox, tr.Error, ErrNeedsLogin)
	}
	if status != http.StatusOK || tr.AccessToken == "" {
		return "", fmt.Errorf("更新 OAuth token 失敗：HTTP %d %s", status, tr.Error)
	}
	if err := m.save(ctx, tr, tok.RefreshToken); err != nil {
		return "", err
	}
	return tr.AccessToken, nil
}

func (m *Microsoft) Authenticate(ctx context.Context, c *imapclient.Client) error {
	tok, err := m.AccessToken(ctx)
	if err != nil {
		return err
	}
	return c.Authenticate(NewXOAUTH2(m.Username, tok))
}

func (m *Microsoft) StartDeviceLogin(ctx context.Context) (*DeviceCode, error) {
	var dc DeviceCode
	status, err := m.post(ctx, "devicecode", url.Values{"client_id": {m.ClientID}, "scope": {Scope}}, &dc)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK || dc.DeviceCode == "" {
		return nil, fmt.Errorf("取得 device code 失敗：HTTP %d（請確認 client_id、tenant 與「允許公開用戶端流程」設定）", status)
	}
	return &dc, nil
}

func (m *Microsoft) PollDeviceLogin(ctx context.Context, dc *DeviceCode) error {
	interval := time.Duration(dc.Interval) * time.Second
	deadline := m.now().Add(time.Duration(dc.ExpiresIn) * time.Second)
	for {
		var tr tokenResponse
		status, err := m.post(ctx, "token", url.Values{
			"client_id":   {m.ClientID},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {dc.DeviceCode},
		}, &tr)
		if err != nil {
			return err
		}
		switch {
		case status == http.StatusOK && tr.AccessToken != "":
			if tr.RefreshToken == "" {
				return fmt.Errorf("沒有取得 refresh token（scope 需要包含 offline_access）")
			}
			return m.save(ctx, tr, "")
		case tr.Error == "authorization_pending":
		case tr.Error == "slow_down":
			interval += 5 * time.Second
		default:
			return fmt.Errorf("授權失敗：%s %s", tr.Error, tr.ErrorDescription)
		}
		if m.now().After(deadline) {
			return fmt.Errorf("授權逾時，請重新執行 relay login")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
```

- [ ] **Step 6: Run tests — expect PASS**

Run: `go test ./internal/auth/ -v && go vet ./...`
Expected: PASS. `TestDeviceLogin` uses `interval: 0` so polling is immediate.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/auth
git commit -m "feat(auth): password login, XOAUTH2 SASL and Microsoft device-code OAuth with refresh

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 13: Pipeline (decide / skip / process one fetched message)

**Files:**
- Create: `internal/pipeline/pipeline.go`, `internal/pipeline/recipient.go`
- Test: `internal/pipeline/pipeline_test.go`, `internal/pipeline/recipient_test.go`

**Interfaces:**
- Consumes: `store.Store` methods / types (Task 3), `mailparse.Parse` (Task 4), `recognize.Recognizer` + `recognize.New` (Tasks 5–6), `render.Renderer`, `render.Data` (Task 8).
- Produces:
  ```go
  package pipeline
  const MaxMessageSize = 5 << 20
  type Store interface {
      RecordMessage(ctx context.Context, e store.LogEntry, items []store.OutboxItem, st store.MailboxState) (int64, error)
  }
  type Renderer interface{ Render(mailbox string, d render.Data) (render.Rendered, error) }
  type Target struct{ Name, Bot string; ChatID int64 }
  type Mailbox struct {
      Name, DisplayName, Username string
      Targets                     []Target
      MaxAge, TTL                 time.Duration
  }
  type Envelope struct {
      UID          uint32
      InternalDate time.Time
      Size         int64
      From, Subject string   // from the IMAP ENVELOPE; used for skipped-message logs
  }
  type Processor struct {
      Store      Store
      Recognizer recognize.Recognizer
      Renderer   Renderer
      Notify     func()            // wake the dispatcher; may be nil
      Log        *slog.Logger
      Now        func() time.Time  // nil = time.Now
  }
  func (p *Processor) Decide(mb Mailbox, env Envelope) string // "" | "skipped_size" | "skipped_age"
  func (p *Processor) RecordSkipped(ctx context.Context, mb Mailbox, folder string, uidValidity uint32, env Envelope, kind string) error
  func (p *Processor) Process(ctx context.Context, mb Mailbox, folder string, uidValidity uint32, env Envelope, raw []byte) error
  func Recipient(to []mailparse.Address, username string) string
  ```
  Every call to `RecordSkipped`/`Process` advances `mailbox_state(mb.Name, folder)` to `{uidValidity, env.UID}` in the same transaction as the log/outbox writes. A returned error means nothing was written (the watcher will retry the UID after reconnecting).

- [ ] **Step 1: Write the failing recipient test** — `internal/pipeline/recipient_test.go`

```go
package pipeline

import (
	"testing"

	"verify-code-relay/internal/mailparse"
)

func TestRecipient(t *testing.T) {
	addrs := func(list ...string) []mailparse.Address {
		var out []mailparse.Address
		for _, a := range list {
			out = append(out, mailparse.Address{Addr: a})
		}
		return out
	}
	cases := []struct {
		to       []mailparse.Address
		username string
		want     string
	}{
		{addrs("other@x.com", "Me+GitHub@gmail.com"), "me@gmail.com", "Me+GitHub@gmail.com"},
		{addrs("m.e+shop@gmail.com"), "me@gmail.com", "m.e+shop@gmail.com"},
		{addrs("m.e@outlook.com"), "me@outlook.com", "m.e@outlook.com"}, // no match: dots matter outside gmail -> fallback first
		{addrs("list@lists.x.org"), "me@example.com", "list@lists.x.org"},
		{nil, "me@example.com", ""},
		{addrs("me+a@example.com"), "me@example.com", "me+a@example.com"},
	}
	for _, c := range cases {
		if got := Recipient(c.to, c.username); got != c.want {
			t.Errorf("Recipient(%v, %q) = %q, want %q", c.to, c.username, got, c.want)
		}
	}
	// a matching address wins over an earlier non-matching one
	if got := Recipient(addrs("a@b.c", "me+x@example.com"), "me@example.com"); got != "me+x@example.com" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Implement `internal/pipeline/recipient.go`**

```go
package pipeline

import (
	"strings"

	"verify-code-relay/internal/mailparse"
)

func splitAddr(a string) (local, domain string) {
	a = strings.ToLower(strings.TrimSpace(a))
	at := strings.LastIndexByte(a, '@')
	if at < 0 {
		return a, ""
	}
	return a[:at], a[at+1:]
}

func canonicalLocal(local, domain string) string {
	if i := strings.IndexByte(local, '+'); i >= 0 {
		local = local[:i]
	}
	if domain == "gmail.com" || domain == "googlemail.com" {
		local = strings.ReplaceAll(local, ".", "")
	}
	return local
}

// Recipient returns the recipient address that belongs to the mailbox account
// (so +aliases are visible), falling back to the first recipient.
func Recipient(to []mailparse.Address, username string) string {
	uLocal, uDomain := splitAddr(username)
	want := canonicalLocal(uLocal, uDomain)
	for _, a := range to {
		l, d := splitAddr(a.Addr)
		if d == uDomain && canonicalLocal(l, d) == want {
			return a.Addr
		}
	}
	if len(to) > 0 {
		return to[0].Addr
	}
	return ""
}
```

- [ ] **Step 3: Run recipient test — expect PASS**

Run: `go test ./internal/pipeline/ -run TestRecipient -v`
Expected: PASS.

- [ ] **Step 4: Write the failing processor test** — `internal/pipeline/pipeline_test.go`

```go
package pipeline

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/recognize"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/store"
)

var ctx = context.Background()

func newProc(t *testing.T) (*Processor, *store.Store, *int, time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rec, err := recognize.New([]config.Rule{{Name: "promo", From: "*@news.example.com", Ignore: true}}, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Mailboxes: map[string]*config.Mailbox{"gmail": {Name: "gmail"}}}
	rnd, err := render.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	notified := 0
	now := time.Unix(1_800_000_000, 0)
	p := &Processor{Store: st, Recognizer: rec, Renderer: rnd, Notify: func() { notified++ },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	return p, st, &notified, now
}

var mb = Mailbox{
	Name: "gmail", DisplayName: "個人 Gmail", Username: "me@gmail.com",
	Targets: []Target{{Name: "me", Bot: "main", ChatID: 1}, {Name: "fam", Bot: "main", ChatID: 2}},
	MaxAge:  10 * time.Minute,
	TTL:     15 * time.Minute,
}

func eml(from, to, subject, body string) []byte {
	return []byte(strings.ReplaceAll("From: "+from+"\nTo: "+to+"\nSubject: "+subject+"\nContent-Type: text/plain; charset=utf-8\n\n"+body+"\n", "\n", "\r\n"))
}

func TestProcessCodeEnqueuesPerTarget(t *testing.T) {
	p, st, notified, now := newProc(t)
	raw := eml("GitHub <noreply@github.com>", "me+gh@gmail.com", "[GitHub] Please verify your device", "Verification code: 482913")
	err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 11, InternalDate: now, Size: int64(len(raw))}, raw)
	if err != nil {
		t.Fatal(err)
	}
	items, _ := st.DueOutbox(ctx, now, 10)
	if len(items) != 2 || *notified != 1 {
		t.Fatalf("items=%d notified=%d", len(items), *notified)
	}
	it := items[0]
	if !strings.Contains(it.Text, "<code>482913</code>") || !strings.Contains(it.Text, "個人 Gmail") || it.ReplyMarkup == "" {
		t.Errorf("text = %q markup = %q", it.Text, it.ReplyMarkup)
	}
	if !it.ExpiresAt.Equal(now.Add(15*time.Minute)) || it.Bot != "main" || it.LogID == 0 {
		t.Errorf("item = %+v", it)
	}
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if s != (store.MailboxState{UIDValidity: 7, LastUID: 11}) {
		t.Errorf("state = %+v", s)
	}
}

func TestProcessNoMatchAdvancesState(t *testing.T) {
	p, st, notified, now := newProc(t)
	raw := eml("a@b.c", "me@gmail.com", "Hello", "Just saying hi")
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 12, InternalDate: now}, raw); err != nil {
		t.Fatal(err)
	}
	items, _ := st.DueOutbox(ctx, now, 10)
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if len(items) != 0 || *notified != 0 || s.LastUID != 12 {
		t.Fatalf("items=%d notified=%d state=%+v", len(items), *notified, s)
	}
}

func TestProcessIgnored(t *testing.T) {
	p, st, _, now := newProc(t)
	raw := eml("promo@news.example.com", "me@gmail.com", "Your code is SAVE2026", "promo code SAVE2026")
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 13, InternalDate: now}, raw); err != nil {
		t.Fatal(err)
	}
	items, _ := st.DueOutbox(ctx, now, 10)
	if len(items) != 0 {
		t.Fatalf("ignored mail forwarded: %+v", items)
	}
}

func TestProcessUnparseableAdvancesState(t *testing.T) {
	p, st, _, now := newProc(t)
	if err := p.Process(ctx, mb, "INBOX", 7, Envelope{UID: 14, InternalDate: now, From: "x@y.z", Subject: "s"}, []byte("\x00\x01 not mail")); err != nil {
		t.Fatal(err)
	}
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if s.LastUID != 14 {
		t.Fatalf("state = %+v", s)
	}
}

func TestDecideAndRecordSkipped(t *testing.T) {
	p, st, _, now := newProc(t)
	cases := []struct {
		env  Envelope
		want string
	}{
		{Envelope{UID: 1, InternalDate: now.Add(-time.Minute), Size: 1000}, ""},
		{Envelope{UID: 2, InternalDate: now.Add(-11 * time.Minute), Size: 1000}, "skipped_age"},
		{Envelope{UID: 3, InternalDate: now, Size: MaxMessageSize + 1}, "skipped_size"},
	}
	for _, c := range cases {
		if got := p.Decide(mb, c.env); got != c.want {
			t.Errorf("Decide(uid %d) = %q, want %q", c.env.UID, got, c.want)
		}
	}
	if err := p.RecordSkipped(ctx, mb, "INBOX", 7, Envelope{UID: 2, From: "a@b.c", Subject: "old"}, "skipped_age"); err != nil {
		t.Fatal(err)
	}
	s, _, _ := st.GetMailboxState(ctx, "gmail", "INBOX")
	if s.LastUID != 2 {
		t.Fatalf("state = %+v", s)
	}
}
```

- [ ] **Step 5: Run to verify failure**

Run: `go test ./internal/pipeline/ -v`
Expected: FAIL — `undefined: Processor`.

- [ ] **Step 6: Implement `internal/pipeline/pipeline.go`**

```go
// Package pipeline turns one fetched message into forward_log + outbox rows.
package pipeline

import (
	"context"
	"log/slog"
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
```

- [ ] **Step 7: Run tests — expect PASS**

Run: `go test ./internal/pipeline/ -v && go vet ./...`
Expected: PASS. If `TestProcessUnparseableAdvancesState` finds that `mailparse.Parse` does *not* error on that input, the test still passes (the message is then treated as unrecognized) — that's fine, the assertion is only on the state.

- [ ] **Step 8: Commit**

```bash
git add internal/pipeline
git commit -m "feat(pipeline): process fetched messages into forward log and outbox atomically

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 14: Watcher — one IMAP session (select, watermark, catch-up, IDLE/poll, liveness)

**Files:**
- Create: `internal/watcher/watcher.go` (types, `New`, `Key`, `Status`), `internal/watcher/session.go`
- Test: `internal/watcher/testutil_test.go` (shared fakes + in-memory server), `internal/watcher/session_test.go`

**Interfaces:**
- Consumes: `pipeline.Mailbox`, `pipeline.Envelope` (Task 13), `store.MailboxState` (Task 3), `auth.Authenticator`, `auth.ErrNeedsLogin` (Task 12).
- Produces:
  ```go
  package watcher
  type Status string
  const (StatusConnecting, StatusConnected, StatusReconnecting, StatusAuthFailed, StatusNeedsLogin, StatusStopped Status = ...)
  type DialFunc func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error)
  func TLSDialer(addr string) DialFunc
  type Processor interface {
      Decide(mb pipeline.Mailbox, env pipeline.Envelope) string
      RecordSkipped(ctx context.Context, mb pipeline.Mailbox, folder string, uidValidity uint32, env pipeline.Envelope, kind string) error
      Process(ctx context.Context, mb pipeline.Mailbox, folder string, uidValidity uint32, env pipeline.Envelope, raw []byte) error
  }                                                    // *pipeline.Processor satisfies this
  type StateStore interface {
      GetMailboxState(ctx context.Context, mailbox, folder string) (store.MailboxState, bool, error)
      SetMailboxState(ctx context.Context, mailbox, folder string, st store.MailboxState) error
  }                                                    // *store.Store satisfies this
  type Alerter interface{ Alert(key, text string); Recovered(key, text string) }   // *alert.Alerter satisfies this
  type Config struct {
      Mailbox pipeline.Mailbox; Folder string
      Dial DialFunc; Auth auth.Authenticator
      Processor Processor; State StateStore; Alert Alerter; Log *slog.Logger
      PollInterval, AlertAfter time.Duration
      IdleCycle, CmdTimeout, FetchTimeout, MinBackoff, MaxBackoff time.Duration // zero = 25m, 30s, 2m, 1s, 5m
  }
  type AuthError struct{ Err error }                 // returned by session when authentication fails
  func New(cfg Config) *Watcher
  func (w *Watcher) Key() string                     // "mailbox/folder"
  func (w *Watcher) Status() Status
  // session.go (unexported, used by Task 15's Run):
  func (w *Watcher) session(ctx context.Context, connected func()) error
  ```

- [ ] **Step 1: Write the shared test utilities** — `internal/watcher/testutil_test.go`

```go
package watcher

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/store"
)

type testServer struct {
	addr string
	user *imapmemserver.User
	srv  *imapserver.Server
}

func startServer(t *testing.T, caps imap.CapSet) *testServer {
	t.Helper()
	if caps == nil {
		caps = imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}}
	}
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("me@example.com", "pw")
	u.Create("INBOX", nil)
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         caps,
		InsecureAuth: true,
		Logger:       nopLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &testServer{addr: ln.Addr().String(), user: u, srv: srv}
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

func (s *testServer) appendMsg(t *testing.T, subject string) {
	t.Helper()
	c, err := imapclient.DialInsecure(s.addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Login("me@example.com", "pw").Wait(); err != nil {
		t.Fatal(err)
	}
	msg := strings.ReplaceAll("From: a@b.c\nTo: me@example.com\nSubject: "+subject+"\n\nbody\n", "\n", "\r\n")
	cmd := c.Append("INBOX", int64(len(msg)), nil)
	cmd.Write([]byte(msg))
	cmd.Close()
	if _, err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func (s *testServer) seenFlags(t *testing.T) int {
	t.Helper()
	c, err := imapclient.DialInsecure(s.addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Login("me@example.com", "pw").Wait()
	c.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	var all imap.SeqSet
	all.AddRange(1, 0) // 1:*
	bufs, err := c.Fetch(all, &imap.FetchOptions{Flags: true}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range bufs {
		for _, f := range b.Flags {
			if f == imap.FlagSeen {
				n++
			}
		}
	}
	return n
}

// ctlConn lets tests kill a connection or turn it into a black hole.
type ctlConn struct {
	net.Conn
	frozen atomic.Bool
}

func (c *ctlConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if err != nil || !c.frozen.Load() {
			return n, err
		}
	}
}

func (c *ctlConn) Write(p []byte) (int, error) {
	if c.frozen.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

type testDialer struct {
	addr  string
	fail  atomic.Bool
	count atomic.Int32
	mu    sync.Mutex
	conns []*ctlConn
}

func (d *testDialer) dial(_ context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
	d.count.Add(1)
	if d.fail.Load() {
		return nil, errors.New("dial refused")
	}
	nc, err := net.Dial("tcp", d.addr)
	if err != nil {
		return nil, err
	}
	cc := &ctlConn{Conn: nc}
	d.mu.Lock()
	d.conns = append(d.conns, cc)
	d.mu.Unlock()
	return imapclient.New(cc, opts), nil
}

func (d *testDialer) last() *ctlConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[len(d.conns)-1]
}

type fakeProc struct {
	st        *store.Store
	mu        sync.Mutex
	processed []uint32
	skipped   []uint32
	skip      map[uint32]string
	failOnce  map[uint32]bool
}

func (f *fakeProc) Decide(_ pipeline.Mailbox, env pipeline.Envelope) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.skip[env.UID]
}

func (f *fakeProc) RecordSkipped(ctx context.Context, mb pipeline.Mailbox, folder string, uv uint32, env pipeline.Envelope, _ string) error {
	f.mu.Lock()
	f.skipped = append(f.skipped, env.UID)
	f.mu.Unlock()
	return f.st.SetMailboxState(ctx, mb.Name, folder, store.MailboxState{UIDValidity: uv, LastUID: env.UID})
}

func (f *fakeProc) Process(ctx context.Context, mb pipeline.Mailbox, folder string, uv uint32, env pipeline.Envelope, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOnce[env.UID] {
		delete(f.failOnce, env.UID)
		return errors.New("boom")
	}
	if !strings.Contains(string(raw), "Subject:") {
		return errors.New("body missing")
	}
	f.processed = append(f.processed, env.UID)
	return f.st.SetMailboxState(ctx, mb.Name, folder, store.MailboxState{UIDValidity: uv, LastUID: env.UID})
}

func (f *fakeProc) got() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.processed...)
}

type fakeAlerts struct {
	mu        sync.Mutex
	alerts    []string
	recovered []string
}

func (a *fakeAlerts) Alert(key, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, key)
}

func (a *fakeAlerts) Recovered(key, _ string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recovered = append(a.recovered, key)
}

func (a *fakeAlerts) snapshot() ([]string, []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.alerts...), append([]string(nil), a.recovered...)
}

type env struct {
	srv    *testServer
	dialer *testDialer
	proc   *fakeProc
	st     *store.Store
	alerts *fakeAlerts
	w      *Watcher
}

func newEnv(t *testing.T, caps imap.CapSet, mutate func(*Config)) *env {
	t.Helper()
	srv := startServer(t, caps)
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{srv: srv, dialer: &testDialer{addr: srv.addr}, proc: &fakeProc{st: st, skip: map[uint32]string{}, failOnce: map[uint32]bool{}},
		st: st, alerts: &fakeAlerts{}}
	cfg := Config{
		Mailbox:      pipeline.Mailbox{Name: "mb", Username: "me@example.com"},
		Folder:       "INBOX",
		Dial:         e.dialer.dial,
		Auth:         auth.Password{Username: "me@example.com", Password: "pw"},
		Processor:    e.proc,
		State:        st,
		Alert:        e.alerts,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		PollInterval: 100 * time.Millisecond,
		AlertAfter:   time.Hour,
		CmdTimeout:   2 * time.Second,
		FetchTimeout: 2 * time.Second,
		MinBackoff:   20 * time.Millisecond,
		MaxBackoff:   100 * time.Millisecond,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	e.w = New(cfg)
	return e
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func equalUIDs(a []uint32, b ...uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: Write the failing session tests** — `internal/watcher/session_test.go`

These call `w.session` directly in a goroutine; `connected` is a channel-closing callback.

```go
package watcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/store"
)

func runSession(t *testing.T, e *env) (cancel func(), connected chan struct{}, done chan error) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(context.Background())
	connected = make(chan struct{})
	done = make(chan error, 1)
	go func() {
		done <- e.w.session(ctx, func() { close(connected) })
	}()
	select {
	case <-connected:
	case err := <-done:
		t.Fatalf("session ended before connecting: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("not connected")
	}
	return func() {
		cancelFn()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("session did not stop")
		}
	}, connected, done
}

func TestFirstRunSkipsBacklogThenIdleDelivers(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.srv.appendMsg(t, "old") // uid 1: backlog, must not be processed
	stop, _, _ := runSession(t, e)
	defer stop()
	st, ok, _ := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if !ok || st.LastUID != 1 || st.UIDValidity == 0 {
		t.Fatalf("watermark = %+v ok=%v", st, ok)
	}
	e.srv.appendMsg(t, "new") // uid 2 arrives during IDLE
	eventually(t, "uid 2 processed", func() bool { return equalUIDs(e.proc.got(), 2) })
	if n := e.srv.seenFlags(t); n != 0 {
		t.Errorf("%d messages marked \\Seen; mailbox must stay untouched", n)
	}
}

func TestRestartCatchesUp(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop, _, _ := runSession(t, e)
	stop()
	e.srv.appendMsg(t, "while down 1")
	e.srv.appendMsg(t, "while down 2")
	stop, _, _ = runSession(t, e)
	defer stop()
	eventually(t, "uids 1,2 processed", func() bool { return equalUIDs(e.proc.got(), 1, 2) })
}

func TestUIDValidityChangeResetsWatermark(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.srv.appendMsg(t, "a")
	e.st.SetMailboxState(context.Background(), "mb", "INBOX", store.MailboxState{UIDValidity: 999, LastUID: 0})
	stop, _, _ := runSession(t, e)
	defer stop()
	st, _, _ := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if st.UIDValidity == 999 || st.LastUID != 1 || len(e.proc.got()) != 0 {
		t.Fatalf("state=%+v processed=%v", st, e.proc.got())
	}
}

func TestDecideSkipsWithoutFetchingBody(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop, _, _ := runSession(t, e)
	defer stop()
	e.proc.mu.Lock()
	e.proc.skip[1] = "skipped_age"
	e.proc.mu.Unlock()
	e.srv.appendMsg(t, "old one")
	eventually(t, "uid 1 skipped", func() bool {
		e.proc.mu.Lock()
		defer e.proc.mu.Unlock()
		return equalUIDs(e.proc.skipped, 1)
	})
	e.srv.appendMsg(t, "fresh")
	eventually(t, "uid 2 processed", func() bool { return equalUIDs(e.proc.got(), 2) })
}

func TestBurstWhileBusyIsDrained(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop, _, _ := runSession(t, e)
	defer stop()
	for i := 0; i < 5; i++ {
		e.srv.appendMsg(t, "burst") // some land while the watcher is fetching, not idling
	}
	eventually(t, "all 5 processed", func() bool { return equalUIDs(e.proc.got(), 1, 2, 3, 4, 5) })
}

func TestPollingWhenNoIdle(t *testing.T) {
	e := newEnv(t, imap.CapSet{imap.CapIMAP4rev1: {}}, nil)
	stop, _, _ := runSession(t, e)
	defer stop()
	e.srv.appendMsg(t, "polled")
	eventually(t, "uid 1 processed by polling", func() bool { return equalUIDs(e.proc.got(), 1) })
}

func TestIdleCycleKeepsHealthyConnection(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.IdleCycle = 50 * time.Millisecond })
	stop, _, done := runSession(t, e)
	defer stop()
	time.Sleep(300 * time.Millisecond) // several Close->NOOP->Idle cycles
	select {
	case err := <-done:
		t.Fatalf("session died on healthy connection: %v", err)
	default:
	}
	e.srv.appendMsg(t, "after cycles")
	eventually(t, "uid 1 processed", func() bool { return equalUIDs(e.proc.got(), 1) })
}

func TestBlackHoleConnectionIsDetected(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.IdleCycle = 100 * time.Millisecond
		c.CmdTimeout = 200 * time.Millisecond
	})
	_, _, done := runSession(t, e)
	e.dialer.last().frozen.Store(true)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dead connection not detected")
	}
}

func TestProcessErrorEndsSession(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.proc.failOnce[1] = true
	_, _, done := runSession(t, e)
	e.srv.appendMsg(t, "will fail")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session should end on processing error")
	}
	st, _, _ := e.st.GetMailboxState(context.Background(), "mb", "INBOX")
	if st.LastUID != 0 {
		t.Errorf("watermark advanced past failed uid: %+v", st)
	}
}

func TestAuthFailureReturnsAuthError(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.w.cfg.Auth = auth.Password{Username: "me@example.com", Password: "nope"}
	err := e.w.session(context.Background(), func() { t.Error("must not connect") })
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("want AuthError, got %T %v", err, err)
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/watcher/ -v`
Expected: FAIL — `undefined: New`, `Config`, ...

- [ ] **Step 4: Implement `internal/watcher/watcher.go`** (types only; `Run` comes in Task 15)

```go
// Package watcher keeps one IMAP connection per (mailbox, folder) and feeds new
// mail to the pipeline.
package watcher

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/store"
)

type Status string

const (
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusReconnecting Status = "reconnecting"
	StatusAuthFailed   Status = "auth_failed"
	StatusNeedsLogin   Status = "needs_login"
	StatusStopped      Status = "stopped"
)

type DialFunc func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error)

// TLSDialer dials implicit TLS with TCP keepalive.
func TLSDialer(addr string) DialFunc {
	return func(_ context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
		o := *opts
		o.Dialer = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 60 * time.Second}
		o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		return imapclient.DialTLS(addr, &o)
	}
}

type Processor interface {
	Decide(mb pipeline.Mailbox, env pipeline.Envelope) string
	RecordSkipped(ctx context.Context, mb pipeline.Mailbox, folder string, uidValidity uint32, env pipeline.Envelope, kind string) error
	Process(ctx context.Context, mb pipeline.Mailbox, folder string, uidValidity uint32, env pipeline.Envelope, raw []byte) error
}

type StateStore interface {
	GetMailboxState(ctx context.Context, mailbox, folder string) (store.MailboxState, bool, error)
	SetMailboxState(ctx context.Context, mailbox, folder string, st store.MailboxState) error
}

type Alerter interface {
	Alert(key, text string)
	Recovered(key, text string)
}

type Config struct {
	Mailbox   pipeline.Mailbox
	Folder    string
	Dial      DialFunc
	Auth      auth.Authenticator
	Processor Processor
	State     StateStore
	Alert     Alerter
	Log       *slog.Logger

	PollInterval time.Duration
	AlertAfter   time.Duration

	IdleCycle    time.Duration
	CmdTimeout   time.Duration
	FetchTimeout time.Duration
	MinBackoff   time.Duration
	MaxBackoff   time.Duration
}

type AuthError struct{ Err error }

func (e *AuthError) Error() string { return "認證失敗：" + e.Err.Error() }
func (e *AuthError) Unwrap() error { return e.Err }

type Watcher struct {
	cfg    Config
	log    *slog.Logger
	status atomic.Value
	now    func() time.Time
}

func orDefault(v *time.Duration, def time.Duration) {
	if *v == 0 {
		*v = def
	}
}

func New(cfg Config) *Watcher {
	orDefault(&cfg.IdleCycle, 25*time.Minute)
	orDefault(&cfg.CmdTimeout, 30*time.Second)
	orDefault(&cfg.FetchTimeout, 2*time.Minute)
	orDefault(&cfg.MinBackoff, time.Second)
	orDefault(&cfg.MaxBackoff, 5*time.Minute)
	orDefault(&cfg.PollInterval, 30*time.Second)
	orDefault(&cfg.AlertAfter, 10*time.Minute)
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	w := &Watcher{cfg: cfg, now: time.Now}
	w.log = cfg.Log.With("mailbox", cfg.Mailbox.Name, "folder", cfg.Folder)
	w.status.Store(StatusConnecting)
	return w
}

func (w *Watcher) Key() string { return w.cfg.Mailbox.Name + "/" + w.cfg.Folder }

func (w *Watcher) Status() Status { return w.status.Load().(Status) }

func (w *Watcher) setStatus(s Status) { w.status.Store(s) }
```

- [ ] **Step 5: Implement `internal/watcher/session.go`**

```go
package watcher

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/store"
)

var errDead = errors.New("IMAP 伺服器無回應")

// await runs f and gives up after d, closing the client so that f's pending
// command is released. IDLE has no read deadline in imapclient, so every
// command we issue goes through await.
func (w *Watcher) await(c *imapclient.Client, d time.Duration, f func() error) error {
	done := make(chan error, 1)
	go func() { done <- f() }()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		c.Close()
		return errDead
	}
}

// session runs one connection until it fails or ctx is cancelled. connected is
// called once the folder is selected and the watermark is initialised.
func (w *Watcher) session(ctx context.Context, connected func()) error {
	sig := make(chan struct{}, 1)
	opts := &imapclient.Options{UnilateralDataHandler: &imapclient.UnilateralDataHandler{
		// Runs on the client's read goroutine: only signal, never call client methods here.
		Mailbox: func(d *imapclient.UnilateralDataMailbox) {
			if d.NumMessages != nil {
				select {
				case sig <- struct{}{}:
				default:
				}
			}
		},
	}}
	c, err := w.cfg.Dial(ctx, opts)
	if err != nil {
		return fmt.Errorf("連線：%w", err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	if err := w.await(c, w.cfg.FetchTimeout, func() error { return w.cfg.Auth.Authenticate(ctx, c) }); err != nil {
		var ie *imap.Error
		if errors.As(err, &ie) || errors.Is(err, auth.ErrNeedsLogin) {
			return &AuthError{Err: err}
		}
		return fmt.Errorf("認證：%w", err)
	}

	mb, folder := w.cfg.Mailbox, w.cfg.Folder
	var sd *imap.SelectData
	if err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		sd, err = c.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
		return err
	}); err != nil {
		return fmt.Errorf("選取資料夾 %s：%w", folder, err)
	}
	uidNext, err := w.uidNext(c, sd)
	if err != nil {
		return err
	}
	st, ok, err := w.cfg.State.GetMailboxState(ctx, mb.Name, folder)
	if err != nil {
		return err
	}
	if !ok || st.UIDValidity != sd.UIDValidity {
		st = store.MailboxState{UIDValidity: sd.UIDValidity, LastUID: uidNext - 1}
		if err := w.cfg.State.SetMailboxState(ctx, mb.Name, folder, st); err != nil {
			return err
		}
		w.log.Info("初始化信箱狀態，既有信件不轉發", "uidvalidity", st.UIDValidity, "last_uid", st.LastUID)
	}
	connected()
	if err := w.drain(ctx, c, &st); err != nil {
		return err
	}

	useIdle := c.Caps().Has(imap.CapIdle) || c.Caps().Has(imap.CapIMAP4rev2)
	if !useIdle {
		w.log.Info("伺服器不支援 IDLE，改用輪詢", "interval", w.cfg.PollInterval)
	}
	for {
		if useIdle {
			if err := w.idleOnce(ctx, c, sig); err != nil {
				return err
			}
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c.Closed():
				return errors.New("連線已關閉")
			case <-time.After(w.cfg.PollInterval):
			}
			if err := w.await(c, w.cfg.CmdTimeout, func() error { return c.Noop().Wait() }); err != nil {
				return fmt.Errorf("NOOP：%w", err)
			}
		}
		if err := w.drain(ctx, c, &st); err != nil {
			return err
		}
	}
}

// drain repeats catchUp until a pass finds nothing new. Some servers (including
// go-imap's imapmemserver) only report EXISTS for changes that happen *during*
// IDLE, so mail that arrives while we are fetching must be picked up here
// before we go back to IDLE.
func (w *Watcher) drain(ctx context.Context, c *imapclient.Client, st *store.MailboxState) error {
	for {
		n, err := w.catchUp(ctx, c, st)
		if err != nil || n == 0 {
			return err
		}
	}
}

func (w *Watcher) uidNext(c *imapclient.Client, sd *imap.SelectData) (uint32, error) {
	if sd.UIDNext != 0 {
		return uint32(sd.UIDNext), nil
	}
	var status *imap.StatusData
	err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		status, err = c.Status(w.cfg.Folder, &imap.StatusOptions{UIDNext: true}).Wait()
		return err
	})
	if err == nil && status.UIDNext != 0 {
		return uint32(status.UIDNext), nil
	}
	var sr *imap.SearchData
	if err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		sr, err = c.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
		return err
	}); err != nil {
		return 0, fmt.Errorf("取得 UIDNEXT：%w", err)
	}
	var max imap.UID
	for _, u := range sr.AllUIDs() {
		if u > max {
			max = u
		}
	}
	return uint32(max) + 1, nil
}

// idleOnce idles until new mail, the liveness cycle, cancellation or disconnect.
func (w *Watcher) idleOnce(ctx context.Context, c *imapclient.Client, sig <-chan struct{}) error {
	var idle *imapclient.IdleCommand
	if err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		idle, err = c.Idle()
		return err
	}); err != nil {
		return fmt.Errorf("IDLE：%w", err)
	}
	cycle := time.NewTimer(w.cfg.IdleCycle)
	defer cycle.Stop()
	cycled := false
	select {
	case <-sig:
	case <-cycle.C:
		cycled = true
	case <-ctx.Done():
	case <-c.Closed():
	}
	if err := w.await(c, w.cfg.CmdTimeout, func() error {
		if err := idle.Close(); err != nil {
			return err
		}
		return idle.Wait()
	}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("結束 IDLE：%w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if cycled {
		if err := w.await(c, w.cfg.CmdTimeout, func() error { return c.Noop().Wait() }); err != nil {
			return fmt.Errorf("NOOP：%w", err)
		}
	}
	return nil
}

func toEnvelope(b *imapclient.FetchMessageBuffer) pipeline.Envelope {
	env := pipeline.Envelope{UID: uint32(b.UID), InternalDate: b.InternalDate, Size: b.RFC822Size}
	if b.Envelope != nil {
		env.Subject = b.Envelope.Subject
		if len(b.Envelope.From) > 0 {
			env.From = b.Envelope.From[0].Addr()
		}
	}
	return env
}

// catchUp handles every message with UID > st.LastUID and returns how many it handled.
func (w *Watcher) catchUp(ctx context.Context, c *imapclient.Client, st *store.MailboxState) (int, error) {
	mb, folder := w.cfg.Mailbox, w.cfg.Folder
	var set imap.UIDSet
	set.AddRange(imap.UID(st.LastUID+1), 0) // LastUID+1:*
	var bufs []*imapclient.FetchMessageBuffer
	if err := w.await(c, w.cfg.CmdTimeout, func() (err error) {
		bufs, err = c.Fetch(set, &imap.FetchOptions{UID: true, InternalDate: true, RFC822Size: true, Envelope: true}).Collect()
		return err
	}); err != nil {
		return 0, fmt.Errorf("抓取新信清單：%w", err)
	}
	n := 0
	sort.Slice(bufs, func(i, j int) bool { return bufs[i].UID < bufs[j].UID })
	for _, b := range bufs {
		uid := uint32(b.UID)
		if uid <= st.LastUID { // servers return the last message for n:* when n > max
			continue
		}
		n++
		env := toEnvelope(b)
		if kind := w.cfg.Processor.Decide(mb, env); kind != "" {
			if err := w.cfg.Processor.RecordSkipped(ctx, mb, folder, st.UIDValidity, env, kind); err != nil {
				return n, err
			}
			st.LastUID = uid
			continue
		}
		var body []*imapclient.FetchMessageBuffer
		if err := w.await(c, w.cfg.FetchTimeout, func() (err error) {
			body, err = c.Fetch(imap.UIDSetNum(b.UID), &imap.FetchOptions{
				UID: true, BodySection: []*imap.FetchItemBodySection{{Peek: true}},
			}).Collect()
			return err
		}); err != nil {
			return n, fmt.Errorf("抓取信件 UID %d：%w", uid, err)
		}
		if len(body) == 0 || len(body[0].BodySection) == 0 {
			// expunged between the two fetches
			if err := w.cfg.State.SetMailboxState(ctx, mb.Name, folder, store.MailboxState{UIDValidity: st.UIDValidity, LastUID: uid}); err != nil {
				return n, err
			}
			st.LastUID = uid
			continue
		}
		if err := w.cfg.Processor.Process(ctx, mb, folder, st.UIDValidity, env, body[0].BodySection[0].Bytes); err != nil {
			return n, fmt.Errorf("處理信件 UID %d：%w", uid, err)
		}
		st.LastUID = uid
	}
	return n, nil
}
```

- [ ] **Step 6: Run tests — expect PASS (with the race detector)**

Run: `go test ./internal/watcher/ -race -count=1 -v && go vet ./...`
Expected: PASS. Debugging aids if something hangs: set `imapclient.Options.DebugWriter = os.Stderr` temporarily inside `testDialer.dial` to see the IMAP wire traffic; never commit that.

- [ ] **Step 7: Commit**

```bash
git add internal/watcher
git commit -m "feat(watcher): IMAP session with read-only select, UID watermark, catch-up, IDLE/poll and liveness checks

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 15: Watcher supervisor (`Run`: reconnect backoff, status, alerts)

**Files:**
- Create: `internal/watcher/run.go`
- Test: `internal/watcher/run_test.go`

**Interfaces:**
- Consumes: Task 14 (`Watcher`, `session`, `AuthError`, `Status*`), `auth.ErrNeedsLogin`.
- Produces: `func (w *Watcher) Run(ctx context.Context)` — returns when `ctx` is cancelled (status `stopped`) or the mailbox needs an OAuth re-login (status stays `needs_login`). Alert keys: `disconnected:<mailbox>/<folder>`, `auth:<mailbox>/<folder>`, `needs_login:<mailbox>`.

- [ ] **Step 1: Write the failing test** — `internal/watcher/run_test.go`

```go
package watcher

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"verify-code-relay/internal/auth"
)

func startRun(t *testing.T, e *env) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.w.Run(ctx); close(done) }()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not stop")
		}
	}
}

func TestRunReconnectsAfterKill(t *testing.T) {
	e := newEnv(t, nil, nil)
	stop := startRun(t, e)
	defer stop()
	eventually(t, "connected", func() bool { return e.w.Status() == StatusConnected })
	e.dialer.last().Conn.Close()
	eventually(t, "redialled", func() bool { return e.dialer.count.Load() >= 2 && e.w.Status() == StatusConnected })
	e.srv.appendMsg(t, "after reconnect")
	eventually(t, "uid 1 processed", func() bool { return equalUIDs(e.proc.got(), 1) })
}

func TestRunRetriesFailedUIDExactlyOnce(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.proc.failOnce[1] = true
	stop := startRun(t, e)
	defer stop()
	eventually(t, "connected", func() bool { return e.w.Status() == StatusConnected })
	e.srv.appendMsg(t, "flaky")
	eventually(t, "uid 1 processed after retry", func() bool { return equalUIDs(e.proc.got(), 1) })
	time.Sleep(200 * time.Millisecond)
	if !equalUIDs(e.proc.got(), 1) {
		t.Fatalf("processed = %v (must be exactly once)", e.proc.got())
	}
}

func TestRunDetectsBlackHoleAndRecovers(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) {
		c.IdleCycle = 100 * time.Millisecond
		c.CmdTimeout = 200 * time.Millisecond
	})
	stop := startRun(t, e)
	defer stop()
	eventually(t, "connected", func() bool { return e.w.Status() == StatusConnected })
	e.dialer.last().frozen.Store(true)
	eventually(t, "reconnected", func() bool { return e.dialer.count.Load() >= 2 && e.w.Status() == StatusConnected })
}

func TestRunAuthFailedAlertsOnceAndKeepsRetrying(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.Auth = auth.Password{Username: "me@example.com", Password: "nope"} })
	stop := startRun(t, e)
	defer stop()
	eventually(t, "auth_failed and retrying", func() bool { return e.w.Status() == StatusAuthFailed && e.dialer.count.Load() >= 3 })
	alerts, _ := e.alerts.snapshot()
	n := 0
	for _, k := range alerts {
		if k == "auth:mb/INBOX" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("auth alerts = %v", alerts)
	}
}

type needsLoginAuth struct{}

func (needsLoginAuth) Authenticate(context.Context, *imapclient.Client) error {
	return auth.ErrNeedsLogin
}

func TestRunNeedsLoginStops(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.Auth = needsLoginAuth{} })
	done := make(chan struct{})
	go func() { e.w.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run should return on needs_login")
	}
	if e.w.Status() != StatusNeedsLogin {
		t.Fatalf("status = %s", e.w.Status())
	}
	alerts, _ := e.alerts.snapshot()
	if len(alerts) != 1 || alerts[0] != "needs_login:mb" {
		t.Fatalf("alerts = %v", alerts)
	}
}

func TestRunDisconnectedAlertAndRecovery(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.AlertAfter = 100 * time.Millisecond })
	e.dialer.fail.Store(true)
	stop := startRun(t, e)
	defer stop()
	eventually(t, "disconnected alert", func() bool {
		a, _ := e.alerts.snapshot()
		return len(a) == 1 && strings.HasPrefix(a[0], "disconnected:mb/INBOX")
	})
	if e.w.Status() != StatusReconnecting {
		t.Errorf("status = %s", e.w.Status())
	}
	e.dialer.fail.Store(false)
	eventually(t, "recovered", func() bool {
		_, r := e.alerts.snapshot()
		return len(r) == 1 && r[0] == "disconnected:mb/INBOX"
	})
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/watcher/ -run TestRun -v`
Expected: FAIL — `e.w.Run undefined`.

- [ ] **Step 3: Implement `internal/watcher/run.go`**

```go
package watcher

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"verify-code-relay/internal/auth"
)

func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// Run keeps a session alive with exponential backoff until ctx is cancelled or
// the mailbox needs an interactive OAuth login.
func (w *Watcher) Run(ctx context.Context) {
	key := w.Key()
	backoff := w.cfg.MinBackoff
	var downSince time.Time
	alertedDown, alertedAuth := false, false

	for {
		err := w.session(ctx, func() {
			w.setStatus(StatusConnected)
			w.log.Info("已連線並開始監看")
			backoff = w.cfg.MinBackoff
			downSince = time.Time{}
			if alertedDown {
				w.cfg.Alert.Recovered("disconnected:"+key, fmt.Sprintf("信箱 %s 已恢復連線", key))
				alertedDown = false
			}
			if alertedAuth {
				w.cfg.Alert.Recovered("auth:"+key, fmt.Sprintf("信箱 %s 認證已恢復", key))
				alertedAuth = false
			}
		})
		if ctx.Err() != nil {
			w.setStatus(StatusStopped)
			return
		}
		var ae *AuthError
		switch {
		case errors.Is(err, auth.ErrNeedsLogin):
			w.setStatus(StatusNeedsLogin)
			name := w.cfg.Mailbox.Name
			w.log.Error("OAuth 授權失效，停止監看", "err", err)
			w.cfg.Alert.Alert("needs_login:"+name, fmt.Sprintf("信箱 %s 的 Microsoft 授權已失效，請執行：relay login %s", name, name))
			return
		case errors.As(err, &ae):
			w.setStatus(StatusAuthFailed)
			if !alertedAuth {
				w.cfg.Alert.Alert("auth:"+key, fmt.Sprintf("信箱 %s 認證失敗：%v", key, ae.Err))
				alertedAuth = true
			}
		default:
			w.setStatus(StatusReconnecting)
		}
		if downSince.IsZero() {
			downSince = w.now()
		}
		if !alertedDown && w.now().Sub(downSince) >= w.cfg.AlertAfter {
			w.cfg.Alert.Alert("disconnected:"+key, fmt.Sprintf("信箱 %s 已超過 %s 無法連線：%v", key, w.cfg.AlertAfter, err))
			alertedDown = true
		}
		wait := jitter(backoff)
		w.log.Warn("連線中斷，稍後重試", "err", err, "retry_in", wait.Round(time.Millisecond))
		backoff = min(backoff*2, w.cfg.MaxBackoff)
		select {
		case <-ctx.Done():
			w.setStatus(StatusStopped)
			return
		case <-time.After(wait):
		}
	}
}
```

- [ ] **Step 4: Run tests — expect PASS (race detector, repeated to catch flakes)**

Run: `gofmt -w internal/watcher && go test ./internal/watcher/ -race -count=3 && go vet ./...`
Expected: PASS three times in a row. A flaky failure here is a real bug (ordering or missed wakeup) — debug it with superpowers:systematic-debugging rather than raising timeouts.

- [ ] **Step 5: Commit**

```bash
git add internal/watcher
git commit -m "feat(watcher): supervisor with jittered backoff, status tracking and alerts

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 16: Health endpoint

**Files:**
- Create: `internal/health/health.go`
- Test: `internal/health/health_test.go`

**Interfaces:**
- Consumes: nothing project-internal (decoupled from `watcher` on purpose; `cmd/relay` adapts `*watcher.Watcher`).
- Produces:
  ```go
  package health
  type Source interface{ Key() string; StatusString() string }
  func Handler(sources []Source) http.Handler   // GET /healthz -> 200 if every source is "connected", else 503; JSON {"mailboxes": {key: status}}
  ```

- [ ] **Step 1: Write the failing test** — `internal/health/health_test.go`

```go
package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type src struct{ key, status string }

func (s src) Key() string          { return s.key }
func (s src) StatusString() string { return s.status }

func get(t *testing.T, h http.Handler) (int, map[string]map[string]string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var body map[string]map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestHealthy(t *testing.T) {
	code, body := get(t, Handler([]Source{src{"a/INBOX", "connected"}, src{"b/INBOX", "connected"}}))
	if code != 200 || body["mailboxes"]["b/INBOX"] != "connected" {
		t.Fatalf("code=%d body=%v", code, body)
	}
}

func TestUnhealthy(t *testing.T) {
	code, body := get(t, Handler([]Source{src{"a/INBOX", "connected"}, src{"o/INBOX", "needs_login"}}))
	if code != 503 || body["mailboxes"]["o/INBOX"] != "needs_login" {
		t.Fatalf("code=%d body=%v", code, body)
	}
}

func TestNotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other", nil))
	if rec.Code != 404 {
		t.Fatalf("code=%d", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/health/ -v`
Expected: FAIL — `undefined: Handler`.

- [ ] **Step 3: Implement `internal/health/health.go`**

```go
// Package health serves /healthz with per-watcher status.
package health

import (
	"encoding/json"
	"net/http"
)

// Source is satisfied by an adapter around *watcher.Watcher (see cmd/relay/run.go).
type Source interface {
	Key() string
	StatusString() string
}

func Handler(sources []Source) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		statuses := map[string]string{}
		healthy := true
		for _, s := range sources {
			st := s.StatusString()
			statuses[s.Key()] = st
			if st != "connected" {
				healthy = false
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(map[string]any{"mailboxes": statuses})
	})
	return mux
}
```

(The interface uses `StatusString() string` so `health` stays independent of `watcher`; `cmd/relay` adapts.)

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/health/ -v && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/health
git commit -m "feat(health): /healthz reporting per-mailbox watcher status

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 17: CLI (`relay run | config validate | login | test | send-test | healthcheck`)

**Files:**
- Create: `cmd/relay/main.go`, `cmd/relay/app.go`, `cmd/relay/run.go`, `cmd/relay/umask_unix.go`, `cmd/relay/umask_other.go`, `cmd/relay/validate.go`, `cmd/relay/testcmd.go`, `cmd/relay/sendtest.go`, `cmd/relay/login.go`, `cmd/relay/healthcheck.go`
- Test: `cmd/relay/main_test.go`, `cmd/relay/run_test.go`

**Interfaces:**
- Consumes: every package above.
- Produces: the `relay` binary. Exit codes: 0 success, 1 runtime/validation failure, 2 usage error. Global flag `--config` (default `$RELAY_CONFIG` or `./config.yaml`) is accepted **before** the subcommand (`relay --config x.yaml run`) and also after it (`relay run --config x.yaml`).

- [ ] **Step 1: Write the failing CLI test** — `cmd/relay/main_test.go`

Tests drive `realMain(args, stdout, stderr) int` so no process is spawned.

```go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	cfg := `data_dir: ` + filepath.Join(dir, "data") + `
bots: { main: { token: "123:abc" } }
targets: { me: { bot: main, chat_id: 42 } }
mailboxes:
  gmail:
    display_name: 個人 Gmail
    host: imap.gmail.com
    username: me@gmail.com
    auth: { type: password, password: pw }
    targets: [me]
` + extra
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := realMain(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsage(t *testing.T) {
	code, _, stderr := run()
	if code != 2 || !strings.Contains(stderr, "用法") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	code, _, _ = run("bogus")
	if code != 2 {
		t.Fatalf("unknown command code=%d", code)
	}
}

func TestConfigValidate(t *testing.T) {
	p := writeConfig(t, "")
	code, stdout, stderr := run("--config", p, "config", "validate")
	if code != 0 || !strings.Contains(stdout, "設定檔正確") || !strings.Contains(stdout, "信箱 1") {
		t.Fatalf("code=%d out=%q err=%q", code, stdout, stderr)
	}
	bad := writeConfig(t, "admin: nobody\n")
	code, _, stderr = run("config", "validate", "--config", bad)
	if code != 1 || !strings.Contains(stderr, `admin: 找不到目標 "nobody"`) {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
	badTpl := writeConfig(t, "templates: { code: \"{{.Nope}}\" }\n")
	code, _, stderr = run("--config", badTpl, "config", "validate")
	if code != 1 || !strings.Contains(stderr, "templates.code") {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
}

func TestTestCommandPrintsTraceAndMessage(t *testing.T) {
	p := writeConfig(t, "")
	code, stdout, stderr := run("--config", p, "test", "--eml", "../../testdata/eml/github-device-en.eml", "--mailbox", "gmail")
	if code != 0 {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
	for _, want := range []string{"關鍵字", "482913", "<code>482913</code>", "個人 Gmail", "copy_text"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	code, stdout, _ = run("--config", p, "test", "--eml", "../../testdata/eml/newsletter-en.eml")
	if code != 0 || !strings.Contains(stdout, "不轉發") {
		t.Fatalf("code=%d out=%q", code, stdout)
	}
}

func TestSendTestUsesTelegram(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("RELAY_TELEGRAM_API", srv.URL) // test hook, see app.go
	p := writeConfig(t, "")
	code, stdout, stderr := run("--config", p, "send-test", "me")
	if code != 0 || !strings.Contains(stdout, "已送出") || got["chat_id"].(float64) != 42 {
		t.Fatalf("code=%d out=%q err=%q got=%v", code, stdout, stderr, got)
	}
	code, _, _ = run("--config", p, "send-test", "nobody")
	if code != 1 {
		t.Fatalf("unknown target code=%d", code)
	}
}

func TestLoginRejectsPasswordMailbox(t *testing.T) {
	p := writeConfig(t, "")
	code, _, stderr := run("--config", p, "login", "gmail")
	if code != 1 || !strings.Contains(stderr, "microsoft_oauth") {
		t.Fatalf("code=%d err=%q", code, stderr)
	}
}

func TestHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	code, _, _ := run("healthcheck", "--url", srv.URL+"/healthz")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	code, _, _ = run("healthcheck", "--url", srv.URL+"/nope")
	if code != 1 {
		t.Fatalf("code=%d", code)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/relay/ -v`
Expected: FAIL — `undefined: realMain`.

- [ ] **Step 3: Implement `cmd/relay/main.go`**

```go
// Command relay forwards verification codes and links from IMAP mailboxes to Telegram.
package main

import (
	"fmt"
	"io"
	"os"
)

const usage = `用法：relay [--config 路徑] <指令> [參數]

指令：
  run                         啟動服務
  config validate             驗證設定檔
  login <信箱>                Microsoft 信箱的 OAuth 授權（device code）
  test --eml <檔案> [--mailbox <信箱>] [--send <目標>]
                              對 .eml 檔執行辨識並顯示判斷過程與訊息
  send-test <目標>            發送測試訊息到指定目標

設定檔預設為 $RELAY_CONFIG 或 ./config.yaml。
`

func main() { os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr)) }

func realMain(args []string, stdout, stderr io.Writer) int {
	configPath := os.Getenv("RELAY_CONFIG")
	if configPath == "" {
		configPath = "config.yaml"
	}
	// global --config before the subcommand
	for len(args) >= 2 && (args[0] == "--config" || args[0] == "-config") {
		configPath, args = args[1], args[2:]
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "run":
		return cmdRun(configPath, rest, stdout, stderr)
	case "config":
		if len(rest) == 0 || rest[0] != "validate" {
			fmt.Fprint(stderr, usage)
			return 2
		}
		return cmdValidate(configPath, rest[1:], stdout, stderr)
	case "login":
		return cmdLogin(configPath, rest, stdout, stderr)
	case "test":
		return cmdTest(configPath, rest, stdout, stderr)
	case "send-test":
		return cmdSendTest(configPath, rest, stdout, stderr)
	case "healthcheck":
		return cmdHealthcheck(configPath, rest, stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "未知的指令：%s\n\n%s", cmd, usage)
		return 2
	}
}
```

- [ ] **Step 4: Implement `cmd/relay/app.go`** (shared helpers)

```go
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/recognize"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/telegram"
)

// newFlags returns a FlagSet that also accepts --config after the subcommand.
func newFlags(name string, configPath *string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(configPath, "config", *configPath, "設定檔路徑")
	return fs
}

type app struct {
	cfg        *config.Config
	recognizer *recognize.Chain
	renderer   *render.Renderer
}

// loadApp loads + validates config and compiles rules and templates. All problems are
// reported together.
func loadApp(path string) (*app, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	var errs []error
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	rec, err := recognize.New(cfg.Rules, cfg.Recognize)
	if err != nil {
		errs = append(errs, err)
	}
	rnd, err := render.New(cfg)
	if err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &app{cfg: cfg, recognizer: rec, renderer: rnd}, nil
}

func newLogger(level string, w io.Writer) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: l}))
}

func telegramClient() *telegram.Client {
	c := telegram.New()
	if u := os.Getenv("RELAY_TELEGRAM_API"); u != "" { // test hook / self-hosted Bot API server
		c.BaseURL = u
	}
	return c
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "錯誤：%v\n", err)
	return 1
}
```

- [ ] **Step 5: Implement `cmd/relay/validate.go`**

```go
package main

import (
	"fmt"
	"io"
)

func cmdValidate(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("config validate", &configPath, stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	a, err := loadApp(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "設定檔有誤（%s）：\n%v\n", configPath, err)
		return 1
	}
	folders := 0
	for _, m := range a.cfg.Mailboxes {
		folders += len(m.Folders)
	}
	fmt.Fprintf(stdout, "設定檔正確：Bot %d 個、目標 %d 個、信箱 %d 個（監看 %d 個資料夾）、自訂規則 %d 條\n",
		len(a.cfg.Bots), len(a.cfg.Targets), len(a.cfg.Mailboxes), folders, len(a.cfg.Rules))
	return 0
}
```

Note: the test checks for `"信箱 1"`; the format string prints `信箱 1 個` which contains it.

- [ ] **Step 6: Implement `cmd/relay/testcmd.go`**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"verify-code-relay/internal/mailparse"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/render"
	"verify-code-relay/internal/telegram"
)

func cmdTest(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("test", &configPath, stderr)
	emlPath := fs.String("eml", "", ".eml 檔案路徑（必填）")
	mailbox := fs.String("mailbox", "", "使用哪個信箱的範本與名稱")
	sendTo := fs.String("send", "", "實際發送到這個目標")
	if err := fs.Parse(args); err != nil || *emlPath == "" {
		fmt.Fprintln(stderr, "用法：relay test --eml <檔案> [--mailbox <信箱>] [--send <目標>]")
		return 2
	}
	a, err := loadApp(configPath)
	if err != nil {
		return fail(stderr, err)
	}
	raw, err := os.ReadFile(*emlPath)
	if err != nil {
		return fail(stderr, err)
	}
	msg, err := mailparse.Parse(raw)
	if err != nil {
		return fail(stderr, fmt.Errorf("解析信件：%w", err))
	}
	displayName, username := "（測試）", ""
	if *mailbox != "" {
		m, ok := a.cfg.Mailboxes[*mailbox]
		if !ok {
			return fail(stderr, fmt.Errorf("找不到信箱 %q", *mailbox))
		}
		displayName, username = m.DisplayName, m.Username
	}

	fmt.Fprintf(stdout, "寄件者：%s <%s>\n主旨：%s\n", msg.From.Name, msg.From.Addr, msg.Subject)
	fmt.Fprintf(stdout, "── 內文（純文字，前 500 字）──\n%s\n", truncateRunes(msg.Text, 500))
	fmt.Fprintf(stdout, "── 連結 %d 個 ──\n", len(msg.Links))
	for _, l := range msg.Links {
		fmt.Fprintf(stdout, "  %s  [%s]\n", l.URL, l.Text)
	}
	res, ignore, tr := a.recognizer.Recognize(msg)
	fmt.Fprintln(stdout, "── 辨識過程 ──")
	for _, s := range tr.Steps {
		fmt.Fprintln(stdout, "  "+s)
	}
	fmt.Fprintln(stdout, "── 結果 ──")
	switch {
	case ignore:
		rule := ""
		if res != nil {
			rule = res.Rule
		}
		fmt.Fprintf(stdout, "被規則 %s 明確略過，不轉發\n", rule)
		return 0
	case res == nil:
		fmt.Fprintln(stdout, "不符合任何規則，不轉發")
		return 0
	}
	fmt.Fprintf(stdout, "規則：%s\n驗證碼：%s\n連結：%s\n服務：%s\n", res.Rule, res.Code, res.URL, res.Service)

	out, err := a.renderer.Render(*mailbox, render.Data{Code: res.Code, URL: res.URL, Service: res.Service,
		FromName: msg.From.Name, FromAddr: msg.From.Addr, To: pipeline.Recipient(msg.To, username),
		Subject: msg.Subject, Mailbox: displayName, Rule: res.Rule, Date: msg.Date})
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "── Telegram 訊息 ──\n%s\n── 按鈕 ──\n%s\n", out.Text, out.ReplyMarkup)

	if *sendTo != "" {
		t, ok := a.cfg.Targets[*sendTo]
		if !ok {
			return fail(stderr, fmt.Errorf("找不到目標 %q", *sendTo))
		}
		err := telegramClient().Send(context.Background(), telegram.SendRequest{Token: a.cfg.Bots[t.Bot].Token,
			ChatID: t.ChatID, Text: out.Text, HTML: true, ReplyMarkup: out.ReplyMarkup})
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "已送出到目標 %s\n", *sendTo)
	}
	return 0
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}
```

- [ ] **Step 7: Implement `cmd/relay/sendtest.go`**

```go
package main

import (
	"context"
	"fmt"
	"html"
	"io"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/telegram"
)

func cmdSendTest(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("send-test", &configPath, stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法：relay send-test <目標>")
		return 2
	}
	name := fs.Arg(0)
	cfg, err := config.Load(configPath)
	if err != nil {
		return fail(stderr, err)
	}
	if err := cfg.Validate(); err != nil {
		return fail(stderr, err)
	}
	t, ok := cfg.Targets[name]
	if !ok {
		return fail(stderr, fmt.Errorf("找不到目標 %q", name))
	}
	text := fmt.Sprintf("✅ verify-code-relay 測試訊息\n目標 <b>%s</b>（Bot %s）設定正確。", html.EscapeString(name), html.EscapeString(t.Bot))
	err = telegramClient().Send(context.Background(), telegram.SendRequest{Token: cfg.Bots[t.Bot].Token, ChatID: t.ChatID, Text: text, HTML: true})
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "已送出測試訊息到目標 %s\n", name)
	return 0
}
```

- [ ] **Step 8: Implement `cmd/relay/login.go`**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/config"
	"verify-code-relay/internal/store"
)

func cmdLogin(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("login", &configPath, stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(stderr, "用法：relay login <信箱>")
		return 2
	}
	name := fs.Arg(0)
	cfg, err := config.Load(configPath)
	if err != nil {
		return fail(stderr, err)
	}
	if err := cfg.Validate(); err != nil {
		return fail(stderr, err)
	}
	m, ok := cfg.Mailboxes[name]
	if !ok {
		return fail(stderr, fmt.Errorf("找不到信箱 %q", name))
	}
	if m.Auth.Type != "microsoft_oauth" {
		return fail(stderr, fmt.Errorf("信箱 %s 的 auth.type 是 %q，只有 microsoft_oauth 需要 login", name, m.Auth.Type))
	}
	st, err := openStore(cfg)
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()
	ms := &auth.Microsoft{Mailbox: name, Username: m.Username, ClientID: m.Auth.ClientID, Tenant: m.Auth.Tenant, Store: st}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	dc, err := ms.StartDeviceLogin(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "請在瀏覽器開啟 %s 並輸入代碼：%s\n（%s）\n等待授權中…\n", dc.VerificationURI, dc.UserCode, dc.Message)
	if err := ms.PollDeviceLogin(ctx, dc); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "授權成功，信箱 %s 的 token 已儲存。若服務正在執行，請重新啟動以生效。\n", name)
	return 0
}

func openStore(cfg *config.Config) (*store.Store, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("建立資料目錄：%w", err)
	}
	return store.Open(filepath.Join(cfg.DataDir, "relay.db"))
}
```

- [ ] **Step 9: Implement `cmd/relay/healthcheck.go`**

```go
package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"verify-code-relay/internal/config"
)

func cmdHealthcheck(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("healthcheck", &configPath, stderr)
	url := fs.String("url", "", "健康檢查網址（預設依設定檔 http_listen）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *url == "" {
		cfg, err := config.Load(configPath)
		if err != nil {
			return fail(stderr, err)
		}
		if cfg.HTTPListen == "" {
			return fail(stderr, fmt.Errorf("http_listen 未設定"))
		}
		addr := cfg.HTTPListen
		if strings.HasPrefix(addr, ":") {
			addr = "127.0.0.1" + addr
		}
		*url = "http://" + addr + "/healthz"
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(*url)
	if err != nil {
		return fail(stderr, err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "不健康：HTTP %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
```

- [ ] **Step 10: Implement `cmd/relay/umask_unix.go`**

```go
//go:build unix

package main

import "syscall"

// restrictUmask makes SQLite's -wal/-shm side files private like the main db file.
func restrictUmask() { syscall.Umask(0o077) }
```

and `cmd/relay/umask_other.go`:

```go
//go:build !unix

package main

func restrictUmask() {}
```

- [ ] **Step 11: Implement `cmd/relay/run.go`**

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"verify-code-relay/internal/alert"
	"verify-code-relay/internal/auth"
	"verify-code-relay/internal/dispatcher"
	"verify-code-relay/internal/health"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/watcher"
)

type healthAdapter struct{ w *watcher.Watcher }

func (h healthAdapter) Key() string          { return h.w.Key() }
func (h healthAdapter) StatusString() string { return string(h.w.Status()) }

func cmdRun(configPath string, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("run", &configPath, stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	restrictUmask()
	a, err := loadApp(configPath)
	if err != nil {
		fmt.Fprintf(stderr, "設定檔有誤（%s）：\n%v\n", configPath, err)
		return 1
	}
	cfg := a.cfg
	log := newLogger(cfg.LogLevel, stdout)
	st, err := openStore(cfg)
	if err != nil {
		return fail(stderr, err)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tokens := map[string]string{}
	for name, b := range cfg.Bots {
		tokens[name] = b.Token
	}
	var alerter *alert.Alerter
	disp := dispatcher.New(dispatcher.Options{Store: st, Sender: telegramClient(), Tokens: tokens, Log: log,
		Alert: func(key, text string) { alerter.Alert(key, text) }})
	var admin *alert.Target
	if cfg.Admin != "" {
		t := cfg.Targets[cfg.Admin]
		admin = &alert.Target{Name: cfg.Admin, Bot: t.Bot, ChatID: t.ChatID}
	}
	alerter = alert.New(st, admin, cfg.Defaults.AlertTTL.Duration, disp.Wake, log)

	proc := &pipeline.Processor{Store: st, Recognizer: a.recognizer, Renderer: a.renderer, Notify: disp.Wake, Log: log}

	var watchers []*watcher.Watcher
	var sources []health.Source
	for _, name := range cfg.SortedMailboxNames() {
		m := cfg.Mailboxes[name]
		var targets []pipeline.Target
		for _, tn := range m.Targets {
			t := cfg.Targets[tn]
			targets = append(targets, pipeline.Target{Name: tn, Bot: t.Bot, ChatID: t.ChatID})
		}
		var authn auth.Authenticator = auth.Password{Username: m.Username, Password: m.Auth.Password}
		if m.Auth.Type == "microsoft_oauth" {
			authn = &auth.Microsoft{Mailbox: name, Username: m.Username, ClientID: m.Auth.ClientID, Tenant: m.Auth.Tenant, Store: st}
		}
		mb := pipeline.Mailbox{Name: name, DisplayName: m.DisplayName, Username: m.Username, Targets: targets,
			MaxAge: m.MaxAge.Duration, TTL: m.TTL.Duration}
		for _, folder := range m.Folders {
			w := watcher.New(watcher.Config{
				Mailbox: mb, Folder: folder,
				Dial:      watcher.TLSDialer(net.JoinHostPort(m.Host, strconv.Itoa(m.Port))),
				Auth:      authn,
				Processor: proc, State: st, Alert: alerter, Log: log,
				PollInterval: m.PollInterval.Duration, AlertAfter: cfg.Defaults.AlertAfter.Duration,
			})
			watchers = append(watchers, w)
			sources = append(sources, healthAdapter{w})
		}
	}

	var wg sync.WaitGroup
	for _, w := range watchers {
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(ctx) }()
	}
	dispDone := make(chan struct{})
	go func() { disp.Run(ctx); close(dispDone) }()
	go pruneLoop(ctx, st, log)

	var httpSrv *http.Server
	if cfg.HTTPListen != "" {
		httpSrv = &http.Server{Addr: cfg.HTTPListen, Handler: health.Handler(sources), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("HTTP 服務失敗", "err", err)
			}
		}()
	}
	log.Info("服務已啟動", "watchers", len(watchers), "http", cfg.HTTPListen)

	<-ctx.Done()
	log.Info("收到停止訊號，正在關閉")
	wg.Wait()
	<-dispDone
	if httpSrv != nil {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		httpSrv.Shutdown(sctx)
		cancel()
	}
	log.Info("已停止")
	return 0
}

type pruner interface {
	PruneLog(ctx context.Context, before time.Time) (int64, error)
}

func pruneLoop(ctx context.Context, st pruner, log interface{ Info(string, ...any) }) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		if n, err := st.PruneLog(ctx, time.Now().Add(-30*24*time.Hour)); err == nil && n > 0 {
			log.Info("已清理舊轉發紀錄", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
```

- [ ] **Step 12: Run tests — expect PASS**

Run: `go test ./cmd/relay/ -v && go vet ./... && go build -o /tmp/relay ./cmd/relay && /tmp/relay --help`
Expected: tests PASS; `--help` prints the Chinese usage and exits 0.

- [ ] **Step 13: Smoke-test `run` against a local IMAP server and a fake Telegram**

This verifies the real wiring (TLS dialer excluded). Create `cmd/relay/run_test.go`:

```go
package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRunSmoke boots the full service with an unreachable IMAP host and checks that
// startup, /healthz (503 while not connected) and graceful shutdown work.
func TestRunSmoke(t *testing.T) {
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer tg.Close()
	t.Setenv("RELAY_TELEGRAM_API", tg.URL)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	httpAddr := ln.Addr().String()
	ln.Close()
	p := writeConfig(t, "http_listen: \""+httpAddr+"\"\n")
	// point the mailbox at a closed port so the watcher keeps reconnecting
	cfg, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(cfg), "host: imap.gmail.com", "host: 127.0.0.1\n    port: 1", 1)), 0o600)

	done := make(chan int, 1)
	proc, _ := os.FindProcess(os.Getpid())
	go func() { done <- realMain([]string{"--config", p, "run"}, io.Discard, io.Discard) }()

	var code int
	for i := 0; i < 100; i++ {
		resp, err := http.Get("http://" + httpAddr + "/healthz")
		if err == nil {
			code = resp.StatusCode
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code != http.StatusServiceUnavailable {
		t.Fatalf("healthz = %d, want 503 while IMAP is unreachable", code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), "data", "relay.db")); err != nil {
		t.Fatalf("db not created: %v", err)
	}
	proc.Signal(os.Interrupt)
	select {
	case c := <-done:
		if c != 0 {
			t.Fatalf("exit code %d", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not shut down")
	}
}
```

Run: `go test ./cmd/relay/ -run TestRunSmoke -v -count=1`
Expected: PASS. (`signal.NotifyContext` intercepts the self-sent SIGINT, so the test process is not killed.)

- [ ] **Step 14: Commit**

```bash
git add cmd/relay
git commit -m "feat(cmd): relay CLI with run, config validate, login, test, send-test and healthcheck

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---
### Task 18: Example config, Docker, README

**Files:**
- Create: `config.example.yaml`, `.env.example`, `Dockerfile`, `.dockerignore`, `docker-compose.yml`, `README.md`
- Test: `cmd/relay/example_test.go`

**Interfaces:**
- Consumes: the `relay` CLI (Task 17).
- Produces: deployable artifacts. `config.example.yaml` must pass `relay config validate` when its env vars are set (enforced by a test).

- [ ] **Step 1: Write the failing example-config test** — `cmd/relay/example_test.go`

```go
package main

import (
	"strings"
	"testing"
)

func TestExampleConfigValidates(t *testing.T) {
	for k, v := range map[string]string{
		"TG_BOT_MAIN": "1:a", "TG_BOT_WORK": "2:b", "GMAIL_APP_PASSWORD": "x",
	} {
		t.Setenv(k, v)
	}
	code, stdout, stderr := run("--config", "../../config.example.yaml", "config", "validate")
	if code != 0 || !strings.Contains(stdout, "設定檔正確") {
		t.Fatalf("code=%d out=%q err=%q", code, stdout, stderr)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/relay/ -run TestExampleConfigValidates -v`
Expected: FAIL — `讀取設定檔：open ../../config.example.yaml: no such file or directory`.

- [ ] **Step 3: Create `config.example.yaml`** (spec §9, with comments)

```yaml
# verify-code-relay 設定範例。複製為 config.yaml 後修改。
# 機密資料請用 ${環境變數} 帶入（寫在 .env），不要直接寫在這裡。

data_dir: /data              # SQLite 資料庫位置（Docker 內為 /data）
log_level: info              # debug | info | warn | error
http_listen: ":8080"         # /healthz；留空代表不啟動

bots:
  main: { token: "${TG_BOT_MAIN}" }
  work: { token: "${TG_BOT_WORK}" }

targets:                     # 目標 = Bot + 聊天室；chat_id 可用 @userinfobot 等工具查詢
  me:        { bot: main, chat_id: 123456789 }
  family:    { bot: main, chat_id: -1001234567890 }
  work_chan: { bot: work, chat_id: -1009876543210 }

admin: me                    # 選填：系統告警（斷線、授權失效、發送失敗）送到這個目標

defaults:
  max_age: 10m               # 收信時間超過多久就不轉發
  ttl: 15m                   # 待發送訊息超過多久就丟棄
  alert_ttl: 24h             # 告警訊息在佇列中保留多久
  poll_interval: 30s         # 伺服器不支援 IDLE 時的輪詢間隔
  alert_after: 10m           # 連續斷線多久才告警

mailboxes:
  personal_gmail:
    display_name: 個人 Gmail
    host: imap.gmail.com
    username: me@gmail.com
    auth: { type: password, password: "${GMAIL_APP_PASSWORD}" }   # Google 帳戶 → 安全性 → 應用程式密碼
    folders: [INBOX, "[Gmail]/Spam"]   # 監看垃圾郵件匣需在 Gmail 標籤設定勾選「在 IMAP 中顯示」
    targets: [me]

  outlook:
    display_name: Outlook
    host: outlook.office365.com
    username: me@outlook.com
    auth:
      type: microsoft_oauth
      client_id: "00000000-0000-0000-0000-000000000000"   # 見 README「Microsoft 信箱」
      tenant: consumers      # 個人帳號 consumers；公司帳號 organizations 或租戶 ID
    targets: [me, work_chan]
    max_age: 5m
    templates:
      code: |
        💼 <b>{{.Service}}</b>：<code>{{.Code}}</code>
        📧 {{.Mailbox}} · {{.Subject}}

# 全域範本（選填）。可用變數：.Code .URL .Service .FromName .FromAddr .To .Subject .Mailbox .Date .Rule
# templates:
#   code: |
#     🔐 <b>{{.Service}}</b> 驗證碼
#     <code>{{.Code}}</code>

rules:
  - name: ignore-newsletters
    from: "*@news.example.com"
    ignore: true
  # - name: github
  #   from: "*@github.com"
  #   code_regex: 'code:\s*(\d{6})'

recognize:
  extra_code_keywords: []
  extra_link_keywords: []
```

- [ ] **Step 4: Create `.env.example`**

```bash
# 複製為 .env 並填入；docker compose 會自動讀取
TG_BOT_MAIN=123456:replace-me
TG_BOT_WORK=654321:replace-me
GMAIL_APP_PASSWORD=abcdabcdabcdabcd
```

- [ ] **Step 5: Run the example test — expect PASS**

Run: `go test ./cmd/relay/ -run TestExampleConfigValidates -v`
Expected: PASS.

- [ ] **Step 6: Create `.dockerignore` and `Dockerfile`**

`.dockerignore`:

```
.git
data
.env
config.yaml
relay
docs
```

`Dockerfile`:

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/relay ./cmd/relay

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/relay /relay
ENV RELAY_CONFIG=/config.yaml
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD ["/relay", "healthcheck"]
ENTRYPOINT ["/relay"]
CMD ["run"]
```

- [ ] **Step 7: Create `docker-compose.yml`**

```yaml
services:
  relay:
    build: .
    image: verify-code-relay:latest
    restart: unless-stopped
    env_file: .env
    volumes:
      - ./config.yaml:/config.yaml:ro
      - ./data:/data
    # 只有需要從主機查看 /healthz 時才開放
    # ports:
    #   - "127.0.0.1:8080:8080"
```

- [ ] **Step 8: Verify the image builds and runs**

Run:
```bash
docker build -t verify-code-relay:dev . &&
docker run --rm verify-code-relay:dev --help &&
TG_BOT_MAIN=1:a TG_BOT_WORK=2:b GMAIL_APP_PASSWORD=x docker run --rm -e TG_BOT_MAIN -e TG_BOT_WORK -e GMAIL_APP_PASSWORD \
  -v "$PWD/config.example.yaml:/config.yaml:ro" verify-code-relay:dev config validate
```
Expected: build succeeds; usage printed; `設定檔正確：Bot 2 個、目標 3 個、信箱 2 個（監看 3 個資料夾）、自訂規則 1 條`.
If `docker build` cannot reach the network in this environment, report it to the user instead of skipping silently.

- [ ] **Step 9: Create `README.md`** (Traditional Chinese; this is the user-facing manual)

````markdown
# verify-code-relay

以 IMAP IDLE 即時監看多個信箱，辨識「驗證碼」與「認證／登入／重設密碼連結」信件，套用範本後透過 Telegram Bot 轉發。可同時管理多個 Bot、多個聊天室與多個信箱。

- 對信箱**完全唯讀**：不標記已讀、不移動、不刪除。
- 只轉發辨識成功的信；其他信件只記錄寄件者與主旨。
- 驗證碼與網址**不會**寫進日誌或資料庫的轉發紀錄。

## 快速開始（Docker）

```bash
cp config.example.yaml config.yaml   # 依需求修改
cp .env.example .env                 # 填入 Bot token、應用程式密碼
mkdir -p data && sudo chown 65532:65532 data   # 容器以 nonroot (UID 65532) 執行
docker compose run --rm relay config validate
docker compose run --rm relay send-test me     # 確認 Bot 與 chat_id
docker compose up -d
docker compose logs -f
```

## Telegram 設定

1. 找 @BotFather 建立 Bot，取得 token，填進 `.env`。
2. 先對 Bot 傳一則訊息（群組則把 Bot 加入群組；頻道則把 Bot 設為管理員）。
3. 取得 chat_id：個人可用 @userinfobot；群組／頻道 ID 通常是 `-100` 開頭。
4. `relay send-test <目標>` 確認能收到。

## 信箱設定

### Gmail / iCloud / 自架信箱（應用程式密碼）
- Gmail：Google 帳戶需開啟兩步驟驗證，再到「安全性 → 應用程式密碼」建立。`host: imap.gmail.com`。
- 要監看 `[Gmail]/Spam` 等資料夾：Gmail 設定 → 標籤 → 勾選該標籤的「在 IMAP 中顯示」。
- iCloud：`host: imap.mail.me.com`，在 appleid.apple.com 建立 App 專用密碼。

### Microsoft（Outlook.com / Hotmail / Microsoft 365）
Microsoft 已停用 IMAP 的帳號密碼登入，必須使用 OAuth2：
1. 到 Azure 入口網站 → Microsoft Entra ID → 應用程式註冊 → 新增註冊。
   - 支援的帳戶類型：個人帳號選「僅限個人 Microsoft 帳戶」，或「任何組織目錄中的帳戶及個人 Microsoft 帳戶」。
   - 重新導向 URI：不需要。
2. 「驗證」→ 進階設定 →「允許公用用戶端流程」設為「是」。
3. 「API 權限」→ 新增權限 → 委派的權限：`IMAP.AccessAsUser.All`、`offline_access`。
4. 把「應用程式（用戶端）識別碼」填入 `auth.client_id`；個人帳號 `tenant: consumers`。
5. 授權：`docker compose run --rm relay login outlook`，依畫面在瀏覽器輸入代碼。
6. `docker compose restart`。之後 token 會自動更新；若授權失效，`admin` 目標會收到告警，重新執行第 5 步即可。

## 指令

| 指令 | 說明 |
|---|---|
| `relay run` | 啟動服務 |
| `relay config validate` | 驗證設定檔 |
| `relay login <信箱>` | Microsoft OAuth 授權 |
| `relay test --eml <檔案> [--mailbox <信箱>] [--send <目標>]` | 對 .eml 檔顯示辨識過程與訊息；Gmail 網頁版「下載郵件」即可取得 .eml |
| `relay send-test <目標>` | 發送測試訊息 |

設定檔路徑：`--config`，或環境變數 `RELAY_CONFIG`，預設 `./config.yaml`。改完設定需重新啟動。

## 辨識不到或辨識錯誤時

1. 下載該信的 `.eml`，執行 `relay test --eml 信件.eml`，看「辨識過程」。
2. 缺關鍵字：加到 `recognize.extra_code_keywords` / `extra_link_keywords`。
3. 特定寄件者格式特殊：在 `rules` 加自訂規則（`from`、`subject`、`code_regex`、`link_regex`），或用 `ignore: true` 排除。

## 範本

使用 Go `html/template`（Telegram HTML 格式），郵件內容會自動跳脫。可用變數：`.Code` `.URL` `.Service` `.FromName` `.FromAddr` `.To` `.Subject` `.Mailbox` `.Date` `.Rule`。依結果使用 `code` / `link` / `both` 範本，查找順序：信箱 `templates` → 全域 `templates` → 內建。按鈕（複製驗證碼、開啟連結）自動產生。

## 健康檢查

`http_listen` 啟用時提供 `GET /healthz`：所有信箱連線正常回 200，否則 503，內容列出各信箱狀態（`connected`、`reconnecting`、`auth_failed`、`needs_login`…）。Docker 映像內建 `HEALTHCHECK`。

## 開發

```bash
go test ./...                       # 單元與整合測試（含記憶體 IMAP 伺服器）
python3 testdata/eml/gen.py         # 重新產生辨識測試信件
go build -o relay ./cmd/relay
```

## 需要手動驗證的項目

自動測試無法涵蓋以下情境，首次部署時請確認：
- [ ] Gmail 應用程式密碼登入、IDLE 收信後 Telegram 立即收到訊息。
- [ ] Outlook.com 以 `relay login` 授權成功，且 token 過期後能自動更新（運行超過 1 小時後仍能收信）。
- [ ] 中斷網路數分鐘後恢復，服務自動重連並補抓斷線期間的信。
- [ ] 重啟服務後，超過 `max_age` 的舊信不會被轉發。
````

- [ ] **Step 10: Full verification**

Run: `gofmt -l . ; go vet ./... && go test -race ./... && CGO_ENABLED=0 go build -o /tmp/relay ./cmd/relay`
Expected: `gofmt -l` prints nothing; all tests PASS; static build succeeds.

- [ ] **Step 11: Commit**

```bash
git add config.example.yaml .env.example Dockerfile .dockerignore docker-compose.yml README.md cmd/relay/example_test.go
git commit -m "docs: example config, Docker deployment and README

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

## Spec coverage map (self-review)

| Spec section | Tasks |
|---|---|
| §1 decisions (IMAP IDLE, YAML, per-mailbox routing, rules-only, Go/Docker, ignore unmatched, read-only, password + MS OAuth, SQLite outbox, admin alerts, reset links, no auto-delete) | 1–18 (read-only: 14; reset links: 6/7) |
| §2 data flow, atomic outbox + watermark | 3 (`RecordMessage`), 13, 14 |
| §2.2 packages | 1–17 (pipeline split noted in header) |
| §3.1 lifecycle, UIDNEXT fallback, handler-only-signals, `uid > last_uid`, size/age skip, liveness | 13 (Decide), 14 |
| §3.2 backoff, alert_after, auth_failed / needs_login | 15 |
| §3.3 password, XOAUTH2, device code, refresh rotation, 5-min margin | 12, 17 (`login`) |
| §4 parsing, charsets, HTML→text, links | 4, 7 |
| §5 recognizer chain, code/link rules, custom rules, trace | 5, 6, 7 |
| §6 templates, escaping, lookup order, `.To` alias rule, buttons, truncation | 8, 13 (`Recipient`) |
| §7 Telegram client + dispatcher table | 9, 10 |
| §8 schema, 0600, WAL, user_version, 30-day prune | 3, 17 (`pruneLoop`, umask) |
| §9 config, env expansion after parse, validation paths, restart-only | 1, 2, 18 |
| §10 CLI | 17 |
| §11 alerts + dedup | 11, 10, 15 |
| §12 logging rules, /healthz, Dockerfile, compose, `relay login` in container, Gmail Spam note | 16, 17, 18 |
| §13 testing strategy | every task; watcher reconnect cases in 14/15 |
