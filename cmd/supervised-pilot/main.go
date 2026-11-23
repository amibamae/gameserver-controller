package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/pilotvultr"
)

const (
	gameUnit   = "minecraft-vanilla.service"
	backupUnit = "minecraft-backup.service"
	timerUnit  = "minecraft-backup.timer"
)

var safeSSHHost = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:-]*$`)
var safeInvocationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var safePilotServerID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var safeCredentialRef = regexp.MustCompile(`^systemd-credential://[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

const (
	defaultVultrCredential = credential.SystemdReferenceScheme + "vultr-pilot-api-token"
	defaultSSHCredential   = credential.SystemdReferenceScheme + "gsc-pilot-ssh-key"
)

type runtimeSettings struct {
	stateDBPath string
}

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	err := runWithSettings(args, stdout, stderr, runtimeSettings{})
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func runWithSettings(args []string, stdout, stderr io.Writer, settings runtimeSettings) error {
	if len(args) == 0 {
		return usage(stderr)
	}

	action := args[0]
	if !knownAction(action) {
		return unknownActionError(action)
	}

	flags := flag.NewFlagSet("gsc-pilot "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.String("host", "", "SSH config alias or user@host for the Minecraft server")
	flags.Bool("execute", false, "execute the selected action; mutating actions are dry runs by default")
	flags.String("confirm-host", "", "repeat the exact SSH target to approve a backup run")
	flags.String("confirm-adopt", "", "repeat <server-id>@<environment>/<instance-id> to approve local managed ownership")
	flags.String("instance-id", "", "exact existing Vultr instance ID for adoption")
	flags.String("expected-ip", "", "expected public IPv4 address for adoption")
	flags.String("os-name", "", "exact expected Vultr operating system name for adoption")
	flags.String("confirm-create", "", "repeat <server-id>@<environment> to approve billable Vultr VM creation")
	flags.Duration("timeout", time.Hour, "maximum time to wait for the selected operation")
	flags.String("server-id", "", "stable logical server ID for Vultr provisioning")
	flags.String("environment", "", "configured environment ID: production, staging, or testing")
	flags.String("region", "", "Vultr region ID")
	flags.String("plan", "", "Vultr plan ID")
	flags.String("os-id", "", "Vultr OS offering ID")
	flags.String("hostname", "", "optional VM hostname")
	flags.String("credential-ref", defaultVultrCredential, "systemd credential reference for the Vultr API key")
	flags.String("operation-id", "", "reuse the exact operation ID to reconcile a create or repeat an adoption")
	flags.String("startup-script-id", "", "exact Vultr bootstrap startup-script ID")
	flags.String("startup-script-sha256", "", "expected SHA-256 digest printed by gsc-bootstrap")
	flags.String("firewall-group-id", "", "Vultr firewall group that admits SSH only from --controller-ip (required for provisioning)")
	flags.String("controller-ip", "", "the controller's fixed public IP address (required for provisioning and handoff)")
	flags.String("ssh-credential-ref", defaultSSHCredential, "systemd credential reference for the controller's SSH private key")
	flags.String("confirm-handoff", "", "repeat <server-id>@<environment>/<instance-id> to approve the SSH handoff")
	flags.String("confirm-observability", "", "repeat <server-id>@<environment>/<instance-id> to authorize private log settings")
	flags.String("logs-endpoint", "", "HTTPS ingestion reverse proxy with /insert/jsonline path")
	flags.String("logs-privacy", "lifecycle", "log privacy mode: lifecycle or disabled (never raw player logs)")
	flags.String("logs-credential-ref", credential.SystemdReferenceScheme+"gsc-victorialogs-ingest-token", "systemd credential reference for VictoriaLogs ingestion only")
	flags.String("wasabi-region", "", "Wasabi region of the backup bucket, for example us-east-1")
	flags.String("wasabi-access-key-ref", defaultWasabiAccessKey, "systemd credential reference for the VM's Wasabi access key ID")
	flags.String("wasabi-secret-key-ref", defaultWasabiSecretKey, "systemd credential reference for the VM's Wasabi secret access key")
	flags.String("public-key-file", "", "controller SSH public key file to register with Vultr")
	flags.String("name", "", "name for the SSH key in Vultr")
	var sshKeyIDs stringListFlag
	flags.Var(&sshKeyIDs, "ssh-key-id", "Vultr SSH key ID; repeat for multiple keys (required for provisioning)")

	if err := parseFlags(flags, args[1:]); err != nil {
		return err
	}

	return (pilotCommand{
		flags: flags, settings: settings, stdout: stdout, stderr: stderr, sshKeyIDs: sshKeyIDs,
	}).dispatch(action)
}

// parseFlags parses command-line flags and returns an error if parsing fails or extra args remain.
func parseFlags(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	return nil
}

// doHistory shows the local history and returns an error if any flags are present.
func doHistory(stdout io.Writer) error {
	return showHistory(stdout)
}

// doStatus gets the status of a Minecraft server from systemd.
func doStatus(host string, stdout io.Writer) error {
	return showStatus(host, stdout)
}

// doBackup runs a backup operation on a Minecraft server.
func doBackup(host string, execute bool, confirmHost string, timeout time.Duration, stdout, stderr io.Writer) error {
	return backup(host, execute, confirmHost, timeout, stdout, stderr)
}

// doProvision provisions a new Vultr VM for a Minecraft server.
func doProvision(opts provisionOptions, stdout, stderr io.Writer) error {
	return provisionVultr(opts, stdout, stderr)
}

// doAdoptVultr adopts an existing Vultr instance for local management.
func doAdoptVultr(execute bool, confirmAdopt string, timeout time.Duration, policy pilotcapacity.Policy, statePath string, credentialRef string, request pilotvultr.AdoptionRequest, stdout io.Writer) error {
	return adoptVultr(execute, confirmAdopt, timeout, policy, statePath, credentialRef, request, stdout)
}

// doHandoff completes the SSH handoff to a newly provisioned or adopted server.
func doHandoff(opts handoffOptions, statePath string, stdout, stderr io.Writer) error {
	return runHandoff(opts, statePath, stdout, stderr)
}

// doRegisterSSHKey registers an SSH key with Vultr for use by new VMs.
func doRegisterSSHKey(execute bool, publicKeyFile, keyName, credentialRef string, timeout time.Duration, stdout io.Writer) error {
	return runRegisterSSHKey(execute, publicKeyFile, keyName, credentialRef, timeout, stdout)
}

// doShowCapacity shows the current capacity policy and state.
func doShowCapacity(policy pilotcapacity.Policy, statePath string, stdout io.Writer) error {
	return showCapacity(policy, statePath, stdout)
}

// doShowBootStatus checks boot status for a server in a specific environment.
func doShowBootStatus(serverID, environmentID string, statePath string, now time.Time, stdout io.Writer) error {
	return showBootStatus(serverID, environmentID, statePath, now, stdout)
}

func (settings runtimeSettings) capacityStatePath() (string, error) {
	if settings.stateDBPath != "" {
		return settings.stateDBPath, nil
	}
	currentUser, err := user.Current()
	if err != nil {
		return "", err
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve current directory for pilot-state migration check: %w", err)
	}
	return resolveCapacityStatePath(currentUser.HomeDir, workingDirectory)
}

func resolveCapacityStatePath(accountHome, workingDirectory string) (string, error) {
	statePath := filepath.Join(accountHome, ".local", "state", "gsc-pilot", "pilot.db")
	legacyPath := filepath.Join(workingDirectory, "var", "pilot.db")
	if _, err := os.Lstat(legacyPath); err == nil {
		return "", fmt.Errorf("legacy pilot database found at %s; stop pilot processes and move its capacity state to %s before using provision or capacity", legacyPath, statePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("check legacy pilot database %s: %w", legacyPath, err)
	}
	return statePath, nil
}

func allowOnlyFlags(flags *flag.FlagSet, allowed ...string) error {
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	var rejected []string
	flags.Visit(func(value *flag.Flag) {
		if !allowedSet[value.Name] {
			rejected = append(rejected, "--"+value.Name)
		}
	})
	if len(rejected) > 0 {
		return fmt.Errorf("remove flags not supported by this action: %s", strings.Join(rejected, ", "))
	}
	return nil
}

func usage(w io.Writer) error {
	fmt.Fprintln(w, "Supervised Minecraft server pilot")
	for _, action := range pilotActions {
		fmt.Fprintln(w, "  "+action.usage)
	}
	return errors.New("an action is required")
}

func showStatus(host string, stdout io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	for _, unit := range []string{gameUnit, backupUnit, timerUnit} {
		state, err := readUnitState(ctx, host, unit)
		if err != nil {
			return fmt.Errorf("read %s on %s: %w", unit, host, err)
		}
		fmt.Fprintf(stdout, "%s: active=%s sub=%s result=%s exit=%s invocation=%s\n",
			unit, state.active, state.sub, state.result, state.exitCode, state.invocationID)
	}
	return nil
}

func rejected(host, operationID, auditPath, reason string, cause error) error {
	if err := appendAuditRecord(auditPath, operationID, "rejected", host, reason); err != nil {
		return fmt.Errorf("operation %s rejected: %w; could not record rejection in %s: %v", operationID, cause, auditPath, err)
	}
	return fmt.Errorf("operation %s rejected (audit log: %s): %w", operationID, auditPath, cause)
}

type unitState struct {
	active       string
	sub          string
	result       string
	exitCode     string
	invocationID string
}

func readUnitState(ctx context.Context, host, unit string) (unitState, error) {
	output, err := remote(ctx, host, "sudo -n systemctl show --property=ActiveState,SubState,Result,ExecMainStatus,InvocationID "+unit)
	if err != nil {
		if output != "" {
			return unitState{}, fmt.Errorf("%w: %s", err, output)
		}
		return unitState{}, err
	}
	values := make(map[string]string, 5)
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			values[name] = value
		}
	}
	state, hasActive := values["ActiveState"]
	sub, hasSub := values["SubState"]
	if !hasActive || !hasSub {
		return unitState{}, fmt.Errorf("unexpected systemctl response for %s", unit)
	}
	return unitState{
		active:       state,
		sub:          sub,
		result:       values["Result"],
		exitCode:     values["ExecMainStatus"],
		invocationID: values["InvocationID"],
	}, nil
}

func systemdInvocationFinished(state unitState) bool {
	switch state.active {
	case "inactive":
		return state.sub == "dead"
	case "active":
		return state.sub == "exited"
	case "failed":
		return state.sub == "failed"
	default:
		return false
	}
}

func remote(ctx context.Context, host, command string) (string, error) {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", host, command}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

// flagSet reports whether a flag was given on the command line.
func flagSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
