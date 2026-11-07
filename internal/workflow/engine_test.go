package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	testOperationType = "test.workflow"
	testAdapterID     = "hosting.fake"
	testCapability    = "hosting.fake.create"
	testExternalRef   = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"instance-1"}`
)

func TestRunPersistsAdapterOutputAndCompletes(t *testing.T) {
	fixture := newEngineFixture(t, "run-success", "1", []fakeadapter.Reply{{
		Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ObservedAt: time.Now().UTC(),
	}})
	stored, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("operation status = %q, want succeeded", stored.Record.Status)
	}
	if got := string(stored.Record.Steps[0].Output); got != testExternalRef {
		t.Fatalf("persisted step output = %s, want %s", got, testExternalRef)
	}
	if len(fixture.adapter.Calls()) != 1 {
		t.Fatalf("adapter calls = %d, want 1", len(fixture.adapter.Calls()))
	}
}

func TestServerDefineCompletesWithTypedResultWithoutAdapterDispatch(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	adapterRegistry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operationStore.Close() })
	parameters := json.RawMessage(`{"server_id":"server-define","environment_id":"staging","display_name":"Test Server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`)
	record := store.OperationRecord{
		OperationID: "define-operation", OperationType: "server.define", ProtocolVersion: schemas.ProtocolVersion(),
		Target:  store.OperationTarget{ServerID: "server-define", EnvironmentID: "staging"},
		ActorID: "operator-1", IdempotencyKey: "define-key",
		RequestFingerprint: "sha256:" + strings.Repeat("a", 64),
		RequestSnapshot:    store.RequestSnapshot{OperationType: "server.define", Parameters: parameters},
	}
	proof := store.IdempotencyProof{
		ActiveKeyVersion: "test-v1",
		Candidates:       []store.IdempotencyVerifier{{KeyVersion: "test-v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}},
	}
	accepted, _, err := operationStore.AcceptOperation(record, proof, []string{store.LogicalServerLockScope("staging", "server-define")})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := NewServerDefineDefinition(operationStore)
	if err != nil {
		t.Fatal(err)
	}
	definitions := NewDefinitions()
	if err := definitions.Register("server.define", definition); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(operationStore, adapterRegistry, schemas, definitions, &testResumeAuthorizer{}, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := engine.Run(context.Background(), accepted.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("server.define status = %q, want succeeded", completed.Record.Status)
	}
	if err := schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-define-result", completed.Record.Result); err != nil {
		t.Fatalf("server.define result is not typed: %v (%s)", err, completed.Record.Result)
	}
	server, err := operationStore.GetLogicalServer("staging", "server-define")
	if err != nil || server.Record.LifecycleState != protocolstate.ServerUnprovisioned {
		t.Fatalf("server.define changed lifecycle unexpectedly: server=%#v err=%v", server, err)
	}
	if _, err := operationStore.GetResourceLock(store.LogicalServerLockScope("staging", "server-define")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("completed definition retained its lock: %v", err)
	}
}

func TestEngineCommitsProjectedLifecycleStateWithOperationProgress(t *testing.T) {
	fixture := newLifecycleEngineFixture(t)
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("operation status = %q, want succeeded", completed.Record.Status)
	}
	server, err := fixture.store.GetLogicalServer("staging", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if server.Record.LifecycleState != protocolstate.ServerPreparing || len(server.Record.LifecycleEvents) != 2 {
		t.Fatalf("projected lifecycle state/history = %q/%d, want preparing/2", server.Record.LifecycleState, len(server.Record.LifecycleEvents))
	}
	lastEvent := server.Record.LifecycleEvents[1]
	if lastEvent.OperationID != completed.Record.OperationID || lastEvent.ToState != protocolstate.ServerPreparing ||
		len(lastEvent.EvidenceStepIDs) != 1 || lastEvent.EvidenceStepIDs[0] != completed.Record.Steps[0].StepID {
		t.Fatalf("completed operation evidence was not linked to the lifecycle event: %#v", lastEvent)
	}
	if server.Record.LastOperationID != completed.Record.OperationID {
		t.Fatalf("server last operation = %q, want %q", server.Record.LastOperationID, completed.Record.OperationID)
	}
}

func TestLifecycleProjectorCannotChangeServerWithoutTransition(t *testing.T) {
	fixture := newLifecycleEngineFixture(t)
	fixture.definition.mutateWithoutTransition = true
	if _, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID); err == nil {
		t.Fatal("projector mutation without a lifecycle transition was accepted")
	}
	operation, err := fixture.store.GetOperation(fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := fixture.store.GetLogicalServer("staging", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if operation.Record.Status != protocolstate.OperationPending || server.Record.DisplayName != "Test Server" || server.Record.LifecycleState != protocolstate.ServerUnprovisioned {
		t.Fatalf("invalid projection changed durable state: operation=%#v server=%#v", operation, server)
	}
}

func TestPreconditionFailureSuspendsBeforeAdapterCall(t *testing.T) {
	fixture := newEngineFixture(t, "precondition", "1", nil)
	fixture.definition.preconditionErr = errors.New("approval is missing")
	stored, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationNeedsOperator {
		t.Fatalf("operation status = %q, want needs_operator", stored.Record.Status)
	}
	if stored.Record.Steps[0].Attempt != 0 || stored.Record.Steps[0].Status != protocolstate.StepPending {
		t.Fatalf("precondition failure claimed the step: %#v", stored.Record.Steps[0])
	}
	if len(fixture.adapter.Calls()) != 0 {
		t.Fatal("adapter was called despite failed preconditions")
	}
}

func TestAcceptedAdapterStepWaitsAndCompletesThroughReconciliation(t *testing.T) {
	fixture := newEngineFixture(t, "accepted-reconciliation", "1", []fakeadapter.Reply{{
		Outcome: adapter.OutcomeAccepted, Output: json.RawMessage(testExternalRef),
		ExternalOperationRef: json.RawMessage(testExternalRef), ObservedAt: time.Now().UTC(),
	}})
	stored, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationWaiting || stored.Record.Steps[0].Status != protocolstate.StepWaiting {
		t.Fatalf("accepted adapter state = %q/%q, want waiting/waiting", stored.Record.Status, stored.Record.Steps[0].Status)
	}
	fixture.definition.resolution = Resolution{
		State: ResolutionCompleted, Output: json.RawMessage(testExternalRef),
		ExternalOperationRef: json.RawMessage(testExternalRef),
	}
	stored, err = fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationSucceeded || stored.Record.Steps[0].Status != protocolstate.StepSucceeded {
		t.Fatalf("reconciled operation state = %q/%q, want succeeded/succeeded", stored.Record.Status, stored.Record.Steps[0].Status)
	}
	if len(fixture.adapter.Calls()) != 1 {
		t.Fatalf("reconciliation redispatched the adapter; calls=%d", len(fixture.adapter.Calls()))
	}
}

func TestAmbiguousDispatchReconcilesWithoutImmediateRetry(t *testing.T) {
	fixture := newEngineFixture(t, "ambiguous", "1", []fakeadapter.Reply{{Err: errors.New("connection closed")}})
	fixture.definition.resolution = Resolution{State: ResolutionNeedsOperator}
	stored, err := fixture.engine.Advance(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationReconciling || stored.Record.Steps[0].Status != protocolstate.StepOutcomeUnknown {
		t.Fatalf("ambiguous dispatch state = %q/%q, want reconciling/outcome_unknown", stored.Record.Status, stored.Record.Steps[0].Status)
	}
	if len(fixture.adapter.Calls()) != 1 {
		t.Fatalf("first advancement made %d adapter calls, want 1", len(fixture.adapter.Calls()))
	}
	stored, err = fixture.engine.Advance(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationNeedsOperator {
		t.Fatalf("unresolved operation status = %q, want needs_operator", stored.Record.Status)
	}
	if len(fixture.adapter.Calls()) != 1 {
		t.Fatalf("reconciliation blindly retried the adapter; calls=%d", len(fixture.adapter.Calls()))
	}
}

func TestReconciliationIsBoundedAndLeavesEffectForOperator(t *testing.T) {
	fixture := newEngineFixture(t, "bounded-reconciliation", "1", []fakeadapter.Reply{{Err: errors.New("connection closed")}})
	fixture.engine.stepTimeout = 10 * time.Millisecond
	if _, err := fixture.engine.Advance(context.Background(), fixture.operation.Record.OperationID); err != nil {
		t.Fatal(err)
	}
	fixture.definition.reconcileFn = func(ctx context.Context, _ store.OperationRecord, _ store.OperationStep) (Resolution, error) {
		<-ctx.Done()
		return Resolution{}, ctx.Err()
	}
	stored, err := fixture.engine.Advance(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationNeedsOperator || stored.Record.Steps[0].Status != protocolstate.StepOutcomeUnknown {
		t.Fatalf("timed-out reconciliation state = %q/%q", stored.Record.Status, stored.Record.Steps[0].Status)
	}
	if len(fixture.adapter.Calls()) != 1 {
		t.Fatalf("reconciliation timeout caused another adapter call; calls=%d", len(fixture.adapter.Calls()))
	}
}

func TestRecoverMarksInterruptedStepUnknownWithoutDispatch(t *testing.T) {
	fixture := newEngineFixture(t, "recover", "1", nil)
	running := fixture.operation.Record
	running.Status = protocolstate.OperationRunning
	running.Steps[0].Status = protocolstate.StepRunning
	running.Steps[0].Attempt = 1
	currentStep := running.Steps[0].StepID
	running.CurrentStepID = &currentStep
	if _, err := fixture.store.UpdateOperation(fixture.operation.Revision, running); err != nil {
		t.Fatal(err)
	}

	recovered, err := fixture.engine.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].Record.Status != protocolstate.OperationReconciling || recovered[0].Record.Steps[0].Status != protocolstate.StepOutcomeUnknown {
		t.Fatalf("unexpected recovery result: %#v", recovered)
	}
	if len(fixture.adapter.Calls()) != 0 {
		t.Fatal("recovery invoked an adapter")
	}
}

func TestUnavailableExactCapabilitySuspendsWithoutDispatch(t *testing.T) {
	fixture := newEngineFixture(t, "capability-version", "2", nil)
	stored, err := fixture.engine.Advance(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationNeedsOperator {
		t.Fatalf("operation status = %q, want needs_operator", stored.Record.Status)
	}
	if len(fixture.adapter.Calls()) != 0 {
		t.Fatal("unavailable capability version reached the adapter")
	}
}

func TestResumeRequiresAuthorizationAndStartsAtReconciliation(t *testing.T) {
	fixture := newEngineFixture(t, "resume", "1", nil)
	pending := fixture.operation.Record
	pending.Status = protocolstate.OperationRunning
	running, err := fixture.store.UpdateOperation(fixture.operation.Revision, pending)
	if err != nil {
		t.Fatal(err)
	}
	needsOperator := running.Record
	needsOperator.Status = protocolstate.OperationNeedsOperator
	needsOperator.Error = protocolError("core.safety.precondition_unmet", "safety_blocked", "Operator review is required.", needsOperator.OperationID, false, "operator_review", true)
	if _, err := fixture.store.UpdateOperation(running.Revision, needsOperator); err != nil {
		t.Fatal(err)
	}

	stored, err := fixture.engine.Resume(context.Background(), "operator-1", fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationReconciling {
		t.Fatalf("resumed status = %q, want reconciling", stored.Record.Status)
	}
	if fixture.authorizer.operatorID != "operator-1" {
		t.Fatalf("authorizer saw operator %q", fixture.authorizer.operatorID)
	}
	if len(fixture.adapter.Calls()) != 0 {
		t.Fatal("resume dispatched before reconciliation")
	}
}

func TestSafeRetryLimitPreventsAnotherAdapterAttempt(t *testing.T) {
	fixture := newEngineFixture(t, "retry-limit", "1", []fakeadapter.Reply{{Err: errors.New("connection closed")}})
	fixture.engine.maxStepAttempts = 1
	fixture.definition.resolution = Resolution{State: ResolutionSafeToRetry}
	if _, err := fixture.engine.Advance(context.Background(), fixture.operation.Record.OperationID); err != nil {
		t.Fatal(err)
	}
	stored, err := fixture.engine.Advance(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationNeedsOperator {
		t.Fatalf("operation status = %q, want needs_operator", stored.Record.Status)
	}
	if len(fixture.adapter.Calls()) != 1 {
		t.Fatalf("retry limit allowed %d adapter calls, want 1", len(fixture.adapter.Calls()))
	}
}

func TestDefinitiveAdapterFailureTerminatesOperation(t *testing.T) {
	fixture := newEngineFixtureWithMutating(t, "definitive-failure", "1", []fakeadapter.Reply{{Err: errors.New("provider rejected request")}}, false)
	stored, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationFailed || stored.Record.Steps[0].Status != protocolstate.StepFailed {
		t.Fatalf("definitive adapter failure left operation in %q/%q", stored.Record.Status, stored.Record.Steps[0].Status)
	}
}

type engineFixture struct {
	store      *store.BoltStore
	engine     *Engine
	adapter    *fakeadapter.Scripted
	definition *testDefinition
	authorizer *testResumeAuthorizer
	operation  store.StoredOperation
}

func newEngineFixture(t *testing.T, operationID, capabilityVersion string, replies []fakeadapter.Reply) engineFixture {
	return newEngineFixtureWithMutating(t, operationID, capabilityVersion, replies, true)
}

func newEngineFixtureWithMutating(t *testing.T, operationID, capabilityVersion string, replies []fakeadapter.Reply, mutating bool) engineFixture {
	return newEngineFixtureForOperation(t, operationID, capabilityVersion, replies, mutating, testOperationType, false)
}

func newLifecycleEngineFixture(t *testing.T) engineFixture {
	return newEngineFixtureForOperation(t, "lifecycle-projection", "1", []fakeadapter.Reply{{
		Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ObservedAt: time.Now().UTC(),
	}}, true, "server.workflowtest", true)
}

func newEngineFixtureForOperation(t *testing.T, operationID, capabilityVersion string, replies []fakeadapter.Reply, mutating bool, operationType string, projectLifecycle bool) engineFixture {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	adapterRegistry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	scripted := fakeadapter.New(adapter.Descriptor{
		AdapterID: testAdapterID, Category: adapter.CategoryHosting,
		DisplayName: "workflow test adapter", ImplementationVersion: "test-1",
		ProtocolVersions: []string{schemas.ProtocolVersion()}, ContractVersions: []string{"1"},
		RequiredPermissions: []string{}, Capabilities: []adapter.Capability{{
			Name: testCapability, Version: "1", Mutating: mutating,
			InputSchemaURI:      "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":common:external-resource-ref",
			OutputSchemaURI:     "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":common:external-resource-ref",
			RequiredPermissions: []string{},
		}},
	})
	if len(replies) == 0 {
		replies = []fakeadapter.Reply{{Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ObservedAt: time.Now().UTC()}}
	}
	if err := scripted.SetScript(testCapability, replies...); err != nil {
		t.Fatal(err)
	}
	if err := adapterRegistry.Register(scripted); err != nil {
		t.Fatal(err)
	}
	definition := &testDefinition{resolution: Resolution{State: ResolutionNeedsOperator}, operationType: operationType}
	var registeredDefinition Definition = definition
	if projectLifecycle {
		registeredDefinition = &projectingTestDefinition{testDefinition: definition}
	}
	definitions := NewDefinitions()
	if err := definitions.Register(operationType, registeredDefinition); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "controller.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	operationStore, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operationStore.Close() })
	if projectLifecycle {
		serverDefinition, err := NewServerDefineDefinition(operationStore)
		if err != nil {
			t.Fatal(err)
		}
		if err := definitions.Register("server.define", serverDefinition); err != nil {
			t.Fatal(err)
		}
	}
	authorizer := &testResumeAuthorizer{}
	engine, err := NewEngine(operationStore, adapterRegistry, schemas, definitions, authorizer, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	if projectLifecycle {
		parameters := json.RawMessage(`{"server_id":"server-1","environment_id":"staging","display_name":"Test Server","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`)
		defineRecord := store.OperationRecord{
			OperationID: "lifecycle-test-definition", OperationType: "server.define", ProtocolVersion: schemas.ProtocolVersion(),
			Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"}, ActorID: "operator-1", IdempotencyKey: "define-key",
			RequestFingerprint: "sha256:" + strings.Repeat("d", 64),
			RequestSnapshot:    store.RequestSnapshot{OperationType: "server.define", Parameters: parameters},
		}
		proof := testProof(3)
		acceptedDefine, _, err := operationStore.AcceptOperation(defineRecord, proof, []string{store.LogicalServerLockScope("staging", "server-1")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := engine.Run(context.Background(), acceptedDefine.Record.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	record := store.OperationRecord{
		OperationID: operationID, OperationType: operationType,
		ProtocolVersion: schemas.ProtocolVersion(), Target: store.OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		ActorID: "operator-1", IdempotencyKey: "sha256:" + strings.Repeat("1", 64),
		RequestFingerprint: "sha256:" + strings.Repeat("a", 64),
		RequestSnapshot:    store.RequestSnapshot{OperationType: operationType, Parameters: json.RawMessage(`{}`)},
		Steps: []store.OperationStep{{
			StepID: "step-1", StepType: "hosting.instance.create", Status: protocolstate.StepPending,
			AdapterID: testAdapterID, CapabilityName: testCapability, CapabilityVersion: capabilityVersion,
			Input: json.RawMessage(testExternalRef),
		}},
	}
	lockScopes := []string(nil)
	if strings.HasPrefix(operationType, "server.") {
		lockScopes = []string{store.LogicalServerLockScope("staging", "server-1")}
	}
	accepted, _, err := operationStore.AcceptOperation(record, testProof(1), lockScopes)
	if err != nil {
		t.Fatal(err)
	}
	return engineFixture{store: operationStore, engine: engine, adapter: scripted, definition: definition, authorizer: authorizer, operation: accepted}
}

func testProof(value byte) store.IdempotencyProof {
	return store.IdempotencyProof{
		ActiveKeyVersion: "test-v1",
		Candidates: []store.IdempotencyVerifier{{
			KeyVersion: "test-v1", Digest: bytes.Repeat([]byte{value}, 32), BindingDigest: bytes.Repeat([]byte{value + 10}, 32),
		}},
	}
}

type testDefinition struct {
	preconditionErr         error
	resolution              Resolution
	reconcileFn             func(context.Context, store.OperationRecord, store.OperationStep) (Resolution, error)
	operationType           string
	mutateWithoutTransition bool
}

func (d *testDefinition) CheckPreconditions(context.Context, store.OperationRecord, store.OperationStep) error {
	return d.preconditionErr
}

func (d *testDefinition) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if d.reconcileFn != nil {
		return d.reconcileFn(ctx, operation, step)
	}
	return d.resolution, nil
}

func (d *testDefinition) BuildSuccessResult(context.Context, store.OperationRecord) (json.RawMessage, error) {
	return genericResult(d.operationType), nil
}

func (d *testDefinition) BuildFailureResult(context.Context, store.OperationRecord, store.OperationStep, json.RawMessage) (json.RawMessage, error) {
	return genericResult(d.operationType), nil
}

type projectingTestDefinition struct {
	*testDefinition
}

func (d *projectingTestDefinition) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	next := current.Record
	if d.mutateWithoutTransition {
		next.DisplayName = "unexpected mutation"
		return next, false, nil
	}
	switch {
	case operation.Status == protocolstate.OperationRunning && current.Record.LifecycleState == protocolstate.ServerUnprovisioned:
		next.LifecycleState = protocolstate.ServerProvisioning
	case operation.Status == protocolstate.OperationSucceeded && current.Record.LifecycleState == protocolstate.ServerProvisioning:
		next.LifecycleState = protocolstate.ServerPreparing
	default:
		return next, false, nil
	}
	return next, true, nil
}

type testResumeAuthorizer struct {
	operatorID string
}

func (a *testResumeAuthorizer) AuthorizeResume(_ context.Context, operatorID string, _ store.OperationRecord) error {
	a.operatorID = operatorID
	return nil
}

func genericResult(operationType string) json.RawMessage {
	return json.RawMessage(`{"outcome_summary":"completed","operation_type":"` + operationType + `","affected_resources":[],"completed_steps":[],"failed_steps":[],"warnings":[],"unresolved_conditions":[],"audit_event_ids":[],"partial":false}`)
}
