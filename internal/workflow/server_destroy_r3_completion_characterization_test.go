package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These characterization tests pin the exact errors and check order of
// server.destroy ValidateCompletion, the exact success and failure result
// documents, and every lifecycle projection branch.

const (
	destroyR3PreEvidence     = "server.destroy lacks exact pre-destruction provider evidence"
	destroyR3RuntimeEvidence = "server.destroy lacks exact stopped-runtime evidence"
	destroyR3DestroyEvidence = "server.destroy lacks an exact completed provider destruction"
	destroyR3AbsenceEvidence = "server.destroy lacks exact provider-absence verification"
	destroyR3Projection      = "server.destroy lifecycle projection target mismatch"
	destroyR3TerminalResult  = "terminal server.destroy result lacks exact backup and provider-absence evidence"
	destroyR3FailureSummary  = "Server destruction did not confirm provider absence; known effects remain attached to this operation."
	destroyR3RuntimeUnitRef  = `{"adapter_id":"execution.fake","environment_id":"staging","resource_kind":"systemd-unit","external_id":"other-unit"}`
	destroyR3CompletedTwo    = `[{"step_id":"inspect-instance","step_type":"hosting.instance.inspect"},{"step_id":"inspect-runtime","step_type":"execution.unit.inspect"}]`
)

// destroyR3CompletedOperation returns the accepted destroy operation with all
// four steps succeeded and fresh outputs, the absence observed last.
func destroyR3CompletedOperation(fixture serverDestroyFixture) store.OperationRecord {
	operation := destroyOperationAfter(fixture, 2)
	destroyCompletedProviderStep(&operation, "completed", "destroyed", startTestInstance)
	started := time.Now().UTC()
	operation.Steps[3].Status = protocolstate.StepSucceeded
	operation.Steps[3].Attempt = 1
	operation.Steps[3].StartedAt = &started
	operation.Steps[3].Output = instanceInspectionOutput(json.RawMessage(startTestInstance), "destroyed", started.Add(time.Second))
	return operation
}

func destroyR3CompletionCase(fixture serverDestroyFixture) *destroyR3Case {
	c := destroyR3NewCase(fixture)
	c.operation = destroyR3CompletedOperation(fixture)
	return c
}

// destroyR3Output replaces one step's recorded output.
func destroyR3Output(index int, output string) func(*destroyR3Case) {
	return func(c *destroyR3Case) { c.operation.Steps[index].Output = json.RawMessage(output) }
}

func destroyR3Status(index int, status protocolstate.StepStatus) func(*destroyR3Case) {
	return func(c *destroyR3Case) { c.operation.Steps[index].Status = status }
}

func destroyR3Inspection(index int, instance, state string, observedAt time.Time) func(*destroyR3Case) {
	return destroyR3Output(index, string(instanceInspectionOutput(json.RawMessage(instance), state, observedAt)))
}

func destroyR3Runtime(state string) func(*destroyR3Case) {
	return destroyR3Output(1, fmt.Sprintf(`{"unit_ref":%s,"state":%q,"observed_at":%q}`, startTestUnit, state, time.Now().UTC().Format(time.RFC3339Nano)))
}

func destroyR3DestroyOutput(instance, outcome, state string, observedAt time.Time) func(*destroyR3Case) {
	return destroyR3Output(2, fmt.Sprintf(`{"instance_ref":%s,"outcome":%q,"state":%q,"observed_at":%q}`, instance, outcome, state, observedAt.Format(time.RFC3339Nano)))
}

func destroyR3Both(first, second func(*destroyR3Case)) func(*destroyR3Case) {
	return func(c *destroyR3Case) {
		first(c)
		second(c)
	}
}

func TestDestroyR3ValidateCompletionExactErrors(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Now().UTC()
	tests := []struct {
		name    string
		ctx     context.Context
		change  func(*destroyR3Case)
		want    string
		backups int
	}{
		{name: "complete evidence", backups: 1},
		{name: "nil context", want: "server.destroy completion validation requires an operation and context"},
		{name: "another operation type", change: func(c *destroyR3Case) { c.operation.OperationType = "server.stop" },
			want: "server.destroy completion validation requires an operation and context"},
		{name: "ended context", ctx: ended, want: context.Canceled.Error()},
		{name: "snapshot for another type", change: func(c *destroyR3Case) { c.operation.RequestSnapshot.OperationType = "server.stop" }, want: destroyR3RequestRequired},
		{name: "provider inspection not succeeded", change: destroyR3Status(0, protocolstate.StepRunning), want: destroyR3PreEvidence},
		{name: "provider inspection missing", change: func(c *destroyR3Case) { c.operation.Steps[0].StepID = "renamed" }, want: destroyR3PreEvidence},
		{name: "provider inspection already destroyed", change: destroyR3Inspection(0, startTestInstance, "destroyed", now), want: destroyR3PreEvidence},
		{name: "provider inspection of another instance", change: destroyR3Inspection(0, otherDestroyInstance, "active", now), want: destroyR3PreEvidence},
		{name: "provider inspection stale", change: destroyR3Inspection(0, startTestInstance, "active", now.Add(-time.Hour)), want: destroyR3PreEvidence},
		{name: "provider inspection active", change: destroyR3Inspection(0, startTestInstance, "active", now), backups: 1},
		{name: "runtime running", change: destroyR3Runtime("running"), want: destroyR3RuntimeEvidence},
		{name: "runtime absent", change: destroyR3Runtime("absent"), backups: 1},
		{name: "runtime not succeeded", change: destroyR3Status(1, protocolstate.StepPending), want: destroyR3RuntimeEvidence},
		{name: "runtime planned for another unit", change: func(c *destroyR3Case) {
			setStepInput(t, &c.operation.Steps[1], "unit_ref", json.RawMessage(destroyR3RuntimeUnitRef))
		}, want: destroyR3RuntimeEvidence},
		{name: "provider evidence is reported before runtime", change: destroyR3Both(destroyR3Status(0, protocolstate.StepRunning), destroyR3Runtime("running")), want: destroyR3PreEvidence},
		{name: "destroy only accepted", change: destroyR3DestroyOutput(startTestInstance, "accepted", "destroyed", now), want: destroyR3DestroyEvidence},
		{name: "destroy still destroying", change: destroyR3DestroyOutput(startTestInstance, "completed", "destroying", now), want: destroyR3DestroyEvidence},
		{name: "destroy of another instance", change: destroyR3DestroyOutput(otherDestroyInstance, "completed", "destroyed", now), want: destroyR3DestroyEvidence},
		{name: "destroy not succeeded", change: destroyR3Status(2, protocolstate.StepRunning), want: destroyR3DestroyEvidence},
		{name: "runtime is reported before destroy", change: destroyR3Both(destroyR3Runtime("running"), destroyR3Status(2, protocolstate.StepRunning)), want: destroyR3RuntimeEvidence},
		{name: "absence shows active", change: destroyR3Inspection(3, startTestInstance, "active", now.Add(time.Second)), want: destroyR3AbsenceEvidence},
		{name: "absence of another instance", change: destroyR3Inspection(3, otherDestroyInstance, "destroyed", now.Add(time.Second)), want: destroyR3AbsenceEvidence},
		{name: "absence not succeeded", change: destroyR3Status(3, protocolstate.StepPending), want: destroyR3AbsenceEvidence},
		{name: "destroy is reported before absence", change: destroyR3Both(destroyR3Status(2, protocolstate.StepRunning), destroyR3Status(3, protocolstate.StepPending)), want: destroyR3DestroyEvidence},
		{name: "absence predates destroy", change: destroyR3Both(destroyR3DestroyOutput(startTestInstance, "completed", "destroyed", now.Add(10*time.Second)),
			destroyR3Inspection(3, startTestInstance, "destroyed", now.Add(5*time.Second))),
			want: "provider absence observation predates destroy completion: observation timestamp moved backwards"},
		{name: "absence at the destroy time", change: destroyR3Both(destroyR3DestroyOutput(startTestInstance, "completed", "destroyed", now.Add(5*time.Second)),
			destroyR3Inspection(3, startTestInstance, "destroyed", now.Add(5*time.Second))), backups: 1},
		{name: "unknown server", change: func(c *destroyR3Case) { c.operation.Target.ServerID = "server-missing" }, want: store.ErrLogicalServerNotFound.Error()},
		{name: "evidence is reported before the server", change: destroyR3Both(destroyR3Status(3, protocolstate.StepPending),
			func(c *destroyR3Case) { c.operation.Target.ServerID = "server-missing" }), want: destroyR3AbsenceEvidence},
		{name: "target instance changed", change: func(c *destroyR3Case) { c.operation.Target.InstanceRef = json.RawMessage(otherDestroyInstance) },
			want: store.ErrLogicalServerTargetMismatch.Error()},
		{name: "unknown generation", change: func(c *destroyR3Case) {
			c.destroyR3WithServer(func(server *store.LogicalServer) { server.DataGenerationStatus = "unknown" })
		}, want: destroyR3KnownGeneration},
		{name: "backup from another generation", change: func(c *destroyR3Case) { c.backups.backup.DataGenerationID = "other" }, want: destroyR3BackupMismatch, backups: 1},
		{name: "backup resolver fails", change: func(c *destroyR3Case) { c.backups.err = errors.New("backup store offline") },
			want: "resolve verified backup for server.destroy: backup store offline", backups: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := destroyR3CompletionCase(fixture)
			if test.change != nil {
				test.change(c)
			}
			ctx := test.ctx
			if ctx == nil && test.name != "nil context" {
				ctx = context.Background()
			}
			destroyR3WantError(t, "ValidateCompletion", c.workflow.ValidateCompletion(ctx, c.operation), test.want)
			if c.backups.calls != test.backups || c.authorizer.calls != 0 {
				t.Fatalf("backup/authorizer calls = %d/%d, want %d/0", c.backups.calls, c.authorizer.calls, test.backups)
			}
		})
	}
}

// destroyR3SequencedServers serves the fixture store but lets a test rewrite
// or fail one numbered GetLogicalServer call.
type destroyR3SequencedServers struct {
	*store.BoltStore
	calls int
	on    func(call int, stored store.StoredLogicalServer, err error) (store.StoredLogicalServer, error)
}

func (s *destroyR3SequencedServers) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	s.calls++
	stored, err := s.BoltStore.GetLogicalServer(environmentID, serverID)
	return s.on(s.calls, stored, err)
}

func destroyR3SuccessJSON(completed, generation string) string {
	return `{"affected_resources":[` + startTestInstance + `],"audit_event_ids":[],"completed_steps":` + completed +
		`,"failed_steps":[],"operation_data":{"data_generation_id":"` + generation + `","final_lifecycle_state":"destroyed","instance_ref":` + startTestInstance +
		`,"provider_absence_confirmed":true,"verified_backup_id":"` + destroyTestBackupID + `"},"operation_type":"server.destroy",` +
		`"outcome_summary":"The selected backup was verified and the exact provider instance is confirmed absent.","partial":false,"unresolved_conditions":[],"warnings":[]}`
}

func TestDestroyR3BuildSuccessResult(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	allCompleted := `[{"step_id":"inspect-instance","step_type":"hosting.instance.inspect"},{"step_id":"inspect-runtime","step_type":"execution.unit.inspect"},` +
		`{"step_id":"destroy-instance","step_type":"hosting.instance.destroy"},{"step_id":"verify-instance-absence","step_type":"hosting.instance.inspect"}]`

	c := destroyR3CompletionCase(fixture)
	result, err := c.workflow.BuildSuccessResult(context.Background(), c.operation)
	if err != nil || string(result) != destroyR3SuccessJSON(allCompleted, destroyTestGeneration) {
		t.Fatalf("BuildSuccessResult = %s, %v; want %s", result, err, destroyR3SuccessJSON(allCompleted, destroyTestGeneration))
	}

	invalid := destroyR3CompletionCase(fixture)
	invalid.operation.Steps[3].Status = protocolstate.StepPending
	result, err = invalid.workflow.BuildSuccessResult(context.Background(), invalid.operation)
	destroyR3WantError(t, "BuildSuccessResult", err, destroyR3AbsenceEvidence)
	if result != nil {
		t.Fatalf("rejected BuildSuccessResult = %s, want nil", result)
	}

	// The result reports the generation of a second, later server load.
	reloaded := destroyR3CompletionCase(fixture)
	reloaded.workflow.servers = &destroyR3SequencedServers{BoltStore: fixture.base.store, on: func(call int, stored store.StoredLogicalServer, err error) (store.StoredLogicalServer, error) {
		if call == 2 {
			stored.Record.DataGenerationID = "generation-after-validation"
		}
		return stored, err
	}}
	result, err = reloaded.workflow.BuildSuccessResult(context.Background(), reloaded.operation)
	if err != nil || string(result) != destroyR3SuccessJSON(allCompleted, "generation-after-validation") {
		t.Fatalf("BuildSuccessResult after reload = %s, %v", result, err)
	}

	failedLoad := destroyR3CompletionCase(fixture)
	failedLoad.workflow.servers = &destroyR3SequencedServers{BoltStore: fixture.base.store, on: func(call int, stored store.StoredLogicalServer, err error) (store.StoredLogicalServer, error) {
		if call == 2 {
			return store.StoredLogicalServer{}, errors.New("store offline")
		}
		return stored, err
	}}
	result, err = failedLoad.workflow.BuildSuccessResult(context.Background(), failedLoad.operation)
	destroyR3WantError(t, "BuildSuccessResult", err, "store offline")
	if result != nil {
		t.Fatalf("BuildSuccessResult after a failed reload = %s, want nil", result)
	}
}

func destroyR3FailureJSON(affected, completed, failed string, partial bool) string {
	return `{"affected_resources":` + affected + `,"audit_event_ids":[],"completed_steps":` + completed + `,"failed_steps":` + failed +
		`,"operation_type":"server.destroy","outcome_summary":"` + destroyR3FailureSummary + `","partial":` + fmt.Sprint(partial) +
		`,"unresolved_conditions":[],"warnings":[]}`
}

func TestDestroyR3BuildFailureResult(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	failure := json.RawMessage(`{"code":"provider_error"}`)
	failedRuntime := func(operation store.OperationRecord) store.OperationStep {
		step := operation.Steps[1]
		step.Status = protocolstate.StepFailed
		return step
	}
	tests := []struct {
		name    string
		change  func(*store.OperationRecord)
		step    func(store.OperationRecord) store.OperationStep
		failure json.RawMessage
		want    string
	}{
		{name: "no failed step", want: destroyR3FailureJSON(`[]`, destroyR3CompletedTwo, `[]`, false)},
		{name: "failed runtime step", step: failedRuntime, failure: failure,
			want: destroyR3FailureJSON(`[]`, destroyR3CompletedTwo, `[{"error":{"code":"provider_error"},"step_id":"inspect-runtime","step_type":"execution.unit.inspect"}]`, false)},
		{name: "failed step without a failure", step: failedRuntime, want: destroyR3FailureJSON(`[]`, destroyR3CompletedTwo, `[]`, false)},
		{name: "succeeded step with a failure", step: func(o store.OperationRecord) store.OperationStep { return o.Steps[1] }, failure: failure,
			want: destroyR3FailureJSON(`[]`, destroyR3CompletedTwo, `[]`, false)},
		{name: "unnamed failed step", step: func(o store.OperationRecord) store.OperationStep {
			step := failedRuntime(o)
			step.StepID = ""
			return step
		}, failure: failure, want: destroyR3FailureJSON(`[]`, destroyR3CompletedTwo, `[]`, false)},
		{name: "destroy dispatched", change: func(o *store.OperationRecord) { o.Steps[2].Attempt = 1 },
			want: destroyR3FailureJSON(`[`+startTestInstance+`]`, destroyR3CompletedTwo, `[]`, true)},
		{name: "destroy dispatched without a target instance", change: func(o *store.OperationRecord) {
			o.Steps[2].Attempt = 1
			o.Target.InstanceRef = nil
		}, want: destroyR3FailureJSON(`[]`, destroyR3CompletedTwo, `[]`, true)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := destroyOperationAfter(fixture, 2)
			if test.change != nil {
				test.change(&operation)
			}
			var step store.OperationStep
			if test.step != nil {
				step = test.step(operation)
			}
			result, err := fixture.workflow.BuildFailureResult(context.Background(), operation, step, test.failure)
			if err != nil || string(result) != test.want {
				t.Fatalf("BuildFailureResult = %s, %v; want %s", result, err, test.want)
			}
		})
	}
}

// destroyR3EditResult decodes a result document, applies edit, and re-encodes it.
func destroyR3EditResult(t *testing.T, raw json.RawMessage, edit func(result, data map[string]any)) json.RawMessage {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	data, ok := result["operation_data"].(map[string]any)
	if !ok {
		t.Fatalf("result has no operation_data: %s", raw)
	}
	edit(result, data)
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// destroyR3Terminal is a succeeded destroy operation with its exact result.
func destroyR3Terminal(t *testing.T, fixture serverDestroyFixture) store.OperationRecord {
	t.Helper()
	c := destroyR3CompletionCase(fixture)
	result, err := c.workflow.BuildSuccessResult(context.Background(), c.operation)
	if err != nil {
		t.Fatal(err)
	}
	c.operation.Status = protocolstate.OperationSucceeded
	c.operation.Result = result
	return c.operation
}

func TestDestroyR3HasValidServerDestroyResult(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	stored, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil {
		t.Fatal(err)
	}
	editResult := func(edit func(result, data map[string]any)) func(*testing.T, *store.OperationRecord, *schemaRuntime) {
		return func(t *testing.T, o *store.OperationRecord, _ *schemaRuntime) { o.Result = destroyR3EditResult(t, o.Result, edit) }
	}
	tests := []struct {
		name   string
		change func(*testing.T, *store.OperationRecord, *schemaRuntime)
		want   bool
	}{
		{name: "exact result", want: true},
		{name: "undecodable result", change: func(_ *testing.T, o *store.OperationRecord, _ *schemaRuntime) { o.Result = json.RawMessage(`{`) }},
		{name: "undecodable parameters", change: func(_ *testing.T, o *store.OperationRecord, _ *schemaRuntime) {
			o.RequestSnapshot.Parameters = json.RawMessage(`{"approval_id":7}`)
		}},
		{name: "another operation type", change: editResult(func(r, _ map[string]any) { r["operation_type"] = "server.stop" })},
		{name: "another instance", change: editResult(func(_, d map[string]any) { d["instance_ref"] = json.RawMessage(otherDestroyInstance) })},
		{name: "another backup", change: editResult(func(_, d map[string]any) { d["verified_backup_id"] = "other" })},
		{name: "another generation", change: editResult(func(_, d map[string]any) { d["data_generation_id"] = "other" })},
		{name: "absence unconfirmed", change: editResult(func(_, d map[string]any) { d["provider_absence_confirmed"] = false })},
		{name: "another final state", change: editResult(func(_, d map[string]any) { d["final_lifecycle_state"] = "failed" })},
		{name: "no target instance", change: func(_ *testing.T, o *store.OperationRecord, _ *schemaRuntime) { o.Target.InstanceRef = nil }, want: true},
		{name: "target names another instance", change: func(_ *testing.T, o *store.OperationRecord, _ *schemaRuntime) {
			o.Target.InstanceRef = json.RawMessage(otherDestroyInstance)
		}},
		{name: "result fails its schema", change: func(_ *testing.T, _ *store.OperationRecord, s *schemaRuntime) {
			*s = destroyR3Schemas{schemaRuntime: fixture.base.schemas, rejectURI: destroyR3URN(fixture, "operations:server-destroy-result")}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := destroyR3Terminal(t, fixture)
			var schemas schemaRuntime = fixture.base.schemas
			if test.change != nil {
				test.change(t, &operation, &schemas)
			}
			if got := hasValidServerDestroyResult(operation, stored.Record, schemas); got != test.want {
				t.Fatalf("hasValidServerDestroyResult = %v, want %v", got, test.want)
			}
		})
	}
}

func destroyR3RecordJSON(t *testing.T, server store.LogicalServer) string {
	t.Helper()
	encoded, err := json.Marshal(server)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestDestroyR3ProjectLifecycle(t *testing.T) {
	fixture := newServerDestroyFixture(t)
	current, err := fixture.base.store.GetLogicalServer(startTestEnvID, startTestServerID)
	if err != nil {
		t.Fatal(err)
	}
	type projection = func(*store.OperationRecord, *store.LogicalServer)
	lifecycle := func(state protocolstate.ServerState, status protocolstate.OperationStatus, attempt int) projection {
		return func(o *store.OperationRecord, s *store.LogicalServer) {
			s.LifecycleState, o.Status, o.Steps[2].Attempt = state, status, attempt
		}
	}
	project := func(state protocolstate.ServerState, keepInstance bool) projection {
		return func(_ *store.OperationRecord, s *store.LogicalServer) {
			s.LifecycleState, s.DesiredState = state, "destroyed"
			if !keepInstance {
				s.InstanceRef = nil
			}
		}
	}
	tests := []struct {
		name    string
		change  projection
		want    projection
		changed bool
		err     string
	}{
		{name: "another operation type", change: func(o *store.OperationRecord, _ *store.LogicalServer) { o.OperationType = "server.stop" }, err: destroyR3Projection},
		{name: "another server", change: func(_ *store.OperationRecord, s *store.LogicalServer) { s.ServerID = "other" }, err: destroyR3Projection},
		{name: "another environment", change: func(_ *store.OperationRecord, s *store.LogicalServer) { s.EnvironmentID = "other" }, err: destroyR3Projection},
		{name: "another instance", change: func(o *store.OperationRecord, _ *store.LogicalServer) { o.Target.InstanceRef = json.RawMessage(otherDestroyInstance) }, err: destroyR3Projection},
		{name: "stopped before dispatch", change: func(o *store.OperationRecord, s *store.LogicalServer) {
			s.LifecycleState, o.Status, o.Steps[2].Attempt, o.Steps[2].Status = protocolstate.ServerStopped, protocolstate.OperationRunning, 0, protocolstate.StepPending
		}},
		{name: "stopped after dispatch", change: lifecycle(protocolstate.ServerStopped, protocolstate.OperationRunning, 1), want: project(protocolstate.ServerDestroying, true), changed: true},
		{name: "stopped with a non-pending destroy", change: lifecycle(protocolstate.ServerStopped, protocolstate.OperationRunning, 0), want: project(protocolstate.ServerDestroying, true), changed: true},
		{name: "failed after dispatch", change: lifecycle(protocolstate.ServerFailed, protocolstate.OperationRunning, 1), want: project(protocolstate.ServerDestroying, true), changed: true},
		{name: "ready server", change: lifecycle(protocolstate.ServerReady, protocolstate.OperationRunning, 1)},
		{name: "destroying while running", change: lifecycle(protocolstate.ServerDestroying, protocolstate.OperationRunning, 1)},
		{name: "destroying and succeeded", change: lifecycle(protocolstate.ServerDestroying, protocolstate.OperationSucceeded, 1), want: project(protocolstate.ServerDestroyed, false), changed: true},
		{name: "destroying and succeeded without evidence", change: func(o *store.OperationRecord, s *store.LogicalServer) {
			lifecycle(protocolstate.ServerDestroying, protocolstate.OperationSucceeded, 1)(o, s)
			o.Result = json.RawMessage(`{}`)
		}, err: destroyR3TerminalResult},
		{name: "destroying and failed", change: lifecycle(protocolstate.ServerDestroying, protocolstate.OperationFailed, 1), want: project(protocolstate.ServerFailed, true), changed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := destroyR3Terminal(t, fixture)
			server := current
			test.change(&operation, &server.Record)
			want := server.Record
			if test.want != nil {
				test.want(&operation, &want)
			}
			if test.err != "" {
				want = store.LogicalServer{}
			}
			next, changed, err := fixture.workflow.ProjectLifecycle(operation, server)
			destroyR3WantError(t, "ProjectLifecycle", err, test.err)
			if changed != test.changed || destroyR3RecordJSON(t, next) != destroyR3RecordJSON(t, want) {
				t.Fatalf("ProjectLifecycle = %s, %v; want %s, %v", destroyR3RecordJSON(t, next), changed, destroyR3RecordJSON(t, want), test.changed)
			}
		})
	}
}
