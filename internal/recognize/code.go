package recognize

import (
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"verify-code-relay/internal/mailparse"
)

var defaultCodeKeywords = []string{
	"驗證碼", "验证码", "校驗碼", "校验码", "動態碼", "动态码", "確認碼", "确认码", "安全碼", "安全码",
	"認證碼", "认证码", "一次性密碼", "一次性密码", "登入碼", "登录码",
	"安全性代碼", "驗證代碼", "验证代码", "動態密碼", "动态密码",
	"verification code", "security code", "confirmation code", "authentication code", "login code",
	"sign-in code", "one-time code", "one-time password", "otp", "passcode", "2fa", "two-factor",
	"your code", "code is", "this code", "the code", "use this code", "enter this code", "code below",
	"following code", "temporary code", "pin code", "pin",
	"認証コード", "確認コード", "セキュリティコード", "ワンタイムパスワード",
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
	// orderWords are matched like keywords: "order" does not match "border" or
	// "reorder", while CJK words and "#" match anywhere.
	orderWords = toRuneKeywords([]string{"訂單", "订单", "order", "invoice", "#"})
)

var (
	// cardWords and zipWords are matched in the lower-cased text just before a candidate,
	// white space collapsed to one space: card numbers ("ending in 4821") and postcodes
	// are not codes.
	cardWords = []string{"末四碼", "末4碼", "末四码", "末4码", "尾號", "尾号", "ending in", "ends in", "last 4", "last4", "last four"}
	zipWords  = []string{"郵遞區號", "郵政編碼", "邮编", "邮政编码", "zip", "postal", "postcode"}
	// streetWords after a number make it a house number ("1600 Amphitheatre Parkway").
	// Product names ("G Suite") rule out words like suite or floor.
	streetWords = map[string]bool{"st": true, "street": true, "ave": true, "avenue": true, "rd": true, "road": true,
		"blvd": true, "boulevard": true, "parkway": true, "pkwy": true}
	usStates = map[string]bool{}
)

func init() {
	for _, s := range strings.Fields("AL AK AZ AR CA CO CT DE DC FL GA HI ID IL IN IA KS KY LA ME MD MA MI MN MS MO MT NE NV NH NJ NM NY NC ND OH OK OR PA RI SC SD TN TX UT VT VA WA WV WI WY") {
		usStates[s] = true
	}
}

const (
	// contextRunes is how far before a candidate cardWords and zipWords are looked for;
	// it covers "last 4 digits 4821".
	contextRunes = 16
	// streetScanRunes is how far after a candidate, on its line, a street word is looked for.
	streetScanRunes = 30
)

// hasContextWord reports whether s contains one of words; a word starting with an ASCII
// letter must not follow another ASCII letter ("zip" is not in "unzip").
func hasContextWord(s string, words []string) bool {
	for _, w := range words {
		for i := 0; ; {
			j := strings.Index(s[i:], w)
			if j < 0 {
				break
			}
			j += i
			if !isASCIILetter(rune(w[0])) || j == 0 || !isASCIILetter(rune(s[j-1])) {
				return true
			}
			i = j + 1
		}
	}
	return false
}

// addressReason returns "地址" when c looks like part of a postal address: a house
// number (a street word later on its line, or 號/路/街/樓 right after it) or a US ZIP
// code ("CA 94043").
func addressReason(c codeCand, t *codeText) string {
	rs, lower := t.rs, t.lower
	j := c.end
	if j < len(rs) && rs[j] == ' ' {
		j++
	}
	if j < len(rs) && strings.ContainsRune("號号路街樓楼", rs[j]) {
		return "地址"
	}
	end := min(len(rs), c.end+streetScanRunes)
	var word []rune
	for i := c.end; i <= end; i++ {
		if i < end && rs[i] != '\n' && (isASCIILetter(lower[i]) || isDigit(lower[i])) {
			word = append(word, lower[i])
			continue
		}
		if streetWords[string(word)] {
			return "地址"
		}
		word = word[:0]
		if i < end && rs[i] == '\n' {
			break
		}
	}
	if len(c.value) == 5 && isAllDigits(c.value) && c.start >= 3 && rs[c.start-1] == ' ' &&
		usStates[string(rs[c.start-3:c.start-1])] && (c.start == 3 || !unicode.IsLetter(rs[c.start-4])) {
		return "地址"
	}
	return ""
}

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

// subjectOrderWords in a subject mean its numbers are references, not codes.
var subjectOrderWords = toRuneKeywords([]string{"訂單", "订单", "order", "orders", "invoice", "invoices",
	"receipt", "receipts", "tracking", "shipment", "shipments", "包裹", "物流", "出貨", "出货", "發票", "发票"})

// addKeyword adds k to the sorted keyword list.
func (t *codeText) addKeyword(k span) {
	i := sort.Search(len(t.kws), func(i int) bool { return t.kws[i].start >= k.start })
	t.kws = slices.Insert(t.kws, i, k)
	t.maxKwLen = max(t.maxKwLen, k.end-k.start)
}

// subjectEcho returns the subject's numbers that also stand alone on a body line, such
// as "Use 123456 to set up …" over a body line "123456": the number in the subject is
// then treated as a keyword. digits are the digit runs of the scanned text, by start.
// Years, excluded numbers and subjects naming an order or shipment never qualify.
func subjectEcho(t *codeText, digits []span, subjLen int) []span {
	subjLen = min(subjLen, len(t.rs))
	for i := 0; i < subjLen; i++ {
		for _, w := range subjectOrderWords {
			if i+len(w) <= subjLen && keywordAt(t.lower[:subjLen], w, i) {
				return nil
			}
		}
	}
	var out []span
	for _, s := range digits {
		if s.start >= subjLen {
			break
		}
		v := string(t.rs[s.start:s.end])
		if y, _ := strconv.Atoi(v); len(v) == 4 && y >= 1900 && y <= 2099 {
			continue
		}
		if excludeReason(codeCand{s, v, v}, t) != "" {
			continue
		}
		for _, b := range digits {
			if b.start > subjLen && string(t.rs[b.start:b.end]) == v && t.aloneOnLine(b) && excludeReason(codeCand{b, v, v}, t) == "" {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

func (f *codeFinder) find(msg *mailparse.Message, tr *Trace) string {
	rs := scanRunes(msg)
	full := string(rs)
	lower := lowerRunes(rs)
	subjLen := len([]rune(msg.Subject))
	b2r := byteToRune(full)
	blocked := append(regexpSpans(urlLikeRe, full, b2r), regexpSpans(emailRe, full, b2r)...)
	kws := findKeywords(lower, f.keywords)
	t := newCodeText(rs, lower, kws, blocked)
	digits := regexpSpans(digitsRe, full, b2r)
	for _, s := range subjectEcho(t, digits, subjLen) {
		tr.Addf("驗證碼：主旨的 %s 也獨佔內文一行，視為關鍵字", string(rs[s.start:s.end]))
		t.addKeyword(s)
	}
	if len(t.kws) == 0 {
		tr.Addf("驗證碼：沒有找到驗證碼關鍵字")
		return ""
	}
	for _, k := range kws {
		tr.Addf("驗證碼：關鍵字 %q 位於 %d", string(rs[k.start:k.end]), k.start)
	}

	var cands []codeCand
	for _, sp := range digits {
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

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// phoneSeps are the separators allowed between two digit groups of a phone
// number. Anything longer, such as " - " or a double space, ends the run.
var phoneSeps = map[string]bool{" ": true, "-": true, "(": true, ")": true, ") ": true, " (": true, ")-": true, "-(": true}

// phoneRun returns the extent [l, r) of the phone-like run around c: digit groups
// joined by phoneSeps, plus a leading '(' (as in "(02) 2345 6789"). Each side is
// followed for at most phoneScanLimit runes.
func phoneRun(rs []rune, c span) (l, r int) {
	l, r = c.start, c.end
	for r-c.end < phoneScanLimit {
		j := r
		for j < len(rs) && j-r < 2 && strings.ContainsRune(" -()", rs[j]) {
			j++
		}
		if j == r || j >= len(rs) || !isDigit(rs[j]) || !phoneSeps[string(rs[r:j])] {
			break
		}
		for r = j; r < len(rs) && isDigit(rs[r]); r++ {
		}
	}
	for c.start-l < phoneScanLimit {
		j := l
		for j > 0 && l-j < 2 && strings.ContainsRune(" -()", rs[j-1]) {
			j--
		}
		if j == l || j == 0 || !isDigit(rs[j-1]) || !phoneSeps[string(rs[j:l])] {
			break
		}
		for l = j; l > 0 && isDigit(rs[l-1]); l-- {
		}
	}
	if l > 0 && rs[l-1] == '(' {
		l--
	}
	return l, r
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
	l, r := phoneRun(rs, c.span)
	if l > 0 && rs[l-1] == '+' {
		return "電話號碼"
	}
	run := string(rs[l:r])
	digits := 0
	for _, ch := range run {
		if isDigit(ch) {
			digits++
		}
	}
	if digits > 8 && strings.ContainsAny(run, "-(") {
		return "電話號碼"
	}
	for _, w := range orderWords {
		for i := max(0, c.start-10); i+len(w) <= c.start; i++ {
			if keywordAt(lower, w, i) {
				return "訂單編號"
			}
		}
	}
	// Spaces are collapsed, not removed, so that hasContextWord can still see word boundaries.
	ctx := strings.Join(strings.Fields(string(lower[max(0, c.start-contextRunes):c.start])), " ")
	if hasContextWord(ctx, cardWords) {
		return "卡號末碼"
	}
	if hasContextWord(ctx, zipWords) {
		return "郵遞區號"
	}
	return addressReason(c, t)
}
