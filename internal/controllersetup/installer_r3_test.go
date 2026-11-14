package controllersetup

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These characterization tests pin the exact reason ApplyInstallBundle
// refuses a bundle, the #228 special-mode refusal, every host operation it
// performs (in order), and what it leaves behind on failure. They were
// written against the code before the complexity refactor and must keep
// passing unchanged.

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

// setupR3Temporary matches a chown of a temporary copy in directory.
func setupR3Temporary(directory, owner string) string {
	return regexp.QuoteMeta("chown "+directory+"/.gsc-install-") + `\d+` + regexp.QuoteMeta(" "+owner)
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

// setupR3ReadOnlyCleanup makes a directory writable again so t.TempDir can
// remove it.
func setupR3ReadOnlyCleanup(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o755); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore %s: %v", path, err)
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

// setupR3Apply applies the bundle under root and checks the exact error.
func setupR3Apply(t *testing.T, root string, bundle InstallBundle, operations InstallerHost, want string) {
	t.Helper()
	err := ApplyInstallBundle(context.Background(), root, debianHost, bundle, operations)
	got := ""
	if err != nil {
		got = err.Error()
	}
	if got != want {
		t.Fatalf("ApplyInstallBundle = %q, want %q", got, want)
	}
}

// setupR3AssertRefusedUntouched checks a refusal that changed nothing.
func setupR3AssertRefusedUntouched(t *testing.T, bundle InstallBundle, want string) {
	t.Helper()
	root := t.TempDir()
	operations := &setupR3RecordingHost{}
	setupR3Apply(t, root, bundle, operations, want)
	if len(operations.calls) != 0 {
		t.Fatalf("host changed before the rejection: %q", operations.calls)
	}
	if entries := setupR3Entries(t, root); len(entries) != 0 {
		t.Fatalf("root changed before the rejection: %q", entries)
	}
}

// setupR3AssertModes checks the full mode (type and permissions) of paths.
func setupR3AssertModes(t *testing.T, root string, modes map[string]fs.FileMode) {
	t.Helper()
	for path, mode := range modes {
		info, err := os.Lstat(mapRootPath(root, path))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if info.Mode() != mode {
			t.Fatalf("%s mode = %v, want %v", path, info.Mode(), mode)
		}
	}
}

func setupR3AssertContents(t *testing.T, root string, files []InstallFile) {
	t.Helper()
	for _, file := range files {
		content, err := os.ReadFile(mapRootPath(root, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(content, file.Content) {
			t.Fatalf("%s = %q, want %q", file.Path, content, file.Content)
		}
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

var setupR3SpecialBits = map[string]fs.FileMode{
	"setuid": fs.ModeSetuid, "setgid": fs.ModeSetgid, "sticky": fs.ModeSticky,
	"raw setuid": 0o4000, "raw setgid": 0o2000, "raw sticky": 0o1000,
	"group write": 0o020, "world write": 0o002,
}

func TestSetupR3ApplyRejectsEverySpecialModeOnFiles(t *testing.T) {
	for name, bit := range setupR3SpecialBits {
		t.Run(name, func(t *testing.T) {
			bundle := setupR3Bundle()
			bundle.Files[1].Mode |= bit
			setupR3AssertRefusedUntouched(t, bundle, "installation file permissions are unsafe")
		})
	}
}

func TestSetupR3ApplyRejectsEverySpecialModeOnDirectories(t *testing.T) {
	for name, bit := range setupR3SpecialBits {
		t.Run(name, func(t *testing.T) {
			bundle := setupR3Bundle()
			bundle.Directories[0].Mode |= bit
			setupR3AssertRefusedUntouched(t, bundle, "installation directory permissions are unsafe")
		})
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bundle := setupR3Bundle()
			test.prepare(&bundle)
			setupR3AssertRefusedUntouched(t, bundle, test.want)
		})
	}
}

func TestSetupR3ApplyHostAndRootBeforeBundle(t *testing.T) {
	for _, test := range []struct {
		host Host
		root string
		want string
	}{
		{host: Host{OSReleaseID: "debian"}, root: "relative", want: "installation requires a supported Debian/systemd host"},
		{host: debianHost, root: "relative", want: "installation root must be absolute"},
		{host: debianHost, root: "/", want: "installation bundle is incomplete or contains an invalid state directory"},
	} {
		err := ApplyInstallBundle(context.Background(), test.root, test.host, InstallBundle{}, &setupR3RecordingHost{})
		if err == nil || err.Error() != test.want {
			t.Fatalf("ApplyInstallBundle(%+v, %q) = %v, want %q", test.host, test.root, err, test.want)
		}
	}
}

func TestSetupR3ApplyExactHostOperationsAndFiles(t *testing.T) {
	root := t.TempDir()
	bundle := setupR3Bundle()
	operations := &setupR3RecordingHost{}
	setupR3Apply(t, root+"/.", bundle, operations, "")
	q := regexp.QuoteMeta
	setupR3MatchCalls(t, operations.calls,
		q("ensure gameserver-controller "+root+"/var/lib/gsc"),
		q("chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller"),
		setupR3Temporary(root+"/etc/gsc", "root:gameserver-controller"),
		setupR3Temporary(root+"/usr/local/bin", "root:root"),
		"reload",
	)
	parent := fs.ModeDir | 0o755
	setupR3AssertModes(t, root, map[string]fs.FileMode{
		"/etc": parent, "/etc/gsc": parent, "/usr": parent, "/usr/local": parent, "/usr/local/bin": parent, "/var": parent, "/var/lib": parent,
		"/var/lib/gsc": fs.ModeDir | 0o750, "/etc/gsc/config.yaml": 0o640, "/usr/local/bin/controller": 0o755,
	})
	setupR3AssertContents(t, root, bundle.Files)
	setupR3AssertDeepEqual(t, "/etc/gsc entries", setupR3Entries(t, mapRootPath(root, "/etc/gsc")), []string{"config.yaml"})
	setupR3AssertDeepEqual(t, "/usr/local/bin entries", setupR3Entries(t, mapRootPath(root, "/usr/local/bin")), []string{"controller"})

	// A second apply keeps the identical files and re-asserts ownership on
	// the final paths, not on temporary copies.
	operations = &setupR3RecordingHost{}
	setupR3Apply(t, root, bundle, operations, "")
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
	setupR3Apply(t, root, bundle, operations, "systemd daemon reload failed; newly installed files were removed")
	q := regexp.QuoteMeta
	setupR3MatchCalls(t, operations.calls,
		q("ensure gameserver-controller "+root+"/var/lib/gsc"),
		q("chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller"),
		setupR3Temporary(root+"/etc/gsc", "root:gameserver-controller"),
		q("chown "+root+"/usr/local/bin/controller root:root"),
		"reload",
		"reload", // the unit files are reloaded again after the rollback
	)
	if setupR3Exists(mapRootPath(root, bundle.Files[0].Path)) {
		t.Fatal("the newly installed file survived the rollback")
	}
	// The pre-existing identical file and the created directories are kept.
	setupR3AssertModes(t, root, map[string]fs.FileMode{
		"/usr/local/bin/controller": 0o755, "/var/lib/gsc": fs.ModeDir | 0o750, "/etc/gsc": fs.ModeDir | 0o755,
	})
}

func TestSetupR3ApplyFileFailureLeavesNoTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	operations := &setupR3RecordingHost{chownErr: func(path string) error {
		if strings.Contains(path, "/usr/local/bin/.gsc-install-") {
			return errors.New("chown failed")
		}
		return nil
	}}
	setupR3Apply(t, root, setupR3Bundle(), operations, "install controller file failed")
	setupR3MatchCalls(t, operations.calls,
		regexp.QuoteMeta("ensure gameserver-controller "+root+"/var/lib/gsc"),
		regexp.QuoteMeta("chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller"),
		setupR3Temporary(root+"/etc/gsc", "root:gameserver-controller"),
		setupR3Temporary(root+"/usr/local/bin", "root:root"),
	)
	setupR3AssertDeepEqual(t, "/etc/gsc entries", setupR3Entries(t, mapRootPath(root, "/etc/gsc")), []string{})
	setupR3AssertDeepEqual(t, "/usr/local/bin entries", setupR3Entries(t, mapRootPath(root, "/usr/local/bin")), []string{})
}

func TestSetupR3ApplyServiceAccountFailureChangesNoFiles(t *testing.T) {
	root := t.TempDir()
	operations := &setupR3RecordingHost{ensureErr: errors.New("useradd failed")}
	setupR3Apply(t, root, setupR3Bundle(), operations, "create or validate controller service account failed")
	setupR3MatchCalls(t, operations.calls, regexp.QuoteMeta("ensure gameserver-controller "+root+"/var/lib/gsc"))
	setupR3AssertDeepEqual(t, "root entries", setupR3Entries(t, root), []string{})
}

func TestSetupR3ApplyAcceptsExistingDirectoryWithTheExactMode(t *testing.T) {
	root := t.TempDir()
	state := mapRootPath(root, "/var/lib/gsc")
	if err := os.MkdirAll(state, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o750); err != nil {
		t.Fatal(err)
	}
	operations := &setupR3RecordingHost{}
	setupR3Apply(t, root, setupR3Bundle(), operations, "")
	setupR3AssertDeepEqual(t, "second call", operations.calls[1], "chown "+state+" gameserver-controller:gameserver-controller")
}

func TestSetupR3ApplyDuplicateDirectoryWithAnotherMode(t *testing.T) {
	root := t.TempDir()
	bundle := setupR3Bundle()
	bundle.Directories = append(bundle.Directories, InstallDirectory{Path: "/var/lib/gsc", Mode: 0o700, Owner: "root", Group: "root"})
	operations := &setupR3RecordingHost{}
	setupR3Apply(t, root, bundle, operations, "prepare private installation directory failed")
	setupR3MatchCalls(t, operations.calls,
		regexp.QuoteMeta("ensure gameserver-controller "+root+"/var/lib/gsc"),
		regexp.QuoteMeta("chown "+root+"/var/lib/gsc gameserver-controller:gameserver-controller"),
	)
	if setupR3Exists(mapRootPath(root, "/etc")) {
		t.Fatal("files were installed after a directory failure")
	}
}

// setupR3ReadOnlyBundle adds a 0500 /opt/ro directory, then extra.
func setupR3ReadOnlyBundle(t *testing.T, root string) InstallBundle {
	t.Helper()
	setupR3SkipIfRoot(t)
	setupR3ReadOnlyCleanup(t, mapRootPath(root, "/opt/ro"))
	bundle := setupR3Bundle()
	bundle.Directories = append(bundle.Directories, InstallDirectory{Path: "/opt/ro", Mode: 0o500, Owner: "root", Group: "root"})
	return bundle
}

func TestSetupR3ApplyDirectoryInsideReadOnlyDirectory(t *testing.T) {
	for path, want := range map[string]string{
		"/opt/ro/sub/dir": "prepare installation directory failed",
		"/opt/ro/dir":     "prepare private installation directory failed",
	} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			bundle := setupR3ReadOnlyBundle(t, root)
			bundle.Directories = append(bundle.Directories, InstallDirectory{Path: path, Mode: 0o750, Owner: "root", Group: "root"})
			operations := &setupR3RecordingHost{}
			setupR3Apply(t, root, bundle, operations, want)
			if len(operations.calls) != 3 {
				t.Fatalf("host calls = %q", operations.calls)
			}
		})
	}
}

func TestSetupR3ApplyFileInsideReadOnlyDirectoryRollsBack(t *testing.T) {
	for path, want := range map[string]string{
		"/opt/ro/sub/file": "prepare installation file directory failed",
		"/opt/ro/file":     "install controller file failed",
	} {
		t.Run(path, func(t *testing.T) {
			root := t.TempDir()
			bundle := setupR3ReadOnlyBundle(t, root)
			bundle.Files = append(bundle.Files, InstallFile{Path: path, Mode: 0o644, Owner: "root", Group: "root", Content: []byte("x")})
			operations := &setupR3RecordingHost{}
			setupR3Apply(t, root, bundle, operations, want)
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

func TestSetupR3ApplyUsesTheCanonicalRoot(t *testing.T) {
	root := t.TempDir()
	operations := &setupR3RecordingHost{}
	// The root is cleaned lexically, so a missing "x" does not matter.
	setupR3Apply(t, filepath.Join(root, "x")+"/..//", setupR3Bundle(), operations, "")
	setupR3AssertDeepEqual(t, "first call", operations.calls[0], "ensure gameserver-controller "+root+"/var/lib/gsc")
	setupR3AssertContents(t, root, setupR3Bundle().Files)
}
