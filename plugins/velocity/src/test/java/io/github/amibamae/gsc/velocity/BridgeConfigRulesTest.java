package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.moandjiezana.toml.Toml;
import java.nio.file.Path;
import java.util.List;
import org.junit.jupiter.api.Test;

/** Pins the message each config rule reports, in the order rules are checked. */
class BridgeConfigRulesTest {
  private static final String VALID_BRIDGE = "token-file = \"/t\"\nproxy-instance-id = \"p\"\n";

  /** An absolute path for the current platform, so tests work on Windows and Unix. */
  private static String abs(String name) {
    return Path.of(name).toAbsolutePath().toString().replace('\\', '/');
  }

  private static String rejection(String toml) {
    return assertThrows(IllegalArgumentException.class, () -> BridgeConfig.parse(new Toml().read(toml)), toml).getMessage();
  }

  @Test
  void reportsEachRule() {
    record Case(String toml, String message) {}
    List<Case> cases = List.of(
        new Case("[routing]\n", "config.toml needs [bridge] and [routing] sections"),
        new Case("[bridge]\n" + VALID_BRIDGE, "config.toml needs [bridge] and [routing] sections"),
        new Case("[bridge]\nbind = \"not an address\"\n" + VALID_BRIDGE + "[routing]\n", "bridge.bind must be an IP address"),
        new Case("[bridge]\nbind = \"192.0.2.1\"\n" + VALID_BRIDGE + "[routing]\n", "bridge.bind must be a loopback address"),
        new Case("[bridge]\nport = 0\n" + VALID_BRIDGE + "[routing]\n", "bridge.port is out of range"),
        new Case("[bridge]\nport = 65536\n" + VALID_BRIDGE + "[routing]\n", "bridge.port is out of range"),
        new Case("[bridge]\nproxy-instance-id = \"p\"\n[routing]\n", "bridge.token-file is required"),
        new Case("[bridge]\ntoken-file = \"/t\"\n[routing]\n", "bridge.proxy-instance-id is required"),
        new Case("[bridge]\ntoken-file = \"/t\"\nproxy-instance-id = \"" + "p".repeat(1025) + "\"\n[routing]\n",
            "bridge.proxy-instance-id is required"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\njoin-backends = [\"Bad Name\"]\n", "routing.join-backends must list server names"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\n[routing.forced-hosts]\n\"a.example\" = []\n",
            "routing.forced-hosts values must be non-empty lists"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\n[routing.forced-hosts]\n\"a.example\" = [\"Bad Name\"]\n",
            "routing.forced-hosts.a.example must list server names"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\nfallback = \"Bad Name\"\n", "routing.fallback must be a server name"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\nhealth-check-seconds = 301\n", "routing.health-check-seconds must be between 1 and 300"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\nauto-move-batch = 101\n", "routing.auto-move-batch must be between 1 and 100"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\nauto-move-interval-seconds = 0\n",
            "routing.auto-move-interval-seconds must be between 1 and 300"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\n[tls]\n", "tls.cert-file is required when tls is enabled"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\n[tls]\ncert-file = \"" + abs("c") + "\"\n",
            "tls.key-file is required when tls is enabled"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\n[tls]\ncert-file = \"" + abs("c")
            + "\"\nkey-file = \"" + abs("k") + "\"\n",
            "tls.controller-ca-file is required when tls is enabled"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\n[tls]\ncert-file = \"c\"\nkey-file = \"" + abs("k")
            + "\"\ncontroller-ca-file = \"" + abs("ca") + "\"\n",
            "tls.cert-file must be an absolute path"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\n[tls]\ncert-file = \"" + abs("c")
            + "\"\nkey-file = \"k\"\ncontroller-ca-file = \"" + abs("ca") + "\"\n",
            "tls.key-file must be an absolute path"),
        new Case("[bridge]\n" + VALID_BRIDGE + "[routing]\n[tls]\ncert-file = \"" + abs("c")
            + "\"\nkey-file = \"" + abs("k") + "\"\ncontroller-ca-file = \"ca\"\n",
            "tls.controller-ca-file must be an absolute path"));
    for (Case c : cases) {
      assertEquals(c.message(), rejection(c.toml()), c.toml());
    }
  }

  @Test
  void tlsIsOnByDefaultAndParsesTheOptionalTable() {
    // Missing [tls] table: the config must provide it with cert-file, key-file and controller-ca-file.
    assertEquals("tls is enabled but no [tls] table is configured; add [tls] with cert-file, key-file and controller-ca-file, or set tls.enabled = false",
        rejection("[bridge]\n" + VALID_BRIDGE + "[routing]\n"));
    String certPath = abs("cert.pem");
    String keyPath = abs("key.pem");
    String caPath = abs("ca.pem");
    BridgeConfig config = BridgeConfig.parse(new Toml().read("[bridge]\n" + VALID_BRIDGE
        + "[routing]\n[tls]\ncert-file = \"" + certPath + "\"\nkey-file = \"" + keyPath
        + "\"\ncontroller-ca-file = \"" + caPath + "\"\n"));
    assertTrue(config.tls().enabled());
    // Compare Paths directly to avoid platform-specific separator differences.
    assertEquals(Path.of(certPath).normalize(), config.tls().certFile().normalize());
    assertEquals(Path.of(keyPath).normalize(), config.tls().keyFile().normalize());
    assertEquals(Path.of(caPath).normalize(), config.tls().controllerCaFile().normalize());
  }

  @Test
  void tlsCanBeDisabled() {
    BridgeConfig config = BridgeConfig.parse(new Toml().read("[bridge]\n" + VALID_BRIDGE
        + "[routing]\n[tls]\nenabled = false\n"));
    assertFalse(config.tls().enabled());
    assertTrue(config.tls().certFile() == null);
    assertTrue(config.tls().keyFile() == null);
    assertTrue(config.tls().controllerCaFile() == null);
  }

  @Test
  void loopbackBindCanBeDisabled() {
    String toml = "[bridge]\nbind = \"192.0.2.1\"\n" + VALID_BRIDGE + "[routing]\n[security]\nenforce-loopback-bind = false\n"
        + "[tls]\ncert-file = \"" + abs("c") + "\"\nkey-file = \"" + abs("k")
        + "\"\ncontroller-ca-file = \"" + abs("ca") + "\"\n";
    BridgeConfig config = BridgeConfig.parse(new Toml().read(toml));
    assertEquals("192.0.2.1", config.bind().getHostAddress());
    assertFalse(config.enforceLoopbackBind());
    // The same bind is still rejected while the lock is explicitly on.
    assertEquals("bridge.bind must be a loopback address", rejection(toml.replace("false", "true")));
  }

  @Test
  void securityDefaultsKeepTheLocksOn() {
    // enforce-fallback is off by default: the controller registers the
    // fallback backend at runtime through the bridge API.
    // TLS is on by default, so a minimal config still needs the [tls] table.
    String certPath = abs("cert.pem");
    String keyPath = abs("key.pem");
    String caPath = abs("ca.pem");
    BridgeConfig config = BridgeConfig.parse(new Toml().read(
        "[bridge]\n" + VALID_BRIDGE + "[routing]\n[tls]\n"
            + "cert-file = \"" + certPath + "\"\nkey-file = \"" + keyPath
            + "\"\ncontroller-ca-file = \"" + caPath + "\"\n"));
    assertTrue(config.enforceTokenPermissions());
    assertTrue(config.enforceLoopbackBind());
    assertFalse(config.enforceFallback());
    assertTrue(config.tls().enabled());
  }

  @Test
  void normalisesForcedHostNames() {
    BridgeConfig config = BridgeConfig.parse(new Toml().read("[bridge]\n" + VALID_BRIDGE
        + "[routing]\n[routing.forced-hosts]\n\"Play.Example.COM.\" = [\"paper-main\"]\n"
        + "[tls]\ncert-file = \"" + abs("c") + "\"\nkey-file = \"" + abs("k")
        + "\"\ncontroller-ca-file = \"" + abs("ca") + "\"\n"));
    assertEquals(List.of("paper-main"), config.forcedHosts().get("play.example.com"));
  }
}
