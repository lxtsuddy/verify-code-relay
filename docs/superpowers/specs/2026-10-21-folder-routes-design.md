# 依資料夾設定轉發路由 設計規格

- 日期：2026-10-21
- 狀態：已實作（分支 folder-routes）
- 前一份相關規格：`2026-10-20-web-admin-design.md`（信箱表單、清單）、`2026-10-19-verify-code-relay-design.md`（以信箱為單位路由）。本規格改變「轉發目標以信箱為單位」的決定：改為以「資料夾 → 目標」配對為單位。兩者衝突時以本規格為準。

## 1. 目標

同一個信箱的不同資料夾可以轉發到不同的 Telegram 目標。信箱表單上原本分開的「資料夾」與「轉發到」合併成一張「轉發路由」卡：一列一組「資料夾 → 目標」配對。

### 已確定的決策

| 項目 | 決策 |
|---|---|
| 路由形狀 | 自由配對：一列＝一個資料夾配一個目標；同一個資料夾可出現在多列（送到多個目標），同一個目標也可配多個資料夾（使用者選擇，未採用「一個資料夾 → 多選目標」） |
| 監看哪些資料夾 | 由路由決定：路由中出現過的資料夾才監看；不存在「監看但沒有目標」的資料夾 |
| 儲存格式 | 有順序的配對清單，順序即畫面上的順序 |
| 舊設定 | 讀取時自動轉換，轉發行為與以前完全相同；下次儲存寫成新格式 |

## 2. 設定格式

```yaml
mailboxes:
  work:
    host: mail.example.com
    username: me@example.com
    routes:
      - folder: INBOX
        target: me
      - folder: INBOX
        target: family
      - folder: Notifications/Security
        target: me
```

- `Mailbox.Routes []Route`，`Route{Folder string \`yaml:"folder"\`; Target string \`yaml:"target"\`}`。
- `Mailbox.Folders`、`Mailbox.Targets` 改為**只供讀取舊格式**的欄位（仍可解析，編碼時一律為空因而省略）。程式其他地方不再使用它們。
- 新增方法：
  - `(*Mailbox) WatchedFolders() []string`：路由中出現過的資料夾，依第一次出現的順序、去重。
  - `(*Mailbox) TargetsOf(folder string) []string`：該資料夾配對的目標，依路由順序、去重。
  - `(*Mailbox) RouteTargets() []string`：路由中出現過的所有目標，依第一次出現的順序、去重。

### 2.1 舊格式轉換

在解析設定的共用步驟（`decodeNode` 之後；涵蓋 `Parse`、`Decode`、`LoadRawBytes`，也就是設定檔、資料庫目前的設定、版本歷史、匯入）對每個信箱執行：

1. `routes` 不為空且 `folders`／`targets` 也有值 → 保留原樣，由驗證回報 `mailboxes.X.routes: routes 不能與舊的 folders／targets 同時使用`。（實作時修訂：原定為解析錯誤；但 `Clone` 以編碼再解碼複製設定，解析錯誤會讓 `Defaulted`／`Build` 對這種 Go 結構直接 panic，改由驗證回報，使用者看到的訊息相同。）
2. `routes` 為空且 `targets` 不為空 → 產生配對：對 `folders`（為空時視為 `[INBOX]`，與舊的預設相同）中的每個資料夾，依序配上 `targets` 中的每個目標。轉換後清空 `folders`、`targets`。
3. `routes` 為空且 `targets` 也為空 → 不轉換（驗證會報「至少需要一條路由」）；`folders` 一併清空。

`applyDefaults` 中「`folders` 為空時補 `INBOX`」改為同樣的轉換（用 Go 結構組出的舊格式信箱也會轉換）。

## 3. 驗證

取代原本 `folders`／`targets` 的檢查，錯誤路徑使用 `mailboxes.X.routes`、`mailboxes.X.routes[i].folder`、`mailboxes.X.routes[i].target`：

- 至少一條路由：「至少需要一條轉發路由」。
- 資料夾不可空白：「不可為空」。
- 目標不可空白；目標必須存在：「找不到目標 %q」。
- 完全相同的配對（資料夾與目標都相同）不可重複：「與第 N 條路由重複」（N 從 1 起算）。

## 4. 轉發

- `runtime` 為每個監看的資料夾（`WatchedFolders()`）建立 watcher 時，傳入的 `pipeline.Mailbox` 的 `Targets` 只含該資料夾的目標（`TargetsOf(folder)`）。
- `pipeline`、`watcher`、`dispatcher`、待發佇列、紀錄不變：信件從哪個資料夾進來，就排入那個資料夾的目標。

## 5. 網頁

### 5.1 信箱表單（新增／編輯）

- 「資料夾」與「轉發到」兩張卡合併為一張「轉發路由」卡（`#routes`）：
  - 說明：「信件從哪個資料夾進來，就轉發到該列的目標。同一個資料夾可以送到多個目標，各寫一列。」
  - 每列：資料夾輸入框（`<input list>`，選項來自「測試連線」列出的資料夾與目前路由中的資料夾）→ 目標下拉選單（全部目標，顯示 `名稱` 與 `bot → chat`）→ 刪除按鈕。
  - 卡片底下：「＋ 新增路由」；卡片頂端保留「測試連線」按鈕，測試成功後資料夾欄位的選項更新，並沿用現有的資料夾標記（例如垃圾郵件資料夾的提醒）於下拉選項文字中。
  - 新增信箱時預設一列：`INBOX →（只有一個目標時預選它，否則請選擇）`。
  - 表單欄位：平行陣列 `route_folder`、`route_target`（依列順序）；全空白的列（兩欄都空）忽略。
  - 驗證錯誤：卡片層級錯誤（`routes`）顯示在卡片頂端；列層級錯誤（`routes[i].folder`／`.target`）顯示在該列下方並標示該欄位 `aria-invalid`。
  - 沒有 JS 時：伺服器渲染目前的路由列加上兩列空白列可填；JS 提供新增／刪除列。
- 編輯時只要路由的**集合**相同（只是順序不同）也算變更（順序是使用者排的，要保存）。移除現有的 `keepOrder` 對資料夾／目標的處理。

### 5.2 信箱清單

- 資料夾欄每個資料夾後接它的狀態與目標：`INBOX ●連線中 → me family`；取代原本整個信箱共用一組目標的「轉發到」欄（清單由五欄變四欄：標誌｜帳號｜資料夾與目標｜動作）。

### 5.3 總覽

- 信箱卡片不變（顯示資料夾與狀態）。

### 5.4 Telegram 頁

- 目標的「使用情況」寫出信箱與資料夾：「信箱 work（INBOX、Notifications/Security）、gmail（INBOX）」。
- 刪除仍被路由使用的目標時，錯誤訊息同樣寫出信箱與資料夾。

### 5.5 命令列

- `relay validate` 的摘要計數「資料夾」改用 `WatchedFolders()`，另加「路由」數。

## 6. 測試

- `config`：新格式解析與 YAML 往返；舊格式轉換（有資料夾、沒有資料夾、多資料夾×多目標的順序）；新舊並存報錯；`WatchedFolders`／`TargetsOf`／`RouteTargets` 的順序與去重；驗證的四種錯誤與路徑。
- `runtime`：兩個資料夾配不同目標時，各 watcher 的 `Mailbox.Targets` 正確。
- `pipeline` 端到端（`cmd/relay` 的 e2e 或 `runtime` 測試）：A 資料夾的信只排入 A 的目標。
- `web`：表單新增／編輯／刪除列、重複配對、空白列忽略、錯誤顯示在對的列、順序保存；清單顯示每個資料夾的目標；Telegram 使用情況與刪除受阻訊息；版本歷史還原舊格式版本後正常、再儲存寫成新格式。
