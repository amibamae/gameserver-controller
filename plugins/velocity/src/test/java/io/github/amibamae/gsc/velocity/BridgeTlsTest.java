package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;

import io.github.amibamae.gsc.velocity.tls.TlsMaterialManager;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.KeyStore;
import java.security.PrivateKey;
import java.time.Duration;
import java.time.Instant;
import java.util.Base64;
import java.util.Set;
import java.util.concurrent.TimeUnit;
import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManagerFactory;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;
import org.slf4j.helpers.NOPLogger;

class BridgeTlsTest {
  private static final String PASSWORD = "test-store-password";

  @Test
  void requiresATrustedClientCertificateAndBearerToken(@TempDir Path directory) throws Exception {
    KeyStore identity = identity(directory.resolve("server.p12"), "SAN=ip:127.0.0.1");
    KeyStore controller = identity(directory.resolve("controller.p12"), "BC=ca:true");
    KeyStore untrusted = identity(directory.resolve("untrusted.p12"), "BC=ca:true");
    Path certificate = pem(directory.resolve("server.crt"), "CERTIFICATE",
        identity.getCertificate("test").getEncoded());
    Path key = pem(directory.resolve("server.key"), "PRIVATE KEY",
        ((PrivateKey) identity.getKey("test", PASSWORD.toCharArray())).getEncoded());
    Path ca = pem(directory.resolve("controller-ca.crt"), "CERTIFICATE",
        controller.getCertificate("test").getEncoded());
    SSLContext context = TlsMaterialManager.load(certificate, key, ca);

    MutableClock clock = new MutableClock(Instant.EPOCH);
    BridgeService service = new BridgeService("test-proxy",
        new BackendRegistry(Set.of(), Duration.ofSeconds(15), clock),
        new SessionRegistry(clock), new BridgeHttpServerTest.FakeProxy(), clock, () -> {});
    String token = BridgeHttpServerTest.TOKEN;
    BridgeHttpServer bridge = new BridgeHttpServer(service, token.getBytes(StandardCharsets.UTF_8),
        true, NOPLogger.NOP_LOGGER, context);
    try {
      InetSocketAddress address = bridge.start(new InetSocketAddress("127.0.0.1", 0));
      URI info = URI.create("https://127.0.0.1:" + address.getPort() + "/v1/info");
      HttpRequest authorized = HttpRequest.newBuilder(info).timeout(Duration.ofSeconds(5))
          .header("Authorization", "Bearer " + token).build();
      assertThrows(IOException.class, () -> request(clientContext(identity, null), authorized));
      assertThrows(IOException.class, () -> request(clientContext(identity, untrusted), authorized));
      assertEquals(200, request(clientContext(identity, controller), authorized));
      assertEquals(401, request(clientContext(identity, controller),
          HttpRequest.newBuilder(info).timeout(Duration.ofSeconds(5)).build()));
    } finally {
      bridge.stop();
    }
  }

  @Test
  void rejectsMissingTlsMaterial(@TempDir Path directory) {
    Path missing = directory.resolve("missing");
    IOException error = assertThrows(IOException.class,
        () -> TlsMaterialManager.load(missing, missing, missing));
    assertEquals("Could not load Velocity bridge TLS material", error.getMessage());
  }

  private static KeyStore identity(Path file, String extension) throws Exception {
    Path bin = Path.of(System.getProperty("java.home"), "bin");
    Path keytool = Files.isRegularFile(bin.resolve("keytool.exe"))
        ? bin.resolve("keytool.exe") : bin.resolve("keytool");
    Process process = new ProcessBuilder(keytool.toString(), "-genkeypair", "-alias", "test",
        "-keyalg", "EC", "-dname", "CN=" + file.getFileName(), "-validity", "1", "-ext", extension,
        "-storetype", "PKCS12", "-keystore", file.toString(), "-storepass", PASSWORD)
        .redirectErrorStream(true).start();
    if (!process.waitFor(30, TimeUnit.SECONDS)) {
      process.destroyForcibly();
      throw new IOException("Timed out generating the test TLS identity");
    }
    if (process.exitValue() != 0) {
      throw new IOException(new String(process.getInputStream().readAllBytes(), StandardCharsets.UTF_8));
    }
    return KeyStore.getInstance(file.toFile(), PASSWORD.toCharArray());
  }

  private static Path pem(Path path, String type, byte[] contents) throws IOException {
    String content = Base64.getMimeEncoder().encodeToString(contents);
    return Files.writeString(path, "-----BEGIN " + type + "-----\n" + content
        + "\n-----END " + type + "-----\n", StandardCharsets.US_ASCII);
  }

  private static SSLContext clientContext(KeyStore server, KeyStore client) throws Exception {
    KeyStore roots = KeyStore.getInstance("PKCS12");
    roots.load(null, null);
    roots.setCertificateEntry("server", server.getCertificate("test"));
    TrustManagerFactory trust = TrustManagerFactory.getInstance("PKIX");
    trust.init(roots);
    KeyManagerFactory keys = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
    keys.init(client, client == null ? null : PASSWORD.toCharArray());
    SSLContext context = SSLContext.getInstance("TLS");
    context.init(keys.getKeyManagers(), trust.getTrustManagers(), null);
    return context;
  }

  private static int request(SSLContext context, HttpRequest request) throws Exception {
    HttpClient client = HttpClient.newBuilder().sslContext(context).proxy(HttpClient.Builder.NO_PROXY)
        .connectTimeout(Duration.ofSeconds(5)).build();
    return client.send(request, HttpResponse.BodyHandlers.discarding()).statusCode();
  }
}
