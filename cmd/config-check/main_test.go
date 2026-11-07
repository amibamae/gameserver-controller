package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateCommand(t *testing.T) {
	root := filepath.Join("..", "..")
	configPath := filepath.Join(root, "configs", "controller.example.yaml")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"validate", configPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got := stdout.String(); !strings.Contains(got, "configuration valid (schema v1") {
		t.Fatalf("unexpected success output: %q", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestValidateCommandDoesNotPrintSecretValues(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "controller.yaml")
	input := strings.ReplaceAll(readExample(t), "systemd-credential://example-wasabi-staging-writer", "systemd-credential://very-private-reference-should-not-print")
	if err := os.WriteFile(configPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"validate", configPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "very-private-reference-should-not-print") {
		t.Fatalf("command printed secret reference: %q", combined)
	}
}

func readExample(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "controller.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestInvalidUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err == nil {
		t.Fatal("run accepted empty args")
	}
	if !strings.Contains(stderr.String(), "usage: config-check validate") {
		t.Fatalf("unexpected usage output: %q", stderr.String())
	}
}
