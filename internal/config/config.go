// Package config loads and validates the relay YAML configuration.
package config

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

var yamlLineRe = regexp.MustCompile(`line \d+: `)

// Duration is a time.Duration read from YAML. Set records whether the value was
// given explicitly, so an explicit 0s is not mistaken for an omitted value.
type Duration struct {
	time.Duration
	Set bool
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("第 %d 行：無效的時間長度 %q", n.Line, n.Value)
	}
	d.Duration = v
	d.Set = true
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
		// Decode straight from the node tree so nodes keep their original line numbers.
		if err := root.Decode(&c); err != nil {
			return nil, fmt.Errorf("解析設定：%w", err)
		}
		// Re-encoding is only used to detect unknown fields (yaml.Node.Decode lacks KnownFields).
		// Its line numbers refer to the re-encoded text, so they are stripped from the message.
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		if err := enc.Encode(&root); err != nil {
			return nil, err
		}
		var check Config
		dec := yaml.NewDecoder(&buf)
		dec.KnownFields(true)
		if err := dec.Decode(&check); err != nil {
			return nil, fmt.Errorf("解析設定：%s", yamlLineRe.ReplaceAllString(err.Error(), ""))
		}
	}
	c.applyDefaults()
	return &c, nil
}

// orDur fills in def when the value was omitted. An explicit value (even 0s) is
// kept so that Validate can reject it.
func orDur(v *Duration, def time.Duration) {
	if !v.Set {
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
