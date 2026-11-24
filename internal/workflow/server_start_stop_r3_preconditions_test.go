package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type startStopR3PreconditionCase struct {
	name    string
	step    string
	attempt int
	state   protocolstate.ServerState
	change  func(*startStopR3Fixture, *store.OperationRecord)
	want    string
}

// startStopR3CheckTarget binds a workflow's planned fixture to its
// CheckPreconditions.
type startStopR3CheckTarget struct {
	planned   func(*testing.T, *startStopR3Fixture) store.OperationRecord
	check     func(*startStopR3Fixture) func(context.Context, store.OperationRecord, store.OperationStep) error
	nilCheck  func(context.Context, store.OperationRecord, store.OperationStep) error
	firstStep string
	initErr   string
}

var (
	startStopR3StartChecks = startStopR3CheckTarget{
		planned: startStopR3PlannedStart,
		check: func(f *startStopR3Fixture) func(context.Context, store.OperationRecord, store.OperationStep) error {
			return f.start.CheckPreconditions
		},
		nilCheck:  (*ServerStartWorkflow)(nil).CheckPreconditions,
		firstStep: startInspectInstanceStep, initErr: "server.start operation and context are required",
	}
	startStopR3StopChecks = startStopR3CheckTarget{
		planned: startStopR3PlannedStop,
		check: func(f *startStopR3Fixture) func(context.Context, store.OperationRecord, store.OperationStep) error {
			return f.stop.CheckPreconditions
		},
		nilCheck:  (*ServerStopWorkflow)(nil).CheckPreconditions,
		firstStep: stopInspectInstanceStep, initErr: "server.stop operation and context are required",
	}
)

func startStopR3RunPreconditions(t *testing.T, target startStopR3CheckTarget, cases []startStopR3PreconditionCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := target.planned(t, f)
			f.server().LifecycleState = test.state
			if test.change != nil {
				test.change(f, &operation)
			}
			step := store.OperationStep{StepID: test.step}
			if test.step != "other-step" {
				step = operationStep(operation, test.step)
			}
			step.Attempt = test.attempt
			err := target.check(f)(context.Background(), operation, step)
			if got := startStopR3ErrText(err); got != test.want {
				t.Fatalf("CheckPreconditions = %q; want %q", got, test.want)
			}
		})
	}
	startStopR3CheckPreconditionContext(t, target)
}

// startStopR3CheckPreconditionContext pins the nil workflow, nil context and
// cancelled context results.
func startStopR3CheckPreconditionContext(t *testing.T, target startStopR3CheckTarget) {
	t.Helper()
	f := newStartStopR3Fixture(t)
	operation := target.planned(t, f)
	step := operationStep(operation, target.firstStep)
	var missing context.Context
	if err := target.check(f)(missing, operation, step); startStopR3ErrText(err) != target.initErr {
		t.Fatalf("nil context = %v", err)
	}
	if err := target.nilCheck(context.Background(), operation, step); startStopR3ErrText(err) != target.initErr {
		t.Fatalf("nil workflow = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := target.check(f)(cancelled, operation, step); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context = %v", err)
	}
}

// TestStartStopR3StartCheckPreconditions pins the exact result of every
// server.start precondition rule.
func TestStartStopR3StartCheckPreconditions(t *testing.T) {
	const mismatch = "operation target does not match the current logical-server instance"
	const runtimeStart = "runtime start requires a stopped server or confirmed start retry and a successful provider inspection"
	const runtimeActive = "runtime start requires the exact provider instance to be active"
	const runtimeSame = "runtime start and verification must target the same planned unit"
	const verifyStart = "runtime inspection requires a completed start command"
	const verifySame = "runtime verification must target the exact started unit"
	const readinessInspect = "game readiness requires a successful exact-unit runtime inspection"
	const readinessRunning = "game readiness requires the execution unit to be confirmed running"
	const readinessBound = "readiness request must be bound to the verified unit and current instance"
	stopped, starting := protocolstate.ServerStopped, protocolstate.ServerStarting
	cases := []startStopR3PreconditionCase{
		{name: "wrong operation type", step: startInspectInstanceStep, state: stopped, change: startStopR3Op(func(o *store.OperationRecord) { o.OperationType = "server.stop" }),
			want: "server.start operation and context are required"},
		{name: "wrong protocol", step: startInspectInstanceStep, state: stopped, change: startStopR3Op(func(o *store.OperationRecord) { o.ProtocolVersion = "0.0.1" }),
			want: "server.start operation and context are required"},
		{name: "unknown server", step: startInspectInstanceStep, state: stopped, change: startStopR3Op(func(o *store.OperationRecord) { o.Target.ServerID = "missing" }),
			want: "operation not found"},
		{name: "server without instance", step: startInspectInstanceStep, state: stopped, change: startStopR3Server(func(s *store.LogicalServer) { s.InstanceRef = nil }), want: mismatch},
		{name: "target instance mismatch", step: startRuntimeStep, state: stopped,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance) }), want: mismatch},
		{name: "matching target instance", step: startInspectInstanceStep, state: stopped,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Target.InstanceRef = json.RawMessage(startTestInstance) })},
		{name: "unsupported step", step: "other-step", state: stopped, want: `unsupported server.start step "other-step"`},
		{name: "inspect ok", step: startInspectInstanceStep, state: stopped},
		{name: "inspect bound to another instance", step: startInspectInstanceStep, state: stopped,
			change: startStopR3Step(startInspectInstanceStep, startStopR3Input(`{"instance_ref":`+startStopR3OtherInstance+`}`)),
			want:   "provider inspection is not bound to the current managed instance"},
		{name: "inspect binding checked before state", step: startInspectInstanceStep, state: starting,
			change: startStopR3Step(startInspectInstanceStep, startStopR3Input(`[]`)),
			want:   "provider inspection is not bound to the current managed instance"},
		{name: "inspect while starting", step: startInspectInstanceStep, state: starting,
			want: "server must remain stopped until the provider instance is confirmed active"},
		{name: "runtime first attempt", step: startRuntimeStep, state: stopped},
		{name: "runtime confirmed retry", step: startRuntimeStep, attempt: 2, state: starting},
		{name: "runtime first attempt while starting", step: startRuntimeStep, state: starting, want: runtimeStart},
		{name: "runtime retry while stopped", step: startRuntimeStep, attempt: 1, state: stopped, want: runtimeStart},
		{name: "runtime while ready", step: startRuntimeStep, state: protocolstate.ServerReady, want: runtimeStart},
		{name: "runtime without successful inspection", step: startRuntimeStep, state: stopped,
			change: startStopR3Step(startInspectInstanceStep, startStopR3Status(protocolstate.StepFailed)), want: runtimeStart},
		{name: "runtime without inspection step", step: startRuntimeStep, state: stopped, change: startStopR3RemoveStep(startInspectInstanceStep), want: runtimeStart},
		{name: "runtime with undecodable inspection", step: startRuntimeStep, state: stopped,
			change: startStopR3Step(startInspectInstanceStep, startStopR3Output(`[]`)), want: runtimeActive},
		{name: "runtime with inspection of another instance", step: startRuntimeStep, state: stopped,
			change: startStopR3Step(startInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startStopR3OtherInstance, "active"))), want: runtimeActive},
		{name: "runtime with stopped instance", step: startRuntimeStep, state: stopped,
			change: startStopR3Step(startInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, "stopped"))), want: runtimeActive},
		{name: "runtime with verification of another unit", step: startRuntimeStep, state: stopped,
			change: startStopR3Step(startVerifyRuntimeStep, startStopR3Input(`{"unit_ref":`+startStopR3OtherUnit+`}`)), want: runtimeSame},
		{name: "runtime without verification step", step: startRuntimeStep, state: stopped, change: startStopR3RemoveStep(startVerifyRuntimeStep), want: runtimeSame},
		{name: "verify ok", step: startVerifyRuntimeStep, state: starting},
		{name: "verify while stopped", step: startVerifyRuntimeStep, state: stopped, want: verifyStart},
		{name: "verify with pending start", step: startVerifyRuntimeStep, state: starting,
			change: startStopR3Step(startRuntimeStep, startStopR3Status(protocolstate.StepPending)), want: verifyStart},
		{name: "verify with accepted start", step: startVerifyRuntimeStep, state: starting,
			change: startStopR3Step(startRuntimeStep, startStopR3Output(startStopR3UnitResult(startTestUnit, "accepted", "running"))), want: verifyStart},
		{name: "verify with start not running", step: startVerifyRuntimeStep, state: starting,
			change: startStopR3Step(startRuntimeStep, startStopR3Output(startStopR3UnitResult(startTestUnit, "completed", "starting"))), want: verifyStart},
		{name: "verify with start of another unit", step: startVerifyRuntimeStep, state: starting,
			change: startStopR3Step(startRuntimeStep, startStopR3Output(startStopR3UnitResult(startStopR3OtherUnit, "completed", "running"))), want: verifyStart},
		{name: "verify with undecodable start output", step: startVerifyRuntimeStep, state: starting,
			change: startStopR3Step(startRuntimeStep, startStopR3Output(`[]`)), want: verifyStart},
		{name: "verify of another unit", step: startVerifyRuntimeStep, state: starting,
			change: startStopR3Step(startVerifyRuntimeStep, startStopR3Input(`{"unit_ref":`+startStopR3OtherUnit+`}`)), want: verifySame},
		{name: "readiness ok", step: startReadinessStep, state: starting},
		{name: "readiness while stopped", step: startReadinessStep, state: stopped, want: readinessInspect},
		{name: "readiness without successful verification", step: startReadinessStep, state: starting,
			change: startStopR3Step(startVerifyRuntimeStep, startStopR3Status(protocolstate.StepRunning)), want: readinessInspect},
		{name: "readiness with undecodable verification", step: startReadinessStep, state: starting,
			change: startStopR3Step(startVerifyRuntimeStep, startStopR3Output(`[]`)), want: readinessRunning},
		{name: "readiness with stopped unit", step: startReadinessStep, state: starting,
			change: startStopR3Step(startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, "stopped"))), want: readinessRunning},
		{name: "readiness with verification output for another unit", step: startReadinessStep, state: starting,
			change: startStopR3Step(startVerifyRuntimeStep, startStopR3Output(startStopR3UnitOutput(startStopR3OtherUnit, "running")))},
		{name: "readiness with verification of another unit", step: startReadinessStep, state: starting,
			change: startStopR3Step(startVerifyRuntimeStep, startStopR3Input(`{"unit_ref":`+startStopR3OtherUnit+`}`)), want: readinessBound},
		{name: "readiness bound to another instance", step: startReadinessStep, state: starting,
			change: startStopR3Step(startReadinessStep, startStopR3Input(`{"instance_ref":`+startStopR3OtherInstance+`}`)), want: readinessBound},
	}
	startStopR3RunPreconditions(t, startStopR3StartChecks, cases)
}

// TestStartStopR3StopCheckPreconditions pins the exact result of every
// server.stop precondition rule.
func TestStartStopR3StopCheckPreconditions(t *testing.T) {
	const mismatch = "operation target does not match the current logical-server instance"
	const planBound = "server.stop plan is not bound to the current managed instance"
	const inspectReady = "runtime inspection requires the ready server and successful provider inspection"
	const inspectActive = "runtime inspection requires the exact provider instance to be active"
	const saveReady = "game save requires a ready server and successful runtime inspection"
	const saveActive = "game save requires the exact provider instance to remain active"
	const saveRunning = "game save requires the exact execution unit to be running"
	const stopSave = "graceful stop requires a confirmed game save and stopping lifecycle state"
	const stopSame = "graceful stop must target the exact inspected execution unit"
	ready, stopping := protocolstate.ServerReady, protocolstate.ServerStopping
	cases := []startStopR3PreconditionCase{
		{name: "wrong operation type", step: stopInspectInstanceStep, state: ready, change: startStopR3Op(func(o *store.OperationRecord) { o.OperationType = "server.start" }),
			want: "server.stop operation and context are required"},
		{name: "wrong protocol", step: stopInspectInstanceStep, state: ready, change: startStopR3Op(func(o *store.OperationRecord) { o.ProtocolVersion = "0.0.1" }),
			want: "server.stop operation and context are required"},
		{name: "unknown server", step: stopInspectInstanceStep, state: ready, change: startStopR3Op(func(o *store.OperationRecord) { o.Target.EnvironmentID = "other" }),
			want: "operation not found"},
		{name: "server without instance", step: stopInspectInstanceStep, state: ready, change: startStopR3Server(func(s *store.LogicalServer) { s.InstanceRef = nil }), want: mismatch},
		{name: "target instance mismatch", step: stopInspectInstanceStep, state: ready,
			change: startStopR3Op(func(o *store.OperationRecord) { o.Target.InstanceRef = json.RawMessage(startStopR3OtherInstance) }), want: mismatch},
		{name: "plan bound to another instance", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopInspectInstanceStep, startStopR3Input(`{"instance_ref":`+startStopR3OtherInstance+`}`)), want: planBound},
		{name: "plan without inspect step", step: stopSaveGameStep, state: ready, change: startStopR3RemoveStep(stopInspectInstanceStep), want: planBound},
		{name: "plan binding checked before step ID", step: "other-step", state: ready,
			change: startStopR3Step(stopInspectInstanceStep, startStopR3Input(`{}`)), want: planBound},
		{name: "unsupported step", step: "other-step", state: ready, want: `unsupported server.stop step "other-step"`},
		{name: "inspect ok", step: stopInspectInstanceStep, state: ready},
		{name: "inspect while stopping", step: stopInspectInstanceStep, state: stopping,
			want: "server must remain ready until the exact provider instance is inspected"},
		{name: "unit inspect ok", step: stopInspectUnitStep, state: ready},
		{name: "unit inspect while stopping", step: stopInspectUnitStep, state: stopping, want: inspectReady},
		{name: "unit inspect without provider inspection", step: stopInspectUnitStep, state: ready,
			change: startStopR3Step(stopInspectInstanceStep, startStopR3Status(protocolstate.StepPending)), want: inspectReady},
		{name: "unit inspect with undecodable provider output", step: stopInspectUnitStep, state: ready,
			change: startStopR3Step(stopInspectInstanceStep, startStopR3Output(`"x"`)), want: inspectActive},
		{name: "unit inspect with another instance", step: stopInspectUnitStep, state: ready,
			change: startStopR3Step(stopInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startStopR3OtherInstance, "active"))), want: inspectActive},
		{name: "unit inspect with destroyed instance", step: stopInspectUnitStep, state: ready,
			change: startStopR3Step(stopInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, "destroyed"))), want: inspectActive},
		{name: "save ok", step: stopSaveGameStep, state: ready},
		{name: "save while stopping", step: stopSaveGameStep, state: stopping, want: saveReady},
		{name: "save without unit inspection", step: stopSaveGameStep, state: ready,
			change: startStopR3Step(stopInspectUnitStep, startStopR3Status(protocolstate.StepFailed)), want: saveReady},
		{name: "save with inactive instance", step: stopSaveGameStep, state: ready,
			change: startStopR3Step(stopInspectInstanceStep, startStopR3Output(startStopR3InstanceOutput(startTestInstance, "stopped"))), want: saveActive},
		{name: "save with undecodable unit output", step: stopSaveGameStep, state: ready,
			change: startStopR3Step(stopInspectUnitStep, startStopR3Output(`[]`)), want: saveRunning},
		{name: "save with unit output for another unit", step: stopSaveGameStep, state: ready,
			change: startStopR3Step(stopInspectUnitStep, startStopR3Output(startStopR3UnitOutput(startStopR3OtherUnit, "running"))), want: saveRunning},
		{name: "save with stopped unit", step: stopSaveGameStep, state: ready,
			change: startStopR3Step(stopInspectUnitStep, startStopR3Output(startStopR3UnitOutput(startTestUnit, "stopped"))), want: saveRunning},
		{name: "save with unit inspect of another unit", step: stopSaveGameStep, state: ready,
			change: startStopR3Step(stopInspectUnitStep, startStopR3Input(`{"unit_ref":`+startStopR3OtherUnit+`}`))},
		{name: "stop ok", step: stopRuntimeStep, state: stopping},
		{name: "stop while ready", step: stopRuntimeStep, state: ready, want: stopSave},
		{name: "stop with pending save", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopSaveGameStep, startStopR3Status(protocolstate.StepPending)), want: stopSave},
		{name: "stop with rejected save", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopSaveGameStep, startStopR3Output(startStopR3SaveOutput(startTestServerID, startTestInstance, startTestRelease, "rejected"))), want: stopSave},
		{name: "stop with save for another release", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopSaveGameStep, startStopR3Output(startStopR3SaveOutput(startTestServerID, startTestInstance, "other", "confirmed"))), want: stopSave},
		{name: "stop with save for another server", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopSaveGameStep, startStopR3Output(startStopR3SaveOutput("other", startTestInstance, startTestRelease, "confirmed"))), want: stopSave},
		{name: "stop with save for another instance", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopSaveGameStep, startStopR3Output(startStopR3SaveOutput(startTestServerID, startStopR3OtherInstance, startTestRelease, "confirmed"))), want: stopSave},
		{name: "stop with save without observation time", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopSaveGameStep, startStopR3Output(strings.Replace(startStopR3SaveOutput(startTestServerID, startTestInstance, startTestRelease, "confirmed"), `"observed_at"`, `"observed"`, 1))), want: stopSave},
		{name: "stop with save without saved time", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopSaveGameStep, startStopR3Output(strings.Replace(startStopR3SaveOutput(startTestServerID, startTestInstance, startTestRelease, "confirmed"), `"saved_at"`, `"saved"`, 1))), want: stopSave},
		{name: "stop with undecodable save input", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopSaveGameStep, startStopR3Input(`[]`)), want: stopSave},
		{name: "stop with unit inspect of another unit", step: stopRuntimeStep, state: stopping,
			change: startStopR3Step(stopInspectUnitStep, startStopR3Input(`{"unit_ref":`+startStopR3OtherUnit+`}`)), want: stopSame},
		{name: "stop without unit inspect step", step: stopRuntimeStep, state: stopping, change: startStopR3RemoveStep(stopInspectUnitStep), want: stopSame},
	}
	startStopR3RunPreconditions(t, startStopR3StopChecks, cases)
}
