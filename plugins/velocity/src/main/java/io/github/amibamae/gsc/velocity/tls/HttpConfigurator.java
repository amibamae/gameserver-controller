package io.github.amibamae.gsc.velocity.tls;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.security.KeyManagementException;
import java.security.NoSuchAlgorithmException;
import java.security.cert.X509Certificate;
import javax.net.ssl.*;

/**
 * Wraps an HttpServer with HTTPS using TLS material from TlsMaterialManager.
 */
final class HttpConfigurator {

  private final SSLContext sslContext;
  
  HttpConfigurator(SSLContext context) {
    this.sslContext = context;
  }

  /**
   * Creates an HTTP/HTTPS server configured with the given TLS material.
   */
  static HttpConfigurator fromTlsMaterial(Material material, boolean tlsEnabled) {
    if (!tlsEnabled) {
      return null;
    }

    try {
      SSLContext context = SSLContext.getInstance("TLS");
      // Initialize with key managers and trust managers for mTLS
      context.init(getKeyManagers(material), getTrustManagers(), null);
      return new HttpConfigurator(context);
    } catch (NoSuchAlgorithmException | KeyManagementException e) {
      throw new RuntimeException("Failed to create HTTPS server", e);
    }
  }

  /**
   * Creates key managers from the TLS material.
   */
  static javax.net.ssl.KeyManager[] getKeyManagers(Material material) throws KeyStoreException, 
      NoSuchAlgorithmException, CertificateException {
    
    // For now, return empty array (no server private key loaded yet)
    // In Step 5, we'll load the actual private key from TlsMaterialManager
    return new KeyManager[0];
  }

  /**
   * Creates trust managers for client certificate validation.
   */
  static java.security.cert.TrustAnchor[] getTrustManagers() {
    // Return empty array - use default system trust managers
    // Or return anchors from CA bundle if strict mTLS enabled
    return new java.security.cert.TrustAnchor[0];
  }

  /**
   * Starts an HTTP or HTTPS server. If TLS is configured, uses HttpsServer logic.
   */
  synchronized InetSocketAddress start(HttpServer server, int port) {
    if (sslContext == null) {
      // Plain HTTP - no TLS wrapping needed
      return startPlainHttp(server);
    }

    // For TLS: create SSL server socket
    try {
      SSLServerSocketFactory ssf = sslContext.getServerSocketFactory();
      
      // Create and bind the SSL server socket
      SSLServerSocket sss = (SSLServerSocket) ssf.createServerSocket(0);
      sss.setNeedClientAuth(false); // Enable mTLS in Step 4
      
      return startSslServer(server, port, sss.getLocalPort());
    } catch (IOException e) {
      throw new RuntimeException("Failed to create SSL server socket", e);
    }
  }

  /**
   * Starts a plain HTTP server.
   */
  private synchronized InetSocketAddress startPlainHttp(HttpServer server) {
    server.setExecutor(java.util.concurrent.Executors.newFixedThreadPool(4));
    server.createContext("/v1/", this::handle);
    server.start();
    return server.getAddress();
  }

  /**
   * Starts an HTTPS server using the SSL socket.
   */
  private synchronized InetSocketAddress startSslServer(HttpServer server, int port, int sslPort) {
    try {
      server.setExecutor(Executors.newFixedThreadPool(4));
      server.createContext("/v1/", this::handle);
      HttpsServer https = HttpsServer.getInstance(server.getHttpConfig());
      https.setHttpsListener(server.getChannel(), InetSocketAddress.createUnresolved("0.0.0.0", port), sslContext);
      https.start();
      return new InetSocketAddress(https.getAddress().getAddress(), https.getAddress().getPort());
    } catch (IOException e) {
      throw new RuntimeException("Failed to start HTTPS server", e);
    }
  }

  /**
   * Handles HTTP requests.
   */
  @SuppressWarnings("unchecked")
  private JsonObject handle(HttpExchange exchange) throws IOException {
    // Check authentication and path validation - skip for now, return empty object
    byte[] body = exchange.getRequestBody();
    if (body.length > MAX_BODY_BYTES) {
      throw new IOException("Request too large");
    }

    String path = extractPath(exchange.getRequestURI().toString());
    JsonObject result;

    // Handle the request
    result = new JsonObject();

    byte[] responseBody = result.toString().getBytes(java.nio.charset.StandardCharsets.UTF_8);
    exchange.getResponseHeaders().add("Content-Length", String.valueOf(responseBody.length));
    exchange.sendResponseHeaders(200, responseBody.length);
    try (OutputStream os = exchange.getResponseBody()) {
      os.write(responseBody);
    }
    return result;
  }

  /**
   * Returns the request path without query parameters.
   */
  static String extractPath(String uri) {
    int q = uri.indexOf('?');
    return q == -1 ? uri : uri.substring(0, q);
  }

  // The maximum size of a request body that we'll accept.
  private static final int MAX_BODY_BYTES = 64 * 1024;

}
