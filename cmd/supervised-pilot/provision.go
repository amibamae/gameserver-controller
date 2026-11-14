package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"time"

	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/pilotvultr"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

var safeStartupScriptDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// provisionAccess is what the SSH handoff needs from a new create: the Vultr
// firewall group, the controller's fixed address, and its SSH key.
type provisionAccess struct {
	FirewallGroupID  string
	ControllerIP     string
	SSHCredentialRef string
}

type provisionOptions struct {
	execute                              bool
	confirmation                         string
	timeout                              time.Duration
	policy                               pilotcapacity.Policy
	statePath                            string
	serverID, environment                string
	region, plan, osID, hostname         string
	sshKeyIDs                            []string
	startupScriptID, startupScriptSHA256 string
	credentialRef, operationID           string
	access                               provisionAccess
}

type validatedProvision struct {
	providerOSID      int
	groupID           string
	controllerAddress netip.Addr
}

type provisionAudit struct {
	auditPath, operationID, auditTarget string
}

func provisionVultr(options provisionOptions, stdout, stderr io.Writer) error {
	validated, err := validateProvision(options)
	if err != nil {
		return err
	}
	expectedConfirmation := options.serverID + "@" + options.environment
	if !options.execute {
		return previewProvision(options, validated, expectedConfirmation, stdout)
	}
	return executeProvision(options, validated, expectedConfirmation, stdout, stderr)
}

func validateProvisionInputs(options provisionOptions) error {
	if options.timeout <= 0 || options.timeout > 24*time.Hour {
		return errors.New("--timeout must be greater than zero and no more than 24h")
	}
	if !safePilotServerID.MatchString(options.serverID) {
		return errors.New("--server-id must be a lower-case identifier")
	}
	if len(options.sshKeyIDs) == 0 {
		return errors.New("at least one --ssh-key-id is required so the VM is reachable")
	}
	if options.startupScriptID == "" || !safeStartupScriptDigest.MatchString(options.startupScriptSHA256) {
		return errors.New("--startup-script-id and a 64-character lowercase --startup-script-sha256 are required")
	}
	if !safeCredentialRef.MatchString(options.credentialRef) || !safeCredentialRef.MatchString(options.access.SSHCredentialRef) {
		return errors.New("--credential-ref and --ssh-credential-ref must use systemd-credential://NAME")
	}
	return nil
}

func validateProvision(options provisionOptions) (validatedProvision, error) {
	if err := validateProvisionInputs(options); err != nil {
		return validatedProvision{}, err
	}
	controllerAddress, err := netip.ParseAddr(options.access.ControllerIP)
	if err != nil || options.access.FirewallGroupID == "" {
		return validatedProvision{}, errors.New("--firewall-group-id and an exact --controller-ip address (not a range) are required so SSH is reachable only from the controller")
	}
	if !options.policy.RuntimeEnforced {
		return validatedProvision{}, errors.New("capacity policy is not marked for runtime enforcement")
	}
	groupID, _, supported := options.policy.GroupForEnvironment(options.environment)
	if !supported {
		return validatedProvision{}, pilotcapacity.ErrUnsupportedEnvironment
	}
	providerOSID, err := vultrapi.ParseOSID(options.osID)
	if err != nil {
		return validatedProvision{}, errors.New("--os-id must be a positive Vultr OS ID")
	}
	validation := vultrapi.CreateInstanceRequest{
		Region: options.region, Plan: options.plan, OSID: providerOSID, Label: "gsc-managed-dry-run",
		Hostname: options.hostname, SSHKeyIDs: append([]string(nil), options.sshKeyIDs...), StartupScriptID: options.startupScriptID,
		FirewallGroupID: options.access.FirewallGroupID,
	}
	if err := vultrapi.ValidateCreateInstanceRequest(validation); err != nil {
		return validatedProvision{}, errors.New("region, plan, hostname, SSH key ID, or firewall group ID is invalid")
	}
	return validatedProvision{providerOSID: providerOSID, groupID: groupID, controllerAddress: controllerAddress}, nil
}

func previewProvision(options provisionOptions, validated validatedProvision, expectedConfirmation string, stdout io.Writer) error {
	if options.confirmation != "" {
		return errors.New("--confirm-create requires --execute")
	}
	if options.operationID != "" {
		return errors.New("--operation-id requires --execute")
	}
	fmt.Fprintln(stdout, "Dry run: no provider API call, credential read, or capacity reservation was made.")
	fmt.Fprintf(stdout, "Would create one managed Vultr VM: server=%s environment=%s group=%s region=%s plan=%s os_id=%d\n", options.serverID, options.environment, validated.groupID, options.region, options.plan, validated.providerOSID)
	fmt.Fprintf(stdout, "Bootstrap startup script ID: %s\nBootstrap SHA-256: %s\n", options.startupScriptID, options.startupScriptSHA256)
	fmt.Fprintf(stdout, "Firewall group %s must admit SSH only from %s; this is checked before the create.\n", options.access.FirewallGroupID, validated.controllerAddress)
	fmt.Fprintln(stdout, "A single-use SSH host key will be generated for this VM; only its public half is stored.")
	fmt.Fprintf(stdout, "Configured hard limit: %d controller-owned VM(s).\n", options.policy.MaxInstancesTotal)
	fmt.Fprintf(stdout, "To create it, add --execute --confirm-create %s.\n", expectedConfirmation)
	return nil
}

func executeProvision(options provisionOptions, validated validatedProvision, expectedConfirmation string, stdout, stderr io.Writer) error {
	if options.confirmation != expectedConfirmation {
		return fmt.Errorf("Vultr create not started: --confirm-create must exactly match %q", expectedConfirmation)
	}
	operationID := options.operationID
	if operationID == "" {
		var err error
		operationID, err = newOperationID()
		if err != nil {
			return fmt.Errorf("create operation ID: %w", err)
		}
	} else if !safeOperationID.MatchString(operationID) {
		return errors.New("--operation-id must be 32 lowercase hexadecimal characters")
	}
	auditPath, err := defaultAuditPath()
	if err != nil {
		return fmt.Errorf("resolve audit log path: %w", err)
	}
	auditTarget := expectedConfirmation
	auditDetail := fmt.Sprintf("region=%s plan=%s os_id=%d startup_script_id=%s startup_script_sha256=%s", options.region, options.plan, validated.providerOSID, options.startupScriptID, options.startupScriptSHA256)
	if err := appendAuditEvent(auditPath, operationID, "provision_requested", "provision", auditTarget, "vultr.instance.create", auditDetail); err != nil {
		return fmt.Errorf("Vultr create not started: could not persist the audit event: %w", err)
	}
	audit := provisionAudit{auditPath: auditPath, operationID: operationID, auditTarget: auditTarget}
	return submitProvision(options, validated, audit, stdout, stderr)
}

func submitProvision(options provisionOptions, validated validatedProvision, audit provisionAudit, stdout, stderr io.Writer) error {
	resolver, err := credential.FromEnvironment()
	if err != nil {
		if auditErr := appendAuditEvent(audit.auditPath, audit.operationID, "provision_failed", "provision", audit.auditTarget, "vultr.instance.create", "systemd credential directory unavailable"); auditErr != nil {
			return fmt.Errorf("load systemd credential directory: %v; audit update also failed: %w", err, auditErr)
		}
		return fmt.Errorf("load systemd credential directory: %w", err)
	}
	controllerKey, err := controllerPublicKey(resolver, options.access.SSHCredentialRef)
	if err != nil {
		if auditErr := appendAuditEvent(audit.auditPath, audit.operationID, "provision_failed", "provision", audit.auditTarget, "vultr.instance.create", "controller SSH key credential unavailable"); auditErr != nil {
			return fmt.Errorf("%v; audit update also failed: %w", err, auditErr)
		}
		return err
	}
	repository, err := store.OpenBolt(options.statePath)
	if err != nil {
		if auditErr := appendAuditEvent(audit.auditPath, audit.operationID, "provision_failed", "provision", audit.auditTarget, "vultr.instance.create", "capacity store unavailable"); auditErr != nil {
			return fmt.Errorf("open capacity store: %v; audit update also failed: %w", err, auditErr)
		}
		return fmt.Errorf("open capacity store: %w", err)
	}
	// The legacy CLI reports the provisioning outcome independently of store cleanup.
	defer func() { _ = repository.Close() }()
	service, err := pilotvultr.NewProvisioner(repository, options.policy, vultrapi.NewClient(nil), resolver, options.credentialRef)
	if err != nil {
		return err
	}
	input := pilotvultr.CreateRequest{
		OperationID: audit.operationID, ServerID: options.serverID, Environment: options.environment,
		Region: options.region, Plan: options.plan, OSID: options.osID, Hostname: options.hostname,
		SSHKeyIDs: append([]string(nil), options.sshKeyIDs...), StartupScriptID: options.startupScriptID,
		StartupScriptSHA256: options.startupScriptSHA256, FirewallGroupID: options.access.FirewallGroupID,
		ControllerIP: validated.controllerAddress, ControllerSSHPublicKey: controllerKey,
	}
	fmt.Fprintf(stdout, "Operation ID: %s\n", audit.operationID)
	fmt.Fprintf(stdout, "Submitting capacity-checked Vultr create for %s in %s.\n", options.serverID, options.environment)
	fmt.Fprintln(stdout, "This creates a billable VM; game installation and readiness are not part of this command.")
	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()
	reservation, err := service.Provision(ctx, input)
	if err != nil {
		return reportProvisionFailure(err, audit, stderr)
	}
	return reportProvisionSuccess(reservation, audit, stdout)
}

func reportProvisionFailure(err error, audit provisionAudit, stderr io.Writer) error {
	if errors.Is(err, pilotvultr.ErrOutcomeUnknown) {
		fmt.Fprintf(stderr, "The slot remains occupied. Re-run with --operation-id %s and the same create parameters to reconcile; the service will not resend an uncertain create.\n", audit.operationID)
	}
	event := "provision_failed"
	detail := "create request did not complete"
	switch {
	case errors.Is(err, pilotvultr.ErrOutcomeUnknown):
		event, detail = "outcome_unknown", "provider result requires reconciliation; capacity remains occupied"
	case errors.Is(err, pilotvultr.ErrProviderRejected):
		event, detail = "provider_rejected", "provider definitively rejected create; reservation was released"
	case isProvisionCapacityRejection(err):
		event, detail = "provision_rejected", "capacity policy rejected the request before provider dispatch"
	case errors.Is(err, pilotvultr.ErrSSHKeyMismatch), errors.Is(err, pilotvultr.ErrFirewall), errors.Is(err, pilotvultr.ErrBootstrapScript):
		event, detail = "provision_rejected", "pre-create check failed; nothing was sent to Vultr"
	}
	if auditErr := appendAuditEvent(audit.auditPath, audit.operationID, event, "provision", audit.auditTarget, "vultr.instance.create", detail); auditErr != nil {
		return fmt.Errorf("provision failed (%v); audit update also failed: %w", err, auditErr)
	}
	if isProvisionCapacityRejection(err) {
		fmt.Fprintf(stderr, "Provision request rejected before provider create. Operation ID: %s\n", audit.operationID)
	}
	return err
}

func isProvisionCapacityRejection(err error) bool {
	return errors.Is(err, store.ErrCapacityReached) || errors.Is(err, store.ErrCapacityOccupied) ||
		errors.Is(err, pilotcapacity.ErrUnsupportedEnvironment)
}

func reportProvisionSuccess(reservation store.CapacityReservation, audit provisionAudit, stdout io.Writer) error {
	if err := appendAuditEvent(audit.auditPath, audit.operationID, "provision_confirmed", "provision", audit.auditTarget, "vultr.instance.create", "exact Vultr instance ID and managed label were observed"); err != nil {
		return fmt.Errorf("Vultr instance %s is present but the audit event could not be persisted: %w", reservation.ProviderInstanceID, err)
	}
	fmt.Fprintf(stdout, "Vultr instance %s is confirmed present and counted (capacity state %s).\n", reservation.ProviderInstanceID, reservation.State)
	fmt.Fprintln(stdout, "Provider presence is confirmed; game readiness is not verified.")
	fmt.Fprintf(stdout, "Next: gsc-pilot handoff --operation-id %s --wasabi-region <region> verifies the VM's host key, then sends the Wasabi restore credentials.\n", audit.operationID)
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
