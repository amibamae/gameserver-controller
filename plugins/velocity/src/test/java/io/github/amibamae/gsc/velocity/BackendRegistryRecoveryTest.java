package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.amibamae.gsc.velocity.BackendRegistry.InstanceRef;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.Set;
import org.junit.jupiter.api.Test;

/** Staging invariants for routing during game recovery on an existing VM. */
class BackendRegistryRecoveryTest {
  private static final Clock CLOCK = Clock.fixed(Instant.parse("2026-11-23T19:00:00Z"), java.time.ZoneOffset.UTC);
  private static final InstanceRef VM = new InstanceRef("hosting.vultr", "staging", "instance", "same-vm");
  private static final BackendAddress ADDRESS = BackendAddress.parse("203.0.113.20:25566");

  private static BackendRegistry registry() {
    return new BackendRegistry(Set.of("lobby"), Duration.ofSeconds(15), CLOCK);
  }

  @Test
  void recoveringPaperCannotBeRoutedJustBecauseThePortComesBack() {
    BackendRegistry backends = registry();
    backends.register("vanilla_no_grief", VM, ADDRESS);
    backends.recordHealth("vanilla_no_grief", ADDRESS, true);
    assertTrue(backends.routable("vanilla_no_grief"));

    // Controller drains before beginning the durable save-and-restart operation.
    backends.drain("vanilla_no_grief");
    backends.recordHealth("vanilla_no_grief", ADDRESS, false);
    backends.recordHealth("vanilla_no_grief", ADDRESS, true);
    assertFalse(backends.routable("vanilla_no_grief"));

    // The controller re-registers only after a different runtime's fresh
    // Paper Done and all required readiness checks have been verified.
    backends.register("vanilla_no_grief", VM, ADDRESS);
    assertFalse(backends.routable("vanilla_no_grief"));
    backends.recordHealth("vanilla_no_grief", ADDRESS, true);
    assertTrue(backends.routable("vanilla_no_grief"));
  }

  @Test
  void proxyRestartDoesNotClearARecoveryDrain() {
    BackendRegistry backends = registry();
    backends.register("vanilla_no_grief", VM, ADDRESS);
    backends.drain("vanilla_no_grief");
    BackendRegistry.Backend persisted = backends.get("vanilla_no_grief").orElseThrow();

    BackendRegistry restored = registry();
    restored.restore(persisted);
    restored.recordHealth("vanilla_no_grief", ADDRESS, true);
    assertFalse(restored.routable("vanilla_no_grief"));
    assertEquals(BackendRegistry.Registration.DRAINING,
        restored.get("vanilla_no_grief").orElseThrow().registration());
  }
}
