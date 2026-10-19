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
	"驗證", "验证", "確認", "确认", "啟用", "启用", "激活", "登入", "登录",
	"重設密碼", "重设密码", "重置密碼", "重置密码",
}

var (
	linkExcludeWords = []string{"unsubscribe", "退訂", "退订", "取消訂閱", "取消订阅", "preferences", "privacy",
		"隱私", "隐私", "terms", "條款", "条款", "help", "support", "說明", "说明"}
	socialHosts  = []string{"facebook.com", "twitter.com", "x.com", "instagram.com", "linkedin.com", "youtube.com"}
	urlHintWords = []string{"verify", "confirm", "activate", "validate", "magic", "reset", "token=", "code=", "login"}
	imageExtRe   = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|svg|bmp|ico)$`)
	tokenValueRe = regexp.MustCompile(`^[A-Za-z0-9_-]{20,}$`)
)

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
	kw, ok := f.containsKeyword(strings.ToLower(msg.Subject + "\n" + msg.Text))
	if !ok {
		tr.Addf("連結：沒有找到連結關鍵字")
		return ""
	}
	tr.Addf("連結：關鍵字 %q", kw)
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
		score := 0
		if _, ok := f.containsKeyword(lt); ok {
			score += 50
		}
		for _, h := range urlHintWords {
			if strings.Contains(lu, h) {
				score += 30
				break
			}
		}
		for _, vals := range u.Query() {
			if len(vals) > 0 && tokenValueRe.MatchString(vals[0]) {
				score += 15
				break
			}
		}
		if senderLabel != "" && MainLabel(u.Hostname()) == senderLabel {
			score += 10
		}
		tr.Addf("  連結 %s（文字 %q）：分數 %d", l.URL, l.Text, score)
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

func linkExcludeReason(u *url.URL, lu, lt string) string {
	for _, w := range linkExcludeWords {
		if strings.Contains(lu, w) || strings.Contains(lt, w) {
			return "含 " + w
		}
	}
	host := strings.ToLower(u.Hostname())
	for _, s := range socialHosts {
		if host == s || strings.HasSuffix(host, "."+s) {
			return "社群網站"
		}
	}
	if imageExtRe.MatchString(u.Path) {
		return "圖片"
	}
	return ""
}
