// Package store persists operation records before any controller side effect.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

const (
	operationsBucketName           = "operations"
	metadataBucketName             = "metadata"
	idempotencyBucketName          = "idempotency"
	locksBucketName                = "resource_locks"
	operationLocksBucketName       = "operation_locks"
	keyVersionsBucketName          = "idempotency_key_versions"
	logicalServersBucketName       = "logical_servers"
	capacityReservationsBucketName = "capacity_reservations"
	databaseSchema                 = "4"
)

var (
	operationsBucket           = []byte(operationsBucketName)
	metadataBucket             = []byte(metadataBucketName)
	idempotencyBucket          = []byte(idempotencyBucketName)
	locksBucket                = []byte(locksBucketName)
	operationLocksBucket       = []byte(operationLocksBucketName)
	keyVersionsBucket          = []byte(keyVersionsBucketName)
	logicalServersBucket       = []byte(logicalServersBucketName)
	capacityReservationsBucket = []byte(capacityReservationsBucketName)
	schemaKey                  = []byte("schema_version")
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
		for _, name := range [][]byte{idempotencyBucket, locksBucket, operationLocksBucket, keyVersionsBucket, logicalServersBucket, capacityReservationsBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		version := meta.Get(schemaKey)
		if version == nil {
			for _, name := range [][]byte{operationsBucket, idempotencyBucket, locksBucket, operationLocksBucket, keyVersionsBucket, logicalServersBucket, capacityReservationsBucket} {
				cursor := tx.Bucket(name).Cursor()
				if key, _ := cursor.First(); key != nil {
					return ErrStoreNeedsMigration
				}
			}
			return meta.Put(schemaKey, []byte(databaseSchema))
		}
		if bytes.Equal(version, []byte("1")) {
			cursor := tx.Bucket(operationsBucket).Cursor()
			if key, _ := cursor.First(); key != nil {
				return ErrStoreNeedsMigration
			}
			return meta.Put(schemaKey, []byte(databaseSchema))
		}
		if bytes.Equal(version, []byte("2")) {
			return meta.Put(schemaKey, []byte(databaseSchema))
		}
		if bytes.Equal(version, []byte("3")) {
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
	if err := db.View(verifyRecoveryIndexes); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("verify operation recovery indexes: %w", err)
	}
	return &BoltStore{db: db}, nil
}

func (s *BoltStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// createOperation is a low-level store primitive for package tests. Controller
// code must use AcceptOperation so idempotency and resource locks are atomic.
func (s *BoltStore) createOperation(record OperationRecord) (StoredOperation, error) {
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

// AcceptOperation atomically deduplicates an accepted request, reserves its
// resource locks, and persists its initial operation record. The verifier
// proof must be derived from the authenticated actor and client key by the
// acceptance service.
func (s *BoltStore) AcceptOperation(record OperationRecord, proof IdempotencyProof, lockScopes []string) (StoredOperation, bool, error) {
	if record.IdempotencyKey == "" || !validFingerprint(record.RequestFingerprint) {
		return StoredOperation{}, false, fmt.Errorf("%w: acceptance requires an idempotency key and request fingerprint", ErrInvalidRecord)
	}
	if err := validateIdempotencyProof(proof); err != nil {
		return StoredOperation{}, false, err
	}
	scopes, err := validateLockScopes(lockScopes)
	if err != nil {
		return StoredOperation{}, false, err
	}
	if requiredScope, required := requiredLogicalServerLock(record); required && !containsString(scopes, requiredScope) {
		return StoredOperation{}, false, fmt.Errorf("%w: standard server operation requires lock %q", ErrInvalidRecord, requiredScope)
	}
	prepared, err := prepareNewRecord(record, time.Now().UTC())
	if err != nil {
		return StoredOperation{}, false, err
	}
	var accepted StoredOperation
	replayed := false
	err = s.db.Update(func(tx *bolt.Tx) error {
		operations := tx.Bucket(operationsBucket)
		idempotency := tx.Bucket(idempotencyBucket)
		keyVersions := tx.Bucket(keyVersionsBucket)
		cursor := keyVersions.Cursor()
		for version, _ := cursor.First(); version != nil; version, _ = cursor.Next() {
			if _, ok := findVerifier(proof, string(version)); !ok {
				return fmt.Errorf("%w: %s", ErrKeyVersionMissing, version)
			}
		}
		var existing *StoredOperation
		for _, candidate := range proof.Candidates {
			value := idempotency.Get(verifierIndex(candidate))
			if value == nil {
				continue
			}
			entry, err := decodeIdempotencyEntry(value)
			if err != nil {
				return err
			}
			if entry.KeyVersion != candidate.KeyVersion || keyVersions.Get([]byte(entry.KeyVersion)) == nil {
				return fmt.Errorf("%w: idempotency entry key version mismatch", ErrCorruptStore)
			}
			if !requestMatches(entry, candidate) {
				return ErrIdempotencyConflict
			}
			value = operations.Get([]byte(entry.OperationID))
			if value == nil {
				return ErrIdempotencyExpired
			}
			stored, err := decodeEnvelope(value)
			if err != nil {
				return err
			}
			if stored.Record.ActorID != prepared.ActorID {
				return fmt.Errorf("%w: idempotency verifier actor mismatch", ErrCorruptStore)
			}
			if existing != nil && existing.Record.OperationID != stored.Record.OperationID {
				return fmt.Errorf("%w: verifier candidates identify multiple operations", ErrCorruptStore)
			}
			existing = &stored
		}
		if existing != nil {
			accepted = *existing
			replayed = true
			return nil
		}
		if operations.Get([]byte(prepared.OperationID)) != nil {
			return ErrAlreadyExists
		}
		var logicalServer *LogicalServer
		if prepared.OperationType == "server.define" {
			if len(prepared.Steps) != 0 || len(prepared.RequestSnapshot.SecretRefs) != 0 || len(prepared.Target.InstanceRef) != 0 {
				return fmt.Errorf("%w: server.define must contain only a logical definition", ErrInvalidRecord)
			}
			server, err := newLogicalServerFromDefinition(prepared)
			if err != nil {
				return err
			}
			key := logicalServerKey(server.EnvironmentID, server.ServerID)
			if tx.Bucket(logicalServersBucket).Get(key) != nil {
				return ErrLogicalServerAlreadyExists
			}
			server.CreatedAt = prepared.CreatedAt
			server.UpdatedAt = prepared.CreatedAt
			server.LastOperationID = prepared.OperationID
			logicalServer = &server
		} else if strings.HasPrefix(prepared.OperationType, "server.") {
			key := logicalServerKey(prepared.Target.EnvironmentID, prepared.Target.ServerID)
			value := tx.Bucket(logicalServersBucket).Get(key)
			if value == nil {
				return ErrLogicalServerNotFound
			}
			var server StoredLogicalServer
			if err := json.Unmarshal(value, &server); err != nil {
				return fmt.Errorf("%w: decode logical server: %v", ErrCorruptStore, err)
			}
			if server.Revision == 0 || server.Record.EnvironmentID != prepared.Target.EnvironmentID || server.Record.ServerID != prepared.Target.ServerID {
				return fmt.Errorf("%w: logical server key does not match its record", ErrCorruptStore)
			}
			if err := validatePersistedLogicalServer(server.Record); err != nil {
				return fmt.Errorf("%w: invalid logical server record", ErrCorruptStore)
			}
		}
		for _, scope := range scopes {
			value := tx.Bucket(locksBucket).Get([]byte(scope))
			if value == nil {
				continue
			}
			lock, err := decodeResourceLock(value)
			if err != nil {
				return err
			}
			return fmt.Errorf("%w: scope %q owned by operation %q", ErrResourceConflict, lock.Scope, lock.OperationID)
		}
		encoded, err := json.Marshal(operationEnvelope{Revision: 1, Record: prepared})
		if err != nil {
			return fmt.Errorf("encode operation record: %w", err)
		}
		if err := operations.Put([]byte(prepared.OperationID), encoded); err != nil {
			return err
		}
		if logicalServer != nil {
			serverJSON, err := encodeLogicalServer(*logicalServer)
			if err != nil {
				return err
			}
			if err := tx.Bucket(logicalServersBucket).Put(logicalServerKey(logicalServer.EnvironmentID, logicalServer.ServerID), serverJSON); err != nil {
				return err
			}
		}
		activeVerifier, _ := findVerifier(proof, proof.ActiveKeyVersion)
		entry := idempotencyEntry{
			OperationID:   prepared.OperationID,
			BindingDigest: activeVerifier.BindingDigest,
			KeyVersion:    proof.ActiveKeyVersion,
			RecordedAt:    prepared.CreatedAt,
		}
		entryJSON, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("encode idempotency entry: %w", err)
		}
		if err := idempotency.Put(verifierIndex(activeVerifier), entryJSON); err != nil {
			return err
		}
		if err := keyVersions.Put([]byte(proof.ActiveKeyVersion), []byte{1}); err != nil {
			return err
		}
		if len(scopes) > 0 {
			locks := tx.Bucket(locksBucket)
			for _, scope := range scopes {
				lockJSON, err := json.Marshal(ResourceLock{Scope: scope, OperationID: prepared.OperationID, AcquiredAt: prepared.CreatedAt})
				if err != nil {
					return err
				}
				if err := locks.Put([]byte(scope), lockJSON); err != nil {
					return err
				}
			}
			scopesJSON, err := json.Marshal(scopes)
			if err != nil {
				return err
			}
			if err := tx.Bucket(operationLocksBucket).Put([]byte(prepared.OperationID), scopesJSON); err != nil {
				return err
			}
		}
		accepted = StoredOperation{Record: prepared, Revision: 1}
		return nil
	})
	if err != nil {
		return StoredOperation{}, false, err
	}
	return accepted, replayed, nil
}

func (s *BoltStore) GetResourceLock(scope string) (ResourceLock, error) {
	var lock ResourceLock
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(locksBucket).Get([]byte(scope))
		if value == nil {
			return ErrNotFound
		}
		var err error
		lock, err = decodeResourceLock(value)
		if err == nil && lock.Scope != scope {
			return fmt.Errorf("%w: resource lock scope mismatch", ErrCorruptStore)
		}
		return err
	})
	return lock, err
}

func (s *BoltStore) ListResourceLocks() ([]ResourceLock, error) {
	var result []ResourceLock
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(locksBucket).Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			lock, err := decodeResourceLock(value)
			if err != nil {
				return err
			}
			if lock.Scope != string(key) {
				return fmt.Errorf("%w: resource lock scope mismatch", ErrCorruptStore)
			}
			result = append(result, lock)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
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
		var err error
		updated, err = updateOperationTx(tx, expectedRevision, next)
		return err
	})
	if err != nil {
		return StoredOperation{}, err
	}
	return updated, nil
}

// UpdateOperationAndLogicalServer commits one operation update and its
// corresponding lifecycle state transition atomically. The next state is
// checked against the protocol transition table and the event is derived from
// the persisted operation identity and completed step evidence.
func (s *BoltStore) UpdateOperationAndLogicalServer(expectedOperationRevision uint64, operation OperationRecord, expectedServerRevision uint64, server LogicalServer) (StoredOperation, StoredLogicalServer, error) {
	if operation.OperationType == "server.backup" && operation.BackupInitialLifecycleState == "" {
		return StoredOperation{}, StoredLogicalServer{}, fmt.Errorf("%w: backup context must precede lifecycle transition", ErrInvalidRecord)
	}
	if operation.OperationID == "" || operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" {
		return StoredOperation{}, StoredLogicalServer{}, fmt.Errorf("%w: operation target is required for lifecycle commit", ErrInvalidRecord)
	}
	if !strings.HasPrefix(operation.OperationType, "server.") || operation.OperationType == "server.define" {
		return StoredOperation{}, StoredLogicalServer{}, fmt.Errorf("%w: only mutating server operations may commit lifecycle transitions", ErrInvalidRecord)
	}
	var updatedOperation StoredOperation
	var updatedServer StoredLogicalServer
	err := s.db.Update(func(tx *bolt.Tx) error {
		operationValue := tx.Bucket(operationsBucket).Get([]byte(operation.OperationID))
		if operationValue == nil {
			return ErrNotFound
		}
		currentOperation, err := decodeEnvelope(operationValue)
		if err != nil {
			return err
		}
		if currentOperation.Revision != expectedOperationRevision {
			return ErrRevisionConflict
		}
		if requiredScope, required := requiredLogicalServerLock(currentOperation.Record); required {
			if err := requireOperationLock(tx, operation.OperationID, requiredScope); err != nil {
				return err
			}
		}
		committed, err := updateOperationTx(tx, expectedOperationRevision, operation)
		if err != nil {
			return err
		}
		serverKey := logicalServerKey(operation.Target.EnvironmentID, operation.Target.ServerID)
		value := tx.Bucket(logicalServersBucket).Get(serverKey)
		if value == nil {
			return ErrLogicalServerNotFound
		}
		var current StoredLogicalServer
		if err := json.Unmarshal(value, &current); err != nil {
			return fmt.Errorf("%w: decode logical server: %v", ErrCorruptStore, err)
		}
		if current.Revision == 0 || current.Record.EnvironmentID != operation.Target.EnvironmentID || current.Record.ServerID != operation.Target.ServerID {
			return fmt.Errorf("%w: logical server key or revision mismatch", ErrCorruptStore)
		}
		if current.Revision != expectedServerRevision {
			return ErrRevisionConflict
		}
		if err := validatePersistedLogicalServer(current.Record); err != nil {
			return fmt.Errorf("%w: invalid logical server record", ErrCorruptStore)
		}
		if len(currentOperation.Record.Target.InstanceRef) > 0 && !equalCanonicalJSON(currentOperation.Record.Target.InstanceRef, current.Record.InstanceRef) {
			return ErrLogicalServerTargetMismatch
		}
		if !sameLogicalServerIdentity(current.Record, server) {
			return ErrImmutableFields
		}
		if server.LifecycleState == current.Record.LifecycleState {
			return fmt.Errorf("%w: lifecycle commit must advance the server state", ErrInvalidRecord)
		}
		if err := protocolstate.ValidateServerTransition(current.Record.LifecycleState, server.LifecycleState); err != nil {
			return err
		}
		if current.Revision == ^uint64(0) {
			return fmt.Errorf("%w: logical server revision exhausted", ErrInvalidRecord)
		}
		server.LifecycleEvents = append([]LogicalServerStateEvent(nil), current.Record.LifecycleEvents...)
		server.LastOperationID = operation.OperationID
		server.UpdatedAt = monotonicTimestamp(current.Record.UpdatedAt, committed.Record.UpdatedAt)
		evidence := make([]string, 0, len(operation.Steps))
		for _, step := range operation.Steps {
			if step.Status == protocolstate.StepSucceeded {
				evidence = append(evidence, step.StepID)
			}
		}
		server.LifecycleEvents = append(server.LifecycleEvents, LogicalServerStateEvent{
			Sequence: uint64(len(server.LifecycleEvents) + 1), OperationID: operation.OperationID,
			ActorID: committed.Record.ActorID, OperationType: committed.Record.OperationType,
			FromState: current.Record.LifecycleState, ToState: server.LifecycleState,
			Cause: committed.Record.OperationType, RequestFingerprint: committed.Record.RequestFingerprint,
			EvidenceStepIDs: evidence, OccurredAt: server.UpdatedAt,
		})
		if err := validatePersistedLogicalServer(server); err != nil {
			return err
		}
		updatedServer = StoredLogicalServer{Record: server, Revision: current.Revision + 1}
		encodedServer, err := json.Marshal(updatedServer)
		if err != nil {
			return fmt.Errorf("encode logical server record: %w", err)
		}
		if err := tx.Bucket(logicalServersBucket).Put(serverKey, encodedServer); err != nil {
			return err
		}
		if err := recordCapacityLifecycleOutcomeTx(tx, committed.Record, server); err != nil {
			return err
		}
		updatedOperation = committed
		return nil
	})
	if err != nil {
		return StoredOperation{}, StoredLogicalServer{}, err
	}
	return updatedOperation, updatedServer, nil
}

func requireOperationLock(tx *bolt.Tx, operationID, requiredScope string) error {
	value := tx.Bucket(operationLocksBucket).Get([]byte(operationID))
	if value == nil {
		return fmt.Errorf("%w: operation %q has no durable lock index", ErrCorruptStore, operationID)
	}
	var scopes []string
	if err := json.Unmarshal(value, &scopes); err != nil {
		return fmt.Errorf("%w: decode operation lock scopes: %v", ErrCorruptStore, err)
	}
	validated, err := validateLockScopes(scopes)
	if err != nil || len(validated) != len(scopes) {
		return fmt.Errorf("%w: operation has invalid lock scopes", ErrCorruptStore)
	}
	if !containsString(validated, requiredScope) {
		return fmt.Errorf("%w: operation lock index omits required server lock %q", ErrCorruptStore, requiredScope)
	}
	value = tx.Bucket(locksBucket).Get([]byte(requiredScope))
	if value == nil {
		return fmt.Errorf("%w: required server lock %q is missing", ErrCorruptStore, requiredScope)
	}
	lock, err := decodeResourceLock(value)
	if err != nil {
		return err
	}
	if lock.OperationID != operationID || lock.Scope != requiredScope {
		return fmt.Errorf("%w: required server lock %q has a mismatched owner or scope", ErrCorruptStore, requiredScope)
	}
	return nil
}

func updateOperationTx(tx *bolt.Tx, expectedRevision uint64, next OperationRecord) (StoredOperation, error) {
	bucket := tx.Bucket(operationsBucket)
	value := bucket.Get([]byte(next.OperationID))
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
	if protocolstate.IsTerminalOperation(current.Record.Status) {
		return StoredOperation{}, ErrTerminalOperation
	}
	if !sameAcceptedRequest(current.Record, next) {
		return StoredOperation{}, ErrImmutableFields
	}
	if next.Status != current.Record.Status {
		if err := protocolstate.ValidateOperationTransition(current.Record.Status, next.Status); err != nil {
			return StoredOperation{}, err
		}
	}
	if err := validateStepChanges(current.Record.Steps, next.Steps, next.Status); err != nil {
		return StoredOperation{}, err
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
	encoded, err := json.Marshal(operationEnvelope{Revision: updated.Revision, Record: updated.Record})
	if err != nil {
		return StoredOperation{}, fmt.Errorf("encode operation record: %w", err)
	}
	if err := bucket.Put([]byte(next.OperationID), encoded); err != nil {
		return StoredOperation{}, err
	}
	return updated, nil
}

func releaseOperationLocks(tx *bolt.Tx, operationID string) error {
	operationLocks := tx.Bucket(operationLocksBucket)
	value := operationLocks.Get([]byte(operationID))
	if value == nil {
		return nil
	}
	var scopes []string
	if err := json.Unmarshal(value, &scopes); err != nil {
		return fmt.Errorf("%w: decode operation lock scopes: %v", ErrCorruptStore, err)
	}
	validated, err := validateLockScopes(scopes)
	if err != nil || len(validated) != len(scopes) {
		return fmt.Errorf("%w: invalid operation lock scopes", ErrCorruptStore)
	}
	locks := tx.Bucket(locksBucket)
	for _, scope := range validated {
		value := locks.Get([]byte(scope))
		if value == nil {
			return fmt.Errorf("%w: operation lock %q is missing", ErrCorruptStore, scope)
		}
		lock, err := decodeResourceLock(value)
		if err != nil {
			return err
		}
		if lock.Scope != scope || lock.OperationID != operationID {
			return fmt.Errorf("%w: operation does not own lock %q", ErrCorruptStore, scope)
		}
		if err := locks.Delete([]byte(scope)); err != nil {
			return err
		}
	}
	return operationLocks.Delete([]byte(operationID))
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
	return current.BackupInitialLifecycleState == next.BackupInitialLifecycleState &&
		current.OperationID == next.OperationID &&
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
	if len(next) != len(current) {
		return fmt.Errorf("%w: accepted operation steps cannot be added or removed", ErrImmutableFields)
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
		if !sameInputBindings(previous.InputBindings, candidate.InputBindings) {
			return fmt.Errorf("%w: recorded step input bindings cannot change", ErrImmutableFields)
		}
		if !bytes.Equal(previous.Input, candidate.Input) {
			return fmt.Errorf("%w: recorded step input cannot change", ErrImmutableFields)
		}
		if len(previous.ResolvedInput) > 0 && !bytes.Equal(previous.ResolvedInput, candidate.ResolvedInput) {
			return fmt.Errorf("%w: resolved step input cannot change", ErrImmutableFields)
		}
		if len(previous.ResolvedInput) == 0 && len(candidate.ResolvedInput) > 0 {
			if len(previous.InputBindings) == 0 || previous.Status != protocolstate.StepPending || candidate.Status != protocolstate.StepPending || candidate.Attempt != previous.Attempt || operationStatus != protocolstate.OperationRunning {
				return fmt.Errorf("%w: resolved input must be committed while the bound step is pending", ErrInvalidRecord)
			}
			steps := make([]inputbinding.Step, len(next))
			for j, item := range next {
				steps[j] = inputbinding.Step{StepID: item.StepID, Status: string(item.Status), Input: item.Input, Output: item.Output, Binding: item.InputBindings}
			}
			expected, err := inputbinding.Resolve(steps, i)
			if err != nil || !bytes.Equal(expected, candidate.ResolvedInput) {
				return fmt.Errorf("%w: resolved input does not match the immutable bindings and recorded outputs", ErrInvalidRecord)
			}
		}
		if len(previous.Output) > 0 && !bytes.Equal(previous.Output, candidate.Output) {
			return fmt.Errorf("%w: recorded step output cannot change", ErrImmutableFields)
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
	return nil
}

func sameInputBindings(left, right []inputbinding.Binding) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
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
