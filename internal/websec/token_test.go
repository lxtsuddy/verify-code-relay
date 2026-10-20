package websec

import "testing"

func TestRandomToken(t *testing.T) {
	a, b := RandomToken(), RandomToken()
	if len(a) != 43 || a == b {
		t.Errorf("tokens %q %q", a, b)
	}
}

func TestHashToken(t *testing.T) {
	if got := HashToken("a"); got != "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb" {
		t.Errorf("got %s", got)
	}
}

func TestEqual(t *testing.T) {
	if !Equal("abc", "abc") || Equal("abc", "abd") || Equal("abc", "abcd") || Equal("", "a") || !Equal("", "") {
		t.Error("Equal wrong")
	}
}
