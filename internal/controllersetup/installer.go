package controllersetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/amibamae/gameserver-controller/internal/config"
)

const (
	serviceAccount = "gameserver-controller"
)

var accountNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)

type InstallFile struct {
	Path    string
	Mode    fs.FileMode
	Owner   string
	Group   string
	Content []byte
}

type InstallDirectory struct {
	Path  string
	Mode  fs.FileMode
	Owner string
	Group string
}

type InstallBundle struct {
	Files       []InstallFile
	Directories []InstallDirectory
	StateDir    string
}

// InstallerHost contains the few host operations that cannot be performed by
// writing files. Tests inject a fake; production calls the Debian tools.
type InstallerHost interface {
	EnsureServiceAccount(context.Context, string, string) error
	Chown(context.Context, string, string, string) error
	DaemonReload(context.Context) error
}

// BuildInstallBundle renders the configuration, service unit, and controller
// executable without changing the host. Applying the bundle installs the unit
// but does not enable or start the service; the API listener is independently
// controlled by the validated configuration.
func BuildInstallBundle(value config.Config, layout Layout, controllerBinary []byte) (InstallBundle, error) {
	if err := config.Validate(value); err != nil {
		return InstallBundle{}, errors.New("controller configuration is invalid")
	}
	if len(controllerBinary) == 0 {
		return InstallBundle{}, errors.New("controller executable is empty")
	}
	for name, path := range map[string]string{
		"configuration": layout.ConfigFile,
		"systemd unit":  layout.ServiceFile,
		"controller":    layout.BinaryFile,
		"state":         layout.StateDir,
		"credentials":   layout.CredentialDir,
	} {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') || filepath.Clean(path) != path {
			return InstallBundle{}, fmt.Errorf("%s destination must be a clean absolute path", name)
		}
		if !systemdSafePath(path) {
			return InstallBundle{}, fmt.Errorf("%s destination contains unsupported characters", name)
		}
	}
	if layout.StateDatabaseFile == "" {
		return InstallBundle{}, errors.New("planned controller state database path is required")
	}
	if !filepath.IsAbs(layout.StateDatabaseFile) || filepath.Clean(layout.StateDatabaseFile) != layout.StateDatabaseFile {
		return InstallBundle{}, errors.New("state database destination must be a clean absolute path")
	}
	if !pathWithin(layout.StateDir, layout.StateDatabaseFile) || layout.StateDatabaseFile == layout.StateDir {
		return InstallBundle{}, errors.New("state database must stay inside the private state directory")
	}
	if pathsOverlap(layout.StateDir, layout.CredentialDir) {
		return InstallBundle{}, errors.New("state and credential directories must not overlap")
	}
	for _, filePath := range []string{layout.ConfigFile, layout.ServiceFile, layout.BinaryFile} {
		if pathWithin(layout.StateDir, filePath) || pathWithin(layout.CredentialDir, filePath) {
			return InstallBundle{}, errors.New("installation files must stay outside state and credential directories")
		}
	}
	if layout.ConfigFile == layout.StateDatabaseFile || layout.ServiceFile == layout.BinaryFile {
		return InstallBundle{}, errors.New("installation destinations overlap")
	}
	seenPaths := make(map[string]struct{})
	for _, path := range []string{layout.ConfigFile, layout.ServiceFile, layout.BinaryFile} {
		if _, exists := seenPaths[path]; exists {
			return InstallBundle{}, errors.New("installation file destinations must be unique")
		}
		seenPaths[path] = struct{}{}
	}

	value.Controller.StateStore.Path = layout.StateDatabaseFile
	// Adapter profiles loaded from adapters.d stay one file per adapter on the
	// installed host, so operators can still add or remove them by file.
	main := value
	main.Adapters = nil
	var adapterFiles []InstallFile
	adapterDir := filepath.Join(filepath.Dir(layout.ConfigFile), config.AdapterDirectoryName)
	for _, profile := range value.Adapters {
		if profile.Source == "" {
			main.Adapters = append(main.Adapters, profile)
			continue
		}
		content, err := yaml.Marshal(profile)
		if err != nil {
			return InstallBundle{}, errors.New("render validated adapter profile")
		}
		adapterFiles = append(adapterFiles, InstallFile{Path: filepath.Join(adapterDir, profile.Source), Mode: 0o640, Owner: "root", Group: serviceAccount, Content: content})
	}
	configBytes, err := yaml.Marshal(main)
	if err != nil {
		return InstallBundle{}, errors.New("render validated controller configuration")
	}
	unit := renderServiceUnit(value, layout)
	bundle := InstallBundle{
		StateDir: layout.StateDir,
		Directories: []InstallDirectory{
			{Path: filepath.Dir(layout.ConfigFile), Mode: 0o750, Owner: "root", Group: serviceAccount},
			{Path: layout.StateDir, Mode: 0o750, Owner: serviceAccount, Group: serviceAccount},
		},
		Files: append([]InstallFile{
			{Path: layout.BinaryFile, Mode: 0o755, Owner: "root", Group: "root", Content: append([]byte(nil), controllerBinary...)},
			{Path: layout.ConfigFile, Mode: 0o640, Owner: "root", Group: serviceAccount, Content: configBytes},
			{Path: layout.ServiceFile, Mode: 0o644, Owner: "root", Group: "root", Content: []byte(unit)},
		}, adapterFiles...),
	}
	if len(adapterFiles) > 0 {
		bundle.Directories = append(bundle.Directories, InstallDirectory{Path: adapterDir, Mode: 0o750, Owner: "root", Group: serviceAccount})
	}
	sort.Slice(bundle.Directories, func(i, j int) bool { return bundle.Directories[i].Path < bundle.Directories[j].Path })
	sort.Slice(bundle.Files, func(i, j int) bool { return bundle.Files[i].Path < bundle.Files[j].Path })
	return bundle, nil
}

func renderServiceUnit(value config.Config, layout Layout) string {
	var output strings.Builder
	output.WriteString("[Unit]\nDescription=Game Server Controller\nAfter=network-online.target\nWants=network-online.target\n\n")
	output.WriteString("[Service]\nType=simple\nUser=" + serviceAccount + "\nGroup=" + serviceAccount + "\n")
	output.WriteString("ExecStart=" + layout.BinaryFile + " serve --config " + layout.ConfigFile + "\n")
	output.WriteString("Restart=on-failure\nRestartSec=5s\nUMask=0077\n")
	if layout.StateDir == "/var/lib/gameserver-controller" {
		output.WriteString("StateDirectory=gameserver-controller\nStateDirectoryMode=0750\n")
	}
	output.WriteString("NoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=strict\nProtectHome=true\n")
	output.WriteString("ProtectKernelTunables=true\nProtectKernelModules=true\nProtectControlGroups=true\n")
	output.WriteString("LockPersonality=true\nRestrictSUIDSGID=true\nReadWritePaths=" + layout.StateDir + "\n")
	for _, name := range ConfiguredCredentialNames(value) {
		output.WriteString("LoadCredentialEncrypted=" + name + ":" + filepath.Join(layout.CredentialDir, name) + "\n")
	}
	output.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return output.String()
}

// ApplyInstallBundle installs only absent files. Exact existing files are
// accepted for idempotency; differing files are preserved and cause failure.
// It reloads systemd but never enables or starts the service.
func ApplyInstallBundle(ctx context.Context, root string, host Host, bundle InstallBundle, operations InstallerHost) error {
	if ctx == nil || operations == nil {
		return errors.New("installer context and host operations are required")
	}
	if err := ctx.Err(); err != nil {
		return errors.New("installation was canceled")
	}
	if host.OSReleaseID != "debian" || !host.Systemd {
		return errors.New("installation requires a supported Debian/systemd host")
	}
	if !filepath.IsAbs(root) {
		return errors.New("installation root must be absolute")
	}
	if !filepath.IsAbs(bundle.StateDir) || filepath.Clean(bundle.StateDir) != bundle.StateDir || len(bundle.Files) == 0 || len(bundle.Directories) == 0 {
		return errors.New("installation bundle is incomplete or contains an invalid state directory")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return errors.New("resolve installation root")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("installation root must be an existing real directory")
	}
	for _, dir := range bundle.Directories {
		if dir.Mode.Perm()&0o022 != 0 || dir.Mode&0o7000 != 0 {
			return errors.New("installation directory permissions are unsafe")
		}
		path, err := mapInstallPath(root, dir.Path)
		if err != nil {
			return err
		}
		if !accountNamePattern.MatchString(dir.Owner) || !accountNamePattern.MatchString(dir.Group) {
			return errors.New("installation directory owner or group is invalid")
		}
		if err := checkInstallParents(root, filepath.Dir(path)); err != nil {
			return err
		}
		if err := preflightInstallDirectory(path, dir.Mode); err != nil {
			return err
		}
	}
	for _, file := range bundle.Files {
		if file.Mode.Perm()&0o022 != 0 || file.Mode&0o7000 != 0 {
			return errors.New("installation file permissions are unsafe")
		}
		if !accountNamePattern.MatchString(file.Owner) || !accountNamePattern.MatchString(file.Group) {
			return errors.New("installation file owner or group is invalid")
		}
		path, err := mapInstallPath(root, file.Path)
		if err != nil {
			return err
		}
		if err := checkInstallParents(root, filepath.Dir(path)); err != nil {
			return err
		}
		if err := preflightInstallFile(path, file); err != nil {
			return err
		}
	}
	if err := operations.EnsureServiceAccount(ctx, serviceAccount, mapRootPath(root, bundle.StateDir)); err != nil {
		return errors.New("create or validate controller service account failed")
	}
	for _, dir := range bundle.Directories {
		path, _ := mapInstallPath(root, dir.Path)
		if err := ensureInstallParents(root, filepath.Dir(path)); err != nil {
			return errors.New("prepare installation directory failed")
		}
		if err := ensureDirectory(path, dir.Mode); err != nil {
			return errors.New("prepare private installation directory failed")
		}
		if err := operations.Chown(ctx, path, dir.Owner, dir.Group); err != nil {
			return errors.New("set installation directory ownership failed")
		}
	}
	created := make([]string, 0, len(bundle.Files))
	for _, file := range bundle.Files {
		path, _ := mapInstallPath(root, file.Path)
		if err := ensureInstallParents(root, filepath.Dir(path)); err != nil {
			rollbackFiles(created)
			return errors.New("prepare installation file directory failed")
		}
		wasCreated, err := installFile(ctx, path, file, operations)
		if err != nil {
			rollbackFiles(created)
			return errors.New("install controller file failed")
		}
		if wasCreated {
			created = append(created, path)
		}
	}
	if err := operations.DaemonReload(ctx); err != nil {
		rollbackFiles(created)
		_ = operations.DaemonReload(ctx)
		return errors.New("systemd daemon reload failed; newly installed files were removed")
	}
	return nil
}

func preflightInstallFile(path string, file InstallFile) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("installation target exists and is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(content, file.Content) || info.Mode().Perm() != file.Mode.Perm() {
		return fmt.Errorf("installation target already exists with different content: %s", path)
	}
	return nil
}

func installFile(ctx context.Context, path string, file InstallFile, operations InstallerHost) (bool, error) {
	if err := preflightInstallFile(path, file); err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := operations.Chown(ctx, path, file.Owner, file.Group); err != nil {
			return false, err
		}
		return false, nil
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".gsc-install-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(file.Mode); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if _, err := temporary.Write(file.Content); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := operations.Chown(ctx, temporaryPath, file.Owner, file.Group); err != nil {
		return false, err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return false, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		_ = os.Remove(path)
		return false, err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	return true, nil
}

func ensureDirectory(path string, mode fs.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, mode); err != nil {
			return err
		}
		return os.Chmod(path, mode)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("directory path is unavailable")
	}
	if info.Mode().Perm() != mode.Perm() {
		return errors.New("existing directory has unexpected permissions")
	}
	return nil
}

func preflightInstallDirectory(path string, mode fs.FileMode) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode.Perm() {
		return errors.New("installation directory exists with an unsafe type or mode")
	}
	return nil
}

func ensureInstallParents(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("installation path escaped root")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("installation path contains a non-directory or symlink")
		}
	}
	return nil
}

func checkInstallParents(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("installation path escaped root")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("installation path contains a non-directory or symlink")
		}
	}
	return nil
}

func systemdSafePath(path string) bool {
	for _, character := range path {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("_./-", character)) {
			return false
		}
	}
	return true
}

func pathWithin(parent, candidate string) bool {
	relative, err := filepath.Rel(parent, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathsOverlap(first, second string) bool {
	return pathWithin(first, second) || pathWithin(second, first)
}

func mapInstallPath(root, absolute string) (string, error) {
	if !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return "", errors.New("installation artifact path must be clean and absolute")
	}
	return mapRootPath(root, absolute), nil
}

func mapRootPath(root, absolute string) string {
	return filepath.Join(root, strings.TrimPrefix(filepath.Clean(absolute), string(filepath.Separator)))
}

func rollbackFiles(paths []string) {
	for i := len(paths) - 1; i >= 0; i-- {
		_ = os.Remove(paths[i])
	}
}

type SystemInstallerHost struct{}

func (SystemInstallerHost) EnsureServiceAccount(ctx context.Context, name, home string) error {
	output, err := exec.CommandContext(ctx, "getent", "passwd", name).Output()
	if err == nil {
		fields := strings.Split(strings.TrimSpace(string(output)), ":")
		if len(fields) != 7 || fields[0] != name || fields[5] != home || fields[6] != "/usr/sbin/nologin" {
			return errors.New("existing service account has unexpected identity")
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil || uid == 0 {
			return errors.New("existing service account has invalid UID")
		}
		groupOutput, err := exec.CommandContext(ctx, "getent", "group", name).Output()
		if err != nil {
			return errors.New("existing service account group is unavailable")
		}
		groupFields := strings.Split(strings.TrimSpace(string(groupOutput)), ":")
		if len(groupFields) < 3 || groupFields[0] != name || groupFields[2] != fields[3] {
			return errors.New("existing service account group does not match")
		}
		return nil
	}
	command := exec.CommandContext(ctx, "useradd", "--system", "--user-group", "--home-dir", home, "--shell", "/usr/sbin/nologin", name)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return errors.New("create controller service account")
	}
	return nil
}

func (SystemInstallerHost) Chown(ctx context.Context, path, owner, group string) error {
	if !accountNamePattern.MatchString(owner) || !accountNamePattern.MatchString(group) {
		return errors.New("invalid installation file owner or group")
	}
	command := exec.CommandContext(ctx, "chown", "--", owner+":"+group, path)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return errors.New("set controller file ownership")
	}
	return nil
}

func (SystemInstallerHost) DaemonReload(ctx context.Context) error {
	command := exec.CommandContext(ctx, "systemctl", "daemon-reload")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Run(); err != nil {
		return errors.New("reload systemd unit files")
	}
	return nil
}
