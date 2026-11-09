package managementapi

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
)

// LoadTLSConfig loads a server certificate for the management listener and
// rejects private keys that are group-writable or accessible to other users.
// TLS 1.2 is the minimum supported protocol version.
func LoadTLSConfig(certificateFile, privateKeyFile string) (*tls.Config, error) {
	if certificateFile == "" || privateKeyFile == "" || certificateFile == privateKeyFile {
		return nil, errors.New("distinct TLS certificate and private-key files are required")
	}
	for _, path := range []string{certificateFile, privateKeyFile} {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect TLS file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("TLS certificate and key must be regular files")
		}
	}
	keyInfo, err := os.Stat(privateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("inspect TLS private key: %w", err)
	}
	if keyInfo.Mode().Perm()&0o037 != 0 {
		return nil, errors.New("TLS private key permissions must deny group write/execute and all other-user access")
	}
	certificate, err := tls.LoadX509KeyPair(certificateFile, privateKeyFile)
	if err != nil {
		return nil, errors.New("load management TLS certificate and private key")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}, nil
}
