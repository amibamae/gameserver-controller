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
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
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
	return runWithSettings(args, stdout, stderr, runtimeSettings{})
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
	host := flags.String("host", "", "SSH config alias or user@host for the Minecraft server")
	execute := flags.Bool("execute", false, "execute the selected action; mutating actions are dry runs by default")
	confirmHost := flags.String("confirm-host", "", "repeat the exact SSH target to approve a backup run")
	confirmAdopt := flags.String("confirm-adopt", "", "repeat <server-id>@<environment>/<instance-id> to approve local managed ownership")
	instanceID := flags.String("instance-id", "", "exact existing Vultr instance ID for adoption")
	expectedIP := flags.String("expected-ip", "", "expected public IPv4 address for adoption")
	osName := flags.String("os-name", "", "exact expected Vultr operating system name for adoption")
	confirmCreate := flags.String("confirm-create", "", "repeat <server-id>@<environment> to approve billable Vultr VM creation")
	timeout := flags.Duration("timeout", time.Hour, "maximum time to wait for the selected operation")
	serverID := flags.String("server-id", "", "stable logical server ID for Vultr provisioning")
	environmentID := flags.String("environment", "", "configured environment ID: production, staging, or testing")
	region := flags.String("region", "", "Vultr region ID")
	plan := flags.String("plan", "", "Vultr plan ID")
	osID := flags.String("os-id", "", "Vultr OS offering ID")
	hostname := flags.String("hostname", "", "optional VM hostname")
	credentialRef := flags.String("credential-ref", defaultVultrCredential, "systemd credential reference for the Vultr API key")
	operationID := flags.String("operation-id", "", "reuse the exact operation ID to reconcile a create or repeat an adoption")
	startupScriptID := flags.String("startup-script-id", "", "exact Vultr bootstrap startup-script ID")
	startupScriptSHA256 := flags.String("startup-script-sha256", "", "expected SHA-256 digest printed by gsc-bootstrap")
	firewallGroupID := flags.String("firewall-group-id", "", "Vultr firewall group that admits SSH only from --controller-ip (required for provisioning)")
	controllerIP := flags.String("controller-ip", "", "the controller's fixed public IP address (required for provisioning and handoff)")
	sshCredentialRef := flags.String("ssh-credential-ref", defaultSSHCredential, "systemd credential reference for the controller's SSH private key")
	confirmHandoff := flags.String("confirm-handoff", "", "repeat <server-id>@<environment>/<instance-id> to approve the SSH handoff")
	wasabiRegion := flags.String("wasabi-region", "", "Wasabi region of the backup bucket, for example us-east-1")
	wasabiAccessRef := flags.String("wasabi-access-key-ref", defaultWasabiAccessKey, "systemd credential reference for the VM's Wasabi access key ID")
	wasabiSecretRef := flags.String("wasabi-secret-key-ref", defaultWasabiSecretKey, "systemd credential reference for the VM's Wasabi secret access key")
	publicKeyFile := flags.String("public-key-file", "", "controller SSH public key file to register with Vultr")
	keyName := flags.String("name", "", "name for the SSH key in Vultr")
	var sshKeyIDs stringListFlag
	flags.Var(&sshKeyIDs, "ssh-key-id", "Vultr SSH key ID; repeat for multiple keys (required for provisioning)")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if action == "history" {
		if flags.NFlag() != 0 {
			return errors.New("history is local and read-only; remove all flags")
		}
		return showHistory(stdout)
	}

	switch action {
	case "status":
		if err := allowOnlyFlags(flags, "host", "execute", "confirm-host"); err != nil {
			return err
		}
		if !safeSSHHost.MatchString(*host) || strings.HasPrefix(*host, "-") {
			return errors.New("--host is required and must be a simple SSH alias or user@host")
		}
		if *execute || *confirmHost != "" {
			return errors.New("status is read-only; remove --execute and --confirm-host")
		}
		return showStatus(*host, stdout)
	case "backup":
		if err := allowOnlyFlags(flags, "host", "execute", "confirm-host", "timeout"); err != nil {
			return err
		}
		if !safeSSHHost.MatchString(*host) || strings.HasPrefix(*host, "-") {
			return errors.New("--host is required and must be a simple SSH alias or user@host")
		}
		if *timeout <= 0 || *timeout > 24*time.Hour {
			return errors.New("--timeout must be greater than zero and no more than 24 hours")
		}
		return backup(*host, *execute, *confirmHost, *timeout, stdout, stderr)
	case "provision":
		if err := allowOnlyFlags(flags, "execute", "confirm-create", "timeout", "server-id", "environment", "region", "plan", "os-id", "hostname", "credential-ref", "operation-id", "ssh-key-id", "startup-script-id", "startup-script-sha256", "firewall-group-id", "controller-ip", "ssh-credential-ref"); err != nil {
			return err
		}
		policy, err := pilotcapacity.LoadEmbeddedPolicy()
		if err != nil {
			return fmt.Errorf("load embedded capacity policy: %w", err)
		}
		statePath, err := settings.capacityStatePath()
		if err != nil {
			return fmt.Errorf("resolve capacity state path: %w", err)
		}
		access := provisionAccess{FirewallGroupID: *firewallGroupID, ControllerIP: *controllerIP, SSHCredentialRef: *sshCredentialRef}
		return provisionVultr(provisionOptions{
			execute: *execute, confirmation: *confirmCreate, timeout: *timeout,
			policy: policy, statePath: statePath, serverID: *serverID, environment: *environmentID,
			region: *region, plan: *plan, osID: *osID, hostname: *hostname,
			sshKeyIDs: sshKeyIDs, startupScriptID: *startupScriptID, startupScriptSHA256: *startupScriptSHA256,
			credentialRef: *credentialRef, operationID: *operationID, access: access,
		}, stdout, stderr)
	case "adopt":
		if err := allowOnlyFlags(flags, "execute", "confirm-adopt", "timeout", "server-id", "environment", "instance-id", "expected-ip", "region", "plan", "os-id", "os-name", "credential-ref", "operation-id"); err != nil {
			return err
		}
		policy, err := pilotcapacity.LoadEmbeddedPolicy()
		if err != nil {
			return err
		}
		statePath, err := settings.capacityStatePath()
		if err != nil {
			return err
		}
		providerOSID, err := vultrapi.ParseOSID(*osID)
		if err != nil {
			return err
		}
		return adoptVultr(*execute, *confirmAdopt, *timeout, policy, statePath, *credentialRef, pilotvultr.AdoptionRequest{
			OperationID: *operationID, ServerID: *serverID, Environment: *environmentID, InstanceID: *instanceID,
			IP: *expectedIP, Region: *region, Plan: *plan, OSID: providerOSID, OS: *osName,
		}, stdout)
	case "handoff":
		if err := allowOnlyFlags(flags, "execute", "confirm-handoff", "timeout", "operation-id", "credential-ref", "ssh-credential-ref", "wasabi-region", "wasabi-access-key-ref", "wasabi-secret-key-ref"); err != nil {
			return err
		}
		statePath, err := settings.capacityStatePath()
		if err != nil {
			return fmt.Errorf("resolve capacity state path: %w", err)
		}
		handoffTimeout := *timeout
		if !flagSet(flags, "timeout") {
			handoffTimeout = 45 * time.Minute
		}
		return runHandoff(handoffOptions{Execute: *execute, Confirmation: *confirmHandoff, Timeout: handoffTimeout, OperationID: *operationID,
			CredentialRef: *credentialRef, SSHCredentialRef: *sshCredentialRef, WasabiAccessRef: *wasabiAccessRef, WasabiSecretRef: *wasabiSecretRef,
			WasabiRegion: *wasabiRegion}, statePath, stdout, stderr)
	case "register-ssh-key":
		if err := allowOnlyFlags(flags, "execute", "public-key-file", "name", "credential-ref", "timeout"); err != nil {
			return err
		}
		return runRegisterSSHKey(*execute, *publicKeyFile, *keyName, *credentialRef, min(*timeout, 2*time.Minute), stdout)
	case "capacity":
		if flags.NFlag() != 0 {
			return errors.New("capacity reads the fixed local policy and state database; remove all flags")
		}
		policy, err := pilotcapacity.LoadEmbeddedPolicy()
		if err != nil {
			return fmt.Errorf("load embedded capacity policy: %w", err)
		}
		statePath, err := settings.capacityStatePath()
		if err != nil {
			return fmt.Errorf("resolve capacity state path: %w", err)
		}
		return showCapacity(policy, statePath, stdout)
	case "boot-status":
		if err := allowOnlyFlags(flags, "server-id", "environment"); err != nil {
			return err
		}
		if !safePilotServerID.MatchString(*serverID) {
			return errors.New("--server-id is required and must be a lower-case identifier")
		}
		if *environmentID != "production" && *environmentID != "staging" && *environmentID != "testing" {
			return errors.New("--environment must be production, staging, or testing")
		}
		statePath, err := settings.capacityStatePath()
		if err != nil {
			return fmt.Errorf("resolve capacity state path: %w", err)
		}
		return showBootStatus(*serverID, *environmentID, statePath, time.Now().UTC(), stdout)
	default:
		return usage(stderr)
	}
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

func backup(host string, execute bool, confirmation string, timeout time.Duration, stdout, stderr io.Writer) error {
	if !execute {
		fmt.Fprintf(stdout, "Dry run: would check %s on %s, start it, and inspect the resulting systemd invocation.\n", backupUnit, host)
		fmt.Fprintln(stdout, "The Paper service remains online; this uses the existing backup service.")
		fmt.Fprintf(stdout, "To run it, add --execute --confirm-host %s\n", host)
		return nil
	}
	if confirmation != host {
		return errors.New("backup not started: --confirm-host must exactly match --host")
	}

	operationID, err := newOperationID()
	if err != nil {
		return fmt.Errorf("create operation ID: %w", err)
	}
	auditPath, err := defaultAuditPath()
	if err != nil {
		return fmt.Errorf("resolve audit log path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	before, err := readUnitState(ctx, host, backupUnit)
	if err != nil {
		return rejected(host, operationID, auditPath, "preflight_failed", fmt.Errorf("preflight %s on %s: %w", backupUnit, host, err))
	}
	if before.active == "failed" || before.result == "failed" {
		return rejected(host, operationID, auditPath, "unit_failed", fmt.Errorf("refusing to retry failed unit %s; inspect its journal and clear the failure manually", backupUnit))
	}
	if before.active != "inactive" {
		return rejected(host, operationID, auditPath, "unit_not_inactive", fmt.Errorf("refusing to start %s while its state is %q; inspect the unit manually first", backupUnit, before.active))
	}
	if err := appendAuditRecord(auditPath, operationID, "started", host, fmt.Sprintf("previous systemd invocation_id=%q", before.invocationID)); err != nil {
		return fmt.Errorf("backup not started (operation %s): could not persist the audit record at %s: %w", operationID, auditPath, err)
	}

	fmt.Fprintf(stdout, "Starting %s on %s (timeout %s).\n", backupUnit, host, timeout)
	fmt.Fprintf(stdout, "Operation ID: %s\nAudit log: %s\n", operationID, auditPath)
	fmt.Fprintln(stdout, "No stop, restore, or destroy command will be sent.")
	if output, err := remote(ctx, host, "sudo -n systemctl start --wait "+backupUnit); err != nil {
		if output != "" {
			fmt.Fprintln(stderr, output)
		}
		detail := fmt.Sprintf("systemctl start returned an error; remote outcome may be unknown (previous invocation_id=%q)", before.invocationID)
		if auditErr := appendAuditRecord(auditPath, operationID, "outcome_unknown", host, detail); auditErr != nil {
			return fmt.Errorf("backup outcome unknown (%v); audit update also failed: %w", err, auditErr)
		}
		return fmt.Errorf("backup outcome unknown; inspect %s on %s: %w", backupUnit, host, err)
	}

	after, err := readUnitState(ctx, host, backupUnit)
	if err != nil {
		detail := "start returned successfully, but post-run status could not be read"
		if auditErr := appendAuditRecord(auditPath, operationID, "outcome_unknown", host, detail); auditErr != nil {
			return fmt.Errorf("%s on %s: %v; audit update also failed: %w", backupUnit, host, err, auditErr)
		}
		return fmt.Errorf("backup outcome unknown; verify %s on %s: %w", backupUnit, host, err)
	}
	if !safeInvocationID.MatchString(after.invocationID) ||
		(before.invocationID != "" && (!safeInvocationID.MatchString(before.invocationID) || before.invocationID == after.invocationID)) {
		detail := fmt.Sprintf("systemd invocation ID is missing, malformed, or unchanged: before=%q after=%q", before.invocationID, after.invocationID)
		if auditErr := appendAuditRecord(auditPath, operationID, "outcome_unknown", host, detail); auditErr != nil {
			return fmt.Errorf("backup outcome unknown (%s); audit update also failed: %w", detail, auditErr)
		}
		return fmt.Errorf("backup outcome unknown; %s; inspect %s on %s before retrying", detail, backupUnit, host)
	}
	if !systemdInvocationFinished(after) {
		detail := fmt.Sprintf("systemd start job returned before the unit reached a completed state: invocation_id=%q active=%q sub=%q", after.invocationID, after.active, after.sub)
		if auditErr := appendAuditRecord(auditPath, operationID, "outcome_unknown", host, detail); auditErr != nil {
			return fmt.Errorf("backup outcome unknown (%s); audit update also failed: %w", detail, auditErr)
		}
		return fmt.Errorf("backup outcome unknown; %s; inspect %s on %s before retrying", detail, backupUnit, host)
	}
	if after.active == "failed" || after.result != "success" || after.exitCode != "0" {
		detail := fmt.Sprintf("systemd invocation_id=%q result=%q exit=%q active=%q", after.invocationID, after.result, after.exitCode, after.active)
		if auditErr := appendAuditRecord(auditPath, operationID, "failed", host, detail); auditErr != nil {
			return fmt.Errorf("%s did not report success (%s); audit update failed: %w", backupUnit, detail, auditErr)
		}
		return fmt.Errorf("%s did not report success (%s)", backupUnit, detail)
	}
	if err := appendAuditRecord(auditPath, operationID, "service_succeeded", host, fmt.Sprintf("systemd invocation_id=%s reported result=success and exit status 0; backup contents and storage integrity are not verified", after.invocationID)); err != nil {
		return fmt.Errorf("%s reported success, but the result could not be recorded in %s: %w", backupUnit, auditPath, err)
	}
	fmt.Fprintf(stdout, "Backup service completed successfully on %s (operation %s).\n", host, operationID)
	fmt.Fprintln(stdout, "This confirms the systemd unit result only; it does not verify backup contents or storage integrity.")
	fmt.Fprintf(stdout, "%s: active=%s sub=%s result=%s exit=%s invocation=%s\n",
		backupUnit, after.active, after.sub, after.result, after.exitCode, after.invocationID)
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
