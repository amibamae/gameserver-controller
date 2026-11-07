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

const (
	destroyTestBackupID   = "backup-destroy-test-001"
	destroyTestProfile    = "wasabi-test"
	destroyTestApproval   = "approval-destroy-test-001"
	destroyTestGeneration = "generation-destroy-test-001"
)

type staticDestroyProfileResolver struct{ profile ServerDestroyProfile }

func (r *staticDestroyProfileResolver) ResolveServerDestroy(context.Context, store.LogicalServer) (ServerDestroyProfile, error) {
	return r.profile, nil
}

type staticDestroyBackupResolver struct{ backup VerifiedServerBackup }

func (r *staticDestroyBackupResolver) ResolveVerifiedServerBackup(context.Context, store.LogicalServer, string, string) (VerifiedServerBackup, error) {
	return r.backup, nil
}

type testServerDestroyAuthorizer struct {
	calls int
	err   error
}

func (a *testServerDestroyAuthorizer) AuthorizeServerDestroy(context.Context, string, string, store.OperationRecord, store.LogicalServer, VerifiedServerBackup) error {
	a.calls++
	return a.err
}

type serverDestroyFixture struct {
	base       serverStartFixture
	workflow   *ServerDestroyWorkflow
	engine     *Engine
	operation  store.StoredOperation
	backups    *staticDestroyBackupResolver
	authorizer *testServerDestroyAuthorizer
}

func newServerDestroyFixture(t *testing.T) serverDestroyFixture {
	t.Helper()
	stopped := newServerStopFixture(t, adapter.OutcomeCompleted, adapter.OutcomeCompleted)
	completedStop, err := stopped.engine.Run(context.Background(), stopped.operation.Record.OperationID)
	if err != nil || completedStop.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("seed stop status = %s, err=%v", completedStop.Record.Status, err)
	}
	base := stopped.base
	if err := seedKnownDestroyGeneration(base.store, base.schemas.ProtocolVersion()); err != nil {
		t.Fatal(err)
	}
	profiles := &staticDestroyProfileResolver{profile: ServerDestroyProfile{UnitRef: json.RawMessage(startTestUnit)}}
	backups := &staticDestroyBackupResolver{backup: VerifiedServerBackup{
		BackupID: destroyTestBackupID, ServerID: startTestServerID, EnvironmentID: startTestEnvID,
		StorageProfile: destroyTestProfile, DataGenerationID: destroyTestGeneration,
		SourceInstanceRef: json.RawMessage(startTestInstance), IntegrityVerified: true,
	}}
	authorizer := &testServerDestroyAuthorizer{}
	workflow, err := NewServerDestroyWorkflow(base.store, base.registry, base.schemas, profiles, backups, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	parameters := json.RawMessage(fmt.Sprintf(`{"expected_instance_ref":%s,"backup_profile":%q,"verified_backup_id":%q,"approval_id":%q}`,
		startTestInstance, destroyTestProfile, destroyTestBackupID, destroyTestApproval))
	operation := store.OperationRecord{
		OperationID: "destroy-operation", OperationType: "server.destroy", ProtocolVersion: base.schemas.ProtocolVersion(),
		Target:  store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID, InstanceRef: json.RawMessage(startTestInstance)},
		ActorID: "operator-1", IdempotencyKey: "destroy-key", RequestFingerprint: "sha256:" + strings.Repeat("d", 64),
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.destroy", Parameters: parameters},
	}
	steps, err := workflow.BuildPlan(context.Background(), operation)
	if err != nil {
		t.Fatal(err)
	}
	operation.Steps = steps
	accepted, _, err := base.store.AcceptOperation(operation, testProof(47), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		t.Fatal(err)
	}
	definitions := NewDefinitions()
	if err := definitions.Register("server.destroy", workflow); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(base.store, base.registry, base.schemas, definitions, &testResumeAuthorizer{}, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	return serverDestroyFixture{base: base, workflow: workflow, engine: engine, operation: accepted, backups: backups, authorizer: authorizer}
}

func TestServerDestroyRequiresVerifiedCurrentBackupAndApproval(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	operation := fixture.operation.Record
	server, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil {
		t.Fatal(err)
	}

	fixture.backups.backup.IntegrityVerified = false
	if _, err := fixture.workflow.BuildPlan(context.Background(), operation); err == nil {
		t.Fatal("destroy plan accepted a backup whose contents were not verified")
	}
	fixture.backups.backup.IntegrityVerified = true
	fixture.backups.backup.DataGenerationID = "stale-generation"
	if _, err := fixture.workflow.BuildPlan(context.Background(), operation); err == nil {
		t.Fatal("destroy plan accepted a backup from an older data generation")
	}
	fixture.backups.backup.DataGenerationID = destroyTestGeneration
	fixture.authorizer.err = fmt.Errorf("approval expired")
	if _, err := fixture.workflow.BuildPlan(context.Background(), operation); err == nil {
		t.Fatal("destroy plan accepted an expired exact-target approval")
	}
	fixture.authorizer.err = nil
	if err := validateServerDestroyTarget(operation, serverDestroyParameters{
		ExpectedInstanceRef: json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"instance","external_id":"other-vm"}`),
		BackupProfile:       destroyTestProfile, VerifiedBackupID: destroyTestBackupID, ApprovalID: destroyTestApproval,
	}, server.Record); err == nil {
		t.Fatal("destroy operation accepted a different exact target")
	}
}

func TestServerDestroyConfirmsBackupStoppedRuntimeAndProviderAbsence(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	baseTime := time.Now().UTC().Add(time.Minute)
	providerBefore := instanceInspectionOutput(json.RawMessage(startTestInstance), "active", baseTime)
	providerAfter := instanceInspectionOutput(json.RawMessage(startTestInstance), "destroyed", baseTime.Add(3*time.Second))
	if err := fixture.base.hosting.SetScript("hosting.instance.inspect",
		fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerBefore, ObservedAt: baseTime},
		fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerAfter, ObservedAt: baseTime.Add(3 * time.Second)},
	); err != nil {
		t.Fatal(err)
	}
	unitOutput := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"stopped","observed_at":%q}`, startTestUnit, baseTime.Add(time.Second).Format(time.RFC3339Nano)))
	if err := fixture.base.execution.SetScript("execution.unit.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: unitOutput, ObservedAt: baseTime.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	destroyOutput := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"outcome":"completed","state":"destroyed","observed_at":%q}`, startTestInstance, baseTime.Add(2*time.Second).Format(time.RFC3339Nano)))
	if err := fixture.base.hosting.SetScript("hosting.instance.destroy", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: destroyOutput, ObservedAt: baseTime.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded || len(completed.Record.Steps) != 4 {
		t.Fatalf("destroy status/steps = %s/%d, error=%s", completed.Record.Status, len(completed.Record.Steps), completed.Record.Error)
	}
	for _, step := range completed.Record.Steps {
		if step.Status != protocolstate.StepSucceeded || step.Attempt != 1 {
			t.Fatalf("step %q status/attempt = %s/%d, want succeeded/1", step.StepID, step.Status, step.Attempt)
		}
	}
	if fixture.authorizer.calls != 2 {
		t.Fatalf("exact-target approval checks = %d, want planning and immediately before provider dispatch", fixture.authorizer.calls)
	}
	if err := fixture.base.schemas.ValidateJSON("urn:gameserver-controller:"+fixture.base.schemas.ProtocolVersion()+":operations:server-destroy-result", completed.Record.Result); err != nil {
		t.Fatalf("server.destroy result is not typed: %v (%s)", err, completed.Record.Result)
	}
	server, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerDestroyed || len(server.Record.InstanceRef) != 0 || server.Record.DataGenerationID != destroyTestGeneration {
		t.Fatalf("destroyed logical server = %#v, err=%v", server.Record, err)
	}
	reservation, err := fixture.base.store.GetCapacityReservation("start-test-capacity")
	if err != nil || reservation.State != store.CapacityDestroyed {
		t.Fatalf("destroyed managed VM reservation = %#v, err=%v; want destroyed only after provider absence", reservation, err)
	}
	if got := fixture.base.hosting.Calls(); len(got) < 3 || got[len(got)-3].Params.Capability != "hosting.instance.inspect" ||
		got[len(got)-2].Params.Capability != "hosting.instance.destroy" || got[len(got)-1].Params.Capability != "hosting.instance.inspect" {
		t.Fatalf("provider calls = %#v; want final inspect, destroy, inspect sequence", got)
	}
}

func TestServerDestroyNeverCallsProviderWhileRuntimeIsRunning(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	observedAt := time.Now().UTC().Add(time.Minute)
	providerOutput := instanceInspectionOutput(json.RawMessage(startTestInstance), "active", observedAt)
	if err := fixture.base.hosting.SetScript("hosting.instance.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerOutput, ObservedAt: observedAt}); err != nil {
		t.Fatal(err)
	}
	runtimeOutput := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"running","observed_at":%q}`, startTestUnit, observedAt.Add(time.Second).Format(time.RFC3339Nano)))
	if err := fixture.base.execution.SetScript("execution.unit.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: runtimeOutput, ObservedAt: observedAt.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	failed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Record.Status != protocolstate.OperationNeedsOperator || failed.Record.Steps[2].Status != protocolstate.StepPending {
		t.Fatalf("destroy with active runtime status/provider step = %s/%s, want needs_operator/pending", failed.Record.Status, failed.Record.Steps[2].Status)
	}
	for _, call := range fixture.base.hosting.Calls() {
		if call.Params.RequestContext.OperationID == fixture.operation.Record.OperationID && call.Params.Capability == "hosting.instance.destroy" {
			t.Fatal("provider destruction was dispatched while the runtime was running")
		}
	}
	server, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil || server.Record.LifecycleState != protocolstate.ServerStopped {
		t.Fatalf("rejected destroy changed lifecycle state: %s, %v", server.Record.LifecycleState, err)
	}
}

func TestServerDestroyReconcilesAcceptedProviderOperationByExactWait(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	baseTime := time.Now().UTC().Add(time.Minute)
	providerBefore := instanceInspectionOutput(json.RawMessage(startTestInstance), "active", baseTime)
	providerAfter := instanceInspectionOutput(json.RawMessage(startTestInstance), "destroyed", baseTime.Add(3*time.Second))
	if err := fixture.base.hosting.SetScript("hosting.instance.inspect",
		fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerBefore, ObservedAt: baseTime},
		fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: providerAfter, ObservedAt: baseTime.Add(3 * time.Second)},
	); err != nil {
		t.Fatal(err)
	}
	unitOutput := json.RawMessage(fmt.Sprintf(`{"unit_ref":%s,"state":"absent","observed_at":%q}`, startTestUnit, baseTime.Add(time.Second).Format(time.RFC3339Nano)))
	if err := fixture.base.execution.SetScript("execution.unit.inspect", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: unitOutput, ObservedAt: baseTime.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	providerOperationRef := json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"staging","resource_kind":"operation","external_id":"destroy-accepted-1"}`)
	destroyOutput := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"outcome":"accepted","state":"destroying","observed_at":%q,"provider_operation_ref":%s}`,
		startTestInstance, baseTime.Add(2*time.Second).Format(time.RFC3339Nano), providerOperationRef))
	if err := fixture.base.hosting.SetScript("hosting.instance.destroy", fakeadapter.Reply{
		Outcome: adapter.OutcomeAccepted, Output: destroyOutput, ObservedAt: baseTime.Add(2 * time.Second), ExternalOperationRef: providerOperationRef,
	}); err != nil {
		t.Fatal(err)
	}
	waitOutput := json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"provider_operation_ref":%s,"operation_status":"succeeded","instance_state":"destroyed","observed_at":%q}`,
		startTestInstance, providerOperationRef, baseTime.Add(3*time.Second).Format(time.RFC3339Nano)))
	if err := fixture.base.hosting.SetScript("hosting.instance.wait", fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: waitOutput, ObservedAt: baseTime.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	waiting, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Record.Status != protocolstate.OperationWaiting {
		t.Fatalf("accepted destroy status = %s, want waiting", waiting.Record.Status)
	}
	reservation, err := fixture.base.store.GetCapacityReservation("start-test-capacity")
	if err != nil || reservation.State != store.CapacityDestroyOutcomeUnknown {
		t.Fatalf("accepted provider operation released the capacity slot: %#v, err=%v", reservation, err)
	}
	completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Record.Status != protocolstate.OperationSucceeded || completed.Record.Steps[2].Attempt != 1 || completed.Record.Steps[3].Attempt != 1 {
		t.Fatalf("accepted destroy reconciliation status/attempts = %s/%d/%d, error=%s", completed.Record.Status, completed.Record.Steps[2].Attempt, completed.Record.Steps[3].Attempt, completed.Record.Error)
	}
	if got := fixture.base.hosting.Calls(); len(got) < 4 || got[len(got)-3].Params.Capability != "hosting.instance.destroy" ||
		got[len(got)-2].Params.Capability != "hosting.instance.wait" || got[len(got)-1].Params.Capability != "hosting.instance.inspect" {
		t.Fatalf("accepted destroy provider calls = %#v; expected one destroy, exact wait, and final inspection", got)
	}
	reservation, err = fixture.base.store.GetCapacityReservation("start-test-capacity")
	if err != nil || reservation.State != store.CapacityDestroyed {
		t.Fatalf("reconciled destroy did not release capacity after provider absence: %#v, err=%v", reservation, err)
	}
}

func instanceInspectionOutput(instanceRef json.RawMessage, state string, observedAt time.Time) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"instance_ref":%s,"state":%q,"observed_at":%q}`, instanceRef, state, observedAt.Format(time.RFC3339Nano)))
}

func seedKnownDestroyGeneration(operationStore *store.BoltStore, protocolVersion string) error {
	operation := store.OperationRecord{
		OperationID: "destroy-test-generation", OperationType: "server.backup", ProtocolVersion: protocolVersion,
		Target:  store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID, InstanceRef: json.RawMessage(startTestInstance)},
		ActorID: "operator-1", IdempotencyKey: "destroy-test-generation-key", RequestFingerprint: "sha256:" + strings.Repeat("e", 64),
		RequestSnapshot: store.RequestSnapshot{OperationType: "server.backup", Parameters: json.RawMessage(`{}`)},
		Steps:           []store.OperationStep{{StepID: "backup-data", StepType: "game.save.request", Status: protocolstate.StepPending, Input: json.RawMessage(`{}`)}},
	}
	accepted, _, err := operationStore.AcceptOperation(operation, testProof(48), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		return err
	}
	server, err := operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil {
		return err
	}
	accepted.Record.Status = protocolstate.OperationRunning
	accepted.Record.Steps[0].Status = protocolstate.StepRunning
	accepted.Record.Steps[0].Attempt = 1
	stepID := accepted.Record.Steps[0].StepID
	accepted.Record.CurrentStepID = &stepID
	server.Record.LifecycleState = protocolstate.ServerBackingUp
	active, backingUp, err := operationStore.UpdateOperationAndLogicalServer(accepted.Revision, accepted.Record, server.Revision, server.Record)
	if err != nil {
		return err
	}
	active.Record.Status = protocolstate.OperationSucceeded
	active.Record.CurrentStepID = nil
	active.Record.Steps[0].Status = protocolstate.StepSucceeded
	active.Record.Steps[0].Output = json.RawMessage(`{}`)
	active.Record.Result = json.RawMessage(`{}`)
	backingUp.Record.LifecycleState = protocolstate.ServerStopped
	backingUp.Record.DataGenerationID = destroyTestGeneration
	backingUp.Record.DataGenerationStatus = "known"
	_, _, err = operationStore.UpdateOperationAndLogicalServer(active.Revision, active.Record, backingUp.Revision, backingUp.Record)
	return err
}
