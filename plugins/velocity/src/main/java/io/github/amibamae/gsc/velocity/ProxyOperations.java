package io.github.amibamae.gsc.velocity;

import java.time.Duration;
import java.util.UUID;

/**
 * The proxy actions the bridge needs. The Velocity implementation is
 * {@link VelocityProxyOperations}; tests use a fake. Keeping this small also
 * makes the bridge easier to port to another proxy.
 */
interface ProxyOperations {
  /** Transfer results reported by the proxy. */
  enum TransferResult {
    TRANSFERRED,
    /** The proxy tried and the player stayed where they were. */
    FAILED,
    /** The proxy did not answer in time; the player may or may not have moved. */
    UNKNOWN
  }

  /** Adds or replaces the proxy's server entry for a managed backend. */
  void applyRegistration(String backendId, BackendAddress address);

  /** Removes the proxy's server entry for a managed backend. */
  void removeRegistration(String backendId);

  /** Checks whether the backend answers at this address. */
  boolean probe(String backendId, BackendAddress address, Duration timeout);

  /** Number of players currently connected to the backend. */
  int connectedPlayers(String backendId);

  /** Moves one connected player to a backend and waits for the result. */
  TransferResult transfer(UUID player, String destinationBackendId, Duration timeout);

  String proxyVersion();
}
