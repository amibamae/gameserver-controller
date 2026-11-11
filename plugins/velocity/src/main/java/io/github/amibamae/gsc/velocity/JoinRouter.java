package io.github.amibamae.gsc.velocity;

import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Optional;
import java.util.function.Predicate;

/**
 * Decides where new players go and where players go when their backend fails.
 * It only picks backends the predicate reports as routable, which the plugin
 * wires to the registry: registered by the controller and healthy.
 */
final class JoinRouter {
  /** Where to send a player, and the waiting cohort to put them in, if any. */
  record Decision(String backendId, String waitingCohortId) {}

  private final List<String> joinBackends;
  private final Map<String, List<String>> forcedHosts;
  private final String fallback;
  private final boolean fallbackOnKick;

  JoinRouter(List<String> joinBackends, Map<String, List<String>> forcedHosts, String fallback, boolean fallbackOnKick) {
    this.joinBackends = List.copyOf(joinBackends);
    this.forcedHosts = Map.copyOf(forcedHosts);
    this.fallback = fallback;
    this.fallbackOnKick = fallbackOnKick;
  }

  static String waitingCohortFor(String backendId) {
    return "waiting-" + backendId;
  }

  String fallback() {
    return fallback;
  }

  /**
   * Chooses the initial backend for a new player. Returns empty when the
   * bridge has no opinion and Velocity's own configuration should decide.
   */
  Optional<Decision> initial(Optional<String> virtualHost, Predicate<String> routable) {
    List<String> candidates = virtualHost
        .map(host -> host.toLowerCase(Locale.ROOT))
        .map(host -> host.endsWith(".") ? host.substring(0, host.length() - 1) : host)
        .map(forcedHosts::get)
        .orElse(joinBackends);
    if (candidates.isEmpty()) {
      return Optional.empty();
    }
    for (String backend : candidates) {
      if (routable.test(backend)) {
        return Optional.of(new Decision(backend, null));
      }
    }
    if (fallback == null || fallback.isEmpty()) {
      return Optional.empty();
    }
    return Optional.of(new Decision(fallback, waitingCohortFor(candidates.get(0))));
  }

  /**
   * Chooses where a player goes after a managed backend kicked them, or
   * empty to let Velocity handle it.
   */
  Optional<Decision> afterKick(String kickedFrom, boolean managed) {
    if (!fallbackOnKick || !managed || fallback == null || fallback.isEmpty() || fallback.equals(kickedFrom)) {
      return Optional.empty();
    }
    return Optional.of(new Decision(fallback, waitingCohortFor(kickedFrom)));
  }
}
