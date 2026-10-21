package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var nameRe = regexp.MustCompile(`^[a-z0-9_-]+$`)

// tokenRe is the shape of a Bot API token ("<bot id>:<secret>"); telegram.ValidToken
// checks the same, and the client refuses any other token.
var tokenRe = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

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
		switch tok := c.Bots[name].Token; {
		case tok == "":
			p.add("bots."+name+".token", "不可為空")
		case !tokenRe.MatchString(tok):
			// never quote the value: it is a secret
			p.add("bots."+name+".token", "格式不正確（應為 數字:英數字）")
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
	for path, v := range map[string]string{"microsoft.client_id": c.Microsoft.ClientID, "microsoft.tenant": c.Microsoft.Tenant} {
		if v != strings.TrimSpace(v) {
			p.add(path, "前後不可有空白")
		}
	}
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
			if id, _ := c.MicrosoftApp(m); id == "" {
				p.add(base+".auth.client_id", "未設定：請在系統設定填寫 Microsoft 應用程式，或在信箱進階設定覆寫")
			}
		default:
			p.add(base+".auth.type", "未知的認證類型 %q（可用：password、microsoft_oauth）", m.Auth.Type)
		}
		if len(m.Folders) > 0 || len(m.Targets) > 0 {
			p.add(base+".routes", "routes 不能與舊的 folders／targets 同時使用")
		}
		if len(m.Routes) == 0 {
			p.add(base+".routes", "至少需要一條轉發路由")
		}
		first := map[Route]int{}
		for i, r := range m.Routes {
			path := fmt.Sprintf("%s.routes[%d]", base, i)
			if r.Folder == "" {
				p.add(path+".folder", "不可為空")
			}
			switch _, ok := c.Targets[r.Target]; {
			case r.Target == "":
				p.add(path+".target", "不可為空")
			case !ok:
				p.add(path+".target", "找不到目標 %q", r.Target)
			}
			if j, dup := first[r]; dup {
				p.add(path, "與第 %d 條路由重複", j+1)
			} else {
				first[r] = i
			}
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
