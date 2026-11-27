package store

import (
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// recordDestroyedCapacityTx releases a managed VM slot in the same Bolt
// transaction that commits the successful server.destroy result and logical
// server transition. The provider's exact final absence step is the evidence
// used for the capacity observation; accepted or ambiguous destroys never
// reach this path.
func recordDestroyedCapacityTx(tx *bolt.Tx, operation OperationRecord, server LogicalServer) error {
	instance, observedAt, err := destroyedCapacityEvidence(operation, server)
	if err != nil {
		return err
	}
	match, err := findDestroyedCapacityReservation(tx.Bucket(capacityReservationsBucket), server, instance)
	if err != nil || match == nil {
		return err
	}
	if match.State != CapacityActive && match.State != CapacityDestroyOutcomeUnknown {
		return fmt.Errorf("%w: cannot release managed VM capacity from state %q", ErrCapacityTransition, match.State)
	}
	return observeCapacityTx(tx, match.OperationID, CapacityProviderObservation{
		Provider: match.Provider, ResourceKey: match.ResourceKey, ProviderInstanceID: instance.ExternalID,
		State: "destroyed", ObservedAt: observedAt,
	})
}

// destroyedCapacityInstance is the exact provider instance a successful
// server.destroy targeted.
type destroyedCapacityInstance struct {
	AdapterID    string `json:"adapter_id"`
	ResourceKind string `json:"resource_kind"`
	ExternalID   string `json:"external_id"`
}

// destroyedCapacityEvidence checks, in order, the destroy result, the
// provider absence step, the target instance, and the absence time bound,
// returning the destroyed instance and when its absence was observed.
func destroyedCapacityEvidence(operation OperationRecord, server LogicalServer) (destroyedCapacityInstance, time.Time, error) {
	if !destroyResultHasEvidence(operation, server) {
		return destroyedCapacityInstance{}, time.Time{}, fmt.Errorf("%w: server.destroy success lacks exact absence and backup evidence", ErrInvalidRecord)
	}
	observedAt, ok := destroyAbsenceObservedAt(operation)
	if !ok {
		return destroyedCapacityInstance{}, time.Time{}, fmt.Errorf("%w: server.destroy success lacks exact provider absence observation", ErrInvalidRecord)
	}
	instance, ok := decodeDestroyedCapacityInstance(operation.Target.InstanceRef)
	if !ok {
		return destroyedCapacityInstance{}, time.Time{}, fmt.Errorf("%w: server.destroy target is not an exact provider instance", ErrInvalidRecord)
	}
	if !plausibleObservationTime(observedAt) {
		return destroyedCapacityInstance{}, time.Time{}, fmt.Errorf("%w: provider absence observation is too far in the future", ErrInvalidRecord)
	}
	return instance, observedAt, nil
}

// decodeDestroyedCapacityInstance returns the provider instance a
// server.destroy target reference names, and false unless it is an exact
// instance with its adapter.
func decodeDestroyedCapacityInstance(ref json.RawMessage) (destroyedCapacityInstance, bool) {
	var instance destroyedCapacityInstance
	if json.Unmarshal(ref, &instance) != nil || instance.AdapterID == "" || instance.ResourceKind != "instance" || instance.ExternalID == "" {
		return destroyedCapacityInstance{}, false
	}
	return instance, true
}

// destroyResultHasEvidence reports whether the server.destroy result names
// the target instance, a verified backup of the server's data generation,
// confirmed provider absence, and a destroyed final lifecycle state.
func destroyResultHasEvidence(operation OperationRecord, server LogicalServer) bool {
	var result struct {
		OperationType string                `json:"operation_type"`
		OperationData destroyResultEvidence `json:"operation_data"`
	}
	if json.Unmarshal(operation.Result, &result) != nil {
		return false
	}
	return result.OperationType == "server.destroy" && equalCanonicalJSON(result.OperationData.InstanceRef, operation.Target.InstanceRef) &&
		result.OperationData.provesDestroyed(server)
}

// destroyResultEvidence is the operation data of a server.destroy result.
type destroyResultEvidence struct {
	InstanceRef         json.RawMessage `json:"instance_ref"`
	VerifiedBackupID    string          `json:"verified_backup_id"`
	DataGenerationID    string          `json:"data_generation_id"`
	ProviderAbsence     bool            `json:"provider_absence_confirmed"`
	FinalLifecycleState string          `json:"final_lifecycle_state"`
}

// provesDestroyed reports whether the evidence names a verified backup of
// the server's data generation, confirmed provider absence, and a destroyed
// final lifecycle state.
func (e destroyResultEvidence) provesDestroyed(server LogicalServer) bool {
	return e.VerifiedBackupID != "" && e.DataGenerationID == server.DataGenerationID &&
		e.ProviderAbsence && e.FinalLifecycleState == "destroyed"
}

// destroyAbsenceObservedAt returns when the first verify-instance-absence
// step observed the target instance destroyed, and false unless that step
// succeeded with exact, timestamped evidence.
func destroyAbsenceObservedAt(operation OperationRecord) (time.Time, bool) {
	step := firstOperationStep(operation.Steps, "verify-instance-absence")
	if step == nil || step.Status != "succeeded" {
		return time.Time{}, false
	}
	var absence destroyAbsenceEvidence
	if json.Unmarshal(step.Output, &absence) != nil || !absence.confirms(operation.Target.InstanceRef) {
		return time.Time{}, false
	}
	return absence.ObservedAt, true
}

// destroyAbsenceEvidence is the output of a verify-instance-absence step.
type destroyAbsenceEvidence struct {
	InstanceRef json.RawMessage `json:"instance_ref"`
	State       string          `json:"state"`
	ObservedAt  time.Time       `json:"observed_at"`
}

// confirms reports whether the evidence shows the instance target destroyed
// at a known time.
func (e destroyAbsenceEvidence) confirms(target json.RawMessage) bool {
	return equalCanonicalJSON(e.InstanceRef, target) && e.State == "destroyed" && !e.ObservedAt.IsZero()
}

// firstOperationStep returns the first step with stepID, or nil.
func firstOperationStep(steps []OperationStep, stepID string) *OperationStep {
	for i := range steps {
		if steps[i].StepID == stepID {
			return &steps[i]
		}
	}
	return nil
}

// findDestroyedCapacityReservation returns the one unreleased reservation for
// the destroyed server, or nil when there is none. An unreleased reservation
// for a different adapter or instance, or a second match, is a conflict.
func findDestroyedCapacityReservation(bucket *bolt.Bucket, server LogicalServer, instance destroyedCapacityInstance) (*CapacityReservation, error) {
	var match *CapacityReservation
	err := forEachCapacityReservation(bucket, func(_ []byte, reservation CapacityReservation) error {
		if !unreleasedCapacityReservationFor(reservation, server) {
			return nil
		}
		if !capacityReservationTargetsInstance(reservation, instance) {
			return fmt.Errorf("%w: managed VM reservation does not match the destroyed instance", ErrCapacityConflict)
		}
		if match != nil {
			return fmt.Errorf("%w: multiple active managed VM reservations match one destroyed server", ErrCapacityConflict)
		}
		copyReservation := reservation
		match = &copyReservation
		return nil
	})
	if err != nil {
		return nil, err
	}
	return match, nil
}

// unreleasedCapacityReservationFor reports whether reservation still holds
// the server's slot.
func unreleasedCapacityReservationFor(reservation CapacityReservation, server LogicalServer) bool {
	return reservation.ServerID == server.ServerID && reservation.EnvironmentID == server.EnvironmentID && !capacityStateReleased(reservation.State)
}

// capacityReservationTargetsInstance reports whether reservation is for the
// destroyed provider instance, through its adapter when it names one.
func capacityReservationTargetsInstance(reservation CapacityReservation, instance destroyedCapacityInstance) bool {
	return (reservation.ProviderAdapterID == "" || reservation.ProviderAdapterID == instance.AdapterID) &&
		reservation.ProviderInstanceID == instance.ExternalID
}
