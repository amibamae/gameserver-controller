package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/controllersetup"
)

// These characterization tests pin the plan and apply commands, the
// controller binary checks, the command dispatcher and the process exit
// behaviour. They were written against the code before the complexity
// refactor and must keep passing unchanged.

var setupR3Debian = controllersetup.Host{OSReleaseID: "debian", Systemd: true}

const setupR3ApplyUsage = "Usage of controller-setup apply:\n  -binary string\n    \tbuilt Linux controller executable\n  -config string\n    \tvalidated controller configuration\n  -confirm string\n    \ttype INSTALL-GAMESERVER-CONTROLLER to confirm local installation\n"

func setupR3ExampleConfig(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "configs", "controller.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// setupR3Build builds a package of this module into a new temporary file.
func setupR3Build(t *testing.T, pkg, goos string) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "binary")
	build := exec.Command("go", "build", "-o", output, pkg)
	build.Dir = filepath.Join("..", "..")
	build.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s for %s: %v\n%s", pkg, goos, err, out)
	}
	if err := os.Chmod(output, 0o755); err != nil {
		t.Fatal(err)
	}
	return output
}

// setupR3BuildForeign builds a Go program from another module.
func setupR3BuildForeign(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/other\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { println(\"gameserver-controller-runtime-v1\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "other")
	build := exec.Command("go", "build", "-o", output, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0", "GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build foreign program: %v\n%s", err, out)
	}
	return output
}

func setupR3WriteFile(t *testing.T, path string, mode fs.FileMode, content []byte) string {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func setupR3SkipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions; this case runs as an unprivileged user in CI")
	}
}

func setupR3Err(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// setupR3CmdHost records installer host operations.
type setupR3CmdHost struct{ calls []string }

func (h *setupR3CmdHost) EnsureServiceAccount(_ context.Context, name, home string) error {
	h.calls = append(h.calls, "ensure "+name+" "+home)
	return nil
}

func (h *setupR3CmdHost) Chown(_ context.Context, path, owner, group string) error {
	h.calls = append(h.calls, "chown "+filepath.Base(path)+" "+owner+":"+group)
	return nil
}

func (h *setupR3CmdHost) DaemonReload(context.Context) error {
	h.calls = append(h.calls, "reload")
	return nil
}

func TestSetupR3ApplyArgumentErrors(t *testing.T) {
	confirm := []string{"--confirm", "INSTALL-GAMESERVER-CONTROLLER"}
	example := setupR3ExampleConfig(t)
	tests := []struct {
		name       string
		args       []string
		isRoot     bool
		noOps      bool
		want       string
		wantStderr string
	}{
		{name: "help", args: []string{"-h"}, wantStderr: setupR3ApplyUsage},
		{name: "unknown flag", args: []string{"--bogus"}, want: "flag provided but not defined: -bogus", wantStderr: "flag provided but not defined: -bogus\n" + setupR3ApplyUsage},
		{name: "no arguments", want: "apply requires --config and --binary"},
		{name: "positional argument", args: []string{"--config", "/c", "--binary", "/b", "extra"}, want: "apply requires --config and --binary"},
		{name: "blank config", args: []string{"--config", "  ", "--binary", "/b"}, want: "apply requires --config and --binary"},
		{name: "blank binary", args: []string{"--config", "/c", "--binary", " "}, want: "apply requires --config and --binary"},
		{name: "missing confirmation", args: []string{"--config", "/c", "--binary", "/b"}, want: "apply requires --confirm INSTALL-GAMESERVER-CONTROLLER"},
		{name: "lowercase confirmation", args: []string{"--config", "/c", "--binary", "/b", "--confirm", "install-gameserver-controller"}, want: "apply requires --confirm INSTALL-GAMESERVER-CONTROLLER"},
		{name: "not root", args: append([]string{"--config", "/c", "--binary", "/b"}, confirm...), want: "controller installation requires local administrator privileges"},
		{name: "no operations", args: append([]string{"--config", "/c", "--binary", "/b"}, confirm...), isRoot: true, noOps: true, want: "controller installation operations are unavailable"},
		{name: "missing config", args: append([]string{"--config", "/missing/config.yaml", "--binary", "/b"}, confirm...), isRoot: true,
			want: "open configuration: open /missing/config.yaml: no such file or directory"},
		{name: "relative binary", args: append([]string{"--config", example, "--binary", "controller"}, confirm...), isRoot: true, want: "controller binary path must be clean and absolute"},
		{name: "missing binary", args: append([]string{"--config", example, "--binary", "/missing/controller"}, confirm...), isRoot: true,
			want: "controller binary must be a non-empty executable regular file no larger than 128 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var operations controllersetup.InstallerHost = &setupR3CmdHost{}
			if test.noOps {
				operations = nil
			}
			root := t.TempDir()
			var stdout, stderr bytes.Buffer
			err := runApplyCommand(test.args, &stdout, &stderr, test.isRoot, setupR3Debian, root, operations)
			if got := setupR3Err(err); got != test.want {
				t.Fatalf("apply = %q, want %q", got, test.want)
			}
			if stdout.Len() != 0 || stderr.String() != test.wantStderr {
				t.Fatalf("stdout=%q stderr=%q, want stderr %q", stdout.String(), stderr.String(), test.wantStderr)
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Fatalf("root changed: %v", entries)
			}
		})
	}
}

func TestSetupR3ApplyEnabledManagementAPIRequiresTLS(t *testing.T) {
	data, err := os.ReadFile(setupR3ExampleConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	enabled := strings.Replace(string(data), "management_api:\n    enabled: false", "management_api:\n    enabled: true", 1)
	if enabled == string(data) {
		t.Fatal("example config layout changed")
	}
	path := filepath.Join(t.TempDir(), "controller.yaml")
	setupR3WriteFile(t, path, 0o600, []byte(enabled))
	if _, err := config.Load(path); err != nil {
		t.Fatalf("enabled config does not load: %v", err)
	}
	var stdout, stderr bytes.Buffer
	err = runApplyCommand([]string{"--config", path, "--binary", "/missing", "--confirm", "INSTALL-GAMESERVER-CONTROLLER"}, &stdout, &stderr, true, setupR3Debian, t.TempDir(), &setupR3CmdHost{})
	want := "enabled management API requires a valid, readable TLS certificate and protected private key before installation"
	if got := setupR3Err(err); got != want {
		t.Fatalf("apply = %q, want %q", got, want)
	}
}

func TestSetupR3LoadControllerBinaryRejections(t *testing.T) {
	dir := t.TempDir()
	script := setupR3WriteFile(t, filepath.Join(dir, "script"), 0o755, []byte("#!/bin/sh\necho gameserver-controller-runtime-v1\n"))
	symlink := filepath.Join(dir, "link")
	if err := os.Symlink(script, symlink); err != nil {
		t.Fatal(err)
	}
	huge := setupR3WriteFile(t, filepath.Join(dir, "huge"), 0o755, nil)
	if err := os.Truncate(huge, 128<<20+1); err != nil {
		t.Fatal(err)
	}
	limit := setupR3WriteFile(t, filepath.Join(dir, "limit"), 0o755, nil)
	if err := os.Truncate(limit, 128<<20); err != nil {
		t.Fatal(err)
	}
	const shape = "controller binary must be a non-empty executable regular file no larger than 128 MiB"
	for path, want := range map[string]string{
		"controller":                  "controller binary path must be clean and absolute",
		"/usr/../controller":          "controller binary path must be clean and absolute",
		"/usr/local/bin/":             "controller binary path must be clean and absolute",
		filepath.Join(dir, "missing"): shape,
		dir:                           shape,
		symlink:                       shape,
		setupR3WriteFile(t, filepath.Join(dir, "plain"), 0o644, []byte("x")):          shape,
		setupR3WriteFile(t, filepath.Join(dir, "group-writable"), 0o775, []byte("x")): shape,
		setupR3WriteFile(t, filepath.Join(dir, "world-writable"), 0o757, []byte("x")): shape,
		setupR3WriteFile(t, filepath.Join(dir, "empty"), 0o755, nil):                  shape,
		huge:   shape,
		limit:  "controller binary does not contain readable Go build metadata",
		script: "controller binary does not contain readable Go build metadata",
		setupR3WriteFile(t, filepath.Join(dir, "owner-exec-only"), 0o100, []byte("x")): "controller binary does not contain readable Go build metadata",
	} {
		binary, err := loadControllerBinary(path)
		if got := setupR3Err(err); got != want || binary != nil {
			t.Errorf("loadControllerBinary(%q) = %d bytes, %q; want %q", path, len(binary), got, want)
		}
	}
}

func TestSetupR3LoadControllerBinaryUnreadable(t *testing.T) {
	setupR3SkipIfRoot(t)
	path := setupR3WriteFile(t, filepath.Join(t.TempDir(), "controller"), 0o111, []byte("x"))
	if _, err := loadControllerBinary(path); setupR3Err(err) != "open controller binary" {
		t.Fatalf("loadControllerBinary = %v", err)
	}
}

func TestSetupR3LoadControllerBinaryBuildChecks(t *testing.T) {
	t.Run("another module", func(t *testing.T) {
		if _, err := loadControllerBinary(setupR3BuildForeign(t)); setupR3Err(err) != "binary is not a Go build of this controller project" {
			t.Fatalf("loadControllerBinary = %v", err)
		}
	})
	t.Run("another operating system", func(t *testing.T) {
		if _, err := loadControllerBinary(setupR3Build(t, "./cmd/controller", "darwin")); setupR3Err(err) != "controller binary must target Linux and the current machine architecture" {
			t.Fatalf("loadControllerBinary = %v", err)
		}
	})
	t.Run("another command of this module", func(t *testing.T) {
		if _, err := loadControllerBinary(setupR3Build(t, "./cmd/protocol-check", "linux")); setupR3Err(err) != "Go executable is not the controller runtime binary" {
			t.Fatalf("loadControllerBinary = %v", err)
		}
	})
	t.Run("the controller", func(t *testing.T) {
		path := setupR3Build(t, "./cmd/controller", "linux")
		binary, err := loadControllerBinary(path)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := os.ReadFile(path)
		if !bytes.Equal(binary, want) {
			t.Fatal("loaded binary differs from the file")
		}
	})
}

func TestSetupR3ApplyInstallOutcomes(t *testing.T) {
	binary := setupR3Build(t, "./cmd/controller", "linux")
	example := setupR3ExampleConfig(t)
	args := []string{"--config", example, "--binary", binary, "--confirm", "INSTALL-GAMESERVER-CONTROLLER"}

	t.Run("unsupported host", func(t *testing.T) {
		root := t.TempDir()
		operations := &setupR3CmdHost{}
		var stdout, stderr bytes.Buffer
		err := runApplyCommand(args, &stdout, &stderr, true, controllersetup.Host{OSReleaseID: "ubuntu", Systemd: true}, root, operations)
		if got := setupR3Err(err); got != "installation target is unsupported; run the plan command to inspect blockers" {
			t.Fatalf("apply = %q", got)
		}
		if len(operations.calls) != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("calls=%q stdout=%q stderr=%q", operations.calls, stdout.String(), stderr.String())
		}
	})

	t.Run("conflicting existing file", func(t *testing.T) {
		root := t.TempDir()
		existing := filepath.Join(root, "etc", "gameserver-controller", "controller.yaml")
		if err := os.MkdirAll(filepath.Dir(existing), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(existing), 0o750); err != nil {
			t.Fatal(err)
		}
		setupR3WriteFile(t, existing, 0o640, []byte("other: true\n"))
		operations := &setupR3CmdHost{}
		var stdout, stderr bytes.Buffer
		err := runApplyCommand(args, &stdout, &stderr, true, setupR3Debian, root, operations)
		if got := setupR3Err(err); got != "installation target already exists with different content: "+existing {
			t.Fatalf("apply = %q", got)
		}
		if len(operations.calls) != 0 || stdout.Len() != 0 {
			t.Fatalf("calls=%q stdout=%q", operations.calls, stdout.String())
		}
	})

	t.Run("success", func(t *testing.T) {
		root := t.TempDir()
		operations := &setupR3CmdHost{}
		var stdout, stderr bytes.Buffer
		if err := runApplyCommand(args, &stdout, &stderr, true, setupR3Debian, root, operations); err != nil {
			t.Fatal(err)
		}
		if stdout.String() != "Controller files installed and systemd reloaded. The service was not enabled, started, or stopped.\n" || stderr.Len() != 0 {
			t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		want := []string{
			"ensure gameserver-controller " + filepath.Join(root, "var/lib/gameserver-controller"),
			"chown gameserver-controller root:gameserver-controller",
			"chown gameserver-controller gameserver-controller:gameserver-controller",
		}
		if len(operations.calls) != 7 || strings.Join(operations.calls[:3], "\n") != strings.Join(want, "\n") || operations.calls[6] != "reload" {
			t.Fatalf("calls = %q", operations.calls)
		}
		for i, owner := range []string{"root:gameserver-controller", "root:root", "root:root"} {
			if call := operations.calls[3+i]; !strings.HasPrefix(call, "chown .gsc-install-") || !strings.HasSuffix(call, " "+owner) {
				t.Fatalf("call %d = %q, want a temporary file chowned to %s", 3+i, call, owner)
			}
		}
		for path, mode := range map[string]fs.FileMode{
			"etc/gameserver-controller":                        fs.ModeDir | 0o750,
			"var/lib/gameserver-controller":                    fs.ModeDir | 0o750,
			"usr/local/bin/controller":                         0o755,
			"etc/gameserver-controller/controller.yaml":        0o640,
			"etc/systemd/system/gameserver-controller.service": 0o644,
		} {
			info, err := os.Lstat(filepath.Join(root, path))
			if err != nil || info.Mode() != mode {
				t.Fatalf("%s = %v, %v; want %v", path, info, err, mode)
			}
		}
		installed, _ := os.ReadFile(filepath.Join(root, "usr/local/bin/controller"))
		original, _ := os.ReadFile(binary)
		if !bytes.Equal(installed, original) {
			t.Fatal("installed controller differs from the built binary")
		}
		unit, _ := os.ReadFile(filepath.Join(root, "etc/systemd/system/gameserver-controller.service"))
		if !strings.Contains(string(unit), "ExecStart=/usr/local/bin/controller serve --config /etc/gameserver-controller/controller.yaml\n") ||
			!strings.Contains(string(unit), "StateDirectory=gameserver-controller\n") {
			t.Fatalf("unit = %s", unit)
		}
	})
}

func TestSetupR3PlanCommand(t *testing.T) {
	example := setupR3ExampleConfig(t)
	const usage = "usage: controller-setup plan --config <config.yaml> --json\n"
	for _, args := range [][]string{
		nil, {"plan"}, {"plan", "--config", example}, {"plan", "--config", example, "--yaml"}, {"plan", "--config", " ", "--json"},
		{"plan", "-config", example, "--json"}, {"preview", "--config", example, "--json"}, {"plan", "--config", example, "--json", "extra"},
	} {
		var stdout, stderr bytes.Buffer
		err := runWithHost(args, &stdout, &stderr, setupR3Debian)
		if setupR3Err(err) != "invalid command" || stderr.String() != usage || stdout.Len() != 0 {
			t.Fatalf("plan %q = %v, stdout=%q stderr=%q", args, err, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	err := runWithHost([]string{"plan", "--config", "/missing.yaml", "--json"}, &stdout, &stderr, setupR3Debian)
	if setupR3Err(err) != "open configuration: open /missing.yaml: no such file or directory" || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("plan = %v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	host := controllersetup.Host{OSReleaseID: "ubuntu"}
	if err := runWithHost([]string{"plan", "--config", example, "--json"}, &stdout, &stderr, host); err != nil {
		t.Fatal(err)
	}
	value, err := config.Load(example)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := controllersetup.BuildPlan(value, host, defaultLayout())
	if err != nil {
		t.Fatal(err)
	}
	want, _ := json.MarshalIndent(plan, "", "  ")
	if stdout.String() != string(want)+"\n" || stderr.Len() != 0 {
		t.Fatalf("plan output = %q, want %q", stdout.String(), want)
	}
}

func TestSetupR3DefaultLayout(t *testing.T) {
	want := controllersetup.Layout{
		ConfigFile:    "/etc/gameserver-controller/controller.yaml",
		ServiceFile:   "/etc/systemd/system/gameserver-controller.service",
		BinaryFile:    "/usr/local/bin/controller",
		StateDir:      "/var/lib/gameserver-controller",
		CredentialDir: "/etc/credstore.encrypted",
	}
	if got := defaultLayout(); got != want {
		t.Fatalf("defaultLayout = %+v", got)
	}
}

func TestSetupR3InspectHostMatchesThisMachine(t *testing.T) {
	host := inspectHost()
	_, err := os.Stat("/run/systemd/system")
	if host.Systemd != (err == nil) {
		t.Fatalf("Systemd = %t, stat error %v", host.Systemd, err)
	}
	want := ""
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if value, ok := strings.CutPrefix(line, "ID="); ok {
				want = strings.Trim(value, "\"'")
				break
			}
		}
	}
	if host.OSReleaseID != want {
		t.Fatalf("OSReleaseID = %q, want %q", host.OSReleaseID, want)
	}
}

func TestSetupR3RunDispatch(t *testing.T) {
	type result struct{ err, stdout, stderr string }
	tests := []struct {
		name string
		args []string
		want result
	}{
		{name: "no command", want: result{err: "invalid command", stderr: "usage: controller-setup plan --config <config.yaml> --json\n"}},
		{name: "unknown command", args: []string{"bogus"}, want: result{err: "invalid command", stderr: "usage: controller-setup plan --config <config.yaml> --json\n"}},
		{name: "apply", args: []string{"apply"}, want: result{err: "apply requires --config and --binary"}},
		{name: "apply help", args: []string{"apply", "-h"}, want: result{stderr: setupR3ApplyUsage}},
		{name: "keys", args: []string{"keys"}, want: result{err: "invalid command", stderr: "usage: controller-setup keys {issue|revoke} [options]\n"}},
	}
	host := inspectHost()
	if host.OSReleaseID != "debian" || !host.Systemd {
		tests = append(tests,
			struct {
				name string
				args []string
				want result
			}{name: "credentials off Debian/systemd", args: []string{"credentials", "set"}, want: result{err: "credential installation requires a Debian machine running systemd"}},
			struct {
				name string
				args []string
				want result
			}{name: "ssh-key off Debian/systemd", args: []string{"ssh-key", "generate"}, want: result{err: "SSH key installation requires a Debian machine running systemd"}},
		)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := run(test.args, &stdout, &stderr)
			got := result{err: setupR3Err(err), stdout: stdout.String(), stderr: stderr.String()}
			if got != test.want {
				t.Fatalf("run = %+v, want %+v", got, test.want)
			}
		})
	}
}

// TestSetupR3MainProcess runs main in a child process; it does nothing in
// the normal test run.
func TestSetupR3MainProcess(t *testing.T) {
	args := os.Getenv("SETUP_R3_MAIN_ARGS")
	if args == "" {
		return
	}
	os.Args = append([]string{"controller-setup"}, strings.Split(args, "\x1f")...)
	main()
	os.Exit(0)
}

func setupR3RunMain(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestSetupR3MainProcess$")
	command.Env = append(os.Environ(), "SETUP_R3_MAIN_ARGS="+strings.Join(args, "\x1f"))
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, stdout.String(), stderr.String()
}

func TestSetupR3MainExitBehaviour(t *testing.T) {
	code, stdout, stderr := setupR3RunMain(t, "bogus")
	if code != 1 || stdout != "" || stderr != "usage: controller-setup plan --config <config.yaml> --json\nerror: invalid command\n" {
		t.Fatalf("bogus: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = setupR3RunMain(t, "apply")
	if code != 1 || stdout != "" || stderr != "error: apply requires --config and --binary\n" {
		t.Fatalf("apply: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = setupR3RunMain(t, "apply", "-h")
	if code != 0 || stdout != "" || stderr != setupR3ApplyUsage {
		t.Fatalf("apply -h: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = setupR3RunMain(t, "plan", "--config", setupR3ExampleConfig(t), "--json")
	var plan map[string]any
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &plan) != nil || plan["target"] != "existing-machine" {
		t.Fatalf("plan: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
