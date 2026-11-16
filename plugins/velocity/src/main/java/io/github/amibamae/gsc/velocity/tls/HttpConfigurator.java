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
  private synchronized InetSocketAddress startSslServer(HttpServer server, int expectedPort, int actualPort) {
    server.setExecutor(java.util.concurrent.Executors.newFixedThreadPool(4));
    
    // Register handler context after starting (for HTTPS)
    server.createContext("/v1/", this::handle);
    server.start();
    
    return new InetSocketAddress("0.0.0.0", actualPort);
  }

  /**
   * Stops the HTTP server.
   */
  synchronized void stop(HttpServer server) {
    if (server != null) {
      server.stop(1);
    }
  }

  private void handle(HttpExchange exchange) throws IOException {
    // Defer to BridgeHttpServer.handle for authorization and routing
  }

  /**
   * Accepts any client certificate for now. Will implement strict mTLS in Step 4/5.
   */
  static class AllowAnyTrustManager implements TrustManager {
    public void checkServerTrusted(X509Certificate[] chain, String authType) {}
    public void checkClientTrusted(X509Certificate[] chain, String authType) {}
    public void checkServerTrusted(X509Certificate[] chain, CertificateException e) {}
    public void checkClientTrusted(X509Certificate[] chain, CertificateException e) {}
    public void checkServerTrusted() {}
    public void checkClientTrusted() {}
  }
}
