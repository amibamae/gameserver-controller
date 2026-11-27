package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type startStopR3FailureCase struct {
	name   string
	step   string
	change func(*store.OperationRecord)
	want   ResolutionState
	error  string
}

func startStopR3Unmet(message, operationID string) string {
	return fmt.Sprintf(`{"category":"safety_blocked","code":"core.safety.precondition_unmet","message":%q,"operation_id":%q,"recovery":{"action":"inspect_operation","operator_required":false},"retryable":false}`, message, operationID)
}

func startStopR3OpStep(id string, change func(*store.OperationStep)) func(*store.OperationRecord) {
	return func(o *store.OperationRecord) { startStopR3SetStep(o, id, change) }
}

type startStopR3FailureHandler func(context.Context, store.OperationRecord, store.OperationStep, error) (Resolution, error)

func startStopR3RunFailures(t *testing.T, planned func(*testing.T, *startStopR3Fixture) store.OperationRecord,
	handler func(*startStopR3Fixture) startStopR3FailureHandler, cases []startStopR3FailureCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := planned(t, f)
			if test.change != nil {
				test.change(&operation)
			}
			resolution, err := handler(f)(context.Background(), operation, operationStep(operation, test.step), errors.New("unmet"))
			startStopR3CheckFailure(t, resolution, err, test)
		})
	}
}

func startStopR3CheckFailure(t *testing.T, resolution Resolution, err error, test startStopR3FailureCase) {
	t.Helper()
	if err != nil || resolution.State != test.want || string(resolution.Error) != test.error {
		t.Fatalf("HandlePreconditionFailure = %#v, %v; want %s %s", resolution, err, test.want, test.error)
	}
	if resolution.Output != nil || resolution.ExternalOperationRef != nil {
		t.Fatalf("HandlePreconditionFailure carries output or reference: %#v", resolution)
	}
}

// TestStartStopR3StartHandlePreconditionFailure pins every terminal and
// suspended server.start precondition-failure outcome.
func TestStartStopR3StartHandlePreconditionFailure(t *testing.T) {
	startInactive := startStopR3Unmet("The exact provider instance is not active; no runtime start was issued.", "server.start-r3")
	startNotRunning := startStopR3Unmet("The exact execution unit is not confirmed running; game readiness was not checked.", "server.start-r3")
	startCases := []startStopR3FailureCase{
		{name: "inspect step", step: startInspectInstanceStep, want: ResolutionNeedsOperator},
		{name: "verify step", step: startVerifyRuntimeStep, want: ResolutionNeedsOperator},
		{name: "runtime with active instance", step: startRuntimeStep, want: ResolutionNeedsOperator},
		{name: "runtime with unknown instance state", step: startRuntimeStep, change: startStopR3OpStep(startInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, "unknown"))), want: ResolutionNeedsOperator},
		{name: "runtime with failed inspection", step: startRuntimeStep, change: startStopR3OpStep(startInspectInstanceStep, startStopR3Status(protocolstate.StepFailed)), want: ResolutionNeedsOperator},
		{name: "runtime with inspection of another instance", step: startRuntimeStep, change: startStopR3OpStep(startInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startStopR3OtherInstance, "stopped"))), want: ResolutionNeedsOperator},
		{name: "runtime with undecodable inspection", step: startRuntimeStep, change: startStopR3OpStep(startInspectInstanceStep, startStopR3Output(`[]`)), want: ResolutionNeedsOperator},
	}
	for _, state := range []string{"creating", "stopped", "destroying", "destroyed", "failed"} {
		startCases = append(startCases, startStopR3FailureCase{name: "runtime with " + state + " instance", step: startRuntimeStep,
			change: startStopR3OpStep(startInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, state))), want: ResolutionDefinitiveError, error: startInactive})
	}
	startCases = append(startCases,
		startStopR3FailureCase{name: "readiness with running unit", step: startReadinessStep, want: ResolutionNeedsOperator},
		startStopR3FailureCase{name: "readiness with unknown unit", step: startReadinessStep, change: startStopR3OpStep(startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, "unknown"))), want: ResolutionNeedsOperator},
		startStopR3FailureCase{name: "readiness with another unit", step: startReadinessStep, change: startStopR3OpStep(startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startStopR3OtherUnit, "stopped"))), want: ResolutionNeedsOperator},
		startStopR3FailureCase{name: "readiness without verification", step: startReadinessStep, change: startStopR3OpStep(startVerifyRuntimeStep, startStopR3Status(protocolstate.StepPending)), want: ResolutionNeedsOperator},
	)
	for _, state := range []string{"absent", "stopped", "starting", "stopping", "failed"} {
		startCases = append(startCases, startStopR3FailureCase{name: "readiness with " + state + " unit", step: startReadinessStep,
			change: startStopR3OpStep(startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, state))), want: ResolutionDefinitiveError, error: startNotRunning})
	}
	startStopR3RunFailures(t, startStopR3PlannedStart, func(f *startStopR3Fixture) startStopR3FailureHandler { return f.start.HandlePreconditionFailure }, startCases)
}

// TestStartStopR3StopHandlePreconditionFailure pins every terminal and
// suspended server.stop precondition-failure outcome.
func TestStartStopR3StopHandlePreconditionFailure(t *testing.T) {
	stopInactive := startStopR3Unmet("The exact provider instance is not active; no game-save or runtime-stop call was issued.", "server.stop-r3")
	stopNotRunning := startStopR3Unmet("The exact execution unit is not running; no game-save or stop call was issued.", "server.stop-r3")
	stopRejected := startStopR3Unmet("The game adapter rejected its shutdown save; the execution unit was not stopped.", "server.stop-r3")
	stopCases := []startStopR3FailureCase{
		{name: "inspect step", step: stopInspectInstanceStep, want: ResolutionNeedsOperator},
		{name: "unit inspect with active instance", step: stopInspectUnitStep, want: ResolutionDefinitiveError, error: startStopR3Unmet("", "server.stop-r3")},
		{name: "unit inspect with unknown instance state", step: stopInspectUnitStep, change: startStopR3OpStep(stopInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, "unknown"))), want: ResolutionNeedsOperator},
		{name: "unit inspect with another instance", step: stopInspectUnitStep, change: startStopR3OpStep(stopInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startStopR3OtherInstance, "stopped"))), want: ResolutionNeedsOperator},
		{name: "unit inspect without provider inspection", step: stopInspectUnitStep, change: startStopR3OpStep(stopInspectInstanceStep, startStopR3Status(protocolstate.StepRunning)), want: ResolutionNeedsOperator},
		{name: "save with running unit", step: stopSaveGameStep, want: ResolutionNeedsOperator},
		{name: "save with unknown unit", step: stopSaveGameStep, change: startStopR3OpStep(stopInspectUnitStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, "unknown"))), want: ResolutionNeedsOperator},
		{name: "save with another unit", step: stopSaveGameStep, change: startStopR3OpStep(stopInspectUnitStep, startStopR3Output(startStopR3UnitOutput(startStopR3OtherUnit, "stopped"))), want: ResolutionNeedsOperator},
		{name: "save without unit inspection", step: stopSaveGameStep, change: startStopR3OpStep(stopInspectUnitStep, startStopR3Status(protocolstate.StepFailed)), want: ResolutionNeedsOperator},
		{name: "save with unknown instance state", step: stopSaveGameStep, change: startStopR3OpStep(stopInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, "unknown"))), want: ResolutionNeedsOperator},
		{name: "stop with confirmed save", step: stopRuntimeStep, want: ResolutionNeedsOperator},
		{name: "stop with rejected save", step: stopRuntimeStep, change: startStopR3OpStep(stopSaveGameStep, startStopR3Output(startStopR3SaveOutput(startTestServerID, startTestInstance, startTestRelease, "rejected"))),
			want: ResolutionDefinitiveError, error: stopRejected},
		{name: "stop with rejected save for another release", step: stopRuntimeStep, change: startStopR3OpStep(stopSaveGameStep, startStopR3Output(startStopR3SaveOutput(startTestServerID, startTestInstance, "other", "rejected"))),
			want: ResolutionNeedsOperator},
		{name: "unknown step", step: "", want: ResolutionNeedsOperator},
	}
	for _, state := range []string{"creating", "stopped", "destroying", "destroyed", "failed"} {
		for _, id := range []string{stopInspectUnitStep, stopSaveGameStep} {
			stopCases = append(stopCases, startStopR3FailureCase{name: id + " with " + state + " instance", step: id,
				change: startStopR3OpStep(stopInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, state))), want: ResolutionDefinitiveError, error: stopInactive})
		}
	}
	for _, state := range []string{"absent", "stopped", "failed"} {
		stopCases = append(stopCases, startStopR3FailureCase{name: "save with " + state + " unit", step: stopSaveGameStep,
			change: startStopR3OpStep(stopInspectUnitStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, state))), want: ResolutionDefinitiveError, error: stopNotRunning})
	}
	startStopR3RunFailures(t, startStopR3PlannedStop, func(f *startStopR3Fixture) startStopR3FailureHandler { return f.stop.HandlePreconditionFailure }, stopCases)
}

type startStopR3Projection struct {
	state   protocolstate.ServerState
	desired string
	changed bool
	err     string
}

func startStopR3CheckProjection(t *testing.T, next store.LogicalServer, changed bool, err error, want startStopR3Projection) {
	t.Helper()
	if got := startStopR3ErrText(err); got != want.err {
		t.Fatalf("error = %q; want %q", got, want.err)
	}
	if err != nil {
		if changed || next.ServerID != "" {
			t.Fatalf("error projection = %#v, %v", next, changed)
		}
		return
	}
	if next.LifecycleState != want.state || next.DesiredState != want.desired || changed != want.changed {
		t.Fatalf("projection = %s/%s/%v; want %+v", next.LifecycleState, next.DesiredState, changed, want)
	}
	if next.ServerID != startTestServerID || !equalJSON(next.InstanceRef, json.RawMessage(startTestInstance)) {
		t.Fatalf("projection lost the record: %#v", next)
	}
}

// TestStartStopR3StartProjectLifecycle pins every server.start lifecycle
// projection branch.
func TestStartStopR3StartProjectLifecycle(t *testing.T) {
	type result = startStopR3Projection
	none := func(state protocolstate.ServerState) result { return result{state: state, desired: "stopped"} }
	cases := []struct {
		name   string
		state  protocolstate.ServerState
		change func(*startStopR3Fixture, *store.OperationRecord)
		want   result
	}{
		{name: "wrong operation type", state: protocolstate.ServerStopped, change: startStopR3Op(func(o *store.OperationRecord) { o.OperationType = "server.stop" }),
			want: result{err: "server.start lifecycle projection target mismatch"}},
		{name: "other server", state: protocolstate.ServerStopped, change: startStopR3Op(func(o *store.OperationRecord) { o.Target.ServerID = "other" }),
			want: result{err: "server.start lifecycle projection target mismatch"}},
		{name: "other environment", state: protocolstate.ServerStopped, change: startStopR3Op(func(o *store.OperationRecord) { o.Target.EnvironmentID = "other" }),
			want: result{err: "server.start lifecycle projection target mismatch"}},
		{name: "stopped with pending start", state: protocolstate.ServerStopped,
			change: func(_ *startStopR3Fixture, o *store.OperationRecord) {
				startStopR3SetStep(o, startRuntimeStep, startStopR3Status(protocolstate.StepPending))
			},
			want: none(protocolstate.ServerStopped)},
		{name: "stopped with dispatched start", state: protocolstate.ServerStopped, want: result{state: protocolstate.ServerStarting, desired: "running", changed: true}},
		{name: "starting and failed", state: protocolstate.ServerStarting,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Status = protocolstate.OperationFailed }),
			want:   result{state: protocolstate.ServerFailed, desired: "stopped", changed: true}},
		{name: "starting with undecodable verification", state: protocolstate.ServerStarting,
			change: func(_ *startStopR3Fixture, o *store.OperationRecord) {
				startStopR3SetStep(o, startVerifyRuntimeStep, startStopR3Output(`[]`))
			},
			want: result{err: "execution unit verification output cannot be decoded"}},
		{name: "starting with failed unit", state: protocolstate.ServerStarting,
			change: func(_ *startStopR3Fixture, o *store.OperationRecord) {
				startStopR3SetStep(o, startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, "failed")))
			},
			want: result{state: protocolstate.ServerFailed, desired: "stopped", changed: true}},
		{name: "starting with pending readiness", state: protocolstate.ServerStarting, want: none(protocolstate.ServerStarting)},
		{name: "starting with pending verification and readiness", state: protocolstate.ServerStarting,
			change: func(_ *startStopR3Fixture, o *store.OperationRecord) {
				startStopR3SetStep(o, startVerifyRuntimeStep, func(s *store.OperationStep) { s.Status = protocolstate.StepPending; s.Output = json.RawMessage(`[]`) })
			},
			want: none(protocolstate.ServerStarting)},
		{name: "starting with dispatched readiness", state: protocolstate.ServerStarting,
			change: func(_ *startStopR3Fixture, o *store.OperationRecord) {
				startStopR3SetStep(o, startReadinessStep, startStopR3Status(protocolstate.StepRunning))
			},
			want: result{state: protocolstate.ServerValidating, desired: "running", changed: true}},
		{name: "validating and running", state: protocolstate.ServerValidating, want: none(protocolstate.ServerValidating)},
		{name: "validating and failed", state: protocolstate.ServerValidating,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Status = protocolstate.OperationFailed }),
			want:   result{state: protocolstate.ServerDegraded, desired: "stopped", changed: true}},
		{name: "validating and succeeded without result", state: protocolstate.ServerValidating,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Status = protocolstate.OperationSucceeded }),
			want:   result{err: "terminal server.start result lacks its exact readiness assertion"}},
		{name: "ready is unchanged", state: protocolstate.ServerReady,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Status = protocolstate.OperationFailed }),
			want:   none(protocolstate.ServerReady)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3PlannedStart(t, f)
			f.server().LifecycleState = test.state
			if test.change != nil {
				test.change(f, &operation)
			}
			next, changed, err := f.start.ProjectLifecycle(operation, f.servers.record)
			startStopR3CheckProjection(t, next, changed, err, test.want)
		})
	}
}

func startStopR3ExpectResult(t *testing.T, got json.RawMessage, err error, want string) {
	t.Helper()
	if err != nil || string(got) != want {
		t.Fatalf("result = %s, %v\nwant %s", got, err, want)
	}
}

// startStopR3FailureShape is the variable part of a failure result.
type startStopR3FailureShape struct {
	operationType, summary, affected, completed, failed, unresolved string
	partial                                                         bool
}

func (shape startStopR3FailureShape) json() string {
	return fmt.Sprintf(`{"affected_resources":[%s],"audit_event_ids":[],"completed_steps":[%s],"failed_steps":[%s],"operation_type":%q,"outcome_summary":%q,"partial":%t,"unresolved_conditions":[%s],"warnings":[]}`,
		shape.affected, shape.completed, shape.failed, shape.operationType, shape.summary, shape.partial, shape.unresolved)
}

const startStopR3Failure = `{"code":"x"}`

// TestStartStopR3StartFailureResults pins the exact server.start failure
// result bytes.
func TestStartStopR3StartFailureResults(t *testing.T) {
	base := startStopR3FailureShape{
		operationType: "server.start", summary: "Server start did not satisfy all runtime and readiness postconditions.",
		completed: `{"step_id":"inspect-instance","step_type":"hosting.instance.inspect"},{"step_id":"start-runtime","step_type":"execution.unit.start"},{"step_id":"verify-runtime","step_type":"execution.unit.inspect"}`,
	}
	postcondition := `{"condition_id":"server.start.postcondition_unmet","description":"The adapter calls completed, but the exact running and readiness postconditions were not satisfied."}`
	f := newStartStopR3Fixture(t)
	operation := startStopR3PlannedStart(t, f)
	failed := operationStep(operation, startReadinessStep)
	failed.Status = protocolstate.StepFailed
	got, err := f.start.BuildFailureResult(context.Background(), operation, failed, json.RawMessage(startStopR3Failure))
	want := base
	want.affected, want.partial = startTestInstance, true
	want.failed = `{"error":{"code":"x"},"step_id":"check-readiness","step_type":"game.readiness.check"}`
	startStopR3ExpectResult(t, got, err, want.json())

	operation.Error = json.RawMessage(`{"code":"core.safety.precondition_unmet","message":"blocked"}`)
	operation.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance)
	startStopR3SetStep(&operation, startRuntimeStep, func(s *store.OperationStep) { s.Attempt = 0 })
	got, err = f.start.BuildFailureResult(context.Background(), operation, store.OperationStep{}, json.RawMessage(startStopR3Failure))
	want = base
	want.affected, want.unresolved = startStopR3OtherInstance, `{"condition_id":"server.start.precondition_unmet","description":"blocked"}`
	startStopR3ExpectResult(t, got, err, want.json())

	operation.Error = json.RawMessage(`{"code":"core.safety.precondition_unmet","message":""}`)
	got, err = f.start.BuildFailureResult(context.Background(), operation, store.OperationStep{}, nil)
	want.unresolved = postcondition
	startStopR3ExpectResult(t, got, err, want.json())

	operation.Error = nil
	operation.Target = store.OperationTarget{ServerID: "missing", EnvironmentID: startTestEnvID}
	got, err = f.start.BuildFailureResult(context.Background(), operation, operationStep(operation, startReadinessStep), json.RawMessage(startStopR3Failure))
	startStopR3ExpectResult(t, got, err, base.json())
	got, err = f.start.BuildFailureResult(context.Background(), operation, store.OperationStep{}, nil)
	want = base
	want.unresolved = postcondition
	startStopR3ExpectResult(t, got, err, want.json())
}

// TestStartStopR3StopFailureResults pins the exact server.stop failure result
// bytes.
func TestStartStopR3StopFailureResults(t *testing.T) {
	base := startStopR3FailureShape{
		operationType: "server.stop", summary: "Server stop did not confirm both the game save and runtime termination.",
		completed: `{"step_id":"inspect-instance","step_type":"hosting.instance.inspect"},{"step_id":"inspect-runtime","step_type":"execution.unit.inspect"}`,
	}
	f := newStartStopR3Fixture(t)
	operation := startStopR3PlannedStop(t, f)
	failed := operationStep(operation, stopRuntimeStep)
	failed.Status = protocolstate.StepFailed
	got, err := f.stop.BuildFailureResult(context.Background(), operation, failed, json.RawMessage(startStopR3Failure))
	want := base
	want.completed += `,{"step_id":"save-game-data","step_type":"game.save.request"}`
	want.failed, want.partial = `{"error":{"code":"x"},"step_id":"stop-runtime","step_type":"execution.unit.graceful_stop"}`, true
	startStopR3ExpectResult(t, got, err, want.json())

	operation.Target.InstanceRef = json.RawMessage(startTestInstance)
	startStopR3SetStep(&operation, stopSaveGameStep, startStopR3Status(protocolstate.StepPending))
	got, err = f.stop.BuildFailureResult(context.Background(), operation, store.OperationStep{}, nil)
	want = base
	want.affected = startTestInstance
	want.unresolved = `{"condition_id":"server.stop.postcondition_unmet","description":"The operation did not establish exact game-save and stopped-unit evidence."}`
	startStopR3ExpectResult(t, got, err, want.json())

	startStopR3SetStep(&operation, stopRuntimeStep, func(s *store.OperationStep) { s.Attempt = 1 })
	got, err = f.stop.BuildFailureResult(context.Background(), operation, operationStep(operation, stopRuntimeStep), json.RawMessage(startStopR3Failure))
	want = base
	want.affected, want.partial = startTestInstance, true
	startStopR3ExpectResult(t, got, err, want.json())
}
