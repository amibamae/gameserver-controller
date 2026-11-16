package store

import (
	"fmt"
	"reflect"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestBackupContextCaptureOrdersRevisionLockAndServerChecks(t *testing.T) {
	s, operation := backupContextFixture(t)
	id := operation.Record.OperationID
	scope := LogicalServerLockScope("staging", "server-1")
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(logicalServersBucket).Delete(logicalServerKey("staging", "server-1"))
	}); err != nil {
		t.Fatal(err)
	}
	assertBackupCaptureError(t, s, id, operation.Revision, ErrLogicalServerNotFound)

	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(locksBucket).Delete([]byte(scope))
	}); err != nil {
		t.Fatal(err)
	}
	assertBackupCaptureError(t, s, id, operation.Revision, fmt.Errorf("%w: required server lock %q is missing", ErrCorruptStore, scope))
	assertBackupCaptureError(t, s, id, operation.Revision+1, ErrRevisionConflict)
	assertBackupCaptureError(t, s, "unknown-operation", operation.Revision+1, ErrNotFound)
	assertBackupOperationUnchanged(t, s, operation)
}

func TestBackupContextCaptureRechecksLockOnReplay(t *testing.T) {
	s, operation := backupContextFixture(t)
	id := operation.Record.OperationID
	captured, err := s.CaptureBackupInitialState(id, operation.Revision)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := s.CaptureBackupInitialState(id, captured.Revision)
	if err != nil || !reflect.DeepEqual(replayed, captured) {
		t.Fatalf("replay = %#v, %v, want unchanged capture %#v", replayed, err, captured)
	}

	scope := LogicalServerLockScope("staging", "server-1")
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(locksBucket).Delete([]byte(scope))
	}); err != nil {
		t.Fatal(err)
	}
	assertBackupCaptureError(t, s, id, captured.Revision, fmt.Errorf("%w: required server lock %q is missing", ErrCorruptStore, scope))
	assertBackupOperationUnchanged(t, s, captured)
}

func assertBackupCaptureError(t *testing.T, s *BoltStore, operationID string, revision uint64, want error) {
	t.Helper()
	captured, err := s.CaptureBackupInitialState(operationID, revision)
	if err == nil || err.Error() != want.Error() {
		t.Fatalf("capture error = %v, want %v", err, want)
	}
	if !reflect.DeepEqual(captured, StoredOperation{}) {
		t.Fatalf("rejected capture returned %+v", captured)
	}
}

func assertBackupOperationUnchanged(t *testing.T, s *BoltStore, want StoredOperation) {
	t.Helper()
	loaded, err := s.GetOperation(want.Record.OperationID)
	if err != nil || !reflect.DeepEqual(loaded, want) {
		t.Fatalf("stored operation = %#v, %v, want %#v", loaded, err, want)
	}
}
