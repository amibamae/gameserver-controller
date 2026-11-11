# GSC Velocity bridge plugin

A Velocity plugin that lets the gameserver controller manage backend
servers and move players. It serves the
[proxy bridge API](../../docs/PROXY_BRIDGE_API.md) on a loopback port, which
the controller's Velocity integration adapter (`internal/velocityproxy`)
calls. Other tools can use the same API.

**Status:** Draft. Unit-tested with a fake proxy, and its HTTP API has been
exercised by the Go client. It has not yet run on a live Velocity proxy.

## What it does

- **Backend registration.** The controller registers backends at the
  instance's address (an IP, or a managed hostname that DNS keeps pointed at
  the instance). Servers listed in `velocity.toml` are never changed.
- **Health checks.** A registered backend is routable only after a recent
  successful ping. A failed ping or a crash-kick removes routability; only
  the controller can register a backend.
- **Join routing.** New players go to the first routable backend in
  `join-backends`, or in the matching `forced-hosts` list for the hostname
  they joined through. When none is routable, they wait on the `fallback`
  server (normally the lobby) in the waiting cohort `waiting-<backend>`.
- **Fallback on failure.** If a managed backend goes down, its players are
  sent to the fallback and put in that backend's waiting cohort instead of
  being disconnected.
- **Moving waiting players.** Once a backend is ready, the controller reads
  the waiting cohort and transfers those players. The plugin does not move
  them on its own.
- **Privacy.** Session IDs are random per connection. The API never returns
  player names, UUIDs, or IP addresses, and the plugin does not log session
  IDs.

Registrations are saved to `backends.json` in the plugin data directory and
restored on restart. Restored backends become routable again only after a
health check passes.

## Install

1. Build: `gradle build` (Java 21 toolchain, Gradle 8.x). The jar is in
   `build/libs/`. It targets Java 17 bytecode and the Velocity 3.4 API.
   Velocity 4.x requires Java 25; the API calls the plugin uses have the same
   signatures in 4.3.1, but it has not been built or run against 4.x.
2. Copy the jar into Velocity's `plugins/` directory and start the proxy
   once. The plugin writes `plugins/gsc-velocity-bridge/config.toml`.
3. Create the token file named by `bridge.token-file`, readable only by the
   Velocity service user (at least 16 characters), for example:

   ```bash
   head -c 32 /dev/urandom | base64 > /etc/gsc/velocity-bridge.token
   ```

   Give the controller the same token through its systemd credential.
4. Set `bridge.proxy-instance-id` to the controller's proxy instance ID,
   list the controller-managed backends in `routing.join-backends`, and set
   `routing.fallback` to a server from `velocity.toml`. Restart the proxy.

The bridge refuses to start if the token file is readable by other users,
if `bind` is not a loopback address, or if the fallback is not a
`velocity.toml` server.
