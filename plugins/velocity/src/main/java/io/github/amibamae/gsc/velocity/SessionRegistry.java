package io.github.amibamae.gsc.velocity;

import java.security.SecureRandom;
import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HexFormat;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;

/**
 * Opaque, per-connection session IDs and waiting cohorts. Session IDs are
 * random and never derived from the player's account. The player's UUID stays
 * inside this class and the transfer path; it is never returned by the API.
 */
final class SessionRegistry {
  static final Duration DISCONNECTED_RETENTION = Duration.ofMinutes(10);

  /** What the API may reveal about one session. */
  record SessionView(String sessionId, boolean connected, String backendId, String cohortId, Instant observedAt) {}

  private static final class Entry {
    final String sessionId;
    final UUID player;
    String backendId;
    String cohortId;
    Instant disconnectedAt;

    Entry(String sessionId, UUID player) {
      this.sessionId = sessionId;
      this.player = player;
    }
  }

  private final Clock clock;
  private final SecureRandom random = new SecureRandom();
  private final Map<String, Entry> bySession = new HashMap<>();
  private final Map<UUID, Entry> byPlayer = new HashMap<>();

  SessionRegistry(Clock clock) {
    this.clock = clock;
  }

  /** Starts a new session for a player who just connected to the proxy. */
  synchronized String connect(UUID player) {
    purge();
    Entry previous = byPlayer.remove(player);
    if (previous != null && previous.disconnectedAt == null) {
      previous.disconnectedAt = clock.instant();
    }
    byte[] bytes = new byte[16];
    random.nextBytes(bytes);
    String id = HexFormat.of().formatHex(bytes);
    Entry entry = new Entry(id, player);
    bySession.put(id, entry);
    byPlayer.put(player, entry);
    return id;
  }

  /** Records the backend a player is now connected to. */
  synchronized void connectedTo(UUID player, String backendId, String fallbackId) {
    Entry entry = byPlayer.get(player);
    if (entry == null) {
      return;
    }
    entry.backendId = backendId;
    if (!backendId.equals(fallbackId)) {
      // A player who reached a real backend is no longer waiting.
      entry.cohortId = null;
    }
  }

  synchronized void setCohort(UUID player, String cohortId) {
    Entry entry = byPlayer.get(player);
    if (entry != null) {
      entry.cohortId = cohortId;
    }
  }

  synchronized void disconnect(UUID player) {
    Entry entry = byPlayer.remove(player);
    if (entry != null) {
      entry.disconnectedAt = clock.instant();
      entry.backendId = null;
      entry.cohortId = null;
    }
    purge();
  }

  synchronized Optional<SessionView> view(String sessionId) {
    purge();
    Entry entry = bySession.get(sessionId);
    if (entry == null) {
      return Optional.empty();
    }
    boolean connected = entry.disconnectedAt == null;
    return Optional.of(new SessionView(entry.sessionId, connected, connected ? entry.backendId : null, connected ? entry.cohortId : null, clock.instant()));
  }

  /** Returns the connected player for a session, for the transfer path only. */
  synchronized Optional<UUID> connectedPlayer(String sessionId) {
    Entry entry = bySession.get(sessionId);
    return entry == null || entry.disconnectedAt != null ? Optional.empty() : Optional.of(entry.player);
  }

  synchronized List<String> cohort(String cohortId) {
    List<String> members = new ArrayList<>();
    for (Entry entry : byPlayer.values()) {
      if (entry.disconnectedAt == null && cohortId.equals(entry.cohortId)) {
        members.add(entry.sessionId);
      }
    }
    members.sort(null);
    return members;
  }

  private void purge() {
    Instant cutoff = clock.instant().minus(DISCONNECTED_RETENTION);
    bySession.values().removeIf(entry -> entry.disconnectedAt != null && entry.disconnectedAt.isBefore(cutoff));
  }
}
