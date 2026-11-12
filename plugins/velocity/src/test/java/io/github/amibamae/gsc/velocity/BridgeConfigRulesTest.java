package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import com.moandjiezana.toml.Toml;
import java.util.List;
import org.junit.jupiter.api.Test;

/** Pins the message each config rule reports, in the order rules are checked. */
class BridgeConfigRulesTest {
  private static final String VALID_BRIDGE = "token-file = \"/t\"\nproxy-instance-id = \"p\"\n";

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
            "routing.auto-move-interval-seconds must be between 1 and 300"));
    for (Case c : cases) {
      assertEquals(c.message(), rejection(c.toml()), c.toml());
    }
  }

  @Test
  void normalisesForcedHostNames() {
    BridgeConfig config = BridgeConfig.parse(new Toml().read("[bridge]\n" + VALID_BRIDGE
        + "[routing]\n[routing.forced-hosts]\n\"Play.Example.COM.\" = [\"paper-main\"]\n"));
    assertEquals(List.of("paper-main"), config.forcedHosts().get("play.example.com"));
  }
}
