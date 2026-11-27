package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/controllersetup"
)

// setupStoreCredentials writes a placeholder encrypted file for every
// credential the configuration references, as `credentials set` would.
func setupStoreCredentials(t *testing.T, root, configPath string) {
	t.Helper()
	value, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "etc", "credstore.encrypted")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range controllersetup.ConfiguredCredentialNames(value) {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("sealed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
