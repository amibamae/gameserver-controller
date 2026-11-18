package store

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// These characterization tests pin UpdateOperationAndLogicalServer and the
// logical-server rules of AcceptOperation: exact errors, the stored bytes
// after each call, and an unchanged store after each rejection.

// storeR3LifecycleFixture accepts a locked server.provision for the logical
// server that testLogicalServerFixture writes, and returns the next
// operation and server records of a legal lifecycle commit.
func storeR3LifecycleFixture(t *testing.T, instanceRef json.RawMessage) (*BoltStore, StoredOperation, StoredLogicalServer, OperationRecord, LogicalServer) {
	t.Helper()
	store := openTestStore(t)
	server := testLogicalServerFixture(t, store)
	operation := testOperation("r3-lifecycle")
	operation.OperationType = "server.provision"
	operation.RequestSnapshot.OperationType = operation.OperationType
	operation.RequestFingerprint = "sha256:" + strings.Repeat("f", 64)
	operation.Target.InstanceRef = instanceRef
	operation.Steps = append(operation.Steps, OperationStep{StepID: "create", StepType: "host.create", Status: protocolstate.StepPending})
	created, _, err := store.AcceptOperation(operation, proofOf("v1", verifier("v1", 1, 2)), []string{LogicalServerLockScope("staging", "server-1")})
	if err != nil {
		t.Fatal(err)
	}
	next := created.Record
	next.Steps = append([]OperationStep(nil), created.Record.Steps...)
	next.Status = protocolstate.OperationRunning
	next.Steps[0].Status, next.Steps[0].Attempt = protocolstate.StepRunning, 1
	nextServer := server.Record
	nextServer.LifecycleState = protocolstate.ServerProvisioning
	return store, created, server, next, nextServer
}

func TestStoreR3UpdateOperationAndLogicalServerRejectionsCharacterization(t *testing.T) {
	serverKey := logicalServerKey("staging", "server-1")
	lockScope := LogicalServerLockScope("staging", "server-1")
	putServer := func(t *testing.T, s *BoltStore, stored StoredLogicalServer) {
		storeR3RawPut(t, s, logicalServersBucket, serverKey, storeR3JSON(t, stored))
	}
	type call struct {
		operationRevision uint64
		operation         OperationRecord
		serverRevision    uint64
		server            LogicalServer
	}
	tests := []struct {
		name        string
		instanceRef json.RawMessage
		prepare     func(*testing.T, *BoltStore, *call, StoredLogicalServer)
		want        error
		message     string
	}{
		{name: "backup without context", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) {
			c.operation.OperationType, c.operation.OperationID = "server.backup", ""
		}, want: ErrInvalidRecord, message: "invalid operation record: backup context must precede lifecycle transition"},
		{name: "missing operation ID", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) { c.operation.OperationID = "" },
			want: ErrInvalidRecord, message: "invalid operation record: operation target is required for lifecycle commit"},
		{name: "missing server ID", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) { c.operation.Target.ServerID = "" },
			want: ErrInvalidRecord, message: "invalid operation record: operation target is required for lifecycle commit"},
		{name: "missing environment ID", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) {
			c.operation.Target.EnvironmentID = ""
		},
			want: ErrInvalidRecord, message: "invalid operation record: operation target is required for lifecycle commit"},
		{name: "non-server operation", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) {
			c.operation.OperationType = "test.operation"
		},
			want: ErrInvalidRecord, message: "invalid operation record: only mutating server operations may commit lifecycle transitions"},
		{name: "server definition", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) {
			c.operation.OperationType = "server.define"
		},
			want: ErrInvalidRecord, message: "invalid operation record: only mutating server operations may commit lifecycle transitions"},
		{name: "unknown operation", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) { c.operation.OperationID = "missing" },
			want: ErrNotFound, message: "operation not found"},
		{name: "stale operation revision", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) { c.operationRevision = 7 },
			want: ErrRevisionConflict, message: "operation revision conflict"},
		{name: "missing lock index", prepare: func(t *testing.T, s *BoltStore, c *call, _ StoredLogicalServer) {
			storeR3RawPut(t, s, operationLocksBucket, []byte("r3-lifecycle"), nil)
		}, want: ErrCorruptStore, message: `corrupt operation store: operation "r3-lifecycle" has no durable lock index`},
		{name: "missing lock checked before request", prepare: func(t *testing.T, s *BoltStore, c *call, _ StoredLogicalServer) {
			storeR3RawPut(t, s, locksBucket, []byte(lockScope), nil)
			c.operation.ActorID = "operator-2"
		}, want: ErrCorruptStore, message: `corrupt operation store: required server lock "` + lockScope + `" is missing`},
		{name: "changed request", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) { c.operation.ActorID = "operator-2" },
			want: ErrImmutableFields, message: "accepted operation fields are immutable"},
		{name: "request checked before server", prepare: func(t *testing.T, s *BoltStore, c *call, _ StoredLogicalServer) {
			storeR3RawPut(t, s, logicalServersBucket, serverKey, nil)
			c.operation.ActorID = "operator-2"
		}, want: ErrImmutableFields, message: "accepted operation fields are immutable"},
		{name: "missing server", prepare: func(t *testing.T, s *BoltStore, _ *call, _ StoredLogicalServer) {
			storeR3RawPut(t, s, logicalServersBucket, serverKey, nil)
		}, want: ErrLogicalServerNotFound, message: "logical server not found"},
		{name: "corrupt server", prepare: func(t *testing.T, s *BoltStore, _ *call, _ StoredLogicalServer) {
			storeR3RawPut(t, s, logicalServersBucket, serverKey, []byte(`{`))
		}, want: ErrCorruptStore, message: "corrupt operation store: decode logical server: unexpected end of JSON input"},
		{name: "server revision zero", prepare: func(t *testing.T, s *BoltStore, _ *call, server StoredLogicalServer) {
			server.Revision = 0
			putServer(t, s, server)
		}, want: ErrCorruptStore, message: "corrupt operation store: logical server key or revision mismatch"},
		{name: "server under another key", prepare: func(t *testing.T, s *BoltStore, _ *call, server StoredLogicalServer) {
			server.Record.ServerID = "server-2"
			putServer(t, s, server)
		}, want: ErrCorruptStore, message: "corrupt operation store: logical server key or revision mismatch"},
		{name: "server environment under another key", prepare: func(t *testing.T, s *BoltStore, _ *call, server StoredLogicalServer) {
			server.Record.EnvironmentID = "production"
			putServer(t, s, server)
		}, want: ErrCorruptStore, message: "corrupt operation store: logical server key or revision mismatch"},
		{name: "stale server revision", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) { c.serverRevision = 9 },
			want: ErrRevisionConflict, message: "operation revision conflict"},
		{name: "server revision checked before record", prepare: func(t *testing.T, s *BoltStore, c *call, server StoredLogicalServer) {
			server.Record.DataGenerationStatus = "bogus"
			putServer(t, s, server)
			c.serverRevision = 9
		}, want: ErrRevisionConflict, message: "operation revision conflict"},
		{name: "invalid stored server", prepare: func(t *testing.T, s *BoltStore, _ *call, server StoredLogicalServer) {
			server.Record.DataGenerationStatus = "bogus"
			putServer(t, s, server)
		}, want: ErrCorruptStore, message: "corrupt operation store: invalid logical server record"},
		{name: "instance ref without server instance", instanceRef: json.RawMessage(`{"id":"i-1"}`),
			want: ErrLogicalServerTargetMismatch, message: "operation target does not match the current logical-server instance"},
		{name: "instance ref of another instance", instanceRef: json.RawMessage(`{"id":"i-1"}`), prepare: func(t *testing.T, s *BoltStore, _ *call, server StoredLogicalServer) {
			server.Record.InstanceRef = json.RawMessage(`{"id":"i-2"}`)
			putServer(t, s, server)
		}, want: ErrLogicalServerTargetMismatch, message: "operation target does not match the current logical-server instance"},
		{name: "changed server identity", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) { c.server.DisplayName = "Renamed" },
			want: ErrImmutableFields, message: "accepted operation fields are immutable"},
		{name: "changed server creation time", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) {
			c.server.CreatedAt = c.server.CreatedAt.Add(time.Second)
		}, want: ErrImmutableFields, message: "accepted operation fields are immutable"},
		{name: "same lifecycle state", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) {
			c.server.LifecycleState = protocolstate.ServerUnprovisioned
		}, want: ErrInvalidRecord, message: "invalid operation record: lifecycle commit must advance the server state"},
		{name: "illegal server transition", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) {
			c.server.LifecycleState = protocolstate.ServerReady
		},
			want: protocolstate.ErrIllegalTransition, message: `server "unprovisioned" cannot transition to "ready": illegal protocol state transition`},
		{name: "server revision exhausted", prepare: func(t *testing.T, s *BoltStore, c *call, server StoredLogicalServer) {
			server.Revision = ^uint64(0)
			putServer(t, s, server)
			c.serverRevision = ^uint64(0)
		}, want: ErrInvalidRecord, message: "invalid operation record: logical server revision exhausted"},
		{name: "invalid next server", prepare: func(_ *testing.T, _ *BoltStore, c *call, _ StoredLogicalServer) { c.server.DesiredState = "paused" },
			want: ErrInvalidRecord, message: "invalid operation record: invalid logical server desired state"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, created, server, next, nextServer := storeR3LifecycleFixture(t, test.instanceRef)
			c := call{operationRevision: created.Revision, operation: next, serverRevision: server.Revision, server: nextServer}
			if test.prepare != nil {
				test.prepare(t, store, &c, server)
			}
			before := storeR3Snapshot(t, store.db)
			_, _, err := store.UpdateOperationAndLogicalServer(c.operationRevision, c.operation, c.serverRevision, c.server)
			storeR3AssertErr(t, err, test.want, test.message)
			storeR3AssertUnchanged(t, store.db, before)
		})
	}
}

func storeR3AssertStoredServer(t *testing.T, store *BoltStore, stored StoredLogicalServer) {
	t.Helper()
	key := logicalServerKey(stored.Record.EnvironmentID, stored.Record.ServerID)
	if got, want := storeR3RawGet(t, store, logicalServersBucket, key), storeR3JSON(t, stored); string(got) != string(want) {
		t.Fatalf("stored server bytes:\n got %s\nwant %s", got, want)
	}
}

// storeR3Commit runs one lifecycle commit and requires the stored bytes of
// both records to be the returned values.
type storeR3CommitInput struct {
	operation  StoredOperation
	next       OperationRecord
	server     StoredLogicalServer
	nextServer LogicalServer
}

func storeR3Commit(t *testing.T, store *BoltStore, in storeR3CommitInput) (StoredOperation, StoredLogicalServer) {
	t.Helper()
	committed, stored, err := store.UpdateOperationAndLogicalServer(in.operation.Revision, in.next, in.server.Revision, in.nextServer)
	if err != nil {
		t.Fatal(err)
	}
	storeR3AssertStoredOperation(t, store, committed)
	storeR3AssertStoredServer(t, store, stored)
	return committed, stored
}

func TestStoreR3UpdateOperationAndLogicalServerStoredStateCharacterization(t *testing.T) {
	store, created, server, next, nextServer := storeR3LifecycleFixture(t, json.RawMessage(`{"region":"ams","id":"i-1"}`))
	// The server instance matches canonically, not byte for byte.
	server.Record.InstanceRef = json.RawMessage(`{"id":"i-1","region":"ams"}`)
	storeR3RawPut(t, store, logicalServersBucket, logicalServerKey("staging", "server-1"), storeR3JSON(t, server))
	nextServer.InstanceRef = server.Record.InstanceRef
	// Caller-supplied history, owner, and time are replaced by the store.
	nextServer.LastOperationID = "caller"
	nextServer.UpdatedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	nextServer.LifecycleEvents = []LogicalServerStateEvent{{Sequence: 9, OperationID: "caller"}}

	operation, stored := storeR3Commit(t, store, storeR3CommitInput{created, next, server, nextServer})
	wantEvent := LogicalServerStateEvent{
		Sequence: 1, OperationID: "r3-lifecycle", ActorID: "operator-1", OperationType: "server.provision",
		FromState: protocolstate.ServerUnprovisioned, ToState: protocolstate.ServerProvisioning, Cause: "server.provision",
		RequestFingerprint: "sha256:" + strings.Repeat("f", 64), EvidenceStepIDs: []string{}, OccurredAt: stored.Record.UpdatedAt,
	}
	storeR3Expect(t, stored, map[string]bool{
		"operation revision 2": operation.Revision == 2,
		"server revision 2":    stored.Revision == 2,
		"last operation":       stored.Record.LastOperationID == "r3-lifecycle",
		"updated with commit":  stored.Record.UpdatedAt.Equal(operation.Record.UpdatedAt),
		"one generated event":  reflect.DeepEqual(stored.Record.LifecycleEvents, []LogicalServerStateEvent{wantEvent}),
	})

	// The second commit appends an event whose evidence is every succeeded step.
	second := operation.Record
	second.Steps = append([]OperationStep(nil), operation.Record.Steps...)
	second.Steps[0].Status = protocolstate.StepSucceeded
	secondServer := stored.Record
	secondServer.LifecycleState = protocolstate.ServerPreparing
	operation2, stored2 := storeR3Commit(t, store, storeR3CommitInput{operation, second, stored, secondServer})
	wantSecond := LogicalServerStateEvent{
		Sequence: 2, OperationID: "r3-lifecycle", ActorID: "operator-1", OperationType: "server.provision",
		FromState: protocolstate.ServerProvisioning, ToState: protocolstate.ServerPreparing, Cause: "server.provision",
		RequestFingerprint: "sha256:" + strings.Repeat("f", 64), EvidenceStepIDs: []string{"validate"}, OccurredAt: stored2.Record.UpdatedAt,
	}
	storeR3Expect(t, stored2, map[string]bool{
		"operation revision 3": operation2.Revision == 3,
		"server revision 3":    stored2.Revision == 3,
		"monotonic update":     !stored2.Record.UpdatedAt.Before(stored.Record.UpdatedAt),
		"appended event":       reflect.DeepEqual(stored2.Record.LifecycleEvents, []LogicalServerStateEvent{wantEvent, wantSecond}),
	})
}

func storeR3DefineRecord(id, environmentID, serverID string) OperationRecord {
	record := testOperation(id)
	record.OperationType, record.RequestSnapshot.OperationType = "server.define", "server.define"
	record.Steps = nil
	record.Target = OperationTarget{ServerID: serverID, EnvironmentID: environmentID}
	record.RequestFingerprint = "sha256:" + strings.Repeat("d", 64)
	record.RequestSnapshot.Parameters = json.RawMessage(`{"server_id":"` + serverID + `","environment_id":"` + environmentID + `","display_name":"Prod","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"},"network_profile":"public"}`)
	return record
}

func TestStoreR3AcceptanceLogicalServerRejectionsCharacterization(t *testing.T) {
	const onlyDefinition = "invalid operation record: server.define must contain only a logical definition"
	stagingKey := logicalServerKey("staging", "server-1")
	tests := []struct {
		name    string
		record  func() OperationRecord
		prepare func(*testing.T, *BoltStore)
		want    error
		message string
	}{
		{name: "definition with secrets", record: func() OperationRecord {
			r := storeR3DefineRecord("r3-define", "production", "server-9")
			r.RequestSnapshot.SecretRefs = []SecretReference{{SecretRef: "vault://a", Purpose: "p"}}
			return r
		}, want: ErrInvalidRecord, message: onlyDefinition},
		{name: "definition with instance", record: func() OperationRecord {
			r := storeR3DefineRecord("r3-define", "production", "server-9")
			r.Target.InstanceRef = json.RawMessage(`{"id":"i-1"}`)
			return r
		}, want: ErrInvalidRecord, message: onlyDefinition},
		{name: "definition with unreadable parameters", record: func() OperationRecord {
			r := storeR3DefineRecord("r3-define", "production", "server-9")
			r.RequestSnapshot.Parameters = json.RawMessage(`{"server_id":1}`)
			return r
		}, want: ErrInvalidRecord, message: "invalid operation record: server.define parameters are invalid"},
		{name: "definition for another target", record: func() OperationRecord {
			r := storeR3DefineRecord("r3-define", "production", "server-9")
			r.Target.ServerID = "server-8"
			return r
		}, want: ErrInvalidRecord, message: "invalid operation record: server.define target does not match its parameters"},
		{name: "parameters checked before existence", record: func() OperationRecord {
			r := storeR3DefineRecord("r3-define", "staging", "server-1")
			r.RequestSnapshot.Parameters = json.RawMessage(`{"server_id":"server-2","environment_id":"staging"}`)
			return r
		}, prepare: func(t *testing.T, s *BoltStore) { testLogicalServerFixture(t, s) }, want: ErrInvalidRecord,
			message: "invalid operation record: server.define target does not match its parameters"},
		{name: "definition already exists", record: func() OperationRecord { return storeR3DefineRecord("r3-define", "staging", "server-1") },
			prepare: func(t *testing.T, s *BoltStore) { testLogicalServerFixture(t, s) }, want: ErrLogicalServerAlreadyExists, message: "logical server already exists"},
		{name: "definition with invalid fields", record: func() OperationRecord {
			r := storeR3DefineRecord("r3-define", "production", "server-9")
			r.RequestSnapshot.Parameters = json.RawMessage(`{"server_id":"server-9","environment_id":"production"}`)
			return r
		}, want: ErrInvalidRecord, message: "invalid operation record: invalid logical server record"},
		{name: "lifecycle on corrupt server", record: func() OperationRecord { r, _ := serverBackupRecord("r3-backup"); return r },
			prepare: func(t *testing.T, s *BoltStore) { storeR3RawPut(t, s, logicalServersBucket, stagingKey, []byte(`{`)) },
			want:    ErrCorruptStore, message: "corrupt operation store: decode logical server: unexpected end of JSON input"},
		{name: "lifecycle on server revision zero", record: func() OperationRecord { r, _ := serverBackupRecord("r3-backup"); return r },
			prepare: func(t *testing.T, s *BoltStore) {
				server := testLogicalServerFixture(t, s)
				server.Revision = 0
				storeR3RawPut(t, s, logicalServersBucket, stagingKey, storeR3JSON(t, server))
			}, want: ErrCorruptStore, message: "corrupt operation store: logical server key does not match its record"},
		{name: "lifecycle on invalid server", record: func() OperationRecord { r, _ := serverBackupRecord("r3-backup"); return r },
			prepare: func(t *testing.T, s *BoltStore) {
				server := testLogicalServerFixture(t, s)
				server.Record.DataGenerationStatus = "bogus"
				storeR3RawPut(t, s, logicalServersBucket, stagingKey, storeR3JSON(t, server))
			}, want: ErrCorruptStore, message: "corrupt operation store: invalid logical server record"},
		{name: "lifecycle without server", record: func() OperationRecord { r, _ := serverBackupRecord("r3-backup"); return r },
			want: ErrLogicalServerNotFound, message: "logical server not found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			if test.prepare != nil {
				test.prepare(t, store)
			}
			record := test.record()
			before := storeR3Snapshot(t, store.db)
			_, _, err := store.AcceptOperation(record, proofOf("v1", verifier("v1", 5, 6)), []string{LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID)})
			storeR3AssertErr(t, err, test.want, test.message)
			storeR3AssertUnchanged(t, store.db, before)
		})
	}
}

func TestStoreR3AcceptanceLogicalServerStoredStateCharacterization(t *testing.T) {
	store := openTestStore(t)
	// Unreadable neighbours are skipped by the cross-environment ID check.
	storeR3RawPut(t, store, logicalServersBucket, []byte("garbage"), []byte(`{`))
	record := storeR3DefineRecord("r3-define", "production", "server-9")
	accepted, replayed, err := store.AcceptOperation(record, proofOf("v1", verifier("v1", 5, 6)), []string{LogicalServerLockScope("production", "server-9")})
	if err != nil || replayed {
		t.Fatalf("define: replayed=%v err=%v", replayed, err)
	}
	want, err := newLogicalServerFromDefinition(accepted.Record)
	if err != nil {
		t.Fatal(err)
	}
	want.CreatedAt, want.UpdatedAt, want.LastOperationID = accepted.Record.CreatedAt, accepted.Record.CreatedAt, "r3-define"
	wantBytes, err := encodeLogicalServer(want)
	if err != nil {
		t.Fatal(err)
	}
	if got := storeR3RawGet(t, store, logicalServersBucket, logicalServerKey("production", "server-9")); string(got) != string(wantBytes) {
		t.Fatalf("defined server bytes:\n got %s\nwant %s", got, wantBytes)
	}
	storeR3AssertStoredOperation(t, store, accepted)

	// A non-server operation needs no logical server.
	plain := testOperation("r3-plain")
	plain.RequestFingerprint = "sha256:" + strings.Repeat("c", 64)
	if _, _, err := store.AcceptOperation(plain, proofOf("v1", verifier("v1", 3, 4)), nil); err != nil {
		t.Fatalf("plain operation: %v", err)
	}
	// A lifecycle operation on the defined server is accepted.
	backup := testOperation("r3-backup")
	backup.OperationType, backup.RequestSnapshot.OperationType = "server.backup", "server.backup"
	backup.Target = OperationTarget{ServerID: "server-9", EnvironmentID: "production"}
	backup.RequestFingerprint = "sha256:" + strings.Repeat("b", 64)
	if _, _, err := store.AcceptOperation(backup, proofOf("v1", verifier("v1", 9, 9)), []string{LogicalServerLockScope("production", "server-9")}); !errors.Is(err, ErrResourceConflict) {
		t.Fatalf("backup while define holds the lock: %v", err)
	}
}
