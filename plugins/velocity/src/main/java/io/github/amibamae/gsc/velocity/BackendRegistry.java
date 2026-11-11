package io.github.amibamae.gsc.velocity;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.regex.Pattern;

/**
 * Backends the controller manages through the bridge API. A backend is
 * routable only while it is registered, not draining, and has passed a recent
 * health check. The registry never makes a backend routable on its own: only
 * the controller registers backends, and a failed health check only removes
 * routability.
 */
final class BackendRegistry {
  static final Pattern BACKEND_ID = Pattern.compile("^[a-z0-9][a-z0-9._-]{0,63}$");

  enum Registration {
    REGISTERED("registered"),
    DRAINING("draining"),
    UNREGISTERED("unregistered");

    final String wire;

    Registration(String wire) {
      this.wire = wire;
    }
  }

  /** Protocol external resource reference for the backend's instance. */
  record InstanceRef(String adapterId, String environmentId, String resourceKind, String externalId) {
    InstanceRef {
      for (String value : new String[] {adapterId, environmentId, resourceKind, externalId}) {
        if (value == null || value.isEmpty() || value.length() > 1024) {
          throw new IllegalArgumentException("instance reference fields are required");
        }
      }
    }
  }

  /** Immutable view of one backend. */
  record Backend(
      String id,
      Registration registration,
      InstanceRef instance,
      BackendAddress address,
      boolean healthy,
      Instant lastHealthy) {}

  private final Set<String> staticBackends;
  private final Clock clock;
  private final Duration healthWindow;
  private final Map<String, Backend> backends = new LinkedHashMap<>();

  /**
   * @param staticBackends server names configured statically in Velocity,
   *     which the bridge refuses to change
   * @param healthWindow how long a successful health check keeps a backend
   *     routable
   */
  BackendRegistry(Set<String> staticBackends, Duration healthWindow, Clock clock) {
    this.staticBackends = Set.copyOf(staticBackends);
    this.healthWindow = healthWindow;
    this.clock = clock;
  }

  static boolean validId(String id) {
    return id != null && BACKEND_ID.matcher(id).matches();
  }

  boolean isStatic(String id) {
    return staticBackends.contains(id);
  }

  synchronized Optional<Backend> get(String id) {
    return Optional.ofNullable(backends.get(id));
  }

  synchronized boolean isManaged(String id) {
    return backends.containsKey(id);
  }

  synchronized List<Backend> all() {
    return new ArrayList<>(backends.values());
  }

  /** Registers or replaces a backend. Health starts unknown. */
  synchronized Backend register(String id, InstanceRef instance, BackendAddress address) {
    requireManageable(id);
    Backend backend = new Backend(id, Registration.REGISTERED, instance, address, false, null);
    backends.put(id, backend);
    return backend;
  }

  /** Restores a persisted registration without trusting its old health. */
  synchronized void restore(Backend saved) {
    requireManageable(saved.id());
    backends.put(saved.id(), new Backend(saved.id(), saved.registration(), saved.instance(), saved.address(), false, null));
  }

  synchronized Optional<Backend> drain(String id) {
    requireManageable(id);
    Backend current = backends.get(id);
    if (current == null) {
      return Optional.empty();
    }
    Backend drained = new Backend(id, Registration.DRAINING, current.instance(), current.address(), current.healthy(), current.lastHealthy());
    backends.put(id, drained);
    return Optional.of(drained);
  }

  synchronized Optional<Backend> unregister(String id) {
    requireManageable(id);
    return Optional.ofNullable(backends.remove(id));
  }

  /**
   * Records a health-check result. The check is ignored if the backend was
   * replaced with a different address since the check started.
   */
  synchronized void recordHealth(String id, BackendAddress checked, boolean ok) {
    Backend current = backends.get(id);
    if (current == null || !current.address().equals(checked)) {
      return;
    }
    Instant now = clock.instant();
    backends.put(id, new Backend(id, current.registration(), current.instance(), current.address(), ok, ok ? now : current.lastHealthy()));
  }

  /** Marks a backend unhealthy immediately, for example after it kicked players. */
  synchronized void markUnhealthy(String id) {
    Backend current = backends.get(id);
    if (current != null) {
      backends.put(id, new Backend(id, current.registration(), current.instance(), current.address(), false, current.lastHealthy()));
    }
  }

  synchronized boolean routable(String id) {
    Backend backend = backends.get(id);
    return backend != null && routable(backend);
  }

  boolean routable(Backend backend) {
    return backend.registration() == Registration.REGISTERED
        && backend.healthy()
        && backend.lastHealthy() != null
        && !backend.lastHealthy().plus(healthWindow).isBefore(clock.instant());
  }

  private void requireManageable(String id) {
    if (!validId(id)) {
      throw new BridgeException(400, "invalid_backend_id", "Backend ID is not a valid server name.");
    }
    if (isStatic(id)) {
      throw new BridgeException(409, "backend_not_managed", "Backend is configured statically.");
    }
  }
}
