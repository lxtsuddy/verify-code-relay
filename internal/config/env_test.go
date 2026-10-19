package config

import (
	"strings"
	"testing"
)

func lookupFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestExpandString(t *testing.T) {
	env := lookupFrom(map[string]string{"A": "x", "QUOTE": `p"a#ss: }`})
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"${A}", "x"},
		{"pre-${A}-post", "pre-x-post"},
		{"$$100", "$100"},
		{"cost $5", "cost $5"},
		{"${QUOTE}", `p"a#ss: }`},
	}
	for _, c := range cases {
		got, err := expandString(c.in, env)
		if err != nil || got != c.want {
			t.Errorf("expandString(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestExpandStringMissing(t *testing.T) {
	_, err := expandString("${NOPE}", lookupFrom(nil))
	if err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("want error mentioning NOPE, got %v", err)
	}
}

func TestExpandStringUnterminated(t *testing.T) {
	if _, err := expandString("${A", lookupFrom(nil)); err == nil {
		t.Fatal("want error for unterminated ${")
	}
}
