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
