package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
	"time"
)

func (w *ServerProvisionWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.provision" {
		return errors.New("server.provision completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	created, err := provisionCreateStepOutput(operation)
	if err != nil || !provisionPostconditionsConfirmed(operation) {
		return errors.New("server.provision exact instance, bootstrap, game, or execution postconditions were not confirmed")
	}
	server, err := w.loadProvisionCompletionServer(ctx, operation)
	if err != nil {
		return err
	}
	if err := w.checkProvisionCompletionServer(operation, server, created); err != nil {
		return err
	}
	return w.recordProvisionCapacity(operation, created)
}

// provisionPostconditionsConfirmed reports whether every host, game and
// execution step after the provider create is verified.
func provisionPostconditionsConfirmed(operation store.OperationRecord) bool {
	return provisionHostPostconditions(operation) && provisionUnitPostconditions(operation)
}

func provisionHostPostconditions(operation store.OperationRecord) bool {
	return provisionWaitSucceeded(operation) && provisionBootstrapSucceeded(operation) && provisionInstallSucceeded(operation)
}

func provisionUnitPostconditions(operation store.OperationRecord) bool {
	return provisionUnitCreated(operation) && provisionUnitStarted(operation) && provisionUnitRunning(operation)
}

// loadProvisionCompletionServer loads the target server and, for a
// restore, requires compatible-source and verified-restore evidence.
func (w *ServerProvisionWorkflow) loadProvisionCompletionServer(ctx context.Context, operation store.OperationRecord) (store.LogicalServer, error) {
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return store.LogicalServer{}, errors.New("server.provision request cannot be decoded")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.LogicalServer{}, err
	}
	if request.RestoreBackup != "" {
		profile, err := w.profiles.ResolveServerProvision(ctx, stored.Record, operation.RequestSnapshot.Parameters)
		if err != nil || !provisionCompatibilitySucceeded(operation, w.schemas, profile) || !provisionRestoreSucceeded(operation) {
			return store.LogicalServer{}, errors.New("server.provision requested restore lacks exact compatible source and verified restore evidence")
		}
	}
	return stored.Record, nil
}

// checkProvisionCompletionServer requires the server to be bound to the
// created instance and the controller-owned generation, with a valid
// readiness assertion.
func (w *ServerProvisionWorkflow) checkProvisionCompletionServer(operation store.OperationRecord, server store.LogicalServer, created provisionCreateOutput) error {
	if !equalJSON(server.InstanceRef, created.InstanceRef) {
		return errors.New("server.provision logical server is not bound to the exact created instance")
	}
	if server.DataGenerationStatus != "known" || server.DataGenerationID != provisionDataGeneration(operation.OperationID) {
		return errors.New("server.provision has no controller-owned data generation for the installed world")
	}
	_, err := BuildReadinessAssertion(w.schemas, operation, server, provisionReadinessStep, w.assertionTTL)
	return err
}

// recordProvisionCapacity makes the reservation active for the exact
// instance, then records its provision-to-ready evidence.
func (w *ServerProvisionWorkflow) recordProvisionCapacity(operation store.OperationRecord, created provisionCreateOutput) error {
	ref, ok := decodeProvisionRef(created.InstanceRef)
	if !ok {
		return errors.New("server.provision provider instance reference is invalid")
	}
	createTime, err := time.Parse(time.RFC3339Nano, created.ObservedAt)
	if err != nil {
		return errors.New("server.provision create observation time is invalid")
	}
	if err := w.activateProvisionCapacity(operation, ref, createTime); err != nil {
		return err
	}
	var readiness readinessCheckOutput
	readinessStep := operationStep(operation, provisionReadinessStep)
	if readinessStep.Status != protocolstate.StepSucceeded || json.Unmarshal(readinessStep.Output, &readiness) != nil {
		return errors.New("server.provision readiness output is missing or invalid")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, readiness.ObservedAt)
	if err != nil {
		return errors.New("server.provision readiness observation time is invalid")
	}
	if _, err := w.capacity.RecordCapacityReady(operation.Target.ServerID, operation.Target.EnvironmentID, ref.ExternalID, observedAt); err != nil {
		return fmt.Errorf("record exact provision-to-ready capacity evidence: %w", err)
	}
	return nil
}

// activateProvisionCapacity records the exact instance on a reservation
// whose create outcome is unknown, then requires it to be active for it.
func (w *ServerProvisionWorkflow) activateProvisionCapacity(operation store.OperationRecord, ref provisionRef, createTime time.Time) error {
	reservation, err := w.capacity.GetCapacityReservation(operation.OperationID)
	if err != nil {
		return fmt.Errorf("load provision capacity reservation: %w", err)
	}
	if reservation.State == store.CapacityCreateOutcomeUnknown {
		reservation, err = w.capacity.ObserveCapacity(operation.OperationID, store.CapacityProviderObservation{Provider: reservation.Provider,
			ResourceKey: reservation.ResourceKey, ProviderInstanceID: ref.ExternalID, State: "present", ObservedAt: time.Now().UTC(), ProvisionedAt: createTime})
		if err != nil {
			return fmt.Errorf("record exact provisioned capacity: %w", err)
		}
	}
	if reservation.State != store.CapacityActive || reservation.ProviderInstanceID != ref.ExternalID {
		return errors.New("server.provision capacity reservation is not active for the exact instance")
	}
	return nil
}
