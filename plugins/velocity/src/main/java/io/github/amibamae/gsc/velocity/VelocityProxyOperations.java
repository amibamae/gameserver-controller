package io.github.amibamae.gsc.velocity;

import com.velocitypowered.api.proxy.ConnectionRequestBuilder;
import com.velocitypowered.api.proxy.Player;
import com.velocitypowered.api.proxy.ProxyServer;
import com.velocitypowered.api.proxy.server.RegisteredServer;
import com.velocitypowered.api.proxy.server.ServerInfo;
import java.time.Duration;
import java.util.Map;
import java.util.Optional;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

/** {@link ProxyOperations} backed by the Velocity API. */
final class VelocityProxyOperations implements ProxyOperations {
  private final ProxyServer proxy;
  private final Map<String, ServerInfo> registered = new ConcurrentHashMap<>();

  VelocityProxyOperations(ProxyServer proxy) {
    this.proxy = proxy;
  }

  @Override
  public synchronized void applyRegistration(String backendId, BackendAddress address) {
    ServerInfo info = new ServerInfo(backendId, address.toUnresolved());
    ServerInfo previous = registered.get(backendId);
    if (info.equals(previous)) {
      return;
    }
    if (previous != null) {
      // Connected players keep their existing connection; new connections
      // use the new address.
      proxy.unregisterServer(previous);
    }
    proxy.registerServer(info);
    registered.put(backendId, info);
  }

  @Override
  public synchronized void removeRegistration(String backendId) {
    ServerInfo previous = registered.remove(backendId);
    if (previous != null) {
      proxy.unregisterServer(previous);
    }
  }

  @Override
  public boolean probe(String backendId, BackendAddress address, Duration timeout) {
    Optional<RegisteredServer> server = proxy.getServer(backendId);
    if (server.isEmpty() || !server.get().getServerInfo().getAddress().equals(address.toUnresolved())) {
      return false;
    }
    try {
      server.get().ping().get(timeout.toMillis(), TimeUnit.MILLISECONDS);
      return true;
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
      return false;
    } catch (ExecutionException | TimeoutException e) {
      return false;
    }
  }

  @Override
  public int connectedPlayers(String backendId) {
    return proxy.getServer(backendId).map(server -> server.getPlayersConnected().size()).orElse(0);
  }

  @Override
  public TransferResult transfer(UUID playerId, String destinationBackendId, Duration timeout) {
    Optional<Player> player = proxy.getPlayer(playerId);
    Optional<RegisteredServer> destination = proxy.getServer(destinationBackendId);
    if (player.isEmpty() || destination.isEmpty()) {
      return TransferResult.FAILED;
    }
    try {
      ConnectionRequestBuilder.Result result =
          player.get().createConnectionRequest(destination.get()).connect().get(timeout.toMillis(), TimeUnit.MILLISECONDS);
      return result.isSuccessful() ? TransferResult.TRANSFERRED : TransferResult.FAILED;
    } catch (InterruptedException e) {
      Thread.currentThread().interrupt();
      return TransferResult.UNKNOWN;
    } catch (TimeoutException e) {
      return TransferResult.UNKNOWN;
    } catch (ExecutionException e) {
      return TransferResult.FAILED;
    }
  }

  @Override
  public String proxyVersion() {
    return proxy.getVersion().getVersion();
  }
}
