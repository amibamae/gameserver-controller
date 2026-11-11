package io.github.amibamae.gsc.velocity;

import com.google.gson.JsonArray;
import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import io.github.amibamae.gsc.velocity.BackendRegistry.Backend;
import io.github.amibamae.gsc.velocity.BackendRegistry.Registration;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.AtomicMoveNotSupportedException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

/**
 * Saves managed backend registrations so they survive a proxy restart.
 * Restored backends start unhealthy and become routable only after a health
 * check passes.
 */
final class BackendStore {
  private final Path file;

  BackendStore(Path file) {
    this.file = file;
  }

  List<Backend> load() throws IOException {
    List<Backend> result = new ArrayList<>();
    if (!Files.exists(file)) {
      return result;
    }
    JsonElement root = JsonParser.parseString(Files.readString(file, StandardCharsets.UTF_8));
    for (JsonElement element : root.getAsJsonObject().getAsJsonArray("backends")) {
      JsonObject json = element.getAsJsonObject();
      Registration registration = Registration.valueOf(json.get("registration").getAsString().toUpperCase(Locale.ROOT));
      if (registration == Registration.UNREGISTERED) {
        continue;
      }
      result.add(new Backend(
          json.get("backend_id").getAsString(),
          registration,
          BridgeService.parseInstanceRef(json.getAsJsonObject("instance_ref")),
          BackendAddress.parse(json.get("address").getAsString()),
          false,
          null));
    }
    return result;
  }

  void save(List<Backend> backends) throws IOException {
    JsonArray array = new JsonArray();
    for (Backend backend : backends) {
      JsonObject json = new JsonObject();
      json.addProperty("backend_id", backend.id());
      json.addProperty("registration", backend.registration().wire);
      JsonObject instance = new JsonObject();
      instance.addProperty("adapter_id", backend.instance().adapterId());
      instance.addProperty("environment_id", backend.instance().environmentId());
      instance.addProperty("resource_kind", backend.instance().resourceKind());
      instance.addProperty("external_id", backend.instance().externalId());
      json.add("instance_ref", instance);
      json.addProperty("address", backend.address().toString());
      array.add(json);
    }
    JsonObject root = new JsonObject();
    root.addProperty("version", 1);
    root.add("backends", array);
    Files.createDirectories(file.getParent());
    Path temporary = file.resolveSibling(file.getFileName() + ".tmp");
    Files.writeString(temporary, root.toString(), StandardCharsets.UTF_8);
    try {
      Files.move(temporary, file, StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE);
    } catch (AtomicMoveNotSupportedException e) {
      Files.move(temporary, file, StandardCopyOption.REPLACE_EXISTING);
    }
  }
}
