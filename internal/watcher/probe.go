package watcher

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message/charset"

	"verify-code-relay/internal/auth"
)

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
		return nil, fmt.Errorf("列出資料夾：%w", err)
	}
	c.Logout().Wait() // best effort: the folders are already known

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
