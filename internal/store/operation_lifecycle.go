package store

import (
	"encoding/json"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// UpdateOperationAndLogicalServer commits one operation update and its
// corresponding lifecycle state transition atomically. The next state is
// checked against the protocol transition table and the event is derived from
// the persisted operation identity and completed step evidence.
func (s *BoltStore) UpdateOperationAndLogicalServer(expectedOperationRevision uint64, operation OperationRecord, expectedServerRevision uint64, server LogicalServer) (StoredOperation, StoredLogicalServer, error) {
	if err := validateLifecycleCommitRequest(operation); err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	commit := lifecycleCommit{
		expectedOperationRevision: expectedOperationRevision, operation: operation,
		expectedServerRevision: expectedServerRevision, server: server,
	}
	var updatedOperation StoredOperation
	var updatedServer StoredLogicalServer
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		updatedOperation, updatedServer, err = commitLifecycleTx(tx, commit)
		return err
	})
	if err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	return updatedOperation, updatedServer, nil
}

// lifecycleCommit is one requested operation update and the logical-server
// state it moves to, with the revisions the caller last read.
type lifecycleCommit struct {
	expectedOperationRevision uint64
	operation                 OperationRecord
	expectedServerRevision    uint64
	server                    LogicalServer
}

// validateLifecycleCommitRequest checks the request before any read: a
// backup must have recorded its context, the target must be named, and only
// a mutating server operation may commit a lifecycle transition.
func validateLifecycleCommitRequest(operation OperationRecord) error {
	if operation.OperationType == "server.backup" && operation.BackupInitialLifecycleState == "" {
		return fmt.Errorf("%w: backup context must precede lifecycle transition", ErrInvalidRecord)
	}
	if !lifecycleTargetNamed(operation) {
		return fmt.Errorf("%w: operation target is required for lifecycle commit", ErrInvalidRecord)
	}
	if !mutatesLogicalServer(operation.OperationType) {
		return fmt.Errorf("%w: only mutating server operations may commit lifecycle transitions", ErrInvalidRecord)
	}
	return nil
}

func lifecycleTargetNamed(operation OperationRecord) bool {
	return operation.OperationID != "" && operation.Target.ServerID != "" && operation.Target.EnvironmentID != ""
}

func mutatesLogicalServer(operationType string) bool {
	return strings.HasPrefix(operationType, "server.") && operationType != "server.define"
}

// commitLifecycleTx runs the whole lifecycle commit inside one transaction:
// the locked operation update, then the checked server transition, its
// state event, and any capacity outcome.
func commitLifecycleTx(tx *bolt.Tx, commit lifecycleCommit) (StoredOperation, StoredLogicalServer, error) {
	operation := commit.operation
	currentOperation, err := loadLockedOperation(tx, operation.OperationID, commit.expectedOperationRevision)
	if err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	committed, err := updateOperationTx(tx, commit.expectedOperationRevision, operation)
	if err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	current, err := loadLifecycleServer(tx, operation.Target, commit.expectedServerRevision)
	if err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	if err := validateLifecycleTransition(currentOperation.Record, current, commit.server); err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	server := nextLifecycleServer(current.Record, commit.server, committed.Record)
	if err := validatePersistedLogicalServer(server); err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	updatedServer := StoredLogicalServer{Record: server, Revision: current.Revision + 1}
	if err := putLogicalServer(tx, logicalServerKey(operation.Target.EnvironmentID, operation.Target.ServerID), updatedServer); err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	if err := recordCapacityLifecycleOutcomeTx(tx, committed.Record, server); err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	return committed, updatedServer, nil
}

// loadLockedOperation reads the operation at expectedRevision and, for a
// server operation, requires it to hold its durable logical-server lock.
func loadLockedOperation(tx *bolt.Tx, operationID string, expectedRevision uint64) (StoredOperation, error) {
	current, err := loadOperationAtRevision(tx, operationID, expectedRevision)
	if err != nil {
		return StoredOperation{}, err
	}
	if requiredScope, required := requiredLogicalServerLock(current.Record); required {
		if err := requireOperationLock(tx, operationID, requiredScope); err != nil {
			return StoredOperation{}, err
		}
	}
	return current, nil
}

// loadLifecycleServer reads the target's logical server at expectedRevision
// and requires its stored record to be valid.
func loadLifecycleServer(tx *bolt.Tx, target OperationTarget, expectedRevision uint64) (StoredLogicalServer, error) {
	current, err := readLogicalServerTx(tx, target, "logical server key or revision mismatch")
	if err != nil {
		return StoredLogicalServer{}, err
	}
	if current.Revision != expectedRevision {
		return StoredLogicalServer{}, ErrRevisionConflict
	}
	if err := requireValidStoredServer(current.Record); err != nil {
		return StoredLogicalServer{}, err
	}
	return current, nil
}

// readLogicalServerTx decodes the logical server stored under the target's
// key. mismatch is the corruption message for a record with no revision or
// with another identity than its key.
func readLogicalServerTx(tx *bolt.Tx, target OperationTarget, mismatch string) (StoredLogicalServer, error) {
	value := tx.Bucket(logicalServersBucket).Get(logicalServerKey(target.EnvironmentID, target.ServerID))
	if value == nil {
		return StoredLogicalServer{}, ErrLogicalServerNotFound
	}
	var server StoredLogicalServer
	if err := json.Unmarshal(value, &server); err != nil {
		return StoredLogicalServer{}, fmt.Errorf("%w: decode logical server: %v", ErrCorruptStore, err)
	}
	if server.Revision == 0 || server.Record.EnvironmentID != target.EnvironmentID || server.Record.ServerID != target.ServerID {
		return StoredLogicalServer{}, fmt.Errorf("%w: %s", ErrCorruptStore, mismatch)
	}
	return server, nil
}

func requireValidStoredServer(record LogicalServer) error {
	if err := validatePersistedLogicalServer(record); err != nil {
		return fmt.Errorf("%w: invalid logical server record", ErrCorruptStore)
	}
	return nil
}

// validateLifecycleTransition requires the operation to target the server's
// current instance, the server identity to be unchanged, and the state to
// advance along a protocol transition with a revision left to use.
func validateLifecycleTransition(operation OperationRecord, current StoredLogicalServer, next LogicalServer) error {
	if len(operation.Target.InstanceRef) > 0 && !equalCanonicalJSON(operation.Target.InstanceRef, current.Record.InstanceRef) {
		return ErrLogicalServerTargetMismatch
	}
	if !sameLogicalServerIdentity(current.Record, next) {
		return ErrImmutableFields
	}
	if next.LifecycleState == current.Record.LifecycleState {
		return fmt.Errorf("%w: lifecycle commit must advance the server state", ErrInvalidRecord)
	}
	if err := protocolstate.ValidateServerTransition(current.Record.LifecycleState, next.LifecycleState); err != nil {
		return err
	}
	if current.Revision == ^uint64(0) {
		return fmt.Errorf("%w: logical server revision exhausted", ErrInvalidRecord)
	}
	return nil
}

// nextLifecycleServer returns next with the store-owned history: the
// current events plus one for this transition, the committing operation,
// and a monotonic update time.
func nextLifecycleServer(current, next LogicalServer, committed OperationRecord) LogicalServer {
	next.LifecycleEvents = append([]LogicalServerStateEvent(nil), current.LifecycleEvents...)
	next.LastOperationID = committed.OperationID
	next.UpdatedAt = monotonicTimestamp(current.UpdatedAt, committed.UpdatedAt)
	next.LifecycleEvents = append(next.LifecycleEvents, LogicalServerStateEvent{
		Sequence: uint64(len(next.LifecycleEvents) + 1), OperationID: committed.OperationID,
		ActorID: committed.ActorID, OperationType: committed.OperationType,
		FromState: current.LifecycleState, ToState: next.LifecycleState,
		Cause: committed.OperationType, RequestFingerprint: committed.RequestFingerprint,
		EvidenceStepIDs: succeededStepIDs(committed.Steps), OccurredAt: next.UpdatedAt,
	})
	return next
}

func succeededStepIDs(steps []OperationStep) []string {
	evidence := make([]string, 0, len(steps))
	for _, step := range steps {
		if step.Status == protocolstate.StepSucceeded {
			evidence = append(evidence, step.StepID)
		}
	}
	return evidence
}

func putLogicalServer(tx *bolt.Tx, key []byte, stored StoredLogicalServer) error {
	encoded, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("encode logical server record: %w", err)
	}
	return tx.Bucket(logicalServersBucket).Put(key, encoded)
}
