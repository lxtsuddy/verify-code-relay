# verify-code-relay Web 管理介面設計規格

- 日期：2026-10-20
- 狀態：已確認設計，待撰寫實作計畫
- 前一份規格：`2026-10-19-verify-code-relay-design.md`（第一版，YAML 設定）。本規格取代其中「第一版不做 Web UI、不做熱重載」的決定；兩份衝突時以本規格為準。

## 1. 目標

以命令列與 YAML 管理設定太困難。提供一個 Web 管理介面，**所有設定都能在網頁上完成**：新增 Telegram Bot 與目標、新增信箱帳號、設定轉發路由、規則、範本、預設值，並在網頁上完成 Microsoft 授權。存檔即生效，不需要重新部署。

### 已確定的決策

| 項目 | 決策 |
|---|---|
| 設定來源 | SQLite（與現有狀態同一個資料庫）。YAML 只用於一次性匯入與備份匯出 |
| 儲存形式 | 整份設定一筆文件，每次存檔新增一個版本，可還原；保留最近 50 版 |
| 套用方式 | 存檔即驗證、寫入並在程式內重載，不重啟程序 |
| 技術 | 同一個程式內建 Web；Go `html/template` 伺服器渲染＋少量原生 JS，靜態檔以 `embed` 打包；不引入 Node |
| 對外存取 | 使用者的 Caddy（公網、處理 TLS）→ K3s Traefik Ingress → Pod。介面暴露在公網，權限驗證必須嚴謹 |
| 帳號 | 單一管理員，密碼＋TOTP |
| 鎖定機制 | 不做帳號／IP 鎖定；改用全域限速與 Telegram 通知（§3.3） |
| 前端設計 | 實作頁面時使用 frontend-design skill，做出好看、互動完善、體驗好的介面 |

### 第一版範圍

- 設定管理：Bot、目標、信箱（含轉發路由）、自訂規則、辨識關鍵字、全域與信箱範本、預設值、admin 目標、日誌等級。
- 網頁上的 Microsoft device code 授權。
- 狀態總覽與轉發紀錄。
- 發送測試訊息（目標）、測試連線（信箱）。
- 修改密碼、重新綁定 TOTP、設定版本歷史與還原。

### 不做

多帳號與角色權限、`.eml` 上傳測試（保留 CLI `relay test --eml`）、帳號／IP 鎖定、備用救援碼、設定差異比對畫面、SPA 前端。

## 2. 架構

### 2.1 程序層級設定（環境變數）

資料庫位置必須在開啟資料庫前就知道，因此以下設定來自環境變數，不存資料庫：

| 變數 | 預設 | 說明 |
|---|---|---|
| `RELAY_DATA_DIR` | `/data` | SQLite 資料庫目錄 |
| `RELAY_LISTEN` | `:8080` | Web 管理介面 |
| `RELAY_HEALTH_LISTEN` | `:8081` | 內部健康檢查（不對外）；空字串代表不啟動 |
| `RELAY_ADMIN_PASSWORD` | （無） | 只在資料庫還沒有管理員時用來建立管理員 |
| `RELAY_TRUSTED_PROXIES` | （空） | 以逗號分隔的 CIDR；只影響記錄與通知中的 IP 是否準確 |

其餘設定全部存在資料庫。`config.Config` 的 `data_dir`、`http_listen` 欄位在匯入時略過並提示（保留欄位以相容舊 YAML）。

### 2.2 元件

| 套件 | 職責 |
|---|---|
| `internal/store` | 新增資料表：`config_versions`、`admin`、`sessions`（§2.3） |
| `internal/config` | 新增：序列化（`Config` → 原始 YAML，只輸出明確設定的欄位）、不展開 `${ENV}` 的解析入口；空設定（沒有信箱）視為合法 |
| `internal/runtime`（新） | 持有「目前運作中的服務」；`Apply(cfg)` 重載；提供狀態查詢 |
| `internal/web`（新） | 路由、登入／session／CSRF、限速、各頁 handler、範本、`embed` 靜態檔 |
| `internal/telegram` | 新增 `GetMe`、`GetUpdates` |
| `internal/watcher` | 新增「測試連線並列出資料夾」，重用現有 dial 與 auth |
| `internal/health` | 新增 `/livez`、`/readyz`；`/healthz` 不變 |
| `cmd/relay` | `run` 改從資料庫載入設定並啟動 Web；CLI 調整（§2.6） |

既有的 pipeline、dispatcher、alert、recognize、render、mailparse 不變。現有 `newService` 的組裝邏輯搬進 `runtime`。

### 2.3 資料表

```sql
CREATE TABLE config_versions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at INTEGER NOT NULL,
    source     TEXT NOT NULL,      -- web | import | restore | bootstrap
    ip         TEXT,               -- web 來源的用戶端 IP
    summary    TEXT NOT NULL,      -- 例如「新增信箱 outlook」，不含機密
    yaml       TEXT NOT NULL       -- 使用者輸入的原始設定（未套預設值、未展開 ${ENV}），含機密
);
CREATE TABLE admin (
    id             INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash  TEXT NOT NULL,   -- argon2id，PHC 字串格式
    totp_secret    TEXT,            -- base32；NULL 代表尚未綁定
    totp_pending   TEXT,            -- 綁定流程中的暫存金鑰
    totp_last_step INTEGER NOT NULL DEFAULT 0,  -- 已用過的最大時間步，防止重放
    updated_at     INTEGER NOT NULL
);
CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,    -- SHA-256(token)，資料庫不存原始 token
    csrf       TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    ip         TEXT,
    user_agent TEXT
);
```

- 目前設定 = `config_versions` 中 id 最大的一筆；沒有任何一筆時視為空設定。
- 寫入新版本後刪除超過最近 50 版的舊版，避免舊機密無限期留存。
- 資料庫檔維持 0600。Bot token、應用程式密碼、TOTP 金鑰以明文存在資料庫，與既有 OAuth refresh token 屬同一風險等級；資料庫與 PVC 備份需妥善保管（README 註明）。

### 2.4 存檔與重載流程

1. handler 讀出目前設定（原始形式），套用表單修改。
2. 套預設值後執行與 `relay config validate` 相同的檢查：`Validate`、`recognize.New`、`render.New`。
3. 失敗：回到原表單，錯誤依路徑（例如 `mailboxes.outlook.host`）標在對應欄位旁；未對應到欄位的錯誤顯示在表單頂端。不寫入資料庫。
4. 成功：寫入新版本（含 summary、來源、IP），呼叫 `runtime.Apply`，以 Post/Redirect/Get 導回並顯示「已儲存並套用」。

`runtime.Apply(cfg)`：

- 以互斥鎖序列化，同一時間只有一次重載。
- 停掉舊服務（cancel context → 等待 watchers、dispatcher、pruneLoop 結束；超過 30 秒記錄警告並繼續等待），再以新設定建立並啟動服務。資料庫與 HTTP 伺服器不中斷。
- 重載期間狀態為「套用中」。IMAP 連線會重建；補抓依 `last_uid`，不漏信；outbox 在資料庫中，不遺失。
- 驗證與編譯（`runtime.Build`）在寫入資料庫與 `Apply` 之前完成，`Apply` 只接受已建好的設定，因此不會失敗，不需要回復前一版的機制。

`runtime` 同時提供狀態快照：各 watcher 的 key 與狀態、待發佇列數量、服務是否套用中。

### 2.5 名稱與刪除

- 信箱、Bot、目標的**名稱**是內部鍵（信箱狀態與 OAuth token 以信箱名稱儲存），**建立後不可修改**；可修改的是信箱的「顯示名稱」。
- 刪除仍被引用的 Bot 或目標時擋下，並列出引用它的目標／信箱（admin 目標也算引用）。
- 刪除信箱時一併刪除其 OAuth token；`mailbox_state` 保留無害。

### 2.6 CLI

| 指令 | 變更 |
|---|---|
| `relay run` | 從資料庫載入設定，啟動服務、Web 與健康檢查；資料庫沒有管理員時以 `RELAY_ADMIN_PASSWORD` 建立，未設定則記錄提示、Web 只顯示「尚未設定管理員」 |
| `relay config import <yaml>` | 讀檔並展開 `${ENV}`，驗證後存成新版本（source=import） |
| `relay config export` | 輸出目前設定的 YAML（含機密），用於備份 |
| `relay config validate <yaml>` | 保留，匯入前檢查 |
| `relay admin reset` | 互動輸入新密碼並清除 TOTP；用於忘記密碼或遺失手機 |
| `relay login`、`send-test`、`test --eml` | 改從資料庫讀設定；`login` 保留作為備援，完成後需重新啟動 Pod（Web 授權則自動套用） |
| `relay healthcheck` | 改打 `RELAY_HEALTH_LISTEN` 的 `/readyz` |

`--config`／`RELAY_CONFIG` 只剩 `import`、`validate` 使用。

## 3. 認證與安全

### 3.1 首次設定與救援

- 資料庫沒有管理員時，以 `RELAY_ADMIN_PASSWORD` 建立（argon2id 雜湊）。之後此變數不再使用，改密碼在 Web 上進行。
- 第一次登入（只需密碼）後，session 處於「待綁定 TOTP」狀態（由 `admin.totp_secret IS NULL` 推導，不另存欄位），除了綁定頁與登出外所有頁面都導向綁定頁。綁定頁顯示 QR code（`otpauth://` URI）與手動輸入用的 base32 金鑰，輸入一組正確驗證碼後才完成綁定。
- 不提供「第一個訪客建立帳號」的流程，避免公網上被搶先建立。
- 救援：`kubectl exec -it ... /relay admin reset`。

### 3.2 登入

- 單一表單：密碼＋6 位數驗證碼（已綁定時）。任何失敗只顯示「登入失敗」，不透露是哪一項錯誤。
- 密碼：argon2id（`golang.org/x/crypto/argon2`），以固定時間比較。
- TOTP：自行實作 RFC 6238（HMAC-SHA1、30 秒、6 位數、容許前後各一步）；已使用過的時間步（`totp_last_step`）不能再用。QR code 以 `rsc.io/qr` 產生 PNG。
- 登入成功後重新產生 session token（防 session fixation）。
- 修改密碼需輸入目前密碼與驗證碼；重新綁定 TOTP 需輸入目前驗證碼。修改密碼後其他 session 全部失效。

### 3.3 防暴力破解（不鎖定）

- **全域限速**：所有登入嘗試合計 token bucket，每秒 1 次、容量 5；超過時直接回「請稍後再試」，不執行 argon2。只限速，不鎖任何人。
- **密碼正確但驗證碼錯誤**：立即發 Telegram 告警到 admin 目標（附 IP、User-Agent），內容建議立刻修改密碼。頁面上與其他失敗無法區分。
- **登入成功通知**：發到 admin 目標，附 IP 與 User-Agent。
- **失敗摘要**：一小時內失敗超過 10 次時發一則摘要（每小時最多一則）。
- 「密碼正確但驗證碼錯誤」走 `alert` 套件（一小時去重即可）。登入成功通知與失敗摘要**不經去重**，直接寫入 outbox 送往 admin 目標，每次登入都會通知。未設定 admin 目標時只寫日誌。通知文字不含密碼或驗證碼。

### 3.4 Session 與請求防護

- Cookie：`__Host-relay_session`，`HttpOnly`、`Secure`、`SameSite=Strict`、`Path=/`。瀏覽器在 `http://localhost`（port-forward）也接受 Secure cookie。
- 閒置 30 分鐘或建立滿 12 小時失效；過期 session 定期清除；提供登出。
- 所有非 GET 請求：檢查表單中的 CSRF token（與 session 綁定）**且** `Origin`（或缺少時的 `Referer`）必須與請求的 Host 相同，scheme 必須是 https（只有 localhost／loopback 允許 http，供 port-forward 使用）。
- 回應標頭：`Content-Security-Policy: default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'`、`X-Frame-Options: DENY`、`X-Content-Type-Options: nosniff`、`Referrer-Policy: same-origin`（站外不送 referrer；`no-referrer` 會讓瀏覽器在同站表單 POST 送出 `Origin: null`，使 Origin 檢查無法運作）、`Cache-Control: no-store`（靜態檔除外）。另外 `Sec-Fetch-Site` 存在且不是 `same-origin` 的非 GET 請求一律拒絕（縱深防禦）。
- 除登入頁、靜態檔外的所有路由都需要已登入且已綁定 TOTP 的 session。
- 機密欄位（Bot token、應用程式密碼）**只能寫入**：頁面只顯示「已設定」，送出空值代表不變；任何頁面、錯誤訊息、日誌都不得包含機密。
- 用戶端 IP：預設取連線來源位址；來源屬於 `RELAY_TRUSTED_PROXIES` 時，從 `X-Forwarded-For` 由右往左略過受信任位址，取第一個不受信任的位址。IP 只用於記錄與通知，不用於任何安全判斷。系統設定頁顯示「目前被判定的 IP」以便確認代理設定。

## 4. 頁面

導覽：總覽、信箱、Telegram、規則與範本、轉發紀錄、系統設定。視覺與互動細節在實作時以 frontend-design skill 設計；以下為功能需求。

**CSP 限制**：所有 CSS／JS 都是 `embed` 的獨立檔案，不使用 inline `<style>`、`style=` 屬性、inline `<script>` 或 `onclick` 等事件屬性；字型使用系統字型或打包進執行檔，不載入任何外部資源（CDN、Google Fonts）。

### 4.1 總覽

- 每個「信箱／資料夾」一張狀態卡（已連線、補抓中、重新連線中、需要授權、認證失敗、已停止）；需要授權的卡片有「重新授權」按鈕。
- 待發佇列數量、最近 20 筆轉發紀錄、服務是否套用中。
- 每 10 秒以 fetch 更新狀態區塊（JSON 端點，同樣需要登入）。

### 4.2 信箱

- 列表：顯示名稱、帳號、資料夾數、目標、狀態。
- 新增／編輯表單：
  - 服務類型「Gmail／iCloud／Outlook／自訂」自動帶入主機、埠號、認證方式，並顯示申請應用程式密碼或 Azure 註冊的步驟提示。
  - 帳號、應用程式密碼（只寫）或 Microsoft `client_id`、`tenant`。
  - **轉發路由**：勾選要送往的目標（至少一個）。
  - 資料夾：「測試連線」實際登入並列出伺服器的資料夾供勾選；也可手動輸入。
  - 進階（預設收合）：`max_age`、`ttl`、`poll_interval`、信箱專用範本。
- 「測試連線」使用表單目前的值（密碼欄留空時用已儲存的密碼），20 秒逾時，回報成功與資料夾清單或錯誤原因。Microsoft 信箱需先完成授權才能測試。
- 「授權」（Microsoft）：彈出視窗顯示 user code（一鍵複製）與驗證網址；瀏覽器每 3 秒輪詢進度；成功後存 token 並觸發 `runtime.Apply`；逾時、被拒或錯誤時顯示原因並可重試。同一信箱同時只有一個授權流程；flow 存在記憶體，程序重啟即失效。

### 4.3 Telegram

- Bot：名稱、token（只寫）。新增或更換 token 時以 `getMe` 驗證並顯示 Bot 的 @username。
- 目標：名稱、Bot、chat_id。「讀取最近對話」以 `getUpdates` 列出最近傳訊給 Bot 或 Bot 所在的聊天室（名稱、類型、chat_id），點選即帶入表單。Bot 若設有 webhook，`getUpdates` 會失敗，顯示說明。
- 每個目標有「發送測試」按鈕。
- admin 目標在系統設定頁選擇。

### 4.4 規則與範本

- 自訂規則列表：可上移／下移調整順序；欄位 `name`、`from`、`subject`、`code_regex`、`link_regex`、`ignore`，旁附欄位說明（同 README）。
- 辨識關鍵字：`extra_code_keywords`、`extra_link_keywords` 標籤式輸入。
- 全域範本 `code`／`link`／`both`：編輯器旁列出可用變數，並以範例資料即時渲染 **Telegram 訊息預覽**（伺服器端 `render` 試算，失敗時顯示錯誤）。

### 4.5 轉發紀錄

`forward_log` 分頁列表（時間、信箱、資料夾、寄件者、主旨、種類、規則、結果），可依信箱與結果篩選。主旨沿用既有遮蔽；不含驗證碼與網址。

### 4.6 系統設定

- 預設值（`max_age`、`ttl`、`alert_ttl`、`poll_interval`、`alert_after`）、admin 目標、日誌等級。
- 修改密碼、重新綁定 TOTP、目前被判定的 IP。
- 設定版本歷史：時間、來源、IP、摘要；「還原此版本」= 以該版內容驗證後存成新版本（source=restore）並套用。

## 5. 健康檢查與部署

### 5.1 探針

- `:8080`：只有 Web。
- `:8081`（`RELAY_HEALTH_LISTEN`）：
  - `/livez`：程序存活，固定 200。
  - `/readyz`：HTTP 正常且資料庫可查詢時 200。
  - `/healthz`：沿用現有各信箱狀態明細（沒有信箱時 200）。
- readiness 改用 `/readyz`，**不依信箱狀態**，因此 Outlook 需要重新授權時 Web 仍可連線；信箱問題由 Telegram 告警與總覽頁呈現。liveness 用 `/livez`。
- Dockerfile `HEALTHCHECK` 改用 `relay healthcheck`（打 `:8081/readyz`）。

### 5.2 K3s（`deploy/k3s/`）

- 移除 `configMapGenerator` 與 config volume；`secretGenerator` 的 `secret.env` 只放 `RELAY_ADMIN_PASSWORD`。
- Deployment：env 加入 `RELAY_TRUSTED_PROXIES`（範例值，選配）；探針改為上述端點；PVC、單一 replica、`Recreate`、唯讀根檔案系統不變。
- 新增 Service（ClusterIP，port 80 → 8080）與 Ingress（Traefik，`host` 為範例網域，不設 TLS，由 Caddy 處理）。8081 不經 Service 暴露。

### 5.3 README

- Caddy 設定範例（`reverse_proxy` 到 Traefik，保留 Host）。
- 選配：K3s `HelmChartConfig` 設定 Traefik `forwardedHeaders.trustedIPs` 包含 Caddy 的 IP，否則通知中的 IP 會是 Caddy 的位址；搭配系統設定頁的「目前被判定的 IP」確認。
- 首次部署：apply → 以 Secret 中的密碼登入 → 綁定 TOTP → 在 Web 新增設定，或 `relay config import`。
- 從現有 ConfigMap 部署遷移：先 `kubectl exec -i ... /relay config import /dev/stdin < config.yaml`（需能展開的環境變數仍在 Secret 中），確認後再套用新的 manifests 並把機密從 `secret.env` 移除。
- 部署後到首次登入綁定 TOTP 之前，任何知道密碼的人都能完成綁定：部署後應立即登入綁定，`RELAY_ADMIN_PASSWORD` 要夠長且隨機。
- 資料庫含所有機密，備份需妥善保管。
- 手動驗證清單加入 Web 相關項目（§6.4）。

## 6. 測試

### 6.1 單元測試

- TOTP：RFC 6238 測試向量、前後一步容許、重放拒絕。
- 密碼：argon2id 雜湊與驗證、錯誤格式。
- 限速：token bucket 行為。
- 用戶端 IP：無代理、受信任代理鏈、偽造的 `X-Forwarded-For`。
- 設定序列化：原始設定 → YAML → 解析的往返相等；不展開 `${...}`；只輸出明確設定的欄位；空設定合法。
- `runtime.Apply`：啟停順序、重載期間狀態、序列化、失敗回復前一版。
- store：新資料表、版本保留 50 版、session 過期清除。

### 6.2 Handler 測試（httptest）

- 表格列出**每一條路由**：未登入、已登入未綁定 TOTP 時都被擋下或導向。
- 非 GET 請求：缺 CSRF token、token 錯誤、`Origin` 不符都被拒絕。
- **回應內容不含機密**：以含特殊標記的 token／密碼建立設定，掃描所有頁面與 JSON 端點的回應。
- 登入：成功、各種失敗訊息一致、限速、「密碼正確但驗證碼錯誤」發告警、成功通知、session 重新產生。
- 首次登入強制綁定 TOTP。
- 存檔：驗證錯誤標到欄位、成功寫入版本並呼叫 Apply、刪除被引用項目被擋下、名稱不可修改。
- 授權流程：以假的 Microsoft 端點跑 device code 成功、逾時、拒絕。

### 6.3 端對端

空資料庫啟動 → 以 HTTP 用戶端登入並綁定 TOTP → 透過表單新增 Bot、目標、信箱（記憶體 IMAP 伺服器＋假 Telegram）→ 寄信後收到轉發 → 修改信箱目標 → 確認重載後依新路由轉發。

### 6.4 手動驗證

- 以瀏覽器走過所有頁面（桌機與手機寬度）。
- 經 Caddy → Traefik 的真實部署：登入、TOTP 綁定、Cookie 正常、通知中的 IP 正確。
- 網頁上完成 Outlook 授權。
- 「讀取最近對話」取得 chat_id。
