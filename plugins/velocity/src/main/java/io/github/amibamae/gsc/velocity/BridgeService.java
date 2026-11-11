package io.github.amibamae.gsc.velocity;

import com.google.gson.JsonArray;
import com.google.gson.JsonNull;
import com.google.gson.JsonObject;
import io.github.amibamae.gsc.velocity.BackendRegistry.Backend;
import io.github.amibamae.gsc.velocity.BackendRegistry.InstanceRef;
import io.github.amibamae.gsc.velocity.BackendRegistry.Registration;
import java.time.Clock;
import java.time.Duration;
import java.util.List;
import java.util.Optional;
import java.util.UUID;
import java.util.regex.Pattern;

/**
 * Implements the proxy bridge API, version 1, independently of HTTP. Every
 * method returns the JSON body for a 200 reply or throws
 * {@link BridgeException} for a refusal that changed nothing.
 */
final class BridgeService {
  static final String API_VERSION = "1";
  static final Duration PROBE_TIMEOUT = Duration.ofSeconds(3);
  static final Duration TRANSFER_TIMEOUT = Duration.ofSeconds(10);
  private static final Pattern COHORT_ID = Pattern.compile("^[a-z0-9][a-z0-9._-]{0,127}$");
  private static final Pattern SESSION_ID = Pattern.compile("^[0-9a-f]{32}$");

  private final String proxyInstanceId;
  private final BackendRegistry backends;
  private final SessionRegistry sessions;
  private final ProxyOperations proxy;
  private final Clock clock;
  private final Runnable onBackendsChanged;

  BridgeService(String proxyInstanceId, BackendRegistry backends, SessionRegistry sessions, ProxyOperations proxy, Clock clock, Runnable onBackendsChanged) {
    this.proxyInstanceId = proxyInstanceId;
    this.backends = backends;
    this.sessions = sessions;
    this.proxy = proxy;
    this.clock = clock;
    this.onBackendsChanged = onBackendsChanged;
  }

  JsonObject info() {
    JsonObject info = new JsonObject();
    info.addProperty("api_version", API_VERSION);
    info.addProperty("proxy_platform", "velocity");
    info.addProperty("proxy_version", proxy.proxyVersion());
    info.addProperty("proxy_instance_id", proxyInstanceId);
    JsonArray features = new JsonArray();
    for (String feature : List.of("backends", "sessions", "transfer", "cohorts", "join_routing")) {
      features.add(feature);
    }
    info.add("features", features);
    return info;
  }

  JsonObject backend(String id) {
    requireBackendId(id);
    return backendJson(id, backends.get(id));
  }

  JsonObject register(String id, JsonObject body) {
    requireBackendId(id);
    if (body == null || !body.has("instance_ref") || !body.get("instance_ref").isJsonObject() || !body.has("address")) {
      throw new BridgeException(400, "invalid_request", "instance_ref and address are required.");
    }
    InstanceRef instance = parseInstanceRef(body.getAsJsonObject("instance_ref"));
    BackendAddress address;
    try {
      address = BackendAddress.parse(body.get("address").getAsString());
    } catch (UnsupportedOperationException | IllegalStateException e) {
      throw new BridgeException(400, "invalid_address", "Address must be host:port.");
    }
    backends.register(id, instance, address);
    proxy.applyRegistration(id, address);
    onBackendsChanged.run();
    // Check health now so a successful reply usually shows routable.
    backends.recordHealth(id, address, proxy.probe(id, address, PROBE_TIMEOUT));
    return backend(id);
  }

  JsonObject drain(String id) {
    requireBackendId(id);
    if (backends.drain(id).isEmpty()) {
      throw new BridgeException(404, "backend_not_found", "Backend is not registered.");
    }
    onBackendsChanged.run();
    return backend(id);
  }

  JsonObject unregister(String id) {
    requireBackendId(id);
    if (backends.unregister(id).isPresent()) {
      proxy.removeRegistration(id);
      onBackendsChanged.run();
    }
    return backend(id);
  }

  JsonObject session(String sessionId) {
    requireSessionId(sessionId);
    SessionRegistry.SessionView view = sessions.view(sessionId)
        .orElseThrow(() -> new BridgeException(404, "session_not_found", "Session not found."));
    JsonObject json = new JsonObject();
    json.addProperty("session_id", view.sessionId());
    json.addProperty("state", view.connected() ? "connected" : "disconnected");
    if (view.connected() && view.backendId() != null) {
      json.addProperty("backend_id", view.backendId());
    }
    if (view.connected() && view.cohortId() != null) {
      json.addProperty("waiting_cohort_id", view.cohortId());
    }
    json.addProperty("observed_at", view.observedAt().toString());
    return json;
  }

  /**
   * Transfers one session. Returns an outcome body; throws a 504 when the
   * proxy did not answer in time, which clients treat as an unknown outcome.
   */
  JsonObject transfer(String sessionId, JsonObject body) {
    requireSessionId(sessionId);
    if (body == null || !body.has("destination_backend_id") || !body.get("destination_backend_id").isJsonPrimitive()) {
      throw new BridgeException(400, "invalid_request", "destination_backend_id is required.");
    }
    String destination = body.get("destination_backend_id").getAsString();
    requireBackendId(destination);
    Optional<UUID> player = sessions.connectedPlayer(sessionId);
    if (player.isEmpty()) {
      return outcome("not_attempted", "session_not_connected");
    }
    if (!backends.routable(destination)) {
      return outcome("not_attempted", "destination_not_routable");
    }
    ProxyOperations.TransferResult result = proxy.transfer(player.get(), destination, TRANSFER_TIMEOUT);
    switch (result) {
      case TRANSFERRED:
        return outcome("transferred", null);
      case FAILED:
        return outcome("failed", "connection_failed");
      default:
        throw new BridgeException(504, "transfer_outcome_unknown", "The proxy did not report a transfer result in time.");
    }
  }

  JsonObject cohort(String cohortId) {
    if (cohortId == null || !COHORT_ID.matcher(cohortId).matches()) {
      throw new BridgeException(400, "invalid_cohort_id", "Cohort ID is invalid.");
    }
    JsonObject json = new JsonObject();
    json.addProperty("cohort_id", cohortId);
    JsonArray ids = new JsonArray();
    sessions.cohort(cohortId).forEach(ids::add);
    json.add("session_ids", ids);
    json.addProperty("observed_at", clock.instant().toString());
    return json;
  }

  private JsonObject backendJson(String id, Optional<Backend> found) {
    JsonObject json = new JsonObject();
    json.addProperty("backend_id", id);
    if (found.isEmpty()) {
      json.addProperty("registration", Registration.UNREGISTERED.wire);
      json.add("instance_ref", JsonNull.INSTANCE);
      json.add("address", JsonNull.INSTANCE);
      json.addProperty("routable", false);
    } else {
      Backend backend = found.get();
      json.addProperty("registration", backend.registration().wire);
      InstanceRef ref = backend.instance();
      JsonObject instance = new JsonObject();
      instance.addProperty("adapter_id", ref.adapterId());
      instance.addProperty("environment_id", ref.environmentId());
      instance.addProperty("resource_kind", ref.resourceKind());
      instance.addProperty("external_id", ref.externalId());
      json.add("instance_ref", instance);
      json.addProperty("address", backend.address().toString());
      json.addProperty("routable", backends.routable(backend));
      json.addProperty("active_sessions", proxy.connectedPlayers(id));
    }
    json.addProperty("observed_at", clock.instant().toString());
    return json;
  }

  private static JsonObject outcome(String outcome, String reason) {
    JsonObject json = new JsonObject();
    json.addProperty("outcome", outcome);
    if (reason != null) {
      json.addProperty("reason", reason);
    }
    return json;
  }

  static InstanceRef parseInstanceRef(JsonObject json) {
    try {
      return new InstanceRef(
          json.get("adapter_id").getAsString(),
          json.get("environment_id").getAsString(),
          json.get("resource_kind").getAsString(),
          json.get("external_id").getAsString());
    } catch (RuntimeException e) {
      throw new BridgeException(400, "invalid_instance_ref", "instance_ref must name adapter_id, environment_id, resource_kind, and external_id.");
    }
  }

  private static void requireBackendId(String id) {
    if (!BackendRegistry.validId(id)) {
      throw new BridgeException(400, "invalid_backend_id", "Backend ID is not a valid server name.");
    }
  }

  private static void requireSessionId(String id) {
    if (id == null || !SESSION_ID.matcher(id).matches()) {
      // Unknown formats cannot belong to this bridge.
      throw new BridgeException(404, "session_not_found", "Session not found.");
    }
  }
}
