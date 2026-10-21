# 設定說明

繁體中文 | [English](configuration.en.md)

所有設定都在 Web 管理介面完成，存檔即生效；每次存檔留下一個版本，可在 **系統設定 → 版本歷史** 還原。

## Telegram

在 Web 的 **Telegram** 頁操作：

1. 找 [@BotFather](https://t.me/BotFather) 建立 Bot，取得 token，在「Bot」新增（會向 Telegram 確認 token 有效）。之後要換 token 也在這裡，存檔即生效。
2. 對 Bot 傳一則訊息（群組則把 Bot 加入群組並在群組中發言；頻道則把 Bot 設為管理員並發一則貼文）。
3. 在「目標」新增，按「讀取最近對話」列出 Bot 最近收到訊息的聊天室，選一個就會填入 chat_id。找不到時也可以手動輸入（個人可用 @userinfobot 查詢；群組／頻道 ID 通常是 `-100` 開頭）。
4. 按目標旁的「發送測試」確認能收到。

## 信箱

在 Web 的 **信箱** 頁新增：選擇服務（會帶入 IMAP 主機）、填入帳號與密碼、再在「轉發路由」一列一組寫下「資料夾 → 目標」：信件從哪個資料夾進來就送到該列的目標，同一個資料夾要送到多個目標就寫多列（「測試連線」會實際登入並列出資料夾供挑選）。路由裡出現的資料夾才會監看。存檔後立即開始監看。

信箱的「名稱」建立後不能修改（收信進度與授權以名稱儲存），「顯示名稱」可以隨時改。

### Gmail / iCloud / 自架信箱（應用程式密碼）

- Gmail：Google 帳戶需開啟兩步驟驗證，再到「安全性 → 應用程式密碼」建立。主機 `imap.gmail.com`。
- 要監看 `[Gmail]/Spam` 等資料夾：Gmail 設定 → 標籤 → 勾選該標籤的「在 IMAP 中顯示」，測試連線時就會列出。
- iCloud：主機 `imap.mail.me.com`，在 appleid.apple.com 建立 App 專用密碼。
- 其他支援 IMAP over TLS（預設 993 埠）的信箱：選「自訂」，填入主機與埠號。

### Microsoft（Outlook.com / Hotmail / Microsoft 365）

Microsoft 已停用 IMAP 的帳號密碼登入，必須使用 OAuth2，需要自己註冊一個 Azure 應用程式（免費）：

1. 到 Azure 入口網站 → Microsoft Entra ID → 應用程式註冊 → 新增註冊。
   - 支援的帳戶類型：個人帳號選「僅限個人 Microsoft 帳戶」，或「任何組織目錄中的帳戶及個人 Microsoft 帳戶」。
   - 重新導向 URI：不需要。
2. 「驗證」→ 進階設定 →「允許公用用戶端流程」設為「是」。
3. 「API 權限」→ 新增權限 → 委派的權限：`IMAP.AccessAsUser.All`、`offline_access`。
4. 在 Web 的「系統設定 → Microsoft 應用程式」填入「應用程式（用戶端）識別碼」，只要設定一次，所有 Microsoft 信箱共用。預設 Tenant：個人帳號填 `consumers`，公司或學校帳號填 `organizations` 或租戶 ID，留空為 `common`。
5. 新增信箱時選 Outlook（或「Microsoft 授權」），填帳號即可。個別信箱要用別的 Azure 應用程式或 tenant 時，在信箱的「進階設定」覆寫，留空就沿用系統設定。
6. 存檔後在信箱頁（或總覽頁）按「授權」，依畫面到 Microsoft 的網頁輸入代碼並登入。授權完成後自動開始監看，不需要重新啟動。

授權綁定在使用的識別碼與 tenant 上：在系統設定改了它們，沿用的信箱都要重新授權（儲存前會列出哪些信箱的授權會被清除）。信箱自己覆寫的值不受影響：例如只覆寫識別碼的信箱，預設 tenant 改了仍要重新授權。

之後 token 會自動更新。若授權失效（例如改了密碼），通知對象會收到告警、總覽頁會顯示「重新授權」按鈕，再授權一次即可。

備援：也可以在伺服器上以命令列授權，但完成後要重新啟動服務才會套用：

```bash
kubectl -n verify-code-relay exec -it deploy/verify-code-relay -- /relay login outlook
kubectl -n verify-code-relay rollout restart deploy/verify-code-relay
```

## 系統設定

- **通知對象**：選一個目標接收登入通知與系統告警（斷線、授權失效、發送失敗）。沒有設定時只寫日誌。
- **轉發預設值**：收信時間超過多久就不轉發（預設 10 分鐘）、待發送訊息超過多久就丟棄（預設 15 分鐘）、伺服器不支援 IDLE 時的輪詢間隔、連續斷線多久才告警，以及日誌等級。個別信箱可在「進階設定」覆寫時效、保留時間與輪詢間隔。
- **登入安全**：修改密碼、重新綁定兩步驟驗證；兩者都會登出其他裝置。
- **目前的連線**：伺服器判定的用戶端 IP，用來確認反向代理設定（見[部署指南](deploy.md#選配在通知中看到真實-ip)）。

## 匯入 YAML

平常不需要 YAML。想一次建立大量設定、或從備份搬家時，可以用 `relay config import` 匯入成一個新版本；格式見 [`config.example.yaml`](../config.example.yaml)：

```bash
relay config validate config.yaml          # 先檢查
relay config import config.yaml            # 匯入；K3s 上用 exec -i 從標準輸入傳入：
kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay config import /dev/stdin < config.yaml
```

- 機密可以寫成 `${環境變數}`，匯入時從**執行 import 的環境**展開後存進資料庫（K3s 上是 Pod 的環境，所以變數要在 `secret.env` 中）。`$$` 代表字面上的 `$`。
- 服務執行中匯入時，請重新啟動（K3s：`kubectl -n verify-code-relay rollout restart deploy/verify-code-relay`）或在 Web 上任意存檔一次以套用。
- 匯入會清除不再適用的 Microsoft 授權（信箱被移除，主機、埠號、帳號、client_id 或 tenant 改了，包括 `microsoft` 區段改了而信箱沿用它，或信箱剛改成 Microsoft 授權），並列出「信箱 X 的 Microsoft 授權已清除，請重新授權」；之後在 Web 上重新授權即可。
- `relay config export` 輸出目前設定（含機密），格式相同，可用於備份或搬家。

欄位說明：

| 欄位 | 說明 |
|---|---|
| `log_level` | `debug`、`info`、`warn`、`error` |
| `bots.<名稱>.token` | Bot token |
| `targets.<名稱>` | `bot`（Bot 名稱）與 `chat_id` |
| `admin` | 通知對象（目標名稱）：登入通知與系統告警（斷線、授權失效、發送失敗） |
| `defaults` | `max_age`（收信時間超過多久就不轉發）、`ttl`（待發送訊息超過多久就丟棄）、`alert_ttl`、`poll_interval`（伺服器不支援 IDLE 時的輪詢間隔）、`alert_after`（連續斷線多久才告警） |
| `microsoft` | Microsoft 信箱共用的 Azure 應用程式：`client_id`、`tenant`（選填，省略為 `common`；個人帳號用 `consumers`） |
| `mailboxes.<名稱>` | `display_name`、`host`、`port`（預設 993）、`username`、`auth`、`routes`（`folder` → `target` 配對，一列一組；監看的資料夾就是路由裡出現的資料夾。舊的 `folders`＋`targets` 仍可讀取，會轉成「每個資料夾 → 每個目標」），以及覆寫用的 `max_age`、`ttl`、`poll_interval`、`templates` |
| `mailboxes.<名稱>.auth` | `{type: password, password: …}`，或 `{type: microsoft_oauth}`（匯入後在 Web 授權）；`client_id`、`tenant` 選填，寫了就覆寫 `microsoft` 的值 |
| `templates` | 全域範本 `code`、`link`、`both`（見[範本](recognition.md#範本)） |
| `rules` | 自訂規則（見[辨識規則](recognition.md)） |
| `recognize` | `extra_code_keywords`、`extra_link_keywords` |

## 指令

服務執行中，除了 `run` 以外的指令都在容器內執行（K3s：`kubectl -n verify-code-relay exec [-i|-it] deploy/verify-code-relay -- /relay …`；Docker：`docker exec [-i|-it] relay /relay …`），讀取同一個資料庫。

| 指令 | 說明 |
|---|---|
| `relay run` | 啟動服務、Web 管理介面（`:8080`）與健康檢查（`:8081`） |
| `relay config import <檔案>` | 匯入 YAML 設定（展開 `${環境變數}`），存成新版本 |
| `relay config export` | 輸出目前設定（含機密），用於備份 |
| `relay config validate [檔案]` | 檢查 YAML 設定檔，不寫入資料庫 |
| `relay admin reset` | 重設管理員密碼並清除 TOTP，登出所有裝置 |
| `relay login <信箱>` | Microsoft 授權（備援；建議在 Web 上授權），完成後需重新啟動 |
| `relay test --eml <檔案> [--mailbox <信箱>] [--send <目標>]` | 對 .eml 檔顯示辨識過程與訊息（與 Web 的「用信件測試」相同的辨識；只有這裡能 `--send` 實際送出）；Gmail 網頁版「下載郵件」即可取得 .eml |
| `relay send-test <目標>` | 發送測試訊息（Web 的 Telegram 頁也有「發送測試」） |
| `relay healthcheck` | 檢查 `:8081/readyz`（映像的 `HEALTHCHECK` 使用） |

`import`、`validate` 的檔案也可以用 `--config` 或環境變數 `RELAY_CONFIG` 指定。其他指令一律讀取資料庫中的設定。

## 健康檢查

健康檢查在 `RELAY_HEALTH_LISTEN`（預設 `:8081`），與 Web 分開，不要對外暴露：

| 路徑 | 說明 |
|---|---|
| `GET /livez` | 程序存活，固定 200（liveness probe） |
| `GET /readyz` | 服務正常且資料庫可查詢時 200，否則 503（readiness probe、`relay healthcheck`）。不看信箱狀態 |
| `GET /healthz` | 各信箱的狀態明細（`connected`、`reconnecting`、`auth_failed`、`needs_login`…）：全部正常（或沒有任何信箱）時 200，否則 503。只供查看，探針不使用 |

信箱的狀態也顯示在 Web 的總覽頁。

## 總覽與轉發紀錄

- **總覽的「最近轉發」**預設只列最近 10 封辨識出驗證碼或連結、要轉發的信（含送出中、失敗或過期的）；切到「全部檢查過的信」可看最近 10 封檢查過的任何信件（包括未辨識、被規則略過的）。每 10 秒自動更新。
- **轉發紀錄頁**列出每一封檢查過的信，可以：
  - 切換「全部紀錄」與「只看轉發」；
  - 搜尋主旨或寄件者（主旨中已辨識的驗證碼記錄時就已遮蔽，搜尋不到驗證碼）；
  - 依信箱、種類（有轉發、驗證碼、連結、被規則略過、未辨識、過舊或過大）、結果、時間（24 小時、7 天、30 天內；紀錄本身只保留 30 天）篩選；
  - 每頁 20、50 或 100 筆，用頁碼跳頁。篩選條件都在網址裡，重新整理或分享連結都會保留。
- 驗證碼與網址**不會**寫進日誌或轉發紀錄；主旨中若含有已辨識的驗證碼，記錄時會以 `******` 遮蔽。
