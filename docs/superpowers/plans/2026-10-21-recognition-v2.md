# 辨識強化與 Web 信件測試 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 讓內建辨識認得更多真實驗證信（「this code」寫法、主旨即驗證碼、只有「驗證」字樣或圖片的按鈕、「Click here」連結、只在 HTML 版有內容的信），同時排除更多誤判（卡號末碼、郵遞區號、地址門牌、電子報連結），並在 Web「辨識規則」頁提供上傳／貼上信件測試。

**Architecture:** `mailparse` 改為 HTML 優先，另存純文字版到 `AltText`，並在 HTML 轉文字時記錄強調區段與連結的按鈕樣式、前文；`recognize.Chain` 先用主要內文、沒有結果再用 `AltText` 跑一輪；`code.go`／`link.go` 依規格調整評分與排除；`pipeline.RunTrial` 把「解析→辨識→渲染」抽出，供 CLI `relay test` 與新的 `POST /api/rules/test` 共用；規則頁加測試面板。

**Tech Stack:** Go 1.26、`golang.org/x/net/html`（既有）、`github.com/emersion/go-message`（既有）、Go `html/template` + 原生 JS／CSS（`embed`）。

**Spec:** `docs/superpowers/specs/2026-10-21-recognition-v2-design.md`（每個任務開始前先讀對應章節）

## Global Constraints

- 所有使用者可見文字、Trace、錯誤訊息、日誌訊息用繁體中文，跟隨既有風格；程式碼註解用英文，密度與既有程式相同。
- 不新增任何依賴。
- CSP：`script-src 'self'; style-src 'self'`——不得使用 inline `<style>`、`style=` 屬性、inline `<script>`、`onclick` 等事件屬性。
- Trace 與信件內容含驗證碼與連結：**不得**寫進任何日誌（`slog`）、資料庫或檔案。
- 既有 18 封語料（`testdata/eml/*.eml`）結果必須不變。任何一封改變結果時**停下來回報**，不得改它的 `.expected.yaml`。
- 測試語料只能用 `testdata/eml/gen.py` 產生（`python3 testdata/eml/gen.py`），不要手寫 `.eml`。
- 前端任務（Task 12）實作者**必須先叫用 frontend-design skill**；行動裝置寬度也要可用。
- 每個任務完成前必須通過：`gofmt -l .` 無輸出、`go vet ./...`、`go test -race -count=1 ./...`。
- Commit 訊息風格同 repo（`feat(recognize): ...`、`fix(mailparse): ...`），結尾空一行加 `Co-Authored-By: Claude Code <noreply@anthropic.com>`。
- 工作分支：`recognition-v2`（已存在）。不要 push。不要把 repo 根目錄的 `argon_sug.md` 加進 git。

## 檔案結構

| 檔案 | 變更 |
|---|---|
| `internal/mailparse/html.go` | 位置對應的 normalize、強調區段、連結 `Button`／`Before`／圖片 alt、`renderHTML` |
| `internal/mailparse/parse.go` | `Span`、`Message.AltText`／`Emphasis`、`Link.Button`／`Before`、HTML 優先、`bareLinks`、`mergeLinks` |
| `internal/recognize/recognize.go` | `Chain.Recognize` 兩輪 |
| `internal/recognize/code.go` | 關鍵字、新排除（卡號、郵遞區號、地址）、B 規則、純字母碼、強調加分 |
| `internal/recognize/link.go` | 入口檢查、關鍵字、網址字根、按鈕加分、泛用連結字、排除 |
| `testdata/eml/gen.py` | 新增 7 封應辨識、6 封反例 |
| `internal/pipeline/trial.go`（新） | `Trial`、`RunTrial`、`kindOf`、`renderData` |
| `internal/pipeline/pipeline.go` | `Process` 改用 `kindOf`／`renderData` |
| `cmd/relay/testcmd.go` | 改用 `pipeline.RunTrial` |
| `internal/web/mailtest.go`（新） | `POST /api/rules/test` |
| `internal/web/middleware.go`、`routes.go`、`page_rules.go` | 路由本文上限、路由、規則頁信箱選項 |
| `internal/web/templates/rules.html`、`static/app.js`、`static/app.css` | 測試面板 |
| `README.md`、`docs/TODO.md` | 文件 |

---

### Task 1: mailparse — 位置對應的 normalize 與強調區段

**Files:**
- Modify: `internal/mailparse/html.go`
- Modify: `internal/mailparse/parse.go`（只加 `Span` 型別）
- Test: `internal/mailparse/html_test.go`

**Interfaces:**
- Produces:
  - `type Span struct{ Start, End int }`（`parse.go`，rune 位置，End 不含）
  - `func normalizeLinesMap(s string, verbatim []span) (string, []int)`：第二個回傳值 `pos` 長度 `len(s)+1`，`pos[i]` 為 `s[i:]` 中第一個被保留的位元組在結果中的位元組位置（後面沒有保留者時為結果長度）。
  - `func renderHTML(src string) (string, []Link, []Span)`：`HTMLToText` 的擴充版，第三個回傳值為結果文字中的強調區段（rune 位置）。`HTMLToText(src) (string, []Link)` 保留，內部呼叫 `renderHTML`。
  - `func runeSpans(text string, raw []span, pos []int) []Span`

- [ ] **Step 1: 寫失敗測試**

在 `html_test.go` 加入（`normalizeLinesRef` 是現行 `normalizeLines` 的原樣複製，當作對照組）：

```go
// normalizeLinesRef is normalizeLines as it was before it tracked positions: the
// position-tracking version must produce exactly the same text.
func normalizeLinesRef(s string, verbatim []span) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := true
	off := 0
	for _, l := range lines {
		start, end := off, off+len(l)
		off = end + 1
		for len(verbatim) > 0 && verbatim[0].end <= start {
			verbatim = verbatim[1:]
		}
		if len(verbatim) > 0 && verbatim[0].start < end {
			l = strings.TrimRight(l, " \t\r\f\v")
		} else {
			l = strings.TrimSpace(spaceRun.ReplaceAllString(l, " "))
		}
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
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func TestNormalizeLinesMapMatchesReference(t *testing.T) {
	cases := []struct {
		s        string
		verbatim []span
	}{
		{"", nil},
		{"  a   b  \n\n\n c\t\td \n", nil},
		{"　全形空白　\n nbsp ", nil},
		{"a\u0085\nb ", nil},
		{"bad \xff byte\n\xef\xbf\xbd ok", nil},
		{"x\n  keep   inner  \ny", []span{{2, 20}}},
		{"\n\n\nlead\n\n\ntrail\n\n\n", nil},
		{"中文  字\t\t間\n第二行", nil},
	}
	for _, c := range cases {
		got, pos := normalizeLinesMap(c.s, c.verbatim)
		if want := normalizeLinesRef(c.s, c.verbatim); got != want {
			t.Errorf("normalizeLinesMap(%q) = %q, want %q", c.s, got, want)
		}
		if len(pos) != len(c.s)+1 {
			t.Fatalf("len(pos) = %d, want %d", len(pos), len(c.s)+1)
		}
		for i := 1; i < len(pos); i++ {
			if pos[i] < pos[i-1] || pos[i] > len(got) {
				t.Fatalf("pos not monotonic within [0,%d]: %v", len(got), pos)
			}
		}
	}
}

func TestRenderHTMLEmphasis(t *testing.T) {
	cases := []struct {
		src, text string
		want      []Span
	}{
		{`<p>Your code is <b>123456</b></p>`, "Your code is 123456", []Span{{13, 19}}},
		{`<p>您的驗證碼為 <strong>482913</strong>。</p>`, "您的驗證碼為 482913。", []Span{{7, 13}}},
		{`<p>a    b <b>  777777  </b> c</p>`, "a b 777777 c", []Span{{4, 10}}},
		{`<div style="font-size: 28px">654321</div>`, "654321", []Span{{0, 6}}},
		{`<td style="color:#333;letter-spacing:6px">ABCDEF</td>`, "ABCDEF", []Span{{0, 6}}},
		{`<span style="font-size:12px">111111</span>`, "111111", nil},
		{`<span style="font-size:1.5em">2</span><span style="font-size:16pt">3</span>`, "23", []Span{{0, 1}, {1, 2}}},
		{`<p>x</p><h2>
  Code
</h2>`, "x\nCode", []Span{{2, 6}}},
		{`<b>   </b>tail`, "tail", nil},
	}
	for _, c := range cases {
		text, _, emph := renderHTML(c.src)
		if text != c.text {
			t.Errorf("renderHTML(%q) text = %q, want %q", c.src, text, c.text)
		}
		if !reflect.DeepEqual(emph, c.want) {
			t.Errorf("renderHTML(%q) emphasis = %v, want %v", c.src, emph, c.want)
		}
	}
}
```

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/mailparse -run 'TestNormalizeLinesMapMatchesReference|TestRenderHTMLEmphasis'`
Expected: 編譯失敗（`normalizeLinesMap`、`renderHTML`、`Span` 未定義）。

- [ ] **Step 3: 實作**

`parse.go` 在 `Link` 型別之前加入：

```go
// Span is a rune range [Start, End) of Message.Text.
type Span struct{ Start, End int }
```

`html.go`：

1. import 加 `"slices"`、`"strconv"`、`"unicode"`、`"unicode/utf8"`。
2. 加入強調判斷：

```go
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
```

3. 把 `HTMLToText` 的本體搬到 `renderHTML`，在 element 分支記錄強調區段：

```go
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
	pre := 0
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
			// … the rest of the element branch exactly as in HTMLToText today …
		}
		// … text node and children exactly as today …
	}
	walk(doc)
	text, pos := normalizeLinesMap(b.String(), verbatim)
	return text, links, runeSpans(text, emph, pos)
}
```

（`<a>` 分支提早 `return`，`defer` 仍會執行，所以 `<a>` 本身或其內的強調都會記錄。）

4. 位置對應的 normalize：

```go
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
```

注意 `emph` 依結束順序加入（巢狀時外層在後），所以 `TestRenderHTMLEmphasis` 的期望順序以實際文件順序為準；若巢狀造成順序與期望不同，修測試期望而不是排序（`recognize` 端會自行排序合併）。

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/mailparse ./internal/recognize`
Expected: PASS（含既有的所有 mailparse 測試與 18 封語料）。

- [ ] **Step 5: Commit**

```bash
git add internal/mailparse
git commit -m "feat(mailparse): track positions through normalisation and record emphasised text

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: mailparse — 連結的按鈕樣式、前文與圖片 alt

**Files:**
- Modify: `internal/mailparse/parse.go`（`Link` 欄位）
- Modify: `internal/mailparse/html.go`
- Test: `internal/mailparse/html_test.go`

**Interfaces:**
- Consumes: Task 1 的 `renderHTML`、`normalizeLinesMap`。
- Produces:
  - `Link{URL, Text string; Button bool; Before string}`
  - `func textBefore(text string, off int) string`：回傳 `"上一個非空白行\n同一行在 off 之前的文字"`，兩段各自 `collapseSpaces` 並各取最後 120 rune。
  - `func isButton(a *html.Node) bool`、`func imgAlt(n *html.Node) string`

- [ ] **Step 1: 寫失敗測試**

```go
func TestRenderHTMLLinkDetails(t *testing.T) {
	cases := []struct {
		name, src string
		want      Link
	}{
		{"image alt", `<a href="https://x.test/a"><img src="b.png" alt=" Verify  email "></a>`,
			Link{URL: "https://x.test/a", Text: "Verify email", Before: "\n"}},
		{"text beats alt", `<a href="https://x.test/a">Go <img alt="Verify"></a>`,
			Link{URL: "https://x.test/a", Text: "Go", Before: "\n"}},
		{"bg on a", `<a href="https://x.test/v" style="background-color:#1a73e8;color:#fff">Verify</a>`,
			Link{URL: "https://x.test/v", Text: "Verify", Button: true, Before: "\n"}},
		{"padding on a", `<a href="https://x.test/v" style="padding:12px 24px">Go</a>`,
			Link{URL: "https://x.test/v", Text: "Go", Button: true, Before: "\n"}},
		{"class btn", `<a class="btn btn-primary" href="https://x.test/v">Go</a>`,
			Link{URL: "https://x.test/v", Text: "Go", Button: true, Before: "\n"}},
		{"bgcolor parent td", `<table><tr><td bgcolor="#1a73e8"><a href="https://x.test/v">Verify</a></td></tr></table>`,
			Link{URL: "https://x.test/v", Text: "Verify", Button: true, Before: "\n"}},
		{"white parent is not a button", `<table><tr><td style="background:#FFFFFF"><a href="https://x.test/v">Verify</a></td></tr></table>`,
			Link{URL: "https://x.test/v", Text: "Verify", Before: "\n"}},
		{"padding on parent is not a button", `<table><tr><td style="padding:10px"><a href="https://x.test/v">Go</a></td></tr></table>`,
			Link{URL: "https://x.test/v", Text: "Go", Before: "\n"}},
		{"parent with text is not a button", `<p style="background:#00f">Read <a href="https://x.test/v">more</a></p>`,
			Link{URL: "https://x.test/v", Text: "more", Before: "\nRead"}},
		{"styled only child", `<a href="https://x.test/v"><span style="background:#e00;padding:8px">Verify</span></a>`,
			Link{URL: "https://x.test/v", Text: "Verify", Button: true, Before: "\n"}},
		{"before: previous non-blank line", `<p>Please verify your email by clicking the link below.</p><p><a href="https://x.test/v">Click here</a></p>`,
			Link{URL: "https://x.test/v", Text: "Click here", Before: "Please verify your email by clicking the link below.\n"}},
		{"before: same line", `<p>Thanks.</p><p>To finish,   <a href="https://x.test/v">click here</a></p>`,
			Link{URL: "https://x.test/v", Text: "click here", Before: "Thanks.\nTo finish,"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, links, _ := renderHTML(c.src)
			if len(links) != 1 || links[0] != c.want {
				t.Errorf("links = %+v, want [%+v]", links, c.want)
			}
		})
	}
}

func TestTextBeforeCutsTo120Runes(t *testing.T) {
	long := strings.Repeat("驗", 200)
	got := textBefore(long+"\n\n"+long+" x", len(long)*2+2+len(" x"))
	prev, cur, _ := strings.Cut(got, "\n")
	if utf8.RuneCountInString(prev) != 120 || utf8.RuneCountInString(cur) != 120 {
		t.Fatalf("prev %d runes, cur %d runes", utf8.RuneCountInString(prev), utf8.RuneCountInString(cur))
	}
}
```

（`html_test.go` import 加 `"unicode/utf8"`。）

同時更新既有 `TestHTMLToText` 的期望：

```go
	wantLinks := []Link{{URL: "https://example.com/verify?t=abc", Text: "Verify email", Before: "c\n"}}
```

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/mailparse -run 'TestRenderHTMLLinkDetails|TestTextBefore|TestHTMLToText'`
Expected: 編譯失敗（`Button`、`Before`、`textBefore` 未定義）。

- [ ] **Step 3: 實作**

`parse.go`：

```go
type Link struct {
	URL  string
	Text string
	// Button is set when the link is styled as a button (see isButton).
	Button bool
	// Before is the text before the link: the nearest non-blank line above, "\n", then
	// the link's own line up to the link (see textBefore).
	Before string
}
```

`html.go`：

```go
const beforeRunes = 120

// textBefore returns the text before byte offset off of text as "previous line\nsame
// line": the same line runs from the line start to off, the previous line is the nearest
// non-blank line above it. Each is space-collapsed and cut to its last 120 runes.
func textBefore(text string, off int) string {
	head := text[max(0, off-4096):off]
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
```

`renderHTML` 的 `<a>` 分支改為記錄起點並補欄位，最後填 `Before`：

```go
	var linkAt []int // byte offset in b where each link starts
	…
			if n.DataAtom == atom.A {
				start := b.Len()
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				href := strings.TrimSpace(attr(n, "href"))
				if isHTTP(href) {
					text := collapseSpaces(b.String()[start:])
					if text == "" {
						text = imgAlt(n)
					}
					links = append(links, Link{URL: href, Text: text, Button: isButton(n)})
					linkAt = append(linkAt, start)
				}
				return
			}
	…
	walk(doc)
	text, pos := normalizeLinesMap(b.String(), verbatim)
	for i := range links {
		links[i].Before = textBefore(text, pos[linkAt[i]])
	}
	return text, links, runeSpans(text, emph, pos)
```

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/mailparse ./internal/recognize`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/mailparse
git commit -m "feat(mailparse): note button styling, the text before each link and image alt text

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: mailparse — HTML 優先、AltText 與裸網址前文

**Files:**
- Modify: `internal/mailparse/parse.go`
- Modify: `internal/mailparse/html.go`（`bareLinks`）
- Test: `internal/mailparse/parse_test.go`

**Interfaces:**
- Consumes: `renderHTML`、`textBefore`、`urlRe`。
- Produces:
  - `Message.AltText string`、`Message.Emphasis []Span`
  - `func bareLinks(text string) []Link`：每個裸網址一個 `Link{URL, Before}`（`Text` 空）。
  - `func mergeLinks(htmlLinks []Link, bare ...[]Link) []Link`

- [ ] **Step 1: 寫失敗測試**

更新 `parse_test.go` 第一個測試中「text should come from text/plain」那段為：

```go
	if !strings.Contains(m.Text, "Your code is 123456") || strings.Contains(m.Text, "plain-link") {
		t.Errorf("text should come from text/html: %q", m.Text)
	}
	if !strings.Contains(m.AltText, "Your code is 123456\nhttps://github.com/plain-link") {
		t.Errorf("alt text should come from text/plain: %q", m.AltText)
	}
	if !reflect.DeepEqual(m.Emphasis, []Span{{13, 19}}) {
		t.Errorf("emphasis = %v", m.Emphasis)
	}
```

（若該測試的連結檢查依賴 plain-link 出現在 `m.Links`：它仍會出現，來源改為 `AltText` 的裸網址。）

新增：

```go
func TestParseTextSources(t *testing.T) {
	multi := func(plain, html string) []byte {
		return crlf("From: a@b.c\nSubject: s\nContent-Type: multipart/alternative; boundary=\"b\"\n\n--b\nContent-Type: text/plain; charset=utf-8\n\n" +
			plain + "\n--b\nContent-Type: text/html; charset=utf-8\n\n" + html + "\n--b--\n")
	}
	single := func(ct, body string) []byte {
		return crlf("From: a@b.c\nSubject: s\nContent-Type: " + ct + "; charset=utf-8\n\n" + body + "\n")
	}
	cases := []struct {
		name                string
		raw                 []byte
		text, alt           string
		emphasis            []Span
	}{
		{"plain only", single("text/plain", "Code 1234"), "Code 1234", "", nil},
		{"html only", single("text/html", "<p>Code <b>1234</b></p>"), "Code 1234", "", []Span{{5, 9}}},
		{"html first", multi("Please use an HTML mail client.", "<p>Code <b>1234</b></p>"), "Code 1234", "Please use an HTML mail client.", []Span{{5, 9}}},
		{"same text kept once", multi("Code 1234", "<p>Code 1234</p>"), "Code 1234", "", nil},
		{"blank html falls back", multi("Code 1234", "<p> </p>"), "Code 1234", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := Parse(c.raw)
			if err != nil {
				t.Fatal(err)
			}
			if m.Text != c.text || m.AltText != c.alt || !reflect.DeepEqual(m.Emphasis, c.emphasis) {
				t.Errorf("text %q alt %q emphasis %v", m.Text, m.AltText, m.Emphasis)
			}
		})
	}
}

func TestParseBareLinkBefore(t *testing.T) {
	raw := crlf("From: a@b.c\nSubject: s\nContent-Type: multipart/alternative; boundary=\"b\"\n\n" +
		"--b\nContent-Type: text/plain; charset=utf-8\n\nClick the link below to verify:\nhttps://x.test/v?t=1\n\nOr open https://x.test/both\n" +
		"--b\nContent-Type: text/html; charset=utf-8\n\n<p>Welcome</p><a href=\"https://x.test/both\" class=\"btn\">Verify</a>\n--b--\n")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]Link{}
	for _, l := range m.Links {
		byURL[l.URL] = l
	}
	if l := byURL["https://x.test/v?t=1"]; l.Before != "Click the link below to verify:\n" || l.Text != "" {
		t.Errorf("bare link = %+v", l)
	}
	if l := byURL["https://x.test/both"]; l.Text != "Verify" || !l.Button || l.Before != "Welcome\n" {
		t.Errorf("html link should win over the bare one: %+v", l)
	}
}
```

（`parse_test.go` import 加 `"reflect"`，若尚未有。）

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/mailparse`
Expected: 編譯失敗（`AltText`、`Emphasis` 未定義）。

- [ ] **Step 3: 實作**

`parse.go` 的 `Message` 加欄位（放在 `Text` 後）：

```go
	Text    string
	// AltText is the other body when the message has both: the text/plain part when
	// Text came from text/html. It is empty when there is only one, or both read the same.
	AltText string
	// Emphasis lists the parts of Text set apart in the HTML (bold, code, headings,
	// large or spaced-out type), in rune offsets; nil when Text is not from HTML.
	Emphasis []Span
```

`Parse` 結尾段落改為：

```go
	var plainText, htmlText string
	var htmlLinks []Link
	var emph []Span
	if haveHTML {
		htmlText, htmlLinks, emph = renderHTML(htmlBody)
	}
	if havePlain {
		plainText = normalizeText(plain)
	}
	if htmlText != "" {
		// HTML is what the reader sees; the plain part is often a stub ("use an HTML client").
		m.Text, m.Emphasis = htmlText, emph
		if plainText != htmlText {
			m.AltText = plainText
		}
	} else {
		// An empty text/html part must not hide a populated plain alternative.
		m.Text = plainText
	}
	m.Links = mergeLinks(htmlLinks, bareLinks(m.Text), bareLinks(m.AltText))
```

`html.go`（`ExtractURLs` 保留不動）：

```go
// bareLinks returns the bare http(s) URLs of text, each with the text before it.
func bareLinks(text string) []Link {
	var out []Link
	for _, m := range urlRe.FindAllStringIndex(text, -1) {
		if u := strings.TrimRight(text[m[0]:m[1]], ".,;:!?"); u != "" {
			out = append(out, Link{URL: u, Before: textBefore(text, m[0])})
		}
	}
	return out
}
```

`parse.go` 的 `mergeLinks` 改為：

```go
// mergeLinks de-duplicates by URL. The first entry of a URL wins (HTML links come
// first, so their Button and Before stand); a later entry only fills an empty Text.
func mergeLinks(htmlLinks []Link, bare ...[]Link) []Link {
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
	for _, list := range bare {
		for _, l := range list {
			add(l)
		}
	}
	return out
}
```

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./...`
Expected: PASS（含 18 封語料；撰寫規格時已實測過 HTML 優先不影響語料）。若語料有任何一封改變結果，停下來回報。

- [ ] **Step 5: Commit**

```bash
git add internal/mailparse
git commit -m "feat(mailparse): prefer the HTML body, keep the plain one as AltText

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: recognize — 兩輪辨識

**Files:**
- Modify: `internal/recognize/recognize.go`
- Test: `internal/recognize/recognize_test.go`

**Interfaces:**
- Consumes: `mailparse.Message.AltText`、`Emphasis`。
- Produces: `Chain.Recognize` 行為：有 `AltText` 時 Trace 第一行為 `第一輪：HTML 內文`，進入第二輪時加 `第二輪：純文字內文`；沒有 `AltText` 時 Trace 與現在相同。

- [ ] **Step 1: 寫失敗測試**

```go
// textStage finds a result when the message text contains want, recording each text it saw.
type textStage struct {
	want string
	seen *[]string
}

func (s textStage) Run(msg *mailparse.Message, _ *Trace) (*Result, bool) {
	*s.seen = append(*s.seen, msg.Text)
	if strings.Contains(msg.Text, s.want) {
		return &Result{Code: "1"}, false
	}
	return nil, false
}

func TestChainSecondRoundUsesAltText(t *testing.T) {
	var seen []string
	c := NewChain(textStage{want: "PLAIN", seen: &seen})
	msg := &mailparse.Message{Text: "html", AltText: "PLAIN", Emphasis: []mailparse.Span{{0, 4}}}
	res, _, tr := c.Recognize(msg)
	if res == nil || !reflect.DeepEqual(seen, []string{"html", "PLAIN"}) {
		t.Fatalf("res=%+v seen=%q", res, seen)
	}
	steps := strings.Join(tr.Steps, "\n")
	if !strings.Contains(steps, "第一輪：HTML 內文") || !strings.Contains(steps, "第二輪：純文字內文") {
		t.Fatalf("trace = %q", tr.Steps)
	}
	if msg.Text != "html" || msg.AltText != "PLAIN" || len(msg.Emphasis) != 1 {
		t.Fatalf("message was modified: %+v", msg)
	}
}

func TestChainNoSecondRound(t *testing.T) {
	var seen []string
	c := NewChain(textStage{want: "html", seen: &seen})
	if res, _, _ := c.Recognize(&mailparse.Message{Text: "html", AltText: "PLAIN"}); res == nil || len(seen) != 1 {
		t.Fatalf("first-round result: res=%+v seen=%q", res, seen)
	}
	seen = nil
	res, _, tr := c.Recognize(&mailparse.Message{Text: "nothing"})
	if res != nil || len(seen) != 1 || strings.Contains(strings.Join(tr.Steps, "\n"), "第一輪") {
		t.Fatalf("no AltText: res=%+v seen=%q trace=%q", res, seen, tr.Steps)
	}
}

func TestChainSecondRoundSeesNoEmphasis(t *testing.T) {
	var got []mailparse.Span
	stage := stageFunc(func(msg *mailparse.Message) { got = msg.Emphasis })
	NewChain(stage).Recognize(&mailparse.Message{Text: "a", AltText: "b", Emphasis: []mailparse.Span{{0, 1}}})
	if got != nil {
		t.Fatalf("second round emphasis = %v", got)
	}
}

// stageFunc calls f with every message it is run on and finds nothing.
type stageFunc func(msg *mailparse.Message)

func (f stageFunc) Run(msg *mailparse.Message, _ *Trace) (*Result, bool) {
	f(msg)
	return nil, false
}
```

（import 加 `"reflect"`、`"strings"`。）

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/recognize -run TestChain`
Expected: FAIL（只跑一輪）。

- [ ] **Step 3: 實作**

```go
// Recognize runs the stages on the message; when none finds anything and the message
// has a second body (AltText), it runs them again on a copy reading that body instead.
func (c *Chain) Recognize(msg *mailparse.Message) (*Result, bool, *Trace) {
	tr := &Trace{}
	rounds := []*mailparse.Message{msg}
	if msg.AltText != "" {
		alt := *msg
		alt.Text, alt.AltText, alt.Emphasis = msg.AltText, "", nil
		rounds = append(rounds, &alt)
		tr.Addf("第一輪：HTML 內文")
	}
	for i, m := range rounds {
		if i == 1 {
			tr.Addf("第二輪：純文字內文")
		}
		for _, s := range c.stages {
			res, ignore := s.Run(m, tr)
			if res != nil {
				cp := *res // the stage may reuse its Result; never modify it
				res = &cp
			}
			if ignore {
				return res, true, tr
			}
			if res != nil {
				if res.Service == "" {
					res.Service = ServiceName(msg.From)
				}
				return res, false, tr
			}
		}
	}
	tr.Addf("結果：不符合任何規則")
	return nil, false, tr
}
```

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/recognize`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/recognize
git commit -m "feat(recognize): retry with the plain body when the HTML body yields nothing

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: recognize/code — 關鍵字與新排除（卡號、郵遞區號、地址）

**Files:**
- Modify: `internal/recognize/code.go`
- Test: `internal/recognize/code_test.go`

**Interfaces:**
- Produces: `excludeReason` 新增原因「卡號末碼」「郵遞區號」「地址」；`func addressReason(c codeCand, t *codeText) string`；`func hasContextWord(s string, words []string) bool`。

- [ ] **Step 1: 寫失敗測試**

在 `TestCodeFinder` 的 cases 加入：

```go
		{"this code", "Use 123456 to set up recovery email", "Use this code to finish setting up:\n123456\n\nThis code will expire in 24 hours.", "123456"},
		{"pin", "Your PIN", "PIN: 4821", "4821"},
		{"pin boundary", "Spinning class", "Room 4821", ""},
		{"dynamic password zh", "通知", "您的動態密碼為 739201", "739201"},
		{"card last4 zh", "交易通知", "您末四碼 1234 的信用卡刷卡成功，請勿將驗證碼告訴他人", ""},
		{"card ending in", "Verification code", "Card ending in 4821 was charged", ""},
		{"card last 4 digits", "Verification code", "Card (last 4 digits 4821) was charged", ""},
		{"zip word", "Verification code", "We never ask for your verification code. Zip: 94043", ""},
		{"unzip is not zip", "Verification code", "Unzip 482913", "482913"},
		{"postcode zh", "验证码", "邮编 100080", ""},
		{"street number", "Verification code", "We never ask for your verification code.\nExample Inc., 1600 Main Street, Springfield", ""},
		{"parkway", "Verification code", "Never share your verification code. 1600 Amphitheatre Parkway", ""},
		{"1st is not st", "Verification code", "Your verification code 482913 (1st sign-in)", "482913"},
		{"cjk house number", "驗證碼", "請勿將驗證碼告訴他人。台北市中山路 1200 號", ""},
		{"us zip", "Verification code", "Never share your verification code. Mountain View, CA 94043", ""},
		{"by the way is fine", "Verification code", "Your verification code: 48213 by the way", "48213"},
```

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/recognize -run TestCodeFinder`
Expected: FAIL（新 cases 不符）。

- [ ] **Step 3: 實作**

`defaultCodeKeywords` 改為：

```go
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
```

新增（放在 `orderWords` 附近）：

```go
var (
	// cardWords and zipWords are matched in the lower-cased, space-free text just before
	// a candidate: card numbers ("ending in 4821") and postcodes are not codes.
	cardWords = []string{"末四碼", "末4碼", "末四码", "末4码", "尾號", "尾号", "endingin", "endsin", "last4", "lastfour"}
	zipWords  = []string{"郵遞區號", "郵政編碼", "邮编", "邮政编码", "zip", "postal", "postcode"}
	// streetWords after a number make it a house number ("1600 Amphitheatre Parkway").
	streetWords = map[string]bool{"st": true, "street": true, "ave": true, "avenue": true, "rd": true, "road": true,
		"blvd": true, "boulevard": true, "parkway": true, "pkwy": true, "suite": true, "floor": true}
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
```

`excludeReason` 結尾（`orderWords` 迴圈之後、`return ""` 之前）改為：

```go
	ctx := strings.ReplaceAll(string(lower[max(0, c.start-contextRunes):c.start]), " ", "")
	if hasContextWord(ctx, cardWords) {
		return "卡號末碼"
	}
	if hasContextWord(ctx, zipWords) {
		return "郵遞區號"
	}
	return addressReason(c, t)
```

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/recognize`
Expected: PASS（含 18 封語料；`google-code-en` 的頁尾 `1600 … Parkway`、`CA 94043` 現在會被排除，結果仍是 `583920`）。

- [ ] **Step 5: Commit**

```bash
git add internal/recognize
git commit -m "feat(recognize): more code keywords; skip card digits, postcodes and addresses

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: recognize/code — 主旨＋獨立行（B 規則）

**Files:**
- Modify: `internal/recognize/code.go`
- Test: `internal/recognize/code_test.go`

**Interfaces:**
- Produces: `func subjectEcho(t *codeText, digits []span, subjLen int) []span`、`func (t *codeText) addKeyword(k span)`、`subjectOrderWords`。

- [ ] **Step 1: 寫失敗測試**

在 `TestCodeFinder` cases 加入：

```go
		{"subject echo", "Use 123456 to set up recovery email", "Finish setting up:\n123456\nExpires in 24 hours.", "123456"},
		{"subject echo needs own line", "Use 123456 to sign in", "Your number 123456 was used today.", ""},
		{"subject echo order word", "您的訂單已出貨：20261021", "出貨通知\n貨運單號\n20261021\n", ""},
		{"subject echo orders", "Your orders 48213 shipped", "Package\n48213\n", ""},
		{"subject echo year", "Annual report 2026", "Highlights\n2026\n", ""},
		{"subject echo excluded in subject", "Payment $2500 received", "Total\n2500\n", ""},
		{"subject echo excluded in body", "Ref 2500 update", "Total NT$\n2500\n", ""},
```

並新增一個檢查 Trace 的測試：

```go
func TestSubjectEchoTrace(t *testing.T) {
	tr := &Trace{}
	got := newCodeFinder(nil).find(&mailparse.Message{Subject: "Use 123456 now", Text: "x\n123456\n"}, tr)
	if got != "123456" || !strings.Contains(strings.Join(tr.Steps, "\n"), "主旨的 123456 也獨佔內文一行，視為關鍵字") {
		t.Fatalf("got %q trace %q", got, tr.Steps)
	}
}
```

（import 加 `"strings"`。）

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/recognize -run 'TestCodeFinder|TestSubjectEcho'`
Expected: FAIL。

- [ ] **Step 3: 實作**

```go
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
```

`find` 的開頭改為（關鍵字為空時不再立即返回，先套 B 規則；import 加 `"slices"`）：

```go
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
	// … splitRe / alnumRe candidates and the scoring loop as before …
```

注意：`subjLen` 若超過 `len(rs)`（主旨本身超過掃描上限）時 `t.lower[:subjLen]` 會越界——在 `subjectEcho` 開頭加 `subjLen = min(subjLen, len(t.rs))`。

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/recognize`
Expected: PASS（含語料）。

- [ ] **Step 5: Commit**

```bash
git add internal/recognize
git commit -m "feat(recognize): a subject number that stands alone on a body line counts as a code keyword

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 7: recognize/code — 強調加分與純字母驗證碼

**Files:**
- Modify: `internal/recognize/code.go`
- Test: `internal/recognize/code_test.go`

**Interfaces:**
- Consumes: `mailparse.Message.Emphasis`（rune 位置，相對 `Text`）。
- Produces: `codeText.emph []span`、`func emphSpans(msg *mailparse.Message, subjLen, n int) []span`、`func (t *codeText) emphasized(c span) bool`、`func (t *codeText) afterKeyword(pos int) bool`、`alphaRe`。

- [ ] **Step 1: 寫失敗測試**

```go
func TestCodeFinderEmphasisAndAlpha(t *testing.T) {
	cases := []struct {
		name, subject, text string
		emph                []mailparse.Span
		want                string
	}{
		{"emphasis wins", "Notice", "Your verification code: 111111 or 222222", []mailparse.Span{{34, 40}}, "222222"},
		{"no emphasis, nearest wins", "Notice", "Your verification code: 111111 or 222222", nil, "111111"},
		{"alpha own line", "Sign in", "Your code is:\nQWERTY\nIt expires soon.", nil, "QWERTY"},
		{"alpha emphasised inline", "Sign in", "Enter this code: QWERTY to continue", []mailparse.Span{{17, 23}}, "QWERTY"},
		{"alpha inline without emphasis", "Sign in", "Your code is QWERTY and more", nil, ""},
		{"alpha not right after keyword", "Sign in", "Verification code below.\nHELLO\n", nil, ""},
		{"alpha after 為", "登入", "您的驗證碼為\nABCDEF", nil, "ABCDEF"},
		{"emphasis past scan limit is ignored", "Notice", "Your verification code: 111111", []mailparse.Span{{1 << 20, 1<<20 + 6}}, "111111"},
	}
	f := newCodeFinder(nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := &Trace{}
			if got := f.find(&mailparse.Message{Subject: c.subject, Text: c.text, Emphasis: c.emph}, tr); got != c.want {
				t.Errorf("got %q want %q\ntrace:\n%v", got, c.want, tr.Steps)
			}
		})
	}
}
```

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/recognize -run TestCodeFinderEmphasisAndAlpha`
Expected: FAIL。

- [ ] **Step 3: 實作**

```go
// alphaRe finds letter-only codes; they are candidates only right after a keyword and
// alone on their line or emphasised (see find), as plain words look the same.
var alphaRe = regexp.MustCompile(`\b[A-Z]{5,8}\b`)

const emphasisBonus = 15
```

`codeText` 加欄位 `emph []span // emphasised spans of the scanned text, merged and sorted`，並加：

```go
// emphSpans returns msg.Emphasis as spans of the scanned text (Subject+"\n"+Text, cut to n runes).
func emphSpans(msg *mailparse.Message, subjLen, n int) []span {
	var out []span
	for _, e := range msg.Emphasis {
		if s, end := e.Start+subjLen+1, min(e.End+subjLen+1, n); s < end {
			out = append(out, span{s, end})
		}
	}
	return out
}

// emphasized reports whether c lies inside an emphasised span.
func (t *codeText) emphasized(c span) bool {
	i := sort.Search(len(t.emph), func(i int) bool { return t.emph[i].end > c.start })
	return i < len(t.emph) && t.emph[i].start <= c.start && c.end <= t.emph[i].end
}

// keywordEndsAt reports whether a keyword ends exactly at pos.
func (t *codeText) keywordEndsAt(pos int) bool {
	i := sort.Search(len(t.kws), func(i int) bool { return t.kws[i].start >= pos-t.maxKwLen })
	for ; i < len(t.kws) && t.kws[i].start < pos; i++ {
		if t.kws[i].end == pos {
			return true
		}
	}
	return false
}

// afterKeyword reports whether a keyword ends right before pos, with only white space,
// colons and the words "is", 為, 为, 是 in between ("Your code is: QWERTY").
func (t *codeText) afterKeyword(pos int) bool {
	j := pos
	for {
		if t.keywordEndsAt(j) {
			return true
		}
		switch {
		case j == 0:
			return false
		case unicode.IsSpace(t.lower[j-1]) || strings.ContainsRune(":：為为是", t.lower[j-1]):
			j--
		case j >= 2 && t.lower[j-2] == 'i' && t.lower[j-1] == 's' && (j == 2 || !isASCIILetter(t.lower[j-3])):
			j -= 2
		default:
			return false
		}
	}
}
```

`find` 中，建立 `t` 之後加 `t.emph = mergeSpans(emphSpans(msg, subjLen, len(rs)))`；候選收集（`alnumRe` 之後）加：

```go
	for _, sp := range regexpSpans(alphaRe, full, b2r) {
		if t.afterKeyword(sp.start) && (t.aloneOnLine(sp) || t.emphasized(sp)) {
			raw := string(rs[sp.start:sp.end])
			cands = append(cands, codeCand{sp, raw, raw})
		}
	}
```

評分加（`isAllDigits` 那段之後）：

```go
		if t.emphasized(c.span) {
			score += emphasisBonus
		}
```

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/recognize`
Expected: PASS（含語料與 `perf_test.go`）。

- [ ] **Step 5: Commit**

```bash
git add internal/recognize
git commit -m "feat(recognize): favour emphasised codes and accept letter-only codes after a keyword

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 8: recognize/link — 入口、字根、按鈕、泛用連結字、排除

**Files:**
- Modify: `internal/recognize/link.go`
- Test: `internal/recognize/link_test.go`

**Interfaces:**
- Consumes: `mailparse.Link.Button`、`Before`（格式「上一行\n同一行」）。
- Produces: `func urlHint(tail string) bool`、`func genericLinkText(lt string) bool`、`func (f *linkFinder) introducesLink(before string) (string, bool)`。

- [ ] **Step 1: 寫失敗測試**

在 `TestLinkFinder` 的 cases 加入：

```go
		{"verify button on a tracking link", "Welcome", "Verify", []mailparse.Link{
			{URL: "https://u1.ct.sendgrid.net/ls/click?upn=abc", Text: "Verify", Button: true},
		}, "https://u1.ct.sendgrid.net/ls/click?upn=abc"},
		{"button alone is not enough", "Please verify", "x", []mailparse.Link{
			{URL: "https://example.com/blog/post", Text: "Read more", Button: true},
		}, ""},
		{"click here after a pointing line", "Welcome", "Please verify your email by clicking the link below.", []mailparse.Link{
			{URL: "https://example.com/account/email?k=8d1f", Text: "Click here", Before: "Please verify your email by clicking the link below.\n"},
		}, "https://example.com/account/email?k=8d1f"},
		{"click here on the keyword's line", "Welcome", "To verify your email, click here", []mailparse.Link{
			{URL: "https://example.com/a/b", Text: "click here.", Before: "Hi\nTo verify your email,"},
		}, "https://example.com/a/b"},
		{"click here below an unrelated mention", "Account", "Confirm your settings in your account.", []mailparse.Link{
			{URL: "https://example.com/blog/october", Text: "Click here", Before: "We shipped dark mode. Confirm your settings in your account.\n"},
		}, ""},
		{"bare url after a colon line", "Welcome", "Verify your email:\nhttps://example.com/x/y", []mailparse.Link{
			{URL: "https://example.com/x/y", Before: "Verify your email:\n"},
		}, "https://example.com/x/y"},
		{"gate on link text", "Welcome", "Please use an HTML mail client.", []mailparse.Link{
			{URL: "https://x.test/l/1", Text: "驗證"},
		}, "https://x.test/l/1"},
		{"activation stem", "Welcome", "Activate your account to get started.", []mailparse.Link{
			{URL: "https://example.com/account/activation?key=Qm9yZWFsaXNEaWdpdGFsQWJj", Text: "Continue"},
		}, "https://example.com/account/activation?key=Qm9yZWFsaXNEaWdpdGFsQWJj"},
		{"auth path", "Please sign in", "x", []mailparse.Link{
			{URL: "https://example.com/auth?t=Zx8_kq2LmN0pQr7sTuVwXy-1", Text: "Open"},
		}, "https://example.com/auth?t=Zx8_kq2LmN0pQr7sTuVwXy-1"},
		{"author is not auth", "Account verified", "verify", []mailparse.Link{
			{URL: "https://example.com/author/jane?ref=Zx8_kq2LmN0pQr7sTuVwXy-1", Text: "Read more", Button: true},
		}, ""},
		{"pdf excluded", "Please verify", "x", []mailparse.Link{{URL: "https://example.com/verify/guide.pdf", Text: "Verify"}}, ""},
		{"contact path excluded", "Please verify", "x", []mailparse.Link{{URL: "https://example.com/contact?x=1", Text: "Verify"}}, ""},
		{"contact in query kept", "Please verify", "x", []mailparse.Link{
			{URL: "https://example.com/verify?email=contact%40corp.com", Text: "Verify email"},
		}, "https://example.com/verify?email=contact%40corp.com"},
		{"view in browser excluded", "Please verify", "x", []mailparse.Link{
			{URL: "https://example.com/v/verify-123", Text: "View in browser"},
			{URL: "https://example.com/verify?t=1", Text: "Verify"},
		}, "https://example.com/verify?t=1"},
```

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/recognize -run TestLinkFinder`
Expected: FAIL。

- [ ] **Step 3: 實作**

```go
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
```

`find` 的入口與評分改為：

```go
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
	…（不變）
	for _, l := range msg.Links {
		…（解析與排除不變）
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
		…（其餘不變）
```

`linkExcludeReason` 改為：

```go
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
```

刪除不再使用的 `imageExtRe`。

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/recognize`
Expected: PASS（含語料）。若某封既有語料多出連結結果（例如 `microsoft-security-code-en` 變成 code+link），停下來回報，不要改預期。

- [ ] **Step 5: Commit**

```bash
git add internal/recognize
git commit -m "feat(recognize): read button links, generic link text and URL stems; skip more boilerplate links

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 9: 測試語料

**Files:**
- Modify: `testdata/eml/gen.py`
- Create（由 gen.py 產生）：`testdata/eml/*.eml`、`*.expected.yaml`
- Modify: `docs/superpowers/specs/2026-10-21-recognition-v2-design.md` §6.1 的 `footer-zip-en` 描述

- [ ] **Step 1: 在 `gen.py` 的 `print("ok")` 前加入新語料**

```python
# Recognition v2: wordings without a classic keyword, button-only links, HTML-only
# content, letter codes, and the distractors that must stay unforwarded.
write("google-recovery-en",
      [("From", "Google <noreply@google.com>"), ("To", "bar@outlook.com"), ("Subject", "Use 123456 to set up recovery email"), DATE],
      [("text/plain", "utf-8", "foo@gmail.com wants to use your email address as their recovery email. Use this code to finish setting up bar@outlook.com as a recovery email:\n123456\n\nThis code will expire in 24 hours.\n\nIf you don’t recognize foo@gmail.com, you can remove your email address from their account. Remove email\n")],
      'code: "123456"')

write("verify-button-zh-tw",
      [("From", word("範例服務", "utf-8") + " <noreply@service.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("歡迎加入範例服務", "utf-8")), DATE],
      [("text/html", "utf-8", "<p>感謝您註冊範例服務。</p><table><tr><td bgcolor=\"#1a73e8\"><a href=\"https://u123.ct.sendgrid.net/ls/click?upn=Tw3bQ9xYz\">驗證</a></td></tr></table><p><a href=\"https://service.example.com.tw/help\">說明中心</a></p>")],
      'url: "https://u123.ct.sendgrid.net/ls/click?upn=Tw3bQ9xYz"')

write("verify-button-zh-cn",
      [("From", word("示例服务", "utf-8") + " <noreply@example.cn>"), ("To", "me@example.com"), ("Subject", word("欢迎加入示例服务", "utf-8")), DATE],
      [("text/html", "utf-8", "<p>感谢您注册示例服务。</p><p><a class=\"btn\" href=\"https://email.example.cn/c/eJxVj0sOgjAQhp9\">验证</a></p>")],
      'url: "https://email.example.cn/c/eJxVj0sOgjAQhp9"')

write("verify-button-en",
      [("From", "Example <hello@example.com>"), ("To", "me@gmail.com"), ("Subject", "Welcome to Example"), DATE],
      [("text/plain", "utf-8", "Welcome to Example! Open this email in an HTML-capable client to continue.\n"),
       ("text/html", "utf-8", "<h1>Welcome to Example!</h1><p><a href=\"https://links.example.net/ls/click?upn=VfY8a2\" style=\"background-color:#111;color:#fff;padding:12px 20px\">Verify</a></p><p><a href=\"https://example.com/privacy\">Privacy</a></p>")],
      'url: "https://links.example.net/ls/click?upn=VfY8a2"')

write("image-button-en",
      [("From", "Example <hello@example.com>"), ("To", "me@gmail.com"), ("Subject", "One more step"), DATE],
      [("text/html", "utf-8", "<p>Thanks for signing up! Please confirm your email address.</p><a href=\"https://t.example.com/r/42\"><img src=\"https://cdn.example.com/btn-verify.png\" alt=\"Verify email\"></a><p><a href=\"https://example.com/unsubscribe\">Unsubscribe</a></p>")],
      'url: "https://t.example.com/r/42"')

write("click-here-en",
      [("From", "Example <no-reply@example.com>"), ("To", "me@gmail.com"), ("Subject", "Welcome to Example"), DATE],
      [("text/html", "utf-8", "<p>Hi,</p><p>Please verify your email by clicking the link below.</p><p><a href=\"https://example.com/account/email?k=8d1f\">Click here</a></p><p>Thanks,<br>Example</p>")],
      'url: "https://example.com/account/email?k=8d1f"')

write("html-only-useful-en",
      [("From", "Example <security@example.com>"), ("To", "me@gmail.com"), ("Subject", "Your Example account"), DATE],
      [("text/plain", "utf-8", "This message requires an HTML-capable email client.\n"),
       ("text/html", "utf-8", "<p>Your sign-in code</p><p><strong style=\"font-size:28px;letter-spacing:6px\">408217</strong></p><p>It expires in 10 minutes.</p>")],
      'code: "408217"')

write("alpha-code-en",
      [("From", "Example <security@example.com>"), ("To", "me@gmail.com"), ("Subject", "Sign in to Example"), DATE],
      [("text/html", "utf-8", "<p>Your code is:</p><p><b>QWERTY</b></p><p>It expires in 10 minutes.</p>")],
      'code: "QWERTY"')

write("activation-url-en",
      [("From", "Example <hello@example.com>"), ("To", "me@gmail.com"), ("Subject", "Your account is almost ready"), DATE],
      [("text/html", "utf-8", "<p>Thanks for joining Example. Activate your account to get started.</p><a href=\"https://example.com/account/activation?key=Qm9yZWFsaXNEaWdpdGFsQWJj\">Continue</a>")],
      'url: "https://example.com/account/activation?key=Qm9yZWFsaXNEaWdpdGFsQWJj"')

write("promo-code-en",
      [("From", "Example Shop <deals@shop.example.com>"), ("To", "me@gmail.com"), ("Subject", "20% off this weekend"), DATE],
      [("text/plain", "utf-8", "Use promo code SAVE20 at checkout.\nYour discount code: 8888\nShop now: https://shop.example.com/sale\n")],
      "none: true")

write("order-subject-zh",
      [("From", word("範例購物", "utf-8") + " <order@shop.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("您的訂單已出貨：20261021", "utf-8")), DATE],
      [("text/plain", "utf-8", "出貨通知\n貨運單號\n20261021\n感謝您的購買。\n")],
      "none: true")

write("card-last4-zh",
      [("From", word("範例銀行", "utf-8") + " <notify@bank.example.com.tw>"), ("To", "me@example.com"), ("Subject", word("信用卡消費通知", "utf-8")), DATE],
      [("text/plain", "utf-8", "您末四碼 1234 的信用卡於 10/19 消費 NT$ 520。\n請勿將驗證碼告訴他人。\n")],
      "none: true")

write("newsletter-click-here-en",
      [("From", "Example <news@example.com>"), ("To", "me@gmail.com"), ("Subject", "New in October"), DATE],
      [("text/html", "utf-8", "<h2>New in October</h2><p>We shipped dark mode and faster sync. Confirm your settings in your account.</p><p><a href=\"https://example.com/blog/october\">Click here</a> to read more.</p><p><a href=\"https://example.com/unsubscribe\">Unsubscribe</a></p>")],
      "none: true")

write("author-link-en",
      [("From", "Example Blog <blog@example.com>"), ("To", "me@gmail.com"), ("Subject", "Meet our authors"), DATE],
      [("text/html", "utf-8", "<p>Every author on our blog is verified — verify your own profile any time.</p><p><a href=\"https://blog.example.com/author/jane\" style=\"background:#0a7;padding:10px\">Read more</a></p>")],
      "none: true")

write("footer-zip-en",
      [("From", "Example <security@example.com>"), ("To", "me@gmail.com"), ("Subject", "Security tip"), DATE],
      [("text/html", "utf-8", "<p>We will never ask for your verification code by phone or email.</p><p>Example Inc., 1600 Main Street, Mountain View, CA 94043</p>")],
      "none: true")
```

- [ ] **Step 2: 產生並執行語料測試**

Run: `python3 testdata/eml/gen.py && go test -race -count=1 ./internal/recognize -run TestCorpus -v 2>&1 | tail -50`
Expected: 全部 PASS（共 33 封）。若新語料失敗：先讀 Trace 找原因；屬於實作缺陷就修實作（附單元測試）；若是規格未涵蓋的情況，停下來回報。既有 18 封的 `.eml` 與 `.expected.yaml` 不得有 diff（`git diff --stat testdata/eml` 只應出現 gen.py 與新檔）。

- [ ] **Step 3: 更新規格 §6.1**

把 `footer-zip-en` 一列的描述改為：「安全提醒信「We will never ask for your verification code」，頁尾 `Example Inc., 1600 Main Street, Mountain View, CA 94043`，沒有驗證連結」。

- [ ] **Step 4: Commit**

```bash
git add testdata/eml docs/superpowers/specs/2026-10-21-recognition-v2-design.md
git commit -m "test(recognize): corpus for new wordings, button links and distractors

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 10: pipeline.RunTrial 與 CLI

**Files:**
- Create: `internal/pipeline/trial.go`
- Modify: `internal/pipeline/pipeline.go`
- Modify: `cmd/relay/testcmd.go`
- Test: `internal/pipeline/trial_test.go`

**Interfaces:**
- Produces:

```go
type Trial struct {
	Msg    *mailparse.Message
	Result *recognize.Result // nil when nothing matched
	Kind   string            // code | link | both | ignored | none
	Trace  *recognize.Trace
	Output *render.Rendered // the Telegram message; nil unless Kind is code, link or both
}
func RunTrial(raw []byte, rec recognize.Recognizer, rnd Renderer, mb Mailbox) (*Trial, error)
```

`RunTrial` 只用 `mb.Name`（範本）、`mb.DisplayName`、`mb.Username`。解析失敗回 `nil, err`；渲染失敗回「已填好前面欄位的 `*Trial`」與 err。

- [ ] **Step 1: 寫失敗測試**

```go
package pipeline

import (
	"strings"
	"testing"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/recognize"
	"verify-code-relay/internal/render"
)

func trialDeps(t *testing.T, rules []config.Rule) (*recognize.Chain, *render.Renderer) {
	t.Helper()
	rec, err := recognize.New(rules, config.RecognizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rnd, err := render.New(config.Empty().Defaulted())
	if err != nil {
		t.Fatal(err)
	}
	return rec, rnd
}

const trialMail = "From: Google <noreply@google.com>\r\nTo: bar@outlook.com\r\nSubject: Use 123456 to set up recovery email\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nUse this code to finish:\r\n123456\r\n"

func TestRunTrialCode(t *testing.T) {
	rec, rnd := trialDeps(t, nil)
	tr, err := RunTrial([]byte(trialMail), rec, rnd, Mailbox{DisplayName: "（測試）"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Kind != "code" || tr.Result.Code != "123456" || tr.Output == nil || !strings.Contains(tr.Output.Text, "（測試）") {
		t.Fatalf("trial = %+v output = %+v", tr, tr.Output)
	}
	if len(tr.Trace.Steps) == 0 || tr.Msg.Subject != "Use 123456 to set up recovery email" {
		t.Fatalf("trace %q subject %q", tr.Trace.Steps, tr.Msg.Subject)
	}
}

func TestRunTrialIgnoredAndNone(t *testing.T) {
	rec, rnd := trialDeps(t, []config.Rule{{Name: "g", From: "*@google.com", Ignore: true}})
	tr, err := RunTrial([]byte(trialMail), rec, rnd, Mailbox{})
	if err != nil || tr.Kind != "ignored" || tr.Output != nil || tr.Result.Rule != "custom:g" {
		t.Fatalf("ignored: %+v %v", tr, err)
	}
	rec, rnd = trialDeps(t, nil)
	tr, err = RunTrial([]byte("From: a@b.c\r\nSubject: hi\r\n\r\nhello\r\n"), rec, rnd, Mailbox{})
	if err != nil || tr.Kind != "none" || tr.Output != nil || tr.Result != nil {
		t.Fatalf("none: %+v %v", tr, err)
	}
}

func TestRunTrialParseError(t *testing.T) {
	rec, rnd := trialDeps(t, nil)
	if tr, err := RunTrial([]byte("Content-Transfer-Encoding: bogus\r\n\r\nx"), rec, rnd, Mailbox{}); err == nil || tr != nil {
		t.Fatalf("want a parse error, got %+v %v", tr, err)
	}
}
```

若 `config.Empty().Defaulted()` 無法直接 `render.New`，改用 `runtime` 測試中建立 renderer 的方式（看 `internal/render/render_test.go` 的 helper）；目的只是拿到使用內建範本的 renderer。若 `Content-Transfer-Encoding: bogus` 在現行 `mailparse.Parse` 不回錯誤，改用 `parse_test.go` 中既有、會讓 `Parse` 回錯誤的輸入。

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/pipeline -run TestRunTrial`
Expected: 編譯失敗。

- [ ] **Step 3: 實作**

`internal/pipeline/trial.go`：

```go
package pipeline

import (
	"fmt"

	"verify-code-relay/internal/mailparse"
	"verify-code-relay/internal/recognize"
	"verify-code-relay/internal/render"
)

// Trial is what one message would do, worked out without storing or sending anything;
// `relay test` and the admin's mail test show it. The trace holds codes and links: it
// must never be logged.
type Trial struct {
	Msg    *mailparse.Message
	Result *recognize.Result // nil when nothing matched
	Kind   string            // code | link | both | ignored | none
	Trace  *recognize.Trace
	Output *render.Rendered // the Telegram message; nil unless Kind is code, link or both
}

// RunTrial parses raw and runs it through rec and rnd as Process would for mailbox mb
// (only its Name, DisplayName and Username are used). A parse error returns no Trial; a
// template error returns the Trial so far.
func RunTrial(raw []byte, rec recognize.Recognizer, rnd Renderer, mb Mailbox) (*Trial, error) {
	msg, err := mailparse.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("解析信件：%w", err)
	}
	res, ignore, tr := rec.Recognize(msg)
	t := &Trial{Msg: msg, Result: res, Kind: kindOf(res, ignore), Trace: tr}
	if t.Kind == "ignored" || t.Kind == "none" {
		return t, nil
	}
	out, err := rnd.Render(mb.Name, renderData(msg, res, mb))
	if err != nil {
		return t, fmt.Errorf("套用範本：%w", err)
	}
	t.Output = &out
	return t, nil
}

// kindOf names what a recognition outcome forwards.
func kindOf(res *recognize.Result, ignore bool) string {
	switch {
	case ignore:
		return "ignored"
	case res == nil:
		return "none"
	case res.Code != "" && res.URL != "":
		return "both"
	case res.Code != "":
		return "code"
	}
	return "link"
}

// renderData is what the templates see for msg, recognised as res, in mailbox mb.
func renderData(msg *mailparse.Message, res *recognize.Result, mb Mailbox) render.Data {
	return render.Data{
		Code: res.Code, URL: res.URL, Service: res.Service, FromName: msg.From.Name, FromAddr: msg.From.Addr,
		To: Recipient(msg.To, mb.Username), Subject: msg.Subject, Mailbox: mb.DisplayName, Rule: res.Rule, Date: msg.Date,
	}
}
```

`pipeline.go` 的 `Process`：辨識後改為

```go
	res, ignore, _ := p.Recognizer.Recognize(msg) // the trace may contain codes: never log it
	entry.Kind = kindOf(res, ignore)
	if ignore && res != nil {
		entry.Rule = res.Rule
	}
	if ignore || res == nil {
		… (unchanged)
	}

	entry.Rule = res.Rule
	// Codes must never be stored or logged; the Telegram text below keeps the real subject.
	entry.Subject = redact(entry.Subject, res.Code)
	out, err := p.Renderer.Render(mb.Name, renderData(msg, res, mb))
```

（刪除原本兩段設定 `entry.Kind` 的 switch。）

`cmd/relay/testcmd.go`：讀檔後改為

```go
	t, err := pipeline.RunTrial(raw, a.Recognizer, a.Renderer, pipeline.Mailbox{Name: *mailbox, DisplayName: displayName, Username: username})
	if t == nil {
		return fail(stderr, err)
	}
	msg := t.Msg
	fmt.Fprintf(stdout, "寄件者：%s <%s>\n主旨：%s\n", msg.From.Name, msg.From.Addr, msg.Subject)
	for _, w := range msg.Warnings {
		fmt.Fprintf(stdout, "警告：%s\n", w)
	}
	fmt.Fprintf(stdout, "── 內文（前 500 字）──\n%s\n", truncateRunes(msg.Text, 500))
	fmt.Fprintf(stdout, "── 連結 %d 個 ──\n", len(msg.Links))
	for _, l := range msg.Links {
		fmt.Fprintf(stdout, "  %s  [%s]\n", l.URL, l.Text)
	}
	fmt.Fprintln(stdout, "── 辨識過程 ──")
	for _, s := range t.Trace.Steps {
		fmt.Fprintln(stdout, "  "+s)
	}
	fmt.Fprintln(stdout, "── 結果 ──")
	switch t.Kind {
	case "ignored":
		fmt.Fprintf(stdout, "被規則 %s 明確略過，不轉發\n", t.Result.Rule)
		return 0
	case "none":
		fmt.Fprintln(stdout, "不符合任何規則，不轉發")
		return 0
	}
	res := t.Result
	fmt.Fprintf(stdout, "規則：%s\n驗證碼：%s\n連結：%s\n服務：%s\n", res.Rule, res.Code, res.URL, res.Service)
	if err != nil {
		return fail(stderr, err)
	}
	out := t.Output
	fmt.Fprintf(stdout, "── Telegram 訊息 ──\n%s\n── 按鈕 ──\n%s\n", out.Text, out.ReplyMarkup)
	… (--send 段落不變，使用 out)
```

注意 `ignored` 時 `t.Result` 可能為 nil（stage 回 `nil, true`）：沿用原本「`rule := ""; if res != nil { rule = res.Rule }`」的寫法，不要直接解參考。移除 testcmd.go 不再使用的 import（`mailparse`、`render`）。若 `cmd/relay` 的測試斷言舊標籤「內文（純文字，前 500 字）」，同步更新。

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/pipeline ./cmd/relay`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/pipeline cmd/relay
git commit -m "refactor(pipeline): share parse-recognise-render between relay test and the admin

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 11: Web API `POST /api/rules/test`

**Files:**
- Create: `internal/web/mailtest.go`
- Modify: `internal/web/middleware.go`（本文上限）
- Modify: `internal/web/routes.go`
- Modify: `internal/web/page_rules.go`（`rulesData.Mailboxes`）
- Test: `internal/web/mailtest_test.go`

**Interfaces:**
- Consumes: `pipeline.RunTrial`、`readRules`、`readKeywords`、`checkRules`、`rulesErrors`、`fieldErrors`、`runtime.LoadConfig`、`runtime.Build`、`sanitize`、`previewButtons`、`tglen`、`truncate`。
- Produces:
  - 路由 `POST /api/rules/test`（`authed`），multipart 欄位：`eml`（檔案）或 `eml_text`、`mailbox`、`rule_*`、`extra_code_keywords`、`extra_link_keywords`。
  - 回應見規格 §5.2。成功：`{"ok":true,"message":{"from","subject","warnings","text","links":[{"url","text"}]},"trace":[…],"result":{"kind","code","url","service","rule"},"preview":{"html","buttons","length","max","unsupported"}}`（`preview` 僅 kind 為 code／link／both 時；範本錯誤時改附 `"preview_error"`）。失敗：`{"ok":false,"error":"…","errors":{…}}`（`errors` 僅規則錯誤時）。
  - `rulesData.Mailboxes []mailboxOption`，`type mailboxOption struct{ Name, Label string }`（Label 為顯示名稱，無則名稱），依名稱排序。

- [ ] **Step 1: 寫失敗測試**

`internal/web/mailtest_test.go`：

```go
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"verify-code-relay/internal/runtime"
)

const recoveryMail = "From: Google <noreply@google.com>\r\nTo: bar@outlook.com\r\nSubject: Use 123456 to set up recovery email\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n\r\nUse this code to finish:\r\n123456\r\n"

type mailTestAnswer struct {
	OK      bool
	Error   string
	Errors  map[string]string
	Message struct {
		From, Subject, Text string
		Warnings            []string
		Links               []struct{ URL, Text string }
	}
	Trace  []string
	Result struct{ Kind, Code, URL, Service, Rule string }
	Preview *struct {
		HTML    string
		Buttons []previewButton
		Length  int
		Max     int
	}
}

// postMultipart posts fields and, when eml is not nil, an "eml" file part, with the
// X-CSRF-Token header.
func (e *env) postMultipart(path string, fields url.Values, eml []byte) (*http.Response, string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, vs := range fields {
		for _, v := range vs {
			if err := mw.WriteField(k, v); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	if eml != nil {
		fw, err := mw.CreateFormFile("eml", "mail.eml")
		if err != nil {
			e.t.Fatal(err)
		}
		fw.Write(eml)
	}
	mw.Close()
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	return e.do(req)
}

func (e *env) mailTest(fields url.Values, eml []byte) mailTestAnswer {
	e.t.Helper()
	resp, body := e.postMultipart("/api/rules/test", fields, eml)
	var a mailTestAnswer
	if resp.StatusCode != 200 || json.Unmarshal([]byte(body), &a) != nil {
		e.t.Fatalf("= %d %s", resp.StatusCode, body)
	}
	return a
}

func TestMailTestRecognisesCode(t *testing.T) {
	var logs syncBuf
	e := newEnv(t, nil, func(o *Options) { o.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	e.login()
	a := e.mailTest(nil, []byte(recoveryMail))
	if !a.OK || a.Result.Kind != "code" || a.Result.Code != "123456" || a.Result.Service != "Google" {
		t.Fatalf("answer = %+v", a)
	}
	if a.Preview == nil || !strings.Contains(a.Preview.HTML, "123456") || !strings.Contains(a.Preview.HTML, "（測試）") {
		t.Fatalf("preview = %+v", a.Preview)
	}
	if len(a.Trace) == 0 || a.Message.Subject != "Use 123456 to set up recovery email" || !strings.Contains(a.Message.From, "noreply@google.com") {
		t.Fatalf("trace %q message %+v", a.Trace, a.Message)
	}
	if strings.Contains(logs.String(), "123456") {
		t.Fatalf("the code reached the log:\n%s", logs.String())
	}
}

func TestMailTestUsesUnsavedRules(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	a := e.mailTest(url.Values{
		"rule_name": {"g"}, "rule_from": {"*@google.com"}, "rule_subject": {""},
		"rule_code_regex": {""}, "rule_link_regex": {""}, "rule_ignore": {"1"},
	}, []byte(recoveryMail))
	if !a.OK || a.Result.Kind != "ignored" || a.Result.Rule != "custom:g" || a.Preview != nil {
		t.Fatalf("answer = %+v", a)
	}
	raw, _, _ := runtime.LoadConfig(context.Background(), e.st)
	if len(raw.Rules) != 0 {
		t.Fatalf("the test saved rules: %+v", raw.Rules)
	}
	mail := "From: a@shop.test\r\nSubject: hi\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n驗證號碼 482913\r\n"
	if a := e.mailTest(nil, []byte(mail)); a.Result.Kind != "none" {
		t.Fatalf("without the keyword: %+v", a.Result)
	}
	if a := e.mailTest(url.Values{"extra_code_keywords": {"驗證號碼"}}, []byte(mail)); a.Result.Code != "482913" {
		t.Fatalf("with the keyword: %+v", a.Result)
	}
}

func TestMailTestRuleErrors(t *testing.T) {
	e := newEnv(t, nil)
	e.login()
	a := e.mailTest(url.Values{
		"rule_name": {"bad"}, "rule_from": {"*@x.test"}, "rule_subject": {""},
		"rule_code_regex": {"("}, "rule_link_regex": {""}, "rule_ignore": {"0"},
	}, []byte(recoveryMail))
	if a.OK || !strings.Contains(a.Errors["rules[0].code_regex"], "正規表示式錯誤") {
		t.Fatalf("answer = %+v", a)
	}
}

func TestMailTestInputs(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	pasted := "\n" + strings.ReplaceAll(recoveryMail, "\r\n", "\n")
	if a := e.mailTest(url.Values{"eml_text": {pasted}}, nil); !a.OK || a.Result.Code != "123456" {
		t.Fatalf("pasted: %+v", a)
	}
	if a := e.mailTest(nil, nil); a.OK || !strings.Contains(a.Error, "請上傳") {
		t.Fatalf("empty: %+v", a)
	}
	big := append([]byte(recoveryMail), bytes.Repeat([]byte("x"), 5<<20)...)
	if a := e.mailTest(nil, big); a.OK || !strings.Contains(a.Error, "5 MiB") {
		t.Fatalf("too big: %+v", a.Error)
	}
	// Over the 6 MiB body cap the server stops reading and closes the connection, which a
	// real client may see as a write error: serve this one in memory instead.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("eml", "big.eml")
	fw.Write(bytes.Repeat([]byte("x"), 7<<20))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, e.ts.URL+"/api/rules/test", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", e.csrf)
	req.Header.Set("Origin", e.ts.URL)
	req.Header.Set("Cookie", sessionCookie+"="+e.sessionCookie())
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "5 MiB") {
		t.Fatalf("over the body cap: %d %s", rec.Code, rec.Body.String())
	}
	if a := e.mailTest(url.Values{"mailbox": {"gmail"}}, []byte(recoveryMail)); a.Preview == nil || !strings.Contains(a.Preview.HTML, "個人 Gmail") {
		t.Fatalf("mailbox preview: %+v", a.Preview)
	}
	if a := e.mailTest(url.Values{"mailbox": {"nope"}}, []byte(recoveryMail)); a.OK || !strings.Contains(a.Error, "nope") {
		t.Fatalf("unknown mailbox: %+v", a)
	}
}

func TestMailTestNeedsLoginAndSameOrigin(t *testing.T) {
	e := newEnv(t, nil)
	if resp, _ := e.postMultipart("/api/rules/test", nil, []byte(recoveryMail)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logged out = %d", resp.StatusCode)
	}
	e.login()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("eml_text", recoveryMail)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/rules/test", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", e.csrf)
	req.Header.Set("Origin", "https://evil.test")
	if resp, _ := e.do(req); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin = %d", resp.StatusCode)
	}
}

func TestRulesPageListsMailboxes(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	_, body := e.get("/rules")
	if !strings.Contains(body, `value="gmail"`) || !strings.Contains(body, "個人 Gmail") {
		t.Fatalf("rules page lacks the mailbox option")
	}
}
```

（若 `syncBuf` 的寫法或 `newEnv` 的 opts 簽名與上面不同，照 `web_test.go` 的實際 helper 調整。`TestRulesPageListsMailboxes` 在 Task 12 加入面板後才會通過——本任務先把選項放進 `rulesData`，並在 rules.html 暫時加最小的 `<select name="mailbox">`；Task 12 會改寫成正式介面。）

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/web -run 'TestMailTest|TestRulesPageListsMailboxes'`
Expected: FAIL（404）。

- [ ] **Step 3: 實作**

`middleware.go`：

```go
// maxBody caps request bodies; no form or JSON call in the admin comes near it.
const maxBody = 1 << 20

// bodyLimit is the body cap of a request: the mail test takes a whole message.
func bodyLimit(r *http.Request) int64 {
	if r.URL.Path == mailTestPath {
		return maxMailTestBody
	}
	return maxBody
}
```

並把 `checkOrigin` 中的 `http.MaxBytesReader(w, r.Body, maxBody)` 改為 `http.MaxBytesReader(w, r.Body, bodyLimit(r))`。

`routes.go`（`/rules` 兩行之後）：

```go
	m.HandleFunc("POST "+mailTestPath, s.authed(s.postAPIRulesTest))
```

`internal/web/mailtest.go`：

```go
package web

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"verify-code-relay/internal/config"
	"verify-code-relay/internal/pipeline"
	"verify-code-relay/internal/runtime"
	"verify-code-relay/internal/tglen"
)

// Mail test: POST /api/rules/test (multipart/form-data) runs one message — an "eml" file
// or pasted "eml_text" — through the rules and keywords posted with it (the rules page
// as currently filled in, saved or not), optionally rendered for "mailbox", and answers
//
//	{"ok":true,"message":{"from","subject","warnings","text","links":[{"url","text"}]},
//	 "trace":[…],"result":{"kind","code","url","service","rule"},
//	 "preview":{"html","buttons","length","max","unsupported"}}
//
// or {"ok":false,"error":"…","errors":{path:message}}. The message stays in memory: the
// body cap (maxMailTestBody) is below the ParseMultipartForm memory limit
// (mailTestMemory), so no part spills to a temporary file. Nothing is stored or logged —
// the trace holds codes and links.

const (
	mailTestPath    = "/api/rules/test"
	maxMailTestBody = 6 << 20
	mailTestMemory  = 32 << 20 // must stay above maxMailTestBody
	mailTestText    = 500      // runes of the body shown

	msgMailTooBig  = "信件超過 5 MiB，實際運作時也會略過不辨識"
	msgMailMissing = "請上傳 .eml 檔，或貼上信件原始碼"
)

func (s *Server) postAPIRulesTest(w http.ResponseWriter, r *http.Request) {
	bad := func(msg string) { s.json(w, http.StatusOK, map[string]any{"ok": false, "error": msg}) }
	if err := r.ParseMultipartForm(mailTestMemory); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			bad(msgMailTooBig)
			return
		}
		s.fail(w, r, http.StatusBadRequest, "請求內容不是有效的表單")
		return
	}
	raw, err := mailTestSource(r)
	switch {
	case err != nil:
		s.fail(w, r, http.StatusBadRequest, "無法讀取上傳的檔案")
		return
	case len(raw) == 0:
		bad(msgMailMissing)
		return
	case len(raw) > pipeline.MaxMessageSize:
		bad(msgMailTooBig)
		return
	}

	rules := readRules(r)
	trial, _, err := runtime.LoadConfig(r.Context(), s.o.Store)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	trial.Rules = rules
	trial.Recognize = config.RecognizeOptions{
		ExtraCodeKeywords: readKeywords(r.PostFormValue("extra_code_keywords")),
		ExtraLinkKeywords: readKeywords(r.PostFormValue("extra_link_keywords")),
	}
	b, err := runtime.Build(trial)
	var built FieldErrors
	if err != nil {
		built = fieldErrors(err)
	}
	if fe := rulesErrors(checkRules(rules), built); len(fe) > 0 {
		s.json(w, http.StatusOK, map[string]any{"ok": false, "error": "規則有錯誤，請先修正再測試", "errors": fe})
		return
	}

	mb := pipeline.Mailbox{DisplayName: "（測試）"}
	if name := r.PostFormValue("mailbox"); name != "" {
		m, ok := b.Config.Mailboxes[name]
		if !ok {
			bad("找不到信箱 " + name + "，可能已被刪除；請重新整理頁面")
			return
		}
		mb = pipeline.Mailbox{Name: name, DisplayName: m.DisplayName, Username: m.Username}
	}
	t, err := pipeline.RunTrial(raw, b.Recognizer, b.Renderer, mb)
	if t == nil {
		bad(err.Error())
		return
	}
	s.json(w, http.StatusOK, mailTestAnswer(t, err))
}

// mailTestSource returns the uploaded file, else the pasted source with its line ends
// made CRLF (a browser textarea gives LF or CRLF; leading blank lines are dropped).
func mailTestSource(r *http.Request) ([]byte, error) {
	if f, _, err := r.FormFile("eml"); err == nil {
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, pipeline.MaxMessageSize+1))
	} else if !errors.Is(err, http.ErrMissingFile) {
		return nil, err
	}
	src := strings.TrimLeft(strings.ReplaceAll(r.PostFormValue("eml_text"), "\r\n", "\n"), "\n\r\t ")
	if src == "" {
		return nil, nil
	}
	return []byte(strings.ReplaceAll(src, "\n", "\r\n")), nil
}

func mailTestAnswer(t *pipeline.Trial, renderErr error) map[string]any {
	type link struct {
		URL  string `json:"url"`
		Text string `json:"text"`
	}
	links := []link{}
	for _, l := range t.Msg.Links {
		links = append(links, link{l.URL, l.Text})
	}
	from := t.Msg.From.Addr
	if t.Msg.From.Name != "" {
		from = t.Msg.From.Name + " <" + t.Msg.From.Addr + ">"
	}
	result := map[string]any{"kind": t.Kind}
	if t.Result != nil {
		result["code"], result["url"], result["service"], result["rule"] = t.Result.Code, t.Result.URL, t.Result.Service, t.Result.Rule
	}
	out := map[string]any{
		"ok": true,
		"message": map[string]any{
			"from": from, "subject": t.Msg.Subject, "warnings": append([]string{}, t.Msg.Warnings...),
			"text": truncate(t.Msg.Text, mailTestText), "links": links,
		},
		"trace":  append([]string{}, t.Trace.Steps...),
		"result": result,
	}
	switch {
	case t.Output != nil:
		safe, unsupported := sanitize(t.Output.Text)
		out["preview"] = map[string]any{"html": safe, "buttons": previewButtons(t.Output.ReplyMarkup),
			"length": tglen.Len(t.Output.Text), "max": tglen.Max, "unsupported": unsupported}
	case renderErr != nil:
		out["preview_error"] = renderErr.Error()
	}
	return out
}
```

`page_rules.go`：

```go
// mailboxOption is a mailbox the mail test can preview the message for.
type mailboxOption struct{ Name, Label string }

type rulesData struct {
	Rules        []config.Rule
	NewRule      config.Rule // the blank row the page adds from
	CodeKeywords string      // one per line
	LinkKeywords string
	Mailboxes    []mailboxOption // for the mail test, by name
}
```

`renderRules` 在呼叫 `s.render` 前填入（`getRules` 與 `postRules` 的錯誤回填都走這裡）：

```go
func (s *Server) renderRules(w http.ResponseWriter, r *http.Request, status int, errs FieldErrors, d rulesData) {
	if raw, _, err := runtime.LoadConfig(r.Context(), s.o.Store); err == nil {
		for _, name := range slices.Sorted(maps.Keys(raw.Mailboxes)) {
			label := raw.Mailboxes[name].DisplayName
			if label == "" {
				label = name
			}
			d.Mailboxes = append(d.Mailboxes, mailboxOption{name, label})
		}
	} else {
		s.o.Log.Warn("讀取信箱清單失敗", "err", err)
	}
	s.render(w, r, status, "rules", Page{Title: "辨識規則", Nav: "rules", Errors: errs, Data: d})
}
```

（import 加 `"maps"`、`"slices"`。）`rules.html` 在 `</form>` 後暫時加：

```html
<select name="mailbox" form="mailtest" hidden>{{range .Data.Mailboxes}}<option value="{{.Name}}">{{.Label}}</option>{{end}}</select>
```

（Task 12 會以正式面板取代這行。）

- [ ] **Step 4: 執行確認通過**

Run: `go test -race -count=1 ./internal/web`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat(web): POST /api/rules/test runs a message through the rules as filled in

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 12: Web 測試面板（frontend-design）

**Files:**
- Modify: `internal/web/templates/rules.html`
- Modify: `internal/web/static/app.js`
- Modify: `internal/web/static/app.css`
- Test: `internal/web/rules_test.go`

**Interfaces:**
- Consumes: Task 11 的 API 與 `rulesData.Mailboxes`；`app.js` 既有的 `fetchJSON`、`el`、`icon`、`toast`、`showSafeHTML`、`ready`、`byId`。
- Produces: `relay.fetchJSON(url, body)` 接受 `FormData`（不設 Content-Type、不 JSON 編碼）。

- [ ] **Step 1: 叫用 frontend-design skill**

先叫用 frontend-design skill，閱讀 `app.css` 開頭的設計說明與既有元件（`.card`、`.field`、`.btn`、`.tg-phone` 等），沿用同一套視覺語言設計面板。以下是**必須具備**的結構與 hook；視覺細節、排版、動效由 frontend-design 決定。

- [ ] **Step 2: 寫失敗測試**

`rules_test.go` 加：

```go
func TestRulesPageHasMailTest(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	_, body := e.get("/rules")
	for _, want := range []string{`data-mailtest`, `name="eml"`, `name="eml_text"`, `name="mailbox"`, `value="gmail"`, `data-mt-out`, `尚未儲存也會套用`} {
		if !strings.Contains(body, want) {
			t.Errorf("rules page lacks %q", want)
		}
	}
	if strings.Contains(body, " style=") || strings.Contains(body, "<script>") {
		t.Errorf("inline style or script breaks the CSP")
	}
}
```

Run: `go test ./internal/web -run TestRulesPageHasMailTest` → FAIL。

- [ ] **Step 3: 範本**

在 `rules.html` 的 `</form>` 之後（取代 Task 11 的暫時 `<select>`）加入獨立的表單（**不可**放在 `data-rules-form` 裡，以免儲存規則時送出信件）：

```html
<form class="card mailtest" id="mailtest" data-mailtest novalidate aria-labelledby="mt-title">
  <header class="card-head">
    <div>
      <h2 class="card-title" id="mt-title">用信件測試</h2>
      <p class="card-desc">上傳一封信，看它會被怎麼辨識。使用本頁目前填寫的規則與關鍵字（尚未儲存也會套用）；不會儲存信件，也不會發送到 Telegram。</p>
    </div>
  </header>
  <div class="card-body">
    <div class="mt-source" role="tablist" aria-label="信件來源">
      <button type="button" role="tab" aria-selected="true" aria-controls="mt-file" data-mt-mode="file">上傳 .eml</button>
      <button type="button" role="tab" aria-selected="false" aria-controls="mt-paste" data-mt-mode="paste">貼上原始碼</button>
    </div>
    <div id="mt-file" class="mt-drop" role="tabpanel" data-mt-drop>
      <input class="sr-only" type="file" id="mt-eml" name="eml" accept=".eml,message/rfc822">
      <label for="mt-eml">{{template "icon" "inbox"}}<span data-mt-file-label>拖放 .eml 檔到這裡，或點此選擇</span></label>
    </div>
    <div id="mt-paste" class="field" role="tabpanel" hidden>
      <label class="label" for="mt-text">信件原始碼</label>
      <textarea class="textarea input-mono" id="mt-text" name="eml_text" rows="8" spellcheck="false" placeholder="From: …&#10;Subject: …&#10;&#10;…"></textarea>
      <p class="hint">Gmail：信件右上「⋮」→「顯示原始郵件」→「複製到剪貼簿」（不要全選整頁）。Outlook：「⋯」→「檢視」→「檢視訊息原始碼」。</p>
    </div>
    <div class="field">
      <label class="label" for="mt-mailbox">預覽訊息使用的信箱</label>
      <select class="select" id="mt-mailbox" name="mailbox">
        <option value="">（不指定，用全域範本）</option>
        {{range .Data.Mailboxes}}<option value="{{.Name}}">{{.Label}}</option>{{end}}
      </select>
    </div>
    <div class="mt-actions">
      <button type="submit" class="btn btn-primary" data-busy-text="測試中…">{{template "icon" "check"}}<span>測試</span></button>
    </div>
    <div class="mt-out" data-mt-out aria-live="polite" hidden></div>
  </div>
</form>
```

（`icon` 名稱用 `_` 範本中實際存在的；`inbox`、`check` 已在本頁使用。）

- [ ] **Step 4: JS**

`fetchJSON` 支援 `FormData`：

```js
    if (body instanceof FormData) {
      init.body = body;
    } else if (body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body === null ? {} : body);
    }
```

並更新檔頭說明：`relay.fetchJSON(url, body?)  有 body 時 POST：FormData 原樣送出（multipart），其他以 JSON 編碼。`

在 templates page 區段之後加入（`showSafeHTML` 已在同一 IIFE 內定義）：

```js
  /* ---------- rules page: mail test ----------

    form[data-mailtest]                獨立於規則表單；送出時把 form[data-rules-form] 目前的欄位
                                       （不含 csrf）連同信件以 multipart POST /api/rules/test。
      [data-mt-mode="file|paste"]      切換信件來源（只送出目前來源的欄位）。
      [data-mt-drop]                   拖放區（內含 input[name=eml]）。
      [data-mt-out]                    結果。規則錯誤時把對應欄位標成 aria-invalid（data-mt-invalid）。
  */

  function mtMode(form) {
    var tab = form.querySelector('[data-mt-mode][aria-selected="true"]');
    return tab ? tab.dataset.mtMode : "file";
  }

  function setMtMode(form, mode) {
    form.querySelectorAll("[data-mt-mode]").forEach(function (t) {
      var on = t.dataset.mtMode === mode;
      t.setAttribute("aria-selected", on ? "true" : "false");
      byId(t.getAttribute("aria-controls")).hidden = !on;
    });
  }

  function keptRuleRows(rulesForm) {
    var rows = [];
    rulesForm.querySelectorAll("[data-rule-list] [data-rule]").forEach(function (li) {
      var filled = false;
      li.querySelectorAll("input[name^='rule_']").forEach(function (i) { if (i.value.trim()) filled = true; });
      var act = li.querySelector("select[name='rule_ignore']");
      if (act && act.value !== "0") filled = true;
      if (filled) rows.push(li); // the server drops blank rows before numbering them
    });
    return rows;
  }

  function clearMtMarks() {
    document.querySelectorAll("[data-mt-invalid]").forEach(function (n) {
      n.removeAttribute("aria-invalid");
      n.removeAttribute("data-mt-invalid");
    });
  }

  function markMtErrors(rulesForm, errors) {
    var rows = keptRuleRows(rulesForm);
    var first = null;
    Object.keys(errors).forEach(function (k) {
      var m = /^rules\[(\d+)\](?:\.(\w+))?$/.exec(k);
      var n = null;
      if (m && rows[+m[1]]) n = rows[+m[1]].querySelector("[name='rule_" + (m[2] || "name") + "']");
      else if (k === "recognize.extra_code_keywords") n = byId("kw-code");
      else if (k === "recognize.extra_link_keywords") n = byId("kw-link");
      if (n) {
        n.setAttribute("aria-invalid", "true");
        n.setAttribute("data-mt-invalid", "");
        if (!first) first = n;
      }
    });
    return first;
  }

  function mtSection(title) {
    var s = el("section", "mt-section");
    s.appendChild(el("h3", "mt-section-title", title));
    return s;
  }

  var MT_VERDICT = {
    code: ["ok", "會轉發驗證碼"], link: ["ok", "會轉發連結"], both: ["ok", "會轉發驗證碼與連結"],
    ignored: ["muted", "被規則略過，不轉發"], none: ["warn", "不符合任何規則，不轉發"]
  };

  function showMailTest(out, d) {
    out.textContent = "";
    var v = MT_VERDICT[d.result.kind] || MT_VERDICT.none;
    var head = el("div", "mt-verdict is-" + v[0]);
    head.appendChild(icon(v[0] === "ok" ? "check" : v[0] === "warn" ? "alert" : "info"));
    head.appendChild(el("p", "mt-verdict-text", v[1]));
    out.appendChild(head);

    var facts = el("dl", "mt-facts");
    [["驗證碼", d.result.code], ["連結", d.result.url], ["服務", d.result.service], ["規則", d.result.rule]].forEach(function (f) {
      if (!f[1]) return;
      var row = el("div");
      row.appendChild(el("dt", "", f[0]));
      row.appendChild(el("dd", "mono", f[1]));
      facts.appendChild(row);
    });
    if (facts.firstChild) out.appendChild(facts);

    if (d.preview) {
      var pv = mtSection("Telegram 訊息預覽");
      var bubble = el("div", "tg-bubble mt-bubble");
      var text = el("div", "tg-text");
      showSafeHTML(text, d.preview.html || "");
      bubble.appendChild(text);
      if (d.preview.buttons && d.preview.buttons.length) {
        var kb = el("div", "tg-kb");
        d.preview.buttons.forEach(function (b) {
          kb.appendChild(el("span", "tg-key" + (b.url ? " tg-key-url" : " tg-key-copy"), b.text));
        });
        bubble.appendChild(kb);
      }
      pv.appendChild(bubble);
      out.appendChild(pv);
    } else if (d.preview_error) {
      var pe = mtSection("Telegram 訊息預覽");
      pe.appendChild(el("p", "field-error", "範本錯誤：" + d.preview_error));
      out.appendChild(pe);
    }

    var tr = el("details", "mt-trace");
    tr.appendChild(el("summary", "", "辨識過程（" + d.trace.length + " 步）"));
    var ol = el("ol", "mt-steps mono");
    d.trace.forEach(function (s) { ol.appendChild(el("li", /^\s/.test(s) ? "is-sub" : "", s.trim())); });
    tr.appendChild(ol);
    out.appendChild(tr);

    var msg = d.message;
    var mail = el("details", "mt-mail");
    mail.appendChild(el("summary", "", "信件內容"));
    var mdl = el("dl", "mt-facts");
    [["寄件者", msg.from], ["主旨", msg.subject]].forEach(function (f) {
      var row = el("div");
      row.appendChild(el("dt", "", f[0]));
      row.appendChild(el("dd", "", f[1] || "（無）"));
      mdl.appendChild(row);
    });
    mail.appendChild(mdl);
    (msg.warnings || []).forEach(function (w) { mail.appendChild(el("p", "hint", "警告：" + w)); });
    mail.appendChild(el("pre", "mt-body", msg.text || "（沒有內文）"));
    var links = el("ul", "mt-links");
    (msg.links || []).forEach(function (l) {
      var li = el("li");
      li.appendChild(el("span", "mono", l.url));
      if (l.text) li.appendChild(el("span", "mt-link-text", l.text));
      links.appendChild(li);
    });
    mail.appendChild(el("p", "mt-section-title", "連結 " + (msg.links || []).length + " 個"));
    if (links.firstChild) mail.appendChild(links);
    out.appendChild(mail);
    out.hidden = false;
  }

  function showMailTestError(out, text, items) {
    out.textContent = "";
    var head = el("div", "mt-verdict is-err");
    head.appendChild(icon("alert"));
    head.appendChild(el("p", "mt-verdict-text", text));
    out.appendChild(head);
    if (items && items.length) {
      var ul = el("ul", "mt-errors");
      items.forEach(function (t) { ul.appendChild(el("li", "", t)); });
      out.appendChild(ul);
    }
    out.hidden = false;
  }

  document.addEventListener("submit", function (e) {
    var form = e.target.closest ? e.target.closest("form[data-mailtest]") : null;
    if (!form) return;
    e.preventDefault();
    // The shared submit handler (registered earlier) has already marked the form as
    // submitting and the button busy; undo both when done, as the page stays.
    var rulesForm = document.querySelector("form[data-rules-form]");
    var out = form.querySelector("[data-mt-out]");
    var btn = form.querySelector("button[type=submit]");
    function done() { delete form.dataset.submitting; setBusy(btn, false); }
    var fd = new FormData();
    if (rulesForm) new FormData(rulesForm).forEach(function (v, k) { if (k !== "csrf") fd.append(k, v); });
    var file = byId("mt-eml").files[0];
    var paste = byId("mt-text").value;
    if (mtMode(form) === "file") {
      if (!file) { showMailTestError(out, "請先選擇 .eml 檔"); done(); return; }
      fd.append("eml", file);
    } else {
      if (!paste.trim()) { showMailTestError(out, "請先貼上信件原始碼"); done(); return; }
      fd.append("eml_text", paste);
    }
    fd.append("mailbox", byId("mt-mailbox").value);
    clearMtMarks();
    fetchJSON("/api/rules/test", fd).then(function (d) {
      if (d.ok) { showMailTest(out, d); return; }
      var items = [];
      if (d.errors) {
        Object.keys(d.errors).forEach(function (k) { items.push((k ? k + "：" : "") + d.errors[k]); });
        var first = rulesForm && markMtErrors(rulesForm, d.errors);
        if (first) first.focus();
      }
      showMailTestError(out, d.error || "測試失敗", items);
    }, function (err) {
      showMailTestError(out, err.message);
    }).then(done);
  });

  document.addEventListener("click", function (e) {
    var tab = e.target.closest ? e.target.closest("[data-mt-mode]") : null;
    if (tab) setMtMode(tab.closest("form"), tab.dataset.mtMode);
  });

  document.addEventListener("change", function (e) {
    if (e.target.id !== "mt-eml") return;
    var f = e.target.files[0];
    var label = document.querySelector("[data-mt-file-label]");
    if (label) label.textContent = f ? f.name + "（" + Math.ceil(f.size / 1024) + " KB）" : "拖放 .eml 檔到這裡，或點此選擇";
  });

  ["dragenter", "dragover"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var drop = e.target.closest ? e.target.closest("[data-mt-drop]") : null;
      if (!drop) return;
      e.preventDefault();
      drop.classList.add("is-over");
    });
  });
  ["dragleave", "drop"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var drop = e.target.closest ? e.target.closest("[data-mt-drop]") : null;
      if (!drop) return;
      e.preventDefault();
      drop.classList.remove("is-over");
      if (type === "drop" && e.dataTransfer.files.length) {
        var input = byId("mt-eml");
        input.files = e.dataTransfer.files;
        input.dispatchEvent(new Event("change", { bubbles: true }));
      }
    });
  });
```

（既有的 document `submit` 處理器先執行，會設 `data-submitting` 並讓按鈕顯示 `data-busy-text`；本處理器在結束時以 `done()` 復原，否則第二次測試會被擋下。表單沒有 `action`，JS 未載入時按「測試」不會送出任何東西——在面板加 `<noscript><p class="hint">信件測試需要啟用 JavaScript。</p></noscript>`。）

- [ ] **Step 5: CSS**

在 `app.css` 的 rules 區段之後加入 `.mailtest`、`.mt-source`、`.mt-drop`（含 `.is-over`）、`.mt-actions`、`.mt-out`、`.mt-verdict`（`.is-ok`／`.is-warn`／`.is-muted`／`.is-err`）、`.mt-facts`、`.mt-section`、`.mt-section-title`、`.mt-bubble`、`.mt-trace`、`.mt-steps`（`.is-sub` 縮排）、`.mt-mail`、`.mt-body`（`white-space: pre-wrap`、最大高度可捲動）、`.mt-links`、`.mt-link-text`、`.mt-errors` 的樣式，依 frontend-design 的設計；沿用 `.tg-bubble`／`.tg-kb`／`.tg-key` 既有樣式呈現預覽；`.mailtest` 與 `.rules-form` 同寬（`max-width: 980px`），上方留與卡片間距一致的空白；窄螢幕（≤ 640px）各區塊單欄。

- [ ] **Step 6: 執行確認通過**

Run: `go test -race -count=1 ./internal/web`
Expected: PASS。

- [ ] **Step 7: 截圖確認（暫時檔，不 commit）**

建立 `internal/web/zz_shot_test.go`（**不要 commit**；完成後刪除）：

```go
//go:build shot

package web

import (
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestShot serves the logged-in admin on http://127.0.0.1:18080 for headless screenshots:
// it adds the session cookie and Origin, drops the CSP, and on /rules?demo=1 injects a
// script that pastes a sample mail and runs the test.
func TestShot(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	target, _ := url.Parse(e.ts.URL)
	p := httputil.NewSingleHostReverseProxy(target)
	p.Transport = e.c.Transport
	cookie := sessionCookie + "=" + e.sessionCookie()
	p.Director = func(r *http.Request) {
		r.URL.Scheme, r.URL.Host, r.Host = target.Scheme, target.Host, target.Host
		r.Header.Set("Cookie", cookie)
		if r.Method != http.MethodGet {
			r.Header.Set("Origin", e.ts.URL)
			r.Header.Del("Sec-Fetch-Site")
		}
	}
	demo := `<script>addEventListener("load",function(){document.querySelector('[data-mt-mode="paste"]').click();` +
		`document.getElementById("mt-text").value=` + "`" + strings.ReplaceAll(recoveryMail, "\r\n", "\\n") + "`" + `;` +
		`document.querySelector("form[data-mailtest] button[type=submit]").click();` +
		`setTimeout(function(){document.querySelector(".mt-trace").open=true;document.getElementById("mt-title").scrollIntoView()},800)})</script>`
	p.ModifyResponse = func(resp *http.Response) error {
		resp.Header.Del("Content-Security-Policy")
		if resp.Request.URL.Query().Get("demo") == "1" && strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			b, _ := io.ReadAll(resp.Body)
			b = []byte(strings.Replace(string(b), "</body>", demo+"</body>", 1))
			resp.Body = io.NopCloser(strings.NewReader(string(b)))
			resp.ContentLength = int64(len(b))
			resp.Header.Del("Content-Length")
		}
		return nil
	}
	srv := &http.Server{Addr: "127.0.0.1:18080", Handler: p}
	go srv.ListenAndServe()
	time.Sleep(90 * time.Second)
	srv.Close()
}
```


執行：

```bash
go test -tags shot -run TestShot -timeout 3m ./internal/web &
# 等 http://127.0.0.1:18080/rules 可回應後：
chromium --headless --disable-gpu --hide-scrollbars --window-size=1280,2400 --virtual-time-budget=4000 \
  --screenshot=$CLAUDE_JOB_DIR/tmp/rules-mailtest.png "http://127.0.0.1:18080/rules?demo=1"
chromium --headless --disable-gpu --hide-scrollbars --window-size=390,2400 --virtual-time-budget=4000 \
  --screenshot=$CLAUDE_JOB_DIR/tmp/rules-mailtest-mobile.png "http://127.0.0.1:18080/rules?demo=1"
```

用 Read 工具看兩張截圖：面板與結果（結論列、驗證碼 123456、Telegram 預覽、辨識過程）都要清楚、對齊、不破版。不滿意就調整 CSS 後重拍。確認後刪除 `zz_shot_test.go`。

- [ ] **Step 8: Commit**

```bash
git add internal/web
git commit -m "feat(web): mail test panel on the rules page

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 13: 文件與最終驗證

**Files:**
- Modify: `README.md`
- Modify: `docs/TODO.md`
- Modify: `docs/superpowers/specs/2026-10-21-recognition-v2-design.md`（狀態行）

- [ ] **Step 1: README**

找到 README 中說明辨識規則的章節（搜尋「辨識」「關鍵字」），補充：
- 內建辨識以 HTML 內文為主，純文字版為備援；
- 主旨中的數字若也獨佔內文一行，即使沒有「驗證碼」等字樣也會被當成驗證碼（年份、訂單類主旨除外）；
- 純英文字母驗證碼只在緊接「code: / code is / 驗證碼：」等字樣、且獨佔一行或為粗體時辨識；
- 連結：只有「驗證」「Verify」等字樣的按鈕、圖片按鈕、「Click here」（前文說明用途時）都會辨識；
- 會排除：卡號末碼、郵遞區號、地址門牌、電子報常見連結（取消訂閱、在瀏覽器中檢視、聯絡我們等）。

並在 Web 介面章節加「用信件測試」：在「規則與範本 → 辨識規則」頁下方上傳 `.eml` 或貼上原始碼；使用頁面上目前填寫（尚未儲存也算）的規則；不儲存、不發送；Gmail／Outlook 取得原始碼的方式；5 MiB 上限。保留 CLI `relay test --eml` 的說明。

- [ ] **Step 2: TODO 與規格狀態**

`docs/TODO.md`：若有「`.eml` 上傳測試」延後項目，標為完成（`- [x]`）；新增延後項目「片語型驗證碼（correct-horse-battery）」「辨識結果的信心分數／多候選輸出」，各附一句不做的理由（見規格 §1 決策表）。規格狀態行改為「已實作」。

- [ ] **Step 3: 全部驗證**

Run:

```bash
gofmt -l . && go vet ./... && go test -race -count=1 ./... && git status --short
```

Expected: `gofmt` 無輸出、vet 與測試全過；`git status` 只剩 `?? argon_sug.md`。另用 `go build -o $CLAUDE_JOB_DIR/tmp/relay ./cmd/relay` 確認可建置。

- [ ] **Step 4: Commit**

```bash
git add README.md docs
git commit -m "docs: recognition v2 and the mail test

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```
