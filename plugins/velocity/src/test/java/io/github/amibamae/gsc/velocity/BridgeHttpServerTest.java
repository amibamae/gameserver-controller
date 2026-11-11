package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertFalse;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.slf4j.helpers.NOPLogger;

class BridgeHttpServerTest {
  static final String TOKEN = "test-token-0123456789abcdef";
  static final String INSTANCE = "{\"adapter_id\":\"hosting.vultr\",\"environment_id\":\"test\",\"resource_kind\":\"instance\",\"external_id\":\"instance-test-001\"}";

  /** In-memory proxy. */
  static final class FakeProxy implements ProxyOperations {
    final Map<String, BackendAddress> servers = new HashMap<>();
    final Map<UUID, String> playerServer = new HashMap<>();
    final List<String> calls = new ArrayList<>();
    boolean healthy = true;
    TransferResult nextTransfer = TransferResult.TRANSFERRED;

    @Override
    public void applyRegistration(String backendId, BackendAddress address) {
      calls.add("apply " + backendId + " " + address);
      servers.put(backendId, address);
    }

    @Override
    public void removeRegistration(String backendId) {
      calls.add("remove " + backendId);
      servers.remove(backendId);
    }

    @Override
    public boolean probe(String backendId, BackendAddress address, Duration timeout) {
      return healthy && address.equals(servers.get(backendId));
    }

    @Override
    public int connectedPlayers(String backendId) {
      return (int) playerServer.values().stream().filter(backendId::equals).count();
    }

    @Override
    public TransferResult transfer(UUID player, String destinationBackendId, Duration timeout) {
      calls.add("transfer " + destinationBackendId);
      if (nextTransfer == TransferResult.TRANSFERRED) {
        playerServer.put(player, destinationBackendId);
      }
      return nextTransfer;
    }

    @Override
    public String proxyVersion() {
      return "3.4.0-test";
    }
  }

  final MutableClock clock = new MutableClock(Instant.parse("2026-11-07T10:00:00Z"));
  final FakeProxy proxy = new FakeProxy();
  final BackendRegistry backends = new BackendRegistry(Set.of("lobby"), Duration.ofSeconds(15), clock);
  final SessionRegistry sessions = new SessionRegistry(clock);
  final HttpClient client = HttpClient.newBuilder().proxy(HttpClient.Builder.NO_PROXY).connectTimeout(Duration.ofSeconds(5)).build();
  int saves;
  BridgeHttpServer server;
  URI base;

  @BeforeEach
  void start() throws Exception {
    BridgeService service = new BridgeService("velocity-edge-001", backends, sessions, proxy, clock, () -> saves++);
    server = new BridgeHttpServer(service, TOKEN.getBytes(StandardCharsets.UTF_8), NOPLogger.NOP_LOGGER);
    InetSocketAddress bound = server.start(new InetSocketAddress(InetAddress.getLoopbackAddress(), 0));
    base = URI.create("http://127.0.0.1:" + bound.getPort());
  }

  @AfterEach
  void stop() {
    server.stop();
  }

  record Reply(int status, JsonObject body) {}

  Reply send(String method, String path, String body) throws Exception {
    return send(method, path, body, TOKEN);
  }

  Reply send(String method, String path, String body, String token) throws Exception {
    HttpRequest.Builder request = HttpRequest.newBuilder(base.resolve(path)).timeout(Duration.ofSeconds(5))
        .method(method, body == null ? HttpRequest.BodyPublishers.noBody() : HttpRequest.BodyPublishers.ofString(body));
    if (token != null) {
      request.header("Authorization", "Bearer " + token);
    }
    HttpResponse<String> response = client.send(request.build(), HttpResponse.BodyHandlers.ofString());
    return new Reply(response.statusCode(), JsonParser.parseString(response.body()).getAsJsonObject());
  }

  Reply register(String backend, String address) throws Exception {
    return send("PUT", "/v1/backends/" + backend, "{\"instance_ref\":" + INSTANCE + ",\"address\":\"" + address + "\"}");
  }

  @Test
  void requiresTheBearerToken() throws Exception {
    assertEquals(401, send("GET", "/v1/info", null, null).status());
    assertEquals(401, send("GET", "/v1/info", null, "wrong-token-0123456789").status());
    Reply info = send("GET", "/v1/info", null);
    assertEquals(200, info.status());
    assertEquals("1", info.body().get("api_version").getAsString());
    assertEquals("velocity-edge-001", info.body().get("proxy_instance_id").getAsString());
  }

  @Test
  void backendLifecycle() throws Exception {
    Reply unknown = send("GET", "/v1/backends/paper-main", null);
    assertEquals("unregistered", unknown.body().get("registration").getAsString());
    assertTrue(unknown.body().get("instance_ref").isJsonNull());

    Reply registered = register("paper-main", "203.0.113.20:25565");
    assertEquals(200, registered.status());
    assertEquals("registered", registered.body().get("registration").getAsString());
    assertTrue(registered.body().get("routable").getAsBoolean(), "register health-checks before replying");
    assertEquals("203.0.113.20:25565", registered.body().get("address").getAsString());
    assertEquals("instance-test-001", registered.body().getAsJsonObject("instance_ref").get("external_id").getAsString());

    Reply drained = send("POST", "/v1/backends/paper-main/drain", null);
    assertEquals("draining", drained.body().get("registration").getAsString());
    assertFalse(drained.body().get("routable").getAsBoolean());

    Reply removed = send("DELETE", "/v1/backends/paper-main", null);
    assertEquals("unregistered", removed.body().get("registration").getAsString());
    assertFalse(proxy.servers.containsKey("paper-main"));
    assertEquals(3, saves);
  }

  @Test
  void unhealthyBackendIsRegisteredButNotRoutable() throws Exception {
    proxy.healthy = false;
    Reply registered = register("paper-main", "paper-main.backends.example.com:25565");
    assertEquals("registered", registered.body().get("registration").getAsString());
    assertFalse(registered.body().get("routable").getAsBoolean());
  }

  @Test
  void refusesStaticBackendsAndBadInput() throws Exception {
    Reply refused = register("lobby", "203.0.113.20:25565");
    assertEquals(409, refused.status());
    assertEquals("backend_not_managed", refused.body().getAsJsonObject("error").get("code").getAsString());
    assertTrue(proxy.calls.isEmpty());
    assertEquals(400, register("paper-main", "not-an-address").status());
    assertEquals(400, send("PUT", "/v1/backends/paper-main", "{oops", TOKEN).status());
    assertEquals(400, send("GET", "/v1/backends/Bad_Name", null).status());
    assertEquals(404, send("POST", "/v1/backends/paper-main/drain", null).status());
    assertEquals(405, send("POST", "/v1/info", null).status());
    assertEquals(404, send("GET", "/v1/nothing", null).status());
  }

  @Test
  void sessionsTransfersAndCohorts() throws Exception {
    register("paper-main", "203.0.113.20:25565");
    UUID player = UUID.randomUUID();
    String session = sessions.connect(player);
    sessions.setCohort(player, "waiting-paper-main");
    sessions.connectedTo(player, "lobby", "lobby");

    Reply view = send("GET", "/v1/sessions/" + session, null);
    assertEquals("connected", view.body().get("state").getAsString());
    assertEquals("lobby", view.body().get("backend_id").getAsString());
    assertEquals("waiting-paper-main", view.body().get("waiting_cohort_id").getAsString());
    assertFalse(view.body().toString().contains(player.toString()), "the player UUID must never be returned");

    Reply cohort = send("GET", "/v1/cohorts/waiting-paper-main", null);
    assertEquals(session, cohort.body().getAsJsonArray("session_ids").get(0).getAsString());

    String transfer = "{\"destination_backend_id\":\"paper-main\"}";
    assertEquals("transferred", send("POST", "/v1/sessions/" + session + "/transfer", transfer).body().get("outcome").getAsString());

    proxy.nextTransfer = ProxyOperations.TransferResult.FAILED;
    assertEquals("failed", send("POST", "/v1/sessions/" + session + "/transfer", transfer).body().get("outcome").getAsString());

    proxy.nextTransfer = ProxyOperations.TransferResult.UNKNOWN;
    assertEquals(504, send("POST", "/v1/sessions/" + session + "/transfer", transfer).status());

    send("POST", "/v1/backends/paper-main/drain", null);
    proxy.calls.clear();
    Reply notRoutable = send("POST", "/v1/sessions/" + session + "/transfer", transfer);
    assertEquals("not_attempted", notRoutable.body().get("outcome").getAsString());
    assertTrue(proxy.calls.isEmpty(), "a draining destination must not be contacted");

    sessions.disconnect(player);
    assertEquals("disconnected", send("GET", "/v1/sessions/" + session, null).body().get("state").getAsString());
    assertEquals("not_attempted", send("POST", "/v1/sessions/" + session + "/transfer", transfer).body().get("outcome").getAsString());
    assertEquals(404, send("GET", "/v1/sessions/0123456789abcdef0123456789abcdef", null).status());
  }

  @Test
  void errorMessagesNeverEchoSessionIds() throws Exception {
    String unknown = "0123456789abcdef0123456789abcdef";
    Reply reply = send("GET", "/v1/sessions/" + unknown, null);
    assertFalse(reply.body().toString().contains(unknown));
  }

  @Test
  void refusesNonLoopbackBind() {
    BridgeService service = new BridgeService("p", backends, sessions, proxy, clock, () -> {});
    BridgeHttpServer other = new BridgeHttpServer(service, TOKEN.getBytes(StandardCharsets.UTF_8), NOPLogger.NOP_LOGGER);
    assertThrows(IllegalArgumentException.class, () -> other.start(new InetSocketAddress("0.0.0.0", 0)));
    assertThrows(IllegalArgumentException.class, () -> new BridgeHttpServer(service, "short".getBytes(StandardCharsets.UTF_8), NOPLogger.NOP_LOGGER));
  }
}
