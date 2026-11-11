package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertNotEquals;
import static org.junit.jupiter.api.Assertions.assertNull;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.UUID;
import org.junit.jupiter.api.Test;

class SessionRegistryTest {
  final MutableClock clock = new MutableClock(Instant.parse("2026-11-07T10:00:00Z"));
  final SessionRegistry sessions = new SessionRegistry(clock);
  final UUID player = UUID.fromString("00000000-0000-4000-8000-000000000001");

  @Test
  void sessionIdsAreRandomAndNotDerivedFromThePlayer() {
    String first = sessions.connect(player);
    sessions.disconnect(player);
    String second = sessions.connect(player);
    assertNotEquals(first, second);
    assertTrue(first.matches("[0-9a-f]{32}"));
    assertFalse(first.contains(player.toString().replace("-", "")));
  }

  @Test
  void cohortTracksWaitingPlayersUntilTheyReachABackend() {
    String id = sessions.connect(player);
    sessions.setCohort(player, "waiting-paper-main");
    sessions.connectedTo(player, "lobby", "lobby");
    assertEquals(List.of(id), sessions.cohort("waiting-paper-main"));
    assertEquals("waiting-paper-main", sessions.view(id).orElseThrow().cohortId());

    sessions.connectedTo(player, "paper-main", "lobby");
    assertEquals(List.of(), sessions.cohort("waiting-paper-main"));
    assertNull(sessions.view(id).orElseThrow().cohortId());
  }

  @Test
  void disconnectedSessionsAreKeptForTenMinutes() {
    String id = sessions.connect(player);
    sessions.setCohort(player, "waiting-paper-main");
    sessions.disconnect(player);
    SessionRegistry.SessionView view = sessions.view(id).orElseThrow();
    assertFalse(view.connected());
    assertNull(view.backendId());
    assertTrue(sessions.cohort("waiting-paper-main").isEmpty());
    assertTrue(sessions.connectedPlayer(id).isEmpty());

    clock.advance(Duration.ofMinutes(9));
    assertTrue(sessions.view(id).isPresent());
    clock.advance(Duration.ofMinutes(2));
    assertTrue(sessions.view(id).isEmpty());
  }
}
