# Optional VictoriaLogs and Homelable observability

**Status: opt-in integration tools and SSH configuration are implemented; no production deployment is performed.** See [the follow-up lifecycle guide](OBSERVABILITY_LIFECYCLE.md) for optional automatic handoff setup, a durable controller collector, managed-only Homelable updates and staging recovery tests. **Both integrations remain disabled by default.** These tools do not control world heads, ownership, backups, destructive actions, or player routing. Logs and topology availability never gate normal Paper/Velocity gameplay.

## Responsibilities and boundaries

| Source | Collection | Destination |
| --- | --- | --- |
| Go controller proxy-outage events | `scripts/observability/victorialogs_bridge.py` filters the controller's structured journald JSON lines to an allowlist | Persistent VictoriaLogs |
| Disposable Vultr Paper/restore/bootstrap lifecycle | Debian bootstrap installs `gsc-victorialogs` systemd units **disabled**. After the pinned SSH handoff, `gsc-pilot observability` securely configures the destination/token/privacy | Same persistent VictoriaLogs |
| Inventory of persistent controller, Velocity proxy, lobby and optional backend | `scripts/observability/homelable_sync.py` registers explicitly named nodes only | Operator-managed Homelable |

The Velocity bridge API exposes proxy identity, managed backends and session transfers; it **does not** export backend VM systemd or Paper logs. A separate temporary-VM log shipper is therefore necessary for VM lifecycle diagnostics.

No automatic provisioning/destroy/rollback policy may treat VictoriaLogs or Homelable as an authority. Historical status can be stale when a backend is deleted or a network partition occurs.

## VictoriaLogs deployment prerequisite

Host VictoriaLogs on **persistent** secured infrastructure. Keep its native port private; use an HTTPS ingestion gateway or reverse proxy that actually enforces the dedicated ingestion bearer token. VictoriaLogs' HTTP insertion API is not, by itself, a replacement for authentication and network restrictions. Use exactly the `/insert/jsonline` path; do not put tokens in the URL, Docker Compose files, cloud-init, command lines, or public logs.

The Go controller already emits allowlisted `integration.proxy.*` events as structured JSON to journald. From a trusted local host, the optional filter can forward *only* those event types:

~~~sh
journalctl --unit=gameserver-controller.service --output=cat --no-pager --since="1 hour ago" \
  | python3 scripts/observability/victorialogs_bridge.py \
      --endpoint https://logs.example.invalid/insert/jsonline \
      --token-file /path/to/private/ingest-token
~~~

Use the actual controller service name for your installation, and keep the token file private. For regular collection, run this from an operator-controlled systemd timer/journal reader with an appropriately persisted cursor or a trusted collector. This example is a **one-shot historical export**, not a durable production pipeline. The filter deliberately discards all non-allowlisted JSON, arbitrary log lines, summaries, credentials, chat, account IDs and IPs. It does not attempt to be a complete audit-log archiver.

## Disposable Vultr VM bootstrap

`gsc-bootstrap` installs Python 3, the bounded `victorialogs_export.py` journald shipper, the restricted `gsc-configure-victorialogs` root helper, and a 30-second systemd timer. **The timer is never enabled in bootstrap.** No endpoint, secret, player-telemetry permission or external logging connection is embedded in the Vultr startup script or ordinary VM user-data.

The shipper reads only tagged bootstrap lifecycle and Paper/restore unit messages. With the supported `lifecycle` policy, it forwards **only categorical signals** such as `bootstrap_complete`, `paper_ready`, `paper_warning` and `restore_failed`; it does not send the underlying raw text. It uses bounded ingestion batches, a durable journal cursor, HTTPS with no redirects and a short request timeout. If ingestion fails, it keeps the cursor for retry; game services continue unaffected. A long outage can exceed the VM's journal retention, especially when the VM is destroyed.

### Secure provisioning over pinned SSH

After `gsc-pilot handoff` finishes rotating and pinning the VM's SSH host key and finalizing its firewall, set the VM's log policy through the **controller-side** command:

~~~sh
# Run on the controller; credentials set interactively under encrypted systemd storage.
controller-setup credentials set --pilot --name gsc-victorialogs-ingest-token

# Preview: reads the existing managed VM identity and pinned host-key record.
gsc-pilot observability \
  --operation-id <exact-32-character-provision-operation-id> \
  --logs-privacy lifecycle \
  --logs-endpoint https://logs.example.invalid/insert/jsonline

# Apply only after reviewing the exact target printed by the preview.
gsc-pilot observability \
  --operation-id <exact-32-character-provision-operation-id> \
  --logs-privacy lifecycle \
  --logs-endpoint https://logs.example.invalid/insert/jsonline \
  --execute --confirm-observability <server-id>@<environment>/<instance-id>
~~~

The command uses the existing encrypted `gsc-pilot-ssh-key`, dedicated encrypted `gsc-victorialogs-ingest-token` and Vultr lookup credential from `$CREDENTIALS_DIRECTORY`. Its provider lookup must match the exact instance/reservation; OpenSSH verifies the **saved pinned host key**. The token and privacy policy are delivered over SSH **stdin** to the fixed, restricted root command, which installs root-owned mode-0600 configuration files and then enables the timer. Secrets never enter the shell command line, audit logs or startup-script template. The root installer permits **`lifecycle` or `disabled`** only. Raw player/chat log forwarding is deliberately unavailable because per-player consent cannot currently be proven.

To disable forwarding, use the same operation ID with `--logs-privacy disabled`, preview it, then apply with the same exact confirmation. This stops the timer and removes the VM's credential and endpoint files. This does **not** delete already ingested historical data; remove that separately under the destination's retention and privacy policies.

At the end of a VM lifecycle, perform a final successful exporter run **before any separately authorized manual provider destruction** when logs matter. An abrupt VM loss can prevent the final journal flush. This feature does not change the current rule that automatic production destruction is disabled.

## Homelable integration

Homelable exposes an authenticated `/api/v1/nodes` REST API. The included script is a **non-destructive topology registrar**, not automatic discovery, secret storage, or an authoritative live-inventory service.

Create an operator-reviewed copy of [the sample inventory](../configs/homelable.inventory.example.json) and put your actual existing Homelable **design ID** in it. Obtain a short-lived JWT through Homelable's normal authenticated login and write it to a private token file. Use HTTPS on the private network or HTTP to loopback only.

~~~sh
# Does not require a token or contact the server.
python3 scripts/observability/homelable_sync.py \
  --url https://homelable.example.invalid \
  --inventory /path/to/approved-inventory.json

# Explicit opt-in: creates missing "GSC / ..." nodes only.
python3 scripts/observability/homelable_sync.py \
  --url https://homelable.example.invalid \
  --inventory /path/to/approved-inventory.json \
  --token-file /path/to/private/homelable-jwt --apply
~~~

Existing nodes are left untouched. The script never runs `PATCH`, `DELETE`, `canvas/save`, or a scanner. The destination requires a bearer JWT. In Homelable's local login mode, JWTs expire (default 24 hours); its OIDC mode does not currently offer the same scripting login flow. Do not reuse the non-expiring, full-power `X-MCP-Service-Key` as a routine integration credential. Keep the Homelable UI/API off the public internet.

## Security, performance and implementation limits

- Opt-in only, with sanitized lifecycle categories as the sole supported VM privacy mode. Player chat, usernames, IPs and raw errors are not exported by these new tools.
- Dedicated ingestion token, root-owned VM configuration, authenticated pinned SSH, narrow sudo command and short-lived network requests; existing Wasabi/Vultr credentials are never sent to VictoriaLogs or Homelable.
- VM log exporter has 64 MiB systemd memory cap and 20% CPU quota. It collects in limited batches and will not block starting or stopping Minecraft if the external collector is unavailable.
- No network call or VM creation is made merely by adding this repository code; deployment is a separate operator action.
- Homelable inventory is manually curated and its nodes are **not** automatically reconciled when disposable VMs disappear. Automating that requires later policy and integration review.
- Neither tool solves eleven-nines durability, independently verified world persistence, controller HA leadership, or the current backlog of game/hosting lifecycle workflows.
- The scripts use the Python standard library; VictoriaLogs and Homelable binaries are **not bundled**. If a future deployment bundles them, inspect their current licenses/notice obligations and update `THIRD_PARTY_NOTICES.md` accordingly.

## Tests

Offline tests under `scripts/observability/test_observability.py` cover safe event filtering, rejection of malformed ingestion endpoints, lifecycle-only VM privacy, safe stdin configuration validation, and Homelable dry run/non-destructive node creation. Go tests under `internal/pilothandoff/`, `scripts/bootstrap/`, and `cmd/supervised-pilot/` cover early validation, disabled bootstrap timer/secret-free startup, CLI policy, and audit identity checks.

Before production use, test with **disposable staging** instances and synthetic messages: host-key mismatch, missing token, expired ingestion token, invalid endpoint, network partition, partial journal batch, and a deleted VM. Validate gateway authentication and host firewall boundaries independently; tests must not contact the live production server.

Documentation: [VictoriaLogs ingestion](https://docs.victoriametrics.com/victorialogs/data-ingestion/), [Homelable REST API](https://github.com/Pouzor/homelable/blob/main/docs/api.md).
