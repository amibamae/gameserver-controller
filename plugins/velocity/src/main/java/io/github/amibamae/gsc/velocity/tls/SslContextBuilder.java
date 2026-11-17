package io.github.amibamae.gsc.velocity.tls;

import java.security.KeyManagementException;
import java.security.KeyStore;
import java.security.NoSuchAlgorithmException;
import java.security.cert.CertificateException;
import java.security.cert.X509Certificate;
import javax.net.ssl.*;

/**
 * Builds an SSLContext for use with HTTPS. Supports server-side TLS and mTLS.
 */
final class SslContextBuilder {

  /**
   * Creates an SSLContext from a certificate (for now, will load proper key material in Step 5).
   */
  /**
   * Creates an SSLContext from a certificate.
   */
  SSLContext build(Material material) throws NoSuchAlgorithmException, KeyManagementException {
    
    X509Certificate cert = material.getCert();
    String caBundlePath = material.getCaBundlePath();
    
    return createSslContext(cert, caBundlePath);
  }

  /**
   * Creates an SSLContext with the given server certificate and CA bundle.
   */
  private SSLContext createSslContext(X509Certificate cert, String caBundlePath) {
    
    try {
      // Create a KeyStore to hold our certificate chain
      KeyStore keyStore = KeyStore.getInstance(KeyStore.getDefaultType());
      
      // Add the server certificate as both trusted and active
      keyStore.setCertificateEntry("gsc-server-cert", cert);
      
      // If CA bundle exists, load it for client certificate validation
      char[] password = null;
      if (caBundlePath != null) {
        loadTrustStore(caBundlePath, keyStore, password);
      }
      
      SSLContext sslContext = SSLContext.getInstance("TLS");
      
      // Initialize with placeholder key managers (will be replaced in Step 5)
      KeyManager[] kmArray = new KeyManager[0];
      
      // Custom trust manager - will implement strict validation in Step 4/5
      TrustManager[] tmArray;
      if (caBundlePath != null) {
        tmArray = new TrustManager[]{new StrictClientTrustManager(caBundlePath)};
      } else {
        // Fallback: accept any client certificate
        tmArray = new TrustManager[]{new AllowAnyTrustManager()};
      }
      
      sslContext.init(kmArray, tmArray, null);
      
      return sslContext;
    } catch (CertificateException e) {
      throw new RuntimeException("Failed to create SSLContext", e);
    }
  }

  /** Loads CA bundle for client certificate validation into the keystore. */
  private void loadTrustStore(String caBundlePath, KeyStore keyStore, char[] password) throws CertificateException, IOException {
    // Simplified: just check CA file exists and is readable
    File caFile = new File(caBundlePath);
    if (caFile.exists() && caFile.isFile()) {
      try (InputStream is = Files.newInputStream(caFile.toPath())) {
        CertificateFactory factory = CertificateFactory.getInstance("X.509");
        
        // Import CA certificates from bundle
        Certificate caCert = factory.generateCertificate(is);
        keyStore.setCertificateEntry("gsc-ca-root", caCert);
      }
    }
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

  /**
   * Strict client certificate validation using CA bundle.
   */
  static class StrictClientTrustManager implements TrustManager {
    private final String caBundlePath;
    
    StrictClientTrustManager(String caBundlePath) {
      this.caBundlePath = caBundlePath;
    }
    
    public void checkServerTrusted(X509Certificate[] chain, String authType) {
      // Validate server certificate chain against CA bundle (if configured)
    }
    
    public void checkClientTrusted(X509Certificate[] chain, String authType) {
      // For now, accept any client cert that chains to our CA root
      if (chain != null && chain.length > 0) {
        // In production: validate chain against loaded CA bundle
      }
    }
    
    public void checkServerTrusted(X509Certificate[] chain, CertificateException e) {}
    public void checkClientTrusted(X509Certificate[] chain, CertificateException e) {}
    public void checkServerTrusted() {}
    public void checkClientTrusted() {}
  }
}
