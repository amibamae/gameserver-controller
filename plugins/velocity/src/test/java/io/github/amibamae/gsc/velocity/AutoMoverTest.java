package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.Test;

class AutoMoverTest {
  /** The published conformance vector for integrations section 3.3. */
  static final Path VECTOR = Path.of("..", "..", "protocol", "conformance", "test-vectors", "proxy-cohort-automatic-move.json");

  @Test
  void selectionMatchesThePublishedVector() throws IOException {
    JsonObject vector = JsonParser.parseString(Files.readString(VECTOR)).getAsJsonObject();
    JsonObject input = vector.getAsJsonObject("input");
    JsonObject policy = input.getAsJsonObject("policy");
    AutoMover.Settings settings = new AutoMover.Settings(true, policy.get("batch_size").getAsInt(),
        Duration.ofSeconds(policy.get("interval_seconds").getAsLong()), Duration.ofSeconds(policy.get("retry_after_seconds").getAsLong()));
    var expected = vector.getAsJsonObject("expected").getAsJsonArray("evaluations");
    var evaluations = input.getAsJsonArray("evaluations");
    for (int i = 0; i < evaluations.size(); i++) {
      JsonObject evaluation = evaluations.get(i).getAsJsonObject();
      JsonObject backend = evaluation.getAsJsonObject("backend");
      boolean routable = "registered".equals(backend.get("registration").getAsString()) && backend.get("routable").getAsBoolean();
      List<AutoMover.Candidate> candidates = new ArrayList<>();
      for (JsonElement raw : evaluation.getAsJsonArray("sessions")) {
        JsonObject s = raw.getAsJsonObject();
        candidates.add(new AutoMover.Candidate(s.get("session_id").getAsString(), s.get("connected").getAsBoolean(),
            text(s, "current_backend_id"), text(s, "cohort_id"), Instant.parse(s.get("waiting_since").getAsString()),
            s.has("last_failed_at") ? Instant.parse(s.get("last_failed_at").getAsString()) : null));
      }
      List<String> moves = AutoMover.select(candidates, evaluation.get("backend_id").getAsString(), routable,
          evaluation.get("fallback_id").getAsString(), Instant.parse(evaluation.get("now").getAsString()), settings);
      List<String> want = new ArrayList<>();
      expected.get(i).getAsJsonObject().getAsJsonArray("moves").forEach(e -> want.add(e.getAsString()));
      assertEquals(want, moves, evaluation.get("case_id").getAsString());
    }
  }

  private static String text(JsonObject object, String key) {
    return object.has(key) && !object.get(key).isJsonNull() ? object.get(key).getAsString() : null;
  }

  final MutableClock clock = new MutableClock(Instant.parse("2026-11-07T10:00:00Z"));
  final BackendRegistry backends = new BackendRegistry(Set.of("lobby"), Duration.ofSeconds(15), clock);
  final SessionRegistry sessions = new SessionRegistry(clock);
  final BridgeHttpServerTest.FakeProxy proxy = new BridgeHttpServerTest.FakeProxy();
  final BackendAddress address = BackendAddress.parse("203.0.113.20:25565");

  UUID waitingPlayer() {
    UUID player = UUID.randomUUID();
    sessions.connect(player);
    sessions.setCohort(player, JoinRouter.waitingCohortFor("paper-main"));
    sessions.connectedTo(player, "lobby", "lobby");
    clock.advance(Duration.ofSeconds(1));
    return player;
  }

  long moved() {
    return proxy.calls.stream().filter(call -> call.equals("transfer paper-main")).count();
  }

  @Test
  void movesOneBatchPerTickOnlyWhileRoutable() {
    AutoMover mover = new AutoMover(AutoMover.Settings.DEFAULT, backends, sessions, proxy, "lobby", clock);
    List<UUID> waiting = new ArrayList<>();
    for (int i = 0; i < 7; i++) {
      waiting.add(waitingPlayer());
    }
    UUID choseLobby = UUID.randomUUID();
    sessions.connect(choseLobby);
    sessions.connectedTo(choseLobby, "lobby", "lobby");

    backends.register("paper-main", BackendRegistryTest.INSTANCE, address);
    mover.tick();
    assertEquals(0, moved(), "a registered backend that has not passed a health check must not receive players");

    backends.recordHealth("paper-main", address, true);
    mover.tick();
    assertEquals(5, moved(), "one tick moves at most one batch");
    for (int i = 0; i < 5; i++) {
      // The proxy reports the move; the plugin's connected event clears the cohort.
      sessions.connectedTo(waiting.get(i), "paper-main", "lobby");
    }
    mover.tick();
    assertEquals(7, moved());
    sessions.connectedTo(waiting.get(5), "paper-main", "lobby");
    sessions.connectedTo(waiting.get(6), "paper-main", "lobby");
    mover.tick();
    assertEquals(7, moved(), "a player who chose the lobby is never moved");

    UUID late = waitingPlayer();
    backends.drain("paper-main");
    mover.tick();
    assertEquals(7, moved(), "a draining backend must not receive players");
    assertTrue(sessions.waiting().stream().anyMatch(w -> w.player().equals(late)));
  }

  @Test
  void aFailedMoveWaitsBeforeItIsRetried() {
    AutoMover mover = new AutoMover(AutoMover.Settings.DEFAULT, backends, sessions, proxy, "lobby", clock);
    waitingPlayer();
    backends.register("paper-main", BackendRegistryTest.INSTANCE, address);
    backends.recordHealth("paper-main", address, true);
    proxy.nextTransfer = ProxyOperations.TransferResult.FAILED;
    mover.tick();
    assertEquals(1, moved());
    clock.advance(Duration.ofSeconds(5));
    backends.recordHealth("paper-main", address, true);
    mover.tick();
    assertEquals(1, moved(), "retried before the retry delay");
    clock.advance(Duration.ofSeconds(26));
    backends.recordHealth("paper-main", address, true);
    mover.tick();
    assertEquals(2, moved());
  }

  @Test
  void disabledMoverDoesNothing() {
    AutoMover mover = new AutoMover(new AutoMover.Settings(false, 5, Duration.ofSeconds(5), Duration.ofSeconds(30)), backends, sessions, proxy, "lobby", clock);
    waitingPlayer();
    backends.register("paper-main", BackendRegistryTest.INSTANCE, address);
    backends.recordHealth("paper-main", address, true);
    mover.tick();
    assertEquals(0, moved());
  }

  @Test
  void bridgeInfoAdvertisesAutomaticMoves() {
    BridgeService service = new BridgeService("p", backends, sessions, proxy, clock, () -> {});
    assertTrue(!service.info().getAsJsonArray("features").toString().contains("auto_move"));
    service.advertiseAutoMove(true);
    assertTrue(service.info().getAsJsonArray("features").toString().contains("\"auto_move\""));
  }
}
