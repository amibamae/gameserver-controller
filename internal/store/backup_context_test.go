package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	bolt "go.etcd.io/bbolt"
)

func backupContextFixture(t *testing.T) (*BoltStore, StoredOperation) {
	t.Helper()
	s := openTestStore(t)
	server := testLogicalServerFixture(t, s)
	server.Record.LifecycleState = protocolstate.ServerStopped
	states := []protocolstate.ServerState{protocolstate.ServerUnprovisioned, protocolstate.ServerProvisioning, protocolstate.ServerPreparing, protocolstate.ServerStarting, protocolstate.ServerValidating, protocolstate.ServerReady, protocolstate.ServerStopping, protocolstate.ServerStopped}
	for i := 1; i < len(states); i++ {
		server.Record.LifecycleEvents = append(server.Record.LifecycleEvents, LogicalServerStateEvent{
			Sequence: uint64(i), OperationID: server.Record.LastOperationID, ActorID: "operator-1", OperationType: "server.provision", FromState: states[i-1], ToState: states[i], Cause: "test fixture history", RequestFingerprint: "sha256:" + strings.Repeat("a", 64), OccurredAt: server.Record.CreatedAt,
		})
	}
	server.Record.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-1"}`)
	encoded, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(logicalServersBucket).Put(logicalServerKey("staging", "server-1"), encoded)
	}); err != nil {
		t.Fatal(err)
	}
	op := testOperation("backup-context")
	op.OperationType = "server.backup"
	op.RequestSnapshot.OperationType = op.OperationType
	op.Target.InstanceRef = server.Record.InstanceRef
	op.RequestFingerprint = "sha256:" + strings.Repeat("a", 64)
	proof := IdempotencyProof{ActiveKeyVersion: "v1", Candidates: []IdempotencyVerifier{{KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}}}
	accepted, _, err := s.AcceptOperation(op, proof, []string{LogicalServerLockScope("staging", "server-1")})
	if err != nil {
		t.Fatal(err)
	}
	return s, accepted
}

func TestBackupContextCaptureIsDurableIdempotentAndImmutable(t *testing.T) {
	s, op := backupContextFixture(t)
	captured, err := s.CaptureBackupInitialState(op.Record.OperationID, op.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if captured.Record.BackupInitialLifecycleState != protocolstate.ServerStopped || captured.Revision != op.Revision+1 {
		t.Fatalf("capture = %#v", captured)
	}
	again, err := s.CaptureBackupInitialState(op.Record.OperationID, captured.Revision)
	if err != nil || again.Revision != captured.Revision {
		t.Fatalf("repeat capture = %#v, %v", again, err)
	}
	next := captured.Record
	next.BackupInitialLifecycleState = protocolstate.ServerReady
	if _, err := s.UpdateOperation(captured.Revision, next); !errors.Is(err, ErrImmutableFields) {
		t.Fatalf("metadata rewrite error = %v", err)
	}
	path := s.db.Path()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.GetOperation(op.Record.OperationID)
	if err != nil || loaded.Record.BackupInitialLifecycleState != protocolstate.ServerStopped {
		t.Fatalf("restarted capture = %#v, %v", loaded, err)
	}
}

func TestBackupContextRejectsCallerMetadataAndStaleRevision(t *testing.T) {
	s, op := backupContextFixture(t)
	server, err := s.GetLogicalServer("staging", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	server.Record.LifecycleState = protocolstate.ServerBackingUp
	if _, _, err := s.UpdateOperationAndLogicalServer(op.Revision, op.Record, server.Revision, server.Record); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("lifecycle transition without captured context error = %v", err)
	}
	forged := testOperation("forged-backup")
	forged.OperationType = "server.backup"
	forged.RequestSnapshot.OperationType = forged.OperationType
	forged.BackupInitialLifecycleState = protocolstate.ServerReady
	if _, err := s.createOperation(forged); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("caller metadata error = %v", err)
	}
	if _, err := s.CaptureBackupInitialState(op.Record.OperationID, op.Revision+1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision error = %v", err)
	}
	next := op.Record
	next.BackupInitialLifecycleState = protocolstate.ServerStopped
	if _, err := s.UpdateOperation(op.Revision, next); !errors.Is(err, ErrImmutableFields) {
		t.Fatalf("ordinary update captured context: %v", err)
	}
}

func TestBackupContextRejectsCaptureAfterDispatch(t *testing.T) {
	s, op := backupContextFixture(t)
	next := op.Record
	next.Status = protocolstate.OperationRunning
	next.Steps[0].Status = protocolstate.StepRunning
	next.Steps[0].Attempt = 1
	if _, err := s.UpdateOperation(op.Revision, next); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("dispatch without captured context error = %v", err)
	}
}

func TestBackupContextRejectsChangedTargetAndMissingLock(t *testing.T) {
	for _, scenario := range []string{"target", "lock"} {
		t.Run(scenario, func(t *testing.T) {
			s, op := backupContextFixture(t)
			if err := s.db.Update(func(tx *bolt.Tx) error {
				if scenario == "lock" {
					return tx.Bucket(locksBucket).Delete([]byte(LogicalServerLockScope("staging", "server-1")))
				}
				bucket := tx.Bucket(logicalServersBucket)
				key := logicalServerKey("staging", "server-1")
				var server StoredLogicalServer
				if err := json.Unmarshal(bucket.Get(key), &server); err != nil {
					return err
				}
				server.Record.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"replacement"}`)
				encoded, err := json.Marshal(server)
				if err != nil {
					return err
				}
				return bucket.Put(key, encoded)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CaptureBackupInitialState(op.Record.OperationID, op.Revision); err == nil {
				t.Fatal("capture accepted changed target or missing lock")
			}
			loaded, err := s.GetOperation(op.Record.OperationID)
			if err != nil || loaded.Revision != op.Revision || loaded.Record.BackupInitialLifecycleState != "" {
				t.Fatalf("rejected capture changed operation: %#v, %v", loaded, err)
			}
		})
	}
}
