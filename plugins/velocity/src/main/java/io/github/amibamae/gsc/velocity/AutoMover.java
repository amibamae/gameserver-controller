package io.github.amibamae.gsc.velocity;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * Moves waiting players to their backend once the controller has registered
 * it and it is routable (protocol integrations section 3.3). Only connected
 * members of the backend's waiting cohort who are still on the fallback are
 * moved, oldest first, one batch per tick. A player whose move failed waits
 * before it is tried again. Players who went to the fallback themselves are
 * not in a cohort and are never moved.
 */
final class AutoMover {
  /** How fast to move players. */
  record Settings(boolean enabled, int batchSize, Duration interval, Duration retryAfter) {
    static final Settings DEFAULT = new Settings(true, 5, Duration.ofSeconds(5), Duration.ofSeconds(30));
  }

  /** One session as the selection rule sees it. */
  record Candidate(String sessionId, boolean connected, String currentBackendId, String cohortId, Instant waitingSince, Instant lastFailedAt) {}

  static final Duration MOVE_TIMEOUT = Duration.ofSeconds(10);

  private final Settings settings;
  private final BackendRegistry backends;
  private final SessionRegistry sessions;
  private final ProxyOperations proxy;
  private final String fallback;
  private final Clock clock;
  private final Map<String, Instant> failures = new HashMap<>();
  private final AtomicBoolean running = new AtomicBoolean();

  AutoMover(Settings settings, BackendRegistry backends, SessionRegistry sessions, ProxyOperations proxy, String fallback, Clock clock) {
    this.settings = settings;
    this.backends = backends;
    this.sessions = sessions;
    this.proxy = proxy;
    this.fallback = fallback;
    this.clock = clock;
  }

  /**
   * The sessions to move to backendId now: the selection rule of section
   * 3.3, with no side effects.
   */
  static List<String> select(List<Candidate> candidates, String backendId, boolean routable, String fallbackId, Instant now, Settings settings) {
    if (!routable || fallbackId == null || fallbackId.isEmpty()) {
      return List.of();
    }
    String cohort = JoinRouter.waitingCohortFor(backendId);
    return candidates.stream()
        .filter(c -> c.connected() && cohort.equals(c.cohortId()) && fallbackId.equals(c.currentBackendId()))
        .filter(c -> c.lastFailedAt() == null || !now.isBefore(c.lastFailedAt().plus(settings.retryAfter())))
        .sorted(Comparator.comparing((Candidate c) -> c.waitingSince() == null ? Instant.MAX : c.waitingSince())
            .thenComparing(Candidate::sessionId))
        .limit(settings.batchSize())
        .map(Candidate::sessionId)
        .toList();
  }

  /** Runs one batch for every routable backend. Overlapping ticks are skipped. */
  void tick() {
    if (!settings.enabled() || !running.compareAndSet(false, true)) {
      return;
    }
    try {
      Instant now = clock.instant();
      List<SessionRegistry.Waiting> waiting = sessions.waiting();
      Map<String, UUID> players = new HashMap<>();
      List<Candidate> candidates = new ArrayList<>();
      synchronized (failures) {
        failures.values().removeIf(at -> !now.isBefore(at.plus(settings.retryAfter())));
        for (SessionRegistry.Waiting w : waiting) {
          players.put(w.sessionId(), w.player());
          candidates.add(new Candidate(w.sessionId(), true, w.backendId(), w.cohortId(), w.waitingSince(), failures.get(w.sessionId())));
        }
      }
      for (BackendRegistry.Backend backend : backends.all()) {
        for (String sessionId : select(candidates, backend.id(), backends.routable(backend.id()), fallback, now, settings)) {
          // Re-check right before each move: the backend may have stopped
          // being routable during this batch.
          if (!backends.routable(backend.id())) {
            break;
          }
          ProxyOperations.TransferResult result = proxy.transfer(players.get(sessionId), backend.id(), MOVE_TIMEOUT);
          if (result != ProxyOperations.TransferResult.TRANSFERRED) {
            synchronized (failures) {
              failures.put(sessionId, clock.instant());
            }
          }
        }
      }
    } finally {
      running.set(false);
    }
  }
}
