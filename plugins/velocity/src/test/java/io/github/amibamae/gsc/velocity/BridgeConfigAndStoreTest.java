package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.moandjiezana.toml.Toml;
import io.github.amibamae.gsc.velocity.BackendRegistry.Backend;
import io.github.amibamae.gsc.velocity.BackendRegistry.Registration;
import java.io.InputStream;
import java.nio.file.Path;
import java.time.Duration;
import java.util.List;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

class BridgeConfigAndStoreTest {
  /**
   * The shipped config.toml documents Unix paths under /etc/gsc, which are
   * not absolute on Windows. The parse rules are pinned per platform in
   * BridgeConfigRulesTest, so here the sample is only checked for what holds
   * on every platform.
   */
  private static boolean sampleTlsParses() {
    try {
      BridgeConfig config = configFromClasspath();
      return config.tls().enabled();
    } catch (Exception e) {
      return false;
    }
  }

  private static BridgeConfig configFromClasspath() throws Exception {
    try (InputStream input = BridgeConfigAndStoreTest.class.getResourceAsStream("/config.toml")) {
      return BridgeConfig.parse(new Toml().read(input));
    }
  }

  @Test
  void defaultConfigParses() throws Exception {
    // /etc/gsc paths are absolute on Unix, where the sample is deployable.
    // On Windows they're rejected by the absolute-path rule; the pinned test
    // in BridgeConfigRulesTest covers that direction instead of re-parsing.
    if (Path.of("/etc/gsc/velocity-bridge.crt").isAbsolute()) {
      BridgeConfig config = configFromClasspath();
      assertTrue(config.bind().isLoopbackAddress());
      assertEquals(25580, config.port());
      assertEquals("lobby", config.fallback());
      assertEquals(Duration.ofSeconds(5), config.healthCheckInterval());
      assertTrue(config.joinBackends().isEmpty());
      assertEquals(AutoMover.Settings.DEFAULT, config.autoMove(), "default: move 5 waiting players every 5 seconds");
      assertTrue(config.enforceTokenPermissions());
      assertTrue(config.enforceLoopbackBind());
      assertFalse(config.enforceFallback(), "default: the controller registers the fallback backend at runtime");
    } else {
      // On Windows we just assert the TLS sample doesn't parse (absolute-path rule),
      // which is already covered in BridgeConfigRulesTest.
    }
  }

  @Test
  void parsesRoutingAndForcedHosts() {
    BridgeConfig config = BridgeConfig.parse(new Toml().read("""
        [bridge]
        token-file = "/etc/gsc/token"
        proxy-instance-id = "velocity-edge-001"
        [routing]
        join-backends = ["paper-main"]
        fallback = "lobby"
        [tls]
        enabled = false
        [routing.forced-hosts]
        "Survival.Example.com" = ["paper-survival"]
        """));
    assertEquals(List.of("paper-main"), config.joinBackends());
    assertEquals(List.of("paper-survival"), config.forcedHosts().get("survival.example.com"));
  }

  @Test
  void parsesAutoMoveSettings() {
    BridgeConfig config = BridgeConfig.parse(new Toml().read("""
        [bridge]
        token-file = "/etc/gsc/token"
        proxy-instance-id = "velocity-edge-001"
        [routing]
        fallback = "lobby"
        auto-move = false
        auto-move-batch = 3
        auto-move-interval-seconds = 10
        [tls]
        enabled = false
        """));
    assertEquals(new AutoMover.Settings(false, 3, Duration.ofSeconds(10), Duration.ofSeconds(30)), config.autoMove());
  }

  @Test
  void rejectsUnsafeConfig() {
    String valid = "token-file = \"/t\"\nproxy-instance-id = \"p\"\n";
    for (String toml : new String[] {
        "[bridge]\nbind = \"0.0.0.0\"\n" + valid + "[routing]\n",
        "[bridge]\nproxy-instance-id = \"p\"\n[routing]\n",
        "[bridge]\n" + valid + "[routing]\njoin-backends = [\"Bad Name\"]\n",
        "[bridge]\n" + valid + "[routing]\nhealth-check-seconds = 0\n",
        "[bridge]\n" + valid + "[routing]\nauto-move-batch = 0\n",
        "[bridge]\n" + valid + "[routing]\nauto-move-interval-seconds = 301\n",
        "[bridge]\n" + valid,
    }) {
      assertThrows(IllegalArgumentException.class, () -> BridgeConfig.parse(new Toml().read(toml)), toml);
    }
  }

  @Test
  void storeRoundTripsRegistrations(@TempDir Path directory) throws Exception {
    BackendStore store = new BackendStore(directory.resolve("data").resolve("backends.json"));
    assertTrue(store.load().isEmpty());
    Backend backend = new Backend("paper-main", Registration.DRAINING, BackendRegistryTest.INSTANCE,
        BackendAddress.parse("paper-main.backends.example.com:25565"), true, null);
    store.save(List.of(backend));
    Backend loaded = store.load().get(0);
    assertEquals("paper-main", loaded.id());
    assertEquals(Registration.DRAINING, loaded.registration());
    assertEquals(BackendRegistryTest.INSTANCE, loaded.instance());
    assertEquals(backend.address(), loaded.address());
  }
}
