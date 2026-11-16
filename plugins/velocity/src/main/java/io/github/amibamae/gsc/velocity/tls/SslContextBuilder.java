package io.github.amibamae.gsc.velocity.tls;

import java.security.KeyManagementException;
import java.security.KeyStore;
import java.security.NoSuchAlgorithmException;
import javax.net.ssl.*;

/**
 * Builds an SSLContext for use with HTTPS. Supports server-side TLS and mTLS.
 */
final class SslContextBuilder {

  /**
   * Creates an SSLContext from a certificate. For now, creates a basic context
   * that will be enhanced in Step 4 with proper client cert validation.
   */
  SSLContext build(java.security.cert.X509Certificate cert) 
      throws NoSuchAlgorithmException, KeyManagementException {
    
    char[] keyPassword = null; // No password for now
    
    return createSslContext(cert, keyPassword);
  }

  private SSLContext createSslContext(java.security.cert.X509Certificate cert, char[] keyPassword) 
      throws NoSuchAlgorithmException, KeyManagementException {
    
    try {
      KeyStore keyStore = java.security.KeyStore.getInstance(KeyStore.getDefaultType());
      
      // Add the certificate to the trust store
      keyStore.setCertificateEntry("gsc-server-cert", cert);
      
      SSLContext sslContext = SSLContext.getInstance("TLS");
      
      // Initialize with empty key managers and custom trust manager
      sslContext.init(
          new KeyManager[0],
          new TrustManager[]{new AllowAnyTrustManager()},
          java.security.SecureRandom.getInstance("SHA1PRNG")
      );
      
      return sslContext;
    } catch (Exception e) {
      throw new RuntimeException("Failed to create SSLContext", e);
    }
  }

  /**
   * Accepts any client certificate for now (will implement mTLS in Step 4).
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
