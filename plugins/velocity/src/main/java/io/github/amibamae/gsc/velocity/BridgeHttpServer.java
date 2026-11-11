package io.github.amibamae.gsc.velocity;

import com.google.gson.JsonElement;
import com.google.gson.JsonObject;
import com.google.gson.JsonParser;
import com.google.gson.JsonSyntaxException;
import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;
import org.slf4j.Logger;

/**
 * Serves the proxy bridge API on a loopback address. Requests must carry the
 * configured bearer token. Paths, bodies, and session IDs are never logged.
 */
final class BridgeHttpServer {
  private static final int MAX_BODY_BYTES = 64 * 1024;

  private final BridgeService service;
  private final byte[] token;
  private final Logger logger;
  private HttpServer server;
  private ExecutorService executor;

  BridgeHttpServer(BridgeService service, byte[] token, Logger logger) {
    if (token.length < 16) {
      throw new IllegalArgumentException("bridge token must be at least 16 bytes");
    }
    this.service = service;
    this.token = ("Bearer " + new String(token, StandardCharsets.UTF_8)).getBytes(StandardCharsets.UTF_8);
    this.logger = logger;
  }

  /** Starts listening. The address must be a loopback address. */
  synchronized InetSocketAddress start(InetSocketAddress address) throws IOException {
    if (address.getAddress() == null || !address.getAddress().isLoopbackAddress()) {
      throw new IllegalArgumentException("bridge must bind to a loopback address");
    }
    server = HttpServer.create(address, 16);
    executor = Executors.newFixedThreadPool(4, runnable -> {
      Thread thread = new Thread(runnable, "gsc-bridge-http");
      thread.setDaemon(true);
      return thread;
    });
    server.setExecutor(executor);
    server.createContext("/v1/", this::handle);
    server.start();
    return server.getAddress();
  }

  synchronized void stop() {
    if (server != null) {
      server.stop(1);
      executor.shutdown();
      try {
        executor.awaitTermination(5, TimeUnit.SECONDS);
      } catch (InterruptedException e) {
        Thread.currentThread().interrupt();
      }
      server = null;
    }
  }

  private void handle(HttpExchange exchange) throws IOException {
    try (exchange) {
      int status = 200;
      JsonObject body;
      try {
        authorize(exchange);
        body = route(exchange.getRequestMethod(), exchange.getRequestURI().getRawPath(), readBody(exchange));
      } catch (BridgeException e) {
        status = e.status;
        body = error(e.code, e.getMessage());
      } catch (RuntimeException e) {
        // Do not include the exception message: it may contain request data.
        logger.warn("Bridge request failed with {}", e.getClass().getSimpleName());
        status = 500;
        body = error("internal_error", "The bridge could not complete the request.");
      }
      byte[] bytes = body.toString().getBytes(StandardCharsets.UTF_8);
      exchange.getResponseHeaders().set("Content-Type", "application/json");
      exchange.getResponseHeaders().set("Cache-Control", "no-store");
      exchange.sendResponseHeaders(status, bytes.length);
      try (OutputStream output = exchange.getResponseBody()) {
        output.write(bytes);
      }
    }
  }

  private void authorize(HttpExchange exchange) {
    String header = exchange.getRequestHeaders().getFirst("Authorization");
    byte[] supplied = header == null ? new byte[0] : header.getBytes(StandardCharsets.UTF_8);
    if (!MessageDigest.isEqual(supplied, token)) {
      throw new BridgeException(401, "unauthorized", "A valid bearer token is required.");
    }
  }

  JsonObject route(String method, String path, JsonObject body) {
    String[] parts = path.split("/", -1);
    // parts[0] is empty and parts[1] is "v1".
    if (parts.length < 3 || !parts[1].equals("v1")) {
      throw notFound();
    }
    switch (parts[2]) {
      case "info":
        requireShape(parts, 3, method, "GET");
        return service.info();
      case "backends":
        if (parts.length == 4) {
          return switch (method) {
            case "GET" -> service.backend(parts[3]);
            case "PUT" -> service.register(parts[3], body);
            case "DELETE" -> service.unregister(parts[3]);
            default -> throw methodNotAllowed();
          };
        }
        if (parts.length == 5 && parts[4].equals("drain")) {
          requireShape(parts, 5, method, "POST");
          return service.drain(parts[3]);
        }
        throw notFound();
      case "sessions":
        if (parts.length == 4) {
          requireShape(parts, 4, method, "GET");
          return service.session(parts[3]);
        }
        if (parts.length == 5 && parts[4].equals("transfer")) {
          requireShape(parts, 5, method, "POST");
          return service.transfer(parts[3], body);
        }
        throw notFound();
      case "cohorts":
        requireShape(parts, 4, method, "GET");
        return service.cohort(parts[3]);
      default:
        throw notFound();
    }
  }

  private static void requireShape(String[] parts, int length, String method, String expected) {
    if (parts.length != length) {
      throw notFound();
    }
    if (!method.equals(expected)) {
      throw methodNotAllowed();
    }
  }

  private static JsonObject readBody(HttpExchange exchange) throws IOException {
    try (InputStream input = exchange.getRequestBody()) {
      byte[] bytes = input.readNBytes(MAX_BODY_BYTES + 1);
      if (bytes.length > MAX_BODY_BYTES) {
        throw new BridgeException(413, "request_too_large", "Request body is too large.");
      }
      if (bytes.length == 0) {
        return null;
      }
      try {
        JsonElement element = JsonParser.parseString(new String(bytes, StandardCharsets.UTF_8));
        if (!element.isJsonObject()) {
          throw new BridgeException(400, "invalid_json", "Request body must be a JSON object.");
        }
        return element.getAsJsonObject();
      } catch (JsonSyntaxException e) {
        throw new BridgeException(400, "invalid_json", "Request body must be a JSON object.");
      }
    }
  }

  private static JsonObject error(String code, String message) {
    JsonObject detail = new JsonObject();
    detail.addProperty("code", code);
    detail.addProperty("message", message);
    JsonObject body = new JsonObject();
    body.add("error", detail);
    return body;
  }

  private static BridgeException notFound() {
    return new BridgeException(404, "not_found", "Unknown endpoint.");
  }

  private static BridgeException methodNotAllowed() {
    return new BridgeException(405, "method_not_allowed", "Method not allowed.");
  }
}
