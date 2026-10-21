# 依資料夾設定轉發路由 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 信箱的轉發目標改為「資料夾 → 目標」配對（`routes`），舊的 `folders`／`targets` 自動轉換；順便讓不穩定的效能測試在 CI 上穩定。

**Architecture:** `config.Mailbox` 新增 `Routes []Route`；舊欄位只作輸入，在解碼後與套用預設值時轉成配對。`runtime` 為每個資料夾建立 watcher 時只帶入該資料夾的目標，`pipeline` 不變。網頁信箱表單把兩張卡合併為一張可增刪列的「轉發路由」卡；清單、Telegram 使用情況、總覽、命令列改讀路由。

**Tech Stack:** Go、`gopkg.in/yaml.v3`、`html/template`、原生 JS／CSS。

**Spec:** `docs/superpowers/specs/2026-10-21-folder-routes-design.md`

## Global Constraints

- 錯誤路徑：`mailboxes.X.routes`、`mailboxes.X.routes[i].folder`、`mailboxes.X.routes[i].target`。
- 錯誤訊息：「至少需要一條轉發路由」、「不可為空」、「找不到目標 %q」、「與第 N 條路由重複」（N 從 1 起算）、「routes 不能與舊的 folders／targets 同時使用」。
- 舊格式轉換：`folders` 為空視為 `[INBOX]`；配對順序為「資料夾依序，每個資料夾內目標依序」。
- 表單欄位：平行陣列 `route_folder`、`route_target`；兩欄都空白的列忽略。
- 前端樣式沿用現有設計系統（`app.css` 檔頭配方、`@layer`、container queries）；新 UI 用 frontend-design 的原則把關，並以 Playwright 截圖檢視。
- 每次提交前：`gofmt -l .` 無輸出、`go vet ./...`、`go test -race -count=1 ./...` 通過。
- 截圖用的暫時測試檔（`internal/web/zz_demo_test.go`）不提交。

---

### Task 1: 穩定 `TestParseManyLinksIsFast`

**Files:** Modify `internal/mailparse/perf_test.go:71-74`

在 CI 的 `-race` 下上限 2s，實測 2.15s 失敗。測試的目的是擋住「1 MiB 連結」造成的平方級退化（那會慢上數十倍），時間上限放寬到 race 下 10 倍不影響這個目的。

- [ ] 把 `limit *= 4` 改為 `limit *= 10`，註解說明：race 偵測器在共用 CI 機器上慢 4 倍以上且不穩定；這個上限只為擋住平方級退化。
- [ ] Run: `go test -race -count=3 -run TestParseManyLinksIsFast ./internal/mailparse/` → PASS
- [ ] Commit: `test(mailparse): give the link-parsing time bound room on a busy CI runner`

### Task 2: 設定：`Route`、舊格式轉換、驗證

**Files:**
- Modify `internal/config/config.go`（`Mailbox`、`Route`、方法、`decodeNode`、`applyDefaults`）
- Modify `internal/config/validate.go:114-138`
- Modify `config.example.yaml`、`README.md:239`
- Test `internal/config/routes_test.go`（新）、既有 `config_test.go`／`validate_test.go` 中受影響的案例

**Interfaces（Produces）：**
```go
type Route struct {
	Folder string `yaml:"folder"`
	Target string `yaml:"target"`
}
// Mailbox: Routes []Route `yaml:"routes,omitempty"`; Folders、Targets 保留但註明 legacy input。
func (m *Mailbox) WatchedFolders() []string        // 第一次出現順序、去重
func (m *Mailbox) TargetsOf(folder string) []string // 路由順序、去重
func (m *Mailbox) RouteTargets() []string           // 第一次出現順序、去重
func (m *Mailbox) adoptLegacyRoutes()               // Routes 空：由 Folders×Targets 產生；清空舊欄位
```

- [ ] 寫測試 `routes_test.go`：
  - `Decode` 新格式 → `Routes` 正確、`Encode` 往返相同；
  - 舊格式 `folders: [INBOX, Spam]`、`targets: [me, fam]` → 四條配對順序 INBOX→me、INBOX→fam、Spam→me、Spam→fam，`Folders`／`Targets` 為空；
  - 舊格式沒有 `folders` → INBOX 配對；
  - 新舊並存 → `Decode` 錯誤含「routes 不能與舊的 folders／targets 同時使用」；
  - Go 結構的舊格式經 `Defaulted()` 轉換；
  - 三個方法的順序與去重；
  - `Validate`：沒有路由、資料夾空白、目標空白、找不到目標、重複配對（訊息「與第 1 條路由重複」，路徑 `routes[2]`）。
- [ ] 確認失敗 → 實作：`decodeNode` 解碼後對每個信箱：若 `Routes` 與舊欄位並存回傳錯誤，否則 `adoptLegacyRoutes()`；`applyDefaults` 移除補 INBOX，改呼叫 `adoptLegacyRoutes()`；`validate.go` 以路由檢查取代資料夾／目標檢查，舊欄位仍有值時報並存錯誤。
- [ ] 既有 config 測試：YAML 舊格式案例保持（轉換的回歸測試）；直接比對 `Folders`／`Targets` 的斷言改為比對 `Routes`。
- [ ] `config.example.yaml` 改用 `routes`（`personal_gmail` 兩個資料夾都到 me；`outlook` INBOX 到 me 與 work_chan）；README 表格把 `folders`、`targets` 改為 `routes`（`folder`、`target` 配對；舊的 `folders`／`targets` 仍可讀取並自動轉換）。
- [ ] Run `go test ./internal/config/` → PASS；Commit: `feat(config): forward by folder: routes of folder → target, the old folders and targets read as routes`

### Task 3: 執行期：每個資料夾只送到自己的目標

**Files:** Modify `internal/runtime/runtime.go:338-360`；Test `internal/runtime/runtime_test.go`

- [ ] 測試：信箱 `gmail` 路由 INBOX→me、Spam→fam；建出的 service 有兩個 watcher，INBOX 的 `Mailbox.Targets` 只有 me、Spam 的只有 fam（以 `runningWatcher` 加上 `targets []string` 欄位供測試讀取，或測 `watcherConfigs(cfg)` 這個抽出的純函式——擇一，以不擴大 API 為原則：抽出 `func mailboxFor(cfg, name, folder) pipeline.Mailbox`）。
- [ ] 實作：迴圈改為 `for _, folder := range m.WatchedFolders()`，`targets` 由 `m.TargetsOf(folder)` 組成。
- [ ] 既有 runtime 測試中 `Folders:`／`Targets:` 結構改成 `Routes`（或保留舊格式以驗證 `Defaulted` 轉換，兩種都要有）。
- [ ] `cmd/relay/validate.go`：資料夾數改 `len(m.WatchedFolders())`，並印出路由數（「…（監看 N 個資料夾、M 條路由）…」），更新對應測試字串。
- [ ] Run `go test ./internal/runtime/ ./cmd/relay/` → PASS；Commit: `feat(runtime): each folder forwards to its own targets`

### Task 4: 信箱表單「轉發路由」卡

**Files:**
- Modify `internal/web/page_mailboxes.go`（`mailboxForm`、`formFromMailbox`、`readMailboxForm`、`mailbox()`、`remapMailbox`、`getMailboxNew`、`renderMailboxForm`、移除 `keepOrder` 對資料夾／目標的使用）
- Modify `internal/web/templates/mailbox_form.html`（合併兩張卡）
- Modify `internal/web/static/app.js`（mailbox form 區塊：路由列、測試連線填選項）
- Modify `internal/web/static/app.css`（`.route-rows` 等，放 `pages` 層 mailboxes 區塊）
- Test `internal/web/mailboxes_test.go`、`msauth_test.go`、`cmd/relay/web_e2e_test.go`（表單欄位改名）

**Interfaces：**
```go
type routeRow struct{ Folder, Target string }
// mailboxForm: Routes []routeRow（取代 Folders、Targets）
type routeRowView struct {
	Folder, Target          string
	FolderErr, TargetErr    string // routes[i].folder / .target 的錯誤
}
// mailboxFormData: Targets []routeOption（下拉選項，保留 Name/Bot/ChatID）、Rows []routeRowView、
//                  FolderOptions []string（路由中的資料夾；測試連線後由 JS 補上伺服器的）
```

表單錯誤鍵：`routes`（卡片層級）、`routes[i].folder`、`routes[i].target`（`remapMailbox` 將 `.routes[i].folder` 等原樣對應過去，`.routes` → `routes`）。

- [ ] 測試（先寫、先失敗）：
  - 新增信箱送出 `route_folder=[INBOX,Spam,""]`、`route_target=[me,fam,""]` → 存入兩條路由、空白列忽略；
  - 重複配對 → 422，錯誤顯示在第二列（頁面含該列的錯誤文字且該列輸入框 `aria-invalid="true"`）；
  - 沒有任何路由 → 卡片層級「至少需要一條轉發路由」；
  - 編輯：只調換兩列順序也會儲存新版本；
  - 新增頁預設一列 INBOX，只有一個目標時預選；
  - 編輯頁渲染現有路由列 ＋ 兩列空白列（無 JS 備援）。
- [ ] 實作 Go 與範本：卡片 `#routes`，每列 `.route-row`：資料夾 `<input class="input input-mono" name="route_folder" list="f-folder-options">`、`→`、目標 `<select class="select" name="route_target">`（第一個選項「選擇目標」空值）、刪除 `.icon-btn`；`<datalist id="f-folder-options">`；列錯誤在列下方 `.field-error`；卡片頂端保留「測試連線」與 `.probe` 說明（文字改為「…列出伺服器上的資料夾，可在路由的資料夾欄位挑選」）；底部「＋ 新增路由」。沒有目標時顯示原本的「還沒有任何 Telegram 目標」提示。
- [ ] 實作 JS：`[data-route-add]` 複製 `<template id="tpl-route-row">` 新增一列並聚焦資料夾欄；`[data-route-remove]` 刪除該列（只剩一列時清空而不刪）；測試連線成功後把伺服器資料夾寫入 datalist（加上「收件匣」／「垃圾郵件」標記作為 option 的 label）；任一欄變更時移除該列的 `aria-invalid`。刪除舊的 folder chip／`addFolder`／`showFolders` 程式與 `.folders`／`.folder*`／`.routes`／`.route-pick`／`.route-card` 等不再使用的 CSS（`.route`、`.route-line`、`.route-chat`、`.route-bot` 仍被 Telegram 與清單使用，保留）。
- [ ] 實作 CSS：列為 grid `minmax(0, 1.2fr) auto minmax(0, 1fr) auto`（箭頭用現有 `.route-line` 的郵務路徑線）；卡片容器 < 520px 時資料夾一行、目標＋刪除一行。
- [ ] 其他測試檔把 `"folders"`、`"targets"` 表單欄位改為 `route_folder`、`route_target`（加測試輔助 `routeForm(pairs ...string) url.Values`）。
- [ ] Run `go test -race ./internal/web/ ./cmd/relay/` → PASS；截圖檢視 1280／768／390 的新增與編輯頁（含錯誤狀態）；Commit: `feat(web): one forwarding routes card: folder → target rows`

### Task 5: 清單、Telegram、總覽改讀路由

**Files:** Modify `internal/web/page_mailboxes.go`（清單列）、`templates/mailboxes.html`、`app.css`（清單四欄）、`page_telegram.go`（`mailboxesUsing` → 含資料夾）、`page_dashboard.go:280`；Test `mailboxes_test.go`、`telegram_test.go`、`dashboard_test.go`

- [ ] 測試：清單中 `work` 的 Spam 資料夾後面只列 fam；Telegram 目標 me 的使用情況為「信箱 work（INBOX）」；刪除仍被使用的目標時訊息含「work（INBOX）」；總覽卡片的資料夾來自路由。
- [ ] 實作：`folderRow` 加 `Targets []string`；清單模板在每個資料夾的狀態後面接 `.route-line` 與目標方框；移除獨立的 `.mb-route` 欄，清單 container 寬模式欄軌改為 `glyph | account | folders(含目標) | actions`；`mailboxesUsing` 回傳 `[]string{"work（INBOX、Spam）"}`；`dashCard` 用 `m.WatchedFolders()`。
- [ ] Run tests → PASS；截圖 1920／1100／390 的信箱清單與 Telegram；Commit: `feat(web): lists and usage name each folder's targets`

### Task 6: 整體驗證與收尾

- [ ] 版本歷史還原：存一個舊格式版本（直接寫入 YAML）→ 還原 → 頁面正常、再儲存後最新版本為 `routes` 格式（加測試到 `settings_test.go` 的版本測試旁）。
- [ ] `go test -race -count=1 ./...`、`gofmt`、`go vet`。
- [ ] Playwright：`check.mjs` 全頁面無溢出；信箱表單、清單、Telegram 截圖檢視。
- [ ] 更新 `app.css` 檔頭配方（「路由列」取代「勾選卡」）、`app.js` mailbox form 註解。
- [ ] 刪除暫時測試檔；合併前詢問使用者（合併、發布版本）。
