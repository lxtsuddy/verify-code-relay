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

容器內看不到主機的檔案，`relay test --eml` 要先把信件檔掛載進去（路徑需為絕對路徑），或用 `-T` 從標準輸入讀取：
```bash
docker compose run --rm -v "$PWD/信件.eml:/mail.eml:ro" relay test --eml /mail.eml
docker compose run --rm -T relay test --eml /dev/stdin < 信件.eml
```

映像內建 `HEALTHCHECK`（執行 `relay healthcheck`），所以：
- `http_listen` 留空時健康檢查一定失敗，容器會一直顯示 `unhealthy`。保持 `":8080"`，或在 `docker-compose.yml` 的服務下加上 `healthcheck: { disable: true }`。
- `/healthz` 回 503（補抓中、斷線、需要重新授權）時也會顯示 `unhealthy`。這只是狀態標示，Docker 不會因此重啟容器；請看日誌或 `/healthz` 內容找原因。

## 部署到 K3s

設定檔在 `deploy/k3s/`，以 Kustomize 管理：單一 replica（SQLite 與重複轉發的限制）、K3s 內建 `local-path` PVC 存放資料庫、以 UID 65532 及唯讀根檔案系統執行。程式只需要對外連線，不需要 Service 或 Ingress。

1. **發布映像**：GitHub Actions（`.github/workflows/ci.yml`）在每次 push 時執行測試；推送 `v*` 標籤時建置 amd64 映像並發布到 `ghcr.io/lxtsuddy/verify-code-relay:<版本>`：
   ```bash
   git tag v0.1.0 && git push origin v0.1.0
   ```
   private repo 發布的套件預設也是私有。首次發布後，需在 GitHub 的 Package settings 改成 Public，叢集才能不帶憑證拉取。注意：套件改成公開後就無法再改回私有。
2. **指定版本**：`deploy/k3s/kustomization.yaml` 的 `images.newTag` 要對應已發布的標籤（不含 `v`，例如 `0.1.0`）。
3. **準備設定與機密**（兩個檔案都已列入 `.gitignore`）：
   ```bash
   cp config.example.yaml deploy/k3s/config.yaml   # data_dir 保持 /data，http_listen 保持 ":8080"
   cp .env.example deploy/k3s/secret.env           # 填入 Bot token、應用程式密碼
   ```
   `http_listen` 必須啟用，存活檢查依賴 8080 埠。
4. **部署並檢查**：
   ```bash
   kubectl apply -k deploy/k3s
   kubectl -n verify-code-relay rollout status deploy/verify-code-relay
   kubectl -n verify-code-relay exec deploy/verify-code-relay -- /relay config validate
   kubectl -n verify-code-relay exec deploy/verify-code-relay -- /relay send-test me
   kubectl -n verify-code-relay logs -f deploy/verify-code-relay
   ```
5. **Microsoft 信箱授權**（Pod 執行中即可，未授權時 Pod 只會顯示未就緒）：
   ```bash
   kubectl -n verify-code-relay exec -it deploy/verify-code-relay -- /relay login outlook
   kubectl -n verify-code-relay rollout restart deploy/verify-code-relay
   ```

**日常操作：**
- 修改 `deploy/k3s/config.yaml` 或 `secret.env` 後重新 `kubectl apply -k deploy/k3s`。ConfigMap／Secret 名稱帶內容雜湊，Pod 會自動重啟套用新設定。
- 升級：推送新的 `v*` 標籤發布映像、更新 `newTag`，再 `kubectl apply -k deploy/k3s`。
- 測試 .eml：映像沒有 shell 也沒有 `tar`，`kubectl cp` 無法使用。改用 `exec -i`（不要加 `-t`）把信件從標準輸入傳進去：
  ```bash
  kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay test --eml /dev/stdin < 信件.eml
  kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay test --eml /dev/stdin --mailbox personal_gmail --send me < 信件.eml
  ```
- 健康狀態：`/healthz` 在補抓中、斷線或需要重新授權時回 503。readiness probe 據此把 Pod 標為未就緒；liveness probe 只檢查 8080 埠是否在聽，避免因授權失效被不斷重啟。Kubernetes 不使用映像內的 Docker `HEALTHCHECK`。
- 備份：資料庫在 PVC 上（`local-path` 預設位於節點的 `/var/lib/rancher/k3s/storage/`），內含 OAuth token，請妥善保管。
- 刪除：`kubectl delete -k deploy/k3s` 會一併刪除 PVC 與資料庫。

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
6. `docker compose restart`。之後 token 會自動更新；若授權失效，`admin` 目標會收到告警，重新執行第 5 步，再重新啟動服務（第 6 步）即可。

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

`http_listen` 啟用時提供 `GET /healthz`：所有信箱連線正常回 200，否則 503，內容列出各信箱狀態（`connected`、`reconnecting`、`auth_failed`、`needs_login`…）。Docker 映像內建 `HEALTHCHECK`，`http_listen` 留空或回 503 時容器會顯示 `unhealthy`（見「快速開始」）。

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
- [ ] 落入垃圾郵件匣的驗證信也會轉發（Gmail 需在 `folders` 加入 `[Gmail]/Spam`，並在標籤設定勾選「在 IMAP 中顯示」）。
- [ ] 在容器內執行 `login`（`docker compose run --rm relay login <信箱>` 或 K3s 的 `kubectl exec -it`）授權成功，token 寫入 `/data`，重新啟動後仍有效。
- [ ] `relay test --eml 信件.eml --send <目標>` 實際送出，Telegram 收到的訊息格式與按鈕（複製驗證碼、開啟連結）正常。
