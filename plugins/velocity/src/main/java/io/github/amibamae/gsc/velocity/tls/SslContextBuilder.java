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
import java.security.cert.Certificate;
import java.security.cert.CertificateException;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;
import java.security.spec.PKCS8EncodedKeySpec;
import java.util.Base64;
import javax.net.ssl.KeyManagerFactory;
import javax.net.ssl.SSLContext;
import javax.net.ssl.TrustManagerFactory;

/** Loads the bridge's server identity and the CA bundle for client authentication. */
public final class SslContextBuilder {
  private SslContextBuilder() {}

  public static SSLContext build(Path certificate, Path privateKey, Path clientCas) throws IOException {
    try {
      Certificate[] chain = readCertificates(certificate);
      X509Certificate leaf = (X509Certificate) chain[0];
      leaf.checkValidity();
      PrivateKey key = readPrivateKey(privateKey, leaf.getPublicKey().getAlgorithm());
      verifyKeyPair(leaf, key);

      SSLContext context = SSLContext.getInstance("TLS");
      context.init(serverKeys(chain, key).getKeyManagers(), clientTrust(clientCas).getTrustManagers(), null);
      return context;
    } catch (GeneralSecurityException | IllegalArgumentException e) {
      throw new IOException("Invalid Velocity bridge TLS material", e);
    }
  }

  private static Certificate[] readCertificates(Path path) throws IOException, CertificateException {
    try (InputStream input = Files.newInputStream(path)) {
      Certificate[] certificates = CertificateFactory.getInstance("X.509")
          .generateCertificates(input).toArray(Certificate[]::new);
      if (certificates.length == 0) {
        throw new CertificateException("Certificate file is empty");
      }
      return certificates;
    }
  }

  private static PrivateKey readPrivateKey(Path path, String algorithm)
      throws IOException, GeneralSecurityException {
    String pem = Files.readString(path).strip();
    String header = "-----BEGIN PRIVATE KEY-----";
    String footer = "-----END PRIVATE KEY-----";
    if (!pem.startsWith(header) || !pem.endsWith(footer)) {
      throw new IOException("Velocity bridge key must be an unencrypted PKCS#8 PEM private key");
    }
    String body = pem.substring(header.length(), pem.length() - footer.length()).replaceAll("\\s", "");
    return KeyFactory.getInstance(algorithm).generatePrivate(new PKCS8EncodedKeySpec(Base64.getDecoder().decode(body)));
  }

  private static void verifyKeyPair(X509Certificate certificate, PrivateKey key) throws GeneralSecurityException {
    String algorithm = switch (key.getAlgorithm()) {
      case "RSA" -> "SHA256withRSA";
      case "EC" -> "SHA256withECDSA";
      case "EdDSA" -> "EdDSA";
      default -> throw new GeneralSecurityException("Unsupported Velocity bridge private key algorithm");
    };
    Signature signature = Signature.getInstance(algorithm);
    byte[] challenge = "Velocity bridge key verification".getBytes(StandardCharsets.US_ASCII);
    signature.initSign(key);
    signature.update(challenge);
    byte[] proof = signature.sign();
    signature.initVerify(certificate);
    signature.update(challenge);
    if (!signature.verify(proof)) {
      throw new GeneralSecurityException("Velocity bridge private key does not match the certificate");
    }
  }

  private static KeyManagerFactory serverKeys(Certificate[] chain, PrivateKey key)
      throws GeneralSecurityException, IOException {
    KeyStore store = KeyStore.getInstance("PKCS12");
    store.load(null, null);
    char[] password = new char[0];
    store.setKeyEntry("bridge", key, password, chain);
    KeyManagerFactory keys = KeyManagerFactory.getInstance(KeyManagerFactory.getDefaultAlgorithm());
    keys.init(store, password);
    return keys;
  }

  private static TrustManagerFactory clientTrust(Path clientCas) throws GeneralSecurityException, IOException {
    KeyStore store = KeyStore.getInstance("PKCS12");
    store.load(null, null);
    Certificate[] certificates = readCertificates(clientCas);
    for (int i = 0; i < certificates.length; i++) {
      store.setCertificateEntry("controller-ca-" + i, certificates[i]);
    }
    TrustManagerFactory trust = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm());
    trust.init(store);
    return trust;
  }
}
