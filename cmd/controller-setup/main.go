// Command controller-setup previews and applies local installation, manages
// encrypted systemd credentials, and administers API keys on Debian hosts.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/authkey"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/controllersetup"
	"github.com/amibamae/gameserver-controller/internal/keyadmin"
	"github.com/amibamae/gameserver-controller/internal/managementapi"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "apply" {
		return runApplyCommand(args[1:], stdout, stderr, os.Geteuid() == 0, inspectHost(), "/", controllersetup.SystemInstallerHost{})
	}
	if len(args) > 0 && args[0] == "keys" {
		return runKeyCommand(args[1:], stdout, stderr, os.Geteuid() == 0, os.Geteuid())
	}
	if len(args) > 0 && args[0] == "credentials" {
		host := inspectHost()
		if host.OSReleaseID != "debian" || !host.Systemd {
			return errors.New("credential installation requires a Debian machine running systemd")
		}
		return runCredentialCommand(args[1:], stdout, stderr, os.Geteuid() == 0, askPassword, controllersetup.SystemdEncryptor{}, "/etc/credstore.encrypted")
	}
	return runWithHost(args, stdout, stderr, inspectHost())
}

func runApplyCommand(args []string, stdout, stderr io.Writer, isRoot bool, host controllersetup.Host, root string, operations controllersetup.InstallerHost) error {
	flags := flag.NewFlagSet("controller-setup apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "validated controller configuration")
	binaryPath := flags.String("binary", "", "built Linux controller executable")
	confirmation := flags.String("confirm", "", "type INSTALL-GAMESERVER-CONTROLLER to confirm local installation")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*binaryPath) == "" {
		return errors.New("apply requires --config and --binary")
	}
	if *confirmation != "INSTALL-GAMESERVER-CONTROLLER" {
		return errors.New("apply requires --confirm INSTALL-GAMESERVER-CONTROLLER")
	}
	if !isRoot {
		return errors.New("controller installation requires local administrator privileges")
	}
	if operations == nil {
		return errors.New("controller installation operations are unavailable")
	}
	value, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if value.Controller.ManagementAPI.Enabled {
		if _, err := managementapi.LoadTLSConfig(value.Controller.ManagementAPI.TLSCertificate, value.Controller.ManagementAPI.TLSPrivateKey); err != nil {
			return errors.New("enabled management API requires a valid, readable TLS certificate and protected private key before installation")
		}
	}
	binary, err := loadControllerBinary(*binaryPath)
	if err != nil {
		return err
	}
	plan, err := controllersetup.BuildPlan(value, host, defaultLayout())
	if err != nil {
		return err
	}
	if !plan.TargetSupported {
		return errors.New("installation target is unsupported; run the plan command to inspect blockers")
	}
	bundle, err := controllersetup.BuildInstallBundle(value, plan.Layout, binary)
	if err != nil {
		return err
	}
	if err := controllersetup.ApplyInstallBundle(context.Background(), root, host, bundle, operations); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Controller files installed and systemd reloaded. The service was not enabled, started, or stopped.")
	return nil
}

func loadControllerBinary(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("controller binary path must be clean and absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 || info.Size() <= 0 || info.Size() > 128<<20 {
		return nil, errors.New("controller binary must be a non-empty executable regular file no larger than 128 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open controller binary")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return nil, errors.New("controller binary changed while it was being inspected")
	}
	build, err := buildinfo.Read(file)
	if err != nil {
		return nil, errors.New("controller binary does not contain readable Go build metadata")
	}
	if build.Main.Path != "github.com/amibamae/gameserver-controller" {
		return nil, errors.New("binary is not a Go build of this controller project")
	}
	settings := make(map[string]string, len(build.Settings))
	for _, setting := range build.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["GOOS"] != "linux" || settings["GOARCH"] != runtime.GOARCH {
		return nil, errors.New("controller binary must target Linux and the current machine architecture")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("rewind controller binary")
	}
	binary, err := io.ReadAll(io.LimitReader(file, (128<<20)+1))
	if err != nil {
		return nil, errors.New("read controller binary")
	}
	if len(binary) == 0 || len(binary) > 128<<20 {
		return nil, errors.New("controller binary size is outside the supported range")
	}
	if !bytes.Contains(binary, []byte("gameserver-controller-runtime-v1")) {
		return nil, errors.New("Go executable is not the controller runtime binary")
	}
	return binary, nil
}

type scopeFlags []string

func (s *scopeFlags) String() string { return strings.Join(*s, ",") }
func (s *scopeFlags) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type localAdminAuthorizer struct{ isRoot bool }

func (a localAdminAuthorizer) AuthorizeIssue(context.Context, string, keyadmin.IssueRequest) error {
	if !a.isRoot {
		return errors.New("local administrator privileges are required")
	}
	return nil
}

func (a localAdminAuthorizer) AuthorizeRevoke(context.Context, string, string) error {
	if !a.isRoot {
		return errors.New("local administrator privileges are required")
	}
	return nil
}

type keyIssueOutput struct {
	ID        string          `json:"id"`
	Token     string          `json:"token"`
	ActorID   string          `json:"actor_id"`
	Audience  string          `json:"audience"`
	Scopes    []authkey.Scope `json:"scopes"`
	CreatedAt time.Time       `json:"created_at"`
	ExpiresAt time.Time       `json:"expires_at"`
}

func runKeyCommand(args []string, stdout, stderr io.Writer, isRoot bool, uid int) error {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: controller-setup keys {issue|revoke} [options]")
		return errors.New("invalid command")
	}
	operation := args[0]
	flags := flag.NewFlagSet("controller-setup keys "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("database", "", "existing configured bbolt database")
	actorID := flags.String("actor", "", "actor identity assigned to the new key")
	audience := flags.String("audience", "", "exact controller audience for the key")
	expiresIn := flags.String("expires-in", "", "required key lifetime (1m through 90d)")
	keyID := flags.String("key-id", "", "key identifier to revoke")
	confirmKeyID := flags.String("confirm-key-id", "", "repeat the exact key identifier to confirm revocation")
	var scopes scopeFlags
	flags.Var(&scopes, "scope", "exact permission:environment:resource scope; repeat for multiple scopes")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*databasePath) == "" {
		return errors.New("--database must name the existing controller database")
	}
	var issueRequest keyadmin.IssueRequest
	switch operation {
	case "issue":
		if *keyID != "" || *confirmKeyID != "" {
			return errors.New("issue does not accept revocation flags")
		}
		if strings.TrimSpace(*actorID) == "" || strings.TrimSpace(*audience) == "" || *expiresIn == "" || len(scopes) == 0 {
			return errors.New("issue requires --actor, --audience, --expires-in, and at least one --scope")
		}
		lifetime, err := time.ParseDuration(*expiresIn)
		if err != nil {
			return errors.New("--expires-in must be a Go duration between 1m and 90d")
		}
		parsedScopes := make([]authkey.Scope, 0, len(scopes))
		for _, value := range scopes {
			parts := strings.Split(value, ":")
			if len(parts) != 3 {
				return errors.New("each --scope must be permission:environment:resource")
			}
			parsedScopes = append(parsedScopes, authkey.Scope{Permission: parts[0], Environment: parts[1], Resource: parts[2]})
		}
		issueRequest = keyadmin.IssueRequest{ActorID: *actorID, Audience: *audience, Scopes: parsedScopes, Lifetime: lifetime}
	case "revoke":
		if *actorID != "" || *audience != "" || *expiresIn != "" || len(scopes) != 0 {
			return errors.New("revoke does not accept issuance flags")
		}
		if *keyID == "" || *confirmKeyID != *keyID {
			return errors.New("revoke requires --key-id and --confirm-key-id with the exact same identifier")
		}
	default:
		fmt.Fprintln(stderr, "usage: controller-setup keys {issue|revoke} [options]")
		return errors.New("invalid command")
	}
	if !isRoot {
		return errors.New("key administration requires local administrator privileges")
	}
	if err := requireExistingDatabase(*databasePath); err != nil {
		return err
	}
	db, err := store.OpenBolt(filepath.Clean(*databasePath))
	if err != nil {
		return err
	}
	defer db.Close()
	manager, err := authkey.NewManager(db)
	if err != nil {
		return err
	}
	service, err := keyadmin.NewService(manager, localAdminAuthorizer{isRoot: isRoot}, time.Now)
	if err != nil {
		return err
	}
	requesterID := "uid-" + strconv.Itoa(uid)
	if operation == "issue" {
		issued, err := service.Issue(context.Background(), requesterID, issueRequest)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(keyIssueOutput{ID: issued.ID, Token: issued.Secret, ActorID: issued.ActorID,
			Audience: issued.Audience, Scopes: issued.Scopes, CreatedAt: issued.CreatedAt, ExpiresAt: issued.ExpiresAt}); err != nil {
			return fmt.Errorf("could not write newly issued key %s; it may have been created, revoke it before retrying", issued.ID)
		}
		return nil
	}
	if err := service.Revoke(context.Background(), requesterID, *keyID); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "API key revoked.")
	return nil
}

func requireExistingDatabase(path string) error {
	directory, err := os.Lstat(filepath.Dir(path))
	if err != nil || directory.Mode()&os.ModeSymlink != 0 || !directory.IsDir() {
		return errors.New("controller database directory must be an existing directory, not a symlink")
	}
	if directory.Mode().Perm()&0o022 != 0 {
		return errors.New("controller database directory must not be writable by group or other users")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return errors.New("controller database must already exist; check --database")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("controller database must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("controller database has group or world permissions; restrict it before managing keys")
	}
	return nil
}

type passwordPrompt func(context.Context, string) ([]byte, error)

func runCredentialCommand(args []string, stdout, stderr io.Writer, isRoot bool, prompt passwordPrompt, encryptor controllersetup.Encryptor, credentialDirectory string) error {
	if len(args) == 0 || args[0] != "set" {
		fmt.Fprintln(stderr, "usage: controller-setup credentials set --config <config.yaml> --name <configured-credential> [--replace --confirm-replace <name>]")
		return errors.New("invalid command")
	}
	flags := flag.NewFlagSet("controller-setup credentials set", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "validated controller configuration")
	name := flags.String("name", "", "configured systemd credential name")
	replace := flags.Bool("replace", false, "replace an existing encrypted credential")
	confirmReplace := flags.String("confirm-replace", "", "repeat the exact credential name to confirm replacement")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*configPath) == "" || strings.TrimSpace(*name) == "" {
		return errors.New("--config and --name are required; no secret may be supplied as a command-line argument")
	}
	if !isRoot {
		return errors.New("credential installation must run with local administrator privileges")
	}
	if *replace != (*confirmReplace == *name) {
		return errors.New("replacement requires --replace and --confirm-replace with the exact credential name")
	}
	value, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if prompt == nil || encryptor == nil {
		return errors.New("secure credential input is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	first, err := prompt(ctx, "Enter provider credential for "+*name)
	if err != nil {
		return errors.New("secure credential prompt failed")
	}
	defer zeroSecret(first)
	second, err := prompt(ctx, "Re-enter provider credential for "+*name)
	if err != nil {
		return errors.New("credential confirmation prompt failed")
	}
	defer zeroSecret(second)
	if len(first) != len(second) || subtle.ConstantTimeCompare(first, second) != 1 {
		return errors.New("credential entries did not match")
	}
	if err := controllersetup.SetEncryptedCredential(ctx, value, *name, credentialDirectory, first, *replace, encryptor); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stored encrypted credential for configured reference %q. The value was not displayed.\n", *name)
	return nil
}

func askPassword(ctx context.Context, prompt string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "systemd-ask-password", "--timeout=90s", "-n", prompt)
	output, err := cmd.Output()
	if err != nil {
		zeroSecret(output)
		return nil, errors.New("secure prompt unavailable")
	}
	return output, nil
}

func zeroSecret(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func runWithHost(args []string, stdout, stderr io.Writer, host controllersetup.Host) error {
	if len(args) != 4 || args[0] != "plan" || args[1] != "--config" || strings.TrimSpace(args[2]) == "" || args[3] != "--json" {
		fmt.Fprintln(stderr, "usage: controller-setup plan --config <config.yaml> --json")
		return errors.New("invalid command")
	}
	value, err := config.Load(args[2])
	if err != nil {
		return err
	}
	plan, err := controllersetup.BuildPlan(value, host, defaultLayout())
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(plan)
}

func defaultLayout() controllersetup.Layout {
	return controllersetup.Layout{
		ConfigFile:    "/etc/gameserver-controller/controller.yaml",
		ServiceFile:   "/etc/systemd/system/gameserver-controller.service",
		BinaryFile:    "/usr/local/bin/controller",
		StateDir:      "/var/lib/gameserver-controller",
		CredentialDir: "/etc/credstore.encrypted",
	}
}

func inspectHost() controllersetup.Host {
	host := controllersetup.Host{}
	if runtime.GOOS != "linux" {
		host.OSReleaseID = runtime.GOOS
		return host
	}
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok && key == "ID" {
				host.OSReleaseID = strings.Trim(value, "\"'")
				break
			}
		}
	}
	_, err := os.Stat("/run/systemd/system")
	host.Systemd = err == nil
	return host
}
