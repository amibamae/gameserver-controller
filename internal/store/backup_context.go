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

func captureBackupInitialState(tx *bolt.Tx, operationID string, expectedRevision uint64) (StoredOperation, error) {
	operations := tx.Bucket(operationsBucket)
	key := []byte(operationID)
	current, err := readBackupCaptureOperation(operations.Get(key), expectedRevision)
	if err != nil {
		return StoredOperation{}, err
	}
	target := current.Record.Target
	if err := requireOperationLock(tx, operationID, LogicalServerLockScope(target.EnvironmentID, target.ServerID)); err != nil {
		return StoredOperation{}, err
	}
	server, err := readBackupCaptureServer(tx, target)
	if err != nil {
		return StoredOperation{}, err
	}
	if current.Record.BackupInitialLifecycleState != "" {
		return current, nil
	}
	return writeBackupInitialState(operations, key, current, server.LifecycleState)
}

func readBackupCaptureOperation(value []byte, expectedRevision uint64) (StoredOperation, error) {
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
	record := current.Record
	if record.OperationType != "server.backup" || protocolstate.IsTerminalOperation(record.Status) {
		return StoredOperation{}, fmt.Errorf("%w: active server.backup required", ErrInvalidRecord)
	}
	if !validBackupCaptureTarget(record.Target) {
		return StoredOperation{}, ErrLogicalServerTargetMismatch
	}
	return current, nil
}

func validBackupCaptureTarget(target OperationTarget) bool {
	var instance struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if json.Unmarshal(target.InstanceRef, &instance) != nil {
		return false
	}
	return instance.AdapterID != "" && instance.EnvironmentID == target.EnvironmentID &&
		instance.ResourceKind == "instance" && instance.ExternalID != ""
}

func readBackupCaptureServer(tx *bolt.Tx, target OperationTarget) (LogicalServer, error) {
	value := tx.Bucket(logicalServersBucket).Get(logicalServerKey(target.EnvironmentID, target.ServerID))
	if value == nil {
		return LogicalServer{}, ErrLogicalServerNotFound
	}
	var server StoredLogicalServer
	if json.Unmarshal(value, &server) != nil || server.Revision == 0 || validatePersistedLogicalServer(server.Record) != nil {
		return LogicalServer{}, ErrCorruptStore
	}
	if !backupCaptureServerMatches(target, server.Record) {
		return LogicalServer{}, ErrLogicalServerTargetMismatch
	}
	return server.Record, nil
}

func backupCaptureServerMatches(target OperationTarget, server LogicalServer) bool {
	left, leftErr := jcs.Transform(target.InstanceRef)
	right, rightErr := jcs.Transform(server.InstanceRef)
	sameInstance := leftErr == nil && rightErr == nil && bytes.Equal(left, right)
	return sameInstance && server.ServerID == target.ServerID && server.EnvironmentID == target.EnvironmentID
}

func writeBackupInitialState(operations *bolt.Bucket, key []byte, current StoredOperation, state protocolstate.ServerState) (StoredOperation, error) {
	record := current.Record
	for _, step := range record.Steps {
		if step.Status != protocolstate.StepPending || step.Attempt != 0 {
			return StoredOperation{}, fmt.Errorf("%w: backup context must precede dispatch", ErrInvalidRecord)
		}
	}
	record.BackupInitialLifecycleState = state
	if record.BackupInitialLifecycleState == "" || validateRecord(record) != nil {
		return StoredOperation{}, fmt.Errorf("%w: backup requires ready, degraded, or stopped initial state", ErrInvalidRecord)
	}
	if current.Revision == ^uint64(0) {
		return StoredOperation{}, ErrInvalidRecord
	}
	record.UpdatedAt = monotonicTimestamp(record.UpdatedAt, time.Now().UTC())
	result := StoredOperation{Record: record, Revision: current.Revision + 1}
	encoded, err := json.Marshal(operationEnvelope{Record: record, Revision: result.Revision})
	if err != nil {
		return result, err
	}
	return result, operations.Put(key, encoded)
}
