package websec

import (
	"strings"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil || !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash = %q %v", h, err)
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Error("salt not random")
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Errorf("verify good: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Error("verify bad password succeeded")
	}
	if _, err := VerifyPassword("$argon2id$garbage", "x"); err == nil {
		t.Error("malformed hash accepted")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, enc := range []string{
		"",
		"plain",
		"$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2E$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=18$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2E$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=x,t=2,p=1$c2FsdHNhbHRzYWx0c2E$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2E$!!!",
		"$argon2id$v=19$m=19456,t=2,p=1$c2FsdHNhbHRzYWx0c2E$",
		"$argon2id$v=19$m=4000000000,t=2,p=1$c2FsdHNhbHRzYWx0c2E$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=19456,t=0,p=1$c2FsdHNhbHRzYWx0c2E$AAAAAAAAAAAAAAAAAAAAAA",
		"$argon2id$v=19$m=19456,t=2,p=0$c2FsdHNhbHRzYWx0c2E$AAAAAAAAAAAAAAAAAAAAAA",
	} {
		if ok, err := VerifyPassword(enc, "x"); ok || err == nil {
			t.Errorf("%q: ok=%v err=%v, want error", enc, ok, err)
		}
	}
}
