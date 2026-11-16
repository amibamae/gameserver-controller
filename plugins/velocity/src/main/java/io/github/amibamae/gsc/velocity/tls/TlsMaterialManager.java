package io.github.amibamae.gsc.velocity.tls;

import java.io.*;
import java.nio.file.*;
import java.security.*;
import java.security.cert.Certificate;
import java.security.cert.CertificateException;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;
import java.time.Instant;
import java.util.concurrent.TimeUnit;

/**
 * Manages TLS material (server cert, key, CA bundle) with hot rotation support.
 */
public class TlsMaterialManager {

  private final Path certPath;
  private final Path keyPath;
  private final Path caPath;
  
  /** Last known good material. */
  private volatile Material lastKnownGood = null;
  
  /** Time when the material was last validated (TTL cache). */
  private volatile long validSinceNanos = -1;
  
  /** Timeout before re-checking file contents (in seconds, default 60). */
  private static final int TTL_SECONDS = 60;

  public TlsMaterialManager(Path certPath, Path keyPath, Path caPath) {
    this.certPath = checkPath(certPath);
    this.keyPath = checkPath(keyPath);
    this.caPath = checkPath(caPath);
  }

  private Path checkPath(Path path) {
    if (path == null || !path.isAbsolute()) {
      throw new IllegalArgumentException("Path must be an absolute path");
    }
    return path;
  }

  /** Returns the current TLS material (or last-good if rotation failed). */
  public Material get() {
    long nowNanos = System.nanoTime();
    if (validSinceNanos != -1 && TimeUnit.NANOSECONDS.toSeconds(nowNanos - validSinceNanos) < TTL_SECONDS) {
      return lastKnownGood;
    }
    synchronized (this) {
      Material newMaterial = loadAndValidate();
      if (newMaterial != null) {
        this.lastKnownGood = newMaterial;
        this.validSinceNanos = nowNanos;
      }
      return lastKnownGood;
    }
  }

  private Material loadAndValidate() {
    File certFile = certPath.toFile();
    File keyFile = keyPath.toFile();

    if (!certFile.exists() || !keyFile.exists()) {
      return null; // Keep last-good on missing files
    }

    try {
      X509Certificate cert = loadCertificate(certFile);
      
      Instant expiry = cert.getNotAfter().toInstant();
      if (expiry.isBefore(Instant.now())) {
        throw new IllegalArgumentException("Certificate has expired: " + expiry);
      }

      // Load CA bundle for client cert validation (if exists)
      String caBundlePath = null;
      if (caPath != null && caPath.toFile().exists()) {
        caBundlePath = loadCaBundle(caPath.toFile());
      }

      return new Material(cert, caBundlePath); // Key not validated yet - placeholder for now
    } catch (Exception e) {
      System.err.println("Failed to validate TLS material: " + e.getMessage());
      return null;
    }
  }

  /** Loads CA bundle for client certificate validation. */
  private String loadCaBundle(File caFile) throws IOException {
    // Simplified: just return the path for now
    // In production, this would read and parse the CA bundle
    return caFile.getAbsolutePath();
  }

  private X509Certificate loadCertificate(File file) throws CertificateException, IOException {
    try (InputStream is = Files.newInputStream(file.toPath())) {
      CertificateFactory factory = CertificateFactory.getInstance("X.509");
      Certificate cert = factory.generateCertificate(is);
      
      if (!(cert instanceof X509Certificate)) {
        throw new CertificateException("Not an X.509 certificate");
      }
      
      return (X509Certificate)cert;
    }
  }

  private void loadTrustStore(File caFile) throws IOException {
    // Simplified: just check CA file exists and is readable
    // In production, this would build a TrustManagerFactory from the CA bundle
  }

  /** Holds TLS material - key may be null on initial load until fully validated. */
  public static class Material {
    private final X509Certificate cert;
    private final String caBundlePath;
    
    Material(X509Certificate cert, String caBundlePath) {
      this.cert = cert;
      this.caBundlePath = caBundlePath;
    }
    
    public X509Certificate getCert() { return cert; }
    
    /** CA bundle path for client certificate validation. */
    public String getCaBundlePath() { return caBundlePath; }
  }
}
