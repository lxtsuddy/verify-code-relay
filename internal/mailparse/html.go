package mailparse

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var blockAtoms = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Tr: true, atom.Li: true, atom.Table: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Ul: true, atom.Ol: true, atom.Blockquote: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true, atom.Hr: true, atom.Center: true,
}

var skipAtoms = map[atom.Atom]bool{
	atom.Head: true, atom.Style: true, atom.Script: true, atom.Img: true, atom.Noscript: true, atom.Template: true,
}

func hidden(n *html.Node) bool {
	for _, a := range n.Attr {
		if a.Key == "style" {
			s := strings.ToLower(strings.ReplaceAll(a.Val, " ", ""))
			if strings.Contains(s, "display:none") {
				return true
			}
		}
		if a.Key == "hidden" {
			return true
		}
	}
	return false
}

// HTMLToText renders HTML to plain text (block elements become newlines) and
// returns the http(s) <a href> links with their visible text.
func HTMLToText(src string) (string, []Link) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", nil
	}
	var b strings.Builder
	var links []Link
	pre := 0 // depth of enclosing <pre> elements; whitespace is kept verbatim inside them
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skipAtoms[n.DataAtom] || hidden(n) {
				return
			}
			if n.DataAtom == atom.Br {
				b.WriteByte('\n')
				return
			}
			if n.DataAtom == atom.Pre {
				pre++
				defer func() { pre-- }()
			}
			if blockAtoms[n.DataAtom] {
				b.WriteByte('\n')
			}
			if n.DataAtom == atom.Td || n.DataAtom == atom.Th {
				b.WriteByte(' ')
			}
			if n.DataAtom == atom.A {
				start := b.Len()
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				href := strings.TrimSpace(attr(n, "href"))
				if isHTTP(href) {
					links = append(links, Link{URL: href, Text: collapseSpaces(b.String()[start:])})
				}
				return
			}
		}
		if n.Type == html.TextNode {
			s := strings.ReplaceAll(n.Data, "\u00a0", " ")
			if pre == 0 {
				s = htmlSpaceRun.ReplaceAllString(s, " ")
			}
			b.WriteString(s)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockAtoms[n.DataAtom] {
			b.WriteByte('\n')
		}
	}
	walk(doc)
	return normalizeText(b.String()), links
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func isHTTP(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

var spaceRun = regexp.MustCompile(`[ \t\r\f\v]+`)

// htmlSpaceRun collapses source whitespace (including newlines) inside HTML text nodes.
var htmlSpaceRun = regexp.MustCompile(`[ \t\r\n\f\v]+`)

func collapseSpaces(s string) string {
	return strings.TrimSpace(spaceRun.ReplaceAllString(strings.ReplaceAll(s, "\n", " "), " "))
}

// normalizeText collapses spaces inside lines, trims lines and keeps at most one blank line in a row.
func normalizeText(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := true
	for _, l := range lines {
		l = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
		if l == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, l)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

var urlRe = regexp.MustCompile(`(?i)https?://[^\s<>"'()\[\]{}（）「」，。、]+`)

// ExtractURLs finds bare http(s) URLs in text, trimming trailing punctuation.
func ExtractURLs(text string) []string {
	var out []string
	for _, u := range urlRe.FindAllString(text, -1) {
		u = strings.TrimRight(u, ".,;:!?")
		if u != "" {
			out = append(out, u)
		}
	}
	return out
}
