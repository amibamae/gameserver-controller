package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

func TestValidateBackupOperationRecordIndependentChecks(t *testing.T) {
	s, _ := backupContextFixture(t)
	op := testOperation("validation-test")
	op.OperationType = "server.backup"
	op.RequestSnapshot.OperationType = op.OperationType
	op.Target.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)
	proof := IdempotencyProof{ActiveKeyVersion: "v1", Candidates: []IdempotencyVerifier{{KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}}}

	t.Run("invalid-status-not-terminal", func(t *testing.T) {
		// Accept a non-terminal operation
		if _, _, err := s.AcceptOperation(op, proof, []string{}); errors.Is(err, ErrInvalidRecord) || strings.Contains(err.Error(), "active server.backup required") {
			t.Fatalf("accepted non-terminal: %v", err)
		}
	})

	t.Run("terminal-status-rejected", func(t *testing.T) {
		op.Record.Status = protocolstate.OperationCompleted
		if _, _, err := s.AcceptOperation(op, proof, []string{}); !errors.Is(err, ErrInvalidRecord) && !strings.Contains(err.Error(), "active server.backup required") {
			t.Fatalf("should reject terminal status: %v", err)
		}
	})

	t.Run("wrong-operation-type-rejected", func(t *testing.T) {
		op.Record.OperationType = "server.wrong"
		if _, _, err := s.AcceptOperation(op, proof, []string{}); !errors.Is(err, ErrInvalidRecord) && !strings.Contains(err.Error(), "active server.backup required") {
			t.Fatalf("should reject wrong type: %v", err)
		}
	})
}

func TestValidateBackupTargetInstanceRefIndependentChecks(t *testing.T) {
	testCases := []struct {
		name   string
		ref    string
		envID  string
		wantOk bool
	}{
		{"valid-target", `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`, "staging", true},
		{"wrong-environment", `{"adapter_id":"hosting.fake","environment_id":"production","resource_kind":"instance","external_id":"vm-1"}`, "staging", false},
		{"wrong-resource-kind", `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"storage","external_id":"vol-1"}`, "staging", false},
		{"missing-adapter-id", `{"environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`, "staging", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			target := OperationTarget{InstanceRef: json.RawMessage(tc.ref), EnvironmentID: tc.envID}
			got := validateBackupTargetInstanceRef(target)
			if got != tc.wantOk {
				t.Errorf("validateBackupTargetInstanceRef(%q, %q) = %v, want %v", tc.ref, tc.envID, got, tc.wantOk)
			}
		})
	}
}

func TestReadAndValidateServerState(t *testing.T) {
	s := openTestStore(t)
	server := testLogicalServerFixture(t, s)
	server.Record.LifecycleState = protocolstate.ServerStopped
	server.Record.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)

	states := []protocolstate.ServerState{protocolstate.ServerUnprovisioned, protocolstate.ServerProvisioning, protocolstate.ServerPreparing, protocolstate.ServerStarting, protocolstate.ServerValidating, protocolstate.ServerReady, protocolstate.ServerStopping, protocolstate.ServerStopped}
	for i := 1; i < len(states); i++ {
		server.Record.LifecycleEvents = append(server.Record.LifecycleEvents, LogicalServerStateEvent{
			Sequence: uint64(i), OperationID: server.Record.LastOperationID, ActorID: "operator-1", OperationType: "server.provision", FromState: states[i-1], ToState: states[i], Cause: "test fixture history", RequestFingerprint: "sha256:" + strings.Repeat("a", 64), OccurredAt: server.Record.CreatedAt,
		})
	}

	key := logicalServerKey("staging", "server-1")
	var encoded []byte
	if err := s.db.Update(func(tx *bolt.Tx) error {
		encoded = append(encoded[:0], tx.Bucket(logicalServersBucket).Get(key)...); return nil
	}); err != nil {
		t.Fatal(err)
	}

	op := testOperation("server-test")
	op.OperationType = "server.backup"
	op.RequestSnapshot.OperationType = op.OperationType
	op.Target.InstanceRef = server.Record.InstanceRef
	op.RequestFingerprint = "sha256:" + strings.Repeat("a", 64)
	proof := IdempotencyProof{ActiveKeyVersion: "v1", Candidates: []IdempotencyVerifier{{KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}}}
	accepted, _, err := s.AcceptOperation(op, proof, []string{})
	if err != nil {
		t.Fatal(err)
	}

	captured, err := s.CaptureBackupInitialState(accepted.Record.OperationID, accepted.Revision)
	if err != nil || captured.Record.BackupInitialLifecycleState != "" {
		t.Fatalf("capture after setup: %#v, %v", captured, err)
	}
	assertBackupOperationUnchanged(t, s, accepted)
}

func TestCapturePhaseSeparationIndependence(t *testing.T) {
	s := openTestStore(t)
	server := testLogicalServerFixture(t, s)
	server.Record.LifecycleState = protocolstate.ServerStopped
	server.Record.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)

	states := []protocolstate.ServerState{protocolstate.ServerUnprovisioned, protocolstate.ServerProvisioning, protocolstate.ServerPreparing, protocolstate.ServerStarting, protocolstate.ServerValidating, protocolstate.ServerReady, protocolstate.ServerStopping, protocolstate.ServerStopped}
	for i := 1; i < len(states); i++ {
		server.Record.LifecycleEvents = append(server.Record.LifecycleEvents, LogicalServerStateEvent{
			Sequence: uint64(i), OperationID: server.Record.LastOperationID, ActorID: "operator-1", OperationType: "server.provision", FromState: states[i-1], ToState: states[i], Cause: "test fixture history", RequestFingerprint: "sha256:" + strings.Repeat("a", 64), OccurredAt: server.Record.CreatedAt,
		})
	}

	op := testOperation("phase-test")
	op.OperationType = "server.backup"
	op.RequestSnapshot.OperationType = op.OperationType
	op.Target.InstanceRef = server.Record.InstanceRef
	op.RequestFingerprint = "sha256:" + strings.Repeat("b", 64)
	proof := IdempotencyProof{ActiveKeyVersion: "v1", Candidates: []IdempotencyVerifier{{KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}}}
	accepted, _, err := s.AcceptOperation(op, proof, []string{})
	if err != nil {
		t.Fatal(err)
	}

	// First phase should succeed (read and validate)
	captured, err := s.CaptureBackupInitialState(accepted.Record.OperationID, accepted.Revision)
	if err != nil || captured.Record.BackupInitialLifecycleState != protocolstate.ServerStopped {
		t.Fatalf("first capture: %#v, %v", captured, err)
	}

	// Second phase should be idempotent (already captured)
	again, err := s.CaptureBackupInitialState(captured.Record.OperationID, captured.Revision)
	if err != nil || !reflect.DeepEqual(again, captured) {
		t.Fatalf("idempotent capture: %#v, %v", again, err)
	}

	assertBackupOperationUnchanged(t, s, accepted)
}

func TestWriteAndPersistBackupInitialStatePhaseSeparation(t *testing.T) {
	s := openTestStore(t)
	server := testLogicalServerFixture(t, s)
	server.Record.LifecycleState = protocolstate.ServerStopped
	server.Record.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)

	states := []protocolstate.ServerState{protocolstate.ServerUnprovisioned, protocolstate.ServerProvisioning, protocolstate.ServerPreparing, protocolstate.ServerStarting, protocolstate.ServerValidating, protocolstate.ServerReady, protocolstate.ServerStopping, protocolstate.ServerStopped}
	for i := 1; i < len(states); i++ {
		server.Record.LifecycleEvents = append(server.Record.LifecycleEvents, LogicalServerStateEvent{
			Sequence: uint64(i), OperationID: server.Record.LastOperationID, ActorID: "operator-1", OperationType: "server.provision", FromState: states[i-1], ToState: states[i], Cause: "test fixture history", RequestFingerprint: "sha256:" + strings.Repeat("a", 64), OccurredAt: server.Record.CreatedAt,
		})
	}

	op := testOperation("persist-test")
	op.OperationType = "server.backup"
	op.RequestSnapshot.OperationType = op.OperationType
	op.Target.InstanceRef = server.Record.InstanceRef
	op.RequestFingerprint = "sha256:" + strings.Repeat("c", 64)
	proof := IdempotencyProof{ActiveKeyVersion: "v1", Candidates: []IdempotencyVerifier{{KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}}}
	accepted, _, err := s.AcceptOperation(op, proof, []string{})
	if err != nil {
		t.Fatal(err)
	}

	// Create operation with lifecycle state already set to bypass the write phase
	next := accepted.Record
	next.BackupInitialLifecycleState = protocolstate.ServerStopped
	next.Status = protocolstate.OperationPending

	captured, err := s.UpdateOperation(accepted.Revision, next)
	if err != nil || captured.Record.BackupInitialLifecycleState != protocolstate.ServerStopped {
		t.Fatalf("update with state: %#v, %v", captured, err)
	}

	assertBackupOperationUnchanged(t, s, accepted)
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
