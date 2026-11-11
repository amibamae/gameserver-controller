package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import org.junit.jupiter.api.Test;

class JoinRouterTest {
  final JoinRouter router = new JoinRouter(List.of("paper-main", "paper-backup"), Map.of("survival.example.com", List.of("paper-survival")), "lobby", true);

  @Test
  void newPlayersGoToTheFirstRoutableJoinBackend() {
    assertEquals(new JoinRouter.Decision("paper-main", null), router.initial(Optional.empty(), Set.of("paper-main", "paper-backup")::contains).orElseThrow());
    assertEquals(new JoinRouter.Decision("paper-backup", null), router.initial(Optional.empty(), Set.of("paper-backup")::contains).orElseThrow());
  }

  @Test
  void playersWaitOnTheFallbackWhenNothingIsRoutable() {
    assertEquals(new JoinRouter.Decision("lobby", "waiting-paper-main"), router.initial(Optional.empty(), id -> false).orElseThrow());
  }

  @Test
  void forcedHostsReplaceTheJoinList() {
    assertEquals(new JoinRouter.Decision("paper-survival", null), router.initial(Optional.of("Survival.Example.com."), Set.of("paper-survival")::contains).orElseThrow());
    assertEquals(new JoinRouter.Decision("lobby", "waiting-paper-survival"), router.initial(Optional.of("survival.example.com"), Set.of("paper-main")::contains).orElseThrow());
    assertEquals("paper-main", router.initial(Optional.of("play.example.com"), Set.of("paper-main")::contains).orElseThrow().backendId());
  }

  @Test
  void kickedPlayersReturnToTheFallbackOnlyFromManagedBackends() {
    assertEquals(new JoinRouter.Decision("lobby", "waiting-paper-main"), router.afterKick("paper-main", true).orElseThrow());
    assertTrue(router.afterKick("minigames", false).isEmpty());
    assertTrue(router.afterKick("lobby", true).isEmpty());
    assertTrue(new JoinRouter(List.of("paper-main"), Map.of(), "lobby", false).afterKick("paper-main", true).isEmpty());
  }

  @Test
  void noJoinBackendsLeavesVelocityInCharge() {
    assertTrue(new JoinRouter(List.of(), Map.of(), "lobby", true).initial(Optional.empty(), id -> true).isEmpty());
  }
}
