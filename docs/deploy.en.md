# Deployment guide

[繁體中文](deploy.md) | English

## Docker

For a local trial, see the [quick start in the README](../README.en.md#quick-start-docker-local-trial). For real use, point your HTTPS reverse proxy at `127.0.0.1:8080` and set `RELAY_TRUSTED_PROXIES` (see "Real client IPs" below).

- The image has a built-in `HEALTHCHECK` (`relay healthcheck`, which calls `:8081/readyz`). It only checks the service and the database, not the mailboxes. Mailbox status is on the dashboard.
- Every command except `run` is executed inside the running container, against the same database: `docker exec [-i|-it] relay /relay …`.

## K3s (Kubernetes)

The manifests in `deploy/k3s/` are managed with Kustomize:

- **A single replica.** SQLite cannot be written by two pods, and two instances would forward everything twice (`strategy: Recreate`).
- The database lives on a K3s `local-path` PVC. The pod runs as UID 65532 with a read-only root filesystem.
- The health port 8081 is only for the probes and is not exposed through the Service.

The example traffic path is browser → (HTTPS) **reverse proxy** → (HTTP) **K3s Traefik Ingress** → Service (port 80) → pod port 8080. The reverse proxy terminates TLS; Caddy is used as the example below. Terminating TLS at the Ingress with cert-manager works too.

### First deployment

1. **Pick a version.** `images.newTag` in `deploy/k3s/kustomization.yaml` is the image version to run (without the `v`, e.g. `0.3.1`). Available versions are listed on [GitHub Packages](https://github.com/lxtsuddy/verify-code-relay/pkgs/container/verify-code-relay). If you build your own image, change `newName` too.
2. **Set your domain.** Change `host` in `deploy/k3s/ingress.yaml`.
3. **Create `secret.env`** (already in `.gitignore`):
   ```bash
   cp .env.example deploy/k3s/secret.env   # only RELAY_ADMIN_PASSWORD is needed
   ```
   Use a long random password of at least 12 characters (e.g. `openssl rand -base64 24`). It is only used when the database has no admin yet; change the password later under 系統設定 (Settings) in the web UI.
4. **Deploy:**
   ```bash
   kubectl apply -k deploy/k3s
   kubectl -n verify-code-relay rollout status deploy/verify-code-relay
   kubectl -n verify-code-relay logs -f deploy/verify-code-relay
   ```
5. **Configure the reverse proxy.** With Caddy (`<Traefik address>` is the IP of a K3s node; Traefik listens on port 80 by default):
   ```
   relay.example.com {
       reverse_proxy <Traefik address>:80
   }
   ```
   Caddy keeps the `Host` header and sets `X-Forwarded-For` by default, and Traefik uses `Host` to find this Ingress. If Caddy runs inside the cluster, it can `reverse_proxy verify-code-relay.verify-code-relay.svc.cluster.local:80` directly, with no Ingress needed.
6. **First sign-in.** Open `https://relay.example.com` and sign in with the password from `secret.env`. Enrol TOTP as prompted: scan the QR code with an app such as Google Authenticator or 1Password, then enter a code. Next, follow the dashboard steps to add a bot, a target and a mailbox. Finally, pick a target under **系統設定 → 通知對象 (Settings → Notification target)** for sign-in notifications and alerts. See [Configuration](configuration.en.md) for details.

   Alternatively, import an existing YAML file with `relay config import` (see [YAML import](configuration.en.md#yaml-import)).

Without a domain or reverse proxy yet, configure it locally through a port-forward (`http://localhost` accepts the Secure cookie):

```bash
kubectl -n verify-code-relay port-forward deploy/verify-code-relay 8080:8080
# open http://localhost:8080
```

## Optional: real client IPs in notifications

Sign-in notifications, alerts and config versions record the client IP. `X-Forwarded-For` is only trusted when it comes from an address listed in `RELAY_TRUSTED_PROXIES`. The IP is used only for logs and notifications and never for security decisions.

In the K3s + Caddy setup above, Traefik does not trust the `X-Forwarded-For` sent by Caddy by default, so Caddy's address is what gets recorded. To see real IPs:

1. Make Traefik trust Caddy. On a K3s server node, create `/var/lib/rancher/k3s/server/manifests/traefik-config.yaml` (K3s applies it automatically):
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
               - <Caddy IP>/32
   ```
2. Make the service trust Traefik and Caddy. `RELAY_TRUSTED_PROXIES` in `deploy/k3s/deployment.yaml` defaults to `"10.42.0.0/16"`, the default K3s pod network that Traefik connects from. Add Caddy's IP, e.g. `"10.42.0.0/16,192.168.1.5"`, and run `kubectl apply -k deploy/k3s`.
3. Open **系統設定 → 目前的連線 (Settings → Current connection)** and check that the IP the server determined for you is your real address. The page also shows the connecting address and the `X-Forwarded-For` it received. If Traefik sees a source other than Caddy's IP, adjust both places above to match what the page shows. This can happen when Caddy and K3s share a host, where the source may be a node or pod-network address.

## Day-to-day operations

The examples use K3s. For Docker, replace `kubectl -n verify-code-relay exec [-i|-it] deploy/verify-code-relay --` with `docker exec [-i|-it] relay`.

- **Changing settings**: saving in the web UI applies changes immediately, with no redeploy or restart. Every save creates a version you can restore under **系統設定 → 版本歷史 (Settings → Version history)**; the last 50 versions are kept.
- **Upgrading**: change `newTag` in `kustomization.yaml` and run `kubectl apply -k deploy/k3s`. The database is migrated automatically, and a migrated database may not work with older versions, so back it up before downgrading.
- **Testing an .eml**: the image has no shell and no `tar`, so `kubectl cp` does not work. Pipe the mail through standard input with `exec -i` (no `-t`):
  ```bash
  kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay test --eml /dev/stdin < mail.eml
  kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay test --eml /dev/stdin --mailbox personal_gmail --send me < mail.eml
  ```
  To just see the recognition result, 用信件測試 (Test with a mail) in the web UI is easier; see [Recognition](recognition.en.md#testing-with-a-mail).
- **Health**: the readiness probe calls `:8081/readyz` and the liveness probe calls `:8081/livez`. Neither looks at mailboxes, so the pod stays ready and the web UI stays reachable when Outlook needs re-authorisation or a mailbox is disconnected, and you can fix it in the browser. Mailbox problems show up as Telegram alerts and on the dashboard. Kubernetes ignores the Docker `HEALTHCHECK` in the image.
- **Backups**: the database is on the PVC (`local-path` defaults to `/var/lib/rancher/k3s/storage/` on the node). You can also export the configuration as YAML:
  ```bash
  (umask 077; kubectl -n verify-code-relay exec deploy/verify-code-relay -- /relay config export > backup-$(date +%F).yaml)
  ```
  `umask 077` makes the export readable only by you. The database and exports contain every secret. Keep them somewhere safe, such as an encrypted backup or a password manager, and never commit them to git.
- **Resetting the admin** (lost phone or forgotten password): this clears TOTP and signs out every device. Sign in again afterwards and re-enrol. For non-interactive use, run it with `exec -i` and pass the new password as one line on standard input.
  ```bash
  kubectl -n verify-code-relay exec -it deploy/verify-code-relay -- /relay admin reset
  ```
- **Removing**: `kubectl delete -k deploy/k3s` deletes the PVC and the database along with everything else.
