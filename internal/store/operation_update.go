package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// UpdateOperation replaces a record only when expectedRevision matches. The
// accepted request fields and step order remain immutable, and state changes
// must follow the protocol transition tables.
func (s *BoltStore) UpdateOperation(expectedRevision uint64, next OperationRecord) (StoredOperation, error) {
	if next.OperationID == "" {
		return StoredOperation{}, fmt.Errorf("%w: missing operation_id", ErrInvalidRecord)
	}
	var updated StoredOperation
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		updated, err = updateOperationTx(tx, expectedRevision, next)
		return err
	})
	if err != nil {
		return StoredOperation{}, err
	}
	return updated, nil
}

// updateOperationTx applies one checked operation update inside tx: it
// validates the change, assigns the store-owned timestamps, releases the
// locks of a terminal operation, and writes the next revision.
func updateOperationTx(tx *bolt.Tx, expectedRevision uint64, next OperationRecord) (StoredOperation, error) {
	current, err := loadOperationAtRevision(tx, next.OperationID, expectedRevision)
	if err != nil {
		return StoredOperation{}, err
	}
	if err := validateOperationChange(current.Record, next); err != nil {
		return StoredOperation{}, err
	}
	next = stampOperationUpdate(current.Record, next, monotonicTimestamp(current.Record.UpdatedAt, time.Now().UTC()))
	if err := validateRecord(next); err != nil {
		return StoredOperation{}, err
	}
	if err := validateTerminalOutcome(next); err != nil {
		return StoredOperation{}, err
	}
	if protocolstate.IsTerminalOperation(next.Status) {
		if err := releaseOperationLocks(tx, next.OperationID); err != nil {
			return StoredOperation{}, err
		}
	}
	if current.Revision == ^uint64(0) {
		return StoredOperation{}, fmt.Errorf("%w: revision exhausted", ErrInvalidRecord)
	}
	updated := StoredOperation{Record: next, Revision: current.Revision + 1}
	if err := putOperation(tx, updated); err != nil {
		return StoredOperation{}, err
	}
	return updated, nil
}

// loadOperationAtRevision reads and decodes the stored operation and
// requires it to be at expectedRevision.
func loadOperationAtRevision(tx *bolt.Tx, operationID string, expectedRevision uint64) (StoredOperation, error) {
	value := tx.Bucket(operationsBucket).Get([]byte(operationID))
	if value == nil {
		return StoredOperation{}, ErrNotFound
	}
	current, err := decodeEnvelope(value)
	if err != nil {
		return StoredOperation{}, err
	}
	if current.Revision != expectedRevision {
		return StoredOperation{}, ErrRevisionConflict
	}
	return current, nil
}

// validateOperationChange rejects a change to a terminal operation or to
// the accepted request, an illegal status transition, and an illegal step
// change.
func validateOperationChange(current, next OperationRecord) error {
	if protocolstate.IsTerminalOperation(current.Status) {
		return ErrTerminalOperation
	}
	if !sameAcceptedRequest(current, next) {
		return ErrImmutableFields
	}
	if next.Status != current.Status {
		if err := protocolstate.ValidateOperationTransition(current.Status, next.Status); err != nil {
			return err
		}
	}
	return validateStepChanges(current.Steps, next.Steps, next.Status)
}

// stampOperationUpdate replaces the caller's timestamps with the store's:
// creation and first start are kept, the update and any start or
// completion happen now.
func stampOperationUpdate(current, next OperationRecord, now time.Time) OperationRecord {
	if next.Steps == nil {
		next.Steps = []OperationStep{}
	}
	next.CreatedAt = current.CreatedAt
	next.UpdatedAt = now
	next.StartedAt = current.StartedAt
	if next.StartedAt == nil && next.Status == protocolstate.OperationRunning {
		startedAt := now
		next.StartedAt = &startedAt
	}
	next.Steps = updateStepTimestamps(current.Steps, next.Steps, now)
	next.CompletedAt = nil
	if protocolstate.IsTerminalOperation(next.Status) {
		completedAt := now
		next.CompletedAt = &completedAt
	}
	return next
}

func putOperation(tx *bolt.Tx, stored StoredOperation) error {
	encoded, err := json.Marshal(operationEnvelope{Revision: stored.Revision, Record: stored.Record})
	if err != nil {
		return fmt.Errorf("encode operation record: %w", err)
	}
	return tx.Bucket(operationsBucket).Put([]byte(stored.Record.OperationID), encoded)
}

// sameAcceptedRequest reports whether next keeps every field that was fixed
// when the request was accepted.
func sameAcceptedRequest(current, next OperationRecord) bool {
	return acceptedIdentityOf(current) == acceptedIdentityOf(next) &&
		sameOperationTarget(current.Target, next.Target) &&
		sameRequestSnapshot(current.RequestSnapshot, next.RequestSnapshot) &&
		equalTimePointer(current.Deadline, next.Deadline)
}

// acceptedIdentity holds the comparable scalar fields of an accepted request.
type acceptedIdentity struct {
	backupInitialLifecycleState protocolstate.ServerState
	operationID                 string
	operationType               string
	protocolVersion             string
	schemaPackageRevision       string
	actorID                     string
	correlationID               string
	idempotencyKey              string
	requestFingerprint          string
}

func acceptedIdentityOf(record OperationRecord) acceptedIdentity {
	return acceptedIdentity{
		backupInitialLifecycleState: record.BackupInitialLifecycleState,
		operationID:                 record.OperationID,
		operationType:               record.OperationType,
		protocolVersion:             record.ProtocolVersion,
		schemaPackageRevision:       record.SchemaPackageRevision,
		actorID:                     record.ActorID,
		correlationID:               record.CorrelationID,
		idempotencyKey:              record.IdempotencyKey,
		requestFingerprint:          record.RequestFingerprint,
	}
}

func sameOperationTarget(current, next OperationTarget) bool {
	return current.ServerID == next.ServerID && current.EnvironmentID == next.EnvironmentID && bytes.Equal(current.InstanceRef, next.InstanceRef)
}

func sameRequestSnapshot(current, next RequestSnapshot) bool {
	return current.OperationType == next.OperationType && bytes.Equal(current.Parameters, next.Parameters) &&
		secretReferencesEqual(current.SecretRefs, next.SecretRefs)
}

func secretReferencesEqual(left, right []SecretReference) bool {
	return slices.EqualFunc(left, right, sameSecretReference)
}

func sameSecretReference(left, right SecretReference) bool {
	return left.SecretRef == right.SecretRef && left.Purpose == right.Purpose && left.VersionRef == right.VersionRef &&
		slices.Equal(left.PermissionScope, right.PermissionScope)
}

func equalTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func updateStepTimestamps(current, next []OperationStep, now time.Time) []OperationStep {
	for i := range next {
		if i >= len(current) {
			next[i].StartedAt = nil
			next[i].CompletedAt = nil
			continue
		}
		previous := current[i]
		next[i].StartedAt = previous.StartedAt
		next[i].CompletedAt = previous.CompletedAt
		if previous.Status == next[i].Status {
			continue
		}
		if next[i].Status == protocolstate.StepRunning && next[i].StartedAt == nil {
			startedAt := now
			next[i].StartedAt = &startedAt
		}
		switch next[i].Status {
		case protocolstate.StepSucceeded, protocolstate.StepFailed, protocolstate.StepSkipped, protocolstate.StepCancelled:
			completedAt := now
			next[i].CompletedAt = &completedAt
		}
	}
	return next
}

func validateTerminalOutcome(record OperationRecord) error {
	if !protocolstate.IsTerminalOperation(record.Status) {
		return nil
	}
	if record.CompletedAt == nil {
		return fmt.Errorf("%w: terminal operation requires completed_at", ErrInvalidRecord)
	}
	for _, step := range record.Steps {
		switch step.Status {
		case protocolstate.StepRunning, protocolstate.StepWaiting, protocolstate.StepOutcomeUnknown:
			return ErrUnresolvedEffect
		case protocolstate.StepPending:
			if record.Status == protocolstate.OperationSucceeded {
				return fmt.Errorf("%w: successful operation has a pending step", ErrInvalidRecord)
			}
		}
		if record.Status == protocolstate.OperationSucceeded && step.Status != protocolstate.StepSucceeded && step.Status != protocolstate.StepSkipped {
			return fmt.Errorf("%w: successful operation has an incomplete step", ErrInvalidRecord)
		}
	}
	switch record.Status {
	case protocolstate.OperationSucceeded:
		if len(record.Result) == 0 {
			return fmt.Errorf("%w: success requires a result", ErrInvalidRecord)
		}
	case protocolstate.OperationFailed:
		if len(record.Error) == 0 {
			return fmt.Errorf("%w: failure requires an error", ErrInvalidRecord)
		}
	}
	return nil
}

func monotonicTimestamp(previous, now time.Time) time.Time {
	if now.After(previous) {
		return now
	}
	return previous.Add(time.Nanosecond)
}
