package watcher

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"

	"github.com/lxtsuddy/verify-code-relay/internal/auth"
)

// ErrListFolders marks a Probe failure after a successful login: the folder list could
// not be read.
var ErrListFolders = errors.New("列出資料夾失敗")

// probeLogoutWait bounds the wait for the server's LOGOUT reply; the folders are already
// known by then, so a slow goodbye must not hold up the answer.
var probeLogoutWait = 3 * time.Second

// Probe connects, authenticates and lists the selectable folders (no \Noselect), sorted
// with INBOX first. It logs out before returning. A rejected login is an *AuthError, as
// in a watcher session; cancelling ctx closes the connection.
func Probe(ctx context.Context, dial DialFunc, a auth.Authenticator) ([]string, error) {
	c, err := dial(ctx, &imapclient.Options{WordDecoder: &mime.WordDecoder{CharsetReader: charset.Reader}})
	if err != nil {
		return nil, fmt.Errorf("連線：%w", err)
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	if err := a.Authenticate(ctx, c); err != nil {
		var ie *imap.Error
		if errors.As(err, &ie) || errors.Is(err, auth.ErrNeedsLogin) {
			return nil, &AuthError{Err: err}
		}
		return nil, fmt.Errorf("認證：%w", err)
	}
	list, err := c.List("", "*", nil).Collect()
	if err != nil {
		return nil, fmt.Errorf("%w：%w", ErrListFolders, err)
	}
	bye := make(chan struct{})
	go func() { c.Logout().Wait(); close(bye) }() // released by c.Close at the latest
	select {
	case <-bye:
	case <-time.After(probeLogoutWait):
	case <-ctx.Done():
	}

	seen := map[string]bool{}
	var out []string
	for _, m := range list {
		if m == nil || seen[m.Mailbox] || hasAttr(m.Attrs, imap.MailboxAttrNoSelect) || hasAttr(m.Attrs, imap.MailboxAttrNonExistent) {
			continue
		}
		seen[m.Mailbox] = true
		out = append(out, m.Mailbox)
	}
	sort.Slice(out, func(i, j int) bool {
		ii, ij := isInbox(out[i]), isInbox(out[j])
		if ii != ij {
			return ii
		}
		return out[i] < out[j]
	})
	return out, nil
}

func hasAttr(attrs []imap.MailboxAttr, want imap.MailboxAttr) bool {
	for _, a := range attrs {
		if a == want {
			return true
		}
	}
	return false
}

// isInbox reports whether name is INBOX, which IMAP treats case-insensitively.
func isInbox(name string) bool { return strings.EqualFold(name, "INBOX") }
