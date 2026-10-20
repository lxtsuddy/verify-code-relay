package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeDoesNotExpandOrDefault(t *testing.T) {
	c, err := Decode([]byte("bots:\n  main: { token: \"abc${X}$$\" }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Bots["main"].Token != "abc${X}$$" {
		t.Errorf("token = %q, want literal", c.Bots["main"].Token)
	}
	if c.LogLevel != "" || c.Defaults.MaxAge.Set {
		t.Errorf("defaults applied: %+v", c)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	if _, err := Decode([]byte("nope: 1\n")); err == nil {
		t.Fatal("want error for unknown field")
	}
}

func TestEncodeOmitsUnset(t *testing.T) {
	c := Empty()
	c.Bots["main"] = Bot{Token: "t"}
	c.Defaults.TTL = Duration{Duration: 5 * time.Minute, Set: true}
	out, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, bad := range []string{"max_age", "log_level", "mailboxes", "templates", "data_dir"} {
		if strings.Contains(s, bad) {
			t.Errorf("encoded output contains unset %q:\n%s", bad, s)
		}
	}
	if !strings.Contains(s, "ttl: 5m0s") {
		t.Errorf("ttl missing:\n%s", s)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	env := map[string]string{"TG_BOT_MAIN": "1:a", "TG_BOT_WORK": "2:b", "GMAIL_APP_PASSWORD": "pw ${not} $$"}
	raw, err := LoadRawBytes(mustRead(t, "../../config.example.yaml"), func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	enc, err := Encode(raw)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(enc)
	if err != nil {
		t.Fatalf("decode encoded: %v\n%s", err, enc)
	}
	enc2, _ := Encode(back)
	if string(enc) != string(enc2) {
		t.Errorf("round trip changed output:\n%s\n---\n%s", enc, enc2)
	}
	if back.Mailboxes["personal_gmail"].Auth.Password != "pw ${not} $$" {
		t.Errorf("secret changed: %q", back.Mailboxes["personal_gmail"].Auth.Password)
	}
	if back.Mailboxes["outlook"].MaxAge.Duration != 5*time.Minute || back.Mailboxes["personal_gmail"].MaxAge.Set {
		t.Errorf("durations not preserved")
	}
}

func TestDefaultedDoesNotMutate(t *testing.T) {
	c := Empty()
	c.Mailboxes["m"] = &Mailbox{Host: "h"}
	d := c.Defaulted()
	if c.LogLevel != "" || c.Mailboxes["m"].Port != 0 {
		t.Error("Defaulted mutated the original")
	}
	if d.LogLevel != "info" || d.Mailboxes["m"].Port != 993 || d.Mailboxes["m"].Name != "m" {
		t.Errorf("defaults not applied: %+v", d.Mailboxes["m"])
	}
}

func TestEmptyConfigIsValid(t *testing.T) {
	if err := Empty().Defaulted().Validate(); err != nil {
		t.Fatalf("empty config must be valid: %v", err)
	}
}

func TestCloneIsDeepForRawConfigs(t *testing.T) {
	c := Empty()
	c.Bots["main"] = Bot{Token: "t"}
	c.Mailboxes["m"] = &Mailbox{
		Host:      "h",
		Folders:   []string{"INBOX"},
		TTL:       Duration{Duration: time.Minute, Set: true},
		Templates: Templates{Code: "code"},
	}
	d := c.Clone()
	if d.Mailboxes["m"].Name != "m" {
		t.Errorf("Name = %q, want m", d.Mailboxes["m"].Name)
	}
	if d.Mailboxes["m"].TTL != c.Mailboxes["m"].TTL || d.Mailboxes["m"].Templates.Code != "code" {
		t.Errorf("clone differs: %+v", d.Mailboxes["m"])
	}
	d.Bots["other"] = Bot{Token: "x"}
	d.Mailboxes["m"].Host = "changed"
	d.Mailboxes["m"].Folders[0] = "Changed"
	d.Mailboxes["new"] = &Mailbox{}
	if len(c.Bots) != 1 || len(c.Mailboxes) != 1 {
		t.Errorf("original maps changed: %d bots, %d mailboxes", len(c.Bots), len(c.Mailboxes))
	}
	if c.Mailboxes["m"].Host != "h" || c.Mailboxes["m"].Folders[0] != "INBOX" {
		t.Errorf("original mailbox changed: %+v", c.Mailboxes["m"])
	}
}

func TestExplicitZeroDurationSurvivesRoundTrip(t *testing.T) {
	c := Empty()
	c.Defaults.TTL = Duration{Set: true}
	out, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !back.Defaults.TTL.Set || back.Defaults.TTL.Duration != 0 {
		t.Errorf("explicit 0s lost: %+v\n%s", back.Defaults.TTL, out)
	}
}

func TestDecodeEmptyDocument(t *testing.T) {
	c, err := Decode([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if c.Bots == nil || c.Targets == nil || c.Mailboxes == nil {
		t.Errorf("maps not initialised: %+v", c)
	}
}
