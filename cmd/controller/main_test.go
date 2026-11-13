package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestServeRunsOnlyConfiguredAuthenticatedHTTPSListener(t *testing.T) {
	value, configPath := servingConfig(t)
	issued := issueTestKey(t, value.Controller.StateStore.Path)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, value, configPath, io.Discard) }()
	var client *http.Client
	defer func() {
		if client != nil {
			client.CloseIdleConnections()
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("HTTPS server did not shut down")
		}
	}()

	client = testHTTPSClient(t, value.Controller.ManagementAPI.TLSCertificate)
	response := requestUntilResponse(t, client, value.Controller.ManagementAPI.ListenAddress, "")
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized || response.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unauthenticated HTTPS response: status=%d headers=%v", response.StatusCode, response.Header)
	}
	response = requestUntilResponse(t, client, value.Controller.ManagementAPI.ListenAddress, issued.Secret)
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("valid scoped key should reach operation lookup and get 404, got %d", response.StatusCode)
	}

	// Define a server over HTTPS; the runner takes the operation to succeeded.
	base := "https://" + value.Controller.ManagementAPI.ListenAddress
	definition := `{"operation_type":"server.define","parameters":{"server_id":"server-1","environment_id":"staging","display_name":"Server 1","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}}`
	request, err := http.NewRequest(http.MethodPost, base+"/v1/servers", strings.NewReader(definition))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+issued.Secret)
	request.Header.Set("Idempotency-Key", "define-server-1")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	location := response.Header.Get("Location")
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || !strings.HasPrefix(location, "/v1/operations/") {
		t.Fatalf("define = %d, Location %q", response.StatusCode, location)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		request, _ := http.NewRequest(http.MethodGet, base+location, nil)
		request.Header.Set("Authorization", "Bearer "+issued.Secret)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var operation struct {
			Status string `json:"status"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&operation)
		response.Body.Close()
		if decodeErr == nil && operation.Status == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("defined server operation is %q (HTTP %d)", operation.Status, response.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunRejectsDisabledListenerBeforeOpeningStateStore(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "controller.example.yaml")
	if err := run(context.Background(), []string{"serve", "--config", configPath}, io.Discard, io.Discard); err == nil {
		t.Fatal("serve accepted the disabled example listener")
	}
}

func requestUntilResponse(t *testing.T, client *http.Client, address, token string) *http.Response {
	t.Helper()
	url := "https://" + address + "/v1/operations/not-found"
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(request)
		if err == nil {
			return response
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("HTTPS listener did not become ready: %v", lastErr)
	return nil
}

func testHTTPSClient(t *testing.T, certificateFile string) *http.Client {
	t.Helper()
	certificatePEM, err := os.ReadFile(certificateFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("could not add test certificate to trust pool")
	}
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
}

func issueTestKey(t *testing.T, databasePath string) authkey.IssuedKey {
	t.Helper()
	database, err := store.OpenBolt(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := authkey.NewManager(database)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	now := time.Now().UTC()
	issued, err := manager.Issue(authkey.IssueRequest{
		ActorID: "automation", Audience: "controller-test",
		Scopes: []authkey.Scope{{Permission: "server.inspect", Environment: "staging", Resource: "server-1"},
			{Permission: "server.define", Environment: "staging", Resource: "server-1"}},
		CreatedBy: "test", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if closeErr := database.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func writeServerCertificate(t *testing.T, certificatePath, privateKeyPath string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o640); err != nil {
		t.Fatal(err)
	}
}
