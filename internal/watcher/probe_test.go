package watcher

import (
	"bytes"
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

	"github.com/lxtsuddy/verify-code-relay/internal/auth"
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
	addr := startProbeAddr(t)
	return func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return imapclient.New(conn, opts), nil
	}
}

// startProbeAddr runs the server for the probe tests (user u / pw; INBOX, Spam, Archive
// and a \Noselect "[Gmail]") and returns its address.
func startProbeAddr(t *testing.T) string {
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
	return ln.Addr().String()
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

// swallowLogout drops the client's LOGOUT command, so the server never answers it.
type swallowLogout struct{ net.Conn }

func (c swallowLogout) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("LOGOUT")) {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func TestProbeDoesNotWaitLongForLogout(t *testing.T) {
	old := probeLogoutWait
	probeLogoutWait = 200 * time.Millisecond
	defer func() { probeLogoutWait = old }()
	addr := startProbeAddr(t)
	dial := func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, err
		}
		return imapclient.New(swallowLogout{conn}, opts), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	got, err := Probe(ctx, dial, auth.Password{Username: "u", Password: "pw"})
	if err != nil || len(got) != 3 {
		t.Fatalf("probe = %q, %v", got, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("probe waited %v for LOGOUT", d)
	}
}

// failList answers LIST with NO after a normal login.
type failList struct{ imapserver.SessionIMAP4rev2 }

func (failList) List(*imapserver.ListWriter, string, []string, *imap.ListOptions) error {
	return &imap.Error{Type: imap.StatusResponseTypeNo, Text: "LIST is broken"}
}

func TestProbeListFailureIsMarked(t *testing.T) {
	mem := imapmemserver.New()
	u := imapmemserver.NewUser("u", "pw")
	u.Create("INBOX", nil)
	mem.AddUser(u)
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return failList{mem.NewSession().(imapserver.SessionIMAP4rev2)}, nil, nil
		},
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}}, InsecureAuth: true, Logger: nopLogger{},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)
	defer srv.Close()
	dial := func(ctx context.Context, opts *imapclient.Options) (*imapclient.Client, error) {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			return nil, err
		}
		return imapclient.New(conn, opts), nil
	}
	_, err = Probe(context.Background(), dial, auth.Password{Username: "u", Password: "pw"})
	var ae *AuthError
	if !errors.Is(err, ErrListFolders) || errors.As(err, &ae) {
		t.Fatalf("err = %v, want ErrListFolders", err)
	}
}
