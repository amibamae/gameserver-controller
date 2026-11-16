package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/amibamae/gameserver-controller/internal/pilotcapacity"
	"github.com/amibamae/gameserver-controller/internal/pilotvultr"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/vultrapi"
)

func adoptVultr(execute bool, confirmation string, timeout time.Duration, policy pilotcapacity.Policy, statePath, credentialRef string, request pilotvultr.AdoptionRequest, stdout io.Writer) error {
	if !validAdoptionArguments(timeout, request.ServerID, credentialRef) || policy.Validate() != nil || !policy.RuntimeEnforced {
		return errors.New("adoption requires valid timeout, server ID, credential reference, and enforced capacity policy")
	}
	groupID, _, ok := policy.GroupForEnvironment(request.Environment)
	if !ok {
		return pilotcapacity.ErrUnsupportedEnvironment
	}
	providedOperation := request.OperationID != ""
	if !providedOperation {
		request.OperationID = strings.Repeat("0", 32)
	}
	if err := pilotvultr.ValidateAdoptionRequest(request); err != nil {
		return err
	}
	target := request.ServerID + "@" + request.Environment
	if !execute {
		expected := target + "/" + request.InstanceID
		return printAdoptionPreview(stdout, request, groupID, expected, confirmation != "" || providedOperation)
	}
	execution := vultrAdoptionExecution{timeout: timeout, policy: policy, statePath: statePath, credentialRef: credentialRef}
	return execution.adopt(request, target, confirmation, providedOperation, stdout)
}

func validAdoptionArguments(timeout time.Duration, serverID, credentialRef string) bool {
	return timeout > 0 && timeout <= 24*time.Hour &&
		safePilotServerID.MatchString(serverID) && safeCredentialRef.MatchString(credentialRef)
}

func printAdoptionPreview(stdout io.Writer, request pilotvultr.AdoptionRequest, groupID, expected string, executionOptionsProvided bool) error {
	if executionOptionsProvided {
		return errors.New("--confirm-adopt and --operation-id require --execute")
	}
	fmt.Fprintln(stdout, "Dry run: no provider API call, credential read, or capacity change was made.")
	fmt.Fprintf(stdout, "Would adopt existing Vultr VM: target=%s group=%s ip=%s region=%s plan=%s os_id=%d os=%q\n", expected, groupID, request.IP, request.Region, request.Plan, request.OSID, request.OS)
	fmt.Fprintf(stdout, "To verify inventory and adopt local managed ownership, add --execute --confirm-adopt %s.\n", expected)
	return nil
}

type vultrAdoptionExecution struct {
	timeout       time.Duration
	policy        pilotcapacity.Policy
	statePath     string
	credentialRef string
}

func (execution vultrAdoptionExecution) adopt(request pilotvultr.AdoptionRequest, target, confirmation string, providedOperation bool, stdout io.Writer) error {
	expected := target + "/" + request.InstanceID
	if confirmation != expected {
		return fmt.Errorf("adoption not started: --confirm-adopt must exactly match %q", expected)
	}
	if !providedOperation {
		var err error
		request.OperationID, err = newOperationID()
		if err != nil {
			return err
		}
	}

	// Print the recovery identity before any local state mutation.
	fmt.Fprintf(stdout, "Operation ID: %s\n", request.OperationID)
	auditPath, err := defaultAuditPath()
	if err != nil {
		return err
	}
	if err := appendAuditEvent(auditPath, request.OperationID, "adoption_requested", "adopt", target, "vultr.instance.adopt", "exact-target local ownership adoption requested"); err != nil {
		return err
	}
	reservation, err := execution.adoptCapacity(request)
	if err != nil {
		if auditErr := appendAuditEvent(auditPath, request.OperationID, "adoption_failed", "adopt", target, "vultr.instance.adopt", "local adoption did not complete; inspect capacity before starting another operation"); auditErr != nil {
			return fmt.Errorf("adoption failed (%v); audit update also failed: %w", err, auditErr)
		}
		return err
	}
	if err := appendAuditEvent(auditPath, request.OperationID, "adoption_confirmed", "adopt", target, "vultr.instance.adopt", "exact provider metadata checked and managed capacity committed"); err != nil {
		return fmt.Errorf("ownership is committed; audit completion failed, inspect capacity and reuse operation ID %s: %w", request.OperationID, err)
	}
	fmt.Fprintf(stdout, "Existing Vultr instance %s is now counted in managed capacity.\n", reservation.ProviderInstanceID)
	fmt.Fprintln(stdout, "No provider resource was modified. Game readiness, backup safety, and lifecycle integration are not verified.")
	return nil
}

func (execution vultrAdoptionExecution) adoptCapacity(request pilotvultr.AdoptionRequest) (store.CapacityReservation, error) {
	resolver, err := credential.FromEnvironment()
	if err != nil {
		return store.CapacityReservation{}, err
	}
	repository, err := store.OpenBolt(execution.statePath)
	if err != nil {
		return store.CapacityReservation{}, err
	}
	defer repository.Close()
	service, err := pilotvultr.NewAdopter(repository, execution.policy, vultrapi.NewClient(nil), resolver, execution.credentialRef)
	if err != nil {
		return store.CapacityReservation{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), execution.timeout)
	defer cancel()
	return service.Adopt(ctx, request)
}
