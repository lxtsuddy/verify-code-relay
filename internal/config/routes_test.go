package config

import (
	"reflect"
	"strings"
	"testing"
)

const routesBase = `
bots: { main: { token: "1:t" } }
targets: { me: { bot: main, chat_id: 1 }, fam: { bot: main, chat_id: 2 } }
mailboxes:
  work:
    host: imap.example.com
    username: me@example.com
    auth: { type: password, password: pw }
`

func decodeOK(t *testing.T, yaml string) *Config {
	t.Helper()
	c, err := Decode([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRoutesDecodeAndRoundTrip(t *testing.T) {
	c := decodeOK(t, routesBase+`
    routes:
      - { folder: INBOX, target: me }
      - { folder: Spam, target: fam }
`)
	want := []Route{{"INBOX", "me"}, {"Spam", "fam"}}
	if got := c.Mailboxes["work"].Routes; !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v", got)
	}
	data, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "folders") || strings.Contains(string(data), "targets: [") {
		t.Errorf("encoded config still has the old fields:\n%s", data)
	}
	if again := decodeOK(t, string(data)); !reflect.DeepEqual(again.Mailboxes["work"].Routes, want) {
		t.Errorf("round trip lost routes: %v", again.Mailboxes["work"].Routes)
	}
}

// The old folders × targets become routes in that order, so mail goes where it went.
func TestRoutesFromLegacyFields(t *testing.T) {
	c := decodeOK(t, routesBase+`
    folders: [INBOX, Spam]
    targets: [me, fam]
`)
	m := c.Mailboxes["work"]
	want := []Route{{"INBOX", "me"}, {"INBOX", "fam"}, {"Spam", "me"}, {"Spam", "fam"}}
	if !reflect.DeepEqual(m.Routes, want) {
		t.Fatalf("routes = %v", m.Routes)
	}
	if m.Folders != nil || m.Targets != nil {
		t.Errorf("old fields kept: %v %v", m.Folders, m.Targets)
	}
	// no folders meant INBOX
	c = decodeOK(t, routesBase+`
    targets: [me]
`)
	if got := c.Mailboxes["work"].Routes; !reflect.DeepEqual(got, []Route{{"INBOX", "me"}}) {
		t.Errorf("routes without folders = %v", got)
	}
	if err := c.Defaulted().Validate(); err != nil {
		t.Errorf("converted config invalid: %v", err)
	}
}

// A mailbox built in Go with the old fields is converted when defaults are applied.
func TestRoutesFromLegacyStruct(t *testing.T) {
	c := Empty()
	c.Bots["main"] = Bot{Token: "1:t"}
	c.Targets["me"] = Target{Bot: "main", ChatID: 1}
	c.Mailboxes["m"] = &Mailbox{Host: "h", Username: "u", Auth: Auth{Type: "password", Password: "p"},
		Folders: []string{"INBOX", "Spam"}, Targets: []string{"me"}}
	d := c.Defaulted()
	if got := d.Mailboxes["m"].Routes; !reflect.DeepEqual(got, []Route{{"INBOX", "me"}, {"Spam", "me"}}) {
		t.Fatalf("routes = %v", got)
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRoutesAndLegacyFieldsTogether(t *testing.T) {
	c := decodeOK(t, routesBase+`
    targets: [me]
    routes:
      - { folder: INBOX, target: me }
`)
	err := c.Defaulted().Validate()
	if err == nil || !strings.Contains(err.Error(), "mailboxes.work.routes: routes 不能與舊的 folders／targets 同時使用") {
		t.Fatalf("err = %v", err)
	}
}

func TestRouteHelpers(t *testing.T) {
	m := &Mailbox{Routes: []Route{{"Spam", "fam"}, {"INBOX", "me"}, {"Spam", "me"}, {"INBOX", "me"}, {"INBOX", "fam"}}}
	if got := m.WatchedFolders(); !reflect.DeepEqual(got, []string{"Spam", "INBOX"}) {
		t.Errorf("WatchedFolders = %v", got)
	}
	if got := m.TargetsOf("INBOX"); !reflect.DeepEqual(got, []string{"me", "fam"}) {
		t.Errorf("TargetsOf(INBOX) = %v", got)
	}
	if got := m.TargetsOf("Archive"); got != nil {
		t.Errorf("TargetsOf(Archive) = %v", got)
	}
	if got := m.RouteTargets(); !reflect.DeepEqual(got, []string{"fam", "me"}) {
		t.Errorf("RouteTargets = %v", got)
	}
}

func TestValidateRoutes(t *testing.T) {
	cases := []struct {
		name   string
		routes []Route
		want   string
	}{
		{"none", nil, "mailboxes.work.routes: 至少需要一條轉發路由"},
		{"empty folder", []Route{{"", "me"}}, "mailboxes.work.routes[0].folder: 不可為空"},
		{"empty target", []Route{{"INBOX", ""}}, "mailboxes.work.routes[0].target: 不可為空"},
		{"unknown target", []Route{{"INBOX", "nobody"}}, `mailboxes.work.routes[0].target: 找不到目標 "nobody"`},
		{"duplicate", []Route{{"INBOX", "me"}, {"Spam", "me"}, {"INBOX", "me"}}, "mailboxes.work.routes[2]: 與第 1 條路由重複"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := decodeOK(t, routesBase)
			c.Mailboxes["work"].Routes = tc.routes
			err := c.Defaulted().Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	// the same folder to two targets, and two folders to one target, are fine
	c := decodeOK(t, routesBase)
	c.Mailboxes["work"].Routes = []Route{{"INBOX", "me"}, {"INBOX", "fam"}, {"Spam", "me"}}
	if err := c.Defaulted().Validate(); err != nil {
		t.Fatal(err)
	}
}
