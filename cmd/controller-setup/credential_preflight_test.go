//go:build !windows

package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/controllersetup"
)

func TestApplyRefusesBeforeHostChangesWhenCredentialsAreMissing(t *testing.T) {
	binary := setupR3Build(t, "./cmd/controller", "linux")
	example := setupR3ExampleConfig(t)
	value, err := config.Load(example)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	operations := &setupR3CmdHost{}
	var stdout, stderr bytes.Buffer
	err = runApplyCommand([]string{"--config", example, "--binary", binary, "--confirm", "INSTALL-GAMESERVER-CONTROLLER"},
		&stdout, &stderr, true, setupR3Debian, root, operations)
	want := "store these configured credentials with `controller-setup credentials set` or `credentials generate` before installing; each must be a non-empty regular file with no group or other permissions in /etc/credstore.encrypted: " +
		strings.Join(controllersetup.ConfiguredCredentialNames(value), ", ")
	if got := setupR3Err(err); got != want {
		t.Fatalf("apply = %q, want %q", got, want)
	}
	if len(operations.calls) != 0 || stdout.Len() != 0 {
		t.Fatalf("calls=%q stdout=%q", operations.calls, stdout.String())
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("root changed: %v", entries)
	}
}
