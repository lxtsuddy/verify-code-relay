package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
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
		{"dup folder", func(c *Config) { c.Mailboxes["gmail"].Folders = []string{"INBOX", "INBOX"} }, `mailboxes.gmail.folders[1]: 資料夾 "INBOX" 重複`},
		{"dup target", func(c *Config) { c.Mailboxes["gmail"].Targets = []string{"me", "me"} }, `mailboxes.gmail.targets[1]: 目標 "me" 重複`},
		{"negative default max_age", func(c *Config) { c.Defaults.MaxAge.Duration = -time.Minute }, "defaults.max_age: 必須大於 0"},
		{"negative default ttl", func(c *Config) { c.Defaults.TTL.Duration = -time.Second }, "defaults.ttl: 必須大於 0"},
		{"negative default alert_ttl", func(c *Config) { c.Defaults.AlertTTL.Duration = -time.Hour }, "defaults.alert_ttl: 必須大於 0"},
		{"negative default poll_interval", func(c *Config) { c.Defaults.PollInterval.Duration = -time.Second }, "defaults.poll_interval: 必須大於 0"},
		{"negative default alert_after", func(c *Config) { c.Defaults.AlertAfter.Duration = -time.Minute }, "defaults.alert_after: 必須大於 0"},
		{"negative mailbox max_age", func(c *Config) { c.Mailboxes["gmail"].MaxAge.Duration = -time.Minute }, "mailboxes.gmail.max_age: 必須大於 0"},
		{"negative mailbox ttl", func(c *Config) { c.Mailboxes["gmail"].TTL.Duration = -time.Minute }, "mailboxes.gmail.ttl: 必須大於 0"},
		{"negative mailbox poll_interval", func(c *Config) { c.Mailboxes["gmail"].PollInterval.Duration = -time.Second }, "mailboxes.gmail.poll_interval: 必須大於 0"},
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

// A negative duration in the YAML survives applyDefaults (only zero is replaced).
func TestValidateNegativeDurationFromYAML(t *testing.T) {
	c, err := Parse([]byte(`
defaults: { ttl: -5m }
bots: { main: { token: "t" } }
targets: { me: { bot: main, chat_id: 1 } }
mailboxes:
  gmail:
    host: imap.gmail.com
    username: me@gmail.com
    auth: { type: password, password: pw }
    targets: [me]
    poll_interval: -1s
`), lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate()
	for _, want := range []string{"defaults.ttl: 必須大於 0", "mailboxes.gmail.poll_interval: 必須大於 0"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in error, got %v", want, err)
		}
	}
}

// An explicit 0s must not be silently replaced by the default.
func TestValidateZeroDurationFromYAML(t *testing.T) {
	c, err := Parse([]byte(`
defaults: { alert_after: 0s }
bots: { main: { token: "t" } }
targets: { me: { bot: main, chat_id: 1 } }
mailboxes:
  gmail:
    host: imap.gmail.com
    username: me@gmail.com
    auth: { type: password, password: pw }
    targets: [me]
    ttl: 0s
`), lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate()
	for _, want := range []string{"defaults.alert_after: 必須大於 0", "mailboxes.gmail.ttl: 必須大於 0"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q in error, got %v", want, err)
		}
	}
}

func TestValidateMultipleErrors(t *testing.T) {
	c := validConfig()
	c.LogLevel = "loud"
	c.Bots["main"] = Bot{}
	c.Mailboxes["gmail"].Host = ""
	c.Rules[0].CodeRegex = "("
	err := c.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	want := []string{
		"bots.main.token: 不可為空",
		"log_level: 必須是",
		"mailboxes.gmail.host: 不可為空",
		"rules[0].code_regex: 正規表示式錯誤",
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d problems, want %d:\n%v", len(lines), len(want), err)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("line %d = %q, want prefix %q", i, lines[i], w)
		}
	}
}

// Problems are ordered with numeric indexes compared by value: rules[2] before rules[10].
func TestValidateErrorOrderNatural(t *testing.T) {
	c := validConfig()
	for i := 1; i <= 10; i++ {
		c.Rules = append(c.Rules, Rule{Name: fmt.Sprintf("r%d", i), From: "x", Ignore: true})
	}
	c.Rules[2].Ignore = false
	c.Rules[10].Ignore = false
	err := c.Validate()
	if err == nil {
		t.Fatal("want errors")
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "rules[2]:") || !strings.HasPrefix(lines[1], "rules[10]:") {
		t.Fatalf("want rules[2] before rules[10], got:\n%v", err)
	}
}

func TestNaturalLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"rules[2]", "rules[10]", true},
		{"rules[10]", "rules[2]", false},
		{"rules[9].name", "rules[10]: x", true},
		{"a", "b", true},
		{"b", "a", false},
		{"a", "a", false},
		{"rules[2].name", "rules[2]: x", true},
		{"a1b", "a1c", true},
		{"a", "a1", true},
		{"a1", "a", false},
		{"x[2]", "x[02]", true},
		{"x[02]", "x[2]", false},
	}
	for _, tc := range cases {
		if got := naturalLess(tc.a, tc.b); got != tc.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestValidateNilMailbox(t *testing.T) {
	c := validConfig()
	c.Mailboxes["broken"] = nil
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "mailboxes.broken: 不可為空") {
		t.Fatalf("want nil mailbox error, got %v", err)
	}
}

func TestValidateOKVariants(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
	}{
		{"microsoft_oauth", func(c *Config) {
			c.Mailboxes["gmail"].Auth = Auth{Type: "microsoft_oauth", ClientID: "abc", Tenant: "common"}
		}},
		{"link_regex only", func(c *Config) { c.Rules[0].CodeRegex = ""; c.Rules[0].LinkRegex = `https://x\.example/\S+` }},
		{"ignore only", func(c *Config) { c.Rules[0].CodeRegex = ""; c.Rules[0].Ignore = true }},
		{"subject only", func(c *Config) { c.Rules[0].From = ""; c.Rules[0].Subject = "驗證碼" }},
		{"empty admin", func(c *Config) { c.Admin = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(c)
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateNamesAndLinkRegex(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
		want   string
	}{
		{"bad target name", func(c *Config) { c.Targets["Work Chat"] = Target{Bot: "main", ChatID: 2} }, "targets.Work Chat: 名稱只能包含"},
		{"bad mailbox name", func(c *Config) {
			m := *c.Mailboxes["gmail"]
			c.Mailboxes["Gmail"] = &m
		}, "mailboxes.Gmail: 名稱只能包含"},
		{"bad rule name", func(c *Config) { c.Rules[0].Name = "GitHub" }, "rules[0].name: 名稱只能包含"},
		{"empty rule name", func(c *Config) { c.Rules[0].Name = "" }, "rules[0].name: 名稱只能包含"},
		{"bad link regex", func(c *Config) { c.Rules[0].LinkRegex = "(" }, "rules[0].link_regex: 正規表示式錯誤"},
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
