package auth

import (
	"context"
	"net"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

func memServer(t *testing.T) string {
	t.Helper()
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("me@example.com", "pw")
	u.Create("INBOX", nil)
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func TestPassword(t *testing.T) {
	addr := memServer(t)
	for _, tc := range []struct {
		pw   string
		want bool
	}{{"pw", true}, {"bad", false}} {
		c, err := imapclient.DialInsecure(addr, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = Password{Username: "me@example.com", Password: tc.pw}.Authenticate(context.Background(), c)
		if (err == nil) != tc.want {
			t.Errorf("pw %q: err = %v", tc.pw, err)
		}
		c.Close()
	}
}

func TestXOAUTH2InitialResponse(t *testing.T) {
	mech, ir, err := NewXOAUTH2("me@outlook.com", "TOKEN").Start()
	if err != nil || mech != "XOAUTH2" {
		t.Fatalf("mech=%q err=%v", mech, err)
	}
	if string(ir) != "user=me@outlook.com\x01auth=Bearer TOKEN\x01\x01" {
		t.Errorf("ir = %q", ir)
	}
	// On failure the server sends a JSON challenge; the client must answer with an empty response.
	resp, err := NewXOAUTH2("u", "t").Next([]byte(`{"status":"401"}`))
	if err != nil || len(resp) != 0 {
		t.Errorf("Next = %q, %v", resp, err)
	}
}
