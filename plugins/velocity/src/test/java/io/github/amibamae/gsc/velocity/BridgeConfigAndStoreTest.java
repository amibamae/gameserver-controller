package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
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
  @Test
  void defaultConfigParses() throws Exception {
    try (InputStream input = getClass().getResourceAsStream("/config.toml")) {
      BridgeConfig config = BridgeConfig.parse(new Toml().read(input));
      assertTrue(config.bind().isLoopbackAddress());
      assertEquals(25580, config.port());
      assertEquals("lobby", config.fallback());
      assertEquals(Duration.ofSeconds(5), config.healthCheckInterval());
      assertTrue(config.joinBackends().isEmpty());
      assertEquals(AutoMover.Settings.DEFAULT, config.autoMove(), "default: move 5 waiting players every 5 seconds");
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
