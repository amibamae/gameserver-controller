package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type allowValidator struct{}

func (allowValidator) ValidateOperationRequest(context.Context, store.OperationRecord) error {
	return nil
}

type allowAuthorizer struct{}

func (allowAuthorizer) AuthorizeOperation(context.Context, string, store.OperationRecord) error {
	return nil
}

func newTestService(t *testing.T, operationStore *store.BoltStore, ring *Keyring) *Service {
	t.Helper()
	service, err := NewService(operationStore, allowValidator{}, allowAuthorizer{}, LogicalServerLockPolicy{}, ring)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testKeyring(t *testing.T, active string, entries map[string]byte) *Keyring {
	t.Helper()
	keys := make(map[string][]byte, len(entries))
	for version, value := range entries {
		key := make([]byte, 32)
		for i := range key {
			key[i] = value
		}
		keys[version] = key
	}
	ring, err := NewKeyring(active, keys)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func testOperation() store.OperationRecord {
	return store.OperationRecord{
		OperationType:   "server.provision",
		ProtocolVersion: "0.21.0-draft",
		Target: store.OperationTarget{
			ServerID:      "server-1",
			EnvironmentID: "staging",
		},
		RequestSnapshot: store.RequestSnapshot{
			OperationType: "server.provision",
			Parameters:    json.RawMessage(`{"plan":"small","count":1.0}`),
		},
		Steps: []store.OperationStep{{
			StepID:   "create-instance",
			StepType: "hosting.instance.create",
			Status:   protocolstate.StepPending,
		}},
	}
}

func openStore(t *testing.T) (*store.BoltStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "controller.db")
	operationStore, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := operationStore.Close(); err != nil {
			t.Errorf("close operation store: %v", err)
		}
	})
	return operationStore, path
}

func TestRequestFingerprintUsesJCSAndRejectsDuplicateKeys(t *testing.T) {
	first, err := RequestFingerprint(json.RawMessage(`{"plan":"small","count":1.0}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := RequestFingerprint(json.RawMessage(`{"count":1,"plan":"small"}`))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("JCS fingerprints differ for equivalent objects: %s != %s", first, second)
	}
	if _, err := RequestFingerprint(json.RawMessage(`{"plan":"small","plan":"large"}`)); err == nil {
		t.Fatal("duplicate properties must not be fingerprinted")
	}
	if _, err := RequestFingerprint(json.RawMessage(`[1,2]`)); err == nil {
		t.Fatal("top-level array must not be accepted as request parameters")
	}
}

func TestAcceptReplaysCanonicalSameRequestAndBindsActor(t *testing.T) {
	operationStore, _ := openStore(t)
	service := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	firstRequest := testOperation()
	firstRequest.Target.InstanceRef = json.RawMessage(`{"external_id":"instance-1","resource_kind":"instance"}`)

	first, replayed, err := service.Accept(context.Background(), "trusted-actor", "request-key", firstRequest)
	if err != nil || replayed {
		t.Fatalf("first acceptance: replayed=%t err=%v", replayed, err)
	}
	if first.Record.ActorID != "trusted-actor" || len(first.Record.OperationID) != 32 || first.Record.RequestFingerprint == "" {
		t.Fatalf("acceptance did not assign trusted identity and fingerprint: %#v", first.Record)
	}

	retry := testOperation()
	retry.ActorID = "untrusted-body-actor"
	retry.OperationID = "caller-selected-id"
	retry.RequestSnapshot.Parameters = json.RawMessage(`{"count":1,"plan":"small"}`)
	retry.Target.InstanceRef = json.RawMessage(`{"resource_kind":"instance","external_id":"instance-1"}`)
	second, replayed, err := service.Accept(context.Background(), "trusted-actor", "request-key", retry)
	if err != nil || !replayed {
		t.Fatalf("same request retry: replayed=%t err=%v", replayed, err)
	}
	if second.Record.OperationID != first.Record.OperationID || second.Record.ActorID != "trusted-actor" {
		t.Fatalf("retry did not identify original trusted operation: %#v", second.Record)
	}
	locks, err := operationStore.ListResourceLocks()
	if err != nil || len(locks) != 1 || locks[0].OperationID != first.Record.OperationID {
		t.Fatalf("replay changed resource reservation: locks=%#v err=%v", locks, err)
	}

	otherActorRequest := testOperation()
	otherActorRequest.Target.ServerID = "server-2"
	otherActor, replayed, err := service.Accept(context.Background(), "other-actor", "request-key", otherActorRequest)
	if err != nil || replayed || otherActor.Record.OperationID == first.Record.OperationID {
		t.Fatalf("actor-scoped key did not create a distinct request: operation=%#v replayed=%t err=%v", otherActor.Record, replayed, err)
	}
}

func TestIdempotencyConflictAndResourceConflict(t *testing.T) {
	operationStore, _ := openStore(t)
	service := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	if _, _, err := service.Accept(context.Background(), "actor", "key-one", testOperation()); err != nil {
		t.Fatal(err)
	}

	changed := testOperation()
	changed.RequestSnapshot.Parameters = json.RawMessage(`{"plan":"large"}`)
	if _, _, err := service.Accept(context.Background(), "actor", "key-one", changed); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("same key with changed request: got %v, want ErrIdempotencyConflict", err)
	}
	if _, _, err := service.Accept(context.Background(), "actor", "key-two", testOperation()); !errors.Is(err, store.ErrResourceConflict) {
		t.Fatalf("new key against occupied server: got %v, want ErrResourceConflict", err)
	}
	changedTarget := testOperation()
	changedTarget.Target.InstanceRef = json.RawMessage(`{"resource_kind":"instance","external_id":"different"}`)
	if _, _, err := service.Accept(context.Background(), "actor", "key-one", changedTarget); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("same key with changed external target: got %v, want ErrIdempotencyConflict", err)
	}
	changedSecret := testOperation()
	changedSecret.RequestSnapshot.SecretRefs = []store.SecretReference{{SecretRef: "vault://minecraft/storage", Purpose: "storage credentials"}}
	if _, _, err := service.Accept(context.Background(), "actor", "key-one", changedSecret); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("same key with changed secret reference: got %v, want ErrIdempotencyConflict", err)
	}
}

type rejectingAuthorizer struct{ err error }

func (a rejectingAuthorizer) AuthorizeOperation(context.Context, string, store.OperationRecord) error {
	return a.err
}

func TestAuthorizationFailureDoesNotPersistOperationOrLock(t *testing.T) {
	operationStore, _ := openStore(t)
	authorizationErr := errors.New("approval required")
	service, err := NewService(operationStore, allowValidator{}, rejectingAuthorizer{err: authorizationErr}, LogicalServerLockPolicy{}, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Accept(context.Background(), "actor", "key", testOperation()); !errors.Is(err, authorizationErr) {
		t.Fatalf("authorization denial: got %v", err)
	}
	operations, err := operationStore.ListRecoverableOperations()
	if err != nil || len(operations) != 0 {
		t.Fatalf("authorization denial persisted an operation: operations=%#v err=%v", operations, err)
	}
	locks, err := operationStore.ListResourceLocks()
	if err != nil || len(locks) != 0 {
		t.Fatalf("authorization denial reserved a resource: locks=%#v err=%v", locks, err)
	}
}

func TestTerminalCommitReleasesLockAndSuspendedWorkKeepsIt(t *testing.T) {
	operationStore, _ := openStore(t)
	service := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	first, _, err := service.Accept(context.Background(), "actor", "key-one", testOperation())
	if err != nil {
		t.Fatal(err)
	}
	cancelled := first.Record
	cancelled.Status = protocolstate.OperationCancelled
	if _, err := operationStore.UpdateOperation(first.Revision, cancelled); err != nil {
		t.Fatal(err)
	}
	if _, err := operationStore.GetResourceLock(LogicalServerLockScope("staging", "server-1")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("terminal commit did not release lock: got %v", err)
	}

	second, _, err := service.Accept(context.Background(), "actor", "key-two", testOperation())
	if err != nil {
		t.Fatalf("accept after terminal release: %v", err)
	}
	running := second.Record
	running.Status = protocolstate.OperationRunning
	running.Steps[0].Status = protocolstate.StepRunning
	running.Steps[0].Attempt = 1
	runningStored, err := operationStore.UpdateOperation(second.Revision, running)
	if err != nil {
		t.Fatal(err)
	}
	unknown := runningStored.Record
	unknown.Status = protocolstate.OperationReconciling
	unknown.Steps[0].Status = protocolstate.StepOutcomeUnknown
	unknownStored, err := operationStore.UpdateOperation(runningStored.Revision, unknown)
	if err != nil {
		t.Fatal(err)
	}
	needsOperator := unknownStored.Record
	needsOperator.Status = protocolstate.OperationNeedsOperator
	needsOperatorStored, err := operationStore.UpdateOperation(unknownStored.Revision, needsOperator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := operationStore.GetResourceLock(LogicalServerLockScope("staging", "server-1")); err != nil {
		t.Fatalf("suspended operation lost its resource lock: %v", err)
	}
	if _, _, err := service.Accept(context.Background(), "actor", "key-three", testOperation()); !errors.Is(err, store.ErrResourceConflict) {
		t.Fatalf("operation bypassed suspended lock: got %v, want ErrResourceConflict", err)
	}
	if needsOperatorStored.Record.Status != protocolstate.OperationNeedsOperator {
		t.Fatalf("unexpected suspended operation: %#v", needsOperatorStored.Record)
	}
}

func TestKeyRotationReplaysOldKeysAndFailsClosedWhenVersionMissing(t *testing.T) {
	operationStore, _ := openStore(t)
	oldService := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	first, _, err := oldService.Accept(context.Background(), "actor", "key-one", testOperation())
	if err != nil {
		t.Fatal(err)
	}
	rotated := newTestService(t, operationStore, testKeyring(t, "v2", map[string]byte{"v1": 1, "v2": 2}))
	replay, replayed, err := rotated.Accept(context.Background(), "actor", "key-one", testOperation())
	if err != nil || !replayed || replay.Record.OperationID != first.Record.OperationID {
		t.Fatalf("rotated keyring failed to replay old association: replayed=%t err=%v", replayed, err)
	}

	missingOldKey := newTestService(t, operationStore, testKeyring(t, "v2", map[string]byte{"v2": 2}))
	if _, _, err := missingOldKey.Accept(context.Background(), "actor", "new-key", testOperation()); !errors.Is(err, store.ErrKeyVersionMissing) {
		t.Fatalf("missing historical verifier must fail closed: got %v", err)
	}
}

func TestConcurrentDuplicateAcceptanceReturnsOneOperation(t *testing.T) {
	operationStore, _ := openStore(t)
	service := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	const callers = 8
	results := make([]store.StoredOperation, callers)
	replayed := make([]bool, callers)
	errorsSeen := make([]error, callers)
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], replayed[index], errorsSeen[index] = service.Accept(context.Background(), "actor", "same-key", testOperation())
		}(i)
	}
	wait.Wait()
	operationID := results[0].Record.OperationID
	if operationID == "" {
		t.Fatal("no operation was accepted")
	}
	createdCount := 0
	for i := range results {
		if errorsSeen[i] != nil {
			t.Fatalf("concurrent acceptance %d: %v", i, errorsSeen[i])
		}
		if results[i].Record.OperationID != operationID {
			t.Fatalf("concurrent retry returned another operation: %q != %q", results[i].Record.OperationID, operationID)
		}
		if !replayed[i] {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("got %d first acceptances, want exactly 1", createdCount)
	}
}

func TestResourceLockSurvivesStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.db")
	operationStore, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	accepted, _, err := service.Accept(context.Background(), "actor", "key-one", testOperation())
	if err != nil {
		t.Fatal(err)
	}
	if err := operationStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	lock, err := reopened.GetResourceLock(LogicalServerLockScope("staging", "server-1"))
	if err != nil || lock.OperationID != accepted.Record.OperationID {
		t.Fatalf("lock did not survive restart: lock=%#v err=%v", lock, err)
	}
	service = newTestService(t, reopened, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	if _, _, err := service.Accept(context.Background(), "actor", "key-two", testOperation()); !errors.Is(err, store.ErrResourceConflict) {
		t.Fatalf("restart allowed a conflicting operation: got %v", err)
	}
}

func TestLogicalServerLockScopeIsUnambiguous(t *testing.T) {
	first := LogicalServerLockScope("a/b", "c")
	second := LogicalServerLockScope("a", "b/c")
	if first == second || strings.Contains(first, "\x00") {
		t.Fatalf("unexpected lock scope encoding: first=%q second=%q", first, second)
	}
}
