# Optional direct-join wake flow

This is a future option for games that do not use a proxy. It does not change
the Velocity path. It assumes clients can connect directly to a game server
using one join hostname and that the controller owns the relevant Cloudflare
DNS records.

## Player flow

1. While the game server is stopped, the join hostname resolves to an
   always-on lobby.
2. A player joins the lobby. The lobby kicks them with a message giving the
   estimated wait time and telling them to rejoin once that time has passed.
   The lobby then sends an authenticated, rate-limited wake request to the
   controller API. It never receives Vultr or Wasabi keys.
3. The controller deduplicates requests, checks the environment and VM cap,
   then starts or provisions the controller-owned server.
4. If the player reconnects before readiness, the lobby reads the operation
   status, repeats the wait-time estimate, and disconnects them again.
   It does not hold a player session open while the VM boots.
5. The controller waits for provisioning, world restore, process startup, and
   game-specific readiness checks to pass. A successful VM create call does
   not mean the game is ready.
6. Once the game is ready, the controller changes the managed join record to
   the exact game endpoint. The player reconnects; a fresh DNS lookup can
   resolve to the game server. A player whose resolver still has the lobby
   address cached may land at the lobby once more and need to retry.

> [!NOTE]
> To calculate a wait-time estimate from observed boot durations, the server
> owner must opt in to sharing boot-time telemetry with the lobby/DNS service.
> Without opt-in or enough samples, the lobby uses a conservative configured
> wait.

## DNS cutover and TTL

Use Cloudflare DNS-only records for this direct-join hostname. Set the TTL to
60 seconds on the A/AAAA record and any SRV or CNAME records used in the join
lookup. Set this before enabling DNS handoff and keep it at 60 seconds. On
Cloudflare's standard plans, 60 seconds is the lowest TTL for DNS-only
records; proxied records use an automatic 300-second TTL and do not meet this
target. See [Cloudflare's TTL documentation](https://developers.cloudflare.com/dns/manage-dns-records/reference/ttl/).

Keep the hostname pointed at the lobby while the VM provisions, boots, restores
the world, and passes readiness checks. Do not point it at the VM as soon as
the VM is created: an early reconnect would bypass the lobby and could reach a
game server that is not ready yet.

After readiness, update the DNS record to the game endpoint. Resolvers that
cached the lobby address can keep using it until that cached 60-second TTL
expires. Keep the lobby available during this transition. If a player reaches
it after the controller reports readiness, tell them the game is ready and
ask them to retry shortly. Lowering the TTL during cutover does not shorten
the lifetime of answers that resolvers already cached. Some resolvers can
serve stale data when they cannot refresh an expired answer, so the TTL is not
an absolute guarantee. See [RFC 1035](https://www.rfc-editor.org/rfc/rfc1035.html)
and [RFC 8767](https://www.rfc-editor.org/rfc/rfc8767.html).

The player must reconnect; DNS does not move an existing connection. The
rejoin estimate should include a conservative readiness buffer and allow for
up to one 60-second DNS cache window after readiness. The lobby must still
handle a cache-delayed retry after the controller marks the server ready.

## Local boot timing and optional shared telemetry

For each controller-owned VM, record the local duration from confirmed
provisioning to Paper's required readiness checks passing. Use prior durations
for that server/profile to show an estimated wait and an advisory progress bar
while the backend boots. The display must remain below 100% until Paper is
actually ready; timing estimates never permit DNS changes or player routing.
When there are too few measurements, show a conservative configured estimate
and make progress indeterminate rather than inventing precision.

Local boot timing supports the owner's estimate and progress display. Sharing
averaged boot durations with a hosted lobby/DNS service is separate optional
telemetry: it starts disabled, and the server owner must explicitly opt in to
that sharing. The service may use the shared average only with an additional
safety buffer and a separate allowance for the 60-second DNS cache window.
Without opt-in or enough shared samples, use a conservative fallback. Install,
upgrade, and migration must not enable sharing silently.

Shared telemetry contains only boot-duration measurements and the minimum
server-profile/release grouping needed for a useful average. It must not include
player names, UUIDs, IP addresses, connection history, gameplay activity, or
credentials. Explain its fields, purpose, destination, retention, access, and
withdrawal path as required by [the telemetry policy](../SECURITY.md#optional-telemetry).
Wake-up, readiness, DNS handoff, and the local progress display must continue
when sharing is disabled.

## Safety and DNS ownership

- Keep the join hostname pointed at the lobby until the game is actually ready;
  a successful VM create call is not readiness.
- Bind the controller operation to one approved DNS zone and exact record name.
  Customer requests must not be able to choose arbitrary zones or records.
- Update and verify A/AAAA records for the precise endpoint. Use SRV records
  only when the game and client require them. Preserve unrelated records.
- Before stopping or destroying a direct-join server, point the join hostname
  back to the lobby, allow the 60-second DNS TTL transition, and drain or
  disconnect players under the game's lifecycle policy.
- Direct DNS exposes the game VM's address and does not provide proxy-layer
  filtering, routing, or connection transfer. Keep management, SSH, RCON, and
  other administrative ports private; expose only required game ports.
- Public wake requests can create provider cost. Apply per-player cooldowns,
  global rate limits, one-operation deduplication, per-tenant VM quotas, and
  operator-configured spending limits before starting a VM.
- DNS API credentials are secrets. Scope them to the managed zone and record
  operations where supported, store them under
  [the provider credential policy](SECRET_MANAGEMENT.md), and never expose
  them to the lobby or player application.

The future implementation needs a DNS integration contract for record inspect,
update, and reconciliation; an authenticated wake API; local provision-to-Paper
readiness timing and advisory progress; and separately owner-enabled shared
boot-duration averages with bounded retention. Tests must cover duplicate
requests, boot failure, readiness failure, sharing opt-in/opt-out, DNS update
uncertainty, TTL delay, shutdown handoff, and credential isolation. This flow
is not implemented by the current pilot or controller.
