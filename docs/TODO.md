# 待辦清單

2026-10-20：第一輪清單已在 `todo-cleanup` 分支處理完，未勾選的是刻意保留的項目。

第一版開發期間各任務審查中「不影響合併、延後處理」的項目。依影響程度排序；每項附上相關檔案。
已在最終修正中處理的項目（辨識效能、分段驗證碼遮蔽、FETCH 途中斷線測試、退避重置時機、README 指引）不再列出。

## 一、行為與穩健性

- [x] **告警文字不精確**：連線成功但第一次補抓一直失敗時，斷線告警寫「無法連線」，實際原因是處理失敗。改成區分「無法連線」與「無法完成補抓」。（`internal/watcher/run.go`）
- [ ] **IDLE 前的極短空窗**：最後一次補抓與進入 IDLE 之間到達的信，若伺服器在 IDLE 開始時不回報先前的變化，要等下一個事件或 25 分鐘循環才會處理。可考慮縮短 IDLE 循環或在 IDLE 確認後再補抓一次。（`internal/watcher/session.go`）
  - 2026-10-20：「IDLE 確認後再補抓」無法消除空窗（IDLE 期間不能下指令，重進 IDLE 前又有同樣空窗）。待以真實 Gmail／Outlook 確認伺服器在 IDLE 開始時是否回報先前變化，再決定是否縮短 `IdleCycle`。
- [x] **TLSDialer 忽略 context**：停止服務時若正在連線，最多要等 30 秒逾時。改用 `tls.Dialer{}.DialContext` 再 `imapclient.New`。（`internal/watcher/watcher.go`）
- [x] **Microsoft device code 輪詢**：`interval` 為 0 時會密集輪詢，正式環境應至少 5 秒；暫時性錯誤（5xx、網路）會直接中止授權且錯誤訊息為空，應重試並附上 HTTP 狀態。（`internal/auth/microsoft.go`）
- [x] **OAuth token 更新失敗時**：access token 仍在有效期內就應繼續使用；refresh token 輪替後若儲存失敗會遺失新 token；同一信箱多個資料夾共用時沒有鎖。（`internal/auth/microsoft.go`）
- [x] **派送器細節**：
  - 資料庫持續無法刪除已送出的項目時，每個週期都會重送（資料庫壞掉時整個服務已失效，影響有限）。
  - 429／純文字改送也會累加 attempts，讓之後的退避時間偏長。
  - 非 `SendError` 的錯誤會原樣寫入日誌（目前不會發生，防禦性處理）。
  - 一般訊息遇到找不到 Bot 時只記錄，沒有告警。
  - `lastSent` map 不會清理。
  （`internal/dispatcher/dispatcher.go`）
- [x] **Telegram 用戶端**：HTTP 200 但回應無法讀取時被當成暫時性錯誤，可能造成重複訊息；`ErrorKind.String()` 超出範圍會 panic；`HTTP` 為 nil 會 panic。（`internal/telegram/telegram.go`）
- [x] **長度計算單位**：Telegram 的 4096 上限以 UTF-16 單位計算，目前以 rune 計算，主旨含大量 emoji 時可能仍超過。（`internal/render/render.go`、`internal/alert/alert.go`）
- [x] **範本截斷順序**：目前先清空主旨，再移除過長網址；應先嘗試移除過長網址，盡量保留主旨。主旨被清空時會留下「📧 信箱 ·」。（`internal/render/render.go`）
- [x] **範本試算只用完整資料**：`{{if not .URL}}{{.Nope}}{{end}}` 這類範本能通過啟動檢查，執行時才失敗；可再用空值資料試算一次。（`internal/render/render.go`）
- [x] **辨識規則細節**：
  - 訂單關鍵字以子字串比對，`border`／`reorder` 也會命中。
  - 電話號碼判斷較脆弱（例如「654321 - 12345 users」）。
  - 連結的網址提示詞同時比對主機名稱（規格為路徑與參數）；token 只檢查同名參數的第一個值。
  - 自訂規則的 `link_regex` 回傳比對到的子字串，應在文件說明需比對整個網址。
  - 可選擷取群組沒有參與比對時，規則會落空。
  - `Chain` 會修改階段回傳的 `Result`。
  （`internal/recognize/`）
- [x] **郵件解析細節**：part 讀取錯誤被忽略、5 MiB 截斷無測試；`<pre>` 內的縮排被移除；全形標點（：；！？）會黏在網址結尾。（`internal/mailparse/`）
- [x] **設定檔細節**：
  - 未加引號的 `${X}` 若展開成 `null`／`~` 會變成空字串（驗證會擋下空密碼與 token）。
  - `0s` 與未設定無法區分。
  - 錯誤排序用字串排序，`rules[10]` 會排在 `rules[2]` 前面。
  - 程式自行建構含 nil `*Mailbox` 的 Config 時 `Validate` 會 panic。
  （`internal/config/`）
- [x] **資料庫細節**：`data_dir` 含 `?`、`#`、`%` 時 SQLite URI 會出錯；讀取版本與遷移不在同一交易內。（`internal/store/store.go`）
- [x] **CLI 細節**：`login`、`send-test` 的參數若放在位置參數之後不會被解析；HTTP `Shutdown` 錯誤被忽略；`pruneLoop` 未等待結束；`main.go` 的 `goto parsed` 可改用標籤 break。（`cmd/relay/`）
- [x] **健康檢查**：重複的 key 可能顯示 connected 卻回傳 503（已修正）。
- [x] **健康檢查**：沒有任何信箱時回傳 200——延到 Web 管理介面設計時與探針一併決定（全新安裝本來就沒有信箱）。（`internal/health/health.go`）
  - 2026-10-20：以 Web 管理介面的探針設計定案：readiness／liveness 改用 `:8081` 的 `/readyz`、`/livez`，不看信箱狀態；`/healthz` 只供查看信箱明細，沒有信箱時回 200 是正確行為（全新安裝尚未設定信箱，不應視為不健康）。
- [x] **告警**：去重用的 map 不會清理；`alert_ttl` ≤ 0 未在告警模組防護（設定驗證已擋下負值）。（`internal/alert/alert.go`）

## 二、測試補強

- [x] config：YAML 層級的 `$$`、多個錯誤同時回報、`microsoft_oauth`／只有 `link_regex`／`ignore` 規則的通過案例、targets／mailboxes／rules 的名稱檢查、`link_regex` 編譯錯誤、空的 `admin`。
- [x] store：WAL 模式、`user_version`、-wal／-shm 檔案權限的斷言；在最後一步失敗的原子性測試；測試中被忽略的設定錯誤。
- [x] recognize：email 排除、「元」、訂單／invoice、同分取先、關鍵字前方距離加倍、大小寫不敏感、連字號分段、`may` 不鄰數字時不當月份、`extra_link_keywords`、`?` 萬用字元、mailto／x.com、中文排除詞、無群組的 `code_regex`、透過 `New` 的規則落空。
- [x] corpus：預期檔以 `KnownFields` 嚴格解析並檢查「恰好一種」；偵測孤立的 `.expected.yaml`；加入更難的干擾案例（關鍵字前的數字、表格中的驗證碼、Big5 連結信）。
- [x] render：不要忽略 `Render`／`New` 的錯誤；硬截斷、大量 `&` 的主旨、Service／FromName／To 的跳脫、信箱層級 link／both 覆寫、沒有驗證碼也沒有連結的錯誤。
- [x] telegram：429 預設 5 秒、description 中的 token 遮蔽、其他 5xx、200 但 `ok:false`、context 取消。
- [x] dispatcher：`forward_log.result` 的值、`Run`／`Wake`／停止流程、同聊天室的先後順序。
- [x] alert：寫入失敗、連續兩次 Recovered、60 分鐘內仍去重、`-race`、未對齊的 HTML 實體截斷邊界。
- [x] auth：授權逾時、`slow_down`、context 取消、`interaction_required`、錯誤訊息不含 token；測試伺服器的 `forms` 需加鎖。
- [x] pipeline：ignored／skipped 的 kind 與 result、成功時的 Kind、範本失敗路徑。
- [x] watcher：能偵測 drain 遺漏的測試（需要會延後回報 EXISTS 的連線包裝）、略過信件時確實沒抓內文、black-hole 測試斷言 `errDead`、UIDNEXT 的 STATUS／SEARCH 備援、信件在兩次 FETCH 之間被刪除、`toEnvelope`、取消後的 `StatusStopped`、「只發一次」斷線告警、needs_login 文字、認證恢復、認證失敗轉為網路斷線的告警；`waitIdle` 等的是用戶端送出 IDLE，不是伺服器完成註冊（極少數情況可能不穩）。
- [x] health：`Content-Type`、非 GET 請求、`Unmarshal` 錯誤檢查。
- [x] cmd：不帶 `--mailbox` 的 `test`、`test --send`、未知的 `--mailbox`、`healthcheck` 由 `http_listen` 推導網址、umask 與 prune；`TestRunSmoke` 的 umask 會影響整個測試行程。

## 三、文件

- [x] README：Docker 環境下 `relay test --eml` 需掛載信件檔的範例；`http_listen` 留空或 `/healthz` 回 503 時容器會顯示不健康的說明；手動驗證清單補上垃圾郵件匣、容器內 `login`、`test --send`。
- [x] Dockerfile：映像內的 `/data` 預設不屬於 UID 65532，沒有掛載時無法寫入。
- [x] `alert` 套件的匯出型別與函式補上註解，並註明告警文字會寫入日誌，呼叫端不可放入機密。

## 四、第二輪發現（2026-10-20 處理 TODO 時記下，尚未處理）

- [ ] 派送器：被 Forbidden／BadRequest 拒絕後若刪除待發訊息也失敗，每個週期會再送一次並再失敗（資料庫壞掉時才會發生）。（`internal/dispatcher/`）
- [ ] 派送器：「已送出但刪不掉」的 id 只記在記憶體，重啟後最多重送一次。
- [ ] 辨識：只用空白分隔的電話號碼（例如 `0800 123 456`）不會被排除。（`internal/recognize/`）
- [ ] watcher：認證失敗後轉成網路斷線時，停機時間從第一次認證失敗起算，可能立刻發斷線告警；補抓失敗時日誌仍寫「連線中斷，稍後重試」。（`internal/watcher/run.go`）
- [ ] 設定：`defaults` 的時間長度設成 0 或負值時，同一個錯誤會在 `defaults.*` 與沿用它的各信箱重複列出。（`internal/config/`）

## 五、Web 管理介面延後項目（2026-10-20，web-admin 分支審查時記下）

- [x] 「測試連線」的 Microsoft token 更新若剛好碰上同時進行的存檔（改主機），可能把已刪除的 token 寫回；改用與授權流程相同、在 saveMu 下核對帳號的 store 包裝。（`internal/web/page_mailboxes.go`）（已修正）
- [x] `relay config import` 不會清除因主機／帳號變更而失效的 OAuth token；改主機請在 Web 上操作，或匯入後重新授權。（`cmd/relay/configcmd.go`）（已修正）
- [x] 信箱改成 Microsoft 授權時，若資料庫殘留舊 token 不會被清掉。（`internal/web/page_mailboxes.go` `tokenInvalidated`）（已修正）
- [ ] 服務執行中用 `relay config import` 改了主機或帳號時，舊服務若在重新啟動前剛好更新 token，會把已清除的 token 寫回（CLI 無法停住執行中的服務）；匯入後請立即重新啟動。可改由 runtime 的 token store 在儲存前核對資料庫中最新的設定。（`internal/runtime`）
- [ ] 登入限速是全域的（每秒 1 次），網路上任何人都能以約每秒一次的請求讓管理員難以登入；可考慮在全域上限下再加每 IP 的限速。（`internal/web/server.go`）
- [ ] 每次存檔都會重建所有信箱連線並重置告警去重狀態；可改為只重建有變更的信箱。（`internal/runtime`）
- [ ] 總覽：短暫重新連線就整頁轉紅、「這段期間收到的驗證碼不會轉發」說法過重（重連後會補抓）；輪詢錯誤沒有退避。（`internal/web/page_dashboard.go`、`static/app.js`）
- [ ] 範本錯誤（Go 原文）、正規表示式錯誤仍以英文顯示。
- [ ] 信箱列表不會自動更新狀態（總覽會）；表單驗證失敗後未勾選的伺服器資料夾會消失。
- [ ] 失敗表單送出後網址列停在 POST 路徑，重新整理會看到 404。
