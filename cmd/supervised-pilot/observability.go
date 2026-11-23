package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
	"github.com/amibamae/gameserver-controller/internal/pilothandoff"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

type observabilityOptions struct {
	Execute                                                bool
	Confirmation, OperationID                              string
	VultrCredentialRef, SSHCredentialRef, LogCredentialRef string
	Endpoint, PrivacyMode                                  string
	Timeout                                                time.Duration
}

// runObservability is dry-run by default and never logs endpoint or token.
func runObservability(o observabilityOptions, statePath string, out io.Writer) error {
	if err := validateObservabilityOptions(o); err != nil {
		return err
	}
	repository, target, err := loadObservabilityTarget(o.OperationID, statePath)
	if err != nil {
		return err
	}
	defer repository.Close()
	if !o.Execute {
		if o.Confirmation != "" {
			return errors.New("--confirm-observability requires --execute")
		}
		fmt.Fprintf(out, "Dry run: no secret read, SSH call, provider call or configuration change.\nVM: %s\nPrivacy: %s\nAdd --execute --confirm-observability %s to apply.\n",
			target, o.PrivacyMode, target)
		return nil
	}
	if o.Confirmation != target {
		return fmt.Errorf("--confirm-observability must match %q", target)
	}
	return applyObservability(o, repository, target, out)
}

func validateObservabilityOptions(o observabilityOptions) error {
	if !safeOperationID.MatchString(o.OperationID) {
		return errors.New("--operation-id must be a 32-character provision operation ID")
	}
	if o.Timeout <= 0 || o.Timeout > 2*time.Hour {
		return errors.New("--timeout must be greater than zero and at most 2h")
	}
	for _, ref := range []string{o.VultrCredentialRef, o.SSHCredentialRef, o.LogCredentialRef} {
		if !safeCredentialRef.MatchString(ref) {
			return errors.New("observability credential references must be systemd-credential://NAME")
		}
	}
	switch o.PrivacyMode {
	case "lifecycle":
		if o.Endpoint == "" {
			return errors.New("--logs-endpoint is required for lifecycle log export")
		}
	case "disabled":
		if o.Endpoint != "" {
			return errors.New("disabled log export must not have an endpoint")
		}
	default:
		return errors.New("--logs-privacy must be lifecycle or disabled")
	}
	return nil
}

func loadObservabilityTarget(operationID, statePath string) (*store.BoltStore, string, error) {
	if _, err := os.Stat(statePath); err != nil {
		return nil, "", errors.New("pilot state database not found; provision and hand off first")
	}
	repository, err := store.OpenBolt(statePath)
	if err != nil {
		return nil, "", errors.New("could not open pilot state database")
	}
	reservation, err := repository.GetCapacityReservation(operationID)
	if err != nil {
		repository.Close()
		return nil, "", errors.New("no provisioned VM for the requested operation")
	}
	record, err := repository.GetHostHandoff(operationID)
	if err != nil {
		repository.Close()
		return nil, "", errors.New("the VM has no completed SSH host-key handoff")
	}
	if !observabilityReady(reservation, record) {
		repository.Close()
		return nil, "", errors.New("observability requires an active VM and completed SSH security handoff")
	}
	target := reservation.ServerID + "@" + reservation.EnvironmentID + "/" + reservation.ProviderInstanceID
	return repository, target, nil
}

func observabilityReady(reservation store.CapacityReservation, record store.HostHandoffRecord) bool {
	if reservation.State != store.CapacityActive || reservation.ProviderInstanceID == "" {
		return false
	}
	return record.State == hosthandoff.Rotated && record.BootstrapReady && record.SecurityFinalized
}

func applyObservability(o observabilityOptions, repository *store.BoltStore, target string, out io.Writer) error {
	auditPath, err := defaultAuditPath()
	if err != nil {
		return errors.New("observability audit log unavailable; refusing change")
	}
	if err := appendAuditEvent(auditPath, o.OperationID, "observability_requested",
		"observability", target, "vm.ssh.observability", o.PrivacyMode); err != nil {
		return errors.New("observability audit write failed; refusing change")
	}
	resolver, err := credential.FromEnvironment()
	if err != nil {
		return errors.New("systemd credential directory unavailable")
	}
	identity, err := resolver.Path(o.SSHCredentialRef)
	if err != nil {
		return errors.New("controller SSH credential unavailable")
	}
	runner := pilothandoff.Runner{
		Store: repository, Provider: vultrapi.NewClient(nil),
		Credentials: resolver, SSH: pilothandoff.OpenSSH{IdentityFile: identity},
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.Timeout)
	defer cancel()
	err = runner.ConfigureVictoriaLogs(ctx, pilothandoff.VictoriaLogsConfig{
		OperationID: o.OperationID, VultrCredentialRef: o.VultrCredentialRef,
		IngestCredentialRef: o.LogCredentialRef, Endpoint: o.Endpoint,
		PrivacyMode: o.PrivacyMode,
	})
	if err != nil {
		_ = appendAuditEvent(auditPath, o.OperationID, "observability_failed",
			"observability", target, "vm.ssh.observability", "private configuration unsuccessful")
		return errors.New("private observability provisioning failed; verify pinned SSH and VM service state")
	}
	if err := appendAuditEvent(auditPath, o.OperationID, "observability_completed",
		"observability", target, "vm.ssh.observability", o.PrivacyMode); err != nil {
		return errors.New("VM updated, but observability audit completion failed")
	}
	fmt.Fprintf(out, "Private observability policy %s applied to %s over pinned SSH.\n", o.PrivacyMode, target)
	return nil
}
