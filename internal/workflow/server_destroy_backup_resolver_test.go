package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type recordedBackupSource struct{ record store.StoredOperation }

func (s recordedBackupSource) GetOperation(id string) (store.StoredOperation, error) {
	if id != s.record.Record.OperationID {
		return store.StoredOperation{}, store.ErrNotFound
	}
	return s.record, nil
}

type recordedRemoteReadback struct {
	result BackupObjectReadback
	err    error
	calls  *int
}

func (r recordedRemoteReadback) Readback(_ context.Context, _ json.RawMessage) (BackupObjectReadback, error) {
	if r.calls != nil {
		*r.calls++
	}
	return r.result, r.err
}

func recordedBackupFixture(t *testing.T) (store.OperationRecord, store.LogicalServer) {
	t.Helper()
	fixture := newBackupR3Fixture(t, false)
	op := fixture.complete
	server := fixture.server
	server.LifecycleState = protocolstate.ServerStopped
	server.LastOperationID = op.OperationID
	server.LifecycleEvents = append(server.LifecycleEvents, store.LogicalServerStateEvent{
		OperationID: op.OperationID, OperationType: "server.backup", ToState: protocolstate.ServerStopped,
	})
	finished := time.Now().UTC()
	op.CompletedAt = &finished
	op.Status = protocolstate.OperationSucceeded
	workflow := fixture.backupR3Workflow(server, backupR3Streams{})
	result, err := workflow.BuildSuccessResult(context.Background(), op)
	if err != nil {
		t.Fatalf("build successful offline backup evidence: %v", err)
	}
	op.Result = result
	return op, server
}

func TestStoredBackupResolverRequiresFreshFullRemoteReadback(t *testing.T) {
	op, server := recordedBackupFixture(t)
	calls := 0
	reader := recordedRemoteReadback{
		result: BackupObjectReadback{Remote: true, SizeBytes: 32, SHA256: backupTestDigest},
		calls: &calls,
	}
	schemas := newBackupR3Fixture(t, false).workflow.schemas
	resolver, err := NewStoredBackupResolver(recordedBackupSource{record: store.StoredOperation{Record: op}}, reader, schemas)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resolver.ResolveVerifiedServerBackup(context.Background(), server, server.StorageProfile, op.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IntegrityVerified || !result.OffInstanceVerified || !result.ConsistencyVerified || !result.FinalWriteBoundaryVerified {
		t.Fatalf("incomplete verified backup flags: %#v", result)
	}
	if calls != 1 {
		t.Fatalf("remote objects checked = %d, want one complete readback", calls)
	}
}

func TestStoredBackupResolverBlocksOldOrUnverifiableRecoveryPoints(t *testing.T) {
	op, server := recordedBackupFixture(t)
	schemas := newBackupR3Fixture(t, false).workflow.schemas
	for _, tc := range []struct {
		name   string
		change func(*store.OperationRecord, *store.LogicalServer, *recordedRemoteReadback)
	}{
		{"old backup followed by new start", func(_ *store.OperationRecord, s *store.LogicalServer, _ *recordedRemoteReadback) {
			s.LastOperationID = "later-start"
			s.LifecycleEvents = append(s.LifecycleEvents, store.LogicalServerStateEvent{
				OperationID: "later-start", OperationType: "server.start", ToState: protocolstate.ServerReady,
			})
		}},
		{"wrong current generation", func(_ *store.OperationRecord, s *store.LogicalServer, _ *recordedRemoteReadback) {
			s.DataGenerationID = "another-generation"
		}},
		{"wrong instance", func(_ *store.OperationRecord, s *store.LogicalServer, _ *recordedRemoteReadback) {
			s.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"another-vm"}`)
		}},
		{"backup not successful", func(o *store.OperationRecord, _ *store.LogicalServer, _ *recordedRemoteReadback) {
			o.Status = protocolstate.OperationFailed
		}},
		{"game save not confirmed", func(o *store.OperationRecord, _ *store.LogicalServer, _ *recordedRemoteReadback) {
			setBackupOutputField(t, o, backupSaveGameStep, "outcome", "unknown")
		}},
		{"missing remote storage", func(_ *store.OperationRecord, _ *store.LogicalServer, r *recordedRemoteReadback) {
			r.err = errors.New("no such object")
		}},
		{"local-only storage", func(_ *store.OperationRecord, _ *store.LogicalServer, r *recordedRemoteReadback) {
			r.result.Remote = false
		}},
		{"incorrect full checksum", func(_ *store.OperationRecord, _ *store.LogicalServer, r *recordedRemoteReadback) {
			r.result.SHA256 = strings.Repeat("0", 64)
		}},
		{"truncated object", func(_ *store.OperationRecord, _ *store.LogicalServer, r *recordedRemoteReadback) {
			r.result.SizeBytes = 8
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backup := backupR3Copy(op)
			current := server
			reader := recordedRemoteReadback{result: BackupObjectReadback{Remote: true, SizeBytes: 32, SHA256: backupTestDigest}}
			tc.change(&backup, &current, &reader)
			resolver, err := NewStoredBackupResolver(
				recordedBackupSource{record: store.StoredOperation{Record: backup}}, reader, schemas)
			if err != nil {
				t.Fatal(err)
			}
			if result, err := resolver.ResolveVerifiedServerBackup(context.Background(), current, current.StorageProfile, backup.OperationID); err == nil {
				t.Fatalf("unsafe backup accepted: %#v", result)
			}
		})
	}
}

func TestFinalRecoveryBoundarySurvivesOnlyItsOwnDestroy(t *testing.T) {
	op, server := recordedBackupFixture(t)
	if !finalBackupLifecycleBoundary(server, op.OperationID) {
		t.Fatal("completed offline backup must establish stopped boundary")
	}
	server.LifecycleState = protocolstate.ServerDestroying
	server.LastOperationID = "destroy-1"
	server.LifecycleEvents = append(server.LifecycleEvents, store.LogicalServerStateEvent{
		OperationID: "destroy-1", OperationType: "server.destroy", ToState: protocolstate.ServerDestroying,
	})
	if !finalBackupLifecycleBoundary(server, op.OperationID) {
		t.Fatal("same-instance destroy should preserve final backup boundary")
	}
	server.LifecycleEvents = append(server.LifecycleEvents, store.LogicalServerStateEvent{
		OperationID: "start-2", OperationType: "server.start", ToState: protocolstate.ServerReady,
	})
	if finalBackupLifecycleBoundary(server, op.OperationID) {
		t.Fatal("a later start invalidates the final write boundary")
	}
}
