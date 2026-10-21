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
	seen := map[string]int{} // index in links of each URL
	var emph []span          // byte spans of b
	pre := 0                 // depth of enclosing <pre> elements; whitespace is kept verbatim inside them
	var verbatim []span
	var linkAt, lineAt []int // byte offsets in b where each link, and its line, start
	line := 0                // byte offset in b where the current line starts
	newline := func() {
		b.WriteByte('\n')
		line = b.Len()
	}
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skipAtoms[n.DataAtom] || hidden(n) {
				return
			}
			if n.DataAtom == atom.Br {
				newline()
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
				newline()
			}
			if n.DataAtom == atom.Td || n.DataAtom == atom.Th {
				b.WriteByte(' ')
			}
			if n.DataAtom == atom.A {
				start, startLine := b.Len(), line
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				href := strings.TrimSpace(attr(n, "href"))
				if !isHTTP(href) {
					return
				}
				text := collapseSpaces(b.String()[start:])
				if text == "" {
					text = imgAlt(n)
				}
				// The first link to a URL stands (as in mergeLinks); a later one only fills an empty text.
				if i, ok := seen[href]; ok {
					if links[i].Text == "" {
						links[i].Text = text
					}
				} else if len(links) < maxLinks {
					seen[href] = len(links)
					links = append(links, Link{URL: href, Text: text, Button: isButton(n)})
					linkAt, lineAt = append(linkAt, start), append(lineAt, startLine)
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
			if i := strings.LastIndexByte(s, '\n'); i >= 0 {
				line = b.Len() - len(s) + i + 1
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockAtoms[n.DataAtom] {
			newline()
		}
	}
	walk(doc)
	text, pos := normalizeLinesMap(b.String(), verbatim)
	for i := range links {
		links[i].Before = textBefore(text, linkOffset(text, pos, lineAt[i], linkAt[i]))
	}
	return text, links, runeSpans(text, emph, pos)
}

// linkOffset maps the start of a link, at byte start of a line starting at byte line of
// the text before normalisation, to text (pos as returned by normalizeLinesMap). Usually
// that is pos[start]; but when nothing after start is kept on its line (an empty link
// ending a paragraph), pos[start] is on the next line, so the end of the link's own
// line is taken instead — unless nothing before start is kept on it either.
func linkOffset(text string, pos []int32, line, start int) int {
	o := int(pos[start])
	if int(pos[line]) < o {
		// Kept text before the link on its line: o is either inside that line (no
		// newline before it) or past its end and any blank line.
		return len(strings.TrimRight(text[:o], "\n"))
	}
	return o
}

const (
	beforeRunes = 120
	// beforeWindow is how many bytes before a link textBefore looks at.
	beforeWindow = 1 << 10
)

// textBefore returns the text before byte offset off of text as "previous line\nsame
// line": the same line runs from the line start to off, the previous line is the nearest
// non-blank line above it. Each is space-collapsed and cut to its last 120 runes. Only
// the beforeWindow bytes before off are read, starting on a rune boundary.
func textBefore(text string, off int) string {
	lo := max(0, off-beforeWindow)
	for lo < off && !utf8.RuneStart(text[lo]) {
		lo++
	}
	head := text[lo:off]
	i := strings.LastIndexByte(head, '\n')
	cur, prev := head[i+1:], ""
	for rest := head[:max(i, 0)]; i >= 0; {
		i = strings.LastIndexByte(rest, '\n')
		if line := rest[i+1:]; strings.TrimSpace(line) != "" {
			prev = line
			break
		}
		rest = rest[:max(i, 0)]
	}
	return lastRunes(collapseSpaces(prev), beforeRunes) + "\n" + lastRunes(collapseSpaces(cur), beforeRunes)
}

func lastRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[len(r)-n:])
	}
	return s
}

var (
	buttonClassRe = regexp.MustCompile(`(?i)\b(btn|button)`)
	backgroundRe  = regexp.MustCompile(`(?:^|;)\s*background(?:-color)?\s*:\s*([^;]+)`)
	paddingRe     = regexp.MustCompile(`(?:^|;)\s*padding(?:-[a-z]+)?\s*:`)
)

// hasBackground reports whether n has a visible background colour (style or bgcolor);
// white counts as none, as e-mail layouts put it on every cell.
func hasBackground(n *html.Node) bool {
	v := strings.ToLower(strings.TrimSpace(attr(n, "bgcolor")))
	if m := backgroundRe.FindStringSubmatch(strings.ToLower(attr(n, "style"))); m != nil {
		v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), "!important"))
	}
	switch v {
	case "", "none", "transparent", "inherit", "initial", "#fff", "#ffffff", "white":
		return false
	}
	return true
}

func buttonish(n *html.Node, self bool) bool {
	if buttonClassRe.MatchString(attr(n, "class")) || buttonClassRe.MatchString(attr(n, "role")) || hasBackground(n) {
		return true
	}
	return self && paddingRe.MatchString(strings.ToLower(attr(n, "style")))
}

// onlyElementChild returns n's single element child when n has no other element and
// no non-blank text, else nil.
func onlyElementChild(n *html.Node) *html.Node {
	var only *html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.ElementNode:
			if only != nil {
				return nil
			}
			only = c
		case c.Type == html.TextNode && strings.TrimSpace(c.Data) != "":
			return nil
		}
	}
	return only
}

// isButton reports whether the link a is styled as a button: a itself has a button
// class or role, a background or padding; or its only child, or a parent whose only
// child it is (mail layouts colour the surrounding <td>), has a button class or role
// or a background.
func isButton(a *html.Node) bool {
	if buttonish(a, true) {
		return true
	}
	if c := onlyElementChild(a); c != nil && buttonish(c, false) {
		return true
	}
	p := a.Parent
	return p != nil && p.Type == html.ElementNode && onlyElementChild(p) == a && buttonish(p, false)
}

// imgAlt returns the first non-blank alt text of an image inside n.
func imgAlt(n *html.Node) string {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || hidden(c) {
			continue
		}
		if c.DataAtom == atom.Img {
			if a := collapseSpaces(attr(c, "alt")); a != "" {
				return a
			}
			continue
		}
		if a := imgAlt(c); a != "" {
			return a
		}
	}
	return ""
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
	return normalize(s, verbatim, nil)
}

// normalizeLinesMap is normalizeLines that also maps positions: for every byte offset i
// of s (0..len(s)), pos[i] is the offset in the result of the first byte kept from s[i:],
// or len(result) when nothing after i is kept. Offsets are int32 to halve the map
// (bodies are capped at maxPartBytes).
func normalizeLinesMap(s string, verbatim []span) (string, []int32) {
	pos := make([]int32, len(s)+1)
	return normalize(s, verbatim, pos), pos
}

// lineSpaces are the white space characters collapsed inside a line. All are single
// bytes, and UTF-8 never uses bytes below 0x80 inside a multi-byte rune, so lines can
// be scanned byte by byte.
const lineSpaces = " \t\r\f\v"

// normalize implements normalizeLines, filling pos as normalizeLinesMap describes when
// it is not nil. It streams: kept bytes go straight to the result, so the cost is the
// result plus pos, whatever the line lengths.
func normalize(s string, verbatim []span, pos []int32) string {
	for i := range pos {
		pos[i] = -1
	}
	var b strings.Builder
	b.Grow(len(s))
	lines := 0
	blank := true
	for start := 0; start <= len(s); {
		end := strings.IndexByte(s[start:], '\n')
		if end < 0 {
			end = len(s)
		} else {
			end += start
		}
		l := s[start:end]
		for len(verbatim) > 0 && verbatim[0].end <= start {
			verbatim = verbatim[1:]
		}
		// The kept part of the line is l[ts:te]: a <pre> line keeps its indent, other
		// lines are trimmed of all Unicode white space (as strings.TrimSpace).
		var ts, te int
		keep := len(verbatim) > 0 && verbatim[0].start < end
		if keep {
			te = len(strings.TrimRight(l, lineSpaces))
		} else {
			ts = len(l) - len(strings.TrimLeftFunc(l, unicode.IsSpace))
			te = ts + len(strings.TrimRightFunc(l[ts:], unicode.IsSpace))
		}
		start = end + 1
		if ts == te {
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
		blank = false
		run := false
		for i := ts; i < te; i++ {
			c := l[i]
			if !keep && strings.IndexByte(lineSpaces, c) >= 0 {
				if !run {
					if pos != nil {
						pos[end-len(l)+i] = int32(b.Len())
					}
					b.WriteByte(' ')
				}
				run = true
				continue
			}
			run = false
			if pos != nil {
				pos[end-len(l)+i] = int32(b.Len())
			}
			b.WriteByte(c)
		}
	}
	// Lines are already trimmed and never start blank; TrimSpace would eat a <pre> indent.
	text := strings.TrimRight(b.String(), "\n")
	next := int32(len(text))
	for i := len(pos) - 1; i >= 0; i-- {
		if pos[i] >= 0 && pos[i] < int32(len(text)) {
			next = pos[i]
		}
		pos[i] = next
	}
	return text
}

// runeSpans maps byte spans of the text before normalisation to rune spans of text
// (pos as returned by normalizeLinesMap), trimmed of white space; empty ones are dropped.
func runeSpans(text string, raw []span, pos []int32) []Span {
	var bs []span
	for _, sp := range raw {
		s, e := int(pos[sp.start]), int(pos[sp.end])
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

// bareLinks returns the distinct bare http(s) URLs of text, at most maxLinks, each with
// the text before its first occurrence.
func bareLinks(text string) []Link {
	var out []Link
	seen := map[string]bool{}
	for i := 0; len(out) < maxLinks; {
		m := urlRe.FindStringIndex(text[i:])
		if m == nil {
			break
		}
		start, end := i+m[0], i+m[1]
		i = end
		if u := strings.TrimRight(text[start:end], ".,;:!?"); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, Link{URL: u, Before: textBefore(text, start)})
		}
	}
	return out
}
