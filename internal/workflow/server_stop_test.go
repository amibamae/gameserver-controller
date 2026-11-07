package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestServerStopSavesAndStopsTheExactRuntime(t *testing.T) {
	fixture := newServerStopFixture(t, adapter.OutcomeCompleted, adapter.OutcomeCompleted)
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded || len(completed.Record.Steps) != 4 {
		t.Fatalf("server.stop status/steps = %s/%d, error=%s", completed.Record.Status, len(completed.Record.Steps), completed.Record.Error)
	}
	for _, step := range completed.Record.Steps {
		if step.Status != protocolstate.StepSucceeded || step.Attempt != 1 {
			t.Fatalf("step %q status/attempt = %s/%d", step.StepID, step.Status, step.Attempt)
		}
	}
	if err := fixture.base.schemas.ValidateJSON("urn:gameserver-controller:"+fixture.base.schemas.ProtocolVersion()+":operations:server-stop-result", completed.Record.Result); err != nil {
		t.Fatalf("server.stop result is not typed: %v (%s)", err, completed.Record.Result)
	}
	server, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerStopped || server.Record.DesiredState != "stopped" {
		t.Fatalf("logical server state = %#v, err=%v; want stopped/stopped", server.Record, err)
	}
	if len(server.Record.LifecycleEvents) < 2 || server.Record.LifecycleEvents[len(server.Record.LifecycleEvents)-1].ToState != protocolstate.ServerStopped ||
		server.Record.LifecycleEvents[len(server.Record.LifecycleEvents)-2].ToState != protocolstate.ServerStopping {
		t.Fatalf("lifecycle history does not record stopping then stopped: %#v", server.Record.LifecycleEvents)
	}
	gameCalls := fixture.base.game.Calls()
	if len(gameCalls) != 2 || gameCalls[1].Params.Capability != "game.save.request" {
		t.Fatalf("game calls = %#v, expected readiness then exact game save", gameCalls)
	}
	executionCalls := fixture.base.execution.Calls()
	if len(executionCalls) != 4 || executionCalls[2].Params.Capability != "execution.unit.inspect" || executionCalls[3].Params.Capability != "execution.unit.graceful_stop" {
		t.Fatalf("execution calls = %#v, expected start verification, then stop inspection and graceful stop", executionCalls)
	}
}

func TestServerStopReconcilesUnknownGracefulStopByExactUnitInspection(t *testing.T) {
	fixture := newServerStopFixture(t, adapter.OutcomeCompleted, adapter.OutcomeUnknown)
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded || completed.Record.Steps[3].Attempt != 1 {
		t.Fatalf("reconciled stop status/attempt = %s/%d, error=%s", completed.Record.Status, completed.Record.Steps[3].Attempt, completed.Record.Error)
	}
	if got := fixture.base.execution.Calls(); len(got) != 5 || got[3].Params.Capability != "execution.unit.graceful_stop" || got[4].Params.Capability != "execution.unit.inspect" {
		t.Fatalf("stop was not inspected after ambiguous result: %#v", got)
	}
}

func TestServerStopReconciliationRejectsInspectionForDifferentUnit(t *testing.T) {
	fixture := newServerStopFixture(t, adapter.OutcomeCompleted, adapter.OutcomeUnknown)
	operation := fixture.operation.Record
	for i := range operation.Steps {
		if operation.Steps[i].StepID == stopInspectUnitStep {
			operation.Steps[i].Input = json.RawMessage(`{"unit_ref":{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"other-server"}}`)
		}
	}
	stopStep := operationStep(operation, stopRuntimeStep)
	callsBeforeReconciliation := len(fixture.base.execution.Calls())
	resolution, err := fixture.workflow.Reconcile(context.Background(), operation, stopStep)
	if err != nil || resolution.State != ResolutionNeedsOperator {
		t.Fatalf("reconciliation = %#v, %v; want needs operator for wrong-unit inspection", resolution, err)
	}
	if callsAfterReconciliation := len(fixture.base.execution.Calls()); callsAfterReconciliation != callsBeforeReconciliation {
		t.Fatalf("reconciliation made an adapter call for the untrusted unit: %d -> %d", callsBeforeReconciliation, callsAfterReconciliation)
	}
}

func TestServerStopDoesNotStopWhenGameSaveIsUnknown(t *testing.T) {
	fixture := newServerStopFixture(t, adapter.OutcomeUnknown, adapter.OutcomeCompleted)
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationNeedsOperator {
		t.Fatalf("unknown game-save status = %s, want needs_operator", completed.Record.Status)
	}
	for _, call := range fixture.base.execution.Calls() {
		if call.Params.Capability == "execution.unit.graceful_stop" {
			t.Fatal("runtime stop ran without confirmed game-save evidence")
		}
	}
	server, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerStopping {
		t.Fatalf("unknown save did not retain the stopping state and lock: %s, %v", server.Record.LifecycleState, err)
	}
}

func TestServerStopDoesNotSaveWhenProviderInstanceIsInactive(t *testing.T) {
	fixture := newServerStopFixture(t, adapter.OutcomeCompleted, adapter.OutcomeCompleted)
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	output := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"state":"stopped","observed_at":%q}`, startTestInstance, observedAt))
	if err := fixture.base.hosting.SetScript("hosting.instance.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: output, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationFailed || completed.Record.Steps[1].Status != protocolstate.StepPending {
		t.Fatalf("inactive-provider operation/step state = %s/%s, want failed/pending", completed.Record.Status, completed.Record.Steps[1].Status)
	}
	if got := len(fixture.base.game.Calls()); got != 1 {
		t.Fatalf("game adapter calls = %d, want only the earlier server.start readiness call", got)
	}
	for _, call := range fixture.base.execution.Calls() {
		if call.Params.Capability == "execution.unit.graceful_stop" {
			t.Fatal("runtime stop ran while the exact provider instance was stopped")
		}
	}
	server, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerDegraded {
		t.Fatalf("precondition failure lifecycle state = %s, %v; want degraded", server.Record.LifecycleState, err)
	}
}

func TestServerStopRejectsForceWithoutApprovalValidator(t *testing.T) {
	base := newServerStartFixture(t, true, adapter.OutcomeCompleted, "active")
	if _, err := base.engine.Run(context.Background(), base.operation.Record.OperationID); err != nil {
		t.Fatal(err)
	}
	profile := &staticStopProfileResolver{profile: ServerStopProfile{GameReleaseID: startTestRelease, UnitRef: json.RawMessage(startTestUnit)}}
	workflow, err := NewServerStopWorkflow(base.store, base.registry, base.schemas, profile)
	if err != nil {
		t.Fatal(err)
	}
	operation := stopOperationRecord(base.schemas.ProtocolVersion(), json.RawMessage(`{"force":true,"approval_id":"approved-stop-9"}`))
	if _, err := workflow.BuildPlan(context.Background(), operation); err == nil {
		t.Fatal("force-stop request was planned without an approval validator and force-stop capability")
	}
}

type staticStopProfileResolver struct{ profile ServerStopProfile }

func (r *staticStopProfileResolver) ResolveServerStop(context.Context, store.LogicalServer) (ServerStopProfile, error) {
	return r.profile, nil
}

type serverStopFixture struct {
	base      serverStartFixture
	workflow  *ServerStopWorkflow
	engine    *Engine
	operation store.StoredOperation
}

func newServerStopFixture(t *testing.T, saveOutcome, stopOutcome adapter.Outcome) serverStopFixture {
	t.Helper()
	base := newServerStartFixture(t, true, adapter.OutcomeCompleted, "active")
	started, err := base.engine.Run(context.Background(), base.operation.Record.OperationID)
	if err != nil || started.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("seed start status = %s, err=%v", started.Record.Status, err)
	}
	profile := &staticStopProfileResolver{profile: ServerStopProfile{GameReleaseID: startTestRelease, UnitRef: json.RawMessage(startTestUnit)}}
	workflow, err := NewServerStopWorkflow(base.store, base.registry, base.schemas, profile)
	if err != nil {
		t.Fatal(err)
	}
	operation := stopOperationRecord(base.schemas.ProtocolVersion(), json.RawMessage(`{}`))
	steps, err := workflow.BuildPlan(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.Steps = steps
	accepted, _, err := base.store.AcceptOperation(operation, testProof(31), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		t.Fatal(err)
	}
	definitions := NewDefinitions()
	if err := definitions.Register("server.stop", workflow); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(base.store, base.registry, base.schemas, definitions, &testResumeAuthorizer{}, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Now().UTC().Format(time.RFC3339Nano)
	providerOutput := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"state":"active","observed_at":%q}`, startTestInstance, observedAt))
	if err := base.hosting.SetScript("hosting.instance.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerOutput, ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	unitRunning := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"running","observed_at":%q}`, startTestUnit, observedAt))
	unitStopped := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"stopped","observed_at":%q}`, startTestUnit, observedAt))
	inspectReplies := []fakeadapter.Reply{{Outcome: adapter.OutcomeCompleted, Output: unitRunning, ObservedAt: time.Now().UTC()}}
	if stopOutcome == adapter.OutcomeUnknown {
		inspectReplies = append(inspectReplies, fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: unitStopped, ObservedAt: time.Now().UTC()})
	}
	if err := base.execution.SetScript("execution.unit.inspect", inspectReplies...); err != nil {
		t.Fatal(err)
	}
	saveState, savedAt := "confirmed", observedAt
	if saveOutcome == adapter.OutcomeUnknown {
		saveState, savedAt = "outcome_unknown", ""
	}
	gameSave := map[string]any{
		"target":     map[string]any{"server_id": startTestServerID, "environment_id": startTestEnvID, "instance_ref": json.RawMessage(startTestInstance)},
		"release_id": startTestRelease, "outcome": saveState, "observed_at": observedAt,
	}
	if savedAt != "" {
		gameSave["saved_at"] = savedAt
	} else {
		gameSave["reconciliation_ref"] = json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"operation","external_id":"save-ambiguous"}`)
	}
	gameSaveOutput, err := json.Marshal(gameSave)
	if err != nil {
		t.Fatal(err)
	}
	gameSaveReply := fakeadapter.Reply{Outcome: saveOutcome, Output: gameSaveOutput, ObservedAt: time.Now().UTC()}
	if saveOutcome == adapter.OutcomeUnknown {
		gameSaveReply.ExternalOperationRef = json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"operation","external_id":"save-ambiguous"}`)
	}
	if err := base.game.SetScript("game.save.request", gameSaveReply); err != nil {
		t.Fatal(err)
	}
	stopState := "completed"
	if stopOutcome == adapter.OutcomeUnknown {
		stopState = "outcome_unknown"
	}
	stopOutput := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"outcome":%q,"state":%q,"observed_at":%q,"external_operation_ref":{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"operation","external_id":"stop-ambiguous"}}`, startTestUnit, stopState, map[bool]string{true: "unknown", false: "stopped"}[stopOutcome == adapter.OutcomeUnknown], observedAt))
	stopReply := fakeadapter.Reply{Outcome: stopOutcome, Output: stopOutput, ObservedAt: time.Now().UTC()}
	if stopOutcome == adapter.OutcomeUnknown {
		stopReply.ExternalOperationRef = json.RawMessage(`{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"operation","external_id":"stop-ambiguous"}`)
	}
	if err := base.execution.SetScript("execution.unit.graceful_stop", stopReply); err != nil {
		t.Fatal(err)
	}
	return serverStopFixture{base: base, workflow: workflow, engine: engine, operation: accepted}
}

func stopOperationRecord(version string, parameters json.RawMessage) store.OperationRecord {
	return store.OperationRecord{
		OperationID: "stop-operation", OperationType: "server.stop", ProtocolVersion: version,
		Target:  store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID, InstanceRef: json.RawMessage(startTestInstance)},
		ActorID: "operator-1", IdempotencyKey: "stop-key", RequestFingerprint: "sha256:" + strings.Repeat("a", 64),
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.stop", Parameters: parameters},
	}
}
