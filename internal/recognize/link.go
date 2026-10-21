package recognize

import (
	"net/url"
	"regexp"
	"strings"

	"verify-code-relay/internal/mailparse"
)

var defaultLinkKeywords = []string{
	"verify", "verification", "confirm", "activate", "activation", "validate", "sign in", "log in",
	"login link", "magic link", "reset password", "reset your password", "password reset",
	"complete registration", "complete your registration", "authenticate",
	"驗證", "验证", "確認", "确认", "啟用", "启用", "激活", "登入", "登录",
	"重設密碼", "重设密码", "重置密碼", "重置密码",
	"完成註冊", "完成注册", "認證", "认证", "核驗", "核验",
}

var (
	linkExcludeWords = []string{"unsubscribe", "退訂", "退订", "取消訂閱", "取消订阅", "preferences", "privacy",
		"隱私", "隐私", "terms", "條款", "条款", "help", "support", "說明", "说明",
		"opt-out", "optout", "view in browser", "view this email", "在瀏覽器中檢視", "在浏览器中查看",
		"聯絡我們", "联系我们", "幫助", "帮助"}
	// linkPathExcludeWords are matched against the link text and URL path only: a
	// verification URL often carries the recipient (?email=contact%40…) in its query.
	linkPathExcludeWords = []string{"contact"}
	socialHosts          = []string{"facebook.com", "twitter.com", "x.com", "instagram.com", "linkedin.com", "youtube.com"}
	// urlHintWords are stems, so "activation" and "validation" count.
	urlHintWords = []string{"verif", "confirm", "activat", "validat", "magic", "reset", "login", "signin", "sign-in",
		"token=", "code=", "ticket="}
	// authHintRe matches "auth" as a path segment or query key, not inside "author" or "oauth".
	authHintRe   = regexp.MustCompile(`(^|[/?&#])auth([/?&#=]|$)`)
	staticExtRe  = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|svg|bmp|ico|css|js|pdf)$`)
	tokenValueRe = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)

	// genericLinkTexts say nothing about where a link goes; the text before it must.
	genericLinkTexts = map[string]bool{"click here": true, "here": true, "click": true, "this link": true, "link": true,
		"點此": true, "點這裡": true, "點擊這裡": true, "点击这里": true, "點擊此處": true, "点击此处": true,
		"按此": true, "這裡": true, "这里": true, "此處": true, "此处": true, "ここ": true, "こちら": true}
	// pointingWords on the line above a generic link show it introduces what follows.
	pointingWords = []string{"below", "following", "link", "button", "click", "下方", "以下", "下列", "下面",
		"連結", "链接", "按鈕", "按钮", "點擊", "点击"}
)

var (
	// notYouWords before a link mark it as the remedy offered when someone else used the
	// account ("If this wasn't you, reset your password"), never a sign-in link — unless
	// an ignoreWords phrase follows the last of them ("若非本人操作請忽略：[登入]"), which
	// is how genuine sign-in and requested-reset mails word it.
	notYouWords = []string{"wasn't you", "was not you", "weren't you", "were not you", "isn't you", "is not you",
		"'s not you", "didn't try", "did not try", "didn't attempt", "did not attempt", "didn't sign in",
		"did not sign in", "didn't log in", "did not log in", "didn't make", "did not make", "didn't do this",
		"didn't authorize", "did not authorize", "didn't initiate", "did not initiate",
		"don't recognize", "do not recognize", "didn't recognize", "did not recognize", "unrecognized",
		"someone may", "compromised",
		"若非本人", "如非本人", "非本人操作", "不是本人", "並非本人", "并非本人", "不是您本人", "不是你本人",
		"若不是您", "若不是你", "如果不是您", "如果不是你",
		"可能已外洩", "可能已洩漏", "可能已泄露", "可能已泄漏",
		"心当たりがない", "心当たりのない", "ご本人でない"}
	ignoreWords = []string{"ignore", "disregard", "no action", "no further action", "nothing to worry",
		"忽略", "不用理會", "不必理會", "無需理會", "无需理会", "不用理会", "無需處理", "无需处理", "破棄", "無視", "削除"}
	// negations right before an ignore word ("do not ignore this", "請勿忽略") keep the
	// remedy reading.
	negations = []string{"don't", "do not", "never", "不要", "請勿", "请勿", "切勿", "勿", "不可"}
	// recoveryWords in a link's text or URL path mark account-recovery pages (password,
	// account security, recent activity). Next to a code they are not the code's sign-in link.
	recoveryWords = []string{"password", "passwd", "密碼", "密码", "パスワード", "secure", "security", "activity",
		"安全", "活動", "活动", "保護", "保护", "セキュリティ", "アクティビティ"}
)

const buttonBonus = 10

const linkThreshold = 50

type linkFinder struct{ keywords []string }

func newLinkFinder(extra []string) *linkFinder {
	var kws []string
	for _, k := range append(append([]string{}, defaultLinkKeywords...), extra...) {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			kws = append(kws, k)
		}
	}
	return &linkFinder{keywords: kws}
}

func (f *linkFinder) containsKeyword(s string) (string, bool) {
	for _, k := range f.keywords {
		if strings.Contains(s, k) {
			return k, true
		}
	}
	return "", false
}

// find returns the best verification link of msg, or "". withCode says a code was
// found in the same message: account-recovery links (recoveryWords) are then left out,
// as they accompany sign-in codes for when someone else signed in.
func (f *linkFinder) find(msg *mailparse.Message, tr *Trace, withCode bool) string {
	kw, ok := f.containsKeyword(strings.ToLower(string(scanRunes(msg))))
	if ok {
		tr.Addf("連結：關鍵字 %q", kw)
	} else {
		// The body may be a stub ("use an HTML client") while a button names the action.
		// Links excluded below ("Confirm email preferences") do not count.
		for _, l := range msg.Links {
			lt := strings.ToLower(l.Text)
			if u, err := url.Parse(l.URL); err != nil || linkExcludeReason(u, strings.ToLower(l.URL), lt) != "" {
				continue
			}
			if kw, ok = f.containsKeyword(lt); ok {
				tr.Addf("連結：連結文字 %q 含關鍵字 %q", l.Text, kw)
				break
			}
		}
	}
	if !ok {
		tr.Addf("連結：沒有找到連結關鍵字")
		return ""
	}
	senderLabel := ""
	if at := strings.LastIndexByte(msg.From.Addr, '@'); at >= 0 {
		senderLabel = MainLabel(msg.From.Addr[at+1:])
	}
	subject := strings.ToLower(msg.Subject)
	best, bestScore := "", -1
	for _, l := range msg.Links {
		u, err := url.Parse(l.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		lu, lt := strings.ToLower(l.URL), strings.ToLower(l.Text)
		if reason := linkExcludeReason(u, lu, lt); reason != "" {
			tr.Addf("  連結 %s：排除（%s）", l.URL, reason)
			continue
		}
		if w, ok := remedyContext(l.Before); ok {
			tr.Addf("  連結 %s：排除（非本人時的補救連結，前文含 %q）", l.URL, w)
			continue
		}
		if withCode {
			w, ok := containsAnyWord(lt, recoveryWords)
			if !ok {
				// the path and fragment only: a query often carries a return address
				// (?next=/settings/security) or tracking (utm_campaign=account-activity)
				// both forms: words run together in escaped ASCII paths, CJK only reads decoded
				w, ok = containsAny(strings.ToLower(u.EscapedPath()+"#"+u.EscapedFragment()+" "+u.Path+"#"+u.Fragment), recoveryWords)
			}
			if ok {
				tr.Addf("  連結 %s：排除（已有驗證碼，不搭配帳號救援連結，含 %q）", l.URL, w)
				continue
			}
		}
		score, note := 0, ""
		textKeyword := false
		if _, ok := f.containsKeyword(lt); ok {
			textKeyword = true
		} else if genericLinkText(lt) {
			if w, ok := purchaseContext(l.Before, subject); ok {
				note = "，前文或主旨含 " + w + "，不採用前文"
			} else if k, ok := f.introducesLink(l.Before); ok {
				textKeyword, note = true, "，前文含 "+k
			}
		}
		if textKeyword {
			score += 50
			if l.Button {
				score += buttonBonus
				note += "，按鈕樣式"
			}
		}
		if urlHint(strings.ToLower(urlTail(u))) {
			score += 30
		}
		if hasTokenValue(u.Query()) {
			score += 15
		}
		if senderLabel != "" && MainLabel(u.Hostname()) == senderLabel {
			score += 10
		}
		tr.Addf("  連結 %s（文字 %q%s）：分數 %d", l.URL, l.Text, note, score)
		if score > bestScore {
			best, bestScore = l.URL, score
		}
	}
	if best == "" || bestScore < linkThreshold {
		tr.Addf("連結：沒有分數達到門檻 %d 的連結", linkThreshold)
		return ""
	}
	tr.Addf("連結：選擇 %s（分數 %d）", best, bestScore)
	return best
}

// urlHint reports whether the URL's path, query or fragment (lower-cased) suggests a
// verification link.
func urlHint(tail string) bool {
	for _, h := range urlHintWords {
		if strings.Contains(tail, h) {
			return true
		}
	}
	return authHintRe.MatchString(tail)
}

// genericLinkText reports whether the lower-cased link text is empty or says only
// "click here" and the like.
func genericLinkText(lt string) bool {
	lt = strings.Trim(lt, " .,:;!?。，：；！？>»›→<«‹←")
	return lt == "" || genericLinkTexts[lt]
}

// purchaseContext returns the commerce word (commerceWords: order, receipt, payment, 訂單 …)
// found in the text before a generic link or in the lower-cased subject: "Order
// confirmation:" above a bare URL introduces an order page, not a verification link.
// "In order to" is not an order.
func purchaseContext(before, subject string) (string, bool) {
	for _, s := range []string{strings.ToLower(before), subject} {
		if w, ok := containsAnyWord(strings.ReplaceAll(s, "in order to", " "), commerceWords); ok {
			return w, true
		}
	}
	return "", false
}

// introducesLink reports whether the text before a generic link (Link.Before, "previous
// line\nsame line") says what it is for: a keyword on the link's own line, or one on the
// line above that also points at what follows ("the link below", "以下連結", a closing
// colon). A keyword alone on the line above is not enough: newsletters mention "confirm"
// in one paragraph and put "Click here to read more" in the next. Keywords match whole
// English words here ("confirm" is not in "Order confirmation").
func (f *linkFinder) introducesLink(before string) (string, bool) {
	prev, cur, _ := strings.Cut(strings.ToLower(before), "\n")
	if k, ok := containsAnyWord(cur, f.keywords); ok {
		return k, true
	}
	k, ok := containsAnyWord(prev, f.keywords)
	if !ok {
		return "", false
	}
	if strings.HasSuffix(prev, ":") || strings.HasSuffix(prev, "：") {
		return k, true
	}
	for _, w := range pointingWords {
		if strings.Contains(prev, w) {
			return k, true
		}
	}
	return "", false
}

// urlTail returns the URL without scheme, user info and host — the path, query
// and fragment, where hint words count. The fragment is kept because single-page
// apps route there (".../#/verify?token=...").
func urlTail(u *url.URL) string {
	t := *u
	t.Scheme, t.User, t.Host = "", nil, ""
	return t.String()
}

// hasTokenValue reports whether any value of any query parameter looks like a token.
func hasTokenValue(q url.Values) bool {
	for _, vals := range q {
		for _, v := range vals {
			if tokenValueRe.MatchString(v) {
				return true
			}
		}
	}
	return false
}

func linkExcludeReason(u *url.URL, lu, lt string) string {
	for _, w := range linkExcludeWords {
		if strings.Contains(lu, w) || strings.Contains(lt, w) {
			return "含 " + w
		}
	}
	path := strings.ToLower(u.Path)
	for _, w := range linkPathExcludeWords {
		if strings.Contains(path, w) || strings.Contains(lt, w) {
			return "含 " + w
		}
	}
	host := strings.ToLower(u.Hostname())
	for _, s := range socialHosts {
		if host == s || strings.HasSuffix(host, "."+s) {
			return "社群網站"
		}
	}
	if staticExtRe.MatchString(u.Path) {
		return "靜態資源"
	}
	return ""
}

// remedyContext returns the notYouWords phrase that makes the text before a link
// (Link.Before) introduce a remedy for someone else using the account. It is false when
// an ignoreWords phrase follows the last such phrase ("If you didn't try to sign in,
// you can safely ignore this email. Otherwise click below:").
func remedyContext(before string) (string, bool) {
	s := normalizeApostrophes(strings.ToLower(before))
	word, end := "", -1
	for _, w := range notYouWords {
		if e := lastWordEnd(s, w); e > end {
			word, end = w, e
		}
	}
	if end < 0 {
		return "", false
	}
	if ignoredAfter(s[end:]) {
		return "", false
	}
	return word, true
}

// ignoredAfter reports whether s holds an ignoreWords phrase that is not negated by a
// negations word just before it ("If this wasn't you, do not ignore this email").
func ignoredAfter(s string) bool {
	for _, w := range ignoreWords {
		for from := 0; ; {
			j := strings.Index(s[from:], w)
			if j < 0 {
				break
			}
			j += from
			head := strings.TrimRight(s[max(0, j-12):j], " ")
			negated := false
			for _, n := range negations {
				if strings.HasSuffix(head, n) {
					negated = true
					break
				}
			}
			if !negated {
				return true
			}
			from = j + len(w)
		}
	}
	return false
}

// lastWordEnd returns the end offset of the last occurrence of w in s, where an ASCII
// letter at either end of w must not touch another ASCII letter (as in containsWord), or -1.
func lastWordEnd(s, w string) int {
	for e := len(s); e > 0; {
		j := strings.LastIndex(s[:e], w)
		if j < 0 {
			return -1
		}
		end := j + len(w)
		if (!isASCIILetter(rune(w[0])) || j == 0 || !isASCIILetter(rune(s[j-1]))) &&
			(!isASCIILetter(rune(w[len(w)-1])) || end == len(s) || !isASCIILetter(rune(s[end]))) {
			return end
		}
		e = end - 1
	}
	return -1
}

// normalizeApostrophes turns typographic apostrophes into ASCII ones ("wasn’t" → "wasn't").
func normalizeApostrophes(s string) string {
	return strings.NewReplacer("’", "'", "‘", "'", "ʼ", "'").Replace(s)
}

// containsAny returns the first of words that s contains anywhere (URLs run words
// together: "resetpassword").
func containsAny(s string, words []string) (string, bool) {
	for _, w := range words {
		if strings.Contains(s, w) {
			return w, true
		}
	}
	return "", false
}
