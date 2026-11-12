package io.github.amibamae.gsc.velocity;

import com.google.inject.Inject;
import com.moandjiezana.toml.Toml;
import com.velocitypowered.api.event.Subscribe;
import com.velocitypowered.api.event.connection.DisconnectEvent;
import com.velocitypowered.api.event.connection.PostLoginEvent;
import com.velocitypowered.api.event.player.KickedFromServerEvent;
import com.velocitypowered.api.event.player.PlayerChooseInitialServerEvent;
import com.velocitypowered.api.event.player.ServerConnectedEvent;
import com.velocitypowered.api.event.proxy.ProxyInitializeEvent;
import com.velocitypowered.api.event.proxy.ProxyShutdownEvent;
import com.velocitypowered.api.plugin.Plugin;
import com.velocitypowered.api.plugin.annotation.DataDirectory;
import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ProxyServer;
import com.velocitypowered.api.proxy.server.RegisteredServer;
import com.velocitypowered.api.scheduler.ScheduledTask;
import io.github.amibamae.gsc.velocity.BackendRegistry.Backend;
import java.io.IOException;
import java.io.InputStream;
import java.net.InetSocketAddress;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.attribute.PosixFilePermission;
import java.time.Clock;
import java.util.HashSet;
import java.util.Optional;
import java.util.Set;
import org.slf4j.Logger;

/**
 * Velocity plugin that serves the proxy bridge API for the gameserver
 * controller, routes new players to controller-registered backends, and
 * keeps waiting players on the fallback server in a waiting cohort.
 */
@Plugin(
    id = "gsc-velocity-bridge",
    name = "GSC Velocity Bridge",
    version = "0.1.0",
    description = "Proxy bridge API and join routing for gameserver-controller",
    url = "https://github.com/amibamae/gameserver-controller",
    authors = {"gameserver-controller contributors"})
public final class GscBridgePlugin {
  private final ProxyServer proxy;
  private final Logger logger;
  private final Path dataDirectory;

  private BackendRegistry backends;
  private SessionRegistry sessions;
  private JoinRouter router;
  private BackendStore store;
  private BridgeHttpServer http;
  private ScheduledTask healthTask;
  private ScheduledTask moveTask;

  @Inject
  public GscBridgePlugin(ProxyServer proxy, Logger logger, @DataDirectory Path dataDirectory) {
    this.proxy = proxy;
    this.logger = logger;
    this.dataDirectory = dataDirectory;
  }

  @Subscribe
  public void onProxyInitialize(ProxyInitializeEvent event) {
    try {
      start();
    } catch (IOException | RuntimeException e) {
      logger.error("GSC bridge did not start: {}", e.getMessage());
      stop();
    }
  }

  @Subscribe
  public void onProxyShutdown(ProxyShutdownEvent event) {
    stop();
  }

  private void start() throws IOException {
    BridgeConfig config = BridgeConfig.parse(new Toml().read(configFile().toFile()));
    byte[] token = readToken(config.tokenFile());
    Clock clock = Clock.systemUTC();

    Set<String> staticBackends = new HashSet<>();
    proxy.getAllServers().forEach(server -> staticBackends.add(server.getServerInfo().getName()));
    if (!config.fallback().isEmpty() && !staticBackends.contains(config.fallback())) {
      throw new IllegalArgumentException("routing.fallback must name a server configured in velocity.toml");
    }
    for (String backend : config.joinBackends()) {
      if (staticBackends.contains(backend)) {
        throw new IllegalArgumentException("routing.join-backends must list controller-managed backends, not velocity.toml servers");
      }
    }

    VelocityProxyOperations operations = new VelocityProxyOperations(proxy);
    backends = new BackendRegistry(staticBackends, config.healthCheckInterval().multipliedBy(3), clock);
    sessions = new SessionRegistry(clock);
    router = new JoinRouter(config.joinBackends(), config.forcedHosts(), config.fallback(), config.fallbackOnKick());
    store = new BackendStore(dataDirectory.resolve("backends.json"));
    for (Backend saved : store.load()) {
      backends.restore(saved);
      operations.applyRegistration(saved.id(), saved.address());
    }

    BridgeService service = new BridgeService(config.proxyInstanceId(), backends, sessions, operations, clock, this::saveBackends);
    boolean autoMove = config.autoMove().enabled() && !config.fallback().isEmpty();
    service.advertiseAutoMove(autoMove);
    http = new BridgeHttpServer(service, token, logger);
    InetSocketAddress bound = http.start(new InetSocketAddress(config.bind(), config.port()));
    logger.info("GSC bridge listening on {}:{}", bound.getAddress().getHostAddress(), bound.getPort());

    healthTask = proxy.getScheduler()
        .buildTask(this, () -> checkHealth(operations))
        .repeat(config.healthCheckInterval())
        .schedule();

    if (autoMove) {
      AutoMover mover = new AutoMover(config.autoMove(), backends, sessions, operations, config.fallback(), clock);
      moveTask = proxy.getScheduler()
          .buildTask(this, mover::tick)
          .repeat(config.autoMove().interval())
          .schedule();
      logger.info("Moving waiting players automatically: {} every {} seconds", config.autoMove().batchSize(), config.autoMove().interval().toSeconds());
    }
  }

  private void stop() {
    if (healthTask != null) {
      healthTask.cancel();
      healthTask = null;
    }
    if (moveTask != null) {
      moveTask.cancel();
      moveTask = null;
    }
    if (http != null) {
      http.stop();
      http = null;
    }
  }

  private void checkHealth(ProxyOperations operations) {
    for (Backend backend : backends.all()) {
      boolean ok = operations.probe(backend.id(), backend.address(), BridgeService.PROBE_TIMEOUT);
      backends.recordHealth(backend.id(), backend.address(), ok);
    }
  }

  private void saveBackends() {
    try {
      store.save(backends.all());
    } catch (IOException e) {
      logger.warn("Could not save managed backends: {}", e.getClass().getSimpleName());
    }
  }

  @Subscribe
  public void onPostLogin(PostLoginEvent event) {
    if (sessions != null) {
      sessions.connect(event.getPlayer().getUniqueId());
    }
  }

  @Subscribe
  public void onChooseInitialServer(PlayerChooseInitialServerEvent event) {
    if (router == null) {
      return;
    }
    Player player = event.getPlayer();
    Optional<JoinRouter.Decision> decision = router.initial(
        player.getVirtualHost().map(InetSocketAddress::getHostString), backends::routable);
    decision.flatMap(choice -> proxy.getServer(choice.backendId()).map(server -> {
      event.setInitialServer(server);
      if (choice.waitingCohortId() != null) {
        sessions.setCohort(player.getUniqueId(), choice.waitingCohortId());
      }
      return server;
    }));
  }

  @Subscribe
  public void onServerConnected(ServerConnectedEvent event) {
    if (sessions != null) {
      sessions.connectedTo(event.getPlayer().getUniqueId(), event.getServer().getServerInfo().getName(), router.fallback());
    }
  }

  @Subscribe
  public void onKickedFromServer(KickedFromServerEvent event) {
    if (router == null) {
      return;
    }
    String from = event.getServer().getServerInfo().getName();
    boolean managed = backends.isManaged(from);
    if (managed) {
      // Stop routing new players there until a health check passes again.
      backends.markUnhealthy(from);
    }
    Optional<JoinRouter.Decision> decision = router.afterKick(from, managed);
    if (decision.isEmpty()) {
      return;
    }
    Optional<RegisteredServer> fallback = proxy.getServer(decision.get().backendId());
    if (fallback.isPresent()) {
      event.setResult(KickedFromServerEvent.RedirectPlayer.create(fallback.get()));
      sessions.setCohort(event.getPlayer().getUniqueId(), decision.get().waitingCohortId());
    }
  }

  @Subscribe
  public void onDisconnect(DisconnectEvent event) {
    if (sessions != null) {
      sessions.disconnect(event.getPlayer().getUniqueId());
    }
  }

  private Path configFile() throws IOException {
    Path file = dataDirectory.resolve("config.toml");
    if (!Files.exists(file)) {
      Files.createDirectories(dataDirectory);
      try (InputStream defaults = getClass().getResourceAsStream("/config.toml")) {
        if (defaults == null) {
          throw new IOException("default config is missing from the plugin jar");
        }
        Files.copy(defaults, file);
      }
      logger.info("Wrote default config to {}; edit it and restart the proxy", file);
    }
    return file;
  }

  private byte[] readToken(Path file) throws IOException {
    try {
      Set<PosixFilePermission> permissions = Files.getPosixFilePermissions(file);
      if (permissions.contains(PosixFilePermission.OTHERS_READ) || permissions.contains(PosixFilePermission.GROUP_READ)) {
        throw new IllegalArgumentException("bridge token file must be readable only by its owner");
      }
    } catch (UnsupportedOperationException e) {
      // Non-POSIX file system: the permissions cannot be checked here.
      logger.warn("Cannot check the permissions of the bridge token file on this file system; "
          + "make sure only the proxy's account can read {}", file);
    }
    byte[] token = new String(Files.readAllBytes(file), java.nio.charset.StandardCharsets.UTF_8).strip()
        .getBytes(java.nio.charset.StandardCharsets.UTF_8);
    if (token.length < 16) {
      throw new IllegalArgumentException("bridge token must be at least 16 characters");
    }
    return token;
  }
}
