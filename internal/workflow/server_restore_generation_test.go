package workflow

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	overwrittenGeneration = "generation-before"
	restoredGeneration    = "restore-restore-e2e"
	startedRestoreParams  = `{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1","start_after_restore":true}`
)

// Replacing a server whose data generation is known is the case
// replace_existing_data exists for. The restore must run to completion and
// leave the restored generation known; the store commits a server record only
// with a lifecycle transition, so the projection must not report a
// generation-only change while the server stays restoring.
func TestRestoreReplacesKnownGenerationEndToEnd(t *testing.T) {
	f := newRestorePlanTestFixture(t)
	seedKnownGeneration(t, f.operationStore, f.version, overwrittenGeneration)
	operation, plan := f.defaultPlan(t)
	f.verifyBackupStream(t, operation, plan)
	f.runStoppedRestore(t)
}

// Dispatching the restore starts overwriting the target, so the generation it
// held stops being current: a verified backup of that generation must no
// longer satisfy server.destroy's current-data check.
func TestRestoreDispatchRetiresTheOverwrittenGeneration(t *testing.T) {
	proven := provenStoppedRestore(t)
	operation := proven.copyOperation()
	operation.Status = protocolstate.OperationRunning
	apply := operationStepIndex(operation, restoreApplyStep)
	operation.Steps[apply].Status = protocolstate.StepRunning
	for i := apply + 1; i < len(operation.Steps); i++ {
		operation.Steps[i].Status = protocolstate.StepPending
	}
	next, changed, err := proven.project(operation, protocolstate.ServerStopped)
	if err != nil || !changed || next.LifecycleState != protocolstate.ServerRestoring {
		t.Fatalf("dispatched restore projected state=%s changed=%v err=%v; want restoring", next.LifecycleState, changed, err)
	}
	if next.DataGenerationID != "" || next.DataGenerationStatus != "unknown" {
		t.Fatalf("dispatched restore projected generation=%q/%s; want empty/unknown", next.DataGenerationID, next.DataGenerationStatus)
	}
}

// A proven restore holds this operation's new generation at every transition
// out of restoring, and never the overwritten one. protocol/lifecycle.md
// requires it before the runtime starts; once a start was attempted without
// the operation succeeding, the runtime may have changed the data.
func TestRestoreProvenTransitionsCarryTheRestoredGeneration(t *testing.T) {
	proven := provenStoppedRestore(t)
	cases := []struct {
		name       string
		status     protocolstate.OperationStatus
		lifecycle  protocolstate.ServerState
		start      protocolstate.StepStatus
		attempt    int
		after      protocolstate.StepStatus // verify-runtime and readiness
		wantState  protocolstate.ServerState
		wantStatus string
	}{
		{"operation fails after a proven restore", protocolstate.OperationFailed, protocolstate.ServerRestoring, "", 0, "", protocolstate.ServerFailed, "known"},
		{"runtime start is dispatched", protocolstate.OperationRunning, protocolstate.ServerRestoring, protocolstate.StepRunning, 1, protocolstate.StepPending, protocolstate.ServerStarting, "unknown"},
		{"operation fails after the runtime start", protocolstate.OperationFailed, protocolstate.ServerStarting, protocolstate.StepFailed, 1, protocolstate.StepPending, protocolstate.ServerFailed, "unknown"},
		{"operation fails with the runtime start skipped", protocolstate.OperationFailed, protocolstate.ServerRestoring, protocolstate.StepSkipped, 0, protocolstate.StepSkipped, protocolstate.ServerFailed, "known"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operation := proven.copyOperation()
			operation.Status = tc.status
			if tc.start != "" {
				operation.RequestSnapshot.Parameters = json.RawMessage(startedRestoreParams)
				operation.Steps = append(operation.Steps,
					store.OperationStep{StepID: restoreStartStep, Status: tc.start, Attempt: tc.attempt},
					store.OperationStep{StepID: restoreVerifyRuntimeStep, Status: tc.after, Attempt: tc.attempt},
					store.OperationStep{StepID: restoreReadinessStep, Status: tc.after, Attempt: tc.attempt})
			}
			next, changed, err := proven.project(operation, tc.lifecycle)
			if err != nil || !changed {
				t.Fatalf("projection changed=%v err=%v; want a transition", changed, err)
			}
			if next.LifecycleState != tc.wantState || next.DataGenerationID != restoredGeneration || next.DataGenerationStatus != tc.wantStatus {
				t.Fatalf("projected state=%s generation=%q/%s; want %s %q/%s", next.LifecycleState, next.DataGenerationID, next.DataGenerationStatus,
					tc.wantState, restoredGeneration, tc.wantStatus)
			}
		})
	}
}

// Without a lifecycle transition the projection leaves the stored record as
// it is: the store refuses a commit that does not advance the state.
func TestRestoreProjectionReportsNoChangeWithoutTransition(t *testing.T) {
	proven := provenStoppedRestore(t)
	running := proven.copyOperation()
	running.Status = protocolstate.OperationRunning
	starting := proven.copyOperation()
	starting.Status = protocolstate.OperationRunning
	starting.RequestSnapshot.Parameters = json.RawMessage(startedRestoreParams)
	starting.Steps = append(starting.Steps,
		store.OperationStep{StepID: restoreStartStep, Status: protocolstate.StepSucceeded, Attempt: 1},
		store.OperationStep{StepID: restoreVerifyRuntimeStep, Status: protocolstate.StepRunning, Attempt: 1},
		store.OperationStep{StepID: restoreReadinessStep, Status: protocolstate.StepPending})
	for name, tc := range map[string]struct {
		operation store.OperationRecord
		lifecycle protocolstate.ServerState
	}{
		"proven restore still restoring": {running, protocolstate.ServerRestoring},
		"runtime still starting":         {starting, protocolstate.ServerStarting},
	} {
		t.Run(name, func(t *testing.T) {
			server := proven.serverIn(tc.lifecycle)
			next, changed, err := proven.workflow.ProjectLifecycle(tc.operation, store.StoredLogicalServer{Record: server})
			if err != nil || changed {
				t.Fatalf("projection changed=%v err=%v; want no transition", changed, err)
			}
			if next.LifecycleState != server.LifecycleState || next.DataGenerationID != server.DataGenerationID || next.DataGenerationStatus != server.DataGenerationStatus {
				t.Fatalf("projection without a transition changed the record to %s %q/%s", next.LifecycleState, next.DataGenerationID, next.DataGenerationStatus)
			}
		})
	}
}

// A dispatched restore that was never proven still leaves the data unknown.
func TestRestoreProjectionUnprovenFailureStaysUnknown(t *testing.T) {
	proven := provenStoppedRestore(t)
	operation := proven.copyOperation()
	operation.Status = protocolstate.OperationFailed
	operation.Steps[operationStepIndex(operation, restoreInspectStep)].Status = protocolstate.StepFailed
	next, changed, err := proven.project(operation, protocolstate.ServerRestoring)
	if err != nil || !changed || next.LifecycleState != protocolstate.ServerFailed || next.DataGenerationID != "" || next.DataGenerationStatus != "unknown" {
		t.Fatalf("unproven restore projected state=%s generation=%q/%s changed=%v err=%v; want failed empty/unknown",
			next.LifecycleState, next.DataGenerationID, next.DataGenerationStatus, changed, err)
	}
}

type provenRestore struct {
	workflow  *ServerRestoreWorkflow
	operation store.OperationRecord
	server    store.LogicalServer
}

// provenStoppedRestore runs the scripted stopped restore end to end and
// returns its succeeded operation, whose restore call and inspection are
// proven, with the restored server.
func provenStoppedRestore(t *testing.T) provenRestore {
	t.Helper()
	f := newRestorePlanTestFixture(t)
	operation, plan := f.defaultPlan(t)
	f.verifyBackupStream(t, operation, plan)
	f.runStoppedRestore(t)
	completed, err := f.operationStore.GetOperation("restore-e2e")
	if err != nil {
		t.Fatal(err)
	}
	server, err := f.operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil {
		t.Fatal(err)
	}
	return provenRestore{workflow: f.workflow, operation: completed.Record, server: server.Record}
}

func (p provenRestore) copyOperation() store.OperationRecord {
	operation := p.operation
	operation.Steps = append([]store.OperationStep(nil), p.operation.Steps...)
	return operation
}

// serverIn is the restored server in state, still holding the generation the
// restore overwrites.
func (p provenRestore) serverIn(state protocolstate.ServerState) store.LogicalServer {
	server := p.server
	server.LifecycleState = state
	server.DataGenerationID, server.DataGenerationStatus = overwrittenGeneration, "known"
	return server
}

func (p provenRestore) project(operation store.OperationRecord, state protocolstate.ServerState) (store.LogicalServer, bool, error) {
	return p.workflow.ProjectLifecycle(operation, store.StoredLogicalServer{Record: p.serverIn(state)})
}

func operationStepIndex(operation store.OperationRecord, stepID string) int {
	for i, step := range operation.Steps {
		if step.StepID == stepID {
			return i
		}
	}
	return -1
}

// seedKnownGeneration runs the stopped seed server through a start and stop
// that leave it stopped with generation known.
func seedKnownGeneration(t *testing.T, operationStore *store.BoltStore, version, generation string) {
	t.Helper()
	seed := store.OperationRecord{
		OperationID: "seed-known-generation", OperationType: "server.start", ProtocolVersion: version,
		Target: store.OperationTarget{ServerID: startTestServerID, EnvironmentID: startTestEnvID}, ActorID: "operator-1", IdempotencyKey: "seed-known-generation",
		RequestFingerprint: "sha256:" + fmt.Sprintf("%064d", 4),
		RequestSnapshot:    store.RequestSnapshot{OperationType: "server.start", Parameters: json.RawMessage(`{}`)},
		Steps:              []store.OperationStep{{StepID: "seed-placeholder", StepType: "execution.unit.start", Status: protocolstate.StepPending, Input: json.RawMessage(`{}`)}},
	}
	accepted, _, err := operationStore.AcceptOperation(seed, testProof(24), []string{store.LogicalServerLockScope(startTestEnvID, startTestServerID)})
	if err != nil {
		t.Fatalf("accept generation seed: %v", err)
	}
	for _, state := range []protocolstate.ServerState{
		protocolstate.ServerStarting, protocolstate.ServerValidating, protocolstate.ServerReady,
		protocolstate.ServerDraining, protocolstate.ServerStopping, protocolstate.ServerStopped,
	} {
		server, err := operationStore.GetLogicalServer(startTestEnvID, startTestServerID)
		if err != nil {
			t.Fatal(err)
		}
		current, err := operationStore.GetOperation(accepted.Record.OperationID)
		if err != nil {
			t.Fatal(err)
		}
		current.Record.Status = protocolstate.OperationRunning
		next := server.Record
		next.LifecycleState = state
		next.DataGenerationID, next.DataGenerationStatus = generation, "known"
		if _, _, err := operationStore.UpdateOperationAndLogicalServer(current.Revision, current.Record, server.Revision, next); err != nil {
			t.Fatalf("project generation seed state %s: %v", state, err)
		}
	}
	current, err := operationStore.GetOperation(accepted.Record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	current.Record.Status = protocolstate.OperationFailed
	current.Record.Error = json.RawMessage(`{"code":"test.seed_complete"}`)
	if _, err := operationStore.UpdateOperation(current.Revision, current.Record); err != nil {
		t.Fatalf("finish generation seed: %v", err)
	}
}

// A started restore that succeeds after validation ends ready. The start
// lifecycle must not shadow the terminal projection: every start step has
// left pending by then, which once kept the server validating.
func TestRestoreStartedSuccessProjectsReady(t *testing.T) {
	fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
	restoreR3CompleteAll(t, fixture)
	assertion, err := BuildReadinessAssertion(fixture.workflow.schemas, fixture.operation, *fixture.server(), restoreReadinessStep, fixture.workflow.assertionTTL)
	if err != nil {
		t.Fatalf("could not build readiness assertion: %v", err)
	}
	result := restoreR3Patch(t, restoreR3RestoreResultBase(t, fixture), "ready", "operation_data", "final_lifecycle_state")
	fixture.operation.Result = restoreR3Patch(t, result, json.RawMessage(assertion), "operation_data", "readiness_assertion")
	fixture.operation.Status = protocolstate.OperationSucceeded
	server := fixture.servers.record
	server.Record.LifecycleState = protocolstate.ServerValidating
	next, changed, err := fixture.workflow.ProjectLifecycle(fixture.operation, server)
	if err != nil || !changed || next.LifecycleState != protocolstate.ServerReady || next.DesiredState != "running" {
		t.Fatalf("started restore projected state=%s desired=%s changed=%v err=%v; want ready/running", next.LifecycleState, next.DesiredState, changed, err)
	}
}
