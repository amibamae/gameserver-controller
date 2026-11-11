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
    Duration healthCheckInterval) {

  static BridgeConfig parse(Toml toml) {
    Toml bridge = toml.getTable("bridge");
    Toml routing = toml.getTable("routing");
    if (bridge == null || routing == null) {
      throw new IllegalArgumentException("config.toml needs [bridge] and [routing] sections");
    }
    InetAddress bind;
    try {
      bind = InetAddress.getByName(bridge.getString("bind", "127.0.0.1"));
    } catch (UnknownHostException e) {
      throw new IllegalArgumentException("bridge.bind must be an IP address");
    }
    if (!bind.isLoopbackAddress()) {
      throw new IllegalArgumentException("bridge.bind must be a loopback address");
    }
    long port = bridge.getLong("port", 25580L);
    if (port < 1 || port > 65535) {
      throw new IllegalArgumentException("bridge.port is out of range");
    }
    String tokenFile = bridge.getString("token-file", "");
    if (tokenFile.isEmpty()) {
      throw new IllegalArgumentException("bridge.token-file is required");
    }
    String instance = bridge.getString("proxy-instance-id", "");
    if (instance.isEmpty() || instance.length() > 1024) {
      throw new IllegalArgumentException("bridge.proxy-instance-id is required");
    }
    List<String> join = backendList(routing.getList("join-backends", List.of()), "routing.join-backends");
    Map<String, List<String>> forced = new LinkedHashMap<>();
    Toml forcedTable = routing.getTable("forced-hosts");
    if (forcedTable != null) {
      for (Map.Entry<String, Object> entry : forcedTable.toMap().entrySet()) {
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
    }
    String fallback = routing.getString("fallback", "");
    if (!fallback.isEmpty() && !BackendRegistry.validId(fallback)) {
      throw new IllegalArgumentException("routing.fallback must be a server name");
    }
    long health = routing.getLong("health-check-seconds", 5L);
    if (health < 1 || health > 300) {
      throw new IllegalArgumentException("routing.health-check-seconds must be between 1 and 300");
    }
    return new BridgeConfig(bind, (int) port, Path.of(tokenFile), instance, join, forced, fallback,
        routing.getBoolean("fallback-on-kick", true), Duration.ofSeconds(health));
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
