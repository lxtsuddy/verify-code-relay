package watcher

import (
	"context"
	"crypto/tls"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

// A server that accepts TCP but never answers the TLS handshake must not hold
// up shutdown: TLSDialer has to give up as soon as ctx is cancelled.
func TestTLSDialerHonoursContext(t *testing.T) {
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
			defer c.Close() // hold the connection open, say nothing
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		c, err := TLSDialer(ln.Addr().String())(ctx, &imapclient.Options{})
		if c != nil {
			c.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want error from a silent server")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("TLSDialer ignored context cancellation")
	}
}

// TLSDialer must keep what imapclient.DialTLS used to set up: SNI from the
// address and the "imap" ALPN protocol.
func TestTLSDialerClientHello(t *testing.T) {
	hellos := make(chan *tls.ClientHelloInfo, 1)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			hellos <- h
			return nil, net.ErrClosed // abort the handshake; only the hello matters
		},
	})
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
			go func() { c.(*tls.Conn).Handshake(); c.Close() }()
		}
	}()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if c, err := TLSDialer(net.JoinHostPort("localhost", port))(ctx, &imapclient.Options{}); err == nil {
		c.Close()
		t.Fatal("handshake should have been aborted")
	}
	select {
	case h := <-hellos:
		if h.ServerName != "localhost" {
			t.Errorf("SNI = %q, want localhost", h.ServerName)
		}
		if !slices.Contains(h.SupportedProtos, "imap") {
			t.Errorf("ALPN = %v, want imap", h.SupportedProtos)
		}
		if !slices.Contains(h.SupportedVersions, tls.VersionTLS13) || slices.Contains(h.SupportedVersions, tls.VersionTLS11) {
			t.Errorf("versions = %x, want TLS 1.2+ only", h.SupportedVersions)
		}
	default:
		t.Fatal("no ClientHello received")
	}
}
