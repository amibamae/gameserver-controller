package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.amibamae.gsc.velocity.BackendRegistry.InstanceRef;
import java.time.Duration;
import java.time.Instant;
import java.util.Set;
import org.junit.jupiter.api.Test;

class BackendRegistryTest {
  static final InstanceRef INSTANCE = new InstanceRef("hosting.vultr", "test", "instance", "instance-test-001");
  final MutableClock clock = new MutableClock(Instant.parse("2026-11-07T10:00:00Z"));
  final BackendRegistry registry = new BackendRegistry(Set.of("lobby"), Duration.ofSeconds(15), clock);
  final BackendAddress address = BackendAddress.parse("203.0.113.20:25565");

  @Test
  void registeredBackendIsRoutableOnlyAfterHealthCheck() {
    registry.register("paper-main", INSTANCE, address);
    assertFalse(registry.routable("paper-main"));
    registry.recordHealth("paper-main", address, true);
    assertTrue(registry.routable("paper-main"));
    clock.advance(Duration.ofSeconds(16));
    assertFalse(registry.routable("paper-main"), "an old health check must not keep a backend routable");
  }

  @Test
  void failedHealthCheckOrKickRemovesRoutability() {
    registry.register("paper-main", INSTANCE, address);
    registry.recordHealth("paper-main", address, true);
    registry.recordHealth("paper-main", address, false);
    assertFalse(registry.routable("paper-main"));
    registry.recordHealth("paper-main", address, true);
    registry.markUnhealthy("paper-main");
    assertFalse(registry.routable("paper-main"));
  }

  @Test
  void drainingBackendIsNeverRoutable() {
    registry.register("paper-main", INSTANCE, address);
    registry.recordHealth("paper-main", address, true);
    registry.drain("paper-main");
    registry.recordHealth("paper-main", address, true);
    assertFalse(registry.routable("paper-main"));
    assertEquals(BackendRegistry.Registration.DRAINING, registry.get("paper-main").orElseThrow().registration());
  }

  @Test
  void healthCheckForAReplacedAddressIsIgnored() {
    registry.register("paper-main", INSTANCE, address);
    BackendAddress moved = BackendAddress.parse("203.0.113.99:25565");
    registry.register("paper-main", INSTANCE, moved);
    registry.recordHealth("paper-main", address, true);
    assertFalse(registry.routable("paper-main"));
  }

  @Test
  void refusesStaticAndInvalidBackends() {
    BridgeException error = assertThrows(BridgeException.class, () -> registry.register("lobby", INSTANCE, address));
    assertEquals(409, error.status);
    assertEquals("backend_not_managed", error.code);
    assertThrows(BridgeException.class, () -> registry.register("../x", INSTANCE, address));
    assertThrows(BridgeException.class, () -> registry.unregister("lobby"));
  }

  @Test
  void restoredBackendsStartUnhealthy() {
    registry.restore(new BackendRegistry.Backend("paper-main", BackendRegistry.Registration.REGISTERED, INSTANCE, address, true, clock.instant()));
    assertFalse(registry.routable("paper-main"));
  }
}
