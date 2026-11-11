# Cloudflare DNS adapter

**Status:** Initial implementation in `internal/cloudflaredns`, tested
against a fake Cloudflare API. It is not wired into the controller runtime
and has not been run against a live Cloudflare zone.

The adapter (`integration.cloudflare-dns`) implements the protocol's DNS
record capabilities, `integration.dns.record.inspect`, `.upsert`, and
`.delete`, defined in
[the integration contract](../protocol/adapters/integrations.md#5-dns-records).
It is an integration adapter: it points names at servers but does not create
or own them. A server needs it when its
[network profile](CONFIGURATION.md#network-profile) uses `direct_dns` or
hostname backends.

## What it manages

One adapter instance covers one Cloudflare zone in one environment. Its
configuration lists every record name it may change, and for each name the
purposes allowed:

| Purpose | Use | Extra rule |
|---|---|---|
| `backend_address` | A per-backend hostname the proxy uses to reach the instance | None |
| `direct_join` | The hostname players join through directly (games without a proxy) | A current readiness assertion for the exact instance |
| `fallback` | Pointing a join hostname back at the lobby | Values must equal the configured fallback values |

Any other name in the zone is refused before Cloudflare is called. Records
are DNS-only (`proxied: false`); the adapter refuses to touch a name that has
a proxied record. TTLs below 60 seconds are rejected, because that is the
lowest TTL Cloudflare allows for DNS-only records outside Enterprise plans.
A protocol record with several values is stored as several Cloudflare
records with the same name and type.

## How a change works

1. Check the request: zone, environment, managed name, purpose, TTL, and,
   for `direct_join`, the readiness assertion. A failure is reported as
   `rejected` and nothing is sent to Cloudflare.
2. Read the current records. If `expected_values` is set and does not match,
   reject with `current_values_mismatch`.
3. Create missing values first, then delete old ones and fix TTLs, so the name
   does not resolve to nothing in between.
4. Read the records back. Only an exact match of the requested values and TTL
   is `confirmed`; otherwise the result is `accepted`.

A Cloudflare refusal (4xx) before anything changed is reported as `rejected`.
A 5xx reply, a timeout, or a refusal after an earlier step succeeded is
`outcome_unknown`: inspect the record before retrying.

## Credentials

The adapter reads a Cloudflare API token from a systemd credential
(`APITokenRef`). The invocation's request context must list that reference,
as with the Wasabi adapter. Scope the token to **Zone → DNS → Edit** for the
one zone only. The token is never logged or included in results or errors.
See [secret management](SECRET_MANAGEMENT.md).
