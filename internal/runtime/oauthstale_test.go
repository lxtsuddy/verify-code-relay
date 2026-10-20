package runtime

import (
	"slices"
	"testing"

	"verify-code-relay/internal/config"
)

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
	if got := StaleOAuthTokens(before, after); !slices.Equal(got, want) {
		t.Errorf("stale = %v, want %v", got, want)
	}
	// previous mailboxes unknown: every Microsoft mailbox
	if got := StaleOAuthTokens(nil, after); !slices.Equal(got, []string{"becameMS", "host", "newMS", "same", "user"}) {
		t.Errorf("stale without before = %v", got)
	}
}
