package runtime

import (
	"context"
	"slices"
	"strings"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/store"
)

// Stored Microsoft OAuth tokens belong to one account and server. Every way of storing a
// new config version (a save or restore on the Web, `relay config import`) compares each
// mailbox before and after with StaleOAuthTokens and deletes the tokens that no longer fit
// once the new version is stored (DeleteOAuthTokens).

const authMicrosoft = "microsoft_oauth"

// MSAccount is what a Microsoft token belongs to: the account and app it was issued for
// and the server it may be sent to.
type MSAccount struct {
	Username, ClientID, Tenant string // Tenant "" written out as "common"
	Host                       string // lower case
	Port                       int    // 0 written out as 993
}

// MSAccountOf returns the account a Microsoft token of mailbox m is for.
func MSAccountOf(m *config.Mailbox) MSAccount {
	tenant, port := m.Auth.Tenant, m.Port
	if tenant == "" {
		tenant = "common"
	}
	if port == 0 {
		port = 993
	}
	return MSAccount{m.Username, m.Auth.ClientID, tenant, strings.ToLower(m.Host), port}
}

// TokenInvalidated reports whether an OAuth token stored for a mailbox that was cur (nil:
// it did not exist) no longer fits it once it is next (nil: removed). A removed mailbox
// always loses its token; a mailbox that uses Microsoft afterwards keeps it only when it
// used Microsoft before with the same account, client_id, tenant, host and port, so a
// mailbox that just became Microsoft never inherits a token left over from earlier.
func TokenInvalidated(cur, next *config.Mailbox) bool {
	switch {
	case next == nil:
		return cur != nil
	case next.Auth.Type != authMicrosoft:
		return cur != nil && cur.Auth.Type == authMicrosoft
	}
	return cur == nil || cur.Auth.Type != authMicrosoft || MSAccountOf(cur) != MSAccountOf(next)
}

// StaleOAuthTokens returns, sorted, the mailboxes of before or after whose stored OAuth
// token (if any) no longer fits after (TokenInvalidated). A nil before means the previous
// mailboxes are unknown: every Microsoft mailbox of after is then stale.
func StaleOAuthTokens(before, after map[string]*config.Mailbox) []string {
	var names []string
	for name, m := range before {
		if TokenInvalidated(m, after[name]) {
			names = append(names, name)
		}
	}
	for name, m := range after {
		if _, ok := before[name]; !ok && TokenInvalidated(nil, m) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// DeleteOAuthTokens deletes the stored tokens of the given mailboxes. dropped lists those
// that had one and lost it; failed those whose token may still be stored, each error
// going to onErr (may be nil). Mailboxes without a stored token are left out of both.
func DeleteOAuthTokens(ctx context.Context, st *store.Store, names []string, onErr func(name string, err error)) (dropped, failed []string) {
	for _, name := range names {
		_, ok, err := st.GetOAuthToken(ctx, name)
		if err == nil && !ok {
			continue // nothing stored
		}
		if err == nil {
			err = st.DeleteOAuthToken(ctx, name)
		}
		if err != nil {
			if onErr != nil {
				onErr(name, err)
			}
			failed = append(failed, name)
			continue
		}
		dropped = append(dropped, name)
	}
	return dropped, failed
}
