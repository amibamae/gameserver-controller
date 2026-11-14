package store

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

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
