// Package mailparse turns raw RFC 5322 messages into a normalized Message.
package mailparse

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset" // registers Big5, GB2312, GBK, GB18030, Shift_JIS, ISO-2022-JP, ...
	"github.com/emersion/go-message/mail"
)

type Address struct {
	Name string
	Addr string
}

type Link struct {
	URL  string
	Text string
}

type Message struct {
	From    Address
	To      []Address
	Subject string
	Date    time.Time
	Text    string
	Links   []Link
	// Warnings lists non-fatal problems met while reading the body (unreadable
	// or truncated parts). They carry the part index, content type and decoder
	// error but no body text; a malformed-header error may quote that header line.
	Warnings []string
}

const maxPartBytes = 5 << 20

func Parse(raw []byte) (*Message, error) {
	r, err := mail.CreateReader(bytes.NewReader(raw))
	if r == nil {
		// Unknown transfer encodings and malformed headers yield no reader at all.
		return nil, err
	}
	if err != nil && !message.IsUnknownCharset(err) {
		return nil, err
	}
	m := &Message{}
	h := r.Header
	if s, err := h.Subject(); err == nil {
		m.Subject = s
	} else {
		m.Subject = h.Get("Subject")
	}
	if d, err := h.Date(); err == nil {
		m.Date = d
	}
	if from, err := h.AddressList("From"); err == nil && len(from) > 0 {
		m.From = Address{Name: from[0].Name, Addr: from[0].Address}
	}
	seen := map[string]bool{}
	for _, key := range []string{"To", "Cc", "Delivered-To"} {
		// Repeated headers (forwarding, mailing lists) each carry addresses; read every field.
		fields := h.FieldsByKey(key)
		for fields.Next() {
			list, err := mail.ParseAddressList(fields.Value())
			if err != nil {
				continue
			}
			for _, a := range list {
				k := strings.ToLower(a.Address)
				if a.Address == "" || seen[k] {
					continue
				}
				seen[k] = true
				m.To = append(m.To, Address{Name: a.Name, Addr: a.Address})
			}
		}
	}

	var plain, htmlBody string
	var havePlain, haveHTML bool
	var readErr error // first error that cost us body content
	for n := 1; ; n++ {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) && !message.IsUnknownEncoding(err) {
			// Keep what we have; a broken trailing part must not lose the message.
			m.Warnings = append(m.Warnings, fmt.Sprintf("第 %d 個 part 無法讀取，略過其後內容：%v", n, err))
			readErr = cmp.Or(readErr, err)
			break
		}
		if p == nil {
			continue
		}
		ih, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue // attachment
		}
		if disp, _, _ := ih.ContentDisposition(); disp == "attachment" {
			continue
		}
		ct, _, _ := ih.ContentType()
		if ct == "" {
			ct = "text/plain"
		}
		if ct != "text/plain" && ct != "text/html" {
			continue
		}
		// Read one byte past the limit to tell a part of exactly maxPartBytes from a longer one.
		body, err := io.ReadAll(io.LimitReader(p.Body, maxPartBytes+1))
		if err != nil {
			// Keep the decoded prefix: it often still holds the code.
			m.Warnings = append(m.Warnings, fmt.Sprintf("第 %d 個 part（%s）讀取失敗，只使用前 %d 位元組：%v", n, ct, len(body), err))
			readErr = cmp.Or(readErr, err)
		}
		if len(body) > maxPartBytes {
			body = trimPartialRune(body[:maxPartBytes])
			m.Warnings = append(m.Warnings, fmt.Sprintf("第 %d 個 part（%s）超過 %d 位元組，已截斷", n, ct, maxPartBytes))
		}
		if len(body) == 0 && err != nil {
			continue // nothing decoded; let a later part of the same type stand in
		}
		switch {
		case ct == "text/plain" && !havePlain:
			plain, havePlain = string(body), true
		case ct == "text/html" && !haveHTML:
			htmlBody, haveHTML = string(body), true
		}
	}

	var htmlText string
	var htmlLinks []Link
	if haveHTML {
		htmlText, htmlLinks = HTMLToText(htmlBody)
	}
	if havePlain {
		m.Text = normalizeText(plain)
	}
	if m.Text == "" {
		// An empty text/plain part must not hide a populated HTML alternative.
		m.Text = htmlText
	}
	m.Links = mergeLinks(htmlLinks, ExtractURLs(m.Text))
	if readErr != nil && m.Subject == "" && m.Text == "" && len(m.Links) == 0 {
		// Nothing left to recognize: surface the failure instead of an empty message.
		return nil, fmt.Errorf("讀取信件內文：%w", readErr)
	}
	return m, nil
}

// trimPartialRune drops an incomplete UTF-8 sequence left at the end of b by a byte-count cut.
func trimPartialRune(b []byte) []byte {
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		if utf8.RuneStart(b[len(b)-i]) {
			if !utf8.FullRune(b[len(b)-i:]) {
				return b[:len(b)-i]
			}
			break
		}
	}
	return b
}

// mergeLinks de-duplicates by URL, preferring entries that have visible text.
func mergeLinks(htmlLinks []Link, bare []string) []Link {
	idx := map[string]int{}
	var out []Link
	add := func(l Link) {
		if i, ok := idx[l.URL]; ok {
			if out[i].Text == "" && l.Text != "" {
				out[i].Text = l.Text
			}
			return
		}
		idx[l.URL] = len(out)
		out = append(out, l)
	}
	for _, l := range htmlLinks {
		add(l)
	}
	for _, u := range bare {
		add(Link{URL: u})
	}
	return out
}
