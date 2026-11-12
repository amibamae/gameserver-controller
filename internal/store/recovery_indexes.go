package store

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// verifyRecoveryIndexes fails closed if the operation, idempotency, and lock
// indexes do not describe one consistent recovery state. The passes run in a
// fixed order; each later pass relies on the indexes the earlier ones built.
func verifyRecoveryIndexes(tx *bolt.Tx) error {
	operations := tx.Bucket(operationsBucket)
	locks := tx.Bucket(locksBucket)

	knownVersions, err := verifyKeyVersionIndex(tx.Bucket(keyVersionsBucket))
	if err != nil {
		return err
	}
	idempotencyByOperation, err := verifyIdempotencyIndex(tx.Bucket(idempotencyBucket), knownVersions)
	if err != nil {
		return err
	}
	reverseLocks, err := verifyOperationLockIndex(tx.Bucket(operationLocksBucket), operations, locks)
	if err != nil {
		return err
	}
	if err := verifyResourceLockOwners(locks, operations, reverseLocks); err != nil {
		return err
	}
	return verifyServerOperationIndexes(operations, idempotencyByOperation, reverseLocks)
}

// verifyKeyVersionIndex returns the recorded idempotency key versions.
func verifyKeyVersionIndex(keyVersions *bolt.Bucket) (map[string]struct{}, error) {
	knownVersions := make(map[string]struct{})
	versionCursor := keyVersions.Cursor()
	for version, value := versionCursor.First(); version != nil; version, value = versionCursor.Next() {
		if !validKeyVersion(string(version)) || len(value) == 0 {
			return nil, fmt.Errorf("%w: invalid idempotency key version metadata", ErrCorruptStore)
		}
		knownVersions[string(version)] = struct{}{}
	}
	return knownVersions, nil
}

// verifyIdempotencyIndex checks that every idempotency entry is keyed by its
// key version and verifier digest, uses a recorded key version, and is the
// only entry for its operation. It returns the entries by operation ID.
func verifyIdempotencyIndex(idempotency *bolt.Bucket, knownVersions map[string]struct{}) (map[string]idempotencyEntry, error) {
	idempotencyByOperation := make(map[string]idempotencyEntry)
	cursor := idempotency.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		entry, err := decodeIdempotencyEntry(value)
		if err != nil {
			return nil, err
		}
		parts := strings.SplitN(string(key), ":", 2)
		if len(parts) != 2 || parts[0] != entry.KeyVersion || len(parts[1]) != 64 {
			return nil, fmt.Errorf("%w: invalid idempotency verifier index", ErrCorruptStore)
		}
		if _, err := hex.DecodeString(parts[1]); err != nil {
			return nil, fmt.Errorf("%w: invalid idempotency verifier index", ErrCorruptStore)
		}
		if _, ok := knownVersions[entry.KeyVersion]; !ok {
			return nil, fmt.Errorf("%w: idempotency entry references an unknown key version", ErrCorruptStore)
		}
		if _, duplicate := idempotencyByOperation[entry.OperationID]; duplicate {
			return nil, fmt.Errorf("%w: operation has multiple idempotency entries", ErrCorruptStore)
		}
		idempotencyByOperation[entry.OperationID] = entry
	}
	return idempotencyByOperation, nil
}

// verifyOperationLockIndex checks the reverse lock index: each entry lists the
// stable, valid lock scopes of an existing active operation, and each listed
// lock exists and is owned by that operation. It returns the scopes by
// operation ID.
func verifyOperationLockIndex(operationLocks, operations, locks *bolt.Bucket) (map[string][]string, error) {
	reverseLocks := make(map[string][]string)
	cursor := operationLocks.Cursor()
	for operationID, value := cursor.First(); operationID != nil; operationID, value = cursor.Next() {
		id := string(operationID)
		var scopes []string
		if err := json.Unmarshal(value, &scopes); err != nil {
			return nil, fmt.Errorf("%w: decode operation lock scopes: %v", ErrCorruptStore, err)
		}
		if len(scopes) == 0 {
			return nil, fmt.Errorf("%w: empty operation lock index", ErrCorruptStore)
		}
		validated, err := validateLockScopes(scopes)
		if err != nil || len(validated) != len(scopes) {
			return nil, fmt.Errorf("%w: invalid operation lock scopes", ErrCorruptStore)
		}
		for i := range scopes {
			if scopes[i] != validated[i] {
				return nil, fmt.Errorf("%w: operation lock scopes are not in stable order", ErrCorruptStore)
			}
		}
		operationValue := operations.Get(operationID)
		if operationValue == nil {
			return nil, fmt.Errorf("%w: resource locks refer to a missing operation", ErrCorruptStore)
		}
		stored, err := decodeEnvelope(operationValue)
		if err != nil {
			return nil, err
		}
		if protocolstate.IsTerminalOperation(stored.Record.Status) {
			return nil, fmt.Errorf("%w: terminal operation still owns resource locks", ErrCorruptStore)
		}
		reverseLocks[id] = scopes
		for _, scope := range scopes {
			lockValue := locks.Get([]byte(scope))
			if lockValue == nil {
				return nil, fmt.Errorf("%w: operation lock %q is missing", ErrCorruptStore, scope)
			}
			lock, err := decodeResourceLock(lockValue)
			if err != nil {
				return nil, err
			}
			if lock.Scope != scope || lock.OperationID != id {
				return nil, fmt.Errorf("%w: operation does not own lock %q", ErrCorruptStore, scope)
			}
		}
	}
	return reverseLocks, nil
}

// verifyResourceLockOwners checks the forward lock index: each lock is keyed
// by its scope and owned by an existing active operation whose reverse index
// lists it.
func verifyResourceLockOwners(locks, operations *bolt.Bucket, reverseLocks map[string][]string) error {
	cursor := locks.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		lock, err := decodeResourceLock(value)
		if err != nil {
			return err
		}
		if lock.Scope != string(key) {
			return fmt.Errorf("%w: resource lock scope mismatch", ErrCorruptStore)
		}
		operationValue := operations.Get([]byte(lock.OperationID))
		if operationValue == nil {
			return fmt.Errorf("%w: resource lock refers to a missing operation", ErrCorruptStore)
		}
		stored, err := decodeEnvelope(operationValue)
		if err != nil {
			return err
		}
		if protocolstate.IsTerminalOperation(stored.Record.Status) || !containsString(reverseLocks[lock.OperationID], lock.Scope) {
			return fmt.Errorf("%w: resource lock has no active owner index", ErrCorruptStore)
		}
	}
	return nil
}

// verifyServerOperationIndexes requires every server operation to have an
// idempotency entry, and every active one to hold its logical-server lock.
// Stores written before those indexes existed need migration.
func verifyServerOperationIndexes(operations *bolt.Bucket, idempotencyByOperation map[string]idempotencyEntry, reverseLocks map[string][]string) error {
	cursor := operations.Cursor()
	for operationID, value := cursor.First(); operationID != nil; operationID, value = cursor.Next() {
		stored, err := decodeEnvelope(value)
		if err != nil {
			return err
		}
		id := string(operationID)
		if !strings.HasPrefix(stored.Record.OperationType, "server.") {
			continue
		}
		if _, ok := idempotencyByOperation[id]; !ok {
			return ErrStoreNeedsMigration
		}
		if scopes, locked := reverseLocks[id]; protocolstate.IsTerminalOperation(stored.Record.Status) {
			if locked {
				return fmt.Errorf("%w: terminal operation still has a reverse lock index", ErrCorruptStore)
			}
		} else {
			required, _ := requiredLogicalServerLock(stored.Record)
			if !containsString(scopes, required) {
				return fmt.Errorf("%w: active server operation is missing its logical-server lock", ErrStoreNeedsMigration)
			}
		}
	}
	return nil
}
