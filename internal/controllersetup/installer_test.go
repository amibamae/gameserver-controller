package controllersetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/amibamae/gameserver-controller/internal/config"
)

type fakeInstallerHost struct {
	ensureCalls int
	chownCalls  int
	reloadCalls int
	reloadErr   error
}

func (f *fakeInstallerHost) EnsureServiceAccount(_ context.Context, name, home string) error {
	f.ensureCalls++
	if name != serviceAccount || !filepath.IsAbs(home) {
		return errors.New("unexpected account configuration")
	}
	return nil
}

func (f *fakeInstallerHost) Chown(_ context.Context, path, owner, group string) error {
	f.chownCalls++
	if !filepath.IsAbs(path) || owner == "" || group == "" {
		return errors.New("unexpected ownership request")
	}
	return nil
}

func (f *fakeInstallerHost) DaemonReload(context.Context) error {
	f.reloadCalls++
	return f.reloadErr
}

func TestBuildInstallBundleRendersPrivateConfigAndCredentialUnit(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	bundle, err := BuildInstallBundle(value, layout, []byte("test-controller-binary"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) != 3 || len(bundle.Directories) != 2 || bundle.StateDir != layout.StateDir {
		t.Fatalf("unexpected install bundle summary: %#v", bundle)
	}
	files := make(map[string]InstallFile, len(bundle.Files))
	for _, file := range bundle.Files {
		files[file.Path] = file
	}
	configFile := files[layout.ConfigFile]
	if configFile.Mode.Perm() != 0o640 {
		t.Fatalf("configuration permissions are unsafe: mode=%#o", configFile.Mode.Perm())
	}
	var rendered config.Config
	if err := yaml.Unmarshal(configFile.Content, &rendered); err != nil {
		t.Fatal(err)
	}
	if rendered.Controller.StateStore.Path != layout.StateDatabaseFile {
		t.Fatalf("rendered state path=%q, want %q", rendered.Controller.StateStore.Path, layout.StateDatabaseFile)
	}
	unit := string(files[layout.ServiceFile].Content)
	for _, credential := range []string{"example-vultr-api-token", "example-wasabi-staging-writer"} {
		if !strings.Contains(unit, "LoadCredentialEncrypted="+credential+":") {
			t.Fatalf("systemd unit omitted configured encrypted credential %q", credential)
		}
	}
	for _, directive := range []string{"NoNewPrivileges=true", "ProtectSystem=strict", "ProtectHome=true", "UMask=0077"} {
		if !strings.Contains(unit, directive) {
			t.Fatalf("systemd unit omitted hardening directive %q", directive)
		}
	}
	if strings.Contains(unit, "enable --now") || strings.Contains(unit, "systemctl start") {
		t.Fatal("systemd unit requested automatic service start")
	}
}

func TestBuildInstallBundleRejectsFilesInsideWritableOrCredentialDirectories(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	layout.BinaryFile = filepath.Join(layout.StateDir, "controller")
	if _, err := BuildInstallBundle(value, layout, []byte("binary")); err == nil {
		t.Fatal("accepted a controller executable inside the service-writable state directory")
	}
	layout = buildTestPlan(t, value).Layout
	layout.ConfigFile = filepath.Join(layout.CredentialDir, "controller.yaml")
	if _, err := BuildInstallBundle(value, layout, []byte("binary")); err == nil {
		t.Fatal("accepted a configuration file inside the encrypted credential directory")
	}
}

func TestApplyInstallBundleIsIdempotentAndDoesNotReplaceFiles(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	bundle, err := BuildInstallBundle(value, layout, []byte("test-controller-binary"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	operations := &fakeInstallerHost{}
	if err := ApplyInstallBundle(context.Background(), root, Host{OSReleaseID: "debian", Systemd: true}, bundle, operations); err != nil {
		t.Fatal(err)
	}
	if operations.ensureCalls != 1 || operations.reloadCalls != 1 {
		t.Fatalf("host operation counts after install: %#v", operations)
	}
	for _, file := range bundle.Files {
		path := mapRootPath(root, file.Path)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read installed %s: %v", file.Path, err)
		}
		if string(content) != string(file.Content) {
			t.Fatalf("installed content differs at %s", file.Path)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != file.Mode.Perm() {
			t.Fatalf("installed mode at %s = %v, err=%v", file.Path, info.Mode().Perm(), err)
		}
	}
	if err := ApplyInstallBundle(context.Background(), root, Host{OSReleaseID: "debian", Systemd: true}, bundle, operations); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	if operations.reloadCalls != 2 {
		t.Fatalf("systemd was not reloaded on idempotent apply: %#v", operations)
	}

	changed := bundle
	changed.Files = append([]InstallFile(nil), bundle.Files...)
	changed.Files[0].Content = []byte("different")
	if err := ApplyInstallBundle(context.Background(), root, Host{OSReleaseID: "debian", Systemd: true}, changed, operations); err == nil {
		t.Fatal("apply replaced a pre-existing installation file")
	}
}

func TestApplyInstallBundlePreflightsSymlinksAndRollsBackReloadFailure(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	bundle, err := BuildInstallBundle(value, layout, []byte("test-controller-binary"))
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "tmp", "controller-setup-test")); err != nil {
		t.Fatal(err)
	}
	operations := &fakeInstallerHost{}
	if err := ApplyInstallBundle(context.Background(), root, Host{OSReleaseID: "debian", Systemd: true}, bundle, operations); err == nil {
		t.Fatal("accepted a symlinked installation parent")
	}
	if operations.ensureCalls != 0 {
		t.Fatal("installer mutated host state before completing path preflight")
	}

	root = t.TempDir()
	operations = &fakeInstallerHost{reloadErr: errors.New("systemd unavailable")}
	err = ApplyInstallBundle(context.Background(), root, Host{OSReleaseID: "debian", Systemd: true}, bundle, operations)
	if err == nil {
		t.Fatal("accepted a failed systemd daemon reload")
	}
	for _, file := range bundle.Files {
		if _, err := os.Lstat(mapRootPath(root, file.Path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("new file remained after failed systemd reload: %s", file.Path)
		}
	}
}

func TestApplyInstallBundleRejectsUnsupportedHostAndCanceledContext(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	bundle, err := BuildInstallBundle(value, layout, []byte("test-controller-binary"))
	if err != nil {
		t.Fatal(err)
	}
	operations := &fakeInstallerHost{}
	if err := ApplyInstallBundle(context.Background(), t.TempDir(), Host{OSReleaseID: "ubuntu", Systemd: true}, bundle, operations); err == nil {
		t.Fatal("accepted an unsupported host")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ApplyInstallBundle(ctx, t.TempDir(), Host{OSReleaseID: "debian", Systemd: true}, bundle, operations); err == nil {
		t.Fatal("accepted a canceled install context")
	}
}

func TestApplyInstallBundleRejectsUnsafeModesBeforeHostMutation(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	bundle, err := BuildInstallBundle(value, layout, []byte("test-controller-binary"))
	if err != nil {
		t.Fatal(err)
	}
	bundle.Files[0].Mode = 0o777
	operations := &fakeInstallerHost{}
	if err := ApplyInstallBundle(context.Background(), t.TempDir(), Host{OSReleaseID: "debian", Systemd: true}, bundle, operations); err == nil {
		t.Fatal("accepted a group/world-writable installation file")
	}
	if operations.ensureCalls != 0 {
		t.Fatal("installer changed host state before rejecting unsafe permissions")
	}
}

func loadExampleConfig(t *testing.T) config.Config {
	t.Helper()
	value, err := config.Load(filepath.Join("..", "..", "configs", "controller.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func buildTestPlan(t *testing.T, value config.Config) Plan {
	t.Helper()
	plan, err := BuildPlan(value, Host{OSReleaseID: "debian", Systemd: true}, testLayout())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
