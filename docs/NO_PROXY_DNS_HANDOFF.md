# Optional direct-join wake flow

This is a future option for games that do not use a proxy. It does not change
the Velocity path. It assumes clients can connect directly to a game server
using one join hostname and that the controller owns the relevant DNS record.

## Player flow

1. While the game server is stopped, the join hostname resolves to an
   always-on lobby.
2. A player joins the lobby. The lobby sends an authenticated, rate-limited
   wake request to the controller API. It never receives Vultr or Wasabi keys.
3. The controller deduplicates requests, checks the environment and VM cap,
   then starts or provisions the controller-owned server. The lobby displays
   an estimated wait and disconnects the player with that reason.
4. If the player reconnects before readiness, the lobby reads the operation
   status, repeats the estimate, and disconnects them again. It does not hold
   a player session open while the VM boots.
5. After the game passes provider, process, and game-specific readiness checks,
   the controller changes the managed join record to the exact game endpoint.
   The player reconnects; a fresh DNS lookup directs that connection to the
   game server.

DNS changes do not move an existing connection from the lobby to the game.
The player must reconnect. DNS resolvers can retain the old record until its
TTL expires, and some resolvers may serve stale data during DNS outages, so
record changes are not instantaneous. Use the lowest safe TTL supported by the
DNS provider, keep the lobby available during the transition, and report that
a reconnect may take longer than the boot ETA. See [RFC 1035](https://www.rfc-editor.org/rfc/rfc1035.html)
and [RFC 8767](https://www.rfc-editor.org/rfc/rfc8767.html).

## Safety and DNS ownership

- Keep the join hostname pointed at the lobby until the game is actually ready;
  a successful VM create call is not readiness.
- Bind the controller operation to one approved DNS zone and exact record name.
  Customer requests must not be able to choose arbitrary zones or records.
- Update and verify A/AAAA records for the precise endpoint. Use SRV records
  only when the game and client require them. Preserve unrelated records.
- Before stopping or destroying a direct-join server, point the join hostname
  back to the lobby, allow the DNS TTL transition, and drain or disconnect
  players under the game's lifecycle policy.
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
update, and reconciliation; an authenticated wake API; and operation tests for
duplicate requests, boot failure, readiness failure, DNS update uncertainty,
TTL delay, shutdown handoff, and credential isolation. This flow is not
implemented by the current pilot or controller.
