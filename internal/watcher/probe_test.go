package watcher

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"verify-code-relay/internal/auth"
)

// noselectSession adds a \Noselect folder (like Gmail's "[Gmail]") to every LIST.
type noselectSession struct{ imapserver.SessionIMAP4rev2 }

func (s noselectSession) List(w *imapserver.ListWriter, ref string, patterns []string, opts *imap.ListOptions) error {
	if err := w.WriteList(&imap.ListData{Attrs: []imap.MailboxAttr{imap.MailboxAttrNoSelect}, Delim: '/', Mailbox: "[Gmail]"}); err != nil {
		return err
	}
	return s.SessionIMAP4rev2.List(w, ref, patterns, opts)
}

func startProbeServer(t *testing.T) DialFunc {
	t.Helper()
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("u", "pw")
	for _, f := range []string{"INBOX", "Spam", "Archive"} {
		if err := u.Create(f, nil); err != nil {
			t.Fatal(err)
		}
	}
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return noselectSession{mem.NewSession().(imapserver.SessionIMAP4rev2)}, nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
		Logger:       nopLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", ln.Addr().String())
		if err != nil {
			return nil, err
		}
		return imapclient.New(conn, opts), nil
	}
}

func TestProbeListsSelectableFoldersInboxFirst(t *testing.T) {
	dial := startProbeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := Probe(ctx, dial, auth.Password{Username: "u", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"INBOX", "Archive", "Spam"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("folders = %q, want %q", got, want)
	}
}

func TestProbeWrongPasswordIsAuthError(t *testing.T) {
	dial := startProbeServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Probe(ctx, dial, auth.Password{Username: "u", Password: "nope"})
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want *AuthError", err)
	}
}

func TestProbeDialFailure(t *testing.T) {
	dial := func(context.Context, *imapclient.Options) (*imapclient.Client, error) {
		return nil, errors.New("connection refused")
	}
	_, err := Probe(context.Background(), dial, auth.Password{Username: "u", Password: "pw"})
	var ae *AuthError
	if err == nil || errors.As(err, &ae) {
		t.Fatalf("err = %v, want a plain connection error", err)
	}
}

// A server that accepts the connection but never answers must not hold Probe past ctx.
func TestProbeHonoursContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // silent: no greeting
		}
	}()
	dial := func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return nil, err
		}
		return imapclient.New(conn, opts), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Probe(ctx, dial, auth.Password{Username: "u", Password: "pw"}); err == nil {
		t.Fatal("probe of a silent server succeeded")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("probe took %v after ctx expired", d)
	}
}
