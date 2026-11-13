package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/boottiming"
	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/pilotvultr"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

const (
	gameUnit   = "minecraft-vanilla.service"
	backupUnit = "minecraft-backup.service"
	timerUnit  = "minecraft-backup.timer"
)

var safeSSHHost = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@:-]*$`)
var safeOperationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var safeInvocationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var safeStartupScriptDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var safePilotServerID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var safeCredentialRef = regexp.MustCompile(`^systemd-credential://[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var safePilotTarget = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}@(production|staging|testing)$`)

const (
	defaultVultrCredential = credential.SystemdReferenceScheme + "vultr-pilot-api-token"
	defaultSSHCredential   = credential.SystemdReferenceScheme + "gsc-pilot-ssh-key"
)

// provisionAccess is what the SSH handoff needs from a new create: the Vultr
// firewall group, the controller's fixed address, and its SSH key.
type provisionAccess struct {
	FirewallGroupID  string
	ControllerIP     string
	SSHCredentialRef string
}

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
		return provisionVultr(provisionRequest{
			Execute: *execute, Confirmation: *confirmCreate, Timeout: *timeout,
			ServerID: *serverID, Environment: *environmentID, Region: *region, Plan: *plan, OSID: *osID, Hostname: *hostname,
			SSHKeyIDs: sshKeyIDs, StartupScriptID: *startupScriptID, StartupScriptSHA256: *startupScriptSHA256,
			CredentialRef: *credentialRef, OperationID: *operationID, Access: access,
		}, policy, statePath, stdout, stderr)
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

// provisionRequest is what the provision command was given on the command
// line.
type provisionRequest struct {
	Execute             bool
	Confirmation        string
	Timeout             time.Duration
	ServerID            string
	Environment         string
	Region              string
	Plan                string
	OSID                string
	Hostname            string
	SSHKeyIDs           []string
	StartupScriptID     string
	StartupScriptSHA256 string
	CredentialRef       string
	OperationID         string
	Access              provisionAccess
}

// provisionEnvironment is the fixed policy, state database and output that a
// provision run uses.
type provisionEnvironment struct {
	policy    pilotcapacity.Policy
	statePath string
	stdout    io.Writer
	stderr    io.Writer
}

// checkedProvision holds what validation derived from a provisionRequest.
type checkedProvision struct {
	groupID    string
	osID       int
	controller netip.Addr
}

// provisionVultr validates the request, prints the dry run unless --execute
// is given, and otherwise requires the exact --confirm-create before any
// audit write, credential read, capacity change or provider call.
func provisionVultr(request provisionRequest, policy pilotcapacity.Policy, statePath string, stdout, stderr io.Writer) error {
	checked, err := validateProvisionRequest(request, policy)
	if err != nil {
		return err
	}
	if !request.Execute {
		return provisionDryRun(request, policy, checked, stdout)
	}
	operationID, err := confirmProvisionCreate(request)
	if err != nil {
		return err
	}
	return executeProvision(request, provisionEnvironment{policy: policy, statePath: statePath, stdout: stdout, stderr: stderr}, checked, operationID)
}

func validateProvisionRequest(request provisionRequest, policy pilotcapacity.Policy) (checkedProvision, error) {
	controller, err := validateProvisionInputs(request)
	if err != nil {
		return checkedProvision{}, err
	}
	groupID, osID, err := validateProvisionTarget(request, policy)
	if err != nil {
		return checkedProvision{}, err
	}
	return checkedProvision{groupID: groupID, osID: osID, controller: controller}, nil
}

// validateProvisionInputs checks the flags that need no policy, and returns
// the controller's exact address.
func validateProvisionInputs(request provisionRequest) (netip.Addr, error) {
	if request.Timeout <= 0 || request.Timeout > 24*time.Hour {
		return netip.Addr{}, errors.New("--timeout must be greater than zero and no more than 24h")
	}
	if !safePilotServerID.MatchString(request.ServerID) {
		return netip.Addr{}, errors.New("--server-id must be a lower-case identifier")
	}
	if len(request.SSHKeyIDs) == 0 {
		return netip.Addr{}, errors.New("at least one --ssh-key-id is required so the VM is reachable")
	}
	if request.StartupScriptID == "" || !safeStartupScriptDigest.MatchString(request.StartupScriptSHA256) {
		return netip.Addr{}, errors.New("--startup-script-id and a 64-character lowercase --startup-script-sha256 are required")
	}
	return validateProvisionAccess(request)
}

// validateProvisionAccess checks the credential references and the
// controller-only firewall, and returns the controller's exact address.
func validateProvisionAccess(request provisionRequest) (netip.Addr, error) {
	if !safeCredentialRef.MatchString(request.CredentialRef) || !safeCredentialRef.MatchString(request.Access.SSHCredentialRef) {
		return netip.Addr{}, errors.New("--credential-ref and --ssh-credential-ref must use systemd-credential://NAME")
	}
	controller, err := netip.ParseAddr(request.Access.ControllerIP)
	if err != nil || request.Access.FirewallGroupID == "" {
		return netip.Addr{}, errors.New("--firewall-group-id and an exact --controller-ip address (not a range) are required so SSH is reachable only from the controller")
	}
	return controller, nil
}

// validateProvisionTarget checks the request against the capacity policy and
// the provider's create rules, and returns the capacity group and OS ID.
func validateProvisionTarget(request provisionRequest, policy pilotcapacity.Policy) (string, int, error) {
	if !policy.RuntimeEnforced {
		return "", 0, errors.New("capacity policy is not marked for runtime enforcement")
	}
	groupID, _, supported := policy.GroupForEnvironment(request.Environment)
	if !supported {
		return "", 0, pilotcapacity.ErrUnsupportedEnvironment
	}
	osID, err := vultrapi.ParseOSID(request.OSID)
	if err != nil {
		return "", 0, errors.New("--os-id must be a positive Vultr OS ID")
	}
	validation := vultrapi.CreateInstanceRequest{
		Region: request.Region, Plan: request.Plan, OSID: osID, Label: "gsc-managed-dry-run",
		Hostname: request.Hostname, SSHKeyIDs: append([]string(nil), request.SSHKeyIDs...), StartupScriptID: request.StartupScriptID,
		FirewallGroupID: request.Access.FirewallGroupID,
	}
	if err := vultrapi.ValidateCreateInstanceRequest(validation); err != nil {
		return "", 0, errors.New("region, plan, hostname, SSH key ID, or firewall group ID is invalid")
	}
	return groupID, osID, nil
}

// provisionTarget is <server-id>@<environment>: the exact --confirm-create
// value and the audit target.
func provisionTarget(request provisionRequest) string {
	return request.ServerID + "@" + request.Environment
}

// provisionDryRun prints what --execute would create. It makes no provider
// call, credential read, audit write or capacity reservation.
func provisionDryRun(request provisionRequest, policy pilotcapacity.Policy, checked checkedProvision, stdout io.Writer) error {
	if request.Confirmation != "" {
		return errors.New("--confirm-create requires --execute")
	}
	if request.OperationID != "" {
		return errors.New("--operation-id requires --execute")
	}
	fmt.Fprintln(stdout, "Dry run: no provider API call, credential read, or capacity reservation was made.")
	fmt.Fprintf(stdout, "Would create one managed Vultr VM: server=%s environment=%s group=%s region=%s plan=%s os_id=%d\n", request.ServerID, request.Environment, checked.groupID, request.Region, request.Plan, checked.osID)
	fmt.Fprintf(stdout, "Bootstrap startup script ID: %s\nBootstrap SHA-256: %s\n", request.StartupScriptID, request.StartupScriptSHA256)
	fmt.Fprintf(stdout, "Firewall group %s must admit SSH only from %s; this is checked before the create.\n", request.Access.FirewallGroupID, checked.controller)
	fmt.Fprintln(stdout, "A single-use SSH host key will be generated for this VM; only its public half is stored.")
	fmt.Fprintf(stdout, "Configured hard limit: %d controller-owned VM(s).\n", policy.MaxInstancesTotal)
	fmt.Fprintf(stdout, "To create it, add --execute --confirm-create %s.\n", provisionTarget(request))
	return nil
}

// confirmProvisionCreate requires the exact --confirm-create and returns the
// operation ID to use: the given one, or a new one.
func confirmProvisionCreate(request provisionRequest) (string, error) {
	expected := provisionTarget(request)
	if request.Confirmation != expected {
		return "", fmt.Errorf("Vultr create not started: --confirm-create must exactly match %q", expected)
	}
	if request.OperationID == "" {
		operationID, err := newOperationID()
		if err != nil {
			return "", fmt.Errorf("create operation ID: %w", err)
		}
		return operationID, nil
	}
	if !safeOperationID.MatchString(request.OperationID) {
		return "", errors.New("--operation-id must be 32 lowercase hexadecimal characters")
	}
	return request.OperationID, nil
}

// provisionRun is one confirmed provision operation and its audit trail.
type provisionRun struct {
	auditPath   string
	operationID string
	target      string
	stdout      io.Writer
	stderr      io.Writer
}

func (run provisionRun) audit(event, detail string) error {
	return appendAuditEvent(run.auditPath, run.operationID, event, "provision", run.target, "vultr.instance.create", detail)
}

// recordSetupFailure audits a failure before the provisioner ran and returns
// cause behind prefix.
func (run provisionRun) recordSetupFailure(prefix string, cause error, detail string) error {
	if auditErr := run.audit("provision_failed", detail); auditErr != nil {
		return fmt.Errorf("%s%v; audit update also failed: %w", prefix, cause, auditErr)
	}
	if prefix == "" {
		return cause
	}
	return fmt.Errorf("%s%w", prefix, cause)
}

// executeProvision audits the request, loads the credentials and capacity
// store, and runs the capacity-checked create.
func executeProvision(request provisionRequest, env provisionEnvironment, checked checkedProvision, operationID string) error {
	auditPath, err := defaultAuditPath()
	if err != nil {
		return fmt.Errorf("resolve audit log path: %w", err)
	}
	run := provisionRun{auditPath: auditPath, operationID: operationID, target: provisionTarget(request), stdout: env.stdout, stderr: env.stderr}
	auditDetail := fmt.Sprintf("region=%s plan=%s os_id=%d startup_script_id=%s startup_script_sha256=%s", request.Region, request.Plan, checked.osID, request.StartupScriptID, request.StartupScriptSHA256)
	if err := run.audit("provision_requested", auditDetail); err != nil {
		return fmt.Errorf("Vultr create not started: could not persist the audit event: %w", err)
	}
	resolver, err := credential.FromEnvironment()
	if err != nil {
		return run.recordSetupFailure("load systemd credential directory: ", err, "systemd credential directory unavailable")
	}
	controllerKey, err := controllerPublicKey(resolver, request.Access.SSHCredentialRef)
	if err != nil {
		return run.recordSetupFailure("", err, "controller SSH key credential unavailable")
	}
	repository, err := store.OpenBolt(env.statePath)
	if err != nil {
		return run.recordSetupFailure("open capacity store: ", err, "capacity store unavailable")
	}
	defer repository.Close()
	service, err := pilotvultr.NewProvisioner(repository, env.policy, vultrapi.NewClient(nil), resolver, request.CredentialRef)
	if err != nil {
		return err
	}
	input := pilotvultr.CreateRequest{
		OperationID: operationID, ServerID: request.ServerID, Environment: request.Environment,
		Region: request.Region, Plan: request.Plan, OSID: request.OSID, Hostname: request.Hostname,
		SSHKeyIDs: append([]string(nil), request.SSHKeyIDs...), StartupScriptID: request.StartupScriptID,
		StartupScriptSHA256: request.StartupScriptSHA256, FirewallGroupID: request.Access.FirewallGroupID,
		ControllerIP: checked.controller, ControllerSSHPublicKey: controllerKey,
	}
	fmt.Fprintf(env.stdout, "Operation ID: %s\n", operationID)
	fmt.Fprintf(env.stdout, "Submitting capacity-checked Vultr create for %s in %s.\n", request.ServerID, request.Environment)
	fmt.Fprintln(env.stdout, "This creates a billable VM; game installation and readiness are not part of this command.")
	ctx, cancel := context.WithTimeout(context.Background(), request.Timeout)
	defer cancel()
	reservation, err := service.Provision(ctx, input)
	if err != nil {
		return run.recordProvisionFailure(err)
	}
	return run.reportConfirmed(reservation)
}

// rejectedByCapacity reports whether the capacity policy refused the
// request before anything was sent to the provider.
func rejectedByCapacity(err error) bool {
	return errors.Is(err, store.ErrCapacityReached) || errors.Is(err, store.ErrCapacityOccupied) || errors.Is(err, pilotcapacity.ErrUnsupportedEnvironment)
}

// failedPreCreateCheck reports whether a check made before the create (SSH
// keys, firewall, bootstrap script) failed, so nothing was sent.
func failedPreCreateCheck(err error) bool {
	return errors.Is(err, pilotvultr.ErrSSHKeyMismatch) || errors.Is(err, pilotvultr.ErrFirewall) || errors.Is(err, pilotvultr.ErrBootstrapScript)
}

// provisionFailureEvent names the audit event and detail for a failed
// Provision call.
func provisionFailureEvent(err error) (string, string) {
	switch {
	case errors.Is(err, pilotvultr.ErrOutcomeUnknown):
		return "outcome_unknown", "provider result requires reconciliation; capacity remains occupied"
	case errors.Is(err, pilotvultr.ErrProviderRejected):
		return "provider_rejected", "provider definitively rejected create; reservation was released"
	case rejectedByCapacity(err):
		return "provision_rejected", "capacity policy rejected the request before provider dispatch"
	case failedPreCreateCheck(err):
		return "provision_rejected", "pre-create check failed; nothing was sent to Vultr"
	default:
		return "provision_failed", "create request did not complete"
	}
}

func (run provisionRun) recordProvisionFailure(err error) error {
	if errors.Is(err, pilotvultr.ErrOutcomeUnknown) {
		fmt.Fprintf(run.stderr, "The slot remains occupied. Re-run with --operation-id %s and the same create parameters to reconcile; the service will not resend an uncertain create.\n", run.operationID)
	}
	event, detail := provisionFailureEvent(err)
	if auditErr := run.audit(event, detail); auditErr != nil {
		return fmt.Errorf("provision failed (%v); audit update also failed: %w", err, auditErr)
	}
	if rejectedByCapacity(err) {
		fmt.Fprintf(run.stderr, "Provision request rejected before provider create. Operation ID: %s\n", run.operationID)
	}
	return err
}

func (run provisionRun) reportConfirmed(reservation store.CapacityReservation) error {
	if err := run.audit("provision_confirmed", "exact Vultr instance ID and managed label were observed"); err != nil {
		return fmt.Errorf("Vultr instance %s is present but the audit event could not be persisted: %w", reservation.ProviderInstanceID, err)
	}
	fmt.Fprintf(run.stdout, "Vultr instance %s is confirmed present and counted (capacity state %s).\n", reservation.ProviderInstanceID, reservation.State)
	fmt.Fprintln(run.stdout, "Provider presence is confirmed; game readiness is not verified.")
	fmt.Fprintf(run.stdout, "Next: gsc-pilot handoff --operation-id %s --wasabi-region <region> verifies the VM's host key, then sends the Wasabi restore credentials.\n", run.operationID)
	return nil
}

// controllerPublicKey derives the controller's SSH public key from its
// private key credential without keeping the private key.
func controllerPublicKey(resolver *credential.Resolver, reference string) (string, error) {
	var public string
	err := resolver.WithCredential(reference, func(secret []byte) error {
		key, err := sshkey.ParsePrivateKey(secret)
		if err != nil {
			return err
		}
		defer clear(key)
		public = sshkey.AuthorizedKey(key.Public().(ed25519.PublicKey))
		return nil
	})
	if err != nil {
		return "", errors.New("the controller SSH key credential is missing or is not an unencrypted ed25519 key; create it with controller-setup ssh-key generate")
	}
	return public, nil
}

func showCapacity(policy pilotcapacity.Policy, statePath string, stdout io.Writer) error {
	if !policy.RuntimeEnforced {
		return errors.New("capacity policy is not marked for runtime enforcement")
	}
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		return err
	}
	defer repository.Close()
	reservations, err := repository.ListCapacityReservations()
	if err != nil {
		return err
	}
	occupied, groupCounts := 0, make(map[string]int)
	for _, reservation := range reservations {
		if policy.ReleasedStates[string(reservation.State)] {
			continue
		}
		occupied++
		groupCounts[reservation.CapacityGroup]++
	}
	fmt.Fprintf(stdout, "Managed VM capacity: %d/%d total.\n", occupied, policy.MaxInstancesTotal)
	groupIDs := make([]string, 0, len(policy.CapacityGroups))
	for groupID := range policy.CapacityGroups {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)
	for _, groupID := range groupIDs {
		group := policy.CapacityGroups[groupID]
		fmt.Fprintf(stdout, "%s: %d/%d slot(s)\n", groupID, groupCounts[groupID], group.MaxInstances)
	}
	if len(reservations) == 0 {
		fmt.Fprintln(stdout, "No managed VM reservations are recorded.")
		return nil
	}
	fmt.Fprintln(stdout, "OPERATION_ID\tSERVER\tENVIRONMENT\tSTATE\tPROVIDER_INSTANCE_ID\tRESOURCE_LABEL")
	for _, reservation := range reservations {
		instanceID := reservation.ProviderInstanceID
		if instanceID == "" {
			instanceID = "-"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\n", reservation.OperationID, reservation.ServerID, reservation.EnvironmentID, reservation.State, instanceID, reservation.ResourceKey)
	}
	return nil
}

func showBootStatus(serverID, environmentID, statePath string, now time.Time, stdout io.Writer) error {
	if !safePilotServerID.MatchString(serverID) ||
		(environmentID != "production" && environmentID != "staging" && environmentID != "testing") || now.IsZero() {
		return errors.New("a valid server ID, environment, and current time are required")
	}
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		return err
	}
	defer repository.Close()
	reservations, err := repository.ListCapacityReservations()
	if err != nil {
		return err
	}
	var current *store.CapacityReservation
	var durations []time.Duration
	for i := range reservations {
		reservation := &reservations[i]
		if reservation.ServerID != serverID || reservation.EnvironmentID != environmentID {
			continue
		}
		if !reservation.ProvisionedAt.IsZero() && !reservation.ReadyAt.IsZero() {
			if duration := reservation.ReadyAt.Sub(reservation.ProvisionedAt); duration > 0 {
				durations = append(durations, duration)
			}
		}
		if reservation.State != store.CapacityAbsent && reservation.State != store.CapacityDestroyed {
			current = reservation
		}
	}
	if current == nil {
		fmt.Fprintf(stdout, "No controller-owned VM is currently recorded for %s@%s.\n", serverID, environmentID)
		writeHistoricalBootEstimate(stdout, durations)
		return nil
	}
	if current.State != store.CapacityActive {
		if !current.ReadyAt.IsZero() {
			fmt.Fprintf(stdout, "Paper readiness was previously confirmed at %s.\n", current.ReadyAt.UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(stdout, "Managed VM state: %s. Current Paper readiness is not confirmed; boot progress is indeterminate.\n", current.State)
		writeHistoricalBootEstimate(stdout, durations)
		return nil
	}
	if !current.ReadyAt.IsZero() {
		fmt.Fprintf(stdout, "Paper readiness was confirmed at %s.\n", current.ReadyAt.UTC().Format(time.RFC3339))
		if !current.ProvisionedAt.IsZero() {
			fmt.Fprintf(stdout, "Measured provision-to-Paper-ready time: %s.\n", current.ReadyAt.Sub(current.ProvisionedAt).Round(time.Second))
		}
		fmt.Fprintln(stdout, "Progress: [####################] 100% (initial boot completed)")
		return nil
	}
	if current.ProvisionedAt.IsZero() {
		fmt.Fprintln(stdout, "Provider presence is confirmed, but no provisioning timestamp is available; Paper boot progress is indeterminate.")
		writeHistoricalBootEstimate(stdout, durations)
		return nil
	}
	elapsed := now.Sub(current.ProvisionedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	fmt.Fprintf(stdout, "Paper readiness is pending. Time since provisioning: %s.\n", elapsed.Round(time.Second))
	if len(durations) < 3 {
		fmt.Fprintf(stdout, "Estimate and progress are indeterminate (%d completed boot sample(s); 3 required).\n", len(durations))
		return nil
	}
	estimate, err := boottiming.Estimate(durations, boottiming.DefaultSafetyFactor)
	if err != nil {
		fmt.Fprintf(stdout, "Estimate and progress are indeterminate (%d completed boot sample(s)).\n", len(durations))
		return nil
	}
	remaining := estimate - elapsed
	if remaining < 0 {
		remaining = 0
	}
	percent := boottiming.ProgressPercent(elapsed, estimate, false)
	fmt.Fprintf(stdout, "Estimated total: %s; estimated time remaining: %s.\n", estimate.Round(time.Second), remaining.Round(time.Second))
	fmt.Fprintf(stdout, "Progress: %s (advisory; never reaches 100%% before Paper readiness).\n", bootProgressBar(percent))
	return nil
}

func writeHistoricalBootEstimate(stdout io.Writer, durations []time.Duration) {
	if len(durations) < 3 {
		fmt.Fprintf(stdout, "Historical boot estimate unavailable (%d completed boot sample(s); 3 required).\n", len(durations))
		return
	}
	estimate, err := boottiming.Estimate(durations, boottiming.DefaultSafetyFactor)
	if err != nil {
		fmt.Fprintf(stdout, "Historical boot estimate unavailable (%d completed boot sample(s)).\n", len(durations))
		return
	}
	fmt.Fprintf(stdout, "Historical boot estimate: %s (75th percentile plus 25%% buffer; %d samples).\n", estimate.Round(time.Second), len(durations))
}

func bootProgressBar(percent int) string {
	const width = 20
	if percent < 0 {
		percent = 0
	}
	if percent > 99 {
		percent = 99
	}
	filled := percent * width / 100
	return fmt.Sprintf("[%s%s] %d%%", strings.Repeat("#", filled), strings.Repeat("-", width-filled), percent)
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

type auditRecord struct {
	OperationID string `json:"operation_id"`
	RecordedAt  string `json:"recorded_at"`
	Event       string `json:"event"`
	Action      string `json:"action"`
	Target      string `json:"target"`
	Service     string `json:"service"`
	Detail      string `json:"detail,omitempty"`
}

func newOperationID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func defaultAuditPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "gsc-pilot", "operations.jsonl"), nil
}

func appendAuditRecord(path, operationID, event, host, detail string) error {
	return appendAuditEvent(path, operationID, event, "backup", host, backupUnit, detail)
}

func appendAuditEvent(path, operationID, event, action, target, service, detail string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	record := auditRecord{
		OperationID: operationID,
		RecordedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Event:       event,
		Action:      action,
		Target:      target,
		Service:     service,
		Detail:      detail,
	}
	if err := json.NewEncoder(file).Encode(record); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func showHistory(stdout io.Writer) error {
	auditPath, err := defaultAuditPath()
	if err != nil {
		return fmt.Errorf("resolve audit log path: %w", err)
	}
	file, err := os.Open(auditPath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "No pilot operations have been recorded.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("open audit log %s: %w", auditPath, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNumber := 0
	wroteHeader := false
	for scanner.Scan() {
		lineNumber++
		var record auditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return fmt.Errorf("decode audit log %s line %d: %w", auditPath, lineNumber, err)
		}
		recordedAt, err := time.Parse(time.RFC3339Nano, record.RecordedAt)
		if err != nil {
			return fmt.Errorf("invalid timestamp in audit log %s line %d", auditPath, lineNumber)
		}
		if !validAuditRecord(record) {
			return fmt.Errorf("invalid audit record in %s line %d", auditPath, lineNumber)
		}
		if !wroteHeader {
			fmt.Fprintln(stdout, "RECORDED_AT\tOPERATION_ID\tACTION\tEVENT\tTARGET\tSERVICE\tDETAIL")
			wroteHeader = true
		}
		detail := "-"
		if record.Detail != "" {
			detail = strconv.Quote(record.Detail)
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			recordedAt.UTC().Format(time.RFC3339Nano), record.OperationID, record.Action,
			record.Event, record.Target, record.Service, detail)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read audit log %s: %w", auditPath, err)
	}
	if !wroteHeader {
		fmt.Fprintln(stdout, "No pilot operations have been recorded.")
	}
	return nil
}

func validAuditRecord(record auditRecord) bool {
	if !safeOperationID.MatchString(record.OperationID) {
		return false
	}
	switch record.Action {
	case "backup":
		return record.Service == backupUnit && safeSSHHost.MatchString(record.Target) && validBackupAuditEvent(record.Event)
	case "adopt":
		return record.Service == "vultr.instance.adopt" && safePilotTarget.MatchString(record.Target) && (record.Event == "adoption_requested" || record.Event == "adoption_failed" || record.Event == "adoption_confirmed")
	case "provision":
		return record.Service == "vultr.instance.create" && safePilotTarget.MatchString(record.Target) && validProvisionAuditEvent(record.Event)
	default:
		return false
	}
}

func validBackupAuditEvent(event string) bool {
	switch event {
	case "rejected", "started", "outcome_unknown", "failed", "service_succeeded", "succeeded":
		return true
	default:
		return false
	}
}

func validProvisionAuditEvent(event string) bool {
	switch event {
	case "outcome_unknown", "provision_requested", "provision_failed", "provision_rejected", "provider_rejected", "provision_confirmed":
		return true
	default:
		return false
	}
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
