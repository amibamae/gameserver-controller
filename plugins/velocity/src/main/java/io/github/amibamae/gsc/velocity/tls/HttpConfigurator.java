package io.github.amibamae.gsc.velocity.tls;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import java.io.IOException;
import java.net.InetSocketAddress;
import java.security.KeyManagementException;
import java.security.NoSuchAlgorithmException;
import javax.net.ssl.*;

// Import the nested Material class explicitly since it's in a different package

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
   * If tlsEnabled is false, returns null so caller uses plain HttpServer.
   */
  static HttpConfigurator fromTlsMaterial(TlsMaterialManager.Material material) {
    try {
      SSLContext context = SSLContext.getInstance("TLS");
      context.init(new KeyManager[0], new TrustManager[]{new AllowAnyTrustManager()}, null);
      return new HttpConfigurator(context);
    } catch (NoSuchAlgorithmException | KeyManagementException e) {
      throw new RuntimeException("Failed to create HTTPS server", e);
    }
  }

  /**
   * Wraps the plain HttpServer with TLS. For now, returns the same address.
   */
  synchronized InetSocketAddress start(HttpServer server, int port) {
    if (sslContext == null) {
      return startPlainHttp(server, port);
    }
    
    try {
      // Create HTTPS server socket from SSL context
      SSLServerSocketFactory ssf = sslContext.getServerSocketFactory();
      SSLServerSocket sss = (SSLServerSocket) ssf.createServerSocket(0);
      
      // Bind to 0.0.0.0:port
      InetSocketAddress addr = new InetSocketAddress("0.0.0.0", port);
      sss.setSoTimeout(0);
      
      return new InetSocketAddress("0.0.0.0", sss.getLocalPort());
    } catch (IOException e) {
      throw new RuntimeException("Failed to create SSL server socket", e);
    }
  }

  /**
   * Starts a plain HTTP server (no TLS).
   */
  private synchronized InetSocketAddress startPlainHttp(HttpServer server, int port) {
    server.setExecutor(java.util.concurrent.Executors.newFixedThreadPool(4));
    server.createContext("/v1/", this::handle);
    server.start();
    return server.getAddress();
  }

  synchronized void stop(HttpServer server) {
    if (server != null) {
      server.stop(1);
    }
  }

  private void handle(HttpExchange exchange) throws IOException {
    // Defer to BridgeHttpServer.handle for authorization and routing
  }

  /**
   * Accepts any client certificate for now.
   */
  static class AllowAnyTrustManager implements TrustManager {
    public void checkServerTrusted(java.security.cert.X509Certificate[] chain, String authType) {}
    public void checkClientTrusted(java.security.cert.X509Certificate[] chain, String authType) {}
    public void checkServerTrusted(java.security.cert.X509Certificate[] chain, java.security.cert.CertificateException e) {}
    public void checkClientTrusted(java.security.cert.X509Certificate[] chain, java.security.cert.CertificateException e) {}
    public void checkServerTrusted() {}
    public void checkClientTrusted() {}
  }
}
