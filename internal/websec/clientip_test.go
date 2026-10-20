package websec

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted, err := ParsePrefixes("10.42.0.0/16, 192.168.1.5")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.9:1234", "", "203.0.113.9"},
		{"203.0.113.9:1234", "1.2.3.4", "203.0.113.9"},                           // untrusted peer: XFF ignored
		{"10.42.0.7:1234", "198.51.100.2, 192.168.1.5", "198.51.100.2"},          // Traefik <- Caddy <- client
		{"10.42.0.7:1234", "6.6.6.6, 198.51.100.2, 192.168.1.5", "198.51.100.2"}, // spoofed left entry ignored
		{"10.42.0.7:1234", "", "10.42.0.7"},
		{"10.42.0.7:1234", "192.168.1.5", "192.168.1.5"}, // everything trusted: left-most
		{"10.42.0.7:1234", "garbage, 198.51.100.2", "198.51.100.2"},
		{"[2001:db8::1]:443", "", "2001:db8::1"},
		{"10.42.0.7:1234", "::ffff:198.51.100.2", "198.51.100.2"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(r, trusted); got != c.want {
			t.Errorf("remote=%s xff=%q: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
	if _, err := ParsePrefixes("nope"); err == nil {
		t.Error("bad prefix accepted")
	}
}

func TestClientIPMultipleHeaders(t *testing.T) {
	trusted, _ := ParsePrefixes("10.0.0.0/8")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Add("X-Forwarded-For", "6.6.6.6")
	r.Header.Add("X-Forwarded-For", "198.51.100.2, 10.0.0.9")
	if got := ClientIP(r, trusted); got != "198.51.100.2" {
		t.Errorf("got %s", got)
	}
}

func TestParsePrefixes(t *testing.T) {
	p, err := ParsePrefixes(" 10.0.0.0/8 ,, ::1, 192.168.1.5 ")
	if err != nil || len(p) != 3 {
		t.Fatalf("%v %v", p, err)
	}
	if p[1].Bits() != 128 || p[2].Bits() != 32 {
		t.Errorf("single IPs: %v", p)
	}
	if p, err := ParsePrefixes(""); err != nil || len(p) != 0 {
		t.Errorf("empty: %v %v", p, err)
	}
}

func TestClientIPNoTrusted(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := ClientIP(r, nil); got != "10.0.0.1" {
		t.Errorf("got %s", got)
	}
}
