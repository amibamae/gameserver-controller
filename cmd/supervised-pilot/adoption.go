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
	if timeout <= 0 || timeout > 24*time.Hour || !safePilotServerID.MatchString(request.ServerID) || !safeCredentialRef.MatchString(credentialRef) || policy.Validate() != nil || !policy.RuntimeEnforced {
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
	expected := request.ServerID + "@" + request.Environment + "/" + request.InstanceID
	if !execute {
		if confirmation != "" || providedOperation {
			return errors.New("--confirm-adopt and --operation-id require --execute")
		}
		fmt.Fprintln(stdout, "Dry run: no provider API call, credential read, or capacity change was made.")
		fmt.Fprintf(stdout, "Would adopt existing Vultr VM: target=%s group=%s ip=%s region=%s plan=%s os_id=%d os=%q\n", expected, groupID, request.IP, request.Region, request.Plan, request.OSID, request.OS)
		fmt.Fprintf(stdout, "To verify inventory and adopt local managed ownership, add --execute --confirm-adopt %s.\n", expected)
		return nil
	}
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
	target := request.ServerID + "@" + request.Environment
	if err := appendAuditEvent(auditPath, request.OperationID, "adoption_requested", "adopt", target, "vultr.instance.adopt", "exact-target local ownership adoption requested"); err != nil {
		return err
	}
	adopt := func() (store.CapacityReservation, error) {
		resolver, err := credential.FromEnvironment()
		if err != nil {
			return store.CapacityReservation{}, err
		}
		repository, err := store.OpenBolt(statePath)
		if err != nil {
			return store.CapacityReservation{}, err
		}
		defer repository.Close()
		service, err := pilotvultr.NewAdopter(repository, policy, vultrapi.NewClient(nil), resolver, credentialRef)
		if err != nil {
			return store.CapacityReservation{}, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return service.Adopt(ctx, request)
	}
	reservation, err := adopt()
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
