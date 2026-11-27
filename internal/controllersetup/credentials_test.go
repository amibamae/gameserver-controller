package controllersetup

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
)

type fakeEncryptor struct {
	calls     int
	name      string
	plaintext []byte
	fail      bool
}

func (f *fakeEncryptor) Encrypt(_ context.Context, name, destination string, plaintext []byte) error {
	f.calls++
	f.name = name
	f.plaintext = append([]byte(nil), plaintext...)
	if f.fail {
		return os.ErrPermission
	}
	return os.WriteFile(destination, []byte("synthetic-encrypted-value"), 0o600)
}

func TestSetEncryptedCredentialInstallsPrivateCiphertextAndZerosInput(t *testing.T) {
	// Skip permission-based tests on non-Linux platforms
	if runtime.GOOS != "linux" {
		t.Skip("test requires Linux filesystem permission semantics")
	}
	directory := filepath.Join(t.TempDir(), "credential-store")
	secret := []byte("synthetic-provider-key")
	encryptor := &fakeEncryptor{}
	if err := SetEncryptedCredential(context.Background(), exampleConfig(t), "example-vultr-api-token", directory, secret, false, encryptor); err != nil {
		t.Fatal(err)
	}
	if encryptor.calls != 1 || encryptor.name != "example-vultr-api-token" || string(encryptor.plaintext) != "synthetic-provider-key" {
		t.Fatalf("unexpected encryption call: %+v", encryptor)
	}
	assertClearedCredentialBuffer(t, secret, "plaintext buffer was not cleared")
	path := filepath.Join(directory, encryptor.name)
	assertCredentialMode(t, path, "credential", 0o600)
	assertCredentialMode(t, directory, "directory", 0o700)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "synthetic-provider-key") {
		t.Fatal("plaintext was stored instead of ciphertext")
	}
}

func assertClearedCredentialBuffer(t *testing.T, secret []byte, failureMessage string) {
	t.Helper()
	for _, value := range secret {
		if value != 0 {
			t.Fatal(failureMessage)
		}
	}
}

func assertCredentialMode(t *testing.T, path, label string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", label, got, want)
	}
}

func TestSetEncryptedCredentialRequiresExplicitReplacement(t *testing.T) {
	// Skip permission-based tests on non-Linux platforms
	if runtime.GOOS != "linux" {
		t.Skip("test requires Linux filesystem permission semantics")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "example-vultr-api-token")
	if err := os.WriteFile(path, []byte("old-ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := []byte("new-synthetic-key")
	encryptor := &fakeEncryptor{}
	err := SetEncryptedCredential(context.Background(), exampleConfig(t), "example-vultr-api-token", directory, secret, false, encryptor)
	if err == nil || encryptor.calls != 0 {
		t.Fatalf("existing credential was replaced without explicit approval: err=%v calls=%d", err, encryptor.calls)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || string(content) != "old-ciphertext" {
		t.Fatalf("existing credential changed: content=%q err=%v", content, readErr)
	}
	assertClearedCredentialBuffer(t, secret, "plaintext buffer was not cleared after refusal")
}

func TestSetEncryptedCredentialReplacementIsAtomicOnEncryptionFailure(t *testing.T) {
	// Skip permission-based tests on non-Linux platforms
	if runtime.GOOS != "linux" {
		t.Skip("test requires Linux filesystem permission semantics")
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "example-vultr-api-token")
	if err := os.WriteFile(path, []byte("old-ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	encryptor := &fakeEncryptor{fail: true}
	secret := []byte("new-synthetic-key")
	err := SetEncryptedCredential(context.Background(), exampleConfig(t), "example-vultr-api-token", directory, secret, true, encryptor)
	if err == nil {
		t.Fatal("encryption failure was accepted")
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil || string(content) != "old-ciphertext" {
		t.Fatalf("existing credential changed after encryption failure: content=%q err=%v", content, readErr)
	}
}

func TestSetEncryptedCredentialRejectsUnconfiguredNameAndSymlinkDirectory(t *testing.T) {
	// Skip permission-based tests on non-Linux platforms
	if runtime.GOOS != "linux" {
		t.Skip("test requires Linux filesystem permission semantics")
	}
	secret := []byte("synthetic-provider-key")
	if err := SetEncryptedCredential(context.Background(), exampleConfig(t), "unrelated", t.TempDir(), secret, false, &fakeEncryptor{}); err == nil {
		t.Fatal("unconfigured credential name was accepted")
	}
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "credentials")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := SetEncryptedCredential(context.Background(), exampleConfig(t), "example-vultr-api-token", link, []byte("synthetic-key"), false, &fakeEncryptor{}); err == nil {
		t.Fatal("symlink credential directory was accepted")
	}
}

func exampleConfig(t *testing.T) config.Config {
	t.Helper()
	value, err := config.Load(filepath.Join("..", "..", "configs", "controller.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
