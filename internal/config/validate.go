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
	positive := func(path string, d Duration) {
		// applyDefaults only fills omitted values, so an explicit 0s or a negative value fails here.
		if d.Duration <= 0 {
			p.add(path, "必須大於 0")
		}
	}
	positive("defaults.max_age", c.Defaults.MaxAge)
	positive("defaults.ttl", c.Defaults.TTL)
	positive("defaults.alert_ttl", c.Defaults.AlertTTL)
	positive("defaults.poll_interval", c.Defaults.PollInterval)
	positive("defaults.alert_after", c.Defaults.AlertAfter)
	if c.Admin != "" {
		if _, ok := c.Targets[c.Admin]; !ok {
			p.add("admin", "找不到目標 %q", c.Admin)
		}
	}
	for _, name := range sortedKeys(c.Mailboxes) {
		m := c.Mailboxes[name]
		base := "mailboxes." + name
		checkName(base, name)
		if m == nil {
			// Parse never leaves nil entries; only hand-built configs can.
			p.add(base, "不可為空")
			continue
		}
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
		seenFolder := map[string]bool{}
		for i, f := range m.Folders {
			path := fmt.Sprintf("%s.folders[%d]", base, i)
			switch {
			case f == "":
				p.add(path, "不可為空")
			case seenFolder[f]:
				p.add(path, "資料夾 %q 重複", f)
			}
			seenFolder[f] = true
		}
		if len(m.Targets) == 0 {
			p.add(base+".targets", "至少需要一個目標")
		}
		seenTarget := map[string]bool{}
		for i, t := range m.Targets {
			path := fmt.Sprintf("%s.targets[%d]", base, i)
			if _, ok := c.Targets[t]; !ok {
				p.add(path, "找不到目標 %q", t)
			}
			if seenTarget[t] {
				p.add(path, "目標 %q 重複", t)
			}
			seenTarget[t] = true
		}
		positive(base+".max_age", m.MaxAge)
		positive(base+".ttl", m.TTL)
		positive(base+".poll_interval", m.PollInterval)
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
	sort.SliceStable(p, func(i, j int) bool { return naturalLess(p[i], p[j]) })
	return errors.New(strings.Join(p, "\n"))
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// naturalLess compares strings byte by byte, except that runs of ASCII digits
// are compared by numeric value, so "rules[2]" sorts before "rules[10]".
// Equal numbers with more leading zeros sort after.
func naturalLess(a, b string) bool {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if !isDigit(a[i]) || !isDigit(b[j]) {
			if a[i] != b[j] {
				return a[i] < b[j]
			}
			i++
			j++
			continue
		}
		si, sj := i, j
		for i < len(a) && isDigit(a[i]) {
			i++
		}
		for j < len(b) && isDigit(b[j]) {
			j++
		}
		na := strings.TrimLeft(a[si:i], "0")
		nb := strings.TrimLeft(b[sj:j], "0")
		if len(na) != len(nb) {
			return len(na) < len(nb)
		}
		if na != nb {
			return na < nb
		}
		if i-si != j-sj {
			return i-si < j-sj
		}
	}
	return len(a)-i < len(b)-j
}
