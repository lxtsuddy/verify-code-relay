package recognize

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"verify-code-relay/internal/mailparse"
)

var defaultCodeKeywords = []string{
	"驗證碼", "验证码", "校驗碼", "校验码", "動態碼", "动态码", "確認碼", "确认码", "安全碼", "安全码",
	"認證碼", "认证码", "一次性密碼", "一次性密码", "登入碼", "登录码",
	"verification code", "security code", "confirmation code", "authentication code", "login code",
	"sign-in code", "one-time code", "one-time password", "otp", "passcode", "2fa", "two-factor",
	"your code", "code is",
	"認証コード", "確認コード", "ワンタイムパスワード",
}

var (
	digitsRe   = regexp.MustCompile(`\b\d{4,8}\b`)
	splitRe    = regexp.MustCompile(`\b\d{3}[ -]\d{3}\b`)
	alnumRe    = regexp.MustCompile(`\b[A-Z0-9]{4,8}\b`)
	urlLikeRe  = regexp.MustCompile(`(?i)(https?://|www\.)\S+`)
	emailRe    = regexp.MustCompile(`[\w.+-]+@[\w-]+(\.[\w-]+)+`)
	monthRe    = regexp.MustCompile(`(?i)\b(jan(uary)?|feb(ruary)?|mar(ch)?|apr(il)?|june?|july?|aug(ust)?|sep(t(ember)?)?|oct(ober)?|nov(ember)?|dec(ember)?)\b|\bmay\s+\d|\d\s+may\b`)
	dateNumRe  = regexp.MustCompile(`\d{4}[/-]\d{1,2}|\d{1,2}[/-]\d{1,2}[/-]\d{2,4}`)
	currencies = []string{"$", "¥", "￥", "€", "£", "twd", "usd", "rmb", "cny"}
	orderWords = []string{"訂單", "订单", "order", "invoice", "#"}
)

const (
	codeMaxDistance = 200
	codeThreshold   = 40.0
)

type codeFinder struct{ keywords [][]rune }

func newCodeFinder(extra []string) *codeFinder {
	return &codeFinder{keywords: toRuneKeywords(append(append([]string{}, defaultCodeKeywords...), extra...))}
}

type codeCand struct {
	span
	raw   string
	value string
}

// maxScanRunes caps how much of Subject+"\n"+Text the builtin finders look at.
// Codes sit near the top of real mail; the cap bounds work on huge messages.
const maxScanRunes = 64 << 10

// phoneScanLimit bounds how far a phone-number run is followed in each direction.
const phoneScanLimit = 32

// scanRunes returns Subject+"\n"+Text as runes, truncated to maxScanRunes.
func scanRunes(msg *mailparse.Message) []rune {
	rs := []rune(msg.Subject + "\n" + msg.Text)
	if len(rs) > maxScanRunes {
		rs = rs[:maxScanRunes]
	}
	return rs
}

// codeText holds the per-message data the candidate checks share, so that each
// candidate costs O(log n) instead of O(line) or O(keywords).
type codeText struct {
	rs, lower []rune
	lines     []span // every line, in order; end is the '\n' offset (or len)
	trimmed   []span // lines[i] with surrounding white space removed
	kws       []span // keyword occurrences sorted by start
	maxKwLen  int
	blocked   []span // URL and e-mail spans, merged and sorted
	yearLine  map[int]bool
}

func newCodeText(rs, lower []rune, kws, blocked []span) *codeText {
	t := &codeText{rs: rs, lower: lower, yearLine: map[int]bool{}}
	for s := 0; ; {
		e := s
		for e < len(rs) && rs[e] != '\n' {
			e++
		}
		ts, te := s, e
		for ts < te && unicode.IsSpace(rs[ts]) {
			ts++
		}
		for te > ts && unicode.IsSpace(rs[te-1]) {
			te--
		}
		t.lines = append(t.lines, span{s, e})
		t.trimmed = append(t.trimmed, span{ts, te})
		if e >= len(rs) {
			break
		}
		s = e + 1
	}
	t.kws = append([]span(nil), kws...)
	sort.Slice(t.kws, func(i, j int) bool { return t.kws[i].start < t.kws[j].start })
	for _, k := range t.kws {
		t.maxKwLen = max(t.maxKwLen, k.end-k.start)
	}
	t.blocked = mergeSpans(blocked)
	return t
}

// line returns the index of the line containing pos.
func (t *codeText) line(pos int) int {
	return sort.Search(len(t.lines), func(i int) bool { return t.lines[i].end >= pos })
}

// aloneOnLine reports whether the candidate is the only thing on its line.
func (t *codeText) aloneOnLine(c span) bool {
	return t.trimmed[t.line(c.start)] == c
}

// dateLine reports whether the line containing pos looks like it holds a date.
func (t *codeText) dateLine(pos int) bool {
	ln := t.lines[t.line(pos)]
	if v, ok := t.yearLine[ln.start]; ok {
		return v
	}
	line := string(t.lower[ln.start:ln.end])
	v := strings.Contains(line, "年") || monthRe.MatchString(line) || dateNumRe.MatchString(line)
	t.yearLine[ln.start] = v
	return v
}

// blockedAt reports whether c overlaps a URL or e-mail address.
func (t *codeText) blockedAt(c span) bool {
	i := sort.Search(len(t.blocked), func(i int) bool { return t.blocked[i].end > c.start })
	return i < len(t.blocked) && t.blocked[i].start < c.end
}

// mergeSpans returns the union of list as sorted, disjoint spans.
func mergeSpans(list []span) []span {
	s := append([]span(nil), list...)
	sort.Slice(s, func(i, j int) bool { return s[i].start < s[j].start })
	var out []span
	for _, b := range s {
		if n := len(out); n > 0 && b.start <= out[n-1].end {
			out[n-1].end = max(out[n-1].end, b.end)
			continue
		}
		out = append(out, b)
	}
	return out
}

func (f *codeFinder) find(msg *mailparse.Message, tr *Trace) string {
	rs := scanRunes(msg)
	full := string(rs)
	lower := lowerRunes(rs)
	subjLen := len([]rune(msg.Subject))
	kws := findKeywords(lower, f.keywords)
	if len(kws) == 0 {
		tr.Addf("驗證碼：沒有找到驗證碼關鍵字")
		return ""
	}
	for _, k := range kws {
		tr.Addf("驗證碼：關鍵字 %q 位於 %d", string(rs[k.start:k.end]), k.start)
	}
	b2r := byteToRune(full)
	blocked := append(regexpSpans(urlLikeRe, full, b2r), regexpSpans(emailRe, full, b2r)...)
	t := newCodeText(rs, lower, kws, blocked)

	var cands []codeCand
	for _, sp := range regexpSpans(digitsRe, full, b2r) {
		raw := string(rs[sp.start:sp.end])
		cands = append(cands, codeCand{sp, raw, raw})
	}
	for _, sp := range regexpSpans(splitRe, full, b2r) {
		raw := string(rs[sp.start:sp.end])
		cands = append(cands, codeCand{sp, raw, strings.NewReplacer(" ", "", "-", "").Replace(raw)})
	}
	for _, sp := range regexpSpans(alnumRe, full, b2r) {
		raw := string(rs[sp.start:sp.end])
		if strings.IndexFunc(raw, unicode.IsDigit) >= 0 && strings.IndexFunc(raw, unicode.IsUpper) >= 0 {
			cands = append(cands, codeCand{sp, raw, raw})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].start < cands[j].start })

	best, bestScore := "", -1e9
	for _, c := range cands {
		if reason := excludeReason(c, t); reason != "" {
			tr.Addf("  候選 %q：排除（%s）", c.raw, reason)
			continue
		}
		d, ok := t.keywordDistance(c.span)
		if !ok {
			tr.Addf("  候選 %q：離關鍵字超過 %d 字元", c.raw, codeMaxDistance)
			continue
		}
		score := 100 - float64(d)/2
		if c.start < subjLen {
			score += 30
		}
		if t.aloneOnLine(c.span) {
			score += 20
		}
		if len(c.value) == 6 && isAllDigits(c.value) {
			score += 10
		}
		tr.Addf("  候選 %q：分數 %.1f", c.raw, score)
		if score > bestScore {
			best, bestScore = c.value, score
		}
	}
	if best == "" || bestScore < codeThreshold {
		tr.Addf("驗證碼：沒有分數達到門檻 %.0f 的候選", codeThreshold)
		return ""
	}
	tr.Addf("驗證碼：選擇 %q（分數 %.1f）", best, bestScore)
	return best
}

// keywordDistance returns the weighted distance to the nearest keyword; candidates
// before a keyword count double. ok is false when every keyword is >200 runes away.
// Only keywords starting in [c.start-200-maxKwLen, c.end+200] can be within 200
// runes, so the scan starts at a binary-searched index and stops past that window.
func (t *codeText) keywordDistance(c span) (int, bool) {
	best, ok := 0, false
	lo := c.start - codeMaxDistance - t.maxKwLen
	i := sort.Search(len(t.kws), func(i int) bool { return t.kws[i].start >= lo })
	for ; i < len(t.kws) && t.kws[i].start <= c.end+codeMaxDistance; i++ {
		k := t.kws[i]
		var raw, d int
		switch {
		case c.start >= k.end:
			raw = c.start - k.end
			d = raw
		case c.end <= k.start:
			raw = k.start - c.end
			d = 2 * raw
		default:
			raw, d = 0, 0
		}
		if raw > codeMaxDistance {
			continue
		}
		if !ok || d < best {
			best, ok = d, true
		}
	}
	return best, ok
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func isPhoneRune(r rune) bool {
	return (r >= '0' && r <= '9') || r == ' ' || r == '-' || r == '(' || r == ')'
}

func excludeReason(c codeCand, t *codeText) string {
	rs, lower := t.rs, t.lower
	if t.blockedAt(c.span) {
		return "位於網址或 email 中"
	}
	before := strings.ReplaceAll(string(lower[max(0, c.start-5):c.start]), " ", "")
	for _, cur := range currencies {
		if strings.Contains(before, cur) {
			return "金額"
		}
	}
	after := c.end
	for after < len(rs) && rs[after] == ' ' {
		after++
	}
	if after < len(rs) && rs[after] == '元' {
		return "金額"
	}
	if c.start >= 2 && rs[c.start-1] == ':' && unicode.IsDigit(rs[c.start-2]) ||
		c.end+1 < len(rs) && rs[c.end] == ':' && unicode.IsDigit(rs[c.end+1]) {
		return "時間"
	}
	if len(c.value) == 4 && isAllDigits(c.value) {
		if y, _ := strconv.Atoi(c.value); y >= 1900 && y <= 2099 {
			if t.dateLine(c.start) {
				return "日期中的年份"
			}
		}
	}
	l, r := c.start, c.end
	for l > 0 && c.start-l < phoneScanLimit && isPhoneRune(rs[l-1]) {
		l--
	}
	for r < len(rs) && r-c.end < phoneScanLimit && isPhoneRune(rs[r]) {
		r++
	}
	if l > 0 && rs[l-1] == '+' {
		return "電話號碼"
	}
	run := string(rs[l:r])
	digits := 0
	for _, ch := range run {
		if ch >= '0' && ch <= '9' {
			digits++
		}
	}
	if digits > 8 && strings.ContainsAny(run, "-(") {
		return "電話號碼"
	}
	window := string(lower[max(0, c.start-10):c.start])
	for _, w := range orderWords {
		if strings.Contains(window, w) {
			return "訂單編號"
		}
	}
	return ""
}
