package controllersetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/amibamae/gameserver-controller/internal/config"
)

// These characterization tests pin what BuildInstallBundle renders, the
// exact reason each layout and bundle is refused, every host operation
// ApplyInstallBundle performs (in order), and what it leaves behind on
// failure. They were written against the code before the complexity
// refactor and must keep passing unchanged.

// setupR3RecordingHost records every host operation as one line.
type setupR3RecordingHost struct {
	calls     []string
	ensureErr error
	chownErr  func(path string) error
	reloadErr error
}

func (h *setupR3RecordingHost) EnsureServiceAccount(_ context.Context, name, home string) error {
	h.calls = append(h.calls, "ensure "+name+" "+home)
	return h.ensureErr
}

func (h *setupR3RecordingHost) Chown(_ context.Context, path, owner, group string) error {
	h.calls = append(h.calls, "chown "+path+" "+owner+":"+group)
	if h.chownErr != nil {
		return h.chownErr(path)
	}
	return nil
}

func (h *setupR3RecordingHost) DaemonReload(context.Context) error {
	h.calls = append(h.calls, "reload")
	return h.reloadErr
}

// setupR3MatchCalls compares recorded calls with expected lines, where each
// expected line is a regular expression anchored at both ends.
func setupR3MatchCalls(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("host calls = %q, want %d calls matching %q", got, len(want), want)
	}
	for i := range want {
		if !regexp.MustCompile("^" + want[i] + "$").MatchString(got[i]) {
			t.Fatalf("host call %d = %q, want a match for %q (all calls %q)", i, got[i], want[i], got)
		}
	}
}

func setupR3Bundle() InstallBundle {
	return InstallBundle{
		StateDir: "/var/lib/gsc",
		Directories: []InstallDirectory{
			{Path: "/var/lib/gsc", Mode: 0o750, Owner: serviceAccount, Group: serviceAccount},
		},
		Files: []InstallFile{
			{Path: "/etc/gsc/config.yaml", Mode: 0o640, Owner: "root", Group: serviceAccount, Content: []byte("controller: {}\n")},
			{Path: "/usr/local/bin/controller", Mode: 0o755, Owner: "root", Group: "root", Content: []byte("binary")},
		},
	}
}

func setupR3SkipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions; this case runs as an unprivileged user in CI")
	}
}

// setupR3ReadOnlyCleanup makes directories writable again so t.TempDir can
// remove them.
func setupR3ReadOnlyCleanup(t *testing.T, paths ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, path := range paths {
			_ = os.Chmod(path, 0o755)
		}
	})
}

func setupR3Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func setupR3Entries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

const setupR3TestUnit = `[Unit]
Description=Game Server Controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=gameserver-controller
Group=gameserver-controller
ExecStart=/tmp/controller-setup-test/controller serve --config /tmp/controller-setup-test/controller.yaml
Restart=on-failure
RestartSec=5s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
LockPersonality=true
RestrictSUIDSGID=true
ReadWritePaths=/tmp/controller-setup-test/state
LoadCredentialEncrypted=controller-idempotency-key-v1:/tmp/controller-setup-test/credentials/controller-idempotency-key-v1
LoadCredentialEncrypted=example-vultr-api-token:/tmp/controller-setup-test/credentials/example-vultr-api-token
LoadCredentialEncrypted=example-wasabi-staging-writer:/tmp/controller-setup-test/credentials/example-wasabi-staging-writer

[Install]
WantedBy=multi-user.target
`

const setupR3DefaultUnit = `[Unit]
Description=Game Server Controller
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=gameserver-controller
Group=gameserver-controller
ExecStart=/usr/local/bin/controller serve --config /etc/gameserver-controller/controller.yaml
Restart=on-failure
RestartSec=5s
UMask=0077
StateDirectory=gameserver-controller
StateDirectoryMode=0750
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
LockPersonality=true
RestrictSUIDSGID=true
ReadWritePaths=/var/lib/gameserver-controller
LoadCredentialEncrypted=controller-idempotency-key-v1:/etc/credstore.encrypted/controller-idempotency-key-v1
LoadCredentialEncrypted=example-vultr-api-token:/etc/credstore.encrypted/example-vultr-api-token
LoadCredentialEncrypted=example-wasabi-staging-writer:/etc/credstore.encrypted/example-wasabi-staging-writer

[Install]
WantedBy=multi-user.target
`

func TestSetupR3BuildInstallBundleExactContents(t *testing.T) {
	value := loadExampleConfig(t)
	layout := buildTestPlan(t, value).Layout
	binary := []byte("test-controller-binary")
	bundle, err := BuildInstallBundle(value, layout, binary)
	if err != nil {
		t.Fatal(err)
	}
	binary[0] = 'X' // the bundle keeps its own copy of the executable
	wantDirectories := []InstallDirectory{
		{Path: "/tmp/controller-setup-test", Mode: 0o750, Owner: "root", Group: serviceAccount},
		{Path: "/tmp/controller-setup-test/state", Mode: 0o750, Owner: serviceAccount, Group: serviceAccount},
	}
	if !reflect.DeepEqual(bundle.Directories, wantDirectories) {
		t.Fatalf("directories = %+v, want %+v", bundle.Directories, wantDirectories)
	}
	if bundle.StateDir != "/tmp/controller-setup-test/state" {
		t.Fatalf("state directory = %q", bundle.StateDir)
	}
	main := value
	main.Controller.StateStore.Path = layout.StateDatabaseFile
	main.Adapters = nil
	for _, profile := range value.Adapters {
		if profile.Source == "" {
			main.Adapters = append(main.Adapters, profile)
		}
	}
	wantConfig, err := yaml.Marshal(main)
	if err != nil {
		t.Fatal(err)
	}
	wantFiles := []InstallFile{
		{Path: "/tmp/controller-setup-test/controller", Mode: 0o755, Owner: "root", Group: "root", Content: []byte("test-controller-binary")},
		{Path: "/tmp/controller-setup-test/controller.yaml", Mode: 0o640, Owner: "root", Group: serviceAccount, Content: wantConfig},
		{Path: "/tmp/controller-setup-test/service.unit", Mode: 0o644, Owner: "root", Group: "root", Content: []byte(setupR3TestUnit)},
	}
	if len(bundle.Files) != len(wantFiles) {
		t.Fatalf("files = %d, want %d", len(bundle.Files), len(wantFiles))
	}
	for i, want := range wantFiles {
		got := bundle.Files[i]
		if got.Path != want.Path || got.Mode != want.Mode || got.Owner != want.Owner || got.Group != want.Group || !bytes.Equal(got.Content, want.Content) {
			t.Fatalf("file %d = %s %v %s:%s %q\nwant %s %v %s:%s %q", i, got.Path, got.Mode, got.Owner, got.Group, got.Content, want.Path, want.Mode, want.Owner, want.Group, want.Content)
		}
	}
}

func TestSetupR3BuildInstallBundleDefaultLayoutUnit(t *testing.T) {
	value := loadExampleConfig(t)
	plan, err := BuildPlan(value, debianHost, Layout{
		ConfigFile:    "/etc/gameserver-controller/controller.yaml",
		ServiceFile:   "/etc/systemd/system/gameserver-controller.service",
		BinaryFile:    "/usr/local/bin/controller",
		StateDir:      "/var/lib/gameserver-controller",
		CredentialDir: "/etc/credstore.encrypted",
	})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildInstallBundle(value, plan.Layout, []byte("binary"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range bundle.Files {
		if file.Path == "/etc/systemd/system/gameserver-controller.service" {
			if string(file.Content) != setupR3DefaultUnit {
				t.Fatalf("unit =\n%s\nwant\n%s", file.Content, setupR3DefaultUnit)
			}
			return
		}
	}
	t.Fatal("bundle has no systemd unit")
}

func TestSetupR3BuildInstallBundleAdapterFilesAndDirectories(t *testing.T) {
	value := loadExampleConfig(t)
	for i := range value.Adapters {
		if value.Adapters[i].ID == "game.paper" {
			value.Adapters[i].Source = "30-paper.yaml"
		}
	}
	layout := buildTestPlan(t, value).Layout
	bundle, err := BuildInstallBundle(value, layout, []byte("binary"))
	if err != nil {
		t.Fatal(err)
	}
	wantDirectories := []InstallDirectory{
		{Path: "/tmp/controller-setup-test", Mode: 0o750, Owner: "root", Group: serviceAccount},
		{Path: "/tmp/controller-setup-test/adapters.d", Mode: 0o750, Owner: "root", Group: serviceAccount},
		{Path: "/tmp/controller-setup-test/state", Mode: 0o750, Owner: serviceAccount, Group: serviceAccount},
	}
	if !reflect.DeepEqual(bundle.Directories, wantDirectories) {
		t.Fatalf("directories = %+v, want %+v", bundle.Directories, wantDirectories)
	}
	var paths []string
	for _, file := range bundle.Files {
		paths = append(paths, fmt.Sprintf("%s %v %s:%s", file.Path, file.Mode, file.Owner, file.Group))
	}
	want := []string{
		"/tmp/controller-setup-test/adapters.d/30-paper.yaml -rw-r----- root:gameserver-controller",
		"/tmp/controller-setup-test/controller -rwxr-xr-x root:root",
		"/tmp/controller-setup-test/controller.yaml -rw-r----- root:gameserver-controller",
		"/tmp/controller-setup-test/service.unit -rw-r--r-- root:root",
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("files = %q, want %q", paths, want)
	}
	for _, profile := range value.Adapters {
		if profile.Source != "" {
			wantContent, err := yaml.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(bundle.Files[0].Content, wantContent) {
				t.Fatalf("adapter file content = %q, want %q", bundle.Files[0].Content, wantContent)
			}
		}
	}
}

func TestSetupR3BuildInstallBundleErrors(t *testing.T) {
	value := loadExampleConfig(t)
	base := buildTestPlan(t, value).Layout
	tests := []struct {
		name   string
		value  *config.Config
		binary []byte
		layout func(*Layout)
		want   string
	}{
		{name: "invalid configuration", value: &config.Config{}, want: "controller configuration is invalid"},
		{name: "empty executable", binary: []byte{}, want: "controller executable is empty"},
		{name: "nil executable", binary: nil, want: "controller executable is empty"},
		{name: "relative configuration", layout: func(l *Layout) { l.ConfigFile = "controller.yaml" }, want: "configuration destination must be a clean absolute path"},
		{name: "unclean unit", layout: func(l *Layout) { l.ServiceFile = "/tmp/x/../service.unit" }, want: "systemd unit destination must be a clean absolute path"},
		{name: "NUL in controller", layout: func(l *Layout) { l.BinaryFile = "/tmp/contr\x00oller" }, want: "controller destination must be a clean absolute path"},
		{name: "empty state", layout: func(l *Layout) { l.StateDir = "" }, want: "state destination must be a clean absolute path"},
		{name: "trailing slash credentials", layout: func(l *Layout) { l.CredentialDir = "/tmp/credentials/" }, want: "credentials destination must be a clean absolute path"},
		{name: "space in configuration", layout: func(l *Layout) { l.ConfigFile = "/tmp/a b/controller.yaml" }, want: "configuration destination contains unsupported characters"},
		{name: "percent in unit", layout: func(l *Layout) { l.ServiceFile = "/tmp/%i.service" }, want: "systemd unit destination contains unsupported characters"},
		{name: "dollar in controller", layout: func(l *Layout) { l.BinaryFile = "/tmp/$controller" }, want: "controller destination contains unsupported characters"},
		{name: "semicolon in state", layout: func(l *Layout) { l.StateDir = "/tmp/state;x" }, want: "state destination contains unsupported characters"},
		{name: "unicode in credentials", layout: func(l *Layout) { l.CredentialDir = "/tmp/cré" }, want: "credentials destination contains unsupported characters"},
		{name: "missing state database", layout: func(l *Layout) { l.StateDatabaseFile = "" }, want: "planned controller state database path is required"},
		{name: "relative state database", layout: func(l *Layout) { l.StateDatabaseFile = "state/controller.db" }, want: "state database destination must be a clean absolute path"},
		{name: "unclean state database", layout: func(l *Layout) { l.StateDatabaseFile = l.StateDir + "/x/../controller.db" }, want: "state database destination must be a clean absolute path"},
		{name: "state database outside", layout: func(l *Layout) { l.StateDatabaseFile = "/tmp/controller.db" }, want: "state database must stay inside the private state directory"},
		{name: "state database is the state directory", layout: func(l *Layout) { l.StateDatabaseFile = l.StateDir }, want: "state database must stay inside the private state directory"},
		{name: "credentials inside state", layout: func(l *Layout) { l.CredentialDir = l.StateDir + "/credentials" }, want: "state and credential directories must not overlap"},
		{name: "state inside credentials", layout: func(l *Layout) {
			l.StateDir = l.CredentialDir + "/state"
			l.StateDatabaseFile = l.StateDir + "/controller.db"
		}, want: "state and credential directories must not overlap"},
		{name: "same state and credentials", layout: func(l *Layout) { l.CredentialDir = l.StateDir }, want: "state and credential directories must not overlap"},
		{name: "configuration inside state", layout: func(l *Layout) { l.ConfigFile = l.StateDir + "/controller.yaml" }, want: "installation files must stay outside state and credential directories"},
		{name: "unit inside credentials", layout: func(l *Layout) { l.ServiceFile = l.CredentialDir + "/service.unit" }, want: "installation files must stay outside state and credential directories"},
		{name: "controller is the state directory", layout: func(l *Layout) { l.BinaryFile = l.StateDir }, want: "installation files must stay outside state and credential directories"},
		{name: "unit is the controller", layout: func(l *Layout) { l.ServiceFile = l.BinaryFile }, want: "installation destinations overlap"},
		{name: "configuration is the unit", layout: func(l *Layout) { l.ServiceFile = l.ConfigFile }, want: "installation file destinations must be unique"},
		{name: "configuration is the controller", layout: func(l *Layout) { l.BinaryFile = l.ConfigFile }, want: "installation file destinations must be unique"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := value
			if test.value != nil {
				input = *test.value
			}
			binary := []byte("binary")
			if test.layout == nil && test.value == nil {
				binary = test.binary
			}
			layout := base
			if test.layout != nil {
				test.layout(&layout)
			}
			bundle, err := BuildInstallBundle(input, layout, binary)
			if err == nil || err.Error() != test.want {
				t.Fatalf("BuildInstallBundle = %v, want %q", err, test.want)
			}
			if !reflect.DeepEqual(bundle, InstallBundle{}) {
				t.Fatalf("a refused layout returned a bundle: %+v", bundle)
			}
		})
	}
}

// TestSetupR3UnsafeInstallModeTable pins the #228 fail-closed special-mode
// check: group/world write, setuid, setgid and sticky are refused whether
// they are given as Go mode bits or raw Unix bits.
func TestSetupR3UnsafeInstallModeTable(t *testing.T) {
	for mode, want := range map[fs.FileMode]bool{
		0o755: false, 0o750: false, 0o700: false, 0o644: false, 0o640: false, 0o600: false, 0o555: false, 0: false,
		fs.ModeDir | 0o750: false, fs.ModeSymlink | 0o644: false,
		0o775: true, 0o757: true, 0o777: true, 0o660: true, 0o646: true, 0o620: true, 0o602: true,
		fs.ModeSetuid | 0o755: true, fs.ModeSetgid | 0o750: true, fs.ModeSticky | 0o750: true,
		fs.ModeSetuid: true, fs.ModeSetgid: true, fs.ModeSticky: true,
		0o4755: true, 0o2750: true, 0o1750: true, 0o4000: true, 0o2000: true, 0o1000: true, 0o7000: true,
		fs.ModeDir | fs.ModeSetgid | 0o750: true, fs.ModeDir | 0o1750: true,
	} {
		if got := unsafeInstallMode(mode); got != want {
			t.Errorf("unsafeInstallMode(%v / %#o) = %t, want %t", mode, uint32(mode), got, want)
		}
	}
}

func TestSetupR3ApplyRejectsEverySpecialModeBeforeChangingTheHost(t *testing.T) {
	special := map[string]fs.FileMode{
		"setuid": fs.ModeSetuid, "setgid": fs.ModeSetgid, "sticky": fs.ModeSticky,
		"raw setuid": 0o4000, "raw setgid": 0o2000, "raw sticky": 0o1000,
		"group write": 0o020, "world write": 0o002,
	}
	for name, bit := range special {
		for _, target := range []string{"file", "directory"} {
			t.Run(name+" "+target, func(t *testing.T) {
				root := t.TempDir()
				bundle := setupR3Bundle()
				want := "installation directory permissions are unsafe"
				if target == "file" {
					bundle.Files[1].Mode |= bit
					want = "installation file permissions are unsafe"
				} else {
					bundle.Directories[0].Mode |= bit
				}
				operations := &setupR3RecordingHost{}
				err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations)
				if err == nil || err.Error() != want {
					t.Fatalf("ApplyInstallBundle = %v, want %q", err, want)
				}
				if len(operations.calls) != 0 {
					t.Fatalf("host changed before the rejection: %q", operations.calls)
				}
				if entries := setupR3Entries(t, root); len(entries) != 0 {
					t.Fatalf("root changed before the rejection: %q", entries)
				}
			})
		}
	}
}

func TestSetupR3ApplyCheckOrder(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*InstallBundle)
		want    string
	}{
		{name: "directory mode before directory path", prepare: func(b *InstallBundle) {
			b.Directories[0].Mode = 0o777
			b.Directories[0].Path = "relative"
		}, want: "installation directory permissions are unsafe"},
		{name: "directory path before directory owner", prepare: func(b *InstallBundle) {
			b.Directories[0].Path = "relative"
			b.Directories[0].Owner = "Bad"
		}, want: "installation artifact path must be clean and absolute"},
		{name: "file mode before file owner", prepare: func(b *InstallBundle) {
			b.Files[0].Mode = 0o666
			b.Files[0].Owner = "Bad"
		}, want: "installation file permissions are unsafe"},
		{name: "file owner before file path", prepare: func(b *InstallBundle) {
			b.Files[0].Owner = "Bad"
			b.Files[0].Path = "relative"
		}, want: "installation file owner or group is invalid"},
		{name: "every directory before any file", prepare: func(b *InstallBundle) {
			b.Files[0].Mode = 0o666
			b.Directories[0].Group = "Bad"
		}, want: "installation directory owner or group is invalid"},
		{name: "earlier file first", prepare: func(b *InstallBundle) {
			b.Files[0].Owner = "Bad"
			b.Files[1].Mode = 0o666
		}, want: "installation file owner or group is invalid"},
		{name: "host before root", prepare: func(b *InstallBundle) { b.Files = nil }, want: "installation bundle is incomplete or contains an invalid state directory"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := setupR3Bundle()
			test.prepare(&bundle)
			operations := &setupR3RecordingHost{}
			err := ApplyInstallBundle(context.Background(), t.TempDir(), debianHost, bundle, operations)
			if err == nil || err.Error() != test.want {
				t.Fatalf("ApplyInstallBundle = %v, want %q", err, test.want)
			}
			if len(operations.calls) != 0 {
				t.Fatalf("host changed before the rejection: %q", operations.calls)
			}
		})
	}
	// An unsupported host is refused before an incomplete bundle.
	err := ApplyInstallBundle(context.Background(), "relative", Host{OSReleaseID: "debian"}, InstallBundle{}, &setupR3RecordingHost{})
	if err == nil || err.Error() != "installation requires a supported Debian/systemd host" {
		t.Fatalf("ApplyInstallBundle = %v", err)
	}
	// A relative root is refused before an incomplete bundle.
	err = ApplyInstallBundle(context.Background(), "relative", debianHost, InstallBundle{}, &setupR3RecordingHost{})
	if err == nil || err.Error() != "installation root must be absolute" {
		t.Fatalf("ApplyInstallBundle = %v", err)
	}
}

func TestSetupR3ApplyExactHostOperationsAndFiles(t *testing.T) {
	root := t.TempDir()
	bundle := setupR3Bundle()
	operations := &setupR3RecordingHost{}
	if err := ApplyInstallBundle(context.Background(), root+"/.", debianHost, bundle, operations); err != nil {
		t.Fatal(err)
	}
	q := regexp.QuoteMeta
	setupR3MatchCalls(t, operations.calls,
		q("ensure gameserver-controller "+root+"/var/lib/gsc"),
		q("chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller"),
		q("chown "+root+"/etc/gsc/.gsc-install-")+`\d+`+q(" root:gameserver-controller"),
		q("chown "+root+"/usr/local/bin/.gsc-install-")+`\d+`+q(" root:root"),
		"reload",
	)
	for path, mode := range map[string]fs.FileMode{
		"/etc": 0o755, "/etc/gsc": 0o755, "/usr": 0o755, "/usr/local": 0o755, "/usr/local/bin": 0o755, "/var": 0o755, "/var/lib": 0o755,
	} {
		info, err := os.Lstat(mapRootPath(root, path))
		if err != nil || !info.IsDir() || info.Mode().Perm() != mode {
			t.Fatalf("%s = %v, %v; want a %#o directory", path, info, err, mode)
		}
	}
	info, err := os.Lstat(mapRootPath(root, "/var/lib/gsc"))
	if err != nil || !info.IsDir() || info.Mode() != fs.ModeDir|0o750 {
		t.Fatalf("state directory = %v, %v", info, err)
	}
	for _, file := range bundle.Files {
		path := mapRootPath(root, file.Path)
		info, err := os.Lstat(path)
		content, readErr := os.ReadFile(path)
		if err != nil || readErr != nil || info.Mode() != file.Mode || !bytes.Equal(content, file.Content) {
			t.Fatalf("%s = %v %q (%v %v)", file.Path, info, content, err, readErr)
		}
	}
	for _, dir := range []string{"/etc/gsc", "/usr/local/bin"} {
		if entries := setupR3Entries(t, mapRootPath(root, dir)); len(entries) != 1 {
			t.Fatalf("%s holds %q, want only the installed file", dir, entries)
		}
	}

	// A second apply keeps the identical files and re-asserts ownership on
	// the final paths, not on temporary copies.
	operations = &setupR3RecordingHost{}
	if err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations); err != nil {
		t.Fatal(err)
	}
	setupR3MatchCalls(t, operations.calls,
		q("ensure gameserver-controller "+root+"/var/lib/gsc"),
		q("chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller"),
		q("chown "+root+"/etc/gsc/config.yaml root:gameserver-controller"),
		q("chown "+root+"/usr/local/bin/controller root:root"),
		"reload",
	)
}

func TestSetupR3ApplyReloadFailureRollsBackOnlyNewFiles(t *testing.T) {
	root := t.TempDir()
	bundle := setupR3Bundle()
	writeAt(t, root, bundle.Files[1].Path, bundle.Files[1].Mode, string(bundle.Files[1].Content))
	operations := &setupR3RecordingHost{reloadErr: errors.New("systemd unavailable")}
	err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations)
	if err == nil || err.Error() != "systemd daemon reload failed; newly installed files were removed" {
		t.Fatalf("ApplyInstallBundle = %v", err)
	}
	q := regexp.QuoteMeta
	setupR3MatchCalls(t, operations.calls,
		q("ensure gameserver-controller "+root+"/var/lib/gsc"),
		q("chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller"),
		q("chown "+root+"/etc/gsc/.gsc-install-")+`\d+`+q(" root:gameserver-controller"),
		q("chown "+root+"/usr/local/bin/controller root:root"),
		"reload",
		"reload", // the unit files are reloaded again after the rollback
	)
	if setupR3Exists(mapRootPath(root, bundle.Files[0].Path)) {
		t.Fatal("the newly installed file survived the rollback")
	}
	if !setupR3Exists(mapRootPath(root, bundle.Files[1].Path)) {
		t.Fatal("the pre-existing identical file was removed")
	}
	// Directories created by the install are kept.
	for _, dir := range []string{"/var/lib/gsc", "/etc/gsc"} {
		if !setupR3Exists(mapRootPath(root, dir)) {
			t.Fatalf("directory %s was removed by the rollback", dir)
		}
	}
}

func TestSetupR3ApplyFileFailureLeavesNoTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	bundle := setupR3Bundle()
	operations := &setupR3RecordingHost{chownErr: func(path string) error {
		if strings.HasPrefix(filepath.Base(path), ".gsc-install-") && strings.Contains(path, "/usr/local/bin/") {
			return errors.New("chown failed")
		}
		return nil
	}}
	err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations)
	if err == nil || err.Error() != "install controller file failed" {
		t.Fatalf("ApplyInstallBundle = %v", err)
	}
	if last := operations.calls[len(operations.calls)-1]; !strings.Contains(last, "/usr/local/bin/.gsc-install-") {
		t.Fatalf("installation continued after the failure: %q", operations.calls)
	}
	for _, dir := range []string{"/etc/gsc", "/usr/local/bin"} {
		if entries := setupR3Entries(t, mapRootPath(root, dir)); len(entries) != 0 {
			t.Fatalf("%s holds %q after a rolled-back install", dir, entries)
		}
	}
}

func TestSetupR3ApplyServiceAccountFailureChangesNoFiles(t *testing.T) {
	root := t.TempDir()
	operations := &setupR3RecordingHost{ensureErr: errors.New("useradd failed")}
	err := ApplyInstallBundle(context.Background(), root, debianHost, setupR3Bundle(), operations)
	if err == nil || err.Error() != "create or validate controller service account failed" {
		t.Fatalf("ApplyInstallBundle = %v", err)
	}
	setupR3MatchCalls(t, operations.calls, regexp.QuoteMeta("ensure gameserver-controller "+root+"/var/lib/gsc"))
	if entries := setupR3Entries(t, root); len(entries) != 0 {
		t.Fatalf("root changed: %q", entries)
	}
}

func TestSetupR3ApplyAcceptsExistingDirectoryWithTheExactMode(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(mapRootPath(root, "/var/lib/gsc"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mapRootPath(root, "/var/lib/gsc"), 0o750); err != nil {
		t.Fatal(err)
	}
	operations := &setupR3RecordingHost{}
	if err := ApplyInstallBundle(context.Background(), root, debianHost, setupR3Bundle(), operations); err != nil {
		t.Fatal(err)
	}
	if operations.calls[1] != "chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller" {
		t.Fatalf("existing directory ownership was not set: %q", operations.calls)
	}
}

func TestSetupR3ApplyDirectoryPreparationFailures(t *testing.T) {
	t.Run("a second entry for the same directory with another mode", func(t *testing.T) {
		root := t.TempDir()
		bundle := setupR3Bundle()
		bundle.Directories = append(bundle.Directories, InstallDirectory{Path: "/var/lib/gsc", Mode: 0o700, Owner: "root", Group: "root"})
		operations := &setupR3RecordingHost{}
		err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations)
		if err == nil || err.Error() != "prepare private installation directory failed" {
			t.Fatalf("ApplyInstallBundle = %v", err)
		}
		setupR3MatchCalls(t, operations.calls,
			regexp.QuoteMeta("ensure gameserver-controller "+root+"/var/lib/gsc"),
			regexp.QuoteMeta("chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller"),
		)
		if setupR3Exists(mapRootPath(root, "/etc")) {
			t.Fatal("files were installed after a directory failure")
		}
	})
	t.Run("a parent inside a read-only directory", func(t *testing.T) {
		setupR3SkipIfRoot(t)
		root := t.TempDir()
		setupR3ReadOnlyCleanup(t, mapRootPath(root, "/opt/ro"))
		bundle := setupR3Bundle()
		bundle.Directories = []InstallDirectory{
			{Path: "/opt/ro", Mode: 0o500, Owner: "root", Group: "root"},
			{Path: "/opt/ro/sub/dir", Mode: 0o750, Owner: "root", Group: "root"},
		}
		operations := &setupR3RecordingHost{}
		err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations)
		if err == nil || err.Error() != "prepare installation directory failed" {
			t.Fatalf("ApplyInstallBundle = %v", err)
		}
		if len(operations.calls) != 2 {
			t.Fatalf("host calls = %q", operations.calls)
		}
	})
	t.Run("a directory inside a read-only directory", func(t *testing.T) {
		setupR3SkipIfRoot(t)
		root := t.TempDir()
		setupR3ReadOnlyCleanup(t, mapRootPath(root, "/opt/ro"))
		bundle := setupR3Bundle()
		bundle.Directories = []InstallDirectory{
			{Path: "/opt/ro", Mode: 0o500, Owner: "root", Group: "root"},
			{Path: "/opt/ro/dir", Mode: 0o750, Owner: "root", Group: "root"},
		}
		operations := &setupR3RecordingHost{}
		err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations)
		if err == nil || err.Error() != "prepare private installation directory failed" {
			t.Fatalf("ApplyInstallBundle = %v", err)
		}
	})
}

func TestSetupR3ApplyFilePreparationFailuresRollBack(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "file parent inside a read-only directory", path: "/opt/ro/sub/file", want: "prepare installation file directory failed"},
		{name: "file inside a read-only directory", path: "/opt/ro/file", want: "install controller file failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setupR3SkipIfRoot(t)
			root := t.TempDir()
			setupR3ReadOnlyCleanup(t, mapRootPath(root, "/opt/ro"))
			bundle := setupR3Bundle()
			bundle.Directories = append(bundle.Directories, InstallDirectory{Path: "/opt/ro", Mode: 0o500, Owner: "root", Group: "root"})
			bundle.Files = append(bundle.Files, InstallFile{Path: test.path, Mode: 0o644, Owner: "root", Group: "root", Content: []byte("x")})
			operations := &setupR3RecordingHost{}
			err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations)
			if err == nil || err.Error() != test.want {
				t.Fatalf("ApplyInstallBundle = %v, want %q", err, test.want)
			}
			for _, file := range bundle.Files[:2] {
				if setupR3Exists(mapRootPath(root, file.Path)) {
					t.Fatalf("%s survived the rollback", file.Path)
				}
			}
			if last := operations.calls[len(operations.calls)-1]; last == "reload" {
				t.Fatalf("systemd was reloaded after a failed install: %q", operations.calls)
			}
		})
	}
}
