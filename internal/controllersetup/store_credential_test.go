package controllersetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// encryptFunc adapts a function to Encryptor.
type encryptFunc func(ctx context.Context, name, destination string, plaintext []byte) error

func (f encryptFunc) Encrypt(ctx context.Context, name, destination string, plaintext []byte) error {
	return f(ctx, name, destination, plaintext)
}

// writeCiphertext is an encryptor that writes a marked copy of the input.
func writeCiphertext(_ context.Context, _, destination string, plaintext []byte) error {
	return os.WriteFile(destination, append([]byte("sealed:"), plaintext...), 0o644)
}

// TestStoreEncryptedCredentialOutcomes pins the exact error, the installed
// file, and the absence of leftover temporary files for every outcome.
func TestStoreEncryptedCredentialOutcomes(t *testing.T) {
	const name = "example-vultr-api-token"
	cases := []struct {
		name      string
		credName  string
		plaintext []byte
		relative  bool
		existing  string // "", "file" or "directory" at the final path before the call
		replace   bool
		encrypt   encryptFunc
		want      string
		installed string // expected final file content, or "" for none
	}{
		{name: "new credential", credName: name, plaintext: []byte("secret"), encrypt: writeCiphertext, installed: "sealed:secret"},
		{name: "replace existing", credName: name, plaintext: []byte("new"), existing: "file", replace: true, encrypt: writeCiphertext, installed: "sealed:new"},
		{name: "existing without replace", credName: name, plaintext: []byte("new"), existing: "file", encrypt: writeCiphertext,
			want: "credential already exists; explicit replacement is required", installed: "old"},
		{name: "existing directory", credName: name, plaintext: []byte("new"), existing: "directory", replace: true, encrypt: writeCiphertext,
			want: "existing credential target is not a regular file"},
		{name: "invalid name", credName: "Bad Name", plaintext: []byte("x"), encrypt: writeCiphertext, want: "credential input is empty or invalid"},
		{name: "empty secret", credName: name, encrypt: writeCiphertext, want: "credential input is empty or invalid"},
		{name: "oversized secret", credName: name, plaintext: make([]byte, maxProviderSecret+1), encrypt: writeCiphertext, want: "credential input is empty or invalid"},
		{name: "relative directory", credName: name, plaintext: []byte("x"), relative: true, encrypt: writeCiphertext, want: "credential directory must be an absolute path"},
		{name: "encryptor fails", credName: name, plaintext: []byte("x"),
			encrypt: func(context.Context, string, string, []byte) error { return errors.New("systemd-creds detail") },
			want:    "encrypt provider credential failed"},
		{name: "encryptor writes nothing", credName: name, plaintext: []byte("x"),
			encrypt: func(context.Context, string, string, []byte) error { return nil },
			want:    "encrypted credential output is missing or invalid"},
		{name: "encryptor writes an empty file", credName: name, plaintext: []byte("x"),
			encrypt: func(_ context.Context, _, destination string, _ []byte) error {
				return os.WriteFile(destination, nil, 0o600)
			},
			want: "encrypted credential output is missing or invalid"},
		{name: "credential appears during encryption", credName: name, plaintext: []byte("x"),
			encrypt: func(ctx context.Context, n, destination string, plaintext []byte) error {
				if err := os.WriteFile(filepath.Join(filepath.Dir(destination), n), []byte("racer"), 0o600); err != nil {
					return err
				}
				return writeCiphertext(ctx, n, destination, plaintext)
			},
			want: "credential appeared during setup; no replacement was made", installed: "racer"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "credentials")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			finalPath := filepath.Join(directory, name)
			switch test.existing {
			case "file":
				if err := os.WriteFile(finalPath, []byte("old"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(finalPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			target := directory
			if test.relative {
				target = "credentials"
			}
			err := storeEncryptedCredential(context.Background(), test.credName, target, test.plaintext, test.replace, test.encrypt)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != test.want {
				t.Fatalf("error = %q, want %q", got, test.want)
			}
			content, readErr := os.ReadFile(finalPath)
			switch {
			case test.installed == "" && test.existing != "directory" && readErr == nil:
				t.Fatalf("credential installed unexpectedly: %q", content)
			case test.installed != "" && string(content) != test.installed:
				t.Fatalf("installed = %q, %v; want %q", content, readErr, test.installed)
			}
			if test.installed != "" && test.want == "" {
				if info, _ := os.Stat(finalPath); info.Mode().Perm() != 0o600 {
					t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
				}
			}
			entries, _ := os.ReadDir(directory)
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".controller-credential-") {
					t.Fatalf("temporary file left behind: %s", entry.Name())
				}
			}
		})
	}
}
