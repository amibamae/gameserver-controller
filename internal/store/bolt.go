// Package store persists operation records before any controller side effect.
package store

import (
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
	db, err := openPrivateBoltFile(path)
	if err != nil {
		return nil, err
	}
	if err := db.Update(initializeSchema); err != nil {
		return nil, closeAfterFailedOpen(db, fmt.Errorf("initialize operation store: %w", err))
	}
	if err := db.View(verifyRecoveryIndexes); err != nil {
		return nil, closeAfterFailedOpen(db, fmt.Errorf("verify operation recovery indexes: %w", err))
	}
	return &BoltStore{db: db}, nil
}

// openPrivateBoltFile creates the parent directory, opens the database, and
// restricts the file to the current user.
func openPrivateBoltFile(path string) (*bolt.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create operation store directory: %w", err)
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open bbolt operation store: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, closeAfterFailedOpen(db, fmt.Errorf("restrict operation store permissions: %w", err))
	}
	return db, nil
}

// closeAfterFailedOpen closes db after a failed open step and returns that
// step's error. The close error is deliberately dropped: the caller must
// see the open step's error, whose message is part of the store contract,
// and there is no store left to report a close failure against.
func closeAfterFailedOpen(db *bolt.DB, err error) error {
	_ = db.Close()
	return err
}

// initializeSchema creates every bucket and brings the schema version to
// databaseSchema, refusing stores whose legacy records need a migration.
func initializeSchema(tx *bolt.Tx) error {
	meta, err := tx.CreateBucketIfNotExists(metadataBucket)
	if err != nil {
		return err
	}
	for _, name := range [][]byte{operationsBucket, idempotencyBucket, locksBucket, operationLocksBucket, keyVersionsBucket, logicalServersBucket, capacityReservationsBucket, managementAPIKeysBucket, hostHandoffsBucket} {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return err
		}
	}
	return migrateSchemaVersion(tx, meta)
}

// migrateSchemaVersion stamps databaseSchema on an unversioned or older
// store. An unversioned store must hold no records in the buckets that
// predate versioning, and a version 1 store no operations.
func migrateSchemaVersion(tx *bolt.Tx, meta *bolt.Bucket) error {
	version := meta.Get(schemaKey)
	if version == nil {
		return stampSchemaWhenEmpty(tx, meta, [][]byte{operationsBucket, idempotencyBucket, locksBucket, operationLocksBucket, keyVersionsBucket, logicalServersBucket, capacityReservationsBucket})
	}
	switch string(version) {
	case "1":
		return stampSchemaWhenEmpty(tx, meta, [][]byte{operationsBucket})
	case "2", "3", "4":
		return meta.Put(schemaKey, []byte(databaseSchema))
	case databaseSchema:
		return nil
	}
	return fmt.Errorf("unsupported operation store schema version %q", version)
}

func stampSchemaWhenEmpty(tx *bolt.Tx, meta *bolt.Bucket, buckets [][]byte) error {
	for _, name := range buckets {
		if key, _ := tx.Bucket(name).Cursor().First(); key != nil {
			return ErrStoreNeedsMigration
		}
	}
	return meta.Put(schemaKey, []byte(databaseSchema))
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
