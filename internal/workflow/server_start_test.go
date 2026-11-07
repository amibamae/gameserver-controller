package workflow

import (
	"context"
	"encoding/json"
	"fmt"
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
	startTestServerID = "start-test"
	startTestEnvID    = "staging"
	startTestRelease  = "paper-1.21.11-build-3"
	startTestSetID    = "game.paper:paper-1.21.11:checks-v1"
	startTestInstance = `{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"vm-start-test"}`
	startTestUnit     = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"paper-start-test"}`
)

func TestServerStartRunsDurablePlanAndProjectsReady(t *testing.T) {
	fixture := newServerStartFixture(t, true, adapter.OutcomeCompleted, "active")
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded || len(completed.Record.Steps) != 4 {
		t.Fatalf("operation status/steps = %s/%d, error=%s steps=%#v, want succeeded/4", completed.Record.Status, len(completed.Record.Steps), completed.Record.Error, completed.Record.Steps)
	}
	for _, step := range completed.Record.Steps {
		if step.Status != protocolstate.StepSucceeded || step.Attempt != 1 {
			t.Fatalf("step %q status/attempt = %s/%d, want succeeded/1", step.StepID, step.Status, step.Attempt)
		}
	}
	server, err := fixture.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerReady || server.Record.DesiredState != "running" {
		t.Fatalf("logical server state = %#v, err=%v; want ready/running", server.Record, err)
	}
	reservations, err := fixture.store.ListCapacityReservations()
	if err != nil || len(reservations) != 1 || reservations[0].ReadyAt.IsZero() || reservations[0].ReadyAt.Before(reservations[0].ProvisionedAt) {
		t.Fatalf("local provision-to-Paper-ready interval = %#v, err=%v", reservations, err)
	}
	if len(server.Record.LifecycleEvents) < 3 || server.Record.LifecycleEvents[len(server.Record.LifecycleEvents)-1].ToState != protocolstate.ServerReady ||
		server.Record.LifecycleEvents[len(server.Record.LifecycleEvents)-2].ToState != protocolstate.ServerValidating ||
		server.Record.LifecycleEvents[len(server.Record.LifecycleEvents)-3].ToState != protocolstate.ServerStarting {
		t.Fatalf("lifecycle event history = %#v", server.Record.LifecycleEvents)
	}
	if err := fixture.schemas.ValidateJSON("urn:gameserver-controller:"+fixture.schemas.ProtocolVersion()+":operations:server-start-result", completed.Record.Result); err != nil {
		t.Fatalf("server.start result is not typed: %v (%s)", err, completed.Record.Result)
	}
	if got := len(fixture.hosting.Calls()); got != 1 {
		t.Fatalf("provider inspection calls = %d, want 1", got)
	}
	if got := len(fixture.execution.Calls()); got != 2 {
		t.Fatalf("execution start and verification calls = %d, want 2", got)
	}
	if got := len(fixture.game.Calls()); got != 1 {
		t.Fatalf("game readiness calls = %d, want 1", got)
	}
}

func TestServerStartDoesNotDispatchRuntimeWhenProviderIsNotActive(t *testing.T) {
	fixture := newServerStartFixture(t, true, adapter.OutcomeCompleted, "stopped")
	stored, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Record.Status != protocolstate.OperationFailed || stored.Record.Steps[1].Status != protocolstate.StepPending {
		t.Fatalf("operation/step state = %s/%s, want failed/pending", stored.Record.Status, stored.Record.Steps[1].Status)
	}
	if len(fixture.execution.Calls()) != 0 || len(fixture.game.Calls()) != 0 {
		t.Fatal("runtime or game adapter was called while the provider instance was not active")
	}
	server, err := fixture.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerStopped {
		t.Fatalf("precondition failure changed lifecycle state: %s, %v", server.Record.LifecycleState, err)
	}
}

func TestServerStartInspectsAmbiguousStartBeforeContinuing(t *testing.T) {
	fixture := newServerStartFixture(t, true, adapter.OutcomeUnknown, "active")
	startedAt := time.Now().UTC().Format(time.RFC3339Nano)
	unknownStart := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"outcome":"outcome_unknown","state":"unknown","observed_at":%q,"external_operation_ref":{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"operation","external_id":"start-ambiguous"}}`, startTestUnit, startedAt))
	if err := fixture.execution.SetScript("execution.unit.start", fakeadapter.Reply{
		Outcome: adapter.OutcomeUnknown, Output: unknownStart,
		ExternalOperationRef: json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"operation","external_id":"start-ambiguous"}`),
		ObservedAt:           time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded || completed.Record.Steps[1].Attempt != 1 {
		t.Fatalf("reconciled start status/attempt = %s/%d", completed.Record.Status, completed.Record.Steps[1].Attempt)
	}
	if got := len(fixture.execution.Calls()); got != 3 {
		t.Fatalf("execution calls = %d, want start, reconciliation inspect, and planned verification", got)
	}
	if fixture.execution.Calls()[0].Params.Capability != "execution.unit.start" ||
		fixture.execution.Calls()[1].Params.Capability != "execution.unit.inspect" ||
		fixture.execution.Calls()[2].Params.Capability != "execution.unit.inspect" {
		t.Fatalf("ambiguous start was not inspected before continuing: %#v", fixture.execution.Calls())
	}
}

func TestServerStartFailsClosedWhenReadinessIsUnknown(t *testing.T) {
	fixture := newServerStartFixture(t, false, adapter.OutcomeCompleted, "active")
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationFailed || len(completed.Record.Result) == 0 {
		t.Fatalf("unknown readiness status/result = %s/%s", completed.Record.Status, completed.Record.Result)
	}
	server, err := fixture.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerDegraded {
		t.Fatalf("readiness failure lifecycle state = %s, %v; want degraded", server.Record.LifecycleState, err)
	}
	var result struct {
		Partial bool `json:"partial"`
	}
	if err := json.Unmarshal(completed.Record.Result, &result); err != nil || !result.Partial {
		t.Fatalf("failed start result should report a partial effect: %s, %v", completed.Record.Result, err)
	}
	reservations, err := fixture.store.ListCapacityReservations()
	if err != nil || len(reservations) != 1 || !reservations[0].ReadyAt.IsZero() {
		t.Fatalf("failed readiness recorded a completed boot sample: %#v, err=%v", reservations, err)
	}
}

func TestServerStartPlanRejectsProfileNotDeclaredByGameAdapter(t *testing.T) {
	fixture := newServerStartFixture(t, true, adapter.OutcomeCompleted, "active")
	fixture.profiles.profile.RequiredChecks[1].CheckVersion = "wrong"
	if _, err := fixture.workflow.BuildPlan(context.Background(), fixture.operation.Record); err == nil {
		t.Fatal("planner accepted readiness versions not declared by the selected game adapter")
	}
}

type staticStartProfileResolver struct{ profile ServerStartProfile }

func (r *staticStartProfileResolver) ResolveServerStart(context.Context, store.LogicalServer) (ServerStartProfile, error) {
	return r.profile, nil
}

type serverStartFixture struct {
	store     *store.BoltStore
	schemas   *schemaregistry.Registry
	registry  *adapter.Registry
	workflow  *ServerStartWorkflow
	engine    *Engine
	operation store.StoredOperation
	hosting   *fakeadapter.Scripted
	execution *fakeadapter.Scripted
	game      *fakeadapter.Scripted
	profiles  *staticStartProfileResolver
}

func newServerStartFixture(t *testing.T, allReadinessPass bool, startOutcome adapter.Outcome, providerState string) serverStartFixture {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	version := schemas.ProtocolVersion()
	hosting := fakeadapter.New(adapter.Descriptor{
		AdapterID: "hosting.fake", Category: adapter.CategoryHosting, DisplayName: "fake host", ImplementationVersion: "test-1",
		ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
		Capabilities: []adapter.Capability{{Name: "hosting.instance.inspect", Version: "1", Mutating: false,
			InputSchemaURI:  "urn:gameserver-controller:" + version + ":hosting:instance-inspect-input",
			OutputSchemaURI: "urn:gameserver-controller:" + version + ":hosting:instance-inspect-output", RequiredPermissions: []string{}}},
	})
	execution := fakeadapter.New(adapter.Descriptor{
		AdapterID: "execution.fake", Category: adapter.CategoryExecution, DisplayName: "fake runtime", ImplementationVersion: "test-1",
		ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
		Capabilities: []adapter.Capability{
			{Name: "execution.unit.start", Version: "1", Mutating: true,
				InputSchemaURI:  "urn:gameserver-controller:" + version + ":execution:systemd-unit-start-input",
				OutputSchemaURI: "urn:gameserver-controller:" + version + ":execution:systemd-unit-start-output", RequiredPermissions: []string{}},
			{Name: "execution.unit.graceful_stop", Version: "1", Mutating: true,
				InputSchemaURI:  "urn:gameserver-controller:" + version + ":execution:systemd-unit-graceful-stop-input",
				OutputSchemaURI: "urn:gameserver-controller:" + version + ":execution:systemd-unit-graceful-stop-output", RequiredPermissions: []string{}},
			{Name: "execution.unit.inspect", Version: "1", Mutating: false,
				InputSchemaURI:  "urn:gameserver-controller:" + version + ":execution:systemd-unit-inspect-input",
				OutputSchemaURI: "urn:gameserver-controller:" + version + ":execution:systemd-unit-inspect-output", RequiredPermissions: []string{}},
		},
	})
	checks := []ReadinessRequirement{
		{CheckID: "paper.startup-complete", CheckVersion: "1"},
		{CheckID: "paper.world-loaded", CheckVersion: "2"},
		{CheckID: "paper.players-joinable", CheckVersion: "1"},
	}
	checkDeclarations := make([]any, len(checks))
	for i, check := range checks {
		checkDeclarations[i] = map[string]any{"check_id": check.CheckID, "check_version": check.CheckVersion}
	}
	game := fakeadapter.New(adapter.Descriptor{
		AdapterID: "game.paper", Category: adapter.CategoryGame, DisplayName: "fake Paper", ImplementationVersion: "test-1",
		ProtocolVersions: []string{version}, ContractVersions: []string{"1"}, RequiredPermissions: []string{},
		Capabilities: []adapter.Capability{
			{Name: "game.readiness.check", Version: "1", Mutating: false,
				InputSchemaURI:  "urn:gameserver-controller:" + version + ":game:readiness-check-input",
				OutputSchemaURI: "urn:gameserver-controller:" + version + ":game:readiness-check-output", RequiredPermissions: []string{},
				Constraints: map[string]any{"readiness_check_sets": []any{map[string]any{
					"game_release_id": startTestRelease, "environment_ids": []string{startTestEnvID},
					"readiness_check_set_id": startTestSetID, "required_checks": checkDeclarations,
				}}}},
			{Name: "game.save.request", Version: "1", Mutating: true,
				InputSchemaURI:  "urn:gameserver-controller:" + version + ":game:save-request-input",
				OutputSchemaURI: "urn:gameserver-controller:" + version + ":game:save-request-output", RequiredPermissions: []string{}},
		},
	})
	for _, fake := range []*fakeadapter.Scripted{hosting, execution, game} {
		if err := registry.Register(fake); err != nil {
			t.Fatal(err)
		}
	}
	profileResolver := &staticStartProfileResolver{profile: ServerStartProfile{
		GameReleaseID: startTestRelease, UnitRef: json.RawMessage(startTestUnit),
		ReadinessCheckSetID: startTestSetID, RequiredChecks: checks,
	}}
	path := filepath.Join(t.TempDir(), "controller.db")
	operationStore, err := store.OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operationStore.Close() })
	workflow, err := NewServerStartWorkflow(operationStore, registry, schemas, profileResolver, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedStoppedStartTestServer(operationStore, schemas.ProtocolVersion()); err != nil {
		t.Fatal(err)
	}
	capacity, _, err := operationStore.ReserveCapacity(store.CapacityReservation{
		OperationID: "start-test-capacity", ServerID: startTestServerID, EnvironmentID: startTestEnvID,
		CapacityGroup: "shared_nonproduction", Provider: "vultr", ProviderAdapterID: "hosting.fake", ResourceKey: "gsc-start-test",
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	provisionedAt := time.Now().UTC()
	providerObservedAt := time.Now().UTC()
	capacity, err = operationStore.ObserveCapacity(capacity.OperationID, store.CapacityProviderObservation{
		Provider: capacity.Provider, ResourceKey: capacity.ResourceKey, ProviderInstanceID: "vm-start-test",
		State: "present", ObservedAt: providerObservedAt, ProvisionedAt: provisionedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	parameters := json.RawMessage(`{}`)
	operation := store.OperationRecord{
		OperationID: "start-operation", OperationType: "server.start", ProtocolVersion: version,
		Target:  store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID},
		ActorID: "operator-1", IdempotencyKey: "start-key",
		RequestFingerprint: "sha256:" + strings.Repeat("9", 64),
		RequestSnapshot:    store.RequestSnapshot{OperationType: "server.start", Parameters: parameters},
	}
	steps, err := workflow.BuildPlan(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.Steps = steps
	accepted, _, err := operationStore.AcceptOperation(operation, testProof(17), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		t.Fatal(err)
	}
	definitions := NewDefinitions()
	if err := definitions.Register("server.start", workflow); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(operationStore, registry, schemas, definitions, &testResumeAuthorizer{}, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	providerOutput := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"state":%q,"observed_at":%q}`, startTestInstance, providerState, observedAt))
	if err := hosting.SetScript("hosting.instance.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	unitOutput := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"running","observed_at":%q}`, startTestUnit, observedAt))
	startState := "completed"
	unitState := "running"
	if startOutcome == adapter.OutcomeUnknown {
		startState = "outcome_unknown"
		unitState = "unknown"
	}
	if err := execution.SetScript("execution.unit.start", fakeadapter.Reply{
		Outcome:    startOutcome,
		Output:     json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"outcome":%q,"state":%q,"observed_at":%q}`, startTestUnit, startState, unitState, observedAt)),
		ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	inspectionReplies := []fakeadapter.Reply{{Outcome: adapter.OutcomeCompleted, Output: unitOutput, ObservedAt: time.Now().UTC()}}
	if startOutcome == adapter.OutcomeUnknown {
		inspectionReplies = append([]fakeadapter.Reply{{Outcome: adapter.OutcomeCompleted, Output: unitOutput, ObservedAt: time.Now().UTC()}}, inspectionReplies...)
	}
	if err := execution.SetScript("execution.unit.inspect", inspectionReplies...); err != nil {
		t.Fatal(err)
	}
	readinessResults := []any{
		map[string]any{"check_id": checks[0].CheckID, "check_version": checks[0].CheckVersion, "result": "pass", "observed_at": observedAt},
		map[string]any{"check_id": checks[1].CheckID, "check_version": checks[1].CheckVersion, "result": "pass", "observed_at": observedAt},
		map[string]any{"check_id": checks[2].CheckID, "check_version": checks[2].CheckVersion, "result": "pass", "observed_at": observedAt},
	}
	if !allReadinessPass {
		readinessResults[2].(map[string]any)["result"] = "unknown"
		readinessResults[2].(map[string]any)["reason_code"] = "proxy_route_check_unavailable"
	}
	readinessOutput, err := json.Marshal(map[string]any{
		"server_id": startTestServerID, "environment_id": startTestEnvID, "instance_ref": json.RawMessage(startTestInstance),
		"game_release_id": startTestRelease, "observed_at": observedAt, "check_results": readinessResults,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := game.SetScript("game.readiness.check", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: readinessOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return serverStartFixture{
		store: operationStore, schemas: schemas, registry: registry, workflow: workflow, engine: engine, operation: accepted,
		hosting: hosting, execution: execution, game: game, profiles: profileResolver,
	}
}

func seedStoppedStartTestServer(operationStore *store.BoltStore, protocolVersion string) error {
	definitionParameters := json.RawMessage(`{"server_id":"start-test","environment_id":"staging","display_name":"Start Test","hosting_profile":"test-host","storage_profile":"test-storage","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.fake","profile":"paper-test"}}`)
	definition := store.OperationRecord{
		OperationID: "start-test-define", OperationType: "server.define", ProtocolVersion: protocolVersion,
		Target: store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID}, ActorID: "operator-1", IdempotencyKey: "define-test",
		RequestFingerprint: "sha256:" + strings.Repeat("1", 64),
		RequestSnapshot:    store.RequestSnapshot{OperationType: "server.define", Parameters: definitionParameters},
	}
	acceptedDefine, _, err := operationStore.AcceptOperation(definition, testProof(21), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		return err
	}
	acceptedDefine.Record.Status = protocolstate.OperationRunning
	acceptedDefine, err = operationStore.UpdateOperation(acceptedDefine.Revision, acceptedDefine.Record)
	if err != nil {
		return err
	}
	acceptedDefine.Record.Status = protocolstate.OperationSucceeded
	acceptedDefine.Record.Result = json.RawMessage(`{}`)
	if _, err := operationStore.UpdateOperation(acceptedDefine.Revision, acceptedDefine.Record); err != nil {
		return err
	}
	provision := store.OperationRecord{
		OperationID: "start-test-provision", OperationType: "server.provision", ProtocolVersion: protocolVersion,
		Target: store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID}, ActorID: "operator-1", IdempotencyKey: "provision-test",
		RequestFingerprint: "sha256:" + strings.Repeat("2", 64),
		RequestSnapshot:    store.RequestSnapshot{OperationType: "server.provision", Parameters: json.RawMessage(`{}`)},
		Steps:              []store.OperationStep{{StepID: "provision-placeholder", StepType: "hosting.instance.create", Status: protocolstate.StepPending, Input: json.RawMessage(`{}`)}},
	}
	acceptedProvision, _, err := operationStore.AcceptOperation(provision, testProof(22), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		return fmt.Errorf("accept seed provision: %w", err)
	}
	for _, state := range []protocolstate.ServerState{
		protocolstate.ServerProvisioning, protocolstate.ServerPreparing, protocolstate.ServerStarting,
		protocolstate.ServerValidating, protocolstate.ServerReady,
	} {
		currentServer, err := operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
		if err != nil {
			return err
		}
		currentOperation, err := operationStore.GetOperation(acceptedProvision.Record.OperationID)
		if err != nil {
			return err
		}
		if currentOperation.Record.Status == protocolstate.OperationPending {
			currentOperation.Record.Status = protocolstate.OperationRunning
		}
		nextServer := currentServer.Record
		nextServer.LifecycleState = state
		if state == protocolstate.ServerProvisioning {
			nextServer.InstanceRef = json.RawMessage(startTestInstance)
			nextServer.DesiredState = "running"
		}
		if _, _, err := operationStore.UpdateOperationAndLogicalServer(currentOperation.Revision, currentOperation.Record, currentServer.Revision, nextServer); err != nil {
			return fmt.Errorf("project seed provision state %s: %w", state, err)
		}
	}
	currentProvision, err := operationStore.GetOperation(acceptedProvision.Record.OperationID)
	if err != nil {
		return err
	}
	currentProvision.Record.Status = protocolstate.OperationFailed
	currentProvision.Record.Error = json.RawMessage(`{"code":"test.seed_complete"}`)
	if _, err := operationStore.UpdateOperation(currentProvision.Revision, currentProvision.Record); err != nil {
		return fmt.Errorf("finish seed provision: %w", err)
	}
	stop := store.OperationRecord{
		OperationID: "start-test-stop", OperationType: "server.stop", ProtocolVersion: protocolVersion,
		Target: store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID}, ActorID: "operator-1", IdempotencyKey: "stop-test",
		RequestFingerprint: "sha256:" + strings.Repeat("3", 64),
		RequestSnapshot:    store.RequestSnapshot{OperationType: "server.stop", Parameters: json.RawMessage(`{}`)},
		Steps:              []store.OperationStep{{StepID: "stop-placeholder", StepType: "execution.unit.stop", Status: protocolstate.StepPending, Input: json.RawMessage(`{}`)}},
	}
	acceptedStop, _, err := operationStore.AcceptOperation(stop, testProof(23), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		return fmt.Errorf("accept seed stop: %w", err)
	}
	for _, state := range []protocolstate.ServerState{protocolstate.ServerDraining, protocolstate.ServerStopping, protocolstate.ServerStopped} {
		currentServer, err := operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
		if err != nil {
			return err
		}
		currentOperation, err := operationStore.GetOperation(acceptedStop.Record.OperationID)
		if err != nil {
			return err
		}
		if currentOperation.Record.Status == protocolstate.OperationPending {
			currentOperation.Record.Status = protocolstate.OperationRunning
		}
		nextServer := currentServer.Record
		nextServer.LifecycleState = state
		if _, _, err := operationStore.UpdateOperationAndLogicalServer(currentOperation.Revision, currentOperation.Record, currentServer.Revision, nextServer); err != nil {
			return err
		}
	}
	currentStop, err := operationStore.GetOperation(acceptedStop.Record.OperationID)
	if err != nil {
		return err
	}
	currentStop.Record.Status = protocolstate.OperationFailed
	currentStop.Record.Error = json.RawMessage(`{"code":"test.seed_complete"}`)
	_, err = operationStore.UpdateOperation(currentStop.Revision, currentStop.Record)
	return err
}
