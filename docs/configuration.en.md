# Configuration

[繁體中文](configuration.md) | English

Everything is configured in the web admin UI, and changes take effect on save. Every save creates a version you can restore under **系統設定 → 版本歷史 (Settings → Version history)**. The UI is in Traditional Chinese; labels are given here with an English translation.

## Telegram

On the **Telegram** page:

1. Create a bot with [@BotFather](https://t.me/BotFather) and add its token under Bot. The token is checked with Telegram. Replace the token here later if needed; it takes effect on save.
2. Send the bot a message. For a group, add the bot to the group and post in it. For a channel, make the bot an admin and publish a post.
3. Add a target under 目標 (Targets). Press 讀取最近對話 (Read recent chats) to list the chats the bot recently received messages from; picking one fills in the chat_id. You can also enter it by hand: @userinfobot shows your personal ID, and group/channel IDs usually start with `-100`.
4. Press 發送測試 (Send test) next to the target to check that messages arrive.

## Mailboxes

Add mailboxes on the **信箱 (Mailboxes)** page. Pick a provider (this fills in the IMAP host) and enter the account and password. Then, under 轉發路由 (Routes), write one folder → target pair per row: mail arriving in a folder goes to the target on that row. To send a folder to several targets, add several rows. 測試連線 (Test connection) actually signs in and lists the folders to pick from. Only folders that appear in a route are watched. Watching starts as soon as you save.

A mailbox's 名稱 (Name) cannot be changed after creation, because sync state and authorisation are stored under it. The 顯示名稱 (Display name) can be changed at any time.

### Gmail / iCloud / self-hosted (app passwords)

- Gmail: turn on 2-Step Verification for the Google account, then create a password under Security → App passwords. Host `imap.gmail.com`.
- To watch folders such as `[Gmail]/Spam`, open Gmail Settings → Labels and tick "Show in IMAP" for that label. It then appears in Test connection.
- iCloud: host `imap.mail.me.com`; create an app-specific password at appleid.apple.com.
- Any other mailbox with IMAP over TLS (port 993 by default): pick 自訂 (Custom) and enter the host and port.

### Microsoft (Outlook.com / Hotmail / Microsoft 365)

Microsoft no longer allows password sign-in over IMAP, so OAuth2 is required. You need to register your own Azure application (free):

1. In the Azure portal, go to Microsoft Entra ID → App registrations → New registration.
   - Supported account types: "Personal Microsoft accounts only" for personal accounts, or "Accounts in any organizational directory and personal Microsoft accounts".
   - Redirect URI: not needed.
2. Authentication → Advanced settings → set "Allow public client flows" to Yes.
3. API permissions → Add a permission → Delegated permissions: `IMAP.AccessAsUser.All` and `offline_access`.
4. In the web UI, open **系統設定 → Microsoft 應用程式 (Settings → Microsoft application)** and enter the Application (client) ID. This is done once and shared by all Microsoft mailboxes. For the default tenant, use `consumers` for personal accounts and `organizations` or your tenant ID for work or school accounts. Leaving it empty means `common`.
5. When adding the mailbox, pick Outlook (or Microsoft 授權 / Microsoft authorisation) and enter the account. To use a different Azure application or tenant for one mailbox, override it under the mailbox's 進階設定 (Advanced settings). Leave the fields empty to use the system settings.
6. After saving, press 授權 (Authorise) on the mailbox page (or the dashboard). Follow the prompt to enter the code on Microsoft's page and sign in. Watching starts as soon as authorisation completes, with no restart needed.

Authorisation is tied to the client ID and tenant in use. If you change them in the system settings, every mailbox that inherits them must be re-authorised; the affected mailboxes are listed before you save. Mailboxes that override the values are not affected. For example, a mailbox that overrides only the client ID still needs re-authorisation when the default tenant changes.

Tokens refresh automatically after that. If authorisation stops working (for example after a password change), the notification target gets an alert and the dashboard shows a 重新授權 (Re-authorise) button.

As a fallback you can authorise from the command line on the server, but the service must be restarted afterwards:

```bash
kubectl -n verify-code-relay exec -it deploy/verify-code-relay -- /relay login outlook
kubectl -n verify-code-relay rollout restart deploy/verify-code-relay
```

## System settings

- **通知對象 (Notification target)**: the target for sign-in notifications and system alerts (disconnects, failed authorisation, failed sends). Without one, these only go to the log.
- **轉發預設值 (Forwarding defaults)**: how old a mail may be and still be forwarded (10 minutes by default), how long an unsent message is kept before being dropped (15 minutes by default), the polling interval for servers without IDLE, how long a mailbox must stay disconnected before an alert, and the log level. A mailbox can override the age limit, retention and polling interval under its Advanced settings.
- **登入安全 (Sign-in security)**: change the password or re-enrol TOTP. Both sign out all other devices.
- **目前的連線 (Current connection)**: the client IP as determined by the server, for checking your reverse proxy setup (see the [deployment guide](deploy.en.md#optional-real-client-ips-in-notifications)).

## YAML import

YAML is not needed day to day. To create a lot of configuration at once, or to move from a backup, use `relay config import` to import a file as a new version. The format is shown in [`config.example.yaml`](../config.example.yaml):

```bash
relay config validate config.yaml          # check first
relay config import config.yaml            # import; on K3s, pipe it through stdin with exec -i:
kubectl -n verify-code-relay exec -i deploy/verify-code-relay -- /relay config import /dev/stdin < config.yaml
```

- Secrets can be written as `${ENV_VAR}`. They are expanded from the environment **where import runs** and stored in the database. On K3s that is the pod's environment, so the variables must be in `secret.env`. `$$` is a literal `$`.
- If you import while the service is running, restart it (K3s: `kubectl -n verify-code-relay rollout restart deploy/verify-code-relay`) or save anything in the web UI to apply the import.
- Import clears Microsoft authorisations that no longer apply and lists each mailbox whose authorisation was cleared; re-authorise those in the web UI. An authorisation no longer applies when:
  - the mailbox was removed;
  - its host, port, account, client_id or tenant changed, including a change to the `microsoft` section that the mailbox inherits;
  - the mailbox was just switched to Microsoft authorisation.
- `relay config export` prints the current configuration in the same format, secrets included, for backups or migration.

Fields:

| Field | Description |
|---|---|
| `log_level` | `debug`, `info`, `warn`, `error` |
| `bots.<name>.token` | Bot token |
| `targets.<name>` | `bot` (bot name) and `chat_id` |
| `admin` | Notification target (a target name) for sign-in notifications and system alerts (disconnects, failed authorisation, failed sends) |
| `defaults` | `max_age` (do not forward mail older than this), `ttl` (drop unsent messages older than this), `alert_ttl`, `poll_interval` (polling interval when the server lacks IDLE), `alert_after` (how long a mailbox must stay disconnected before an alert) |
| `microsoft` | The Azure application shared by Microsoft mailboxes: `client_id`, `tenant` (optional, defaults to `common`; use `consumers` for personal accounts) |
| `mailboxes.<name>` | `display_name`, `host`, `port` (default 993), `username`, `auth`, `routes` (`folder` → `target` pairs, one per entry; the watched folders are exactly those in the routes; the old `folders` + `targets` form is still read and becomes "every folder → every target"), plus the overrides `max_age`, `ttl`, `poll_interval`, `templates` |
| `mailboxes.<name>.auth` | `{type: password, password: …}` or `{type: microsoft_oauth}` (authorise in the web UI after importing); optional `client_id` and `tenant` override the `microsoft` section |
| `templates` | Global templates `code`, `link`, `both` (see [Templates](recognition.en.md#templates)) |
| `rules` | Custom rules (see [Recognition](recognition.en.md)) |
| `recognize` | `extra_code_keywords`, `extra_link_keywords` |

## Commands

While the service is running, every command except `run` is executed inside the container, against the same database. On K3s that is `kubectl -n verify-code-relay exec [-i|-it] deploy/verify-code-relay -- /relay …`; with Docker, `docker exec [-i|-it] relay /relay …`.

| Command | Description |
|---|---|
| `relay run` | Start the service, the web admin UI (`:8080`) and health checks (`:8081`) |
| `relay config import <file>` | Import a YAML configuration (expanding `${ENV_VAR}`) as a new version |
| `relay config export` | Print the current configuration (secrets included) for backups |
| `relay config validate [file]` | Check a YAML configuration file without touching the database |
| `relay admin reset` | Reset the admin password, clear TOTP and sign out every device |
| `relay login <mailbox>` | Microsoft authorisation (fallback; prefer the web UI); restart afterwards |
| `relay test --eml <file> [--mailbox <mailbox>] [--send <target>]` | Show the recognition trace and rendered message for an .eml file. Recognition is the same as Test with a mail in the web UI, but only this command can actually send with `--send`. In Gmail on the web, "Download message" gives you the .eml |
| `relay send-test <target>` | Send a test message (also available on the Telegram page) |
| `relay healthcheck` | Check `:8081/readyz` (used by the image's `HEALTHCHECK`) |

The file for `import` and `validate` can also be given with `--config` or the `RELAY_CONFIG` environment variable. All other commands read the configuration from the database.

## Health checks

Health checks listen on `RELAY_HEALTH_LISTEN` (default `:8081`), separate from the web UI. Do not expose them:

| Path | Description |
|---|---|
| `GET /livez` | The process is alive; always 200 (liveness probe) |
| `GET /readyz` | 200 when the service is up and the database answers, otherwise 503 (readiness probe, `relay healthcheck`). Mailboxes are not considered |
| `GET /healthz` | Per-mailbox status (`connected`, `reconnecting`, `auth_failed`, `needs_login`, …): 200 when all are fine (or there are none), otherwise 503. For inspection only; not used by probes |

Mailbox status is also shown on the dashboard.

## Dashboard and forwarding history

- **最近轉發 (Recent forwards)** on the dashboard lists the 10 latest mails with a recognised code or link that were to be forwarded, including ones still sending, failed or expired. Switch to 全部檢查過的信 (All checked mail) to see the 10 latest mails of any kind, including unrecognised mails and mails skipped by a rule. The list refreshes every 10 seconds.
- **轉發紀錄 (Forwarding history)** lists every checked mail. You can:
  - switch between 全部紀錄 (All) and 只看轉發 (Forwarded only);
  - search by subject or sender (codes in subjects are masked when recorded, so you cannot search for a code);
  - filter by mailbox, kind (forwarded, code, link, skipped by rule, unrecognised, too old or too large), result, and time (last 24 hours, 7 days or 30 days; records are kept for 30 days);
  - show 20, 50 or 100 rows per page and jump between pages. The filters live in the URL, so they survive reloads and shared links.
- Codes and URLs are **never** written to logs or the forwarding history. If a subject contains a recognised code, it is recorded as `******`.
