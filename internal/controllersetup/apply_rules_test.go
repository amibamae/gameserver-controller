package controllersetup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests apply a small bundle to a temporary root and pin the reason
// each unsafe host, path, mode, or existing file is rejected, and what the
// installer leaves behind when a host operation fails.

type scriptedInstallerHost struct {
	ensureErr error
	// chownErr fails Chown for any path whose base name contains chownFailOn.
	chownErr    error
	chownFailOn string
	reloadErr   error
	ensureCalls int
	chowned     []string
}

func (h *scriptedInstallerHost) EnsureServiceAccount(_ context.Context, name, home string) error {
	h.ensureCalls++
	if name != serviceAccount || !filepath.IsAbs(home) {
		return errors.New("unexpected account configuration")
	}
	return h.ensureErr
}

func (h *scriptedInstallerHost) Chown(_ context.Context, path, owner, group string) error {
	h.chowned = append(h.chowned, path)
	if h.chownErr != nil && strings.Contains(filepath.Base(path), h.chownFailOn) {
		return h.chownErr
	}
	return nil
}

func (h *scriptedInstallerHost) DaemonReload(context.Context) error { return h.reloadErr }

var debianHost = Host{OSReleaseID: "debian", Systemd: true}

func smallBundle() InstallBundle {
	return InstallBundle{
		StateDir: "/var/lib/gsc",
		Directories: []InstallDirectory{
			{Path: "/var/lib/gsc", Mode: 0o750, Owner: serviceAccount, Group: serviceAccount},
		},
		Files: []InstallFile{
			{Path: "/etc/gsc/config.yaml", Mode: 0o640, Owner: "root", Group: serviceAccount, Content: []byte("controller: {}\n")},
			{Path: "/etc/systemd/system/gsc.service", Mode: 0o644, Owner: "root", Group: "root", Content: []byte("[Service]\n")},
		},
	}
}

func writeAt(t *testing.T, root, path string, mode fs.FileMode, content string) {
	t.Helper()
	full := mapRootPath(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, mode); err != nil {
		t.Fatal(err)
	}
}

func TestApplyInstallBundleRejectsBeforeChangingTheHost(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	var missing context.Context
	tests := []struct {
		name    string
		ctx     context.Context
		host    Host
		root    func(t *testing.T) string
		prepare func(t *testing.T, root string, bundle *InstallBundle)
		want    string
	}{
		{name: "missing context", ctx: missing, want: "installer context and host operations are required"},
		{name: "canceled context", ctx: ended, want: "installation was canceled"},
		{name: "another distribution", host: Host{OSReleaseID: "ubuntu", Systemd: true}, want: "installation requires a supported Debian/systemd host"},
		{name: "no systemd", host: Host{OSReleaseID: "debian"}, want: "installation requires a supported Debian/systemd host"},
		{name: "relative root", root: func(*testing.T) string { return "relative/root" }, want: "installation root must be absolute"},
		{name: "relative state directory", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.StateDir = "var/lib/gsc" }, want: "installation bundle is incomplete"},
		{name: "unclean state directory", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.StateDir = "/var/lib/../gsc" }, want: "installation bundle is incomplete"},
		{name: "no files", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Files = nil }, want: "installation bundle is incomplete"},
		{name: "no directories", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Directories = nil }, want: "installation bundle is incomplete"},
		{name: "missing root", root: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent") }, want: "installation root must be an existing real directory"},
		{name: "root is a file", root: func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "file")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, want: "installation root must be an existing real directory"},
		{name: "root is a symlink", root: func(t *testing.T) string {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(t.TempDir(), link); err != nil {
				t.Fatal(err)
			}
			return link
		}, want: "installation root must be an existing real directory"},
		{name: "group-writable directory", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Directories[0].Mode = 0o770 }, want: "installation directory permissions are unsafe"},
		{name: "setgid directory", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Directories[0].Mode = fs.ModeSetgid | 0o750 }, want: "installation directory permissions are unsafe"},
		{name: "sticky directory", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Directories[0].Mode = fs.ModeSticky | 0o750 }, want: "installation directory permissions are unsafe"},
		{name: "raw setgid bits on a directory", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Directories[0].Mode = 0o2750 }, want: "installation directory permissions are unsafe"},
		{name: "relative directory path", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Directories[0].Path = "var/lib/gsc" }, want: "installation artifact path must be clean and absolute"},
		{name: "invalid directory owner", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Directories[0].Owner = "Root" }, want: "installation directory owner or group is invalid"},
		{name: "invalid directory group", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Directories[0].Group = "" }, want: "installation directory owner or group is invalid"},
		{name: "directory parent is a file", prepare: func(t *testing.T, root string, _ *InstallBundle) { writeAt(t, root, "/var/lib", 0o644, "") },
			want: "installation path contains a non-directory or symlink"},
		{name: "existing directory with another mode", prepare: func(t *testing.T, root string, _ *InstallBundle) {
			if err := os.MkdirAll(mapRootPath(root, "/var/lib/gsc"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, want: "installation directory exists with an unsafe type or mode"},
		{name: "world-writable file", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Files[1].Mode = 0o646 }, want: "installation file permissions are unsafe"},
		{name: "setuid file", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Files[0].Mode = fs.ModeSetuid | 0o640 }, want: "installation file permissions are unsafe"},
		{name: "setgid file", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Files[0].Mode = fs.ModeSetgid | 0o640 }, want: "installation file permissions are unsafe"},
		{name: "raw setuid bits on a file", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Files[0].Mode = 0o4640 }, want: "installation file permissions are unsafe"},
		{name: "invalid file group", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Files[0].Group = "gsc group" }, want: "installation file owner or group is invalid"},
		{name: "unclean file path", prepare: func(_ *testing.T, _ string, b *InstallBundle) { b.Files[0].Path = "/etc/gsc/../config.yaml" }, want: "installation artifact path must be clean and absolute"},
		{name: "file parent is a symlink", prepare: func(t *testing.T, root string, _ *InstallBundle) {
			if err := os.MkdirAll(mapRootPath(root, "/etc"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), mapRootPath(root, "/etc/gsc")); err != nil {
				t.Fatal(err)
			}
		}, want: "installation path contains a non-directory or symlink"},
		{name: "existing file with other content", prepare: func(t *testing.T, root string, _ *InstallBundle) {
			writeAt(t, root, "/etc/gsc/config.yaml", 0o640, "controller: {changed: true}\n")
		}, want: "installation target already exists with different content"},
		{name: "existing file with another mode", prepare: func(t *testing.T, root string, b *InstallBundle) {
			writeAt(t, root, "/etc/gsc/config.yaml", 0o600, string(b.Files[0].Content))
		}, want: "installation target already exists with different content"},
		{name: "existing target is a directory", prepare: func(t *testing.T, root string, _ *InstallBundle) {
			if err := os.MkdirAll(mapRootPath(root, "/etc/gsc/config.yaml"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, want: "installation target exists and is not a regular file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, host := test.ctx, test.host
			if ctx == nil && test.name != "missing context" {
				ctx = context.Background()
			}
			if host == (Host{}) {
				host = debianHost
			}
			root := t.TempDir()
			if test.root != nil {
				root = test.root(t)
			}
			bundle := smallBundle()
			if test.prepare != nil {
				test.prepare(t, root, &bundle)
			}
			operations := &scriptedInstallerHost{}
			err := ApplyInstallBundle(ctx, root, host, bundle, operations)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ApplyInstallBundle = %v, want an error containing %q", err, test.want)
			}
			if operations.ensureCalls != 0 || len(operations.chowned) != 0 {
				t.Fatalf("host changed before the rejection: %+v", operations)
			}
		})
	}
	if err := ApplyInstallBundle(context.Background(), t.TempDir(), debianHost, smallBundle(), nil); err == nil ||
		err.Error() != "installer context and host operations are required" {
		t.Fatalf("ApplyInstallBundle without host operations = %v", err)
	}
}

func TestApplyInstallBundleInstallsTheExactBundle(t *testing.T) {
	root := t.TempDir()
	bundle := smallBundle()
	operations := &scriptedInstallerHost{}
	if err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(mapRootPath(root, "/var/lib/gsc"))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o750 {
		t.Fatalf("state directory = %v, %v; want a 0750 directory", info, err)
	}
	for _, parent := range []string{"/etc", "/etc/gsc", "/etc/systemd/system", "/var/lib"} {
		if info, err := os.Lstat(mapRootPath(root, parent)); err != nil || !info.IsDir() || info.Mode().Perm() != 0o755 {
			t.Fatalf("parent %s = %v, %v; want a 0755 directory", parent, info, err)
		}
	}
	for _, file := range bundle.Files {
		path := mapRootPath(root, file.Path)
		content, err := os.ReadFile(path)
		info, statErr := os.Lstat(path)
		if err != nil || statErr != nil || string(content) != string(file.Content) || info.Mode().Perm() != file.Mode.Perm() {
			t.Fatalf("installed %s = %q (%v), %v %v", file.Path, content, info, err, statErr)
		}
	}
	leftovers, _ := filepath.Glob(mapRootPath(root, "/etc/gsc/.gsc-install-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary install files left behind: %v", leftovers)
	}
	// The directory and each file's temporary copy are chowned before use.
	if len(operations.chowned) != 3 || operations.chowned[0] != mapRootPath(root, "/var/lib/gsc") {
		t.Fatalf("chowned = %v", operations.chowned)
	}
}

func TestApplyInstallBundleHostFailures(t *testing.T) {
	tests := []struct {
		name      string
		host      scriptedInstallerHost
		existing  bool
		want      string
		remaining []string
	}{
		{name: "service account fails", host: scriptedInstallerHost{ensureErr: errors.New("useradd failed")},
			want: "create or validate controller service account failed"},
		{name: "directory ownership fails", host: scriptedInstallerHost{chownErr: errors.New("chown failed"), chownFailOn: "gsc"},
			want: "set installation directory ownership failed"},
		{name: "file ownership fails", host: scriptedInstallerHost{chownErr: errors.New("chown failed"), chownFailOn: ".gsc-install-"},
			want: "install controller file failed"},
		{name: "reload fails after an identical file was kept", host: scriptedInstallerHost{reloadErr: errors.New("systemd unavailable")}, existing: true,
			want: "systemd daemon reload failed; newly installed files were removed", remaining: []string{"/etc/gsc/config.yaml"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bundle := smallBundle()
			if test.existing {
				writeAt(t, root, bundle.Files[0].Path, bundle.Files[0].Mode, string(bundle.Files[0].Content))
			}
			operations := test.host
			err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, &operations)
			if err == nil || err.Error() != test.want {
				t.Fatalf("ApplyInstallBundle = %v, want %q", err, test.want)
			}
			kept := map[string]bool{}
			for _, path := range test.remaining {
				kept[path] = true
			}
			for _, file := range bundle.Files {
				_, statErr := os.Lstat(mapRootPath(root, file.Path))
				if kept[file.Path] != (statErr == nil) {
					t.Fatalf("%s present=%v, want present=%v", file.Path, statErr == nil, kept[file.Path])
				}
			}
		})
	}
}

func TestApplyInstallBundleRollsBackEarlierFilesWhenALaterFileFails(t *testing.T) {
	root := t.TempDir()
	bundle := smallBundle()
	failing := &failSecondFileHost{scriptedInstallerHost: &scriptedInstallerHost{}}
	err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, failing)
	if err == nil || err.Error() != "install controller file failed" {
		t.Fatalf("ApplyInstallBundle = %v, want the file install failure", err)
	}
	if _, err := os.Lstat(mapRootPath(root, bundle.Files[0].Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first file survived a failed install: %v", err)
	}
}

// failSecondFileHost fails the ownership change of the second file's
// temporary copy, after the first file has been installed.
type failSecondFileHost struct {
	*scriptedInstallerHost
	files int
}

func (h *failSecondFileHost) Chown(ctx context.Context, path, owner, group string) error {
	if strings.Contains(filepath.Base(path), ".gsc-install-") {
		h.files++
		if h.files == 2 {
			return errors.New("chown failed")
		}
	}
	return h.scriptedInstallerHost.Chown(ctx, path, owner, group)
}
