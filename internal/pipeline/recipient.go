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
