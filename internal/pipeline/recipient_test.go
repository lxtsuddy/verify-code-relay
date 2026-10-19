package pipeline

import (
	"testing"

	"verify-code-relay/internal/mailparse"
)

func TestRecipient(t *testing.T) {
	addrs := func(list ...string) []mailparse.Address {
		var out []mailparse.Address
		for _, a := range list {
			out = append(out, mailparse.Address{Addr: a})
		}
		return out
	}
	cases := []struct {
		to       []mailparse.Address
		username string
		want     string
	}{
		{addrs("other@x.com", "Me+GitHub@gmail.com"), "me@gmail.com", "Me+GitHub@gmail.com"},
		{addrs("m.e+shop@gmail.com"), "me@gmail.com", "m.e+shop@gmail.com"},
		{addrs("m.e@outlook.com"), "me@outlook.com", "m.e@outlook.com"}, // no match: dots matter outside gmail -> fallback first
		{addrs("list@lists.x.org"), "me@example.com", "list@lists.x.org"},
		{nil, "me@example.com", ""},
		{addrs("me+a@example.com"), "me@example.com", "me+a@example.com"},
	}
	for _, c := range cases {
		if got := Recipient(c.to, c.username); got != c.want {
			t.Errorf("Recipient(%v, %q) = %q, want %q", c.to, c.username, got, c.want)
		}
	}
	// a matching address wins over an earlier non-matching one
	if got := Recipient(addrs("a@b.c", "me+x@example.com"), "me@example.com"); got != "me+x@example.com" {
		t.Errorf("got %q", got)
	}
}
