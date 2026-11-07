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

type authorizerFunc func(context.Context, string, store.OperationRecord) error

func (f authorizerFunc) AuthorizeOperation(ctx context.Context, actorID string, record store.OperationRecord) error {
	return f(ctx, actorID, record)
}

func testPlanner() OperationPlanner {
	return OperationPlanFunc(func(_ context.Context, record store.OperationRecord) ([]store.OperationStep, error) {
		if record.OperationType == "server.define" {
			return nil, nil
		}
		return []store.OperationStep{{StepID: "planned-step", StepType: "hosting.instance.create"}}, nil
	})
}

func newTestService(t *testing.T, operationStore *store.BoltStore, ring *Keyring) *Service {
	t.Helper()
	service, err := NewService(operationStore, allowValidator{}, allowAuthorizer{}, LogicalServerLockPolicy{}, testPlanner(), ring)
	if err != nil {
		t.Fatal(err)
	}
	seedDefinedServer(t, operationStore, service, "server-1")
	return service
}

func seedDefinedServer(t *testing.T, operationStore *store.BoltStore, service *Service, serverID string) {
	t.Helper()
	if _, err := operationStore.GetLogicalServer("staging", serverID); err == nil {
		return
	} else if !errors.Is(err, store.ErrLogicalServerNotFound) {
		t.Fatalf("check test logical server: %v", err)
	}
	definition := store.OperationRecord{
		OperationType:   "server.define",
		ProtocolVersion: "0.21.0-draft",
		Target:          store.OperationTarget{ServerID: serverID, EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{
			OperationType: "server.define",
			Parameters:    json.RawMessage(`{"server_id":"` + serverID + `","environment_id":"staging","display_name":"Test Server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`),
		},
	}
	accepted, _, err := service.Accept(context.Background(), "seed-actor", "seed-"+serverID, definition)
	if err != nil {
		t.Fatalf("accept test server definition: %v", err)
	}
	running := accepted.Record
	running.Status = protocolstate.OperationRunning
	runningStored, err := operationStore.UpdateOperation(accepted.Revision, running)
	if err != nil {
		t.Fatalf("start test server definition: %v", err)
	}
	succeeded := runningStored.Record
	succeeded.Status = protocolstate.OperationSucceeded
	succeeded.Result = json.RawMessage(`{}`)
	if _, err := operationStore.UpdateOperation(runningStored.Revision, succeeded); err != nil {
		t.Fatalf("complete test server definition: %v", err)
	}
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
	seedDefinedServer(t, operationStore, service, "server-2")
	firstRequest := testOperation()
	firstRequest.Target.InstanceRef = json.RawMessage(`{"external_id":"instance-1","resource_kind":"instance"}`)

	first, replayed, err := service.Accept(context.Background(), "trusted-actor", "request-key", firstRequest)
	if err != nil || replayed {
		t.Fatalf("first acceptance: replayed=%t err=%v", replayed, err)
	}
	if first.Record.ActorID != "trusted-actor" || len(first.Record.OperationID) != 32 || first.Record.RequestFingerprint == "" {
		t.Fatalf("acceptance did not assign trusted identity and fingerprint: %#v", first.Record)
	}
	if len(first.Record.Steps) != 1 || first.Record.Steps[0].StepID != "planned-step" {
		t.Fatalf("acceptance did not persist the trusted planner output: %#v", first.Record.Steps)
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

func TestDefineAtomicallyCreatesDurableLogicalServer(t *testing.T) {
	operationStore, _ := openStore(t)
	service := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	definition := store.OperationRecord{
		OperationType:   "server.define",
		ProtocolVersion: "0.21.0-draft",
		Target:          store.OperationTarget{ServerID: "server-new", EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{
			OperationType: "server.define",
			Parameters:    json.RawMessage(`{"server_id":"server-new","environment_id":"staging","display_name":"Test Server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`),
		},
	}
	accepted, replayed, err := service.Accept(context.Background(), "trusted-actor", "define-key", definition)
	if err != nil || replayed {
		t.Fatalf("accept definition: replayed=%t err=%v", replayed, err)
	}
	server, err := operationStore.GetLogicalServer("staging", "server-new")
	if err != nil {
		t.Fatalf("read created logical server: %v", err)
	}
	if server.Revision != 1 || server.Record.LifecycleState != protocolstate.ServerUnprovisioned || server.Record.DataGenerationStatus != "unknown" || server.Record.LastOperationID != accepted.Record.OperationID {
		t.Fatalf("initial server record is incomplete: %#v", server)
	}
	if server.Record.HostingProfile != "vultr-small" || server.Record.StorageProfile != "wasabi-worlds" || server.Record.GameAdapterID != "game.paper" || server.Record.ExecutionAdapterID != "execution.systemd" {
		t.Fatalf("server definition was not persisted: %#v", server.Record)
	}

	_, replayed, err = service.Accept(context.Background(), "trusted-actor", "define-key", definition)
	if err != nil || !replayed {
		t.Fatalf("idempotent definition replay: replayed=%t err=%v", replayed, err)
	}
	servers, err := operationStore.ListLogicalServers()
	if err != nil || len(servers) != 2 {
		t.Fatalf("definition replay created duplicate servers: servers=%#v err=%v", servers, err)
	}
	_, replayed, err = service.Accept(context.Background(), "trusted-actor", "another-define-key", definition)
	if !errors.Is(err, store.ErrLogicalServerAlreadyExists) || replayed {
		t.Fatalf("second definition for the same server: replayed=%t err=%v", replayed, err)
	}
	operations, err := operationStore.ListRecoverableOperations()
	if err != nil || len(operations) != 1 {
		t.Fatalf("rejected duplicate definition left an operation: operations=%#v err=%v", operations, err)
	}
}

func TestDefineRejectsCallerWorkflowStepsWithoutPartialCreate(t *testing.T) {
	operationStore, _ := openStore(t)
	service := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	definition := store.OperationRecord{
		OperationType:   "server.define",
		ProtocolVersion: "0.21.0-draft",
		Target:          store.OperationTarget{ServerID: "server-new", EnvironmentID: "staging"},
		RequestSnapshot: store.RequestSnapshot{
			OperationType: "server.define",
			Parameters:    json.RawMessage(`{"server_id":"server-new","environment_id":"staging","display_name":"Test Server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`),
		},
		Steps: []store.OperationStep{{StepID: "untrusted", StepType: "hosting.instance.create", Status: protocolstate.StepPending}},
	}
	if _, _, err := service.Accept(context.Background(), "trusted-actor", "define-key", definition); !errors.Is(err, ErrCallerSuppliedPlan) {
		t.Fatalf("define with caller-supplied steps: got %v, want ErrCallerSuppliedPlan", err)
	}
	if _, err := operationStore.GetLogicalServer("staging", "server-new"); !errors.Is(err, store.ErrLogicalServerNotFound) {
		t.Fatalf("rejected definition created a logical server: got %v", err)
	}
	operations, err := operationStore.ListRecoverableOperations()
	if err != nil || len(operations) != 0 {
		t.Fatalf("rejected definition created an operation: operations=%#v err=%v", operations, err)
	}
}

func TestAcceptRejectsCallerSuppliedStepsForLifecycleOperation(t *testing.T) {
	operationStore, _ := openStore(t)
	service := newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	request := testOperation()
	request.Steps = []store.OperationStep{{StepID: "caller-step", StepType: "hosting.instance.destroy", Status: protocolstate.StepPending}}
	if _, _, err := service.Accept(context.Background(), "actor", "key", request); !errors.Is(err, ErrCallerSuppliedPlan) {
		t.Fatalf("caller-supplied lifecycle plan: got %v, want ErrCallerSuppliedPlan", err)
	}
	operations, err := operationStore.ListRecoverableOperations()
	if err != nil || len(operations) != 0 {
		t.Fatalf("rejected caller plan was persisted: operations=%#v err=%v", operations, err)
	}
}

func TestAcceptRejectsMissingTrustedLifecyclePlan(t *testing.T) {
	operationStore, _ := openStore(t)
	newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	planner := OperationPlanFunc(func(context.Context, store.OperationRecord) ([]store.OperationStep, error) { return nil, nil })
	service, err := NewService(operationStore, allowValidator{}, allowAuthorizer{}, LogicalServerLockPolicy{}, planner, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Accept(context.Background(), "actor", "key", testOperation()); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("empty server.provision plan: got %v, want ErrInvalidPlan", err)
	}
	operations, err := operationStore.ListRecoverableOperations()
	if err != nil || len(operations) != 0 {
		t.Fatalf("request without a plan was persisted: operations=%#v err=%v", operations, err)
	}
}

func TestAuthorizationReceivesTrustedPlanAndAuthenticatedActor(t *testing.T) {
	operationStore, _ := openStore(t)
	newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	planner := OperationPlanFunc(func(_ context.Context, record store.OperationRecord) ([]store.OperationStep, error) {
		if record.ActorID != "verified-actor" || record.OperationID == "" {
			return nil, errors.New("planner did not receive trusted operation identity")
		}
		return []store.OperationStep{{StepID: "safe-plan", StepType: "hosting.instance.inspect"}}, nil
	})
	authorizer := authorizerFunc(func(_ context.Context, actorID string, record store.OperationRecord) error {
		if actorID != "verified-actor" || record.ActorID != actorID || len(record.Steps) != 1 || record.Steps[0].StepID != "safe-plan" {
			return errors.New("authorizer did not receive the trusted plan and actor")
		}
		return nil
	})
	service, err := NewService(operationStore, allowValidator{}, authorizer, LogicalServerLockPolicy{}, planner, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	if err != nil {
		t.Fatal(err)
	}
	request := testOperation()
	request.ActorID = "spoofed-body-actor"
	accepted, _, err := service.Accept(context.Background(), "verified-actor", "key", request)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Record.ActorID != "verified-actor" || accepted.Record.Steps[0].StepID != "safe-plan" {
		t.Fatalf("accepted record does not bind trusted actor and plan: %#v", accepted.Record)
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
	newTestService(t, operationStore, testKeyring(t, "v1", map[string]byte{"v1": 1}))
	authorizationErr := errors.New("approval required")
	service, err := NewService(operationStore, allowValidator{}, rejectingAuthorizer{err: authorizationErr}, LogicalServerLockPolicy{}, testPlanner(), testKeyring(t, "v1", map[string]byte{"v1": 1}))
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
