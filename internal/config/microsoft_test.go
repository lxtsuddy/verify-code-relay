package config

import (
	"strings"
	"testing"
)

func TestMicrosoftAppEffectiveValues(t *testing.T) {
	ms := func(clientID, tenant string) *Mailbox {
		return &Mailbox{Auth: Auth{Type: "microsoft_oauth", ClientID: clientID, Tenant: tenant}}
	}
	for _, tc := range []struct {
		name                 string
		global               Microsoft
		m                    *Mailbox
		wantClient, wantTent string
	}{
		{"old per-mailbox config, tenant set", Microsoft{}, ms("own", "consumers"), "own", "consumers"},
		{"old per-mailbox config, tenant empty stays common", Microsoft{}, ms("own", ""), "own", "common"},
		{"global only", Microsoft{ClientID: "app", Tenant: "consumers"}, ms("", ""), "app", "consumers"},
		{"global without tenant", Microsoft{ClientID: "app"}, ms("", ""), "app", "common"},
		{"override wins", Microsoft{ClientID: "app", Tenant: "consumers"}, ms("own", "organizations"), "own", "organizations"},
		{"client override keeps global tenant", Microsoft{ClientID: "app", Tenant: "consumers"}, ms("own", ""), "own", "consumers"},
		{"tenant override keeps global client", Microsoft{ClientID: "app", Tenant: "consumers"}, ms("", "organizations"), "app", "organizations"},
		{"nothing set", Microsoft{}, ms("", ""), "", "common"},
	} {
		c := &Config{Microsoft: tc.global}
		id, tenant := c.MicrosoftApp(tc.m)
		if id != tc.wantClient || tenant != tc.wantTent {
			t.Errorf("%s: MicrosoftApp = (%q, %q), want (%q, %q)", tc.name, id, tenant, tc.wantClient, tc.wantTent)
		}
	}
}

func TestMicrosoftSectionRoundTrip(t *testing.T) {
	src := "microsoft:\n  client_id: app-id\n  tenant: consumers\nmailboxes:\n  outlook:\n    host: outlook.office365.com\n    username: me@outlook.com\n    auth:\n      type: microsoft_oauth\n"
	c, err := Decode([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if c.Microsoft.ClientID != "app-id" || c.Microsoft.Tenant != "consumers" {
		t.Fatalf("microsoft = %+v", c.Microsoft)
	}
	out, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "microsoft:\n  client_id: app-id\n  tenant: consumers\n") {
		t.Errorf("encoded:\n%s", out)
	}
	// a config without the section (every stored version before it existed) encodes without it
	old := Empty()
	old.Mailboxes["outlook"] = &Mailbox{Host: "outlook.office365.com", Username: "u", Auth: Auth{Type: "microsoft_oauth", ClientID: "own"}}
	out, err = Encode(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "microsoft:") {
		t.Errorf("unset section was written:\n%s", out)
	}
}

func TestParseNoLongerFillsMailboxTenant(t *testing.T) {
	c, err := Parse([]byte("microsoft: { client_id: app, tenant: consumers }\nmailboxes:\n  o: { host: h, username: u, auth: { type: microsoft_oauth } }\n"), lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Mailboxes["o"].Auth.Tenant; got != "" {
		t.Fatalf("defaulted mailbox tenant = %q, want empty (the global tenant applies)", got)
	}
	if id, tenant := c.MicrosoftApp(c.Mailboxes["o"]); id != "app" || tenant != "consumers" {
		t.Fatalf("effective = %q %q", id, tenant)
	}
}

func TestValidateMicrosoftApp(t *testing.T) {
	outlook := func(c *Config, clientID string) {
		c.Mailboxes["outlook"] = &Mailbox{Name: "outlook", DisplayName: "outlook", Host: "outlook.office365.com", Port: 993,
			Username: "me@outlook.com", Auth: Auth{Type: "microsoft_oauth", ClientID: clientID}, Folders: []string{"INBOX"},
			Targets: []string{"me"}, MaxAge: c.Defaults.MaxAge, TTL: c.Defaults.TTL, PollInterval: c.Defaults.PollInterval}
	}
	for _, tc := range []struct {
		name   string
		mutate func(c *Config)
		want   string // "" = valid
	}{
		{"own client id", func(c *Config) { outlook(c, "own") }, ""},
		{"global client id", func(c *Config) { c.Microsoft.ClientID = "app"; outlook(c, "") }, ""},
		{"no client id anywhere", func(c *Config) { outlook(c, "") }, "mailboxes.outlook.auth.client_id: 未設定：請在系統設定填寫 Microsoft 應用程式，或在信箱進階設定覆寫"},
		{"global without Microsoft mailboxes", func(c *Config) { c.Microsoft = Microsoft{ClientID: "app", Tenant: "consumers"} }, ""},
		{"global client id with spaces", func(c *Config) { c.Microsoft.ClientID = " app" }, "microsoft.client_id: 前後不可有空白"},
		{"blank global tenant", func(c *Config) { c.Microsoft.Tenant = "  " }, "microsoft.tenant: 前後不可有空白"},
	} {
		c := validConfig()
		tc.mutate(c)
		err := c.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: error = %v, want %q", tc.name, err, tc.want)
		}
	}
}
