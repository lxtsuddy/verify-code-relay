# Web 管理介面響應式版面 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 讓管理介面在 320–2560px 任何寬度都不橫向溢出、寬螢幕置中，元件依自身寬度排版；外觀風格不變。

**Architecture:** 保留 Go `html/template` 伺服器渲染與原生 JS。重建 `internal/web/static/app.css` 的版面系統：`@layer` 分層、設計變數、置中的頁面寬度（範本區塊宣告）、三段導覽列、container queries 取代零散的視窗斷點；紀錄表改為 CSS grid 列（欄寬與內容無關），信箱／Telegram 清單用 subgrid 對齊。

**Tech Stack:** CSS（container queries、subgrid、`:has()`、`@layer`）、Go `html/template`、原生 JS；驗證用 Playwright（只在開發機，位於 `$CLAUDE_JOB_DIR/tmp`，不進 repo）。

**Spec:** `docs/superpowers/specs/2026-10-21-responsive-layout-design.md`

## Global Constraints

- 不引入 Node 建置、不引入前端框架；`app.css`、`app.js` 仍為單檔，經 `go:embed` 打包。
- 不改配色、郵務風格元件（郵戳、郵票、郵遞區號格）、動畫、文案、伺服器端行為、API。
- 視窗斷點只用 `640px`、`860px`、`1200px`；其餘一律 container queries。
- 頁面寬度：`--page-wide: 1360px`（總覽、紀錄、信箱清單、Telegram）、`--page-form: 1040px`（信箱表單、規則、範本、設定、版本歷史）。
- 字級變數：`--fs-2xs` 11.5／`--fs-xs` 12.5／`--fs-sm` 13.5／`--fs-md` 14.5／`--fs-base` 15／`--fs-lg` 16／`--fs-xl` 18／`--fs-2xl` 22（px）。
- 版面相容：Chrome／Edge 117+、Safari 16+、Firefox 121+。
- 每次提交前：`gofmt -l .` 無輸出、`go vet ./...`、`go test -race -count=1 ./...` 通過。
- `internal/web/zz_demo_test.go` 是截圖用的暫時檔，**不可提交**（提交時只 `git add` 列出的檔案），Task 8 刪除。
- 註解與文案用繁體中文（CSS 註解沿用檔案現有的英文風格）。

## 驗證工具（已建好，位於 `$CLAUDE_JOB_DIR/tmp`）

- 示範伺服器：`DEMO_OUT=$CLAUDE_JOB_DIR/tmp/demo.txt go test ./internal/web -run TestZZDemoServer -count=1 -timeout 12h -v`（背景執行）。啟動後寫出 `demo.txt`（網址＋session cookie）；刪除 `demo.txt` 即停止。**範本（.html）或 Go 有改動時要重啟**；只改 `app.css`／`app.js` 不用重啟（工具會從工作目錄直接讀這兩個檔）。
- `node shots.mjs OUTDIR [寬度,...] [頁面,...]`：全頁截圖（固定時鐘、reduced motion、停用動畫）。
- `node diff.mjs DIR_A DIR_B`：逐像素比對，有差異時列出並以 exit 1 結束，差異圖輸出到 `DIR_A-diff/`。
- `node check.mjs [寬度,...] [頁面,...]`：整頁與 `.table-wrap`、`.mb-list`、`.tg-list`、`.card`、`.main` 的橫向溢出檢查；有溢出時 exit 1。
- 預設頁面：`/`、`/?recent=all`、`/logs`、`/logs?kind=hit`、`/mailboxes`、`/mailboxes/new`、`/mailboxes/work`、`/mailboxes/outlook`、`/telegram`、`/rules`、`/templates`、`/settings`、`/settings/versions`、`/no-such-page`、`/login`；預設寬度 390／768／1024／1100／1280／1440／1920／2560。
- 修改前基準：`check.mjs` 有 10 組失敗（`/`、`/logs` 在 768／1024／1100 的表格溢出）。

---

### Task 1: 修改前基準截圖

**Files:** 無（只產生 `$CLAUDE_JOB_DIR/tmp/base/`）

- [ ] **Step 1: 確認示範伺服器在跑**

Run: `cat $CLAUDE_JOB_DIR/tmp/demo.txt`
Expected: 第一行為 `https://127.0.0.1:PORT`，第二行為 `__Host-relay_session=...`

- [ ] **Step 2: 在涵蓋所有現有斷點區間的寬度截圖**

現有斷點區間：≤640、641–760、761–860、861–1080、1081–1100、>1100。

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs base 390,640,700,800,1000,1090,1280`
Expected: 每個寬度印出 `done`，`base/` 內 105 張 PNG。

- [ ] **Step 3: 重拍一次確認截圖穩定**

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs base2 390,640,700,800,1000,1090,1280 && node diff.mjs base base2`
Expected: `105 compared, 0 different`

---

### Task 2: CSS 分層與重新排列（畫面完全不變）

**Files:**
- Modify: `internal/web/static/app.css`（整檔）

**Interfaces:**
- Produces: 分層 `@layer tokens, base, layout, components, pages, utilities;`，後續任務把新規則加進對應的層。每個元件的 `@media` 規則緊接在該元件區塊之後。

這是純搬移：**不改任何宣告的值**。用 `@layer` 包住現有區塊，並把檔尾的 `/* narrow screens */` 兩個 `@media` 區塊與其他散落的 `@media` 拆開，放回各自元件區塊的最後。

- [ ] **Step 1: 在檔頭註解之後宣告層的順序**

```css
@layer tokens, base, layout, components, pages, utilities;
```

- [ ] **Step 2: 依現有順序把區塊包進層**

| 層 | 現有區塊（依檔案順序） |
|---|---|
| `tokens` | `:root { … }` |
| `base` | `/* base */`（含 `.muted`、`.hint`、`.eyebrow`、`.link`、`.stack`、`.row`、`.grid-2`） |
| `layout` | `/* brand */`、`/* app shell */`、`/* page header */` |
| `components` | `/* cards */`（含 `.facts`）、`/* badges & tags */`、`/* buttons */`、`/* forms */`、`/* the signature: postcode boxes */`、`/* tables */`、`/* empty state */`、`/* callouts, flash, toast */`、`/* modal */` |
| `pages` | `/* centred pages */` 起到 `/* overview */`、紀錄頁（log page）、pager 為止的所有頁面區塊，順序不變 |
| `utilities` | `/* motion */`（`@keyframes` 留在層外的檔尾；`prefers-reduced-motion` 規則放進 `utilities`） |

- [ ] **Step 3: 把檔尾 `/* narrow screens */` 的規則拆回各區塊**

`@media (max-width: 860px)` 與 `@media (max-width: 640px)` 中的每條規則，依選擇器搬到所屬區塊尾端的同一個 `@media` 裡，例如：
- `.shell`、`.rail`、`.nav`、`.nav-item`、`.brand-mark`、`.rail-logout`、`.logout-btn`、`.main`、`.page-head`、`.page-title` → `layout` 層的 shell／page header 區塊之後。
- `.grid-2` → `base`；`.card-*`、`.facts` → cards；`.page-actions`、`.form-actions` → forms；`.toasts`、`.toast` → callouts；`dialog.modal` → modal。
- `.auth-*`、`.step`、`.qr` → centred pages；`.setting-*`、`.card-section`、`.foot-note`、`.ip-*`、`.postmark` → settings；`.ledger-*` → versions；`.tg-*`、`.token-row`、`.chats-hint`、`.chat-*` → telegram；`.mb-*`、`.stamps`、`.stamp-*`、`.grid-3`、`.oauth-state`、`.save-bar`、`.danger-body` → mailboxes；`.modal-oauth`、`.oauth-*` → oauth dialog。
- 已經在元件旁的 `@media`（ms-app 860、mb-item 1080、tpl 1100、rules 640、mailtest 760／640、overview 860／640）位置不動。

`@keyframes sheet-in` 移到其他 `@keyframes` 旁（層外）。

- [ ] **Step 4: 逐像素比對**

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs t2 390,640,700,800,1000,1090,1280 && node diff.mjs base t2`
Expected: `105 compared, 0 different`

出現差異時，打開 `base-diff/` 的差異圖找出是哪條規則的優先順序改變（搬移後被同層、同權重、較後面的規則蓋掉），把那條規則移到能維持原順序的位置，再比對一次，直到 0 差異。

- [ ] **Step 5: 提交**

```bash
git add internal/web/static/app.css
git commit -m "style(web): layer app.css and keep each component's breakpoints beside it"
```

---

### Task 3: 字級與間距變數

**Files:**
- Modify: `internal/web/static/app.css`

- [ ] **Step 1: 在 `:root` 加入變數**

```css
  --fs-2xs: 11.5px;
  --fs-xs: 12.5px;
  --fs-sm: 13.5px;
  --fs-md: 14.5px;
  --fs-base: 15px;
  --fs-lg: 16px;
  --fs-xl: 18px;
  --fs-2xl: 22px;

  --sp-1: 4px;
  --sp-2: 8px;
  --sp-3: 12px;
  --sp-4: 16px;
  --sp-5: 20px;
  --sp-6: 24px;
  --sp-8: 32px;
  --sp-10: 40px;
```

- [ ] **Step 2: 字級改用變數**

把 `font-size: Npx` 依下表換成變數（`@media` 內的也一樣）：

| 現值 | 變數 |
|---|---|
| 10px、11px、11.5px | `var(--fs-2xs)` |
| 12px、12.5px | `var(--fs-xs)` |
| 13px、13.5px | `var(--fs-sm)` |
| 14px、14.5px | `var(--fs-md)` |
| 15px、15.5px | `var(--fs-base)` |
| 16px | `var(--fs-lg)` |
| 17px、18px | `var(--fs-xl)` |
| 20px、22px、23px | `var(--fs-2xl)` |

**保留原值**（裝飾性、與圖形尺寸綁定）：`.otp-input`（26px）、`.otp-sm`、`.postcode` 系列、`.stamp-glyph`（46／40px）、`.mb-glyph`（24px）、`.dash-glyph`（20px）、`.ip-value`（30／26px）、`.pm-arc`、`.pm-mid`、`.pm-arc-time`、`.setup-no`（18px）、`.health-facts dd`（22／20px）、`.rule-no::before`、`.page-title`（Task 4 改為 `clamp()`）、`.auth-title`（24px）、`.tg-key-url::after`（10px）、`.input`／`.select`／`.textarea`／`.tags-input` 的 16px（防 iOS 縮放，改用 `var(--fs-lg)` 亦可，值相同）。

- [ ] **Step 3: 版面層級的間距改用變數**

只換下列選擇器的 `margin`／`padding`／`gap` 中等於 4／8／12／16／20／24／32／40 的值：`.page-head`、`.section-title`、`.section-head`、`.card-head`、`.card-body`、`.card-foot`、`.card + .card`、`.stack`、`.grid-2`、`.grid-3`、`.flash`、`.callout`、`.dash-cards`、`.setup-steps`。元件內部細部間距不動。

- [ ] **Step 4: 檢視差異**

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs t3 390,1280 && node diff.mjs base t3`
Expected: 只有字級被歸併的文字有半像素等級的差異（例如 13px→13.5px 的小字、14px→14.5px 的表格字）。打開 `base-diff/` 幾張確認沒有版面跳動（卡片高度、換行位置大致不變）。

- [ ] **Step 5: 提交**

```bash
git add internal/web/static/app.css
git commit -m "style(web): one type and spacing scale"
```

---

### Task 4: 置中的頁面寬度與三段導覽列

**Files:**
- Modify: `internal/web/templates/layout.html`（`app` 版型的 `<main>`、檔頭註解）
- Modify: `internal/web/templates/mailbox_form.html`、`rules.html`、`templates.html`、`settings.html`、`versions.html`（加 `{{define "width"}}form{{end}}`）
- Modify: `internal/web/static/app.css`（`layout` 層；移除 `.mb-form`、`.rules-form`、`.tpl-form`、`.mailtest`、`.danger-zone` 的 `max-width`）
- Test: `internal/web/web_test.go`

**Interfaces:**
- Produces: `<main id="main" class="main main-wide">` 或 `main-form`；CSS 變數 `--page-max`、`--gutter`、`--rail-w`、`--rail-w-compact`。

- [ ] **Step 1: 寫失敗的測試**

加到 `internal/web/web_test.go` 檔尾：

```go
// Pages declare their width: lists and the overview are wide, forms are narrower.
func TestPageWidthClass(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	for path, want := range map[string]string{
		"/":                  "main-wide",
		"/logs":              "main-wide",
		"/mailboxes":         "main-wide",
		"/telegram":          "main-wide",
		"/mailboxes/new":     "main-form",
		"/mailboxes/gmail":   "main-form",
		"/rules":             "main-form",
		"/templates":         "main-form",
		"/settings":          "main-form",
		"/settings/versions": "main-form",
	} {
		_, body := e.get(path)
		if !strings.Contains(body, `<main id="main" class="main `+want+`"`) {
			t.Errorf("%s: want <main> with class %s", path, want)
		}
	}
}
```

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/web -run TestPageWidthClass -count=1`
Expected: FAIL，每個路徑都報 `want <main> with class …`

- [ ] **Step 3: 版型產生寬度 class**

`layout.html` 的 `app` 版型：

```html
  <main id="main" class="main main-{{block "width" .}}wide{{end}}" tabindex="-1">
```

在 `mailbox_form.html`、`rules.html`、`templates.html`、`settings.html`、`versions.html` 檔尾（最後一個 `{{end}}` 之後）各加一行：

```html
{{define "width"}}form{{end}}
```

檔頭註解的 Page files 說明加一行：

```
  {{define "width"}}form{{end}}  optional: a form page (narrower, --page-form); default "wide" (--page-wide)
```

- [ ] **Step 4: 執行確認通過**

Run: `go test ./internal/web -run TestPageWidthClass -count=1`
Expected: PASS

- [ ] **Step 5: 外框 CSS**

`:root`（`tokens` 層）把 `--rail-w: 232px;` 換成：

```css
  --rail-w-full: 232px;
  --rail-w-compact: 88px;
  --rail-w: var(--rail-w-full);
  --page-wide: 1360px;
  --page-form: 1040px;
  --gutter: clamp(16px, 0.75rem + 1.8vw, 48px);
```

`layout` 層的 `.main` 換成：

```css
/* the page column is centred in the space beside the rail; each page declares its width
   (main-wide for lists and the overview, main-form for forms; see layout.html) */
.main {
  --page-max: var(--page-wide);
  width: 100%; max-width: calc(var(--page-max) + 2 * var(--gutter)); margin-inline: auto;
  padding: clamp(24px, 1rem + 1.2vw, 40px) var(--gutter) 72px;
}
.main-form { --page-max: var(--page-form); }
```

`.page-title` 的 `font-size: 28px` 換成 `font-size: clamp(24px, 1.1rem + 0.8vw, 30px);`，並刪除 `@media (max-width: 860px)` 中的 `.main { padding: … }` 與 `.page-title { font-size: 24px; }`。

刪除 `.mb-form { max-width: 860px; }`、`.rules-form, .tpl-form { max-width: 980px; }` 的 `max-width`、`.mailtest` 的 `max-width: 980px;`、`.danger-zone` 的 `max-width: 860px;`（保留各規則的其他宣告）。

- [ ] **Step 6: 精簡導覽列（860–1199px）**

加在 `layout` 層 shell 區塊尾端、`@media (max-width: 860px)` 之前：

```css
/* mid widths (laptops with display scaling): an icon rail with small labels, so the
   content keeps its room; the current page is still a tab joined to the content */
@media (min-width: 861px) and (max-width: 1199px) {
  :root { --rail-w: var(--rail-w-compact); }
  .rail { padding: 18px 0 14px 8px; }
  .rail .brand { justify-content: center; padding: 0 8px 22px 0; }
  .rail .brand-name {
    position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap;
  }
  .nav-item, .logout-btn {
    flex-direction: column; justify-content: center; gap: 4px;
    padding: 9px 2px 8px; font-size: var(--fs-xs); letter-spacing: 0.02em; line-height: 1.25; text-align: center;
  }
  .nav-item { margin-right: 8px; }
  .nav-item .icon, .logout-btn .icon { width: 20px; height: 20px; }
  /* the joined tab is 8px wider than the others; pad it so its icon stays in line */
  .nav-item[aria-current="page"] { padding-right: 10px; border-radius: 9px 0 0 9px; }
  .rail-logout { margin-right: 8px; }
  .logout-btn { width: 100%; }
}
```

- [ ] **Step 7: 截圖檢視**

重啟示範伺服器（範本有改），然後：

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs t4 900,1100,1280,1920,2560 /,/mailboxes/work,/rules`
Expected（打開圖片檢查）：
- 1920、2560：內容在導覽列右側置中，左右留白相等；總覽寬度約 1360，信箱表單、規則頁約 1040。
- 900、1100：導覽列約 88px，圖示下有小字，「規則與範本」單行不換行；目前頁面的分頁與內容區相接、圖示與其他項目對齊。
- 1280：寬導覽列。

- [ ] **Step 8: 全部測試並提交**

Run: `go test -race -count=1 ./internal/web/`
Expected: PASS

```bash
git add internal/web/templates/layout.html internal/web/templates/mailbox_form.html internal/web/templates/rules.html internal/web/templates/templates.html internal/web/templates/settings.html internal/web/templates/versions.html internal/web/static/app.css internal/web/web_test.go
git commit -m "feat(web): centre the page column, size it by page kind, and an icon rail at mid widths"
```

---

### Task 5: 轉發紀錄表：grid 列與三種模式

**Files:**
- Modify: `internal/web/templates/_logs.html`（`title` 屬性）
- Modify: `internal/web/templates/logs.html:67`（表格加 `log-table-rule`）
- Modify: `internal/web/static/app.js`（`recentRow()`）
- Modify: `internal/web/static/app.css`（`components` 層的 tables、`pages` 層的 forward log rows；刪除 overview 區塊 640 `@media` 中的 `.log-table …` 規則）
- Test: `internal/web/dashboard_test.go`

**Interfaces:**
- Consumes: Task 4 的 `.main` 寬度。
- Produces: `.table-wrap` 為 `container: table / inline-size`；`.log-table-rule` 表示有規則欄。

- [ ] **Step 1: 寫失敗的測試**

加到 `internal/web/dashboard_test.go` 檔尾：

```go
// Subjects and senders are clipped on screen, so the full text rides along as a title.
func TestLogRowTitles(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	e.st.RecordMessage(context.Background(), store.LogEntry{At: time.Now(), Mailbox: "gmail", Folder: "INBOX", UID: 1,
		FromAddr: "Long Sender <long.sender@example.com>", Subject: "A long subject line", Kind: "code", Result: "sent"},
		nil, store.MailboxState{UIDValidity: 1, LastUID: 1})
	for _, path := range []string{"/", "/logs"} {
		_, body := e.get(path)
		if !strings.Contains(body, `<span class="log-subject" title="A long subject line">`) {
			t.Errorf("%s: subject without title", path)
		}
		if !strings.Contains(body, `<span class="log-from mono" title="Long Sender &lt;long.sender@example.com&gt;">`) {
			t.Errorf("%s: sender without title", path)
		}
	}
	if _, body := e.get("/logs"); !strings.Contains(body, `class="table log-table log-table-full log-table-rule"`) {
		t.Error("/logs: the table with a rule column should say so")
	}
}
```

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/web -run TestLogRowTitles -count=1`
Expected: FAIL，三個訊息都出現。

- [ ] **Step 3: 範本與 JS**

`_logs.html` 的 `c-msg` 儲存格換成：

```html
  <td class="c-msg"><span class="log-subject"{{with .Subject}} title="{{.}}"{{end}}>{{with .Subject}}{{.}}{{else}}<span class="muted">（沒有主旨）</span>{{end}}</span><span class="log-from mono" title="{{.From}}">{{.From}}</span></td>
```

`logs.html` 第 67 行：

```html
  <table class="table log-table log-table-full log-table-rule">
```

`app.js` 的 `recentRow()` 中，建立 `subj` 與寄件者的地方換成：

```js
    var subj = el("span", "log-subject", r.subject ? r.subject : null);
    if (r.subject) subj.title = r.subject;
    else subj.appendChild(el("span", "muted", "（沒有主旨）"));
    msg.appendChild(subj);
    var from = msg.appendChild(el("span", "log-from mono", r.from));
    from.title = r.from;
```

- [ ] **Step 4: 執行確認通過**

Run: `go test ./internal/web -run 'TestLogRowTitles|TestDashboard|TestLogs' -count=1`
Expected: PASS

- [ ] **Step 5: 紀錄表 CSS**

`components` 層 tables 區塊加：

```css
.table-wrap { container: table / inline-size; }
```

`pages` 層的 `/* forward log rows */` 整段（`.log-table td` 到 `.log-table .badge`）換成下面這段；並刪除 overview 區塊 `@media (max-width: 640px)` 裡所有 `.log-table …` 規則（卡片模式改由這段的預設樣式負責）：

```css
/* forward log rows (the overview's latest and the log page). Every row is a grid with the
   same content-independent tracks, so columns line up without table layout; the layout
   follows the width the table actually gets:
     card     (default)     time + result, subject, mailbox + kind, rule
     compact  (≥ 600px)     time over mailbox | subject | kind over result
     full     (≥ 880px; ≥ 1000px with the rule column)  one column each, header shown */
.log-table, .log-table thead, .log-table tbody { display: block; }
.log-table tr {
  display: grid; align-items: center; gap: 4px 12px;
  grid-template-columns: minmax(0, 1fr) auto;
  grid-template-areas: "time result" "msg msg" "mb kind" "rule rule";
  padding: 12px 16px; border-bottom: 1px solid var(--line);
}
.log-table tbody tr:last-child { border-bottom: 0; }
.log-table th, .log-table td { display: block; min-width: 0; padding: 0; border: 0; background: none; }
.log-table thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
.log-table thead tr { padding: 10px 16px; background: var(--surface-2); border-bottom: 1px solid var(--line); }

.log-table .c-time { grid-area: time; white-space: nowrap; font-size: var(--fs-sm); color: var(--ink-2); font-variant-numeric: tabular-nums; }
.log-table .c-mb { grid-area: mb; display: flex; flex-wrap: wrap; align-items: baseline; gap: 0 8px; font-size: var(--fs-sm); }
.log-table .c-msg { grid-area: msg; }
.log-table .c-kind { grid-area: kind; justify-self: end; white-space: nowrap; }
.log-table .c-rule { grid-area: rule; font-size: var(--fs-xs); color: var(--muted); overflow-wrap: break-word; }
.log-table .c-rule:has(.faint) { display: none; }
.log-table .c-rule .log-rule::before { content: "規則："; color: var(--faint); }
.log-table .c-result { grid-area: result; justify-self: end; white-space: nowrap; }

/* one line per mailbox name and folder; a long one ends in … (the full name is the title) */
.log-mb { min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.log-key { margin-left: 6px; font-size: var(--fs-2xs); font-weight: 500; color: var(--muted); }
.log-folder { min-width: 0; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-xs); color: var(--muted); }
/* the subject wraps to two lines at most, the sender stays on one (full text in title) */
.log-subject {
  display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; line-clamp: 2;
  overflow: hidden; overflow-wrap: anywhere; color: var(--ink);
}
.log-from { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: var(--fs-xs); color: var(--muted); }
.log-rule { color: var(--ink-2); }
.faint { color: var(--faint); }
.tag-hit { background: var(--green-50); color: var(--green-800); }
.log-table .badge { font-size: var(--fs-xs); line-height: 18px; }

@container table (min-width: 600px) {
  .log-table tr {
    grid-template-columns: 9em minmax(0, 1fr) 7.5em;
    grid-template-areas: "time msg kind" "mb msg result" ". rule .";
    gap: 2px 18px;
  }
  .log-table .c-mb { display: grid; gap: 0; }
  .log-table .c-kind, .log-table .c-result { justify-self: start; }
  .log-table .c-rule .log-rule::before { content: none; }
  .log-table .c-rule::before { content: "規則："; color: var(--faint); }
}

@container table (min-width: 880px) {
  .log-table:not(.log-table-rule) thead { position: static; width: auto; height: auto; overflow: visible; clip: auto; }
  .log-table:not(.log-table-rule) tr {
    grid-template-columns: 9em minmax(8em, 13em) minmax(0, 1fr) 7.5em 6.5em;
    grid-template-areas: "time mb msg kind result";
    gap: 0 18px;
  }
  .log-table:not(.log-table-rule) .c-rule::before { content: none; }
}

@container table (min-width: 1000px) {
  .log-table-rule thead { position: static; width: auto; height: auto; overflow: visible; clip: auto; }
  .log-table-rule tr {
    grid-template-columns: 9em minmax(8em, 13em) minmax(0, 1fr) 7.5em minmax(6em, 10em) 6.5em;
    grid-template-areas: "time mb msg kind rule result";
    gap: 0 18px;
  }
  .log-table-rule .c-rule { font-size: var(--fs-sm); color: var(--ink-2); }
  .log-table-rule .c-rule:has(.faint) { display: block; }
  .log-table-rule .c-rule::before { content: none; }
}

.log-table th {
  white-space: nowrap; font-size: var(--fs-xs); font-weight: 700; letter-spacing: 0.06em; color: var(--muted);
}
```

注意：
- 寬模式的表頭 `th` 依 `grid-template-areas` 放置，需要在 `th` 也加上與 `td` 相同的 class。`_logs.html` 不含表頭；`dashboard.html` 與 `logs.html` 的 `<th>` 依序加上 `class="c-time"`、`"c-mb"`、`"c-msg"`、`"c-kind"`、（`"c-rule"`）、`"c-result"`。
- 加 class 後需重啟示範伺服器。

- [ ] **Step 6: 檢查溢出與截圖**

重啟示範伺服器後：

Run: `cd $CLAUDE_JOB_DIR/tmp && node check.mjs 390,768,1024,1100,1280,1920 /,/?recent=all,/logs,/logs?kind=hit`
Expected: `no overflow`

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs t5 390,768,1024,1280,1920 /,/logs`
Expected（打開圖片檢查）：390 為卡片；768、1024 為精簡表格（時間下有信箱、末欄種類疊結果）；1280 總覽為完整表格、紀錄頁視寬度為精簡或完整；1920 都是完整表格且有表頭。每一列欄位對齊，主旨最多兩行，寄件者單行加「…」。

- [ ] **Step 7: 總覽自動更新後的列一致**

`updateRecent()` 只在 `/api/status` 的 `recent` 內容改變時重畫，所以攔截輪詢回應、在每筆主旨後加上「·」強制重畫，再把重畫後的列（去掉「·」）與伺服器渲染的列比對。在 `$CLAUDE_JOB_DIR/tmp` 建立 `live.mjs`：

```js
import { readDemo, open, visit } from './lib.mjs';
const { base, cookies } = readDemo(new URL('./demo.txt', import.meta.url).pathname);
const { browser, ctx, page } = await open(1280, { cookies });
await visit(page, base + '/');
const norm = s => s.replace(/\s+/g, ' ').replace(/> </g, '><').replace(/<time[^>]*>/g, '<time>');
const before = norm(await page.locator('[data-recent]').innerHTML());
await ctx.route('**/api/status', async route => {
  const res = await route.fetch();
  const d = await res.json();
  for (const r of d.recent || []) if (r.subject) r.subject += '·';
  await route.fulfill({ response: res, json: d });
});
await page.waitForFunction(() => document.querySelector('[data-recent]').textContent.includes('·'), null, { timeout: 20000 });
const after = norm((await page.locator('[data-recent]').innerHTML()).replaceAll('·', ''));
if (before === after) console.log('same rows');
else { console.log('DIFFERENT'); console.log(before.slice(0, 800)); console.log('---'); console.log(after.slice(0, 800)); }
await browser.close();
```

Run: `cd $CLAUDE_JOB_DIR/tmp && node live.mjs`
Expected: `same rows`。若不同，修正 `recentRow()` 使 class、`title`、欄位順序與 `_logs.html` 相同，再執行一次。

- [ ] **Step 8: 全部測試並提交**

Run: `go test -race -count=1 ./internal/web/`
Expected: PASS

```bash
git add internal/web/templates/_logs.html internal/web/templates/logs.html internal/web/templates/dashboard.html internal/web/static/app.js internal/web/static/app.css internal/web/dashboard_test.go
git commit -m "feat(web): forward log rows follow the table's own width: full, compact and card"
```

---

### Task 6: 信箱清單與 Telegram 清單對齊

**Files:**
- Modify: `internal/web/templates/mailboxes.html`（資料夾名稱 `title`）
- Modify: `internal/web/static/app.css`（`pages` 層 telegram、mailboxes 區塊；刪除 `@media (max-width: 1080px)` 的 `.mb-*` 規則與 640 中的 `.tg-*`、`.mb-item` 規則）
- Test: `internal/web/mailboxes_test.go`

**Interfaces:**
- Produces: `.mb-list`、`.tg-list` 為 `container: list / inline-size`，`li` 為 subgrid。

- [ ] **Step 1: 寫失敗的測試**

加到 `internal/web/mailboxes_test.go` 檔尾：

```go
// A long folder name is clipped in the list; its full name is the title.
func TestMailboxListFolderTitle(t *testing.T) {
	e := newEnv(t, withSample)
	e.login()
	_, body := e.get("/mailboxes")
	if !strings.Contains(body, `<span class="mb-folder-name mono" title="INBOX">INBOX</span>`) {
		t.Error("folder name without title")
	}
}
```

- [ ] **Step 2: 執行確認失敗**

Run: `go test ./internal/web -run TestMailboxListFolderTitle -count=1`
Expected: FAIL `folder name without title`

- [ ] **Step 3: 範本**

`mailboxes.html` 的資料夾項目：

```html
      {{range .Folders}}<li class="mb-folder"><span class="mb-folder-name mono" title="{{.Name}}">{{.Name}}</span><span class="badge {{.Class}}{{if .Live}} badge-live{{end}}">{{.Label}}</span></li>{{end}}
```

- [ ] **Step 4: 執行確認通過**

Run: `go test ./internal/web -run 'TestMailboxListFolderTitle|TestMailboxes' -count=1`
Expected: PASS

- [ ] **Step 5: 信箱清單 CSS**

`.mb-list` 與 `.mb-item` 換成下面這段，並刪除 `@media (max-width: 1080px) { .mb-item … }` 整段、640 `@media` 中的 `.mb-item { padding … }` 與 `.mb-item:has(.btn-auth) .mb-actions …`：

```css
/* the list is the grid and every row a subgrid, so the columns (the actions too) line up
   from row to row; the layout follows the list's own width:
     narrow  (default)   glyph | account, then folders, route and actions under it
     medium  (≥ 600px)   glyph | account | actions, folders and route under the account
     wide    (≥ 900px)   glyph | account | folders | route | actions */
.mb-list {
  container: list / inline-size;
  display: grid; grid-template-columns: 44px minmax(0, 1fr);
  margin: 0; padding: 0; list-style: none;
  background: var(--surface); border: 1px solid var(--line); border-radius: var(--radius); box-shadow: var(--shadow-1);
}
.mb-item {
  display: grid; grid-column: 1 / -1; grid-template-columns: subgrid; gap: 10px 16px; align-items: center;
  padding: 14px 16px; border-bottom: 1px solid var(--line);
}
.mb-glyph { grid-row: span 4; align-self: start; }
.mb-main, .mb-folders, .mb-route, .mb-actions { grid-column: 2; }
.mb-actions { justify-content: flex-start; }
.mb-folders { display: flex; flex-wrap: wrap; gap: 6px 16px; }
.mb-folder { flex-wrap: nowrap; min-width: 0; max-width: 100%; }
.mb-folder-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.mb-folder .badge { flex: none; }

@container list (min-width: 600px) {
  .mb-list { grid-template-columns: 44px minmax(0, 1fr) auto; }
  .mb-item { padding: 16px 20px; }
  .mb-glyph { grid-row: span 3; }
  .mb-main { grid-column: 2; grid-row: 1; }
  .mb-actions { grid-column: 3; grid-row: 1; align-self: start; justify-content: flex-end; }
  .mb-folders, .mb-route { grid-column: 2 / 4; }
}

@container list (min-width: 900px) {
  .mb-list { grid-template-columns: 44px minmax(0, 1.3fr) minmax(0, 1fr) minmax(0, 0.7fr) auto; }
  .mb-item { gap: 8px 20px; }
  .mb-glyph { grid-row: auto; align-self: center; }
  .mb-main, .mb-folders, .mb-route, .mb-actions { grid-column: auto; grid-row: auto; }
  .mb-actions { align-self: center; }
  .mb-folders { display: grid; gap: 5px; }
}
```

保留原有的 `.mb-item:last-child`、`.mb-item:hover`、`.mb-glyph` 外觀、`.mb-main`、`.mb-title`、`.mb-link`、`.mb-account`、`.mb-host`、`.mb-folder`（`display: flex; align-items: center; gap: 4px 8px; font-size`）、`.mb-route`、`.mb-actions { display: flex; gap: 8px; flex-wrap: wrap; }` 規則；上面只覆寫版面相關的宣告。

- [ ] **Step 6: Telegram 清單 CSS**

`.tg-list`、`.tg-item`、`.tg-bot` 換成下面這段，並刪除 640 `@media` 中所有 `.tg-item`、`.tg-bot` 規則（`.tg-actions { justify-content: flex-start; }`、`.tg-btn-text { display: none; }` 移進下面的預設樣式）：

```css
/* bots and targets: the list is the grid, rows are subgrids so the usage and action
   columns line up; narrow lists stack each row */
.tg-list {
  container: list / inline-size;
  display: grid; grid-template-columns: 38px minmax(0, 1fr);
  margin: 0; padding: 0; list-style: none;
  background: var(--surface); border: 1px solid var(--line); border-radius: var(--radius); box-shadow: var(--shadow-1);
}
.tg-item {
  display: grid; grid-column: 1 / -1; grid-template-columns: subgrid; gap: 8px 12px; align-items: center;
  padding: 14px 16px; border-bottom: 1px solid var(--line);
}
.tg-item > * { grid-column: 1 / -1; }
.tg-bot > .tg-avatar { grid-column: 1; grid-row: span 3; align-self: start; }
.tg-bot > :not(.tg-avatar) { grid-column: 2; }
.tg-actions { justify-content: flex-start; }
.tg-btn-text { display: none; }

@container list (min-width: 720px) {
  .tg-list { grid-template-columns: 38px minmax(0, 1.2fr) minmax(0, 1fr) auto; }
  .tg-item { gap: 6px 22px; padding: 14px 20px; }
  .tg-item > * { grid-column: auto; }
  .tg-target > .tg-main { grid-column: 1 / 3; }
  .tg-bot > .tg-avatar { grid-row: auto; align-self: center; }
  .tg-bot > :not(.tg-avatar) { grid-column: auto; }
  .tg-actions { justify-content: flex-end; }
  .tg-btn-text { display: inline; }
}
```

保留原有的 `.tg-item:last-child`、`.tg-item:hover`、`.tg-avatar` 外觀、`.tg-main`、`.tg-name`、`.tg-handle`、`.tg-uses`、`.tg-actions`（flex 與 gap）規則。

- [ ] **Step 7: 檢查與截圖**

重啟示範伺服器後：

Run: `cd $CLAUDE_JOB_DIR/tmp && node check.mjs 390,768,1024,1100,1280,1920 /mailboxes,/telegram`
Expected: `no overflow`

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs t6 390,768,1100,1920 /mailboxes,/telegram`
Expected（打開圖片檢查）：1920 信箱清單五欄，三列的資料夾欄、目標欄、動作欄起點一致（Outlook 有「授權」按鈕時，動作欄在每一列都同寬）；1100 為中等排法；390 堆疊。Telegram 在 1100、1920 為多欄且列與列對齊，390 堆疊。

- [ ] **Step 8: 全部測試並提交**

Run: `go test -race -count=1 ./internal/web/`
Expected: PASS

```bash
git add internal/web/templates/mailboxes.html internal/web/static/app.css internal/web/mailboxes_test.go
git commit -m "feat(web): mailbox and Telegram lists line up row to row and follow their own width"
```

---

### Task 7: 其他元件改用 container queries

**Files:**
- Modify: `internal/web/templates/dashboard.html`（無；`.board` 已存在）
- Modify: `internal/web/static/app.css`

**Interfaces:**
- Consumes: Task 2 的分層。
- Produces: `.board` 為 `container: board / inline-size`；`.card` 為 `container: card / inline-size`。

- [ ] **Step 1: 容器宣告**

`components` 層 cards 區塊：

```css
.card { container: card / inline-size; }
```

`pages` 層 overview 區塊：

```css
.board { container: board / inline-size; }
```

注意：`.card` 有 `container-type: inline-size` 後，卡片寬度不能再由內容撐開；所有 `.card` 都是區塊層級或 grid 項目，不受影響。`form.card` 同理。

- [ ] **Step 2: 系統狀態卡與開始使用三步驟**

overview 區塊的 `@media (max-width: 860px)` 改為 `@container board (max-width: 760px)`，`@media (max-width: 640px)` 中與 `.health`、`.health-*`、`.dash-cards` 相關的規則移到新的 `@container board (max-width: 560px)`；`.page-head .live-note { order: -1; }` 與 `.section-head:has(.log-view-sm)`、`.log-view-sm` 規則留在 `@media (max-width: 640px)`（屬於頁面層級）。

```css
@container board (max-width: 760px) {
  .health { grid-template-columns: 132px minmax(0, 1fr); gap: 8px 20px; padding: 18px; }
  .health .health-mark { width: 124px; height: 124px; }
  .setup-steps { grid-template-columns: minmax(0, 1fr); gap: 10px; }
  .setup-step + .setup-step::before { left: 33px; top: -11px; width: 0; height: 12px; border-top: 0; border-left: 1.5px dashed var(--line-2); }
}

@container board (max-width: 560px) {
  /* 原 @media (max-width: 640px) 中 .health 起到 .dash-cards 的規則，內容不變 */
}
```

（第二個區塊的內容就是原本 640 `@media` 裡從 `/* seal beside the verdict … */` 到 `.dash-cards { … }` 的規則，原樣搬入。）

- [ ] **Step 3: 訊息範本**

```css
.tpl-grid { display: grid; grid-template-columns: minmax(0, 1fr); }
.tg-phone { border-top: 1px solid var(--line); border-radius: 0 0 var(--radius) var(--radius); }
.tg-chat { min-height: 220px; }
@container card (min-width: 860px) {
  .tpl-grid { grid-template-columns: minmax(0, 1fr) 360px; }
  .tg-phone { border-top: 0; border-left: 1px solid var(--line); border-radius: 0 var(--radius) var(--radius) 0; }
  .tg-chat { min-height: 300px; }
}
```

取代原本 `.tpl-grid` 的 `grid-template-columns`、`.tg-phone` 的 `border-left`／`border-radius`、`.tg-chat` 的 `min-height`，並刪除 `@media (max-width: 1100px)` 整段。確認 `.tpl-grid` 所在的 `.tpl-panel` 是 `.card`（`templates.html`）；若不是，容器改設在 `.tpl-panel`：`.tpl-panel { container: card / inline-size; }`。

- [ ] **Step 4: 信件測試結果**

```css
.mt-result.has-preview { grid-template-columns: minmax(0, 1fr); }
.mt-detail { display: contents; }
.mt-detail > .mt-facts { order: 1; }
.mt-preview { order: 2; }
.mt-trace { order: 3; }
.mt-mail { order: 4; }
@container card (min-width: 760px) {
  .mt-result.has-preview { grid-template-columns: minmax(0, 1fr) 340px; align-items: start; }
  .mt-detail { display: grid; }
  .mt-detail > .mt-facts, .mt-preview, .mt-trace, .mt-mail { order: 0; }
}
```

取代原本 `.mt-result.has-preview` 的欄位定義與 `@media (max-width: 760px)` 整段。原本的 `.mt-detail { display: grid; gap: 12px; align-content: start; min-width: 0; }` 保留在前面，這裡只覆寫 `display`。

- [ ] **Step 5: Microsoft 應用程式卡片與設定列**

```css
.ms-app-body { grid-template-columns: minmax(0, 1fr); }
@container card (min-width: 760px) {
  .ms-app-body { grid-template-columns: minmax(0, 1fr) minmax(0, 0.9fr); }
}
```

取代 `.ms-app-body` 原本的 `grid-template-columns` 與 `@media (max-width: 860px) { .ms-app-body … }`。

`.setting-row` 原本的兩欄定義保留，640 `@media` 中的 `.setting-row { grid-template-columns: minmax(0, 1fr); padding: 14px 16px; }` 改為：

```css
@container card (max-width: 520px) {
  .setting-row { grid-template-columns: minmax(0, 1fr); padding: 14px 16px; }
}
```

- [ ] **Step 6: 檢查與截圖**

Run: `cd $CLAUDE_JOB_DIR/tmp && node check.mjs`
Expected: `no overflow`（全部頁面、全部寬度）

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs t7 390,768,1100,1440,1920 /,/templates,/rules,/settings`
Expected（打開圖片檢查）：總覽狀態卡在窄容器下郵戳縮小、數字排兩欄；範本頁在 1100 以上編輯器與預覽並排（1040 寬的表單頁內，卡片約 1000px）；設定頁 Microsoft 卡片在寬時兩欄。

- [ ] **Step 7: 全部測試並提交**

Run: `go test -race -count=1 ./internal/web/`
Expected: PASS

```bash
git add internal/web/static/app.css
git commit -m "feat(web): overview, templates, mail test and settings follow their own width"
```

---

### Task 8: 全面驗證、文件、清理與合併

**Files:**
- Modify: `internal/web/static/app.css`（檔頭註解）
- Delete: `internal/web/zz_demo_test.go`
- Modify: `docs/TODO.md`（若有前端相關項目需更新）

- [ ] **Step 1: 全部頁面、全部寬度無溢出**

Run: `cd $CLAUDE_JOB_DIR/tmp && node check.mjs`
Expected: `no overflow`

- [ ] **Step 2: 完整截圖檢視**

Run: `cd $CLAUDE_JOB_DIR/tmp && node shots.mjs final`
Expected: 逐一打開 `final/` 中 390、1100、1280、1920、2560 的總覽、紀錄、信箱、信箱編輯、Telegram、規則、範本、設定、登入、404 截圖，確認：沒有被切掉的文字或按鈕、卡片對齊、導覽列三段正確、寬螢幕置中。發現問題就修正並回到 Step 1。

- [ ] **Step 3: 更新 `app.css` 檔頭註解**

- 「版面」段落改寫為：桌機 ≥1200px 寬導覽軌；860–1199px 圖示導覽軌（圖示＋小字）；860px 以下頂部列與可橫向捲動的分頁。內容欄在導覽軌右側置中，頁面以 `{{define "width"}}form{{end}}` 宣告為表單寬度（`--page-form`），預設 `--page-wide`。
- 新增「斷點」段落：視窗斷點只有 640／860／1200；元件用 container queries，容器名稱 `table`（`.table-wrap`）、`list`（`.mb-list`、`.tg-list`）、`board`（`.board`）、`card`（`.card`）。
- 「紀錄」配方改為：每列為 grid，依 `table` 容器寬度為卡片（預設）／精簡（≥600）／完整（≥880，有規則欄時 ≥1000）；有規則欄的表格加 `.log-table-rule`。
- 「清單」配方補上：`.mb-list`、`.tg-list` 的列是 subgrid。
- 新增「層」段落：`@layer tokens, base, layout, components, pages, utilities;`，新規則放進對應的層，元件的 `@media`／`@container` 寫在元件區塊尾端。

- [ ] **Step 4: 刪除暫時測試檔，跑完整檢查**

```bash
rm internal/web/zz_demo_test.go
gofmt -l .
go vet ./...
go test -race -count=1 ./...
```
Expected: `gofmt` 無輸出，`vet` 無輸出，測試全部 PASS。

（刪除前先停止示範伺服器：`rm $CLAUDE_JOB_DIR/tmp/demo.txt`。）

- [ ] **Step 5: 提交**

```bash
git add internal/web/static/app.css
git status --short   # 確認沒有 zz_demo_test.go、argon_sug.md 以外的未追蹤檔
git commit -m "docs(web): document the layout system in app.css"
```

- [ ] **Step 6: 修改前後對照給使用者**

挑 1100、1920、2560 的總覽與紀錄、1100 的信箱清單，`base`／`final` 各一張，回報路徑給使用者。

- [ ] **Step 7: 合併**

依 superpowers:finishing-a-development-branch：`git checkout master && git merge --no-ff responsive-layout`，訊息 `Merge branch 'responsive-layout': a layout that follows the screen and each component's own width`。是否打 `v0.2.7` 標籤發布，詢問使用者。
