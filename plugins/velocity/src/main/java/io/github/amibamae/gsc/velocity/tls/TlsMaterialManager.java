package io.github.amibamae.gsc.velocity.tls;

import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.GeneralSecurityException;
import java.security.KeyFactory;
import java.security.KeyStore;
import java.security.PrivateKey;
import java.security.Signature;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;
import java.security.spec.PKCS8EncodedKeySpec;
import java.util.Arrays;
import java.util.Base64;
import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManagerFactory;

/** Loads the server identity and explicit client trust anchors at startup. */
public final class TlsMaterialManager {
  private static final String KEY_BEGIN = "-----BEGIN PRIVATE KEY-----";
  private static final String KEY_END = "-----END PRIVATE KEY-----";

  private TlsMaterialManager() {}

  public static SSLContext load(Path certFile, Path keyFile, Path controllerCaFile) throws IOException {
    try {
      KeyManagerFactory keys = serverKeys(certFile, keyFile);
      TrustManagerFactory trust = clientTrust(controllerCaFile);
      SSLContext context = SSLContext.getInstance("TLS");
      context.init(keys.getKeyManagers(), trust.getTrustManagers(), null);
      return context;
    } catch (GeneralSecurityException | IllegalArgumentException | IOException e) {
      throw new IOException("Could not load Velocity bridge TLS material", e);
    }
  }

  private static KeyManagerFactory serverKeys(Path certFile, Path keyFile)
      throws GeneralSecurityException, IOException {
    X509Certificate[] chain = certificates(certFile);
    for (X509Certificate certificate : chain) {
      certificate.checkValidity();
    }
    X509Certificate leaf = chain[0];
    PrivateKey key = privateKey(keyFile, leaf.getPublicKey().getAlgorithm());
    verifyKeyPair(key, leaf);

    char[] password = new char[0];
    KeyStore identity = KeyStore.getInstance("PKCS12");
    identity.load(null, password);
    identity.setKeyEntry("bridge", key, password, chain);
    KeyManagerFactory keys = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
    keys.init(identity, password);
    return keys;
  }

  private static TrustManagerFactory clientTrust(Path caFile) throws GeneralSecurityException, IOException {
    KeyStore roots = KeyStore.getInstance("PKCS12");
    roots.load(null, null);
    int index = 0;
    for (X509Certificate root : certificates(caFile)) {
      if (root.getBasicConstraints() < 0) {
        throw new GeneralSecurityException("Controller trust bundle contains a non-CA certificate");
      }
      roots.setCertificateEntry("controller-ca-" + index++, root);
    }
    TrustManagerFactory trust = TrustManagerFactory.getInstance("PKIX");
    trust.init(roots);
    return trust;
  }

  private static X509Certificate[] certificates(Path file) throws GeneralSecurityException, IOException {
    try (InputStream input = Files.newInputStream(file)) {
      X509Certificate[] certificates = CertificateFactory.getInstance("X.509").generateCertificates(input)
          .toArray(X509Certificate[]::new);
      if (certificates.length == 0) {
        throw new GeneralSecurityException("TLS certificate file contains no certificates");
      }
      return certificates;
    }
  }

  private static PrivateKey privateKey(Path file, String algorithm) throws GeneralSecurityException, IOException {
    String pem = Files.readString(file, StandardCharsets.US_ASCII).strip();
    if (!pem.startsWith(KEY_BEGIN) || !pem.endsWith(KEY_END)) {
      throw new GeneralSecurityException("TLS private key must use unencrypted PKCS#8 PEM");
    }
    String payload = pem.substring(KEY_BEGIN.length(), pem.length() - KEY_END.length());
    byte[] der = Base64.getDecoder().decode(payload.replaceAll("\\s", ""));
    try {
      return KeyFactory.getInstance(algorithm).generatePrivate(new PKCS8EncodedKeySpec(der));
    } finally {
      Arrays.fill(der, (byte) 0);
    }
  }

  private static void verifyKeyPair(PrivateKey key, X509Certificate leaf) throws GeneralSecurityException {
    String algorithm = switch (key.getAlgorithm()) {
      case "RSA" -> "SHA256withRSA";
      case "EC" -> "SHA256withECDSA";
      case "EdDSA" -> "EdDSA";
      default -> throw new GeneralSecurityException("Unsupported TLS private key algorithm");
    };
    byte[] challenge = "gsc velocity bridge key check".getBytes(StandardCharsets.US_ASCII);
    Signature signature = Signature.getInstance(algorithm);
    signature.initSign(key);
    signature.update(challenge);
    byte[] proof = signature.sign();
    signature.initVerify(leaf);
    signature.update(challenge);
    if (!signature.verify(proof)) {
      throw new GeneralSecurityException("TLS private key does not match the server certificate");
    }
  }
}
