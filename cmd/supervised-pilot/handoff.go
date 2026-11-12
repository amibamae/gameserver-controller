package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
	"github.com/amibamae/gameserver-controller/internal/pilothandoff"
	"github.com/amibamae/gameserver-controller/internal/sshkey"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

const (
	defaultWasabiAccessKey = credential.SystemdReferenceScheme + "gsc-pilot-vm-wasabi-access-key-id"
	defaultWasabiSecretKey = credential.SystemdReferenceScheme + "gsc-pilot-vm-wasabi-secret-access-key"
)

type handoffOptions struct {
	Execute          bool
	Confirmation     string
	Timeout          time.Duration
	OperationID      string
	CredentialRef    string
	SSHCredentialRef string
	WasabiAccessRef  string
	WasabiSecretRef  string
	WasabiRegion     string
}

// runHandoff verifies a new VM by its single-use host key, swaps and pins
// it, then sends the VM's Wasabi credentials and starts the restore. The
// default is a local dry run that reads only the state database.
func runHandoff(options handoffOptions, statePath string, stdout, stderr io.Writer) error {
	if !safeOperationID.MatchString(options.OperationID) {
		return errors.New("--operation-id is required and must be the 32-character ID printed by provision")
	}
	for _, reference := range []string{options.CredentialRef, options.SSHCredentialRef, options.WasabiAccessRef, options.WasabiSecretRef} {
		if !safeCredentialRef.MatchString(reference) {
			return errors.New("credential references must use systemd-credential://NAME")
		}
	}
	if options.Timeout <= 0 || options.Timeout > 6*time.Hour {
		return errors.New("--timeout must be greater than zero and no more than 6h")
	}
	if options.WasabiRegion == "" {
		return errors.New("--wasabi-region is required, for example us-east-1")
	}
	if _, err := os.Stat(statePath); err != nil {
		return fmt.Errorf("no pilot state database at %s; run provision first", statePath)
	}
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		return fmt.Errorf("open capacity store: %w", err)
	}
	defer repository.Close()
	reservation, err := repository.GetCapacityReservation(options.OperationID)
	if err != nil {
		return fmt.Errorf("no capacity reservation for operation %s: %w", options.OperationID, err)
	}
	record, err := repository.GetHostHandoff(options.OperationID)
	if errors.Is(err, store.ErrHostHandoffNotFound) {
		return pilothandoff.ErrNoHandoff
	}
	if err != nil {
		return err
	}
	target := reservation.ServerID + "@" + reservation.EnvironmentID + "/" + reservation.ProviderInstanceID
	if !options.Execute {
		if options.Confirmation != "" {
			return errors.New("--confirm-handoff requires --execute")
		}
		fmt.Fprintln(stdout, "Dry run: no SSH connection, provider API call, or credential read was made.")
		fmt.Fprintf(stdout, "VM: %s (capacity state %s)\n", target, reservation.State)
		fmt.Fprintf(stdout, "Host key handoff: %s; single-use key %s, valid until %s\n", record.State, keyFingerprint(record.OneTimeHostKey), record.ExpiresAt.Format(time.RFC3339))
		if record.State == hosthandoff.Rotated {
			fmt.Fprintf(stdout, "Pinned host key: %s\n", keyFingerprint(record.PinnedHostKey))
		}
		fmt.Fprintf(stdout, "Progress: bootstrap ready=%v, credentials sent=%v, security finalized=%v, restore started=%v, provider copy replaced=%v\n",
			record.BootstrapReady, record.CredentialsSent, record.SecurityFinalized, record.RestoreStarted, record.UserDataReplaced)
		fmt.Fprintf(stdout, "To run it, add --execute --confirm-handoff %s.\n", target)
		return nil
	}
	if reservation.ProviderInstanceID == "" || options.Confirmation != target {
		return fmt.Errorf("handoff not started: --confirm-handoff must exactly match %q", target)
	}
	auditPath, err := defaultAuditPath()
	if err != nil {
		return fmt.Errorf("resolve audit log path: %w", err)
	}
	if err := appendAuditEvent(auditPath, options.OperationID, "handoff_requested", "handoff", target, "vm.ssh.handoff", "host key "+string(record.State)); err != nil {
		return fmt.Errorf("handoff not started: could not persist the audit event: %w", err)
	}
	resolver, err := credential.FromEnvironment()
	if err != nil {
		return fmt.Errorf("load systemd credential directory: %w", err)
	}
	identity, err := resolver.Path(options.SSHCredentialRef)
	if err != nil {
		return errors.New("the controller SSH key credential is missing or readable by others; create it with controller-setup ssh-key generate")
	}
	runner := pilothandoff.Runner{Store: repository, Provider: vultrapi.NewClient(nil), Credentials: resolver, SSH: pilothandoff.OpenSSH{IdentityFile: identity}}
	ctx, cancel := context.WithTimeout(context.Background(), options.Timeout)
	defer cancel()
	report, err := runner.Run(ctx, pilothandoff.Config{
		OperationID: options.OperationID, VultrCredentialRef: options.CredentialRef,
		WasabiAccessKeyRef: options.WasabiAccessRef, WasabiSecretKeyRef: options.WasabiSecretRef, WasabiRegion: options.WasabiRegion,
		Progress: func(line string) { fmt.Fprintln(stdout, line) },
	})
	if err != nil {
		event := "handoff_failed"
		if errors.Is(err, pilothandoff.ErrHostKey) {
			event = "host_key_mismatch"
		}
		if auditErr := appendAuditEvent(auditPath, options.OperationID, event, "handoff", target, "vm.ssh.handoff", auditSafe(err)); auditErr != nil {
			return fmt.Errorf("%v; audit update also failed: %w", err, auditErr)
		}
		if errors.Is(err, pilothandoff.ErrTimedOut) {
			fmt.Fprintf(stderr, "Re-run the same command to resume; finished steps are not repeated.\n")
		}
		return err
	}
	detail := fmt.Sprintf("pinned %s; provider copy replaced=%v; restore complete", report.PinnedFingerprint, report.UserDataReplaced)
	if err := appendAuditEvent(auditPath, options.OperationID, "handoff_completed", "handoff", target, "vm.ssh.handoff", detail); err != nil {
		return fmt.Errorf("the handoff finished but the audit event could not be persisted: %w", err)
	}
	fmt.Fprintf(stdout, "Handoff complete for %s at %s. The Wasabi restore finished; Minecraft has not been started.\n", report.InstanceID, report.Address)
	return nil
}

// runRegisterSSHKey registers the controller's public key with Vultr, so new
// VMs can be created with it. The default is a dry run.
func runRegisterSSHKey(execute bool, publicKeyFile, name, credentialRef string, timeout time.Duration, stdout io.Writer) error {
	if !filepath.IsAbs(publicKeyFile) {
		return errors.New("--public-key-file must be an absolute path to the key printed by controller-setup ssh-key generate")
	}
	data, err := os.ReadFile(publicKeyFile)
	if err != nil || len(data) > 16<<10 {
		return errors.New("could not read --public-key-file")
	}
	line := strings.TrimSpace(string(data))
	key, err := sshkey.ParseAuthorizedKey(line)
	if err != nil {
		return fmt.Errorf("--public-key-file: %w", err)
	}
	if !safePilotServerID.MatchString(name) {
		return errors.New("--name must be a lower-case identifier for the key in Vultr")
	}
	if !safeCredentialRef.MatchString(credentialRef) {
		return errors.New("--credential-ref must use systemd-credential://NAME")
	}
	if !execute {
		fmt.Fprintln(stdout, "Dry run: no provider API call or credential read was made.")
		fmt.Fprintf(stdout, "Would register SSH key %q with fingerprint %s.\nTo register it, add --execute.\n", name, sshkey.Fingerprint(key))
		return nil
	}
	resolver, err := credential.FromEnvironment()
	if err != nil {
		return fmt.Errorf("load systemd credential directory: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var registered vultrapi.SSHKey
	err = resolver.WithCredential(credentialRef, func(token []byte) error {
		var createErr error
		registered, createErr = vultrapi.NewClient(nil).CreateSSHKey(ctx, token, name, sshkey.AuthorizedKey(key)+" gameserver-controller")
		return createErr
	})
	if errors.Is(err, vultrapi.ErrOutcomeUnknown) {
		return errors.New("Vultr's answer was unclear; check the SSH keys in the Vultr web panel before trying again")
	}
	if err != nil {
		return fmt.Errorf("register SSH key: %w", err)
	}
	fmt.Fprintf(stdout, "Registered SSH key %q with Vultr. Use --ssh-key-id %s when provisioning.\n", name, registered.ID)
	return nil
}

func keyFingerprint(line string) string {
	key, err := sshkey.ParseAuthorizedKey(line)
	if err != nil {
		return "(none)"
	}
	return sshkey.Fingerprint(key)
}

// auditSafe keeps an error to one short line for the audit log.
func auditSafe(err error) string {
	text := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}
