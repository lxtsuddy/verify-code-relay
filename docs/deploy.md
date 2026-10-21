# 部署指南

繁體中文 | [English](deploy.en.md)

## Docker

本機試用見 [README 的快速開始](../README.md#快速開始docker本機試用)。正式使用時把 `-p 127.0.0.1:8080:8080` 交給你的 HTTPS 反向代理，並設定 `RELAY_TRUSTED_PROXIES`（見下方「真實 IP」）。

- 映像內建 `HEALTHCHECK`（`relay healthcheck`，打 `:8081/readyz`），只確認服務與資料庫正常，不看信箱狀態；信箱的狀況請看總覽頁。
- 除了 `run` 以外的指令都在執行中的容器內執行，讀取同一個資料庫：`docker exec [-i|-it] relay /relay …`。

## K3s（Kubernetes）

設定檔在 `deploy/k3s/`，以 Kustomize 管理：

- **單一 replica**：SQLite 不能由兩個 Pod 同時寫入，兩個實例也會重複轉發（`strategy: Recreate`）。
- K3s 內建 `local-path` PVC 存放資料庫；以 UID 65532、唯讀根檔案系統執行。
- 健康檢查埠 8081 只給探針使用，不經 Service 暴露。

範例的對外路徑：瀏覽器 →（HTTPS）**反向代理** →（HTTP）**K3s Traefik Ingress** → Service（port 80）→ Pod 的 8080。TLS 由反向代理處理，下面以 Caddy 為例；用 cert-manager 在 Ingress 上設定 TLS 也可以。

### 首次部署

1. **選擇版本**：`deploy/k3s/kustomization.yaml` 的 `images.newTag` 是要使用的映像版本（不含 `v`，例如 `0.3.1`），可用的版本見 [GitHub Packages](https://github.com/lxtsuddy/verify-code-relay/pkgs/container/verify-code-relay)。使用自己建置的映像時一併修改 `newName`。
2. **指定網域**：`deploy/k3s/ingress.yaml` 的 `host` 改成你的網域。
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
5. **設定反向代理**。以 Caddy 為例（`<Traefik 位址>` 是 K3s 節點的 IP，Traefik 預設在 80 埠）：
   ```
   relay.example.com {
       reverse_proxy <Traefik 位址>:80
   }
   ```
   Caddy 預設保留 `Host` 並設定 `X-Forwarded-For`，Traefik 依 `Host` 找到這個 Ingress。Caddy 也在叢集內時，可以直接 `reverse_proxy verify-code-relay.verify-code-relay.svc.cluster.local:80`，不需要 Ingress。
6. **首次登入**：瀏覽器開 `https://relay.example.com` → 用 `secret.env` 的密碼登入 → 依畫面綁定兩步驟驗證（用 Google Authenticator、1Password 等 App 掃 QR code，輸入一組驗證碼）→ 依總覽頁的步驟新增 Bot、目標與信箱，並在 **系統設定 → 通知對象** 選一個目標接收登入通知與告警。各項設定見[設定說明](configuration.md)。

   也可以改用 `relay config import` 匯入現成的 YAML（見[匯入 YAML](configuration.md#匯入-yaml)）。

還沒有網域或反向代理時，可以先用 port-forward 在本機設定（`http://localhost` 允許 Secure cookie）：

```bash
kubectl -n verify-code-relay port-forward deploy/verify-code-relay 8080:8080
# 瀏覽器開 http://localhost:8080
```

## 選配：在通知中看到真實 IP

登入通知、告警與設定版本會記錄用戶端 IP。程式只採信 `RELAY_TRUSTED_PROXIES` 列出的位址送來的 `X-Forwarded-For`；IP 只用於記錄與通知，不影響任何安全判斷。

以上面的 K3s＋Caddy 為例，預設 Traefik 不信任 Caddy 送來的 `X-Forwarded-For`，記錄到的會是 Caddy 的位址。要看到真實 IP：

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
3. 到 **系統設定 → 目前的連線** 確認「伺服器判定你的 IP」是你的真實位址。頁面也會列出連線來源與收到的 `X-Forwarded-For`；如果 Traefik 看到的來源不是 Caddy 的 IP（例如 Caddy 與 K3s 在同一台主機時，可能是節點或 Pod 網段的位址），依頁面顯示的位址調整上面兩處。

## 日常操作

以下以 K3s 為例；Docker 把 `kubectl -n verify-code-relay exec [-i|-it] deploy/verify-code-relay --` 換成 `docker exec [-i|-it] relay`。

- **修改設定**：在 Web 存檔即生效，不需要重新部署或重啟。每次存檔留下一個版本，可在 **系統設定 → 版本歷史** 還原（保留最近 50 版）。
- **升級**：修改 `kustomization.yaml` 的 `newTag`，再 `kubectl apply -k deploy/k3s`。資料庫會自動升級；升級後的資料庫不一定能給舊版使用，降版前請先備份。
- **測試 .eml**：映像沒有 shell 也沒有 `tar`，`kubectl cp` 無法使用。改用 `exec -i`（不要加 `-t`）把信件從標準輸入傳進去：
  ```bash
  kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay test --eml /dev/stdin < 信件.eml
  kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay test --eml /dev/stdin --mailbox personal_gmail --send me < 信件.eml
  ```
  只想看辨識結果時，用 Web 的「用信件測試」更方便，見[辨識規則](recognition.md#用信件測試)。
- **健康狀態**：readiness probe 打 `:8081/readyz`、liveness probe 打 `:8081/livez`，都**不看信箱狀態**，所以 Outlook 需要重新授權或信箱斷線時 Pod 仍然就緒、Web 仍可連線，可以直接在網頁上處理。信箱的問題由 Telegram 告警與總覽頁呈現。Kubernetes 不使用映像內的 Docker `HEALTHCHECK`。
- **備份**：資料庫在 PVC 上（`local-path` 預設位於節點的 `/var/lib/rancher/k3s/storage/`）。設定也可以匯出成 YAML：
  ```bash
  (umask 077; kubectl -n verify-code-relay exec deploy/verify-code-relay -- /relay config export > backup-$(date +%F).yaml)
  ```
  `umask 077` 讓匯出檔只有自己能讀。資料庫與匯出檔都含有全部機密，請存放在安全的地方（加密的備份、密碼管理器），不要提交到 git。
- **重設管理員**（遺失手機或忘記密碼）：會清除 TOTP 並登出所有裝置，之後重新登入綁定。非互動時可用 `exec -i` 從標準輸入傳入一行新密碼。
  ```bash
  kubectl -n verify-code-relay exec -it deploy/verify-code-relay -- /relay admin reset
  ```
- **刪除**：`kubectl delete -k deploy/k3s` 會一併刪除 PVC 與資料庫。
