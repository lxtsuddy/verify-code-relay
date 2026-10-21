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

func (f *linkFinder) find(msg *mailparse.Message, tr *Trace) string {
	kw, ok := f.containsKeyword(strings.ToLower(string(scanRunes(msg))))
	if ok {
		tr.Addf("連結：關鍵字 %q", kw)
	} else {
		// The body may be a stub ("use an HTML client") while a button names the action.
		for _, l := range msg.Links {
			if kw, ok = f.containsKeyword(strings.ToLower(l.Text)); ok {
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
		score, note := 0, ""
		textKeyword := false
		if _, ok := f.containsKeyword(lt); ok {
			textKeyword = true
		} else if genericLinkText(lt) {
			if k, ok := f.introducesLink(l.Before); ok {
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

// introducesLink reports whether the text before a generic link (Link.Before, "previous
// line\nsame line") says what it is for: a keyword on the link's own line, or one on the
// line above that also points at what follows ("the link below", "以下連結", a closing
// colon). A keyword alone on the line above is not enough: newsletters mention "confirm"
// in one paragraph and put "Click here to read more" in the next.
func (f *linkFinder) introducesLink(before string) (string, bool) {
	prev, cur, _ := strings.Cut(strings.ToLower(before), "\n")
	if k, ok := f.containsKeyword(cur); ok {
		return k, true
	}
	k, ok := f.containsKeyword(prev)
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
