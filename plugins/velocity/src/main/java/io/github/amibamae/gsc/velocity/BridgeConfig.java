package io.github.amibamae.gsc.velocity;

import com.moandjiezana.toml.Toml;
import java.net.InetAddress;
import java.net.UnknownHostException;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;

/** Validated plugin configuration from {@code config.toml}. */
record BridgeConfig(
    InetAddress bind,
    int port,
    Path tokenFile,
    String proxyInstanceId,
    List<String> joinBackends,
    Map<String, List<String>> forcedHosts,
    String fallback,
    boolean fallbackOnKick,
    Duration healthCheckInterval,
    AutoMover.Settings autoMove,
    boolean enforceTokenPermissions,
    boolean enforceLoopbackBind,
    boolean enforceFallback,
    Tls tls) {

  /** TLS settings for the bridge API. The table is optional; defaults keep TLS on. */
  record Tls(boolean enabled, Path certFile, Path keyFile, Path controllerCaFile) {}

  static BridgeConfig parse(Toml toml) {
    Toml bridge = toml.getTable("bridge");
    Toml routing = toml.getTable("routing");
    if (bridge == null || routing == null) {
      throw new IllegalArgumentException("config.toml needs [bridge] and [routing] sections");
    }
    // The controller registers managed backends at runtime through the bridge
    // API, so the fallback does not have to exist in velocity.toml at start-up.
    // The lock is off by default; enable it for static deployments where every
    // server is in velocity.toml and must be checked before the proxy starts.
    Toml security = toml.getTable("security");
    boolean enforceTokenPermissions = security == null || security.getBoolean("enforce-token-permissions", true);
    boolean enforceLoopbackBind = security == null
        ? true
        : security.getBoolean("enforce-loopback-bind", true);
    boolean enforceFallback = security == null
        ? false
        : security.getBoolean("enforce-fallback", false);
    InetAddress bind = loopbackBind(bridge, enforceLoopbackBind);
    long port = bounded(bridge, "port", 25580L, 1, 65535, "bridge.port is out of range");
    String tokenFile = bridge.getString("token-file", "");
    if (tokenFile.isEmpty()) {
      throw new IllegalArgumentException("bridge.token-file is required");
    }
    String instance = bridge.getString("proxy-instance-id", "");
    if (instance.isEmpty() || instance.length() > 1024) {
      throw new IllegalArgumentException("bridge.proxy-instance-id is required");
    }
    List<String> join = backendList(routing.getList("join-backends", List.of()), "routing.join-backends");
    Map<String, List<String>> forced = forcedHosts(routing.getTable("forced-hosts"));
    String fallback = routing.getString("fallback", "");
    if (!fallback.isEmpty() && !BackendRegistry.validId(fallback)) {
      throw new IllegalArgumentException("routing.fallback must be a server name");
    }
    long health = bounded(routing, "health-check-seconds", 5L, 1, 300, "routing.health-check-seconds must be between 1 and 300");
    long batch = bounded(routing, "auto-move-batch", 5L, 1, 100, "routing.auto-move-batch must be between 1 and 100");
    long interval = bounded(routing, "auto-move-interval-seconds", 5L, 1, 300, "routing.auto-move-interval-seconds must be between 1 and 300");
    AutoMover.Settings autoMove = new AutoMover.Settings(routing.getBoolean("auto-move", true), (int) batch,
        Duration.ofSeconds(interval), AutoMover.Settings.DEFAULT.retryAfter());
    return new BridgeConfig(bind, (int) port, Path.of(tokenFile), instance, join, forced, fallback,
        routing.getBoolean("fallback-on-kick", true), Duration.ofSeconds(health), autoMove,
        enforceTokenPermissions, enforceLoopbackBind, enforceFallback, parseTls(toml));
  }

  /**
   * Parses the optional [tls] table. Absent keys keep their defaults (TLS on), so a config that
   * predates TLS starts enforcing it without an edit. When TLS is on, all three file paths are
   * required and must be absolute; the files themselves are loaded later, not here.
   */
  private static Tls parseTls(Toml toml) {
    // toml4j throws NPE for absent tables, so check presence before getTable.
    boolean hasTable = toml.contains("tls");
    if (!hasTable) {
      throw new IllegalArgumentException(
          "tls is enabled but no [tls] table is configured; add [tls] with cert-file, key-file and controller-ca-file, or set tls.enabled = false");
    }
    Toml table = toml.getTable("tls");
    boolean enabled = table.getBoolean("enabled", true);
    if (!enabled) {
      // Paths are only read at start-up when TLS is on, so they stay null when disabled.
      return new Tls(false, null, null, null);
    }
    Path cert = absolutePath(table, "cert-file", "tls.cert-file is required when tls is enabled");
    Path key = absolutePath(table, "key-file", "tls.key-file is required when tls is enabled");
    Path ca = absolutePath(table, "controller-ca-file", "tls.controller-ca-file is required when tls is enabled");
    return new Tls(true, cert, key, ca);
  }

  private static Path absolutePath(Toml table, String key, String missing) {
    String value = table.getString(key, "");
    if (value.isEmpty()) {
      throw new IllegalArgumentException(missing);
    }
    Path path = Path.of(value);
    if (!path.isAbsolute()) {
      throw new IllegalArgumentException("tls." + key + " must be an absolute path");
    }
    return path;
  }

  /** Resolves {@code bridge.bind}; rejects non-loopback addresses unless disabled. */
  private static InetAddress loopbackBind(Toml bridge, boolean enforceLoopback) {
    InetAddress bind;
    try {
      bind = InetAddress.getByName(bridge.getString("bind", "127.0.0.1"));
    } catch (UnknownHostException e) {
      throw new IllegalArgumentException("bridge.bind must be an IP address");
    }
    if (!enforceLoopback || bind.isLoopbackAddress()) {
      return bind;
    }
    throw new IllegalArgumentException("bridge.bind must be a loopback address");
  }

  /** Reads an integer setting and rejects it with message outside [min, max]. */
  private static long bounded(Toml table, String key, long fallback, long min, long max, String message) {
    long value = table.getLong(key, fallback);
    if (value < min || value > max) {
      throw new IllegalArgumentException(message);
    }
    return value;
  }

  /**
   * Reads {@code [routing.forced-hosts]}: lower-case host names without a
   * trailing dot, each mapped to a non-empty list of server names.
   */
  private static Map<String, List<String>> forcedHosts(Toml table) {
    Map<String, List<String>> forced = new LinkedHashMap<>();
    if (table == null) {
      return forced;
    }
    for (Map.Entry<String, Object> entry : table.toMap().entrySet()) {
      // toml4j keeps the quotes around quoted keys such as "play.example.com".
      String host = entry.getKey().replaceAll("^\"|\"$", "").toLowerCase(Locale.ROOT);
      if (host.endsWith(".")) {
        host = host.substring(0, host.length() - 1);
      }
      if (!(entry.getValue() instanceof List<?> list) || list.isEmpty()) {
        throw new IllegalArgumentException("routing.forced-hosts values must be non-empty lists");
      }
      forced.put(host, backendList(list, "routing.forced-hosts." + host));
    }
    return forced;
  }

  private static List<String> backendList(List<?> values, String name) {
    List<String> result = new ArrayList<>();
    for (Object value : values) {
      if (!(value instanceof String id) || !BackendRegistry.validId(id)) {
        throw new IllegalArgumentException(name + " must list server names");
      }
      result.add(id);
    }
    return List.copyOf(result);
  }
}
