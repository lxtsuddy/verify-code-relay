# verify-code-relay

以 IMAP IDLE 即時監看多個信箱，辨識「驗證碼」與「認證／登入／重設密碼連結」信件，套用範本後透過 Telegram Bot 轉發。可同時管理多個 Bot、多個聊天室與多個信箱。

- 對信箱**完全唯讀**：不標記已讀、不移動、不刪除。
- 只轉發辨識成功的信；其他信件只記錄寄件者與主旨。
- 驗證碼與網址**不會**寫進日誌或資料庫的轉發紀錄；主旨中若含有已辨識的驗證碼，記錄時會以 `******` 遮蔽。

## 快速開始（Docker）

```bash
cp config.example.yaml config.yaml   # 依需求修改
cp .env.example .env                 # 填入 Bot token、應用程式密碼
mkdir -p data && sudo chown 65532:65532 data   # 容器以 nonroot (UID 65532) 執行
docker compose run --rm relay config validate
docker compose run --rm relay send-test me     # 確認 Bot 與 chat_id
docker compose up -d
docker compose logs -f
```

## Telegram 設定

1. 找 @BotFather 建立 Bot，取得 token，填進 `.env`。
2. 先對 Bot 傳一則訊息（群組則把 Bot 加入群組；頻道則把 Bot 設為管理員）。
3. 取得 chat_id：個人可用 @userinfobot；群組／頻道 ID 通常是 `-100` 開頭。
4. `relay send-test <目標>` 確認能收到。

## 信箱設定

### Gmail / iCloud / 自架信箱（應用程式密碼）
- Gmail：Google 帳戶需開啟兩步驟驗證，再到「安全性 → 應用程式密碼」建立。`host: imap.gmail.com`。
- 要監看 `[Gmail]/Spam` 等資料夾：Gmail 設定 → 標籤 → 勾選該標籤的「在 IMAP 中顯示」。
- iCloud：`host: imap.mail.me.com`，在 appleid.apple.com 建立 App 專用密碼。

### Microsoft（Outlook.com / Hotmail / Microsoft 365）
Microsoft 已停用 IMAP 的帳號密碼登入，必須使用 OAuth2：
1. 到 Azure 入口網站 → Microsoft Entra ID → 應用程式註冊 → 新增註冊。
   - 支援的帳戶類型：個人帳號選「僅限個人 Microsoft 帳戶」，或「任何組織目錄中的帳戶及個人 Microsoft 帳戶」。
   - 重新導向 URI：不需要。
2. 「驗證」→ 進階設定 →「允許公用用戶端流程」設為「是」。
3. 「API 權限」→ 新增權限 → 委派的權限：`IMAP.AccessAsUser.All`、`offline_access`。
4. 把「應用程式（用戶端）識別碼」填入 `auth.client_id`；個人帳號 `tenant: consumers`。
5. 授權：`docker compose run --rm relay login outlook`，依畫面在瀏覽器輸入代碼。
6. `docker compose restart`。之後 token 會自動更新；若授權失效，`admin` 目標會收到告警，重新執行第 5 步即可。

## 指令

| 指令 | 說明 |
|---|---|
| `relay run` | 啟動服務 |
| `relay config validate` | 驗證設定檔 |
| `relay login <信箱>` | Microsoft OAuth 授權 |
| `relay test --eml <檔案> [--mailbox <信箱>] [--send <目標>]` | 對 .eml 檔顯示辨識過程與訊息；Gmail 網頁版「下載郵件」即可取得 .eml |
| `relay send-test <目標>` | 發送測試訊息 |

設定檔路徑：`--config`（`--config 路徑` 或 `--config=路徑`），或環境變數 `RELAY_CONFIG`，預設 `./config.yaml`。改完設定需重新啟動。

## 辨識不到或辨識錯誤時

1. 下載該信的 `.eml`，執行 `relay test --eml 信件.eml`，看「辨識過程」。
2. 缺關鍵字：加到 `recognize.extra_code_keywords` / `extra_link_keywords`。
3. 特定寄件者格式特殊：在 `rules` 加自訂規則（`from`、`subject`、`code_regex`、`link_regex`），或用 `ignore: true` 排除。

## 範本

使用 Go `html/template`（Telegram HTML 格式），郵件內容會自動跳脫。可用變數：`.Code` `.URL` `.Service` `.FromName` `.FromAddr` `.To` `.Subject` `.Mailbox` `.Date` `.Rule`。依結果使用 `code` / `link` / `both` 範本，查找順序：信箱 `templates` → 全域 `templates` → 內建。按鈕（複製驗證碼、開啟連結）自動產生。

## 健康檢查

`http_listen` 啟用時提供 `GET /healthz`：所有信箱連線正常回 200，否則 503，內容列出各信箱狀態（`connected`、`reconnecting`、`auth_failed`、`needs_login`…）。Docker 映像內建 `HEALTHCHECK`。

## 開發

```bash
go test ./...                       # 單元與整合測試（含記憶體 IMAP 伺服器）
python3 testdata/eml/gen.py         # 重新產生辨識測試信件
go build -o relay ./cmd/relay
```

## 需要手動驗證的項目

自動測試無法涵蓋以下情境，首次部署時請確認：
- [ ] Gmail 應用程式密碼登入、IDLE 收信後 Telegram 立即收到訊息。
- [ ] Outlook.com 以 `relay login` 授權成功，且 token 過期後能自動更新（運行超過 1 小時後仍能收信）。
- [ ] 中斷網路數分鐘後恢復，服務自動重連並補抓斷線期間的信。
- [ ] 重啟服務後，超過 `max_age` 的舊信不會被轉發。
