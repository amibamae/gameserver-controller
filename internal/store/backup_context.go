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

// CaptureBackupInitialState persists core-owned resume context before any
// backup step runs. The operation lock, exact target, server state, and record
// revision are checked in the same transaction as the capture.
func (s *BoltStore) CaptureBackupInitialState(operationID string, expectedRevision uint64) (StoredOperation, error) {
	var result StoredOperation
	err := s.db.Update(func(tx *bolt.Tx) error {
		value := tx.Bucket(operationsBucket).Get([]byte(operationID))
		if value == nil {
			return ErrNotFound
		}
		current, err := decodeEnvelope(value)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return ErrRevisionConflict
		}
		record := current.Record
		if record.OperationType != "server.backup" || protocolstate.IsTerminalOperation(record.Status) {
			return fmt.Errorf("%w: active server.backup required", ErrInvalidRecord)
		}
		var target struct {
			AdapterID     string `json:"adapter_id"`
			EnvironmentID string `json:"environment_id"`
			ResourceKind  string `json:"resource_kind"`
			ExternalID    string `json:"external_id"`
		}
		if json.Unmarshal(record.Target.InstanceRef, &target) != nil || target.AdapterID == "" || target.EnvironmentID != record.Target.EnvironmentID || target.ResourceKind != "instance" || target.ExternalID == "" {
			return ErrLogicalServerTargetMismatch
		}
		if err := requireOperationLock(tx, operationID, LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID)); err != nil {
			return err
		}
		var server StoredLogicalServer
		serverJSON := tx.Bucket(logicalServersBucket).Get(logicalServerKey(record.Target.EnvironmentID, record.Target.ServerID))
		if serverJSON == nil {
			return ErrLogicalServerNotFound
		}
		if json.Unmarshal(serverJSON, &server) != nil || server.Revision == 0 || validatePersistedLogicalServer(server.Record) != nil {
			return ErrCorruptStore
		}
		left, leftErr := jcs.Transform(record.Target.InstanceRef)
		right, rightErr := jcs.Transform(server.Record.InstanceRef)
		if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) || server.Record.ServerID != record.Target.ServerID || server.Record.EnvironmentID != record.Target.EnvironmentID {
			return ErrLogicalServerTargetMismatch
		}
		if record.BackupInitialLifecycleState != "" {
			result = current
			return nil
		}
		for _, step := range record.Steps {
			if step.Status != protocolstate.StepPending || step.Attempt != 0 {
				return fmt.Errorf("%w: backup context must precede dispatch", ErrInvalidRecord)
			}
		}
		record.BackupInitialLifecycleState = server.Record.LifecycleState
		if record.BackupInitialLifecycleState == "" || validateRecord(record) != nil {
			return fmt.Errorf("%w: backup requires ready, degraded, or stopped initial state", ErrInvalidRecord)
		}
		if current.Revision == ^uint64(0) {
			return ErrInvalidRecord
		}
		record.UpdatedAt = monotonicTimestamp(record.UpdatedAt, time.Now().UTC())
		result = StoredOperation{Record: record, Revision: current.Revision + 1}
		encoded, err := json.Marshal(operationEnvelope{Record: record, Revision: result.Revision})
		if err != nil {
			return err
		}
		return tx.Bucket(operationsBucket).Put([]byte(operationID), encoded)
	})
	return result, err
}
