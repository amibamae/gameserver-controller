// Package store persists operation records before any controller side effect.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

const (
	operationsBucketName = "operations"
	metadataBucketName   = "metadata"
	databaseSchema       = "1"
)

var (
	operationsBucket = []byte(operationsBucketName)
	metadataBucket   = []byte(metadataBucketName)
	schemaKey        = []byte("schema_version")
)

// BoltStore is a single-controller-process, durable operation store. bbolt
// serializes writes and commits each transaction to disk before returning.
type BoltStore struct {
	db *bolt.DB
}

type operationEnvelope struct {
	Revision uint64          `json:"revision"`
	Record   OperationRecord `json:"record"`
}

// OpenBolt opens or creates a private bbolt database at path. The file itself
// is restricted to the current user; parent directories retain their existing
// permissions.
func OpenBolt(path string) (*BoltStore, error) {
	if path == "" {
		return nil, fmt.Errorf("open operation store: %w: empty path", ErrInvalidRecord)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create operation store directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bbolt operation store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("restrict operation store permissions: %w", err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists(metadataBucket)
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(operationsBucket); err != nil {
			return err
		}
		version := meta.Get(schemaKey)
		if version == nil {
			return meta.Put(schemaKey, []byte(databaseSchema))
		}
		if !bytes.Equal(version, []byte(databaseSchema)) {
			return fmt.Errorf("unsupported operation store schema version %q", version)
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize operation store: %w", err)
	}
	return &BoltStore{db: db}, nil
}

func (s *BoltStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateOperation commits a pending operation and its initial recovery state.
// A successful return means the operation record is durable and can be read
// after a process restart.
func (s *BoltStore) CreateOperation(record OperationRecord) (StoredOperation, error) {
	record, err := prepareNewRecord(record, time.Now().UTC())
	if err != nil {
		return StoredOperation{}, err
	}
	envelope := operationEnvelope{Revision: 1, Record: record}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return StoredOperation{}, fmt.Errorf("encode operation record: %w", err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationsBucket)
		if bucket.Get([]byte(record.OperationID)) != nil {
			return ErrAlreadyExists
		}
		return bucket.Put([]byte(record.OperationID), encoded)
	}); err != nil {
		return StoredOperation{}, err
	}
	return StoredOperation{Record: record, Revision: envelope.Revision}, nil
}

func (s *BoltStore) GetOperation(operationID string) (StoredOperation, error) {
	if operationID == "" {
		return StoredOperation{}, ErrNotFound
	}
	var stored StoredOperation
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(operationsBucket).Get([]byte(operationID))
		if value == nil {
			return ErrNotFound
		}
		var err error
		stored, err = decodeEnvelope(value)
		return err
	})
	if err != nil {
		return StoredOperation{}, err
	}
	return stored, nil
}

// UpdateOperation replaces a record only when expectedRevision matches. The
// accepted request fields and step order remain immutable, and state changes
// must follow the protocol transition tables.
func (s *BoltStore) UpdateOperation(expectedRevision uint64, next OperationRecord) (StoredOperation, error) {
	if next.OperationID == "" {
		return StoredOperation{}, fmt.Errorf("%w: missing operation_id", ErrInvalidRecord)
	}
	var updated StoredOperation
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(operationsBucket)
		value := bucket.Get([]byte(next.OperationID))
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
		if protocolstate.IsTerminalOperation(current.Record.Status) {
			return ErrTerminalOperation
		}
		if !sameAcceptedRequest(current.Record, next) {
			return ErrImmutableFields
		}
		if next.Status != current.Record.Status {
			if err := protocolstate.ValidateOperationTransition(current.Record.Status, next.Status); err != nil {
				return err
			}
		}
		if err := validateStepChanges(current.Record.Steps, next.Steps, next.Status); err != nil {
			return err
		}
		if next.Steps == nil {
			next.Steps = []OperationStep{}
		}
		now := monotonicTimestamp(current.Record.UpdatedAt, time.Now().UTC())
		next.CreatedAt = current.Record.CreatedAt
		next.UpdatedAt = now
		next.StartedAt = current.Record.StartedAt
		if next.StartedAt == nil && next.Status == protocolstate.OperationRunning {
			startedAt := now
			next.StartedAt = &startedAt
		}
		next.Steps = updateStepTimestamps(current.Record.Steps, next.Steps, now)
		if protocolstate.IsTerminalOperation(next.Status) {
			completedAt := now
			next.CompletedAt = &completedAt
		} else {
			next.CompletedAt = nil
		}
		if err := validateRecord(next); err != nil {
			return err
		}
		if err := validateTerminalOutcome(next); err != nil {
			return err
		}
		if current.Revision == ^uint64(0) {
			return fmt.Errorf("%w: revision exhausted", ErrInvalidRecord)
		}
		updated = StoredOperation{Record: next, Revision: current.Revision + 1}
		encoded, err := json.Marshal(operationEnvelope{Revision: updated.Revision, Record: updated.Record})
		if err != nil {
			return fmt.Errorf("encode operation record: %w", err)
		}
		return bucket.Put([]byte(next.OperationID), encoded)
	})
	if err != nil {
		return StoredOperation{}, err
	}
	return updated, nil
}

// ListRecoverableOperations returns every non-terminal operation, including
// operations suspended in needs_operator, in stable creation order.
func (s *BoltStore) ListRecoverableOperations() ([]StoredOperation, error) {
	var result []StoredOperation
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(operationsBucket).Cursor()
		for _, value := cursor.First(); value != nil; _, value = cursor.Next() {
			stored, err := decodeEnvelope(value)
			if err != nil {
				return err
			}
			if !protocolstate.IsTerminalOperation(stored.Record.Status) {
				result = append(result, stored)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Record.CreatedAt.Equal(result[j].Record.CreatedAt) {
			return result[i].Record.OperationID < result[j].Record.OperationID
		}
		return result[i].Record.CreatedAt.Before(result[j].Record.CreatedAt)
	})
	return result, nil
}

func decodeEnvelope(value []byte) (StoredOperation, error) {
	var envelope operationEnvelope
	if err := json.Unmarshal(value, &envelope); err != nil {
		return StoredOperation{}, fmt.Errorf("%w: decode operation: %v", ErrCorruptStore, err)
	}
	if envelope.Revision == 0 || validateRecord(envelope.Record) != nil {
		return StoredOperation{}, fmt.Errorf("%w: invalid stored operation", ErrCorruptStore)
	}
	if err := validateTerminalOutcome(envelope.Record); err != nil {
		return StoredOperation{}, fmt.Errorf("%w: invalid terminal operation: %v", ErrCorruptStore, err)
	}
	return StoredOperation{Record: envelope.Record, Revision: envelope.Revision}, nil
}

func sameAcceptedRequest(current, next OperationRecord) bool {
	return current.OperationID == next.OperationID &&
		current.OperationType == next.OperationType &&
		current.ProtocolVersion == next.ProtocolVersion &&
		current.SchemaPackageRevision == next.SchemaPackageRevision &&
		current.Target.ServerID == next.Target.ServerID &&
		current.Target.EnvironmentID == next.Target.EnvironmentID &&
		bytes.Equal(current.Target.InstanceRef, next.Target.InstanceRef) &&
		current.ActorID == next.ActorID &&
		current.CorrelationID == next.CorrelationID &&
		current.IdempotencyKey == next.IdempotencyKey &&
		current.RequestFingerprint == next.RequestFingerprint &&
		current.RequestSnapshot.OperationType == next.RequestSnapshot.OperationType &&
		bytes.Equal(current.RequestSnapshot.Parameters, next.RequestSnapshot.Parameters) &&
		secretReferencesEqual(current.RequestSnapshot.SecretRefs, next.RequestSnapshot.SecretRefs) &&
		equalTimePointer(current.Deadline, next.Deadline)
}

func secretReferencesEqual(left, right []SecretReference) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].SecretRef != right[i].SecretRef || left[i].Purpose != right[i].Purpose || left[i].VersionRef != right[i].VersionRef || len(left[i].PermissionScope) != len(right[i].PermissionScope) {
			return false
		}
		for j := range left[i].PermissionScope {
			if left[i].PermissionScope[j] != right[i].PermissionScope[j] {
				return false
			}
		}
	}
	return true
}

func equalTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func validateStepChanges(current, next []OperationStep, operationStatus protocolstate.OperationStatus) error {
	if len(next) < len(current) {
		return fmt.Errorf("%w: operation steps cannot be removed", ErrImmutableFields)
	}
	for i, previous := range current {
		candidate := next[i]
		if previous.StepID != candidate.StepID || previous.StepType != candidate.StepType {
			return fmt.Errorf("%w: step order and identity cannot change", ErrImmutableFields)
		}
		if (previous.AdapterID != "" && previous.AdapterID != candidate.AdapterID) ||
			(previous.CapabilityName != "" && previous.CapabilityName != candidate.CapabilityName) ||
			(previous.CapabilityVersion != "" && previous.CapabilityVersion != candidate.CapabilityVersion) {
			return fmt.Errorf("%w: recorded adapter capability identity cannot change", ErrImmutableFields)
		}
		if len(previous.ExternalOperationRef) > 0 && !bytes.Equal(previous.ExternalOperationRef, candidate.ExternalOperationRef) {
			return fmt.Errorf("%w: recorded external operation reference cannot change", ErrImmutableFields)
		}
		if previous.Status != candidate.Status {
			if err := protocolstate.ValidateStepTransition(previous.Status, candidate.Status); err != nil {
				return err
			}
		}
		if candidate.Status == protocolstate.StepRunning && previous.Status != protocolstate.StepRunning {
			if operationStatus != protocolstate.OperationRunning {
				return fmt.Errorf("%w: a step may start only while the operation is running", ErrInvalidRecord)
			}
			if candidate.Attempt != previous.Attempt+1 {
				return fmt.Errorf("%w: entering running must increment step attempt by one", ErrInvalidRecord)
			}
		} else if candidate.Attempt != previous.Attempt {
			return fmt.Errorf("%w: step attempt can change only when entering running", ErrImmutableFields)
		}
	}
	for i := len(current); i < len(next); i++ {
		if next[i].Status != protocolstate.StepPending || next[i].Attempt != 0 {
			return fmt.Errorf("%w: new steps must start pending", ErrInvalidRecord)
		}
	}
	return nil
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
