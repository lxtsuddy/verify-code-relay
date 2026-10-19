package recognize

import (
	"regexp"
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

func (f *codeFinder) find(msg *mailparse.Message, tr *Trace) string {
	full := msg.Subject + "\n" + msg.Text
	rs := []rune(full)
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
	sortCands(cands)

	best, bestScore := "", -1e9
	for _, c := range cands {
		if reason := excludeReason(c, rs, lower, blocked); reason != "" {
			tr.Addf("  候選 %q：排除（%s）", c.raw, reason)
			continue
		}
		d, ok := keywordDistance(c.span, kws)
		if !ok {
			tr.Addf("  候選 %q：離關鍵字超過 %d 字元", c.raw, codeMaxDistance)
			continue
		}
		score := 100 - float64(d)/2
		if c.start < subjLen {
			score += 30
		}
		if ln := lineAt(rs, c.start); strings.TrimSpace(string(rs[ln.start:ln.end])) == c.raw {
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

func sortCands(c []codeCand) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].start < c[j-1].start; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

// keywordDistance returns the weighted distance to the nearest keyword; candidates
// before a keyword count double. ok is false when every keyword is >200 runes away.
func keywordDistance(c span, kws []span) (int, bool) {
	best, ok := 0, false
	for _, k := range kws {
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

func excludeReason(c codeCand, rs, lower []rune, blocked []span) string {
	if overlaps(c.span, blocked) {
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
			ln := lineAt(lower, c.start)
			line := string(lower[ln.start:ln.end])
			if strings.Contains(line, "年") || monthRe.MatchString(line) || dateNumRe.MatchString(line) {
				return "日期中的年份"
			}
		}
	}
	l, r := c.start, c.end
	for l > 0 && isPhoneRune(rs[l-1]) {
		l--
	}
	for r < len(rs) && isPhoneRune(rs[r]) {
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
