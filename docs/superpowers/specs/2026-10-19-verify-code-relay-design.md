# verify-code-relay 設計規格

- 日期：2026-10-19
- 狀態：已確認設計，待撰寫實作計畫

## 1. 目標

一個常駐服務：以 IMAP IDLE 監看多個電子信箱，辨識「驗證碼信件」與「認證連結信件」（含重設密碼連結），擷取驗證碼／連結，套用範本後透過指定的 Telegram Bot 轉發到指定聊天室。可同時管理多個 Telegram Bot 與多個信箱。

靈感來源：
- 轉發機制：[smtp_to_telegram](https://github.com/KostyaEsmukov/smtp_to_telegram)（但改用 IMAP 拉取，而非內建 SMTP 伺服器）
- 辨識機制：[XposedSmsCode](https://github.com/magisk317/XposedSmsCode)（關鍵字＋正規表示式＋排除規則）

### 已確定的決策

| 項目 | 決策 |
|---|---|
| 郵件來源 | IMAP 連到現有信箱，以 IDLE 即時接收；伺服器不支援 IDLE 時自動退回輪詢 |
| 管理方式 | 第一版用 YAML 設定檔；架構預留以後加 Web UI |
| 路由 | 以信箱為單位：每個信箱對應一或多個目標（Bot + chat_id） |
| 辨識 | 第一版只用規則；`Recognizer` 介面預留 LLM 辨識器 |
| 語言／部署 | Go；Docker（docker-compose）為主，也可直接執行 |
| 未辨識的信 | 忽略（只記日誌）；提供 `relay test --eml` 除錯指令 |
| 對信箱的影響 | 完全唯讀：不標已讀、不移動、不刪除 |
| 認證 | 應用程式密碼（Gmail、iCloud、自架等）＋ Microsoft OAuth2（XOAUTH2、device code 流程） |
| 狀態儲存 | SQLite（`modernc.org/sqlite`，純 Go、不需要 cgo），含待發送佇列 |
| 系統告警 | 選填的 `admin` 目標 |
| 重設密碼連結 | 算作認證連結並轉發 |
| 自動刪除 Telegram 訊息 | 不做 |

### 第一版不做

Web UI、LLM 辨識、依信箱分別設定的自訂規則、設定檔熱重載、Gmail OAuth、自動刪除 Telegram 訊息、內建 SMTP 伺服器、轉發所有信件。

## 2. 架構

### 2.1 資料流

```
[IMAP 信箱 ×N] ──IDLE──▶ Watcher（每個「信箱＋資料夾」一個 goroutine、一條連線）
                              │ UID FETCH <last_uid+1>:*  (BODY.PEEK[])
                              ▼
                         mailparse（MIME 解碼 → Message）
                              │
                              ▼
                         recognize（辨識器鏈 → Result 或不符合）
                              │ 符合
                              ▼
                         render（對每個目標各渲染一則訊息）
                              │
                              ▼  單一交易：INSERT outbox × 目標數 ＋ UPDATE mailbox_state.last_uid
                         store（SQLite）
                              │
                              ▼
                         dispatcher（取出 → telegram.sendMessage → 成功才刪除）
```

不論是否辨識成功，處理完的信都要推進 `last_uid`。未辨識、超過 `max_age`、超過大小上限的信，只寫日誌（以及 `forward_log`），不寫入 outbox。

**投遞保證：** 寫入 outbox 和推進 `last_uid` 在同一個交易內完成，因此任何時間點當機都不會漏信，也不會重複處理同一封信。唯一可能重複的情況是：Telegram 已收到訊息，但程式在刪除 outbox 項目前當機，重啟後會重送一次。這個情況可以接受。

### 2.2 套件與職責

| 套件 | 職責 | 主要依賴 |
|---|---|---|
| `cmd/relay` | `main`、CLI 子指令、組裝各元件 | 全部 |
| `internal/config` | 讀取 YAML、展開 `${ENV}`、驗證、套用預設值 | `gopkg.in/yaml.v3` |
| `internal/store` | SQLite 存取：信箱狀態、OAuth token、outbox、轉發紀錄；schema 遷移 | `modernc.org/sqlite` |
| `internal/auth` | `Authenticator` 介面；`password` 與 `microsoft_oauth` 實作；XOAUTH2 SASL client | `store` |
| `internal/watcher` | IMAP 連線生命週期、IDLE、輪詢備援、重連退避、抓新信 | `go-imap/v2`、`auth`、`store` |
| `internal/mailparse` | MIME 解碼、字元集、HTML 轉純文字、抽出連結 | `go-message`、`golang.org/x/net/html` |
| `internal/recognize` | `Recognizer` 介面、規則辨識器、自訂規則、`Trace` | `mailparse` |
| `internal/render` | `html/template` 範本渲染、長度截斷、按鈕組裝 | `recognize` |
| `internal/telegram` | Bot API `sendMessage` HTTP client、錯誤分類 | `net/http` |
| `internal/dispatcher` | 消化 outbox：限速、重試、過期丟棄、告警 | `store`、`telegram` |
| `internal/alert` | 系統告警：寫入 outbox（送往 `admin`）並去除重複 | `store` |
| `internal/health` | `/healthz` HTTP handler，彙整各 watcher 狀態 | `watcher` |

每個套件只透過介面依賴其他套件，以便單元測試。日後的 Web UI 會讀寫同一個 SQLite 資料庫並共用 `config` 的驗證邏輯，不需要更動處理流程。

## 3. 郵件接收（watcher）

### 3.1 連線生命週期

每個「信箱＋資料夾」使用一條獨立的 IMAP 連線（IDLE 只能監看目前 SELECT 的資料夾）。資料夾預設為 `[INBOX]`。

1. 以 TLS 連線到 `host:port`（port 預設 993）。第一版只支援隱式 TLS，不支援 STARTTLS。
2. 依 `auth.type` 認證（見 3.3）。
3. `SELECT <folder>`，取得 UIDVALIDITY 與 UIDNEXT。SELECT 回應中的 UIDNEXT 是選填的；沒有回傳時，改用 `STATUS <folder> (UIDNEXT)` 取得，再不行就用 `UID SEARCH ALL` 的最大 UID 加 1。
4. 比對 `mailbox_state`：
   - 沒有紀錄，或 UIDVALIDITY 和紀錄不同：把 `last_uid` 設成 `UIDNEXT - 1`，**不處理既有的信**，並寫日誌。
   - UIDVALIDITY 相同：執行一次補抓（見步驟 6）。
5. 伺服器支援 IDLE（IMAP4rev2 或 IDLE capability）時進入 IDLE；否則每 `poll_interval` 執行一次步驟 6。
6. 收到新信通知（unilateral `Mailbox` 更新中 `NumMessages` 改變）時：

   `UnilateralDataHandler` 的 callback 只負責對 channel 送出訊號，不可以在裡面呼叫任何 client 方法，否則會死結。停止 IDLE、FETCH 等動作都由 watcher 自己的 goroutine 執行。

   1. 停止 IDLE（`IdleCommand.Close()` 後 `Wait()`）
   2. `UID FETCH <last_uid+1>:* (UID INTERNALDATE RFC822.SIZE)`
   3. 跳過 `RFC822.SIZE > 5 MiB` 的信，以及 `INTERNALDATE` 早於 `now - max_age` 的信（寫日誌與 `forward_log`，結果標為 `skipped_size` 或 `skipped_age`）
   4. 其餘逐封 `UID FETCH <uid> (BODY.PEEK[])` → 解析 → 辨識 → 渲染 → 單一交易寫入 outbox 並推進 `last_uid`
   5. 回到 IDLE

   注意：`UID FETCH n:*` 在沒有新信時仍可能回傳最後一封信，因此必須過濾 `uid > last_uid`。

   每次被喚醒後，重複執行補抓，直到某一輪沒有任何新 UID，才回到 IDLE。原因是有些伺服器（例如 go-imap 的 imapmemserver）只回報 IDLE 期間發生的變化；處理信件期間到達的新信若不這樣補抓，要等到 25 分鐘的存活檢查才會被發現。
7. **存活偵測**：IDLE 期間伺服器可以長達約 29 分鐘不送出任何資料，所以**不能**設定一般的讀取逾時，否則會誤殺正常連線。改用以下做法：
   - watcher 每 25 分鐘主動循環一次：`Close` IDLE → `NOOP` → 重新 `Idle`。整個循環 30 秒內沒有收到伺服器回應，就視為連線失效，進入重連。
   - 同時啟用 TCP keepalive。
   - 不依賴 go-imap 內部的 IDLE 自動重啟機制來偵測存活，因為無法得知它的間隔，也不確定它會不會把逾時錯誤回報給呼叫端。撰寫實作計畫時，要確認 `imapclient` 實際的行為。

### 3.2 斷線重連

- 任何連線錯誤都會關閉該連線，然後以指數退避重連：1 秒起，每次加倍，上限 5 分鐘，加上 ±20% 的隨機抖動。連線成功、SELECT 並順利完成第一次補抓後，退避時間才歸零（同時清除斷線計時並發送「已恢復」通知）；若只是 SELECT 成功但處理信件持續失敗，退避會繼續加倍，避免每秒重連轟炸信箱服務商。
- 某個 watcher 連續無法連線超過 `alert_after`（預設 10 分鐘）時，發出一次告警；恢復時再發一次「已恢復」。
- 認證失敗（IMAP `NO [AUTHENTICATIONFAILED]` 或 OAuth `invalid_grant`）：
  - `password`：照樣退避重試（可能是暫時性問題），但立即告警，並把該 watcher 狀態標為 `auth_failed`。
  - `microsoft_oauth` 收到 `invalid_grant`：停止這個 watcher，狀態設為 `needs_login`，並告警「請執行 `relay login <信箱>`」。

重連邏輯透過一個可以注入的 `Dialer` 介面取得連線，讓測試能模擬各種斷線時間點。

### 3.3 認證

```go
type Authenticator interface {
    // Authenticate 在已連線的 client 上完成認證。
    Authenticate(ctx context.Context, c *imapclient.Client) error
}
```

- **`password`**：`LOGIN username password`。
- **`microsoft_oauth`**：
  - 端點：`https://login.microsoftonline.com/{tenant}/oauth2/v2.0/devicecode` 與 `.../token`
  - scope：`https://outlook.office.com/IMAP.AccessAsUser.All offline_access`
  - `relay login <信箱>`：執行 device code 流程，在終端機顯示驗證網址與代碼，輪詢 token 端點直到取得授權，把 refresh token、access token 與到期時間存入 `oauth_tokens`。
  - 執行期間，access token 剩不到 5 分鐘到期時，用 refresh token 更新；新的 refresh token 若有回傳，就覆蓋舊的（Microsoft 會輪替 refresh token）。
  - 以 SASL `XOAUTH2` 認證，初始回應格式為 `"user=" + username + "\x01auth=Bearer " + access_token + "\x01\x01"`。
  - 使用者必須自行在 Azure 註冊公開用戶端應用程式，並啟用「允許公開用戶端流程」。README 會附上步驟。
  - 實作時須以實際 Outlook.com 帳號驗證上述 scope 與 tenant（`consumers`）的組合。

## 4. 郵件解析（mailparse）

```go
type Address struct{ Name, Addr string }
type Link struct{ URL, Text string } // Text 為 <a> 的可見文字；純文字網址的 Text 為空

type Message struct {
    From    Address
    To      []Address // To + Cc + Delivered-To，去除重複
    Subject string    // 已解碼 RFC 2047
    Date    time.Time // Date 標頭；解析失敗時用 INTERNALDATE
    Text    string    // 用於辨識的純文字
    Links   []Link
}
```

- 使用 `github.com/emersion/go-message` 並匯入 `github.com/emersion/go-message/charset`，以支援 Big5、GB2312、GBK、GB18030、Shift_JIS、ISO-2022-JP 等字元集；quoted-printable 與 base64 由函式庫處理。
- `Text` 的來源：優先使用第一個 `text/plain` 部分；沒有的話，把第一個 `text/html` 部分轉成純文字。
- HTML 轉純文字：略過 `<style>`、`<script>`、`<head>`、`<img>`，以及帶有 `display:none` 的元素；區塊元素（`p`、`div`、`br`、`tr`、`li`、`h1`–`h6`）輸出換行；合併多餘空白。
- `Links`：HTML 部分的所有 `<a href>`（連同可見文字），加上 `Text` 中以正規表示式找到的 `http(s)://` 網址。兩個來源依網址去除重複，優先保留有可見文字的那筆。只保留 `http`、`https` 開頭的網址。
- 附件一律忽略。

## 5. 辨識（recognize）

### 5.1 介面

```go
type Result struct {
    Code    string // 可能為空
    URL     string // 可能為空；Code 與 URL 至少一個非空
    Service string // 寄件者顯示名稱；沒有的話用寄件者網域的主要部分（如 github）
    Rule    string // 命中的規則名稱，例如 "builtin:code"、"custom:github"
}

type Trace struct{ Steps []string } // 人類可讀的判斷過程，供 relay test 輸出

type Recognizer interface {
    // Recognize 回傳 nil Result 代表不符合；ignore 為 true 代表被規則明確略過。
    Recognize(msg *mailparse.Message) (res *Result, ignore bool, tr *Trace)
}
```

辨識器組成一條鏈，依序執行，第一個回傳非 nil 結果（或 `ignore`）的就生效：

1. 自訂規則（`rules`，依設定檔中的順序）
2. 內建規則：驗證碼與連結一起評估，任一項找到就回傳，兩項都找到就一起放進結果
3. （以後）LLM 辨識器

### 5.2 內建驗證碼規則

1. **關鍵字判斷**：`Subject` 或 `Text` 中出現下列任一關鍵字（不分大小寫），再加上 `recognize.extra_code_keywords`：
   - 中文：驗證碼、验证码、校驗碼、校验码、動態碼、动态码、確認碼、确认码、安全碼、安全码、認證碼、认证码、一次性密碼、一次性密码、登入碼、登录码
   - 英文：verification code、security code、confirmation code、authentication code、login code、sign-in code、one-time code、one-time password、OTP、passcode、2FA、two-factor、your code、code is
   - 日文：認証コード、確認コード、ワンタイムパスワード

   沒有任何關鍵字就不算驗證碼信。
2. **候選字串**（同時掃描 `Subject` 與 `Text`）：
   - 4–8 位純數字：`\b\d{4,8}\b`
   - 分段數字：`\b\d{3}[ -]\d{3}\b`，輸出時去掉空白或連字號
   - 4–8 位英數混合，至少含一個數字和一個大寫字母，不含小寫：`\b(?=[A-Z0-9]*\d)(?=[A-Z0-9]*[A-Z])[A-Z0-9]{4,8}\b`
3. **排除**：
   - 位於網址或 email 地址內
   - 前方 3 個字元內有貨幣符號或代碼（`$ NT$ US$ ¥ ￥ € £ 元 TWD USD RMB CNY`），或後方緊接 `元`
   - 時間格式的一部分（`\d{1,2}:\d{2}`）
   - 介於 1900–2099 的 4 位數，且同一行出現月份名稱、`年`、`/` 或 `-` 日期格式
   - 前方緊接 `+`，或屬於更長的數字串（含以空白或連字號分隔的電話格式，總長超過 8 位）
   - 前方 10 個字元內出現 `訂單`、`订单`、`order`、`invoice`、`#`
4. **評分**（初始值，以測試樣本調整；調整後必須讓所有樣本測試通過）：
   - 只考慮與最近關鍵字距離在 200 字元內的候選字串
   - 基礎分 = `100 - 距離 / 2`；候選字串位於關鍵字**之前**時，距離以 2 倍計
   - 出現在主旨中 +30；在內文中獨立成一行 +20；6 位純數字 +10
   - 取最高分；同分取先出現的；最高分低於 40 視為沒找到

### 5.3 內建連結規則

1. **關鍵字判斷**：`Subject` 或 `Text` 中出現下列任一關鍵字（不分大小寫），再加上 `recognize.extra_link_keywords`：
   - 英文：verify、verification、confirm、activate、activation、validate、sign in、log in、login link、magic link、reset password、reset your password、password reset
   - 中文：驗證、验证、確認、确认、啟用、启用、激活、登入、登录、重設密碼、重设密码、重置密碼、重置密码
2. **候選連結**：`Message.Links`。
3. **排除**：網址或連結文字含 unsubscribe、退訂、退订、取消訂閱、preferences、privacy、隱私、terms、條款、help、support、說明、`mailto:`、`tel:`；網域為常見社群網站（facebook、twitter/x、instagram、linkedin、youtube）；路徑以圖片副檔名結尾。
4. **評分**（初始值，原則同 5.2）：
   - 連結文字含關鍵字 +50
   - 網址路徑或參數含 verify、confirm、activate、validate、magic、reset、token=、code=、login +30
   - 任一參數值長度 ≥ 20 且由英數或 `-_` 組成 +15
   - 連結網域和寄件者網域的主要部分相同 +10
   - 取最高分；同分取先出現的；最高分低於 50 視為沒找到

點擊追蹤轉址連結（例如 `click.example.com/...`）照原樣使用，不另外解析。

### 5.4 自訂規則

```yaml
rules:
  - name: github                 # 必填，不可重複
    from: "*@github.com"         # 選填，寄件地址的萬用字元比對（* 和 ?，不分大小寫）
    subject: "(?i)verification"  # 選填，主旨的正規表示式
    code_regex: 'code:\s*(\d{6})'   # 對 Subject + "\n" + Text 比對，取第一個擷取群組；沒有群組時取整段
    link_regex: 'https://github\.com/\S+'  # 對 Links 的 URL 比對，取第一個命中的
    ignore: false                # true 代表明確略過，不轉發
```

- 每條規則至少要有 `from` 或 `subject` 其中一項，並且要有 `code_regex`、`link_regex`、`ignore: true` 其中一項。
- `from`／`subject` 都符合時規則才算適用；適用後，`code_regex` 或 `link_regex` 任一個比對到值就命中。規則適用但兩者都沒比對到時，不算命中，繼續比對下一條規則，最後才交給內建規則。
- 自訂規則套用在所有信箱上。

### 5.5 Trace

每個步驟都寫進 `Trace`：命中的關鍵字和位置、每個候選字串及其分數、被排除的原因、最後的選擇。`relay test` 會輸出這些內容。

## 6. 渲染（render）

- 使用 `html/template`，郵件來源的欄位全部自動跳脫。Telegram HTML 模式支援 `&lt; &gt; &amp; &quot;` 與所有數字實體，和 `html/template` 的跳脫輸出相容。
- 範本變數：

  | 變數 | 內容 |
  |---|---|
  | `.Code` | 驗證碼（可能為空） |
  | `.URL` | 認證連結（可能為空） |
  | `.Service` | 服務名稱 |
  | `.FromName` / `.FromAddr` | 寄件者 |
  | `.To` | 屬於這個信箱的收件地址（可看出 `+alias`），比對規則見下方；找不到就用第一個收件地址 |
  | `.Subject` | 主旨 |
  | `.Mailbox` | 信箱的 `display_name`（未設定時用信箱名稱） |
  | `.Date` | 郵件日期（`time.Time`，可用 `.Date.Format`） |
  | `.Rule` | 命中的規則名稱 |

- `.To` 的比對規則（不分大小寫）：收件地址的網域和信箱 `username` 的網域相同，而且收件地址本地部分去掉 `+` 之後的內容，等於 `username` 的本地部分。網域為 `gmail.com` 或 `googlemail.com` 時，本地部分的 `.` 也忽略。例如 `username: me@gmail.com` 可以比對到 `m.e+github@gmail.com`。
- 依結果選擇範本：只有驗證碼用 `code`、只有連結用 `link`、兩者都有用 `both`。查找順序為：信箱的 `templates` → 全域的 `templates` → 內建預設。
- 內建預設範本：

  ```
  code:  🔐 <b>{{.Service}}</b> 驗證碼
         <code>{{.Code}}</code>
         📧 {{.Mailbox}} · {{.Subject}}

  link:  🔗 <b>{{.Service}}</b> 認證連結
         {{.URL}}
         📧 {{.Mailbox}} · {{.Subject}}

  both:  🔐 <b>{{.Service}}</b> 驗證碼
         <code>{{.Code}}</code>
         🔗 {{.URL}}
         📧 {{.Mailbox}} · {{.Subject}}
  ```

- 按鈕（inline keyboard，由程式產生，不受範本影響）：
  - 有驗證碼，且長度在 1–256 之間：`📋 複製驗證碼`（`copy_text`）
  - 有連結：`🔗 開啟連結`（`url`）
- 長度限制：渲染結果超過 4096 字元時，先截斷 `.Subject` 再重新渲染（截斷至能放進為止，加上 `…`）；若仍超過，就把整段文字截斷到 4096 字元。範本設計上 `.Code` 與 `.URL` 不會被截斷；若 URL 本身超過 4000 字元，訊息只保留按鈕。

## 7. 發送（telegram ＋ dispatcher）

### 7.1 Telegram client

- 只呼叫 `POST https://api.telegram.org/bot<token>/sendMessage`；不使用 Bot 框架，也不呼叫 `getUpdates`。
- 參數：`chat_id`、`text`、`parse_mode=HTML`、`link_preview_options={"is_disabled":true}`、`reply_markup`（有按鈕時）。
- 錯誤分類：`ok`、`rate_limited(retry_after)`、`bad_request`（400）、`forbidden`（403）、`transient`（5xx、網路錯誤、逾時）。
- 日誌中的 token 一律遮蔽。

### 7.2 Dispatcher

- 一個 goroutine 負責排程：有新的 outbox 項目時透過 channel 立即喚醒，平常每秒檢查一次 `next_attempt_at <= now` 的項目。
- 同一個 `(bot, chat_id)` 依建立順序發送，間隔至少 1 秒；不同聊天室可以並行。
- 依錯誤類型處理：

  | 結果 | 處理 |
  |---|---|
  | `ok` | 刪除 outbox 項目，更新 `forward_log.result = sent` |
  | `rate_limited` | `next_attempt_at = now + retry_after` |
  | `transient` | 指數退避（2 秒起、上限 1 分鐘），`attempts++` |
  | `bad_request` 第一次 | 改成純文字（去除 HTML 標籤並反跳脫）、不帶按鈕重送一次 |
  | `bad_request` 改純文字後仍失敗 | 刪除項目，`result = failed`，告警 |
  | `forbidden` | 刪除項目，`result = failed`，告警 |
  | `now > expires_at` | 刪除項目，`result = expired`，寫日誌（不告警） |

- 告警訊息若送往 `admin` 時本身也失敗，只寫日誌，不再產生新的告警，以避免無限循環。

## 8. 儲存（store）

SQLite 檔案位於 `<data_dir>/relay.db`，建立時權限為 0600，啟用 WAL 模式。schema 版本記錄在 `PRAGMA user_version`，啟動時依序執行遷移。

```sql
CREATE TABLE mailbox_state (
  mailbox      TEXT NOT NULL,
  folder       TEXT NOT NULL,
  uid_validity INTEGER NOT NULL,
  last_uid     INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,            -- unix 秒
  PRIMARY KEY (mailbox, folder)
);

CREATE TABLE oauth_tokens (
  mailbox       TEXT PRIMARY KEY,
  refresh_token TEXT NOT NULL,
  access_token  TEXT NOT NULL,
  expires_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE TABLE outbox (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  log_id          INTEGER,                  -- 對應 forward_log.id；告警訊息為 NULL
  target          TEXT NOT NULL,            -- 目標名稱
  bot             TEXT NOT NULL,
  chat_id         INTEGER NOT NULL,
  text            TEXT NOT NULL,
  reply_markup    TEXT,                     -- JSON
  plain_fallback  INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  expires_at      INTEGER NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL
);

CREATE TABLE forward_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  at         INTEGER NOT NULL,
  mailbox    TEXT NOT NULL,
  folder     TEXT NOT NULL,
  uid        INTEGER NOT NULL,
  from_addr  TEXT NOT NULL,
  subject    TEXT NOT NULL,
  kind       TEXT NOT NULL,   -- code | link | both | none | ignored | skipped_age | skipped_size
  rule       TEXT,
  result     TEXT NOT NULL    -- pending | sent | failed | expired | not_forwarded
);
```

- `forward_log` **不儲存驗證碼和網址**。
- 每個目標各一筆 outbox 項目。同一封信有多個目標時，`forward_log.result` 反映最後一個完成的結果；第一版不提供逐目標的結果查詢。
- `forward_log` 保留 30 天，每天清理一次。

## 9. 設定檔

完整範例（同時作為 `config.example.yaml`）：

```yaml
data_dir: /data
log_level: info                 # debug | info | warn | error
http_listen: ":8080"            # 留空代表不啟動 HTTP；第一版只有 /healthz

bots:
  main: { token: "${TG_BOT_MAIN}" }
  work: { token: "${TG_BOT_WORK}" }

targets:
  me:        { bot: main, chat_id: 123456789 }
  family:    { bot: main, chat_id: -1001234567890 }
  work_chan: { bot: work, chat_id: -1009876543210 }

admin: me                       # 選填

defaults:
  max_age: 10m
  ttl: 15m
  alert_ttl: 24h
  poll_interval: 30s
  alert_after: 10m

mailboxes:
  personal_gmail:
    display_name: 個人 Gmail
    host: imap.gmail.com
    port: 993                   # 選填，預設 993
    username: me@gmail.com
    auth: { type: password, password: "${GMAIL_APP_PASSWORD}" }
    folders: [INBOX, "[Gmail]/Spam"]
    targets: [me]

  outlook:
    display_name: Outlook
    host: outlook.office365.com
    username: me@outlook.com
    auth:
      type: microsoft_oauth
      client_id: "00000000-0000-0000-0000-000000000000"
      tenant: consumers
    targets: [me, work_chan]
    max_age: 5m                 # 選填，覆寫 defaults
    templates:
      code: |
        💼 <b>{{.Service}}</b>：<code>{{.Code}}</code>

templates: {}                   # 選填：code / link / both

rules: []

recognize:
  extra_code_keywords: []
  extra_link_keywords: []
```

- 先解析 YAML，再走訪節點樹（yaml.v3 `Node`），把每個字串純量中的 `${NAME}` 替換成環境變數。這樣密碼裡即使有 `"`、`#`、`: ` 等字元，也不會破壞 YAML 結構。找不到變數就報錯；`$$` 代表字面上的 `$`。
- 名稱（bots、targets、mailboxes、rules）只能包含 `[a-z0-9_-]`。
- 信箱層級可以覆寫 `max_age`、`ttl`、`poll_interval`、`templates`。
- 啟動時的驗證：所有名稱引用都存在；環境變數都有值；正規表示式能編譯；範本能解析，並以假資料成功渲染；每個信箱至少有一個目標；`chat_id ≠ 0`；`auth.type` 為已知值，且該類型的必要欄位齊全。錯誤訊息要包含 YAML 路徑，例如 `mailboxes.outlook.targets[1]: 找不到目標 "work_chanel"`。
- 改完設定須重新啟動才生效。

## 10. CLI

執行檔：`relay`。全域旗標：`--config <path>`，預設 `./config.yaml`，或使用環境變數 `RELAY_CONFIG`。

| 指令 | 行為 |
|---|---|
| `relay run` | 驗證設定 → 開啟資料庫 → 啟動 dispatcher、各 watcher、HTTP。收到 SIGINT 或 SIGTERM 時優雅關閉：停止 watcher、讓 dispatcher 送完手上那一則、關閉資料庫 |
| `relay config validate` | 驗證設定，成功時輸出摘要（幾個 Bot、目標、信箱、規則），回傳碼 0；失敗時回傳 1 |
| `relay login <mailbox>` | 只適用於 `microsoft_oauth` 的信箱：執行 device code 流程並儲存 token |
| `relay test --eml <file> [--mailbox <name>] [--send <target>]` | 解析 → 辨識 → 輸出 Trace 與結果 → 用該信箱的範本渲染（未指定 `--mailbox` 時用全域範本）並輸出訊息與按鈕。加上 `--send` 時直接發送到指定目標，不經過 outbox |
| `relay send-test <target>` | 發送一則固定的測試訊息 |

`relay test` 不需要資料庫，也不連線 IMAP。

## 11. 告警（alert）

告警寫入 outbox，`expires_at = now + alert_ttl`。同一個告警鍵（例如 `disconnected:outlook/INBOX`）在 1 小時內只發一次；「已恢復」通知不受這個限制。沒有設定 `admin` 時只寫日誌。

觸發條件：
- watcher 連續無法連線超過 `alert_after`，以及之後的恢復
- 認證失敗（見 3.2）
- 發送失敗：`bad_request` 改純文字後仍失敗，或 `forbidden`

## 12. 可觀測性與部署

- 日誌：`log/slog` 輸出 JSON 格式到 stdout。**禁止**記錄驗證碼、完整網址、密碼和 token；處理信件時記錄信箱、資料夾、UID、寄件者、主旨與辨識結果類型。
- `/healthz`：所有未停用的 watcher 都處於 `connected`（含 IDLE 或輪詢中）時回傳 200，否則回傳 503。回應內容為 JSON：`{"mailboxes": {"outlook/INBOX": "connected", "personal_gmail/INBOX": "reconnecting", ...}}`。
- Dockerfile：多階段建置（`golang` → `gcr.io/distroless/static:nonroot`），使用 `CGO_ENABLED=0`。`HEALTHCHECK` 呼叫 `relay healthcheck`，這是一個隱藏子指令，會請求本機的 `/healthz`，因為 distroless 映像裡沒有 curl。
- `docker-compose.yml`：把 `./config.yaml` 唯讀掛載到 `/config.yaml`，`./data` 掛載到 `/data`，機密資料從 `.env` 讀取，設定 `restart: unless-stopped`。
- `relay login` 在容器裡的執行方式：`docker compose run --rm relay login outlook`。
- README 要說明：監看 Gmail 的 `[Gmail]/Spam` 等資料夾前，必須在 Gmail 設定的「標籤」頁面，為該標籤勾選「在 IMAP 中顯示」。

## 13. 測試策略

- **recognize**（核心）：表格驅動測試，搭配 `testdata/eml/` 的去識別化樣本。每個 `xxx.eml` 都有一個 `xxx.expected.yaml`（內容為 `code`、`url`、`ignore`、`none`）。第一版的樣本至少涵蓋：
  - 繁中、簡中、英文、日文各至少一封驗證碼信
  - 英文驗證連結、magic link 登入、重設密碼連結
  - 同時有驗證碼與連結的信
  - 干擾情境：含金額、訂單編號、日期年份、電話號碼、退訂連結、追蹤像素
  - Big5、GB2312 編碼的信
  - 不應命中的信：一般通知、電子報
- **mailparse**：字元集、純 HTML 信、多部分信件、連結去重。
- **render**：HTML 跳脫（主旨含 `<`、`&`）、範本查找順序、截斷、按鈕。
- **config**：環境變數展開、各種驗證錯誤與錯誤路徑。
- **telegram ＋ dispatcher**：以 `httptest` 模擬 Bot API，涵蓋 ok、429、5xx、400 → 純文字重送、403、過期、同聊天室間隔。
- **store**：遷移、交易（寫入 outbox 與推進 `last_uid` 的原子性）。
- **watcher**：使用 go-imap v2 的記憶體 IMAP 伺服器（`imapserver` ＋ `imapmemserver`）做整合測試，涵蓋：第一次啟動不轉發舊信、IDLE 中收到新信、UIDVALIDITY 改變、斷線後補抓、超過 `max_age` 的信。重連退避透過注入的 `Dialer` 與可控時鐘測試。**斷線重連是最難完整測試的部分**，必須涵蓋「在 FETCH 途中斷線」與「在 IDLE 中斷線」兩種情況。
- **auth**：以 `httptest` 模擬 Microsoft token 端點，涵蓋 token 更新、refresh token 輪替、`invalid_grant`。device code 流程與實際 Outlook.com 的連線需要手動驗證，並記錄在 README。

## 14. 專案結構

```
cmd/relay/
internal/{config,store,auth,watcher,mailparse,recognize,render,telegram,dispatcher,alert,health}/
testdata/eml/
config.example.yaml
Dockerfile
docker-compose.yml
README.md
docs/superpowers/specs/
```

Go module 路徑暫定為 `verify-code-relay`，發佈時再改成實際的 repo 路徑。

## 15. 未來方向（不在第一版範圍）

- Web UI：讀寫同一個 SQLite 資料庫；屆時需要把設定從 YAML 遷移或同步到資料庫，並加上登入驗證。
- LLM 辨識器：接在辨識器鏈的最後，需要明確的開關，並告知使用者信件內容會送到外部服務。
- 依信箱分別設定的自訂規則、設定檔熱重載、Gmail OAuth、自動刪除 Telegram 訊息、逐目標的投遞紀錄。
