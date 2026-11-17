package io.github.amibamae.gsc.velocity;

import static org.junit.jupiter.api.Assertions.assertEquals;
import static org.junit.jupiter.api.Assertions.assertThrows;
import static org.junit.jupiter.api.Assertions.assertTrue;

import io.github.amibamae.gsc.velocity.tls.SslContextBuilder;
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
import java.time.Clock;
import java.time.Duration;
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
  private static final String TOKEN = BridgeHttpServerTest.TOKEN;
  private static final String STORE_PASSWORD = "fixture-password";

  @TempDir Path temporary;

  @Test
  void requiresBothTrustedClientCertificateAndBearerToken() throws Exception {
    Identity server = createIdentity("server");
    Identity controller = createIdentity("controller");
    assertThrows(IOException.class,
        () -> SslContextBuilder.build(server.certificate(), controller.key(), controller.certificate()));
    SSLContext tls = SslContextBuilder.build(server.certificate(), server.key(), controller.certificate());
    Clock clock = Clock.systemUTC();
    BridgeService service = new BridgeService("edge", new BackendRegistry(Set.of(), Duration.ofSeconds(15), clock),
        new SessionRegistry(clock), new BridgeHttpServerTest.FakeProxy(), clock, () -> {});
    BridgeHttpServer bridge = new BridgeHttpServer(service, TOKEN.getBytes(StandardCharsets.UTF_8), true,
        NOPLogger.NOP_LOGGER, tls);
    int port = bridge.start(new InetSocketAddress("127.0.0.1", 0)).getPort();
    try {
      HttpClient authenticated = client(server, controller);
      HttpClient anonymous = client(server, null);
      URI uri = URI.create("https://127.0.0.1:" + port + "/v1/info");
      var authorized = HttpRequest.newBuilder(uri).timeout(Duration.ofSeconds(5))
          .header("Authorization", "Bearer " + TOKEN).build();
      var noToken = HttpRequest.newBuilder(uri).timeout(Duration.ofSeconds(5)).build();
      assertEquals(200, authenticated.send(authorized, HttpResponse.BodyHandlers.discarding()).statusCode());
      assertEquals(401, authenticated.send(noToken, HttpResponse.BodyHandlers.discarding()).statusCode());
      assertThrows(IOException.class, () -> anonymous.send(authorized, HttpResponse.BodyHandlers.discarding()));
    } finally {
      bridge.stop();
    }
  }

  private Identity createIdentity(String name) throws Exception {
    Path storeFile = temporary.resolve(name + ".p12");
    Path log = temporary.resolve(name + ".log");
    Path keytool = Path.of(System.getProperty("java.home"), "bin", "keytool");
    Process process = new ProcessBuilder(keytool.toString(), "-genkeypair", "-alias", name, "-keyalg", "EC",
        "-dname", "CN=" + name, "-ext", "SAN=ip:127.0.0.1", "-validity", "1", "-storetype", "PKCS12",
        "-keystore", storeFile.toString(), "-storepass", STORE_PASSWORD)
        .redirectErrorStream(true).redirectOutput(log.toFile()).start();
    if (!process.waitFor(20, TimeUnit.SECONDS)) {
      process.destroyForcibly();
      throw new IOException("keytool timed out");
    }
    assertTrue(process.exitValue() == 0, () -> "keytool failed for " + name);
    KeyStore store = KeyStore.getInstance(storeFile.toFile(), STORE_PASSWORD.toCharArray());
    return new Identity(store,
        writePem(name + ".crt", "CERTIFICATE", store.getCertificate(name).getEncoded()),
        writePem(name + ".key", "PRIVATE KEY", store.getKey(name, STORE_PASSWORD.toCharArray()).getEncoded()));
  }

  private Path writePem(String file, String type, byte[] bytes) throws IOException {
    String body = Base64.getMimeEncoder(64, new byte[]{'\n'}).encodeToString(bytes);
    return Files.writeString(temporary.resolve(file), "-----BEGIN " + type + "-----\n" + body
        + "\n-----END " + type + "-----\n");
  }

  private static HttpClient client(Identity server, Identity controller) throws Exception {
    KeyStore trusted = KeyStore.getInstance("PKCS12");
    trusted.load(null, null);
    trusted.setCertificateEntry("server", server.store().getCertificate("server"));
    TrustManagerFactory trust = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
    trust.init(trusted);
    KeyManagerFactory keys = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
    keys.init(controller == null ? null : controller.store(), STORE_PASSWORD.toCharArray());
    SSLContext tls = SSLContext.getInstance("TLS");
    tls.init(keys.getKeyManagers(), trust.getTrustManagers(), null);
    return HttpClient.newBuilder().proxy(HttpClient.Builder.NO_PROXY).sslContext(tls)
        .version(HttpClient.Version.HTTP_1_1).connectTimeout(Duration.ofSeconds(5)).build();
  }

  private record Identity(KeyStore store, Path certificate, Path key) {}
}
