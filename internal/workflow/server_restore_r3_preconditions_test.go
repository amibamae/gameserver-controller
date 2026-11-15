package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests call ServerRestoreWorkflow.CheckPreconditions directly on a
// planned explicit-start restore and pin the exact error, in check order, for
// each planned step.

func restoreR3Check(fixture *restoreR3Fixture, stepID string) error {
	return fixture.workflow.CheckPreconditions(context.Background(), fixture.operation, *fixture.step(stepID))
}

// restoreR3SetOutputField rewrites one field of a recorded step output.
func restoreR3SetOutputField(t *testing.T, fixture *restoreR3Fixture, stepID, field string, value any) {
	t.Helper()
	var output map[string]any
	if err := json.Unmarshal(fixture.step(stepID).Output, &output); err != nil {
		t.Fatal(err)
	}
	output[field] = value
	fixture.step(stepID).Output = restoreR3JSON(t, output)
}

// restoreR3StepExpectations are one step's errors for the per-step scenarios.
type restoreR3StepExpectations struct{ pending, directEmpty, allDirectEmpty, complete, wrongInstance string }

// restoreR3StepCase is one CheckPreconditions scenario for one planned step.
type restoreR3StepCase struct {
	stepID, name, want string
	prepare            func(*testing.T, *restoreR3Fixture)
}

func restoreR3NoChange(*testing.T, *restoreR3Fixture) {}

// restoreR3StepScenarios lists the per-step scenarios for the step at index.
func restoreR3StepScenarios(index int, want restoreR3StepExpectations) []restoreR3StepCase {
	stepID := restoreR3PlanOrder[index]
	complete := func(t *testing.T, fixture *restoreR3Fixture) { restoreR3CompleteThrough(t, fixture, stepID) }
	cases := []restoreR3StepCase{{stepID, "pending", want.pending, restoreR3NoChange}}
	if index > 0 {
		direct := restoreR3PlanOrder[index-1]
		cases = append(cases,
			restoreR3StepCase{stepID, "direct prerequisite empty", want.directEmpty, func(_ *testing.T, fixture *restoreR3Fixture) {
				restoreR3Succeed(fixture, direct, nil)
			}},
			restoreR3StepCase{stepID, "all prerequisites, direct empty", want.allDirectEmpty, func(t *testing.T, fixture *restoreR3Fixture) {
				complete(t, fixture)
				fixture.step(direct).Output = nil
			}})
	}
	return append(cases,
		restoreR3StepCase{stepID, "complete", want.complete, complete},
		restoreR3StepCase{stepID, "wrong instance", want.wrongInstance, func(t *testing.T, fixture *restoreR3Fixture) {
			complete(t, fixture)
			fixture.server().InstanceRef = json.RawMessage(restoreR3OtherInstance)
		}},
		restoreR3StepCase{stepID, "operation bound to another instance", store.ErrLogicalServerTargetMismatch.Error(), func(t *testing.T, fixture *restoreR3Fixture) {
			complete(t, fixture)
			fixture.operation.Target.InstanceRef = json.RawMessage(restoreR3OtherInstance)
		}})
}

// TestServerRestoreR3PreconditionsPerStep pins each step's result when its
// prerequisites are pending, when only its direct prerequisite has succeeded
// with an empty output, when every prerequisite succeeded but the direct one
// has an empty output, when every prerequisite is valid, and when the server
// or the operation has since been bound to another instance.
func TestServerRestoreR3PreconditionsPerStep(t *testing.T) {
	const (
		compatibilityNeedsProvider = "compatibility check requires successful provider inspection"
		compatibilityExactProvider = "restore compatibility check requires the exact provider instance"
		runtimeNeedsInspections    = "runtime inspection requires successful provider and compatibility inspection"
		runtimeExactProvider       = "runtime inspection requires the exact active or stopped provider instance"
		dispatchNeedsCompatibility = "restore dispatch requires the exact backup to pass compatibility checks"
		dispatchNeedsStopped       = "restore dispatch requires the runtime to remain stopped"
		inspectNeedsRestore        = "restore inspection requires an exact completed restore call"
		startNeedsRestore          = "runtime start requires an explicitly requested and verified restore"
		verifyNeedsStart           = "runtime verification requires a completed start of the exact unit"
		readinessNeedsRunning      = "readiness check requires the exact runtime to be confirmed running"
	)
	want := map[string]restoreR3StepExpectations{
		restoreInspectInstanceStep: {complete: "", wrongInstance: "restore requires a stopped or failed server and exact provider inspection"},
		restoreCompatibilityStep:   {compatibilityNeedsProvider, compatibilityExactProvider, compatibilityExactProvider, "", compatibilityExactProvider},
		restoreInspectRuntimeStep:  {runtimeNeedsInspections, runtimeNeedsInspections, "", "", runtimeExactProvider},
		restoreApplyStep:           {dispatchNeedsCompatibility, dispatchNeedsCompatibility, dispatchNeedsStopped, "", "restore streams no longer match the immutable validated plan"},
		restoreInspectStep:         {inspectNeedsRestore, inspectNeedsRestore, inspectNeedsRestore, "", inspectNeedsRestore},
		restoreStartStep:           {startNeedsRestore, startNeedsRestore, startNeedsRestore, "", startNeedsRestore},
		restoreVerifyRuntimeStep:   {verifyNeedsStart, verifyNeedsStart, verifyNeedsStart, "", ""},
		restoreReadinessStep:       {readinessNeedsRunning, readinessNeedsRunning, readinessNeedsRunning, "", ""},
	}
	for index, stepID := range restoreR3PlanOrder {
		for _, test := range restoreR3StepScenarios(index, want[stepID]) {
			t.Run(stepID+"/"+test.name, func(t *testing.T) {
				restoreR3RunStepCase(t, newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart), test)
			})
		}
	}
}

// restoreR3RunStepCase applies the case to a planned fixture and checks the
// step. A passing restore dispatch must recheck the approval exactly once;
// nothing else may consult the authorizer.
func restoreR3RunStepCase(t *testing.T, fixture *restoreR3Fixture, test restoreR3StepCase) {
	t.Helper()
	test.prepare(t, fixture)
	if got := restoreR3ErrText(restoreR3Check(fixture, test.stepID)); got != test.want {
		t.Fatalf("CheckPreconditions = %q; want %q", got, test.want)
	}
	wantCalls := 0
	if test.stepID == restoreApplyStep && test.want == "" {
		wantCalls = 1
	}
	if fixture.authorizer.calls != wantCalls {
		t.Fatalf("authorizer calls = %d; want %d", fixture.authorizer.calls, wantCalls)
	}
}

// TestServerRestoreR3PreconditionsSharedChecks pins the checks every step
// runs before its own rule, in order.
func TestServerRestoreR3PreconditionsSharedChecks(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(*restoreR3Fixture)
		want    string
	}{
		{"wrong operation type", func(f *restoreR3Fixture) { f.operation.OperationType = "server.start" }, "server.restore operation and context are required"},
		{"wrong protocol", func(f *restoreR3Fixture) { f.operation.ProtocolVersion = "0.0.1" }, "server.restore operation and context are required"},
		{"request snapshot type", func(f *restoreR3Fixture) { f.operation.RequestSnapshot.OperationType = "server.start" }, "persisted server.restore request is invalid"},
		{"no backup id", func(f *restoreR3Fixture) {
			f.operation.RequestSnapshot.Parameters = json.RawMessage(`{"replace_existing_data":true}`)
		}, "persisted server.restore request is invalid"},
		{"parameters not JSON", func(f *restoreR3Fixture) { f.operation.RequestSnapshot.Parameters = json.RawMessage(`[`) }, "persisted server.restore request is invalid"},
		{"server missing", func(f *restoreR3Fixture) { f.operation.Target.ServerID = "other" }, store.ErrNotFound.Error()},
		{"server has no instance", func(f *restoreR3Fixture) { f.server().InstanceRef = nil }, store.ErrLogicalServerTargetMismatch.Error()},
		{"requested target instance differs", func(f *restoreR3Fixture) {
			f.operation.RequestSnapshot.Parameters = json.RawMessage(`{"backup_id":"backup-1","replace_existing_data":true,"approval_id":"approval-1","target_instance_ref":` + restoreR3OtherInstance + `}`)
		}, store.ErrLogicalServerTargetMismatch.Error()},
		{"profile unavailable", func(f *restoreR3Fixture) { f.profiles.err = errors.New("no profile") }, "trusted restore profile is unavailable or invalid"},
		{"profile invalid", func(f *restoreR3Fixture) { f.profiles.profile.StorageProfileID = "storage-2" }, "trusted restore profile is unavailable or invalid"},
		{"overwrite intent missing", func(f *restoreR3Fixture) {
			f.operation.RequestSnapshot.Parameters = json.RawMessage(`{"backup_id":"backup-1"}`)
		}, "restore target is not confirmed empty; explicit overwrite intent is required"},
		{"unknown data needs overwrite intent", func(f *restoreR3Fixture) {
			f.profiles.profile.TargetDataKnownEmpty = true
			f.server().DataGenerationStatus = "unknown"
			f.operation.RequestSnapshot.Parameters = json.RawMessage(`{"backup_id":"backup-1"}`)
		}, "restore target is not confirmed empty; explicit overwrite intent is required"},
		{"release fails schema", func(f *restoreR3Fixture) { f.profiles.profile.GameReleaseDescription = json.RawMessage(`{}`) }, "trusted target game release metadata failed schema validation"},
		{"release version differs", func(f *restoreR3Fixture) { f.profiles.profile.GameVersion = "1.21.5" }, "trusted target release metadata does not match the configured game adapter, release, and version"},
		{"unsupported step", func(f *restoreR3Fixture) { f.operation.Steps[0].StepID = "bogus" }, `unsupported server.restore step "bogus"`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
			test.prepare(fixture)
			err := fixture.workflow.CheckPreconditions(context.Background(), fixture.operation, fixture.operation.Steps[0])
			if got := restoreR3ErrText(err); got != test.want {
				t.Fatalf("CheckPreconditions = %q; want %q", got, test.want)
			}
		})
	}
	fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
	var missing context.Context
	if got := restoreR3ErrText(fixture.workflow.CheckPreconditions(missing, fixture.operation, fixture.operation.Steps[0])); got != "server.restore operation and context are required" {
		t.Fatalf("nil context = %q", got)
	}
	var nilWorkflow *ServerRestoreWorkflow
	if got := restoreR3ErrText(nilWorkflow.CheckPreconditions(context.Background(), fixture.operation, fixture.operation.Steps[0])); got != "server.restore operation and context are required" {
		t.Fatalf("nil workflow = %q", got)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fixture.workflow.CheckPreconditions(cancelled, fixture.operation, fixture.operation.Steps[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context = %v", err)
	}
}

// TestServerRestoreR3PreconditionsStepRules pins every distinct rejection
// inside each step's rule, with all earlier steps otherwise valid.
func TestServerRestoreR3PreconditionsStepRules(t *testing.T) {
	hourAgo := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	twoMinutesAgo := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	cases := []struct {
		name, stepID string
		prepare      func(*testing.T, *restoreR3Fixture)
		want         string
		wantCalls    int
	}{
		{name: "inspect running server", stepID: restoreInspectInstanceStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.server().LifecycleState = protocolstate.ServerReady
		}, want: "restore requires a stopped or failed server and exact provider inspection"},
		{name: "inspect failed server", stepID: restoreInspectInstanceStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.server().LifecycleState = protocolstate.ServerFailed
		}},
		{name: "inspect input bound to another instance", stepID: restoreInspectInstanceStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.step(restoreInspectInstanceStep).Input = json.RawMessage(`{"instance_ref":` + restoreR3OtherInstance + `}`)
		}, want: "restore requires a stopped or failed server and exact provider inspection"},
		{name: "compatibility provider running", stepID: restoreCompatibilityStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectInstanceStep, "state", "running")
		}, want: "restore compatibility check requires the exact provider instance"},
		{name: "compatibility provider active", stepID: restoreCompatibilityStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectInstanceStep, "state", "active")
		}},
		{name: "compatibility provider observation stale", stepID: restoreCompatibilityStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectInstanceStep, "observed_at", hourAgo)
		}, want: "restore compatibility check requires the exact provider instance"},
		{name: "runtime provider absent", stepID: restoreInspectRuntimeStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectInstanceStep, "state", "absent")
		}, want: "runtime inspection requires the exact active or stopped provider instance"},
		{name: "runtime provider observation stale", stepID: restoreInspectRuntimeStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectInstanceStep, "observed_at", hourAgo)
		}, want: "runtime inspection requires the exact active or stopped provider instance"},
		{name: "runtime compatibility failed", stepID: restoreInspectRuntimeStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.step(restoreCompatibilityStep).Status = protocolstate.StepFailed
		}, want: "runtime inspection requires successful provider and compatibility inspection"},
		{name: "dispatch while restoring", stepID: restoreApplyStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.server().LifecycleState = protocolstate.ServerRestoring
		}, wantCalls: 1},
		{name: "dispatch while starting", stepID: restoreApplyStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.server().LifecycleState = protocolstate.ServerStarting
		}, want: "restore dispatch requires the exact backup to pass compatibility checks"},
		{name: "dispatch compatibility incompatible", stepID: restoreApplyStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreCompatibilityStep, "compatibility_status", "incompatible")
		}, want: "restore dispatch requires the exact backup to pass compatibility checks"},
		{name: "dispatch compatibility stale", stepID: restoreApplyStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreCompatibilityStep, "observed_at", hourAgo)
		}, want: "restore dispatch requires the exact backup to pass compatibility checks"},
		{name: "dispatch runtime running", stepID: restoreApplyStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectRuntimeStep, "state", "running")
		}, want: "restore dispatch requires the runtime to remain stopped"},
		{name: "dispatch runtime absent", stepID: restoreApplyStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectRuntimeStep, "state", "absent")
		}, wantCalls: 1},
		{name: "dispatch runtime other unit", stepID: restoreApplyStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectRuntimeStep, "unit_ref", json.RawMessage(restoreR3OtherUnit))
		}, want: "restore dispatch requires the runtime to remain stopped"},
		{name: "dispatch runtime observation stale", stepID: restoreApplyStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectRuntimeStep, "observed_at", twoMinutesAgo)
		}, want: "restore dispatch requires the runtime to remain stopped"},
		{name: "dispatch backup gone", stepID: restoreApplyStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.backups.err = errors.New("gone")
		}, want: "exact verified restore backup is no longer available"},
		{name: "dispatch backup unverified", stepID: restoreApplyStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.backups.backup.IntegrityVerified = false
		}, want: "selected backup is not an integrity-verified server.backup result"},
		{name: "dispatch stream plan changed", stepID: restoreApplyStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			var input map[string]any
			if err := json.Unmarshal(f.step(restoreApplyStep).Input, &input); err != nil {
				t.Fatal(err)
			}
			input["target_release_id"] = "paper-2"
			f.step(restoreApplyStep).Input = restoreR3JSON(t, input)
		}, want: "restore streams no longer match the immutable validated plan"},
		{name: "dispatch replace without approval", stepID: restoreApplyStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.operation.RequestSnapshot.Parameters = json.RawMessage(`{"backup_id":"backup-1","replace_existing_data":true,"start_after_restore":true}`)
		}, want: "replace_existing_data requires an approval_id"},
		{name: "dispatch approval refused", stepID: restoreApplyStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.authorizer.err = errors.New("denied")
		}, want: "revalidate exact server.restore approval: denied", wantCalls: 1},
		{name: "dispatch empty target needs no approval", stepID: restoreApplyStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.profiles.profile.TargetDataKnownEmpty = true
			f.operation.RequestSnapshot.Parameters = json.RawMessage(`{"backup_id":"backup-1","start_after_restore":true}`)
		}},
		{name: "inspect restore rejected", stepID: restoreInspectStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreApplyStep, "outcome", "rejected")
		}, want: "restore inspection requires an exact completed restore call"},
		{name: "start not requested", stepID: restoreStartStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.operation.RequestSnapshot.Parameters = json.RawMessage(restoreR3Replace)
		}, want: "runtime start requires an explicitly requested and verified restore"},
		{name: "start while stopped", stepID: restoreStartStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			f.server().LifecycleState = protocolstate.ServerStopped
		}, want: "runtime start requires an explicitly requested and verified restore"},
		{name: "start inspection failed check", stepID: restoreStartStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreInspectStep, "validation_checks", []any{map[string]any{"check_id": "world.other", "result": "pass", "observed_at": restoreR3Now()}})
		}, want: "runtime start requires an explicitly requested and verified restore"},
		{name: "start readiness undeclared", stepID: restoreStartStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			game := f.adapters.descriptors["game.paper"]
			game.Capabilities = game.Capabilities[:3]
			f.adapters.descriptors["game.paper"] = game
		}, want: "runtime start requires the current trusted readiness policy"},
		{name: "start game adapter missing", stepID: restoreStartStep, prepare: func(_ *testing.T, f *restoreR3Fixture) {
			delete(f.adapters.descriptors, "game.paper")
		}, want: "runtime start requires the current trusted readiness policy"},
		{name: "verify start not completed", stepID: restoreVerifyRuntimeStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreStartStep, "outcome", "accepted")
		}, want: "runtime verification requires a completed start of the exact unit"},
		{name: "verify start not running", stepID: restoreVerifyRuntimeStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreStartStep, "state", "starting")
		}, want: "runtime verification requires a completed start of the exact unit"},
		{name: "verify start other unit", stepID: restoreVerifyRuntimeStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreStartStep, "unit_ref", json.RawMessage(restoreR3OtherUnit))
		}, want: "runtime verification requires a completed start of the exact unit"},
		{name: "readiness runtime stopped", stepID: restoreReadinessStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreVerifyRuntimeStep, "state", "stopped")
		}, want: "readiness check requires the exact runtime to be confirmed running"},
		{name: "readiness runtime other unit", stepID: restoreReadinessStep, prepare: func(t *testing.T, f *restoreR3Fixture) {
			restoreR3SetOutputField(t, f, restoreVerifyRuntimeStep, "unit_ref", json.RawMessage(restoreR3OtherUnit))
		}, want: "readiness check requires the exact runtime to be confirmed running"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPlannedRestoreR3Fixture(t, restoreR3ReplaceStart)
			restoreR3CompleteThrough(t, fixture, test.stepID)
			test.prepare(t, fixture)
			if got := restoreR3ErrText(restoreR3Check(fixture, test.stepID)); got != test.want {
				t.Fatalf("CheckPreconditions = %q; want %q", got, test.want)
			}
			if fixture.authorizer.calls != test.wantCalls {
				t.Fatalf("authorizer calls = %d; want %d", fixture.authorizer.calls, test.wantCalls)
			}
		})
	}
}
