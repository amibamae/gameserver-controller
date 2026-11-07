package credential

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithCredentialReadsAndClearsBuffer(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "vultr-token"), []byte("example-token"), 0o400); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(directory)
	if err != nil {
		t.Fatal(err)
	}

	var borrowed []byte
	err = resolver.WithCredential(SystemdReferenceScheme+"vultr-token", func(value []byte) error {
		borrowed = value
		if string(value) != "example-token" {
			return errors.New("callback received an unexpected credential")
		}
		return nil
	})
	if err != nil {
		t.Fatal("credential callback failed")
	}
	for _, value := range borrowed {
		if value != 0 {
			t.Fatal("credential buffer was not overwritten after use")
		}
	}
}

func TestWithCredentialRejectsInvalidReferences(t *testing.T) {
	resolver, err := NewResolver(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, reference := range []string{
		"plain-text-secret",
		"env://TOKEN",
		SystemdReferenceScheme,
		SystemdReferenceScheme + "../outside",
		SystemdReferenceScheme + "name/child",
		SystemdReferenceScheme + "bad name",
		SystemdReferenceScheme + strings.Repeat("a", 129),
	} {
		if err := resolver.WithCredential(reference, func([]byte) error { return nil }); !errors.Is(err, ErrInvalidReference) {
			t.Fatalf("invalid reference was accepted: error=%v", err)
		}
	}
}

func TestWithCredentialRejectsMissingSymlinkAndEmptyFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "empty"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("should not be read"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "linked")); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "empty", "linked"} {
		if err := resolver.WithCredential(SystemdReferenceScheme+name, func([]byte) error { return nil }); !errors.Is(err, ErrCredentialUnavailable) {
			t.Fatalf("unavailable credential %q returned %v", name, err)
		}
	}
}

func TestWithCredentialRejectsOversizedFileAndPassesCallbackError(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "large"), make([]byte, maxCredentialBytes+1), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "token"), []byte("example-token"), 0o400); err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.WithCredential(SystemdReferenceScheme+"large", func([]byte) error { return nil }); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("oversized credential returned %v", err)
	}

	callbackErr := errors.New("provider rejected the request")
	var borrowed []byte
	err = resolver.WithCredential(SystemdReferenceScheme+"token", func(value []byte) error {
		borrowed = value
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error was not returned: %v", err)
	}
	for _, value := range borrowed {
		if value != 0 {
			t.Fatal("credential buffer was not overwritten after callback failure")
		}
	}
}

func TestResolverRequiresAnExistingDirectory(t *testing.T) {
	if _, err := NewResolver(""); err == nil {
		t.Fatal("empty directory was accepted")
	}
	if _, err := NewResolver(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing directory was accepted")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewResolver(file); err == nil {
		t.Fatal("regular file was accepted as a credential directory")
	}
}

func TestFromEnvironment(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", directory)
	if _, err := FromEnvironment(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("missing systemd credential directory was accepted")
	}
}
