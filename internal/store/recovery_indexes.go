package store

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// -----------------------------------------------------------------------------
// Index Building Functions
// These functions extract index metadata from buckets and validate it.
// They fail closed to ensure recoverable consistency.
// -----------------------------------------------------------------------------

// buildKeyVersionIndex reads the key versions bucket and returns a set of valid
// idempotency key versions. It fails on invalid metadata or empty entries.
func buildKeyVersionIndex(keyVersions *bolt.Bucket) (map[string]struct{}, error) {
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

// buildIdempotencyIndex iterates over the idempotency bucket and builds a map
// from operation ID to idempotency entry. It validates:
//   - entries use recorded key versions
//   - keys are properly formatted (version:64-char-hex)
//   - no operation has multiple entries
func buildIdempotencyIndex(idempotency *bolt.Bucket, knownVersions map[string]struct{}) (map[string]idempotencyEntry, error) {
	idempotencyByOperation := make(map[string]idempotencyEntry)
	cursor := idempotency.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		entry, err := decodeIdempotencyEntry(value)
		if err != nil {
			return nil, err
		}
		keyParts := parseIdempotencyKey(string(key))
		if keyParts == nil || len(keyParts[1]) != 64 {
			return nil, fmt.Errorf("%w: invalid idempotency verifier index", ErrCorruptStore)
		}
		if _, err := hex.DecodeString(keyParts[1]); err != nil {
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

// parseIdempotencyKey splits an idempotency key into its version and hex digest parts.
// Returns nil if the format is invalid (not "version:hex").
func parseIdempotencyKey(key string) []string {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return nil
	}
	return parts
}

// buildOperationLockIndex reads the operation locks bucket and builds a map of
// reverse lock indices. For each operation, it validates:
//   - scopes are valid JSON array and not empty
//   - scopes are in stable order and match their decoded values
//   - the referenced operation exists and is non-terminal
//   - each listed scope has a corresponding forward lock owned by this operation
func buildOperationLockIndex(operationLocks, operations, locks *bolt.Bucket) (map[string][]string, error) {
	reverseLocks := make(map[string][]string)
	cursor := operationLocks.Cursor()
	for operationID, value := cursor.First(); operationID != nil; operationID, value = cursor.Next() {
		id := string(operationID)

		// Decode lock scopes
		var scopes []string
		if err := json.Unmarshal(value, &scopes); err != nil {
			return nil, fmt.Errorf("%w: decode operation lock scopes: %v", ErrCorruptStore, err)
		}
		if len(scopes) == 0 {
			return nil, fmt.Errorf("%w: empty operation lock index", ErrCorruptStore)
		}

		// Validate scope stability: check scopes are in canonical sorted order
		validated, validationErr := validateLockScopes(scopes)
		if len(validated) != len(scopes) || validationErr != nil {
			return nil, fmt.Errorf("%w: invalid operation lock scopes", ErrCorruptStore)
		}
		// Scopes must match canonical sorted order (same content, same order)
		for i := range scopes {
			if scopes[i] != validated[i] {
				return nil, fmt.Errorf("%w: operation lock scopes are not in stable order", ErrCorruptStore)
			}
		}

		// Check that operation still exists and is active
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

		reverseLocks[id] = validated

		// Verify each forward lock exists and is owned by this operation
		for i, scope := range validated {
			lockValue := locks.Get([]byte(scope))
			if lockValue == nil {
				return nil, fmt.Errorf("%w: operation lock %q is missing", ErrCorruptStore, scope)
			}
			lock, err := decodeResourceLock(lockValue)
			if err != nil {
				return nil, err
			}
			if lock.Scope != validated[i] || lock.OperationID != id {
				return nil, fmt.Errorf("%w: operation does not own lock %q", ErrCorruptStore, scope)
			}
		}
	}
	return reverseLocks, nil
}

// buildForwardLockIndex iterates the locks bucket and builds a forward index.
// For each lock, it validates:
//   - the key matches the lock's scope
//   - the referenced operation exists
//   - the operation is not terminal and is listed in its reverse index
func buildForwardLockIndex(locks, operations *bolt.Bucket, reverseLocks map[string][]string) error {
	cursor := locks.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		lock, err := decodeResourceLock(value)
		if err != nil {
			return err
		}

		// Key must match lock scope
		if string(key) != lock.Scope {
			return fmt.Errorf("%w: resource lock scope mismatch", ErrCorruptStore)
		}

		// Operation must exist
		operationValue := operations.Get([]byte(lock.OperationID))
		if operationValue == nil {
			return fmt.Errorf("%w: resource lock refers to a missing operation", ErrCorruptStore)
		}

		stored, err := decodeEnvelope(operationValue)
		if err != nil {
			return err
		}

		// Operation must be active and listed in reverse index with this scope
		if protocolstate.IsTerminalOperation(stored.Record.Status) || !containsString(reverseLocks[lock.OperationID], lock.Scope) {
			return fmt.Errorf("%w: resource lock has no active owner index", ErrCorruptStore)
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Server Operation Validation Functions
// These functions validate server-specific invariants across all indexes.
// -----------------------------------------------------------------------------

// verifyKeyVersionIndex is a wrapper for buildKeyVersionIndex that returns an error.
func verifyKeyVersionIndex(keyVersions *bolt.Bucket) (map[string]struct{}, error) {
	return buildKeyVersionIndex(keyVersions)
}

// verifyIdempotencyIndex is a wrapper for buildIdempotencyIndex that returns an error.
func verifyIdempotencyIndex(idempotency *bolt.Bucket, knownVersions map[string]struct{}) (map[string]idempotencyEntry, error) {
	return buildIdempotencyIndex(idempotency, knownVersions)
}

// verifyOperationLockIndex is a wrapper for buildOperationLockIndex that returns an error.
func verifyOperationLockIndex(operationLocks, operations, locks *bolt.Bucket) (map[string][]string, error) {
	return buildOperationLockIndex(operationLocks, operations, locks)
}

// verifyServerOperationIndexes validates server-specific invariants:
//   - every server operation has an idempotency entry
//   - every active server operation holds its required logical-server lock
//
// Returns ErrStoreNeedsMigration if indexes are missing (old stores).
func verifyServerOperationIndexes(operations *bolt.Bucket, idempotencyByOperation map[string]idempotencyEntry, reverseLocks map[string][]string) error {
	cursor := operations.Cursor()
	for operationID, value := cursor.First(); operationID != nil; operationID, value = cursor.Next() {
		stored, err := decodeEnvelope(value)
		if err != nil {
			return err
		}

		id := string(operationID)
		// Skip non-server operations (e.g., provisioning, cleanup)
		if !strings.HasPrefix(stored.Record.OperationType, "server.") {
			continue
		}

		// Check for idempotency entry
		if _, ok := idempotencyByOperation[id]; !ok {
			return ErrStoreNeedsMigration
		}

		scopes, locked := reverseLocks[id]

		// Terminal operations should not have a reverse lock index
		if protocolstate.IsTerminalOperation(stored.Record.Status) {
			if locked {
				return fmt.Errorf("%w: terminal operation still has a reverse lock index", ErrCorruptStore)
			}
			continue
		}

		// Active server operations must hold their logical-server lock
		required, _ := requiredLogicalServerLock(stored.Record)
		if !containsString(scopes, required) {
			return fmt.Errorf("%w: active server operation is missing its logical-server lock", ErrStoreNeedsMigration)
		}
	}
	return nil
}

// -----------------------------------------------------------------------------
// Main Verification Function
// Orchestrates all index verification steps in order.
// -----------------------------------------------------------------------------

// verifyRecoveryIndexes fails closed if the operation, idempotency, and lock
// indexes do not describe one consistent recovery state. The passes run in a
// fixed order; each later pass relies on the indexes the earlier ones built.
func verifyRecoveryIndexes(tx *bolt.Tx) error {
	operations := tx.Bucket(operationsBucket)
	locks := tx.Bucket(locksBucket)

	// Step 1: Build key versions index (needed for idempotency validation)
	knownVersions, err := buildKeyVersionIndex(tx.Bucket(keyVersionsBucket))
	if err != nil {
		return err
	}

	// Step 2: Build idempotency index (depends on knownVersions from step 1)
	idempotencyByOperation, err := buildIdempotencyIndex(tx.Bucket(idempotencyBucket), knownVersions)
	if err != nil {
		return err
	}

	// Step 3: Build operation lock index (needs operations and locks buckets)
	reverseLocks, err := buildOperationLockIndex(tx.Bucket(operationLocksBucket), operations, locks)
	if err != nil {
		return err
	}

	// Step 4: Validate forward lock index
	if err := buildForwardLockIndex(locks, operations, reverseLocks); err != nil {
		return err
	}

	// Step 5: Validate server operation invariants
	return verifyServerOperationIndexes(operations, idempotencyByOperation, reverseLocks)
}
