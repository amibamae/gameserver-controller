// Package store persists operation records before any controller side effect.
package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	managementAPIKeysBucketName    = "management_api_keys"
	hostHandoffsBucketName         = "host_handoffs"
	databaseSchema                 = "5"
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
	managementAPIKeysBucket    = []byte(managementAPIKeysBucketName)
	hostHandoffsBucket         = []byte(hostHandoffsBucketName)
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
		for _, name := range [][]byte{idempotencyBucket, locksBucket, operationLocksBucket, keyVersionsBucket, logicalServersBucket, capacityReservationsBucket, managementAPIKeysBucket, hostHandoffsBucket} {
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
		if bytes.Equal(version, []byte("4")) {
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
	prepared, scopes, err := prepareAcceptance(record, proof, lockScopes)
	if err != nil {
		return StoredOperation{}, false, err
	}
	var accepted StoredOperation
	replayed := false
	err = s.db.Update(func(tx *bolt.Tx) error {
		if err := requireVerifierForEveryKeyVersion(tx, proof); err != nil {
			return err
		}
		existing, err := findReplayedOperation(tx, proof, prepared.ActorID)
		if err != nil {
			return err
		}
		if existing != nil {
			accepted = *existing
			replayed = true
			return nil
		}
		if tx.Bucket(operationsBucket).Get([]byte(prepared.OperationID)) != nil {
			return ErrAlreadyExists
		}
		logicalServer, err := acceptanceLogicalServer(tx, prepared)
		if err != nil {
			return err
		}
		if err := requireFreeLocks(tx, scopes); err != nil {
			return err
		}
		if err := writeAcceptedOperation(tx, prepared, logicalServer, proof, scopes); err != nil {
			return err
		}
		accepted = StoredOperation{Record: prepared, Revision: 1}
		return nil
	})
	if err != nil {
		return StoredOperation{}, false, err
	}
	return accepted, replayed, nil
}

// prepareAcceptance checks the request outside the transaction: it needs an
// idempotency key, a request fingerprint, a valid proof and lock scopes, and
// the logical-server lock when it is a server operation. It returns the
// record as it will be stored and the sorted scopes.
func prepareAcceptance(record OperationRecord, proof IdempotencyProof, lockScopes []string) (OperationRecord, []string, error) {
	if record.IdempotencyKey == "" || !validFingerprint(record.RequestFingerprint) {
		return OperationRecord{}, nil, fmt.Errorf("%w: acceptance requires an idempotency key and request fingerprint", ErrInvalidRecord)
	}
	if err := validateIdempotencyProof(proof); err != nil {
		return OperationRecord{}, nil, err
	}
	scopes, err := validateLockScopes(lockScopes)
	if err != nil {
		return OperationRecord{}, nil, err
	}
	if requiredScope, required := requiredLogicalServerLock(record); required && !containsString(scopes, requiredScope) {
		return OperationRecord{}, nil, fmt.Errorf("%w: standard server operation requires lock %q", ErrInvalidRecord, requiredScope)
	}
	prepared, err := prepareNewRecord(record, time.Now().UTC())
	if err != nil {
		return OperationRecord{}, nil, err
	}
	return prepared, scopes, nil
}

// requireVerifierForEveryKeyVersion fails unless the proof carries a verifier
// for every key version the store has recorded, so a replay under a rotated
// key is still found.
func requireVerifierForEveryKeyVersion(tx *bolt.Tx, proof IdempotencyProof) error {
	cursor := tx.Bucket(keyVersionsBucket).Cursor()
	for version, _ := cursor.First(); version != nil; version, _ = cursor.Next() {
		if _, ok := findVerifier(proof, string(version)); !ok {
			return fmt.Errorf("%w: %s", ErrKeyVersionMissing, version)
		}
	}
	return nil
}

// findReplayedOperation returns the operation an earlier identical request
// created, or nil when no verifier candidate is recorded. A recorded verifier
// for a different request is a conflict; one whose operation is gone has
// expired; and entries that disagree with their key version, actor, or each
// other mean the store is corrupt.
func findReplayedOperation(tx *bolt.Tx, proof IdempotencyProof, actorID string) (*StoredOperation, error) {
	operations := tx.Bucket(operationsBucket)
	idempotency := tx.Bucket(idempotencyBucket)
	keyVersions := tx.Bucket(keyVersionsBucket)
	var existing *StoredOperation
	for _, candidate := range proof.Candidates {
		value := idempotency.Get(verifierIndex(candidate))
		if value == nil {
			continue
		}
		entry, err := decodeIdempotencyEntry(value)
		if err != nil {
			return nil, err
		}
		if entry.KeyVersion != candidate.KeyVersion || keyVersions.Get([]byte(entry.KeyVersion)) == nil {
			return nil, fmt.Errorf("%w: idempotency entry key version mismatch", ErrCorruptStore)
		}
		if !requestMatches(entry, candidate) {
			return nil, ErrIdempotencyConflict
		}
		value = operations.Get([]byte(entry.OperationID))
		if value == nil {
			return nil, ErrIdempotencyExpired
		}
		stored, err := decodeEnvelope(value)
		if err != nil {
			return nil, err
		}
		if stored.Record.ActorID != actorID {
			return nil, fmt.Errorf("%w: idempotency verifier actor mismatch", ErrCorruptStore)
		}
		if existing != nil && existing.Record.OperationID != stored.Record.OperationID {
			return nil, fmt.Errorf("%w: verifier candidates identify multiple operations", ErrCorruptStore)
		}
		existing = &stored
	}
	return existing, nil
}

// acceptanceLogicalServer applies the logical-server rules for a new
// operation. server.define returns the server record to create, after
// checking that the definition holds no steps, secrets, or instance and that
// its server ID is unused in every environment. Any other server operation
// requires an existing, valid logical server and returns nil.
func acceptanceLogicalServer(tx *bolt.Tx, prepared OperationRecord) (*LogicalServer, error) {
	if prepared.OperationType == "server.define" {
		return definedLogicalServer(tx.Bucket(logicalServersBucket), prepared)
	}
	if !strings.HasPrefix(prepared.OperationType, "server.") {
		return nil, nil
	}
	server, err := readLogicalServerTx(tx, prepared.Target, "logical server key does not match its record")
	if err != nil {
		return nil, err
	}
	return nil, requireValidStoredServer(server.Record)
}

// definedLogicalServer returns the logical server a server.define creates.
func definedLogicalServer(servers *bolt.Bucket, prepared OperationRecord) (*LogicalServer, error) {
	if !logicalDefinitionOnly(prepared) {
		return nil, fmt.Errorf("%w: server.define must contain only a logical definition", ErrInvalidRecord)
	}
	server, err := newLogicalServerFromDefinition(prepared)
	if err != nil {
		return nil, err
	}
	if err := requireUnusedServerID(servers, server); err != nil {
		return nil, err
	}
	server.CreatedAt = prepared.CreatedAt
	server.UpdatedAt = prepared.CreatedAt
	server.LastOperationID = prepared.OperationID
	return &server, nil
}

func logicalDefinitionOnly(prepared OperationRecord) bool {
	return len(prepared.Steps) == 0 && len(prepared.RequestSnapshot.SecretRefs) == 0 && len(prepared.Target.InstanceRef) == 0
}

// requireUnusedServerID fails when the server's key is taken or when any
// environment already uses its server ID, because management routes after
// definition name only the server.
func requireUnusedServerID(servers *bolt.Bucket, server LogicalServer) error {
	if servers.Get(logicalServerKey(server.EnvironmentID, server.ServerID)) != nil {
		return ErrLogicalServerAlreadyExists
	}
	return servers.ForEach(func(_, value []byte) error {
		// A record that does not decode is skipped rather than reported
		// here: this check only looks for a taken ID, and the reads that
		// depend on that record report its corruption.
		var existing StoredLogicalServer
		if json.Unmarshal(value, &existing) == nil && existing.Record.ServerID == server.ServerID {
			return ErrLogicalServerAlreadyExists
		}
		return nil
	})
}

// requireFreeLocks fails with ErrResourceConflict when another operation
// holds any of the scopes.
func requireFreeLocks(tx *bolt.Tx, scopes []string) error {
	locks := tx.Bucket(locksBucket)
	for _, scope := range scopes {
		value := locks.Get([]byte(scope))
		if value == nil {
			continue
		}
		lock, err := decodeResourceLock(value)
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: scope %q owned by operation %q", ErrResourceConflict, lock.Scope, lock.OperationID)
	}
	return nil
}

// writeAcceptedOperation stores the operation at revision 1, the logical
// server a definition creates, the active-key idempotency entry, the key
// version, and the forward and reverse lock indexes, in one transaction.
func writeAcceptedOperation(tx *bolt.Tx, prepared OperationRecord, logicalServer *LogicalServer, proof IdempotencyProof, scopes []string) error {
	if err := putOperation(tx, StoredOperation{Record: prepared, Revision: 1}); err != nil {
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
	if err := tx.Bucket(idempotencyBucket).Put(verifierIndex(activeVerifier), entryJSON); err != nil {
		return err
	}
	if err := tx.Bucket(keyVersionsBucket).Put([]byte(proof.ActiveKeyVersion), []byte{1}); err != nil {
		return err
	}
	if len(scopes) == 0 {
		return nil
	}
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
	return tx.Bucket(operationLocksBucket).Put([]byte(prepared.OperationID), scopesJSON)
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

func validateStepChanges(current, next []OperationStep, operationStatus protocolstate.OperationStatus) error {
	if len(next) != len(current) {
		return fmt.Errorf("%w: accepted operation steps cannot be added or removed", ErrImmutableFields)
	}
	for i, previous := range current {
		if err := validateStepChange(next, i, previous, operationStatus); err != nil {
			return err
		}
	}
	return nil
}

// validateStepChange checks step i of next against its previous version:
// recorded fields first, then a newly committed resolved input, then
// recorded outcomes, then the status transition and attempt count.
func validateStepChange(next []OperationStep, i int, previous OperationStep, operationStatus protocolstate.OperationStatus) error {
	candidate := next[i]
	if err := validateRecordedStepFields(previous, candidate); err != nil {
		return err
	}
	if err := validateResolvedInputCommit(next, i, previous, operationStatus); err != nil {
		return err
	}
	if changedOnceRecorded(previous.Output, candidate.Output) {
		return fmt.Errorf("%w: recorded step output cannot change", ErrImmutableFields)
	}
	if changedOnceRecorded(previous.ExternalOperationRef, candidate.ExternalOperationRef) {
		return fmt.Errorf("%w: recorded external operation reference cannot change", ErrImmutableFields)
	}
	return validateStepProgress(previous, candidate, operationStatus)
}

// validateRecordedStepFields rejects a change to a step's identity, recorded
// capability, bindings, input, or recorded resolved input.
func validateRecordedStepFields(previous, candidate OperationStep) error {
	switch {
	case previous.StepID != candidate.StepID || previous.StepType != candidate.StepType:
		return fmt.Errorf("%w: step order and identity cannot change", ErrImmutableFields)
	case !sameRecordedCapability(previous, candidate):
		return fmt.Errorf("%w: recorded adapter capability identity cannot change", ErrImmutableFields)
	case !slices.Equal(previous.InputBindings, candidate.InputBindings):
		return fmt.Errorf("%w: recorded step input bindings cannot change", ErrImmutableFields)
	case !bytes.Equal(previous.Input, candidate.Input):
		return fmt.Errorf("%w: recorded step input cannot change", ErrImmutableFields)
	case changedOnceRecorded(previous.ResolvedInput, candidate.ResolvedInput):
		return fmt.Errorf("%w: resolved step input cannot change", ErrImmutableFields)
	}
	return nil
}

// sameRecordedCapability allows each capability field to be recorded once
// and never changed afterwards.
func sameRecordedCapability(previous, candidate OperationStep) bool {
	return keptOnceRecorded(previous.AdapterID, candidate.AdapterID) &&
		keptOnceRecorded(previous.CapabilityName, candidate.CapabilityName) &&
		keptOnceRecorded(previous.CapabilityVersion, candidate.CapabilityVersion)
}

func keptOnceRecorded(previous, candidate string) bool {
	return previous == "" || previous == candidate
}

func changedOnceRecorded(previous, candidate json.RawMessage) bool {
	return len(previous) > 0 && !bytes.Equal(previous, candidate)
}

// validateResolvedInputCommit checks a resolved input recorded for the first
// time: the bound step must still be pending in a running operation, and the
// input must be what the bindings resolve to from the recorded outputs.
func validateResolvedInputCommit(next []OperationStep, i int, previous OperationStep, operationStatus protocolstate.OperationStatus) error {
	candidate := next[i]
	if len(previous.ResolvedInput) > 0 || len(candidate.ResolvedInput) == 0 {
		return nil
	}
	if !resolvedInputCommitAllowed(previous, candidate, operationStatus) {
		return fmt.Errorf("%w: resolved input must be committed while the bound step is pending", ErrInvalidRecord)
	}
	expected, err := inputbinding.Resolve(inputBindingSteps(next), i)
	if err != nil || !bytes.Equal(expected, candidate.ResolvedInput) {
		return fmt.Errorf("%w: resolved input does not match the immutable bindings and recorded outputs", ErrInvalidRecord)
	}
	return nil
}

func resolvedInputCommitAllowed(previous, candidate OperationStep, operationStatus protocolstate.OperationStatus) bool {
	return boundStepStillPending(previous, candidate) && candidate.Attempt == previous.Attempt && operationStatus == protocolstate.OperationRunning
}

func boundStepStillPending(previous, candidate OperationStep) bool {
	return len(previous.InputBindings) > 0 && previous.Status == protocolstate.StepPending && candidate.Status == protocolstate.StepPending
}

// validateStepProgress checks the step status transition, and that the
// attempt count rises by one exactly when the step enters running, which it
// may do only while the operation is running.
func validateStepProgress(previous, candidate OperationStep, operationStatus protocolstate.OperationStatus) error {
	if previous.Status != candidate.Status {
		if err := protocolstate.ValidateStepTransition(previous.Status, candidate.Status); err != nil {
			return err
		}
	}
	if candidate.Status != protocolstate.StepRunning || previous.Status == protocolstate.StepRunning {
		if candidate.Attempt != previous.Attempt {
			return fmt.Errorf("%w: step attempt can change only when entering running", ErrImmutableFields)
		}
		return nil
	}
	if operationStatus != protocolstate.OperationRunning {
		return fmt.Errorf("%w: a step may start only while the operation is running", ErrInvalidRecord)
	}
	if candidate.Attempt != previous.Attempt+1 {
		return fmt.Errorf("%w: entering running must increment step attempt by one", ErrInvalidRecord)
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
