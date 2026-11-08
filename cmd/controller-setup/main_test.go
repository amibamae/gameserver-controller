package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/controllersetup"
)

func TestPlanCommandPrintsRedactedPreview(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "controller.example.yaml")
	var stdout, stderr bytes.Buffer
	err := runWithHost([]string{"plan", "--config", configPath, "--json"}, &stdout, &stderr,
		controllersetup.Host{OSReleaseID: "debian", Systemd: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"\"target\": \"existing-machine\"", "\"target_supported\": true", "\"credential_reference_count\": 2", "/etc/credstore.encrypted", "/var/lib/gameserver-controller/var/controller.db"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("plan output missing %q: %s", expected, stdout.String())
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestPlanCommandDoesNotPrintCredentialReferences(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "controller.yaml")
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "controller.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const privateRef = "systemd-credential://example-wasabi-staging-writer"
	data = []byte(strings.ReplaceAll(string(data), privateRef, "systemd-credential://private-reference-marker"))
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err = runWithHost([]string{"plan", "--config", configPath, "--json"}, &stdout, &stderr,
		controllersetup.Host{OSReleaseID: "debian", Systemd: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-reference-marker") {
		t.Fatal("plan output exposed a credential reference")
	}
}

func TestPlanCommandRejectsInvalidArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runWithHost(nil, &stdout, &stderr, controllersetup.Host{}); err == nil {
		t.Fatal("run accepted empty arguments")
	}
	if !strings.Contains(stderr.String(), "usage: controller-setup plan") {
		t.Fatalf("unexpected usage output: %q", stderr.String())
	}
}
