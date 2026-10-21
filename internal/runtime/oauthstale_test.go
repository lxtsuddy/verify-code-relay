package runtime

import (
	"slices"
	"testing"

	"verify-code-relay/internal/config"
)

func withMailboxes(app config.Microsoft, m map[string]*config.Mailbox) *config.Config {
	return &config.Config{Microsoft: app, Mailboxes: m}
}

func TestStaleOAuthTokens(t *testing.T) {
	ms := func(host, user string) *config.Mailbox {
		return &config.Mailbox{Host: host, Username: user, Auth: config.Auth{Type: "microsoft_oauth", ClientID: "app"}}
	}
	pw := &config.Mailbox{Host: "imap.gmail.com", Username: "u", Auth: config.Auth{Type: "password", Password: "p"}}
	same := ms("outlook.office365.com", "a")
	sameWritten := ms("OUTLOOK.office365.com", "a")
	sameWritten.Port, sameWritten.Auth.Tenant, sameWritten.DisplayName = 993, "common", "renamed"
	before := map[string]*config.Mailbox{
		"same": same, "host": ms("outlook.office365.com", "a"), "user": ms("outlook.office365.com", "a"),
		"gone": ms("outlook.office365.com", "a"), "toPassword": ms("outlook.office365.com", "a"),
		"becameMS": pw, "stillPassword": pw, "gonePassword": pw,
	}
	after := map[string]*config.Mailbox{
		"same": sameWritten, "host": ms("imap-mail.outlook.com", "a"), "user": ms("outlook.office365.com", "b"),
		"toPassword": pw, "becameMS": ms("outlook.office365.com", "a"), "stillPassword": pw,
		"newMS": ms("outlook.office365.com", "a"), "newPassword": pw,
	}
	want := []string{"becameMS", "gone", "gonePassword", "host", "newMS", "toPassword", "user"}
	if got := StaleOAuthTokens(withMailboxes(config.Microsoft{}, before), withMailboxes(config.Microsoft{}, after)); !slices.Equal(got, want) {
		t.Errorf("stale = %v, want %v", got, want)
	}
	// previous mailboxes unknown: every Microsoft mailbox
	if got := StaleOAuthTokens(nil, withMailboxes(config.Microsoft{}, after)); !slices.Equal(got, []string{"becameMS", "host", "newMS", "same", "user"}) {
		t.Errorf("stale without before = %v", got)
	}
}

// Tokens are bound to the effective client_id and tenant: a change to the microsoft section
// makes stale exactly the mailboxes that use it.
func TestStaleOAuthTokensGlobalApp(t *testing.T) {
	ms := func(clientID, tenant string) *config.Mailbox {
		return &config.Mailbox{Host: "outlook.office365.com", Username: "a", Auth: config.Auth{Type: "microsoft_oauth", ClientID: clientID, Tenant: tenant}}
	}
	mailboxes := func() map[string]*config.Mailbox {
		return map[string]*config.Mailbox{
			"default": ms("", ""), "ownClient": ms("own", ""), "ownTenant": ms("", "organizations"), "ownBoth": ms("own", "consumers"),
			"pw": {Host: "imap.gmail.com", Username: "u", Auth: config.Auth{Type: "password", Password: "p"}},
		}
	}
	old := config.Microsoft{ClientID: "app", Tenant: "consumers"}
	for _, tc := range []struct {
		name string
		next config.Microsoft
		want []string
	}{
		{"unchanged", old, nil},
		{"client_id changed", config.Microsoft{ClientID: "app2", Tenant: "consumers"}, []string{"default", "ownTenant"}},
		{"tenant changed", config.Microsoft{ClientID: "app", Tenant: "organizations"}, []string{"default", "ownClient"}},
		{"tenant cleared (common)", config.Microsoft{ClientID: "app"}, []string{"default", "ownClient"}},
		{"both changed", config.Microsoft{ClientID: "app2", Tenant: "common"}, []string{"default", "ownClient", "ownTenant"}},
	} {
		got := StaleOAuthTokens(withMailboxes(old, mailboxes()), withMailboxes(tc.next, mailboxes()))
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: stale = %v, want %v", tc.name, got, tc.want)
		}
	}
	// "" and "common" are the same tenant
	if got := StaleOAuthTokens(withMailboxes(config.Microsoft{ClientID: "app"}, mailboxes()), withMailboxes(config.Microsoft{ClientID: "app", Tenant: "common"}, mailboxes())); got != nil {
		t.Errorf("blank to common: stale = %v", got)
	}
	// moving a mailbox's own value to the microsoft section changes nothing it signs in with
	before := withMailboxes(config.Microsoft{}, map[string]*config.Mailbox{"o": ms("own", "consumers")})
	after := withMailboxes(config.Microsoft{ClientID: "own", Tenant: "consumers"}, map[string]*config.Mailbox{"o": ms("", "")})
	if got := StaleOAuthTokens(before, after); got != nil {
		t.Errorf("moving to the global app: stale = %v", got)
	}
}
