# Dispatch

**One command. Any app. Instant notifications.**

Dispatch is a small, open-source tool that sends Telegram notifications from your terminal, scripts, cron jobs, CI pipelines, and any application that can run a process or make an HTTP request.

```sh
dispatch send "Backup successful"
```

- **CLI mode:** one command sends a message and exits with a meaningful exit code.
- **HTTP mode:** `dispatch serve` runs an authenticated local API, so apps never need to handle Telegram credentials.
- **No infrastructure:** a single static binary. You don't need a database, a queue, or a hosted service.
- **Secure by default:** listens on loopback only, requires an API key, sends plain text, and keeps secrets out of logs and responses.

---

## Contents

- [Install](#install)
- [Quick start](#quick-start)
- [Configuration](#configuration)
- [CLI usage](#cli-usage)
- [HTTP API](#http-api)
- [Using Dispatch from other languages](#using-dispatch-from-other-languages)
- [Delivery semantics and retries](#delivery-semantics-and-retries)
- [Security](#security)
- [Production deployment](#production-deployment)
- [Troubleshooting](#troubleshooting)
- [Development](#development)

---

## Install

### Prebuilt binaries

Download the archive for your platform from [GitHub Releases](https://github.com/iraosandeep/dispatch/releases). Builds are available for macOS (Apple Silicon and Intel), Linux (amd64 and arm64), and Windows (amd64). You don't need Go installed.

```sh
# Example: Linux amd64
VERSION=1.0.0
curl -LO https://github.com/iraosandeep/dispatch/releases/download/v${VERSION}/dispatch_${VERSION}_linux_amd64.tar.gz
curl -LO https://github.com/iraosandeep/dispatch/releases/download/v${VERSION}/checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf dispatch_${VERSION}_linux_amd64.tar.gz
sudo install dispatch /usr/local/bin/
```

On macOS, if Gatekeeper blocks the unsigned binary, run `xattr -d com.apple.quarantine ./dispatch`.

### With Go

```sh
go install github.com/iraosandeep/dispatch/cmd/dispatch@latest
```

### Homebrew

Planned for after the first stable release.

---

## Quick start

Setup takes about five minutes.

1. **Create a bot.** In Telegram, message [@BotFather](https://t.me/BotFather), send `/newbot`, and copy the token.
2. **Run setup:**

   ```sh
   dispatch config init
   ```

   The setup:
   - validates your token
   - asks you to send your bot a message, then detects your chat ID
   - can generate an API key for the HTTP server
   - saves everything to a config file that only you can read
   - sends a test message

3. **Send a notification:**

   ```sh
   dispatch send "Hello from Dispatch"
   ```

To send to a group, add the bot to the group and send a message there during setup. Dispatch will list the group as a choice.

---

## Configuration

Settings are resolved in this order. Earlier sources win.

1. Command-line flags
2. Environment variables
3. Config file
4. Defaults

| Setting | Environment variable | Flag | Default |
|---|---|---|---|
| Bot token | `DISPATCH_BOT_TOKEN` | *(none, by design)* | none |
| Chat ID | `DISPATCH_CHAT_ID` | `--chat-id` | none |
| HTTP API key(s) | `DISPATCH_API_KEY` (comma-separated) | *(none, by design)* | none |
| Listen address | `DISPATCH_ADDR` | `serve --addr` | `127.0.0.1:8787` |
| Allow non-loopback | `DISPATCH_ALLOW_REMOTE` | `serve --allow-remote` | `false` |
| Delivery timeout (incl. retries) | `DISPATCH_TIMEOUT` | `--timeout` | `30s` |
| HTTP rate limit (req/s, `0` = off) | `DISPATCH_RATE_LIMIT` | `serve --rate-limit` | `1` |
| TLS certificate / key | `DISPATCH_TLS_CERT` / `DISPATCH_TLS_KEY` | `serve --tls-cert` / `--tls-key` | off |
| Config file path | `DISPATCH_CONFIG` | `--config` | `~/.config/dispatch/config.json` |
| Telegram API base URL | `DISPATCH_TELEGRAM_API_URL` | | `https://api.telegram.org` |

The bot token and API keys have **no command-line flags**. Process arguments are visible to other users on the same machine, for example through `ps`.

### Config file

`dispatch config init` writes the config file with mode `0600` in a directory with mode `0700`. It honors `$XDG_CONFIG_HOME`. On Windows, the file goes in `%AppData%\dispatch\config.json`. If the file contains secrets and other users can read it, Dispatch prints a warning.

```json
{
  "bot_token": "123456789:AA...",
  "chat_id": "-1001234567890",
  "api_keys": ["dsp_..."],
  "timeout": "30s"
}
```

```sh
dispatch config show      # effective settings, secrets masked, with each value's source
dispatch config path      # where the config file lives
dispatch config gen-key   # print a new random API key
```

### Environment-only setup for servers and CI

```sh
export DISPATCH_BOT_TOKEN=123456789:AA...
export DISPATCH_CHAT_ID=-1001234567890
dispatch send "Deploy finished"
```

Commands that run in automation never prompt. `config init` is the only interactive command, and it refuses to run without a terminal.

---

## CLI usage

```sh
dispatch send "Backup successful"
dispatch send Backup successful                 # unquoted words are joined
dispatch send --title "Import Failed" --priority high "The accounting import failed"
dispatch send --emoji ✅ "Tests passed"
echo "Deployment succeeded" | dispatch send --stdin
dispatch send --file ./report.txt                # file contents as the message text
dispatch send --attach ./backup.log "Nightly log" # upload a file, message becomes the caption
dispatch send --attach ./report.pdf --title "Monthly report"
dispatch send --chat-id -100987654321 "To another chat"
dispatch send -q "No output on success"
dispatch test                                    # verify the token and send a test message
dispatch version
```

### Message rules

- Messages are sent as **plain text**. Telegram does not interpret markup, so the content needs no escaping.
- Empty messages are rejected.
- Telegram's limit is 4096 characters, counted in UTF-16 units, so most emoji count as two. The count includes the title and any priority or emoji prefixes. **Dispatch never truncates.** It rejects oversized messages and reports their length.
- A single trailing newline is removed from `--stdin` and `--file` input.

### Attachments

`--file` sends a text file's *contents* as the message. `--attach` uploads the file itself as a Telegram **document**, so any file type works: logs, CSVs, PDFs, images, and archives.

- The file can be at most **50 MB**, which is Telegram's limit for bots. Empty files and directories are rejected. To send a directory, archive it first, for example with `tar czf logs.tgz logs/`.
- The message is optional. If present, it becomes the **caption**, limited to **1024 characters** including the title and any prefixes. Captions are plain text, like messages.
- `--title`, `--emoji` and `--priority` work as they do for text messages. `low` uploads silently.
- When `--attach` is used, the default timeout increases to **2 minutes**. An explicit `--timeout` or `DISPATCH_TIMEOUT` overrides it.

### Priority

Telegram has no native notification priority. Dispatch maps priorities onto what Telegram supports:

| Priority | Behavior on Telegram |
|---|---|
| `low` | Delivered **silently**, with no sound or vibration |
| `normal` | Delivered with the chat's usual notification settings |
| `high` | Delivered with sound and prefixed with 🚨 |

`high` **cannot** override a muted chat, Do Not Disturb, or the recipient's notification settings.

### Exit codes

| Code | Meaning | Example causes |
|---|---|---|
| `0` | Telegram accepted the message | |
| `1` | Delivery failed | Telegram rejected the message, unexpected error |
| `2` | Invalid usage or input | Empty or oversized message or caption, missing/empty/oversized attachment, bad flag |
| `3` | Configuration problem | Missing or invalid token, chat not found, bot blocked |
| `4` | Transient failure; retrying later may succeed | Rate limit, timeout, network or DNS error |

### Shell scripts

```bash
#!/usr/bin/env bash
set -u

if python manage.py import_data; then
  dispatch send "Import completed successfully"
else
  status=$?
  dispatch send --title "Import Failed" --priority high \
    "import_data exited with status $status on $(hostname)"
  exit "$status"
fi
```

A failed notification should not hide the job's own exit status. To keep a notification failure from failing the job, append `|| true`:

```bash
dispatch send "Nightly backup done" || true
```

To report the outcome of any command:

```bash
run_and_notify() {
  if "$@"; then dispatch send -e ✅ "Succeeded: $*"
  else s=$?; dispatch send -p high -e ❌ "Failed ($s): $*"; return $s; fi
}
run_and_notify ./backup.sh
```

Cron:

```cron
0 3 * * * /usr/local/bin/backup.sh && /usr/local/bin/dispatch send -q "Backup OK" || /usr/local/bin/dispatch send -p high "Backup FAILED"
```

GitHub Actions:

```yaml
- name: Notify
  if: always()
  env:
    DISPATCH_BOT_TOKEN: ${{ secrets.DISPATCH_BOT_TOKEN }}
    DISPATCH_CHAT_ID: ${{ secrets.DISPATCH_CHAT_ID }}
  run: dispatch send --title "${{ github.repository }}" "Workflow ${{ github.workflow }}: ${{ job.status }}"
```

---

## HTTP API

```sh
export DISPATCH_API_KEY=$(dispatch config gen-key)   # or let `config init` generate one
dispatch serve                                       # listens on 127.0.0.1:8787
```

### `POST /notify`

```sh
curl -X POST http://localhost:8787/notify \
  -H "Authorization: Bearer $DISPATCH_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"message":"Backup successful","title":"Backup Job","priority":"normal"}'
```

| Field | Type | Required | Notes |
|---|---|---|---|
| `message` | string | yes | Plain text, non-empty |
| `title` | string | no | One line, at most 256 characters |
| `priority` | string | no | `low`, `normal` (default), or `high` |
| `emoji` | string | no | Short prefix, for example `"✅"` |

Requests must use `Content-Type: application/json`, contain exactly one JSON object, and be at most 64 KiB. Unknown fields are rejected.

#### Uploading a file

To attach a file, send `multipart/form-data` instead of JSON. Use the same text fields plus one `file` part:

```sh
curl -X POST http://localhost:8787/notify \
  -H "Authorization: Bearer $DISPATCH_API_KEY" \
  -F message="Monthly report" \
  -F priority=low \
  -F file=@./report.pdf
```

- `file` is optional, and only one file is allowed per request. It can be at most 50 MB.
- With a file, `message` is optional and becomes the caption, limited to 1024 characters.
- Unknown parts are rejected. Uploads get at least 2 minutes to reach Telegram.
- Uploads are held in memory, so at most 4 run at once. Beyond that, the server returns `429 RATE_LIMITED` with `Retry-After`.

**Success** returns `200`:

```json
{ "ok": true, "message": "Notification accepted" }
```

**Failure** returns:

```json
{ "ok": false, "error": { "code": "DELIVERY_FAILED", "message": "Telegram could not accept the notification" } }
```

`"ok": true` means **Telegram accepted** the message. It doesn't confirm that the message reached a device or that anyone read it.

### Status and error codes

Error codes are stable and safe to match in code.

| HTTP | `error.code` | Meaning |
|---|---|---|
| 400 | `INVALID_REQUEST` | Malformed JSON, unknown field, wrong type, invalid priority or title |
| 400 | `EMPTY_MESSAGE` | `message` missing or blank |
| 401 | `UNAUTHORIZED` | Missing or invalid API key |
| 404 | `NOT_FOUND` | Unknown path |
| 405 | `METHOD_NOT_ALLOWED` | Wrong HTTP method (see the `Allow` header) |
| 413 | `PAYLOAD_TOO_LARGE` | Request body over 64 KiB |
| 413 | `PAYLOAD_TOO_LARGE` (upload) | Multipart body over 51 MiB, or a text field over 64 KiB |
| 413 | `MESSAGE_TOO_LARGE` | Rendered message over 4096 characters, or caption over 1024 |
| 413 | `ATTACHMENT_TOO_LARGE` | File over Telegram's 50 MB bot upload limit |
| 415 | `UNSUPPORTED_MEDIA_TYPE` | `Content-Type` is not `application/json` or `multipart/form-data` |
| 429 | `RATE_LIMITED` | Dispatch's or Telegram's rate limit; honor `Retry-After` |
| 502 | `INVALID_CREDENTIALS` | Telegram rejected the bot token |
| 502 | `INVALID_DESTINATION` | Chat not found, bot blocked or removed, group migrated |
| 502 | `NETWORK_ERROR` | Telegram could not be reached (connection or DNS failure) |
| 502 | `DELIVERY_FAILED` | Telegram returned another error or is unavailable |
| 503 | `NOT_READY` / `NOT_CONFIGURED` | Missing Telegram configuration |
| 504 | `TIMEOUT` | Telegram did not respond in time. The message may still have been delivered. |

### Health checks

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | The process is alive. Always `200`. |
| `GET /readyz` | Telegram settings are present (`200`), or missing (`503`). |

Neither endpoint sends a message or contacts Telegram. A healthy server does **not** prove that Telegram is reachable or that the token is valid. Use `dispatch test` to check those.

### Server behavior

- Binds to `127.0.0.1:8787` by default. Any non-loopback address requires `--allow-remote`. See [Security](#security).
- Every `/notify` request needs a bearer API key. The server refuses to start without one.
- `/notify` is limited to 1 request/s by default, with a burst of 5. Telegram allows about one message per second per chat. Change this with `--rate-limit` or turn it off with `0`.
- Each delivery is bounded by `--timeout` (default 30s, retries included). Server read, write, and idle timeouts are also bounded.
- `SIGINT` and `SIGTERM` trigger a graceful shutdown: the server stops accepting connections and lets in-flight deliveries finish, waiting up to 20s.
- Logs record method, path, status, and duration. They never include headers, API keys, the bot token, or message content. Use `--log-format json` for structured logs.

---

## Using Dispatch from other languages

Any language can either run `dispatch send` or call the HTTP API.

**Python**

```python
import os, requests

def notify(message, title=None, priority="normal"):
    r = requests.post(
        "http://127.0.0.1:8787/notify",
        headers={"Authorization": f"Bearer {os.environ['DISPATCH_API_KEY']}"},
        json={"message": message, "title": title, "priority": priority},
        timeout=35,
    )
    body = r.json()
    if not body["ok"]:
        raise RuntimeError(f"{body['error']['code']}: {body['error']['message']}")

# Or without a server:
import subprocess
subprocess.run(["dispatch", "send", "--title", "Job", "Finished"], check=True)
```

**Django** management command or Celery task:

```python
from django.core.management.base import BaseCommand
import subprocess

class Command(BaseCommand):
    def handle(self, *args, **opts):
        try:
            run_import()
            subprocess.run(["dispatch", "send", "Import completed"], check=False)
        except Exception as exc:
            subprocess.run(["dispatch", "send", "-p", "high", "-t", "Import Failed", str(exc)], check=False)
            raise
```

Uploading a file from Python:

```python
with open("report.pdf", "rb") as f:
    requests.post(
        "http://127.0.0.1:8787/notify",
        headers={"Authorization": f"Bearer {os.environ['DISPATCH_API_KEY']}"},
        data={"message": "Monthly report"},
        files={"file": ("report.pdf", f)},
        timeout=150,
    )
```

**Node.js**

```js
const res = await fetch("http://127.0.0.1:8787/notify", {
  method: "POST",
  headers: {
    Authorization: `Bearer ${process.env.DISPATCH_API_KEY}`,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ message: "Build finished", priority: "low" }),
});
const body = await res.json();
if (!body.ok) throw new Error(`${body.error.code}: ${body.error.message}`);
```

**Go**

```go
body := strings.NewReader(`{"message":"Worker finished"}`)
req, _ := http.NewRequest("POST", "http://127.0.0.1:8787/notify", body)
req.Header.Set("Authorization", "Bearer "+os.Getenv("DISPATCH_API_KEY"))
req.Header.Set("Content-Type", "application/json")
resp, err := http.DefaultClient.Do(req)
```

---

## Delivery semantics and retries

- **Success means Telegram accepted the message.** Neither Telegram nor Dispatch confirms that a device received it or that anyone read it.
- **Transient failures are retried automatically:** network errors, timeouts, HTTP 5xx, and HTTP 429. Dispatch makes up to 3 attempts, with exponential backoff and jitter (0.5s, then 1s, capped at 8s), all within the overall `--timeout`.
- **Telegram's `retry_after` is honored** when it is 30s or less and fits within the remaining timeout. Otherwise Dispatch fails right away with `RATE_LIMITED` (exit code `4`, HTTP `429` with `Retry-After`).
- **Permanent failures are not retried.** These include an invalid token, chat not found, a blocked bot, or a malformed message.
- **Uploads are retried too.** The whole file is sent again on each attempt. Each JSON call has a 15-second limit per attempt. Uploads are limited only by the overall timeout.
- **Duplicates are possible.** If a request times out after Telegram received it, Dispatch cannot tell whether Telegram accepted it. Retrying in that case can deliver the message twice. Dispatch prefers at-least-once delivery for transient errors. It cannot provide exactly-once delivery.
- Nothing is queued. If every attempt fails, the error goes back to the caller, which decides what to do next.

---

## Security

**Credentials**

- Use environment variables or a secret manager in production. Use the `0600` config file on personal machines.
- Never commit tokens or API keys. The `.gitignore` excludes `config.json`, `.env*`, and key files.
- Secrets are never logged and never returned in HTTP responses. `config show` masks them. Telegram network errors are scrubbed so the bot token, which is part of Telegram's request URL, never appears in them.
- Secrets have no command-line flags, which keeps them out of `ps` output and shell history.

**HTTP authentication**

- Every `/notify` request needs `Authorization: Bearer <key>`. API keys must be at least 16 characters. `dispatch config gen-key` produces 256-bit random keys.
- Keys are compared in constant time.
- **Key rotation:** set `DISPATCH_API_KEY=new_key,old_key`, move clients to the new key, then remove the old one. Restart the server after each change.

**Network exposure**

- The default bind address is `127.0.0.1`, so only local processes can connect.
- Binding to any other address, including `0.0.0.0` or `:8787`, requires `--allow-remote`. The server then logs warnings.
- For remote clients, use a private network or VPN, such as a VPC, Tailscale, or WireGuard, or put Dispatch behind an authenticated TLS-terminating reverse proxy. On untrusted networks, always use HTTPS: either `--tls-cert`/`--tls-key` or a proxy.
- Never expose `/notify` to the public internet without these controls.

**Message content**

- Content is treated as untrusted. It is sent as plain text, without `parse_mode`, so Telegram applies no formatting and the content cannot inject markup.
- Dispatch never runs commands or acts on anything in message content.

To report a vulnerability, open a private security advisory on GitHub.

---

## Production deployment

### AWS Secrets Manager

Store the credentials as one secret:

```sh
aws secretsmanager create-secret --name dispatch/telegram \
  --secret-string '{"DISPATCH_BOT_TOKEN":"123:AA...","DISPATCH_CHAT_ID":"-100...","DISPATCH_API_KEY":"dsp_..."}'
```

**EC2, cron, or scripts.** Give the instance role `secretsmanager:GetSecretValue` on that secret. Load the secret into the environment at runtime, and never write it to disk:

```sh
eval "$(aws secretsmanager get-secret-value --secret-id dispatch/telegram \
  --query SecretString --output text | jq -r 'to_entries[] | "export \(.key)=\(.value|@sh)"')"
dispatch send "Nightly job finished"
```

**ECS.** Reference the secret keys in the task definition. ECS injects them as environment variables:

```json
"secrets": [
  { "name": "DISPATCH_BOT_TOKEN", "valueFrom": "arn:aws:secretsmanager:REGION:ACCOUNT:secret:dispatch/telegram:DISPATCH_BOT_TOKEN::" },
  { "name": "DISPATCH_CHAT_ID",   "valueFrom": "arn:aws:secretsmanager:REGION:ACCOUNT:secret:dispatch/telegram:DISPATCH_CHAT_ID::" },
  { "name": "DISPATCH_API_KEY",   "valueFrom": "arn:aws:secretsmanager:REGION:ACCOUNT:secret:dispatch/telegram:DISPATCH_API_KEY::" }
]
```

Run `dispatch serve` as a sidecar. Containers in the same task share `127.0.0.1` on `awsvpc` networking, so the server can stay on loopback.

**Lambda.** Read the secret with the AWS Parameters and Secrets Lambda Extension or the SDK, and call the Telegram API through a bundled `dispatch` binary. Alternatively, call an internal `dispatch serve` over the VPC.

### systemd

```ini
# /etc/systemd/system/dispatch.service
[Unit]
Description=Dispatch notification API
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/dispatch serve --log-format json
EnvironmentFile=/etc/dispatch/env      # chmod 600, owned by root
DynamicUser=yes
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

Point your load balancer or orchestrator health checks at `/healthz` (liveness) and `/readyz` (readiness).

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `missing DISPATCH_BOT_TOKEN and DISPATCH_CHAT_ID` | Run `dispatch config init`, or export both variables. Check the result with `dispatch config show`. |
| `Telegram rejected the bot token` | Copy the token again from @BotFather. To issue a new token, use `/token`. |
| `Telegram chat not found` | The chat must message the bot first: press **Start** in a private chat, or add the bot to the group. Group IDs are negative, and supergroup IDs start with `-100`. |
| `the bot cannot message this chat` | The bot was blocked or removed from the group. Unblock it or add it back. |
| `group was upgraded to a supergroup` | Set `DISPATCH_CHAT_ID` to the new ID shown in the error. |
| `could not read messages … conflict` during `config init` | The bot has a webhook set. Enter the chat ID manually, or remove the webhook with `https://api.telegram.org/bot<TOKEN>/deleteWebhook`. |
| No chats detected during `config init` | Send the bot a new message, then press Enter again. In groups with privacy mode on, mention the bot or send a `/command`. |
| `Telegram rate limit exceeded` (exit 4 / HTTP 429) | Telegram limits bots to about 1 msg/s per chat and 20 msgs/min per group. Wait for `Retry-After`, or batch messages. |
| `timed out contacting Telegram` | Check outbound HTTPS access to `api.telegram.org`. The message may still have arrived. |
| `could not resolve the Telegram API host` | Check DNS. In restricted networks, set `HTTPS_PROXY`, which Dispatch respects. |
| `message is N characters … limit is 4096` | Shorten the message, split it into parts, or send it as a file with `--attach`. |
| `caption is N characters … limit with an attachment is 1024` | Shorten the caption, or send the long text as a separate message. |
| `DISPATCH_CHAT_ID belongs to a bot` | You used the bot's own ID, which `getMe` returns. Use your user ID instead (ask @userinfobot) or a group ID. |
| `refusing to listen on non-loopback address` | Add `--allow-remote` after reading [Network exposure](#security). |
| Config file warning about permissions | Run `chmod 600 "$(dispatch config path)"`. |

Use `-v` / `--verbose` to see retry attempts and debug logs. Secrets are still redacted.

---

## Development

```sh
make test       # unit and integration tests (no Telegram credentials needed)
make race       # tests with the race detector
make lint       # gofmt + go vet
make build      # bin/dispatch
make cross      # binaries for all release platforms + checksums in dist/
make snapshot   # local GoReleaser dry run
```

The tests run against an in-process fake of the Telegram Bot API (`internal/telegram/telegramtest`). To try Dispatch against the real API, run `dispatch test` with your own credentials. The standard suite never needs them.

**Layout**

```
cmd/dispatch/            entry point
internal/cli/            Cobra commands: send, test, serve, config, version
internal/notification/   provider-agnostic engine: validation, rendering, error codes
internal/telegram/       Bot API client (retries, redaction) + notification provider
internal/server/         HTTP API: handlers, auth, rate limiting, middleware
internal/config/         precedence, file storage, masking
```

To add a provider, implement `notification.Provider`. The CLI and HTTP API don't change.

**Releasing:** push a tag such as `v1.0.0`. The release workflow runs GoReleaser, which builds every platform and publishes archives, `checksums.txt`, and release notes to GitHub Releases.

## License

[MIT](LICENSE)
