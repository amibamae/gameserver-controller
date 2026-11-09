package managementapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadTLSConfigRequiresPrivateKeyPermissionsAndTLS12(t *testing.T) {
	certificateFile, privateKeyFile := writeTestCertificate(t)
	if err := os.Chmod(privateKeyFile, 0o640); err != nil {
		t.Fatal(err)
	}
	config, err := LoadTLSConfig(certificateFile, privateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if config.MinVersion != 0x0303 || len(config.Certificates) != 1 {
		t.Fatalf("TLS config = %#v, want TLS 1.2 minimum and one certificate", config)
	}
	if err := os.Chmod(privateKeyFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTLSConfig(certificateFile, privateKeyFile); err == nil {
		t.Fatal("accepted a private key readable by other users")
	}
}

func TestLoadTLSConfigRejectsSamePathAndNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadTLSConfig(directory, directory); err == nil {
		t.Fatal("accepted identical certificate and key paths")
	}
	if _, err := LoadTLSConfig(directory, filepath.Join(directory, "missing.key")); err == nil {
		t.Fatal("accepted a directory as a certificate")
	}
}

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificateFile := filepath.Join(directory, "tls.crt")
	privateKeyFile := filepath.Join(directory, "tls.key")
	if err := os.WriteFile(certificateFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certificateFile, privateKeyFile
}
