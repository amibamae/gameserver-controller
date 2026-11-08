package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/controllersetup"
)

type testEncryptor struct {
	calls     int
	plaintext []byte
}

func (e *testEncryptor) Encrypt(_ context.Context, _ string, destination string, plaintext []byte) error {
	e.calls++
	e.plaintext = append([]byte(nil), plaintext...)
	return os.WriteFile(destination, []byte("synthetic-ciphertext"), 0o600)
}

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

func TestCredentialSetPromptsTwiceAndNeverPrintsSecret(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "controller.example.yaml")
	secret := []byte("synthetic-provider-secret")
	confirm := []byte("synthetic-provider-secret")
	prompts := 0
	prompt := func(context.Context, string) ([]byte, error) {
		prompts++
		if prompts == 1 {
			return secret, nil
		}
		return confirm, nil
	}
	encryptor := &testEncryptor{}
	var stdout, stderr bytes.Buffer
	err := runCredentialCommand([]string{"set", "--config", configPath, "--name", "example-vultr-api-token"}, &stdout, &stderr, true, prompt, encryptor, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if prompts != 2 || encryptor.calls != 1 || string(encryptor.plaintext) != "synthetic-provider-secret" {
		t.Fatalf("unexpected prompt/encryption flow: prompts=%d encryptor=%+v", prompts, encryptor)
	}
	if strings.Contains(stdout.String()+stderr.String(), "synthetic-provider-secret") {
		t.Fatal("credential command printed the secret")
	}
	for _, input := range [][]byte{secret, confirm} {
		for _, value := range input {
			if value != 0 {
				t.Fatal("credential prompt buffer was not cleared")
			}
		}
	}
}

func TestCredentialSetRequiresAdministratorAndExplicitReplaceConfirmation(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "controller.example.yaml")
	prompts := 0
	prompt := func(context.Context, string) ([]byte, error) {
		prompts++
		return []byte("synthetic-secret"), nil
	}
	encryptor := &testEncryptor{}
	var stdout, stderr bytes.Buffer
	err := runCredentialCommand([]string{"set", "--config", configPath, "--name", "example-vultr-api-token"}, &stdout, &stderr, false, prompt, encryptor, t.TempDir())
	if err == nil || prompts != 0 || encryptor.calls != 0 {
		t.Fatal("non-administrator credential operation continued")
	}
	err = runCredentialCommand([]string{"set", "--config", configPath, "--name", "example-vultr-api-token", "--replace"}, &stdout, &stderr, true, prompt, encryptor, t.TempDir())
	if err == nil || prompts != 0 || encryptor.calls != 0 {
		t.Fatal("replacement without exact name confirmation continued")
	}
}

func TestCredentialSetRejectsMismatchedEntries(t *testing.T) {
	configPath := filepath.Join("..", "..", "configs", "controller.example.yaml")
	values := [][]byte{[]byte("synthetic-first-value"), []byte("synthetic-other-value")}
	index := 0
	prompt := func(context.Context, string) ([]byte, error) {
		value := values[index]
		index++
		return value, nil
	}
	encryptor := &testEncryptor{}
	var stdout, stderr bytes.Buffer
	err := runCredentialCommand([]string{"set", "--config", configPath, "--name", "example-vultr-api-token"}, &stdout, &stderr, true, prompt, encryptor, t.TempDir())
	if err == nil || encryptor.calls != 0 {
		t.Fatal("mismatched credential entries were accepted")
	}
	if strings.Contains(stdout.String()+stderr.String(), "synthetic-") {
		t.Fatal("mismatched credential values were printed")
	}
}

func TestCredentialSetRejectsLiteralSecretArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runCredentialCommand([]string{"set", "--name", "example-vultr-api-token", "--secret", "synthetic-secret"}, &stdout, &stderr, true, nil, &testEncryptor{}, t.TempDir())
	if err == nil {
		t.Fatal("credential command accepted literal secret argument")
	}
	if strings.Contains(stderr.String(), "synthetic-secret") {
		t.Fatal("credential error output exposed argument content")
	}
}
