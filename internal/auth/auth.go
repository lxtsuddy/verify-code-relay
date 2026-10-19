// Package auth authenticates IMAP connections.
package auth

import (
	"context"
	"errors"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"
)

type Authenticator interface {
	Authenticate(ctx context.Context, c *imapclient.Client) error
}

var ErrNeedsLogin = errors.New("需要重新登入")

type Password struct {
	Username string
	Password string
}

func (p Password) Authenticate(_ context.Context, c *imapclient.Client) error {
	return c.Login(p.Username, p.Password).Wait()
}

type xoauth2 struct{ username, token string }

// NewXOAUTH2 returns a SASL XOAUTH2 client (go-sasl does not ship one).
func NewXOAUTH2(username, token string) sasl.Client { return &xoauth2{username, token} }

func (x *xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.username + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

// Next answers the server's JSON error challenge with an empty response so the
// server completes the exchange with a tagged NO.
func (x *xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }
