# Proxy outage notifications

The controller watches each Velocity proxy bridge it runs. When a proxy stops answering, or answers with the wrong identity, the controller tells operators, and it tells them again when the proxy is back. The rules are in [integrations section 7](../protocol/adapters/integrations.md#7-monitoring-and-notifications); this page is the operator guide.

## What happens

1. The controller checks the bridge's identity (`GET /v1/info`) every `check_interval` (default 1 minute). A check changes nothing on the proxy.
2. A failed check starts a streak. After `failure_threshold` consecutive failures (default 2), the controller declares an outage and sends `integration.proxy.unreachable`. If the proxy answers but reports a different instance or API version, it sends `integration.proxy.identity_mismatch`. A failed check at startup counts as the first failure.
3. While the proxy is failing, the checks back off: 30 seconds, then 1, 2 and 4 minutes, then every 5 minutes (configurable).
4. The first good check after an outage sends `integration.proxy.recovered`. A single dropped check that recovers on the next try sends nothing.
5. After any failed check, the controller verifies the proxy identity again before its next proxy call. Calls fail without acting until the check passes.

Every event is written to the controller log (journald) as one JSON line. Notifier adapters send it on to Discord, Slack, email, ntfy, or any HTTPS endpoint. Configure them in [configuration](CONFIGURATION.md#proxy-monitoring-and-notifications).

## What a notification contains

Exactly these fields, and nothing else:

| Field | Example |
|---|---|
| Event type and severity | `integration.proxy.unreachable`, `critical` |
| Environment | `production` |
| Proxy adapter and instance | `integration.velocity`, `velocity-edge-001` |
| Outage start and event time | `2026-11-12T18:00:30Z` |
| Failure count | `2` |
| Event ID | `evt-6f0c…` (stable across retries) |
| Summary | "Proxy integration.velocity (velocity-edge-001, production) is unreachable since 2026-11-12T18:00:30Z." |

Notifications never contain player names, account IDs, session IDs, IP addresses, chat, raw error messages, URLs, or credentials. The event schema allows no other fields.

## Where it goes and who keeps it

Each destination stores what it receives under its own retention policy, which the controller does not control. Choose destinations you are comfortable sharing these operational details with.

- **Discord or Slack** (`implementation: webhook`, `format: discord` or `slack`): sent to the service's incoming webhook. Discord mentions are disabled, so a message cannot ping `@everyone`.
- **Any HTTPS endpoint** (`format: json`): the full event as JSON, with `Idempotency-Key` set to the event ID.
- **Email** (`implementation: smtp`): a plain-text message through your SMTP submission server. It uses STARTTLS or implicit TLS and always authenticates. `Message-ID` is derived from the event ID.
- **ntfy** (`implementation: ntfy`): a push message to your topic, authenticated with an access token. Critical events use priority 5.

## Set up a destination

Secrets go in systemd credentials, never in the configuration file. Each notifier's `secret_refs` entry names one credential, which `controller-setup` loads into the service unit.

- **Discord:** Server Settings → Integrations → Webhooks → New Webhook, then copy the URL. Store the whole URL as the credential (`webhook_url`); anyone who has it can post to the channel.
- **Slack:** create an app with Incoming Webhooks enabled, add a webhook to a channel, and store its URL as `webhook_url`.
- **Email:** use a submission account meant for alerts. Store its password (or app password) as `password`.
- **ntfy:** create an access token with write access to the topic, and store it as `access_token`. On a public server such as ntfy.sh, also reserve the topic so others cannot read it.

## Delivery

Delivery is at least once, so you may occasionally get a duplicate; the event ID is the same each time.

- A destination that accepts the message: done.
- A destination that refuses it (for example a 4xx response or a wrong password): not retried. The controller log records it.
- A timeout, connection failure, rate limit, or 5xx response: retried after 30 seconds, then 1, 2 and 4 minutes, for at most 5 attempts.

Delivery state is kept in memory. If the controller restarts, undelivered notifications are dropped. If a proxy is still down after a restart, the outage is reported again once the threshold is reached.
