package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gowebpki/jcs"
	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// BackupContextStore commits the core-owned lifecycle snapshot required before
// any server.backup step is dispatched.
type BackupContextStore interface {
	CaptureBackupInitialState(operationID string, expectedRevision uint64) (StoredOperation, error)
}

// CaptureBackupInitialState persists core-owned resume context before any
// backup step runs. The operation lock, exact target, server state, and record
// revision are checked in the same transaction as the capture.
func (s *BoltStore) CaptureBackupInitialState(operationID string, expectedRevision uint64) (StoredOperation, error) {
	var result StoredOperation
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		result, err = captureBackupInitialState(tx, operationID, expectedRevision)
		return err
	})
	return result, err
}

// captureBackupInitialState performs the in-transaction work for capturing backup initial state.
func captureBackupInitialState(tx *bolt.Tx, operationID string, expectedRevision uint64) (StoredOperation, error) {
	operations := tx.Bucket(operationsBucket)
	key := []byte(operationID)

	// Phase 1: Read and validate the existing operation record
	current, err := readAndValidateBackupOperation(operations.Get(key), expectedRevision)
	if err != nil {
		return StoredOperation{}, err
	}

	target := current.Record.Target

	// Phase 2: Acquire lock to prevent concurrent modifications
	if err := requireOperationLock(tx, operationID, LogicalServerLockScope(target.EnvironmentID, target.ServerID)); err != nil {
		return StoredOperation{}, err
	}

	// Phase 3: Read server state and validate it matches target
	server, err := readAndValidateServerState(tx, target)
	if err != nil {
		return StoredOperation{}, err
	}

	// Phase 4: If already captured, return the current state unchanged
	if current.Record.BackupInitialLifecycleState != "" {
		return current, nil
	}

	// Phase 5: Capture and persist the initial lifecycle state
	return writeAndPersistBackupInitialState(operations, key, current, server.LifecycleState)
}

// readAndValidateBackupOperation decodes and validates an operation record.
// Returns ErrNotFound if no record exists, ErrRevisionConflict on concurrent modification,
// or other validation errors for invalid records.
func readAndValidateBackupOperation(value []byte, expectedRevision uint64) (StoredOperation, error) {
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

	if !validateBackupOperationRecord(current.Record) {
		return StoredOperation{}, fmt.Errorf("%w: active server.backup required", ErrInvalidRecord)
	}

	if !validateBackupTargetInstanceRef(current.Record.Target) {
		return StoredOperation{}, ErrLogicalServerTargetMismatch
	}

	return current, nil
}

// validateBackupOperationRecord ensures the operation record is of type server.backup
// and has not reached a terminal state.
func validateBackupOperationRecord(record OperationRecord) bool {
	if record.OperationType != "server.backup" {
		return false
	}
	if protocolstate.IsTerminalOperation(record.Status) {
		return false
	}
	return true
}

// validateBackupTargetInstanceRef ensures the operation targets a valid logical server.
func validateBackupTargetInstanceRef(target OperationTarget) bool {
	var instance struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}

	if json.Unmarshal(target.InstanceRef, &instance) != nil {
		return false
	}

	return instance.AdapterID != "" &&
		instance.EnvironmentID == target.EnvironmentID &&
		instance.ResourceKind == "instance" &&
		instance.ExternalID != ""
}

// readAndValidateServerState reads the logical server and validates its state.
// Returns ErrLogicalServerNotFound if no record exists, ErrCorruptStore on invalid data,
// or ErrLogicalServerTargetMismatch when the stored server doesn't match the target.
func readAndValidateServerState(tx *bolt.Tx, target OperationTarget) (LogicalServer, error) {
	value := tx.Bucket(logicalServersBucket).Get(logicalServerKey(target.EnvironmentID, target.ServerID))
	if value == nil {
		return LogicalServer{}, ErrLogicalServerNotFound
	}

	var server StoredLogicalServer
	if json.Unmarshal(value, &server) != nil || server.Revision == 0 || validatePersistedLogicalServer(server.Record) != nil {
		return LogicalServer{}, ErrCorruptStore
	}

	if !validateBackupTargetInstanceRef(target) || server.Record.ServerID != target.ServerID {
		return LogicalServer{}, ErrLogicalServerTargetMismatch
	}

	return server.Record, nil
}

// writeAndPersistBackupInitialState writes the backup initial lifecycle state to storage.
// Validates that no steps have begun, sets the lifecycle state, validates the final record,
// updates timestamps, marshals the record, and persists it atomically.
func writeAndPersistBackupInitialState(operations *bolt.Bucket, key []byte, current StoredOperation, state protocolstate.ServerState) (StoredOperation, error) {
	record := current.Record

	// Validate that no steps have begun or been attempted
	if hasNonPendingSteps(record.Steps) {
		return StoredOperation{}, fmt.Errorf("%w: backup context must precede dispatch", ErrInvalidRecord)
	}

	// Set the initial lifecycle state
	record.BackupInitialLifecycleState = state

	// Validate the lifecycle state and record before finalizing
	if record.BackupInitialLifecycleState == "" || validateRecord(record) != nil {
		return StoredOperation{}, fmt.Errorf("%w: backup requires ready, degraded, or stopped initial state", ErrInvalidRecord)
	}

	// Reject invalid revisions
	if current.Revision == ^uint64(0) {
		return StoredOperation{}, ErrInvalidRecord
	}

	// Update timestamp using monotonic time
	record.UpdatedAt = monotonicTimestamp(record.UpdatedAt, time.Now().UTC())

	result := StoredOperation{Record: record, Revision: current.Revision + 1}
	encoded, err := json.Marshal(operationEnvelope{Record: record, Revision: result.Revision})
	if err != nil {
		return result, err
	}
	return result, operations.Put(key, encoded)
}

// hasNonPendingSteps checks if any step is not in pending state or has been attempted.
func hasNonPendingSteps(steps []OperationStep) bool {
	for _, step := range steps {
		if step.Status != protocolstate.StepPending || step.Attempt != 0 {
			return true
		}
	}
	return false
}

// backupCaptureServerMatches is deprecated; use validateBackupTargetInstanceRef instead.
func backupCaptureServerMatches(target OperationTarget, server LogicalServer) bool {
	left, leftErr := jcs.Transform(target.InstanceRef)
	right, rightErr := jcs.Transform(server.InstanceRef)
	sameInstance := leftErr == nil && rightErr == nil && bytes.Equal(left, right)
	return sameInstance && server.ServerID == target.ServerID && server.EnvironmentID == target.EnvironmentID
}
