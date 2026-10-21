# verify-code-relay

以 IMAP IDLE 即時監看多個信箱，辨識「驗證碼」與「認證／登入／重設密碼連結」信件，套用範本後透過 Telegram Bot 轉發。可同時管理多個 Bot、多個聊天室與多個信箱。

- **Web 管理介面**：所有設定在網頁完成，存檔即生效。Bot、目標、信箱、規則、範本、預設值都在網頁上管理，Microsoft 授權也在網頁上完成；每次存檔留下一個版本，可以還原。
- 單一管理員，以密碼＋兩步驟驗證（TOTP）登入；可放在公網（前面需要 HTTPS 反向代理）。
- 對信箱**完全唯讀**：不標記已讀、不移動、不刪除。
- 只轉發辨識成功的信；其他信件只記錄寄件者與主旨。
- 驗證碼與網址**不會**寫進日誌或資料庫的轉發紀錄；主旨中若含有已辨識的驗證碼，記錄時會以 `******` 遮蔽。

所有設定與機密（Bot token、應用程式密碼、OAuth token、管理員）都存在 `RELAY_DATA_DIR` 的 SQLite 資料庫；程式本身只從環境變數讀取下列程序層級設定：

| 變數 | 預設 | 說明 |
|---|---|---|
| `RELAY_DATA_DIR` | `/data` | SQLite 資料庫目錄 |
| `RELAY_LISTEN` | `:8080` | Web 管理介面 |
| `RELAY_HEALTH_LISTEN` | `:8081` | 內部健康檢查（不要對外）；設成空字串代表不啟動 |
| `RELAY_ADMIN_PASSWORD` | （無） | 只在資料庫還沒有管理員時用來建立管理員，至少 12 個字元 |
| `RELAY_TRUSTED_PROXIES` | （空） | 以逗號分隔的 CIDR 或 IP；只影響記錄與通知中的 IP 是否準確（見「真實 IP」） |

## 快速開始（Docker，本機試用）

```bash
cp .env.example .env                 # 填入 RELAY_ADMIN_PASSWORD（至少 12 個字元）
mkdir -p data && sudo chown 65532:65532 data   # 容器以 nonroot (UID 65532) 執行
docker run -d --name relay --restart unless-stopped \
  --env-file .env -p 127.0.0.1:8080:8080 -v "$PWD/data:/data" \
  ghcr.io/lxtsuddy/verify-code-relay:0.2.0
docker logs -f relay
```

用瀏覽器開 `http://localhost:8080`，以 `RELAY_ADMIN_PASSWORD` 登入、綁定兩步驟驗證，再依總覽頁的步驟新增 Bot、目標與信箱。

- 登入 cookie 帶有 `Secure`，瀏覽器只在 HTTPS 或 `http://localhost` 接受，所以本機試用可以直接用 http。從其他電腦以 `http://<IP>:8080` 連線無法登入（cookie 不會保存、請求來源檢查也會拒絕），正式使用請照下面的 K3s 方式放在 HTTPS 反向代理後面。
- 映像內建 `HEALTHCHECK`（`relay healthcheck`，打 `:8081/readyz`），只確認服務與資料庫正常，不看信箱狀態；信箱的狀況請看總覽頁。
- 測試 `.eml`：`relay test` 讀取的是容器裡的資料庫，所以要在執行中的容器內跑，信件從標準輸入傳進去（不要加 `-t`）：
  ```bash
  docker exec -i relay /relay test --eml /dev/stdin < 信件.eml
  ```
  只想看辨識結果時，用 Web 的「用信件測試」更方便，見「辨識規則」。

## 部署到 K3s

設定檔在 `deploy/k3s/`，以 Kustomize 管理：單一 replica（SQLite 與重複轉發的限制）、K3s 內建 `local-path` PVC 存放資料庫、以 UID 65532 及唯讀根檔案系統執行。

對外路徑：瀏覽器 →（HTTPS）**Caddy** →（HTTP）**K3s Traefik Ingress** → Service（port 80）→ Pod 的 8080。TLS 由 Caddy 處理；健康檢查埠 8081 只給探針使用，不經 Service 暴露。

### 首次部署

1. **發布映像**：GitHub Actions（`.github/workflows/ci.yml`）在每次 push 時執行測試；推送 `v*` 標籤時建置 amd64 映像並發布到 `ghcr.io/lxtsuddy/verify-code-relay:<版本>`：
   ```bash
   git tag v0.2.0 && git push origin v0.2.0
   ```
   private repo 發布的套件預設也是私有。首次發布後，需在 GitHub 的 Package settings 改成 Public，叢集才能不帶憑證拉取。注意：套件改成公開後就無法再改回私有。
2. **指定版本與網域**：`deploy/k3s/kustomization.yaml` 的 `images.newTag` 要對應已發布的標籤（不含 `v`，例如 `0.2.0`）；`deploy/k3s/ingress.yaml` 的 `host` 改成你的網域。
3. **準備 `secret.env`**（已列入 `.gitignore`）：
   ```bash
   cp .env.example deploy/k3s/secret.env   # 只需要 RELAY_ADMIN_PASSWORD
   ```
   密碼至少 12 個字元，請用長而隨機的密碼（例如 `openssl rand -base64 24`）。它只在資料庫還沒有管理員時使用，之後改密碼請在 Web 的「系統設定」進行。
4. **部署**：
   ```bash
   kubectl apply -k deploy/k3s
   kubectl -n verify-code-relay rollout status deploy/verify-code-relay
   kubectl -n verify-code-relay logs -f deploy/verify-code-relay
   ```
5. **設定 Caddy**（在 Caddyfile 加入，`<Traefik 位址>` 是 K3s 節點的 IP，Traefik 預設在 80 埠）：
   ```
   relay.example.com {
       reverse_proxy <Traefik 位址>:80
   }
   ```
   Caddy 預設保留 `Host` 並設定 `X-Forwarded-For`，Traefik 依 `Host` 找到這個 Ingress。
6. **首次登入**：瀏覽器開 `https://relay.example.com` → 用 `secret.env` 的密碼登入 → 依畫面綁定兩步驟驗證（用 Google Authenticator、1Password 等 App 掃 QR code，輸入一組驗證碼）→ 依序新增：
   1. **Telegram → Bot**：貼上 @BotFather 給的 token。
   2. **Telegram → 目標**：先對 Bot 傳一則訊息，再按「讀取最近對話」選擇聊天室，存檔後按「發送測試」確認收得到。
   3. **信箱**：選擇服務、填入帳號與應用程式密碼（Outlook 選 Microsoft 授權，並先在 **系統設定 → Microsoft 應用程式** 填一次 Azure 應用程式，見「信箱設定 → Microsoft」），按「測試連線」挑選資料夾，勾選要轉發的目標。
   4. **系統設定 → 通知對象**：選一個目標接收登入通知與信箱告警（強烈建議，見「安全」）。

   也可以改用 `relay config import` 匯入現成的 YAML（見「匯入 YAML」）。

還沒有網域或 Caddy 時，可以先用 port-forward 在本機設定（`http://localhost` 允許 Secure cookie）：
```bash
kubectl -n verify-code-relay port-forward deploy/verify-code-relay 8080:8080
# 瀏覽器開 http://localhost:8080
```

### 選配：在通知中看到真實 IP

登入通知、告警與設定版本會記錄用戶端 IP。預設 Traefik 不信任 Caddy 送來的 `X-Forwarded-For`，記錄到的會是 Caddy 的位址。要看到真實 IP：

1. 讓 Traefik 信任 Caddy：在 K3s server 節點建立 `/var/lib/rancher/k3s/server/manifests/traefik-config.yaml`（K3s 會自動套用）：
   ```yaml
   apiVersion: helm.cattle.io/v1
   kind: HelmChartConfig
   metadata:
     name: traefik
     namespace: kube-system
   spec:
     valuesContent: |-
       ports:
         web:
           forwardedHeaders:
             trustedIPs:
               - <Caddy 的 IP>/32
   ```
2. 讓程式信任 Traefik 與 Caddy：`deploy/k3s/deployment.yaml` 的 `RELAY_TRUSTED_PROXIES` 預設是 `"10.42.0.0/16"`（K3s 預設 Pod 網段，Traefik 從這裡連進來），加上 Caddy 的 IP，例如 `"10.42.0.0/16,192.168.1.5"`，再 `kubectl apply -k deploy/k3s`。
3. 到 **系統設定 → 目前的連線**（目前被判定的 IP）確認「伺服器判定你的 IP」是你的真實位址。頁面也會列出連線來源與收到的 `X-Forwarded-For`；如果 Traefik 看到的來源不是 Caddy 的 IP（例如 Caddy 與 K3s 在同一台主機時，可能是節點或 Pod 網段的位址），依頁面顯示的位址調整上面兩處。

IP 只用於記錄與通知，不影響任何安全判斷。

### 日常操作

- **修改設定**：在 Web 存檔即生效，不需要重新部署或重啟。每次存檔留下一個版本，可在 **系統設定 → 版本歷史** 還原（保留最近 50 版）。
- **升級**：推送新的 `v*` 標籤發布映像、更新 `newTag`，再 `kubectl apply -k deploy/k3s`。
- **測試 .eml**：映像沒有 shell 也沒有 `tar`，`kubectl cp` 無法使用。改用 `exec -i`（不要加 `-t`）把信件從標準輸入傳進去：
  ```bash
  kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay test --eml /dev/stdin < 信件.eml
  kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay test --eml /dev/stdin --mailbox personal_gmail --send me < 信件.eml
  ```
- **健康狀態**：readiness probe 打 `:8081/readyz`、liveness probe 打 `:8081/livez`，都**不看信箱狀態**，所以 Outlook 需要重新授權或信箱斷線時 Pod 仍然就緒、Web 仍可連線，可以直接在網頁上處理。信箱的問題由 Telegram 告警與總覽頁呈現。Kubernetes 不使用映像內的 Docker `HEALTHCHECK`。
- **備份**：資料庫在 PVC 上（`local-path` 預設位於節點的 `/var/lib/rancher/k3s/storage/`）。設定也可以匯出成 YAML：
  ```bash
  (umask 077; kubectl -n verify-code-relay exec deploy/verify-code-relay -- /relay config export > backup-$(date +%F).yaml)
  ```
  `umask 077` 讓匯出檔只有自己能讀。資料庫與匯出檔都含有全部機密，請存放在安全的地方（加密的備份、密碼管理器），不要提交到 git，見「安全」。
- **刪除**：`kubectl delete -k deploy/k3s` 會一併刪除 PVC 與資料庫。

### 從舊版（ConfigMap）遷移

0.1.0 的設定放在 `deploy/k3s/config.yaml`（ConfigMap），機密以 `${變數}` 從 `secret.env` 帶入。新版的設定改存資料庫，要匯入一次。0.1.0 的程式沒有 `config import`，所以要先換成新版映像、在新 Pod 裡匯入；匯入時 `${}` 變數從 Pod 的環境展開，因此舊的機密要暫時留在 `secret.env`：

1. 在 `deploy/k3s/secret.env` **保留原本的變數**，另外加上一行 `RELAY_ADMIN_PASSWORD=...`；確認 `newTag` 是新版本、`ingress.yaml` 的 `host` 已改成你的網域。
2. 套用新的 manifests：
   ```bash
   kubectl apply -k deploy/k3s
   kubectl -n verify-code-relay rollout status deploy/verify-code-relay
   ```
   新 Pod 沿用同一個 PVC：資料庫會自動升級，信箱的收信進度與 Outlook 的 OAuth token 都會保留（升級後的資料庫無法再給 0.1.0 使用）。匯入前的設定是空的，這段時間不會轉發。
3. **立即登入 Web 並綁定兩步驟驗證**：從這一刻起管理介面已經在公網上，綁定前任何知道密碼的人都能搶先綁定。登入狀態存在資料庫，後面的重新啟動不會讓你登出。
4. 匯入舊設定（`${}` 變數由 Pod 環境中的舊機密提供）：
   ```bash
   kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay config import /dev/stdin < deploy/k3s/config.yaml
   ```
5. 把 `secret.env` 改成**只留 `RELAY_ADMIN_PASSWORD`**，再套用一次。Secret 內容改變會讓 Pod 重新啟動，同時載入剛匯入的設定：
   ```bash
   kubectl apply -k deploy/k3s
   ```
   重新整理 Web，確認信箱、Telegram、規則都在，總覽頁的信箱顯示已連線。
6. 刪除舊的 `deploy/k3s/config.yaml`，並清掉叢集中不再使用的舊物件。**有兩個舊 Secret 仍以明文保存舊的機密**（Bot token、應用程式密碼）：0.1.0 的 `relay-secrets-…`，以及第 2 步套用時產生、含舊變數與管理員密碼的 `relay-secrets-…`；另有一個舊 ConfigMap `relay-config-…`。目前使用中的 Secret 是 Deployment 引用的那一個，不要刪：
   ```bash
   kubectl -n verify-code-relay get configmap,secret        # 列出全部，依建立時間辨認
   kubectl -n verify-code-relay get deploy verify-code-relay \
     -o jsonpath='{.spec.template.spec.containers[0].envFrom[0].secretRef.name}{"\n"}'   # 使用中的 Secret
   kubectl -n verify-code-relay delete configmap/relay-config-<雜湊> secret/relay-secrets-<舊雜湊1> secret/relay-secrets-<舊雜湊2>
   ```

## 安全

管理介面放在公網時：

- **密碼要長且隨機**：`RELAY_ADMIN_PASSWORD` 至少 12 個字元，建議用密碼產生器產生 20 個字元以上。登入有全域限速，但不會鎖定帳號。
- **部署後立即登入並綁定 TOTP**：首次登入只需要密碼，綁定之前任何知道密碼的人都能搶先完成綁定。綁定後每次登入都需要密碼＋驗證碼。
- **設定通知對象**：在 **系統設定 → 通知對象** 選一個目標。之後每次登入成功、修改密碼、重新綁定 TOTP 都會通知；密碼正確但驗證碼錯誤時會立即告警（代表密碼可能已外洩，請立刻修改）。沒有設定時只寫日誌。
- **遺失手機或忘記密碼**：在叢集上重設，會清除 TOTP 並登出所有裝置，之後重新登入綁定：
  ```bash
  kubectl -n verify-code-relay exec -it deploy/verify-code-relay -- /relay admin reset
  ```
  （非互動時可用 `exec -i` 從標準輸入傳入一行新密碼。）
- **資料庫含所有機密**：Bot token、應用程式密碼、OAuth token、密碼雜湊與 TOTP 金鑰都在資料庫裡，`relay config export` 的輸出也含有 token 與密碼。PVC 所在節點、資料庫備份與匯出檔都要妥善保管，不要提交到 git 或放在共用空間。
- Web 頁面、錯誤訊息與日誌都不會顯示已儲存的機密；token 與密碼欄位只能寫入，留空代表不變。
- 健康檢查埠 8081 不要對外暴露（`deploy/k3s` 的 Service 只暴露 8080）。

## Telegram 設定

在 Web 的 **Telegram** 頁操作：

1. 找 @BotFather 建立 Bot，取得 token，在「Bot」新增（會向 Telegram 確認 token 有效）。之後要換 token 也在這裡，存檔即生效。
2. 對 Bot 傳一則訊息（群組則把 Bot 加入群組並在群組中發言；頻道則把 Bot 設為管理員並發一則貼文）。
3. 在「目標」新增，按「讀取最近對話」列出 Bot 最近收到訊息的聊天室，選一個就會填入 chat_id。找不到時也可以手動輸入（個人可用 @userinfobot 查詢；群組／頻道 ID 通常是 `-100` 開頭）。
4. 按目標旁的「發送測試」確認能收到。

## 信箱設定

在 Web 的 **信箱** 頁新增：選擇服務（會帶入 IMAP 主機）、填入帳號與密碼、按「測試連線」實際登入並列出資料夾供勾選，再勾選要轉發到哪些目標。存檔後立即開始監看。信箱的「名稱」建立後不能修改（收信進度與授權以名稱儲存），「顯示名稱」可以隨時改。

### Gmail / iCloud / 自架信箱（應用程式密碼）
- Gmail：Google 帳戶需開啟兩步驟驗證，再到「安全性 → 應用程式密碼」建立。主機 `imap.gmail.com`。
- 要監看 `[Gmail]/Spam` 等資料夾：Gmail 設定 → 標籤 → 勾選該標籤的「在 IMAP 中顯示」，測試連線時就會列出。
- iCloud：主機 `imap.mail.me.com`，在 appleid.apple.com 建立 App 專用密碼。

### Microsoft（Outlook.com / Hotmail / Microsoft 365）
Microsoft 已停用 IMAP 的帳號密碼登入，必須使用 OAuth2：
1. 到 Azure 入口網站 → Microsoft Entra ID → 應用程式註冊 → 新增註冊。
   - 支援的帳戶類型：個人帳號選「僅限個人 Microsoft 帳戶」，或「任何組織目錄中的帳戶及個人 Microsoft 帳戶」。
   - 重新導向 URI：不需要。
2. 「驗證」→ 進階設定 →「允許公用用戶端流程」設為「是」。
3. 「API 權限」→ 新增權限 → 委派的權限：`IMAP.AccessAsUser.All`、`offline_access`。
4. 在 Web 的「系統設定 → Microsoft 應用程式」填入「應用程式（用戶端）識別碼」，只要設定一次，所有 Microsoft 信箱共用。預設 Tenant：個人帳號填 `consumers`，公司或學校帳號填 `organizations` 或租戶 ID，留空為 `common`。
5. 新增信箱時選 Outlook（或「Microsoft 授權」），填帳號即可。個別信箱要用別的 Azure 應用程式或 tenant 時，在信箱的「進階設定」覆寫，留空就沿用系統設定。
6. 存檔後在信箱頁（或總覽頁）按「授權」，依畫面到 Microsoft 的網頁輸入代碼並登入。授權完成後自動開始監看，不需要重新啟動。

授權綁定在使用的識別碼與 tenant 上：在系統設定改了它們，沿用的信箱都要重新授權（儲存前會列出哪些信箱的授權會被清除）。信箱自己覆寫的值不受影響：例如只覆寫識別碼的信箱，預設 tenant 改了仍要重新授權。舊版在每個信箱各自填寫的識別碼與 tenant 會照常使用，視同覆寫。

之後 token 會自動更新。若授權失效（例如改了密碼），通知對象會收到告警、總覽頁會顯示「重新授權」按鈕，再授權一次即可。

備援：也可以在叢集上以命令列授權，但完成後要重新啟動 Pod 才會套用：
```bash
kubectl -n verify-code-relay exec -it deploy/verify-code-relay -- /relay login outlook
kubectl -n verify-code-relay rollout restart deploy/verify-code-relay
```

## 匯入 YAML

平常不需要 YAML。已有設定檔（例如從舊版遷移，或想一次建立大量設定）時，可以用 `relay config import` 匯入成一個新版本；格式見 `config.example.yaml`：

```bash
relay config validate config.yaml          # 先檢查
relay config import config.yaml            # 匯入；K3s 上用 exec -i 從標準輸入傳入：
kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay config import /dev/stdin < config.yaml
```

- 機密可以寫成 `${環境變數}`，匯入時從**執行 import 的環境**展開後存進資料庫（K3s 上是 Pod 的環境，所以變數要在 `secret.env` 中）。`$$` 代表字面上的 `$`。
- 服務執行中匯入時，請重新啟動（K3s：`kubectl -n verify-code-relay rollout restart deploy/verify-code-relay`）或在 Web 上任意存檔一次以套用。
- 匯入會清除不再適用的 Microsoft 授權（信箱被移除，主機、埠號、帳號、client_id 或 tenant 改了，包括 `microsoft` 區段改了而信箱沿用它，或信箱剛改成 Microsoft 授權），並列出「信箱 X 的 Microsoft 授權已清除，請重新授權」；之後在 Web 上重新授權即可。
- 舊版的 `data_dir`、`http_listen` 會被略過，改用環境變數 `RELAY_DATA_DIR`、`RELAY_LISTEN`。
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
| `mailboxes.<名稱>` | `display_name`、`host`、`port`（預設 993）、`username`、`auth`、`folders`（預設 `INBOX`）、`targets`，以及覆寫用的 `max_age`、`ttl`、`poll_interval`、`templates` |
| `mailboxes.<名稱>.auth` | `{type: password, password: …}`，或 `{type: microsoft_oauth}`（匯入後在 Web 授權）；`client_id`、`tenant` 選填，寫了就覆寫 `microsoft` 的值 |
| `templates` | 全域範本 `code`、`link`、`both`（見「範本」） |
| `rules` | 自訂規則（見「辨識規則」） |
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

`import`、`validate` 的檔案也可以用 `--config` 或環境變數 `RELAY_CONFIG` 指定。其他指令一律讀取資料庫中的設定；設定在 Web 存檔即生效，不需要重新啟動。

## 辨識規則

### 內建辨識怎麼運作

- **內文來源**：以 HTML 版內文為主；HTML 版辨識不到時，才改用純文字版再辨識一次（有些信的 HTML 版才有粗體的驗證碼，也有些信的純文字版才有完整說明）。
- **驗證碼**：數字前後出現「驗證碼」「this code」「code below」「PIN is」「one-time PIN」「動態密碼」「安全性代碼」等關鍵字時會被辨識，粗體、大字或字距加寬的文字加分。單獨的「PIN」與「the code」不算關鍵字（常見於「忘記 PIN 請來電」或優惠碼）；只寫「PIN: 4821」的信請自行加關鍵字。
  - 主旨中的數字，若也獨佔內文一行（例如主旨「Use 123456 to set up recovery email」，內文另起一行寫 `123456`），即使附近沒有「驗證碼」等字樣也會被當成驗證碼；1900～2099 的年份，以及主旨提到訂單、發票、出貨、付款、訂房、案件、帳號等（order、invoice、payment、booking、case、account …）時除外。
  - 純英文字母的驗證碼（5～8 個大寫字母）只在緊接「code: / code is / 驗證碼：」等字樣、且獨佔一行或本身就是一段粗體時辨識（標題「YOUR CODE IS READY」裡的 READY 不算）。
- **連結**：關鍵字（驗證、確認、登入、重設密碼、verify、confirm、sign in …）可以出現在主旨、內文，或任何一個連結的文字。
  - 按鈕只寫「驗證」「验证」「Verify」等字樣，或按鈕是圖片（取圖片的替代文字）時，也會辨識。
  - 「Click here」「點此」這類泛用的連結文字，只有在同一行說明用途（「To verify your email, click here」），或上一行說明用途並指向下文（含 below、link、以下、下方，或以冒號結尾）時才採用；純文字信裡的裸網址同樣處理。這裡的英文關鍵字要完整成字（「confirmation」不算「confirm」），而且前文或主旨提到訂單、收據、付款、出貨（order、receipt、payment、訂單、付款 …）時不採用，以免轉發「Order confirmation:」下面的訂單頁。
  - 網址中的 `activation`、`validation`、`verify`、`token=` 等字樣也有加分。
- **會排除**：
  - 卡號末碼（末四碼、尾號、ending in、last 4 …）、郵遞區號（郵遞區號、邮编、zip、postal 與美國 `CA 94043`）、地址門牌（`1600 Main St`、`100 號`），以及電話、金額、時間、年份、訂單編號。
  - 優惠碼：同一行或上一行提到 promo、coupon、discount、voucher、checkout、% off、優惠、折扣、折價的碼（例如「Use this code SAVE20 at checkout」）。
  - 電子報常見連結：取消訂閱、在瀏覽器中檢視、聯絡我們、contact（只看連結文字與網址路徑），以及圖片、`.pdf`、`.css`、`.js` 等靜態檔案。這些連結的文字也不會讓信件通過「含連結關鍵字」的檢查。
  - 帳號被盜時的補救連結：連結前文（同一行或上一行）寫著「If this wasn't you」「didn't try to sign in」「didn't make this change」「若非本人操作」「密碼可能已外洩」這類說法時，不當成登入連結。但這類說法後面接著「請忽略」「ignore this email」時（例如「若非本人操作請忽略：[登入]」），代表是你自己申請的登入或重設密碼信，連結照常轉發。
  - 信裡已經找到驗證碼時，重設密碼、變更密碼、保護帳號、查看活動這類連結（連結文字或網址路徑含 password、密碼、secure、security、activity、安全 …）不會跟驗證碼一起轉發；只轉發驗證碼。
  - 一封信最多看 500 個不同的連結。

### 辨識不到或辨識錯誤時

1. 到 **規則與範本 → 辨識規則**，用頁面下方的「用信件測試」上傳那封信，看「辨識過程」（見下節）。也可以用命令列 `relay test --eml 信件.eml`（K3s／Docker 的寫法見上面「測試 .eml」），兩者的辨識過程相同。
2. 缺關鍵字：在同一頁的「辨識關鍵字」加入驗證碼或連結的關鍵字。
3. 特定寄件者格式特殊：在同一頁的「自訂規則」新增規則（比對寄件者、主旨，指定驗證碼或連結的正規表示式），或把處理方式設為「略過，不轉發」。
   - 驗證碼的正規表示式：對「主旨＋換行＋內文」比對；有擷取群組時取第一個有值的群組，沒有群組時取整段比對結果。
   - 連結的正規表示式：用來**挑選**連結，不是擷取網址的一部分。只要某個連結的網址符合，就轉發該連結的完整網址。
4. 存檔即生效；再寄一次或用「用信件測試」確認。

### 用信件測試

在 **規則與範本 → 辨識規則** 頁最下方：

- 上傳 `.eml`（也可以把檔案拖放到框內），或切換到「貼上原始碼」貼上信件原始碼。取得方式：
  - Gmail：信件右上「⋮」→「下載郵件」得到 `.eml`；或「顯示原始郵件」→「複製到剪貼簿」後貼上（不要全選整頁，頁首的摘要表格會破壞格式）。
  - Outlook：「⋯」→「檢視」→「檢視訊息原始碼」，複製後貼上。
- 使用**本頁目前填寫**的自訂規則與關鍵字，尚未儲存也會套用，所以可以先調整、測試，滿意再存檔。
- 「預覽訊息使用的信箱」可選填：不指定時用全域範本；選擇信箱則使用該信箱的範本與顯示名稱。
- 結果包含：結論（轉發驗證碼／轉發連結／被規則略過／不轉發）、驗證碼或連結、Telegram 訊息預覽、辨識過程（每個候選的分數與排除原因）、信件摘要（寄件者、主旨、內文前 500 字、連結清單）。
- 信件只在記憶體中處理：不會儲存、不會發送到 Telegram、不會寫進日誌。上限 5 MiB（與實際運作時略過的上限相同；超過時頁面直接提示，不會上傳）。需要啟用 JavaScript。

命令列 `relay test --eml` 仍可使用，並與網頁共用同一套辨識與渲染；只有它能用 `--send` 實際送出到 Telegram。

## 範本

在 **規則與範本 → 訊息範本** 編輯全域範本，信箱頁的「進階設定」可以為單一信箱覆寫；編輯時可以即時預覽。使用 Go `html/template`（Telegram HTML 格式），郵件內容會自動跳脫。可用變數：`.Code` `.URL` `.LinkText`（信裡連結的文字，例如「Verify email」）`.Host`（連結網域）`.Service` `.FromName` `.FromAddr` `.To` `.Subject` `.Mailbox` `.Date` `.Rule`。依結果使用 `code` / `link` / `both` 範本，查找順序：信箱範本 → 全域範本 → 內建。

內建範本不印完整網址，改用網域一行（`🌐 github.com`）讓你判斷連結去向，接著分行列出寄件者、收件者（附信箱顯示名稱）與主旨；欄位沒有值的行會省略。按鈕自動產生：「📋 複製驗證碼」，以及以信裡連結文字命名的連結按鈕（例如「🔗 重設密碼」，超過 32 字會截斷；連結沒有文字時顯示「🔗 開啟 網域」）。Telegram 不接受訊息的 HTML、改送純文字時沒有按鈕，連結會以「按鈕文字：網址」附在文末。自訂範本若用了 `{{.URL}}`，照樣印出完整網址。範本只能依序輸出，可用：欄位（含 `.Date.Format "15:04"` 這類方法）、`if`／`else if`／`else`、`with .欄位`、最多兩段的管線（例如 `{{.Subject | html}}`），以及函式 `and` `or` `not` `eq` `ne` `lt` `le` `gt` `ge` `len` `index` `slice` `html` `urlquery` `js` `print` `printf` `println`（每個動作最多 10 個參數；`printf` 的格式必須直接寫在範本裡，寬度與精確度最多 3 位數）。不支援 `range`、`template`、`define`／`block`、變數（`$`）與括號內的子運算。

## 健康檢查

健康檢查在 `RELAY_HEALTH_LISTEN`（預設 `:8081`），與 Web 分開，不要對外暴露：

| 路徑 | 說明 |
|---|---|
| `GET /livez` | 程序存活，固定 200（liveness probe） |
| `GET /readyz` | 服務正常且資料庫可查詢時 200，否則 503（readiness probe、`relay healthcheck`）。不看信箱狀態 |
| `GET /healthz` | 各信箱的狀態明細（`connected`、`reconnecting`、`auth_failed`、`needs_login`…）：全部正常（或沒有任何信箱）時 200，否則 503。只供查看，探針不使用 |

信箱的狀態也顯示在 Web 的總覽頁。

## 總覽與轉發紀錄

- **總覽的「最近轉發」**預設只列最近 10 封有轉發驗證碼或連結的信；切到「全部檢查過的信」可看最近 10 封檢查過的任何信件（包括未辨識、被規則略過的）。每 10 秒自動更新。
- **轉發紀錄頁**列出每一封檢查過的信，可以：
  - 切換「全部紀錄」與「只看轉發」；
  - 搜尋主旨或寄件者（主旨中已辨識的驗證碼記錄時就已遮蔽，搜尋不到驗證碼）；
  - 依信箱、種類（有轉發、驗證碼、連結、被規則略過、未辨識、過舊或過大）、結果、時間（24 小時、7 天、30 天內）篩選；
  - 每頁 20、50 或 100 筆，用頁碼跳頁。篩選條件都在網址裡，重新整理或分享連結都會保留。

## 開發

```bash
go test ./...                       # 單元與整合測試（含記憶體 IMAP 伺服器與經由 Web 的端對端測試）
python3 testdata/eml/gen.py         # 重新產生辨識測試信件
go build -o relay ./cmd/relay
RELAY_DATA_DIR=./data RELAY_ADMIN_PASSWORD='change-me-at-least-12-chars' ./relay run   # 密碼至少 12 字元；瀏覽器開 http://localhost:8080
```

## 需要手動驗證的項目

自動測試無法涵蓋以下情境，首次部署時請確認：
- [ ] 以瀏覽器走過所有頁面（桌機與手機寬度）。
- [ ] 經 Caddy → Traefik 的真實部署：登入、TOTP 綁定、Cookie 正常、通知中的 IP 正確（系統設定 → 目前的連線）。
- [ ] 在網頁上完成 Outlook 授權，且 token 過期後能自動更新（運行超過 1 小時後仍能收信）。
- [ ] 在 Telegram 頁以「讀取最近對話」取得 chat_id。
- [ ] Gmail 應用程式密碼登入、IDLE 收信後 Telegram 立即收到訊息。
- [ ] 中斷網路數分鐘後恢復，服務自動重連並補抓斷線期間的信。
- [ ] 重啟服務後，超過 `max_age` 的舊信不會被轉發。
- [ ] 連續寄兩封測試信（間隔一兩秒），兩封都應立即轉發，不會有一封延遲到 25 分鐘後才到（確認伺服器在 IDLE 開始時會回報先前到達的信）。
- [ ] 落入垃圾郵件匣的驗證信也會轉發（Gmail 需在信箱的資料夾勾選 `[Gmail]/Spam`，並在標籤設定勾選「在 IMAP 中顯示」）。
- [ ] 備援的命令列授權：`kubectl exec -it … /relay login <信箱>` 授權成功，重新啟動後仍有效。
- [ ] 在 **規則與範本 → 辨識規則** 的「用信件測試」上傳一封真實驗證信（`.eml` 與貼上原始碼各試一次），結果與 Telegram 預覽符合預期。
- [ ] `relay test --eml 信件.eml --send <目標>` 實際送出，Telegram 收到的訊息格式與按鈕（複製驗證碼、以連結文字命名的連結按鈕）正常。
