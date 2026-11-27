package controllersetup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
)

func preflightConfig(names ...string) config.Config {
	var value config.Config
	refs := make(map[string]config.SecretReference, len(names))
	for _, name := range names {
		refs[name] = config.SecretReference{SecretRef: "systemd-credential://" + name}
	}
	value.Adapters = []config.AdapterProfile{{SecretRefs: refs}}
	return value
}

func writePreflightCredential(t *testing.T, root, name string, mode os.FileMode, content string) {
	t.Helper()
	directory := filepath.Join(root, "etc", "credstore.encrypted")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

const preflightPrefix = "store these configured credentials with `controller-setup credentials set` or `credentials generate` before installing; each must be a non-empty regular file with no group or other permissions in /etc/credstore.encrypted: "

func TestVerifyStoredCredentialsAcceptsEveryStoredCredential(t *testing.T) {
	root := t.TempDir()
	writePreflightCredential(t, root, "alpha", 0o600, "sealed")
	writePreflightCredential(t, root, "beta", 0o600, "sealed")
	if err := VerifyStoredCredentials(preflightConfig("beta", "alpha"), root, "/etc/credstore.encrypted"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyStoredCredentials(config.Config{}, t.TempDir(), "/etc/credstore.encrypted"); err != nil {
		t.Fatalf("no configured credentials: %v", err)
	}
}

func TestVerifyStoredCredentialsListsEveryUnusableCredential(t *testing.T) {
	root := t.TempDir()
	writePreflightCredential(t, root, "good", 0o600, "sealed")
	writePreflightCredential(t, root, "empty", 0o600, "")
	if err := os.Mkdir(filepath.Join(root, "etc", "credstore.encrypted", "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := []string{"directory", "empty", "missing"}
	if runtime.GOOS != "windows" { // Windows reports no group or other permission bits.
		writePreflightCredential(t, root, "shared", 0o640, "sealed")
		want = append(want, "shared")
		if err := os.Symlink(filepath.Join(root, "etc", "credstore.encrypted", "good"), filepath.Join(root, "etc", "credstore.encrypted", "symlink")); err != nil {
			t.Fatal(err)
		}
		want = append(want, "symlink")
	}
	err := VerifyStoredCredentials(preflightConfig(append([]string{"good"}, want...)...), root, "/etc/credstore.encrypted")
	if err == nil || err.Error() != preflightPrefix+strings.Join(want, ", ") {
		t.Fatalf("err = %v, want names %v", err, want)
	}
}

func TestVerifyStoredCredentialsRequiresCleanAbsoluteDirectory(t *testing.T) {
	for _, directory := range []string{"etc/credstore.encrypted", "/etc/../etc/credstore.encrypted"} {
		err := VerifyStoredCredentials(preflightConfig("alpha"), t.TempDir(), directory)
		if err == nil || err.Error() != "credential directory must be clean and absolute" {
			t.Fatalf("%s: err = %v", directory, err)
		}
	}
}
