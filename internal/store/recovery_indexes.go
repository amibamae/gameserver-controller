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
// indexes do not describe one consistent recovery state.
func verifyRecoveryIndexes(tx *bolt.Tx) error {
	operations := tx.Bucket(operationsBucket)
	idempotency := tx.Bucket(idempotencyBucket)
	locks := tx.Bucket(locksBucket)
	operationLocks := tx.Bucket(operationLocksBucket)
	keyVersions := tx.Bucket(keyVersionsBucket)

	knownVersions := make(map[string]struct{})
	versionCursor := keyVersions.Cursor()
	for version, value := versionCursor.First(); version != nil; version, value = versionCursor.Next() {
		if !validKeyVersion(string(version)) || len(value) == 0 {
			return fmt.Errorf("%w: invalid idempotency key version metadata", ErrCorruptStore)
		}
		knownVersions[string(version)] = struct{}{}
	}

	idempotencyByOperation := make(map[string]idempotencyEntry)
	cursor := idempotency.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		entry, err := decodeIdempotencyEntry(value)
		if err != nil {
			return err
		}
		parts := strings.SplitN(string(key), ":", 2)
		if len(parts) != 2 || parts[0] != entry.KeyVersion || len(parts[1]) != 64 {
			return fmt.Errorf("%w: invalid idempotency verifier index", ErrCorruptStore)
		}
		if _, err := hex.DecodeString(parts[1]); err != nil {
			return fmt.Errorf("%w: invalid idempotency verifier index", ErrCorruptStore)
		}
		if _, ok := knownVersions[entry.KeyVersion]; !ok {
			return fmt.Errorf("%w: idempotency entry references an unknown key version", ErrCorruptStore)
		}
		if _, duplicate := idempotencyByOperation[entry.OperationID]; duplicate {
			return fmt.Errorf("%w: operation has multiple idempotency entries", ErrCorruptStore)
		}
		idempotencyByOperation[entry.OperationID] = entry
	}

	reverseLocks := make(map[string][]string)
	cursor = operationLocks.Cursor()
	for operationID, value := cursor.First(); operationID != nil; operationID, value = cursor.Next() {
		id := string(operationID)
		var scopes []string
		if err := json.Unmarshal(value, &scopes); err != nil {
			return fmt.Errorf("%w: decode operation lock scopes: %v", ErrCorruptStore, err)
		}
		if len(scopes) == 0 {
			return fmt.Errorf("%w: empty operation lock index", ErrCorruptStore)
		}
		validated, err := validateLockScopes(scopes)
		if err != nil || len(validated) != len(scopes) {
			return fmt.Errorf("%w: invalid operation lock scopes", ErrCorruptStore)
		}
		for i := range scopes {
			if scopes[i] != validated[i] {
				return fmt.Errorf("%w: operation lock scopes are not in stable order", ErrCorruptStore)
			}
		}
		operationValue := operations.Get(operationID)
		if operationValue == nil {
			return fmt.Errorf("%w: resource locks refer to a missing operation", ErrCorruptStore)
		}
		stored, err := decodeEnvelope(operationValue)
		if err != nil {
			return err
		}
		if protocolstate.IsTerminalOperation(stored.Record.Status) {
			return fmt.Errorf("%w: terminal operation still owns resource locks", ErrCorruptStore)
		}
		reverseLocks[id] = scopes
		for _, scope := range scopes {
			lockValue := locks.Get([]byte(scope))
			if lockValue == nil {
				return fmt.Errorf("%w: operation lock %q is missing", ErrCorruptStore, scope)
			}
			lock, err := decodeResourceLock(lockValue)
			if err != nil {
				return err
			}
			if lock.Scope != scope || lock.OperationID != id {
				return fmt.Errorf("%w: operation does not own lock %q", ErrCorruptStore, scope)
			}
		}
	}

	cursor = locks.Cursor()
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

	cursor = operations.Cursor()
	for operationID, value := cursor.First(); operationID != nil; operationID, value = cursor.Next() {
		stored, err := decodeEnvelope(value)
		if err != nil {
			return err
		}
		id := string(operationID)
		if strings.HasPrefix(stored.Record.OperationType, "server.") {
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
	}
	return nil
}
