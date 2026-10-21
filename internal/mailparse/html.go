package mailparse

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var blockAtoms = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Tr: true, atom.Li: true, atom.Table: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Ul: true, atom.Ol: true, atom.Blockquote: true, atom.Section: true, atom.Article: true,
	atom.Header: true, atom.Footer: true, atom.Hr: true, atom.Center: true, atom.Pre: true,
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

// emphAtoms are elements whose text stands out; codes are often set in bold or as a heading.
var emphAtoms = map[atom.Atom]bool{
	atom.B: true, atom.Strong: true, atom.Code: true, atom.Kbd: true, atom.Samp: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true,
}

var (
	fontSizeRe      = regexp.MustCompile(`(?:^|;)\s*font-size\s*:\s*([0-9]*\.?[0-9]+)\s*(px|pt|em|rem|%)`)
	letterSpacingRe = regexp.MustCompile(`(?:^|;)\s*letter-spacing\s*:\s*(-?[0-9]*\.?[0-9]+)`)
	// fontSizeMin is the smallest font size, per unit, that counts as large.
	fontSizeMin = map[string]float64{"px": 20, "pt": 15, "em": 1.25, "rem": 1.25, "%": 125}
)

// emphasized reports whether n sets its text apart: a bold, code or heading element,
// a large font or a non-zero letter-spacing.
func emphasized(n *html.Node) bool {
	if emphAtoms[n.DataAtom] {
		return true
	}
	style := strings.ToLower(attr(n, "style"))
	if m := fontSizeRe.FindStringSubmatch(style); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v >= fontSizeMin[m[2]] {
			return true
		}
	}
	if m := letterSpacingRe.FindStringSubmatch(style); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v != 0 {
			return true
		}
	}
	return false
}

// HTMLToText renders HTML to plain text (block elements become newlines) and
// returns the http(s) <a href> links with their visible text.
func HTMLToText(src string) (string, []Link) {
	text, links, _ := renderHTML(src)
	return text, links
}

// renderHTML is HTMLToText that also returns the emphasised parts of the text.
func renderHTML(src string) (string, []Link, []Span) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", nil, nil
	}
	var b strings.Builder
	var links []Link
	var emph []span // byte spans of b
	pre := 0        // depth of enclosing <pre> elements; whitespace is kept verbatim inside them
	var verbatim []span
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
			if emphasized(n) {
				start := b.Len()
				defer func() { emph = append(emph, span{start, b.Len()}) }()
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
			} else {
				verbatim = append(verbatim, span{b.Len(), b.Len() + len(s)})
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
	text, pos := normalizeLinesMap(b.String(), verbatim)
	return text, links, runeSpans(text, emph, pos)
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
	return normalizeLines(strings.ReplaceAll(s, "\r\n", "\n"), nil)
}

// span is a byte range [start, end) of the text being normalized.
type span struct{ start, end int }

// normalizeLines works like normalizeText, except that lines overlapping one of
// the ascending verbatim spans (<pre> content) keep their inner and leading
// whitespace; only trailing whitespace is dropped.
func normalizeLines(s string, verbatim []span) string {
	text, _ := normalizeLinesMap(s, verbatim)
	return text
}

// normalizeLinesMap is normalizeLines that also maps positions: for every byte offset i
// of s (0..len(s)), pos[i] is the offset in the result of the first byte kept from s[i:],
// or len(result) when nothing after i is kept.
func normalizeLinesMap(s string, verbatim []span) (string, []int) {
	type piece struct {
		src, n int  // the bytes s[src:src+n]
		space  bool // a run of white space collapsed to one ' '
	}
	pos := make([]int, len(s)+1)
	for i := range pos {
		pos[i] = -1
	}
	var b strings.Builder
	lines := 0
	blank := true
	off := 0
	for _, l := range strings.Split(s, "\n") {
		start := off
		off += len(l) + 1
		for len(verbatim) > 0 && verbatim[0].end <= start {
			verbatim = verbatim[1:]
		}
		keep := len(verbatim) > 0 && verbatim[0].start < start+len(l) // a <pre> line
		var ps []piece
		run := false
		for i, r := range l {
			n := utf8.RuneLen(r)
			if r == utf8.RuneError {
				_, n = utf8.DecodeRuneInString(l[i:]) // 1 for an invalid byte
			}
			if !keep && strings.ContainsRune(" \t\r\f\v", r) {
				if !run {
					ps = append(ps, piece{start + i, n, true})
				}
				run = true
				continue
			}
			run = false
			ps = append(ps, piece{start + i, n, false})
		}
		isSpace := func(p piece) bool {
			if p.space {
				return true
			}
			r, _ := utf8.DecodeRuneInString(s[p.src:])
			if keep {
				return strings.ContainsRune(" \t\r\f\v", r)
			}
			return unicode.IsSpace(r)
		}
		if !keep {
			for len(ps) > 0 && isSpace(ps[0]) {
				ps = ps[1:]
			}
		}
		for len(ps) > 0 && isSpace(ps[len(ps)-1]) {
			ps = ps[:len(ps)-1]
		}
		if len(ps) == 0 {
			if !blank {
				if lines > 0 {
					b.WriteByte('\n')
				}
				lines++
			}
			blank = true
			continue
		}
		if lines > 0 {
			b.WriteByte('\n')
		}
		lines++
		for _, p := range ps {
			pos[p.src] = b.Len()
			if p.space {
				b.WriteByte(' ')
			} else {
				b.WriteString(s[p.src : p.src+p.n])
			}
		}
		blank = false
	}
	// Lines are already trimmed and never start blank; TrimSpace would eat a <pre> indent.
	text := strings.TrimRight(b.String(), "\n")
	next := len(text)
	for i := len(s); i >= 0; i-- {
		if pos[i] >= 0 && pos[i] < len(text) {
			next = pos[i]
		}
		pos[i] = next
	}
	return text, pos
}

// runeSpans maps byte spans of the text before normalisation to rune spans of text
// (pos as returned by normalizeLinesMap), trimmed of white space; empty ones are dropped.
func runeSpans(text string, raw []span, pos []int) []Span {
	var bs []span
	for _, sp := range raw {
		s, e := pos[sp.start], pos[sp.end]
		for s < e && (text[s] == ' ' || text[s] == '\n') {
			s++
		}
		for e > s && (text[e-1] == ' ' || text[e-1] == '\n') {
			e--
		}
		if s < e {
			bs = append(bs, span{s, e})
		}
	}
	if len(bs) == 0 {
		return nil
	}
	offs := make([]int, 0, 2*len(bs))
	for _, sp := range bs {
		offs = append(offs, sp.start, sp.end)
	}
	slices.Sort(offs)
	runeAt := make(map[int]int, len(offs))
	r, last := 0, 0
	for _, o := range offs {
		r += utf8.RuneCountInString(text[last:o])
		last = o
		runeAt[o] = r
	}
	out := make([]Span, len(bs))
	for i, sp := range bs {
		out[i] = Span{runeAt[sp.start], runeAt[sp.end]}
	}
	return out
}

// urlRe stops at full-width punctuation and brackets: CJK text puts them right
// after a URL without a space, and a real URL would percent-encode them.
// \s is ASCII-only in RE2, so the ideographic space U+3000 is listed too.
var urlRe = regexp.MustCompile(`(?i)https?://[^\s<>"'()\[\]{}（）「」『』【】《》〈〉，。、：；！？\x{3000}]+`)

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
