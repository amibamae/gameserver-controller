package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// servingConfig returns the example configuration with an enabled HTTPS
// listener on a free local address, a fresh state store, and test credentials.
func servingConfig(t *testing.T) (config.Config, string) {
	t.Helper()
	configPath := filepath.Join("..", "..", "configs", "controller.example.yaml")
	value, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if value.Controller.ManagementAPI.Enabled {
		t.Fatal("example configuration unexpectedly enables a listener")
	}
	value.Controller.StateStore.Path = filepath.Join(t.TempDir(), "controller.db")
	value.Controller.ManagementAPI = config.ManagementAPI{
		Enabled: true, ListenAddress: freeAddress(t), Audience: "controller-test",
		TLSCertificate: filepath.Join(t.TempDir(), "tls.crt"),
		TLSPrivateKey:  filepath.Join(t.TempDir(), "tls.key"),
	}
	credentials := t.TempDir()
	if err := os.WriteFile(filepath.Join(credentials, "controller-idempotency-key-v1"), bytes.Repeat([]byte{9}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", credentials)
	value.Controller.IdempotencyKeys = config.IdempotencyKeys{Active: "v1", Versions: map[string]string{"v1": "systemd-credential://controller-idempotency-key-v1"}}
	writeServerCertificate(t, value.Controller.ManagementAPI.TLSCertificate, value.Controller.ManagementAPI.TLSPrivateKey)
	if err := config.Validate(value); err != nil {
		t.Fatalf("validate test configuration: %v", err)
	}
	return value, configPath
}

func TestRunCommandLine(t *testing.T) {
	example := filepath.Join("..", "..", "configs", "controller.example.yaml")
	tests := []struct {
		name       string
		args       []string
		wantErr    string
		wantStdout string
		wantStderr string
	}{
		{"version", []string{"version"}, "", buildIdentity + "\n", ""},
		{"no command", nil, "unsupported command", "", "usage: controller serve --config PATH"},
		{"unknown command", []string{"start"}, "unsupported command", "", "usage: controller serve --config PATH"},
		{"version with arguments", []string{"version", "--verbose"}, "unsupported command", "", "usage:"},
		{"help", []string{"serve", "-h"}, "", "", "-config"},
		{"unknown flag", []string{"serve", "--listen", ":0"}, "flag provided but not defined", "", "-config"},
		{"missing config flag", []string{"serve"}, "serve requires --config PATH", "", ""},
		{"blank config path", []string{"serve", "--config", "  "}, "serve requires --config PATH", "", ""},
		{"positional argument", []string{"serve", "--config", example, "extra"}, "serve requires --config PATH", "", ""},
		{"unreadable config", []string{"serve", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, "", "", ""},
		{"disabled listener", []string{"serve", "--config", example}, "management API listener is disabled in configuration", "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(context.Background(), test.args, &stdout, &stderr)
			switch {
			case test.name == "unreadable config":
				if err == nil {
					t.Fatal("run accepted a missing configuration file")
				}
			case test.wantErr == "":
				if err != nil {
					t.Fatalf("run error = %v, want nil", err)
				}
			case err == nil || !strings.Contains(err.Error(), test.wantErr):
				t.Fatalf("run error = %v, want one containing %q", err, test.wantErr)
			}
			if stdout.String() != test.wantStdout {
				t.Fatalf("stdout = %q, want %q", stdout.String(), test.wantStdout)
			}
			if !strings.Contains(stderr.String(), test.wantStderr) {
				t.Fatalf("stderr = %q, want it to contain %q", stderr.String(), test.wantStderr)
			}
		})
	}
}

func TestServeRejectsAMissingOrEndedContext(t *testing.T) {
	value, configPath := servingConfig(t)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	var missing context.Context
	for name, ctx := range map[string]context.Context{"missing": missing, "ended": ended} {
		t.Run(name, func(t *testing.T) {
			if err := serve(ctx, value, configPath, io.Discard); err == nil || err.Error() != "controller context is unavailable" {
				t.Fatalf("serve error = %v, want the unavailable context error", err)
			}
		})
	}
	if _, err := os.Stat(value.Controller.StateStore.Path); !os.IsNotExist(err) {
		t.Fatalf("serve opened the state store without a usable context: %v", err)
	}
}

func TestServeRejectsMissingTLSFilesBeforeOpeningTheStore(t *testing.T) {
	value, configPath := servingConfig(t)
	if err := os.Remove(value.Controller.ManagementAPI.TLSPrivateKey); err != nil {
		t.Fatal(err)
	}
	if err := serve(context.Background(), value, configPath, io.Discard); err == nil || !strings.Contains(err.Error(), "inspect TLS file") {
		t.Fatalf("serve error = %v, want a TLS file error", err)
	}
	if _, err := os.Stat(value.Controller.StateStore.Path); !os.IsNotExist(err) {
		t.Fatalf("serve opened the state store before loading TLS: %v", err)
	}
}

func TestServeClosesTheStoreWhenTheAddressIsTaken(t *testing.T) {
	value, configPath := servingConfig(t)
	occupied, err := net.Listen("tcp", value.Controller.ManagementAPI.ListenAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var stdout bytes.Buffer
	err = serve(context.Background(), value, configPath, &stdout)
	if err == nil || !strings.HasPrefix(err.Error(), "listen for management API: ") {
		t.Fatalf("serve error = %v, want a listen error", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("serve reported startup after failing to listen: %q", stdout.String())
	}
	// The store holds a file lock while open; reopening proves serve closed it.
	database, err := store.OpenBolt(value.Controller.StateStore.Path)
	if err != nil {
		t.Fatalf("state store still open after the failed start: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

// syncBuffer lets the test read output that serve's goroutines still write.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestServeReportsReadinessAndStopsWithItsContext(t *testing.T) {
	value, configPath := servingConfig(t)
	stdout := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, value, configPath, stdout) }()
	ready := "Management HTTPS listener ready at " + value.Controller.ManagementAPI.ListenAddress + "\n"
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stdout.String(), ready) {
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("no readiness line in %q", stdout.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The example configuration registers no adapters or monitors, so their
	// lines are omitted and the readiness line is the whole startup report.
	if stdout.String() != ready {
		t.Fatalf("startup output = %q, want only %q", stdout.String(), ready)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after its context ended")
	}
}

func TestServeWithoutStdoutStillServes(t *testing.T) {
	value, configPath := servingConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, value, configPath, nil) }()
	client := testHTTPSClient(t, value.Controller.ManagementAPI.TLSCertificate)
	defer client.CloseIdleConnections()
	response := requestUntilResponse(t, client, value.Controller.ManagementAPI.ListenAddress, "")
	response.Body.Close()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve = %v, want nil", err)
	}
}

func TestControllerLog(t *testing.T) {
	controllerLog(nil)("ignored", map[string]any{"field": 1})

	var out bytes.Buffer
	log := controllerLog(&out)
	log("proxy recovered", map[string]any{"integration": "proxy.velocity", "message": "overridden"})
	log("not encodable", map[string]any{"value": make(chan int)})
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("log lines = %q, want only the encodable event", out.String())
	}
	if !strings.Contains(lines[0], `"integration":"proxy.velocity"`) || !strings.Contains(lines[0], `"message":"overridden"`) || !strings.Contains(lines[0], `"time":"`) {
		t.Fatalf("log line = %s", lines[0])
	}
}
