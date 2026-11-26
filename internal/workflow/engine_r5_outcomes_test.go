package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// engineR5Reconcile returns the exact record of an uncertain step that must
// be reconciled.
func engineR5Reconcile(code, message string) json.RawMessage {
	return protocolError(code, "internal", message, "engine-r5", false, "reconcile", false)
}

// engineR5Refused returns the exact record of a step stopped before its
// adapter call.
func engineR5Refused(code, category, message string) json.RawMessage {
	return protocolError(code, category, message, "engine-r5", false, "inspect_operation", false)
}

// engineR5ErrorCase names one durable write and the error it must carry on
// the operation and on step-1.
type engineR5ErrorCase struct {
	name        string
	options     engineR5Options
	setup       func(*testing.T, *engineR5Env)
	run         func(*engineR5Env) (store.StoredOperation, error)
	write       int
	wantErr     string
	wantStatus  protocolstate.OperationStatus
	wantStep    protocolstate.StepStatus
	wantCurrent string
	wantOp      json.RawMessage
	wantStepErr json.RawMessage
}

func engineR5Advance(env *engineR5Env) (store.StoredOperation, error) {
	return env.engine.Advance(env.ctx, env.id)
}

func engineR5RunErrorCase(t *testing.T, tc engineR5ErrorCase) {
	t.Helper()
	env := engineR5NewEnv(t, tc.options)
	if tc.setup != nil {
		tc.setup(t, env)
	}
	run := tc.run
	if run == nil {
		run = engineR5Advance
	}
	_, err := run(env)
	engineR5ExpectErr(t, err, tc.wantErr)
	record := env.store.engineR5Record(t, tc.write)
	if record.Status != tc.wantStatus || record.Steps[0].Status != tc.wantStep {
		t.Errorf("write %d = %s/%s, want %s/%s", tc.write, record.Status, record.Steps[0].Status, tc.wantStatus, tc.wantStep)
	}
	current := ""
	if record.CurrentStepID != nil {
		current = *record.CurrentStepID
	}
	if current != tc.wantCurrent {
		t.Errorf("current step = %q, want %q", current, tc.wantCurrent)
	}
	engineR5ExpectError(t, "operation error", record.Error, tc.wantOp)
	engineR5ExpectError(t, "step error", record.Steps[0].Error, tc.wantStepErr)
}

func engineR5Invoke(response adapter.InvokeResponse, err error) func(*testing.T, *engineR5Env) {
	return func(_ *testing.T, env *engineR5Env) {
		env.engine.adapters = engineR5Adapters{env.engine.adapters, func(_ context.Context, _ string, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			response.ID = request.ID
			return response, err
		}}
	}
}

func TestEngineR5UncertainStepsRecordTheirExactError(t *testing.T) {
	noResponse := engineR5Reconcile("core.adapter.outcome_unknown", "The previous adapter invocation has no recorded response.")
	mayHaveRun := engineR5Reconcile("core.adapter.outcome_unknown", "The adapter call may have taken effect but returned no definitive result.")
	reportedUnknown := engineR5Reconcile("core.adapter.outcome_unknown", "The adapter reported that the external outcome is unknown.")
	refChanged := engineR5Reconcile("core.adapter.external_reference_changed", "The adapter returned a different external operation reference for a recorded step.")
	malformed := engineR5Reconcile("core.adapter.invalid_response", "The adapter returned a malformed response after dispatch.")
	restarted := engineR5Reconcile("core.adapter.outcome_unknown", "The controller restarted while this adapter step was running.")
	now := time.Now().UTC()
	otherRef := json.RawMessage(advanceRefactorOtherRef)
	for _, tc := range []engineR5ErrorCase{
		{name: "running step of a running operation", options: engineR5Options{seed: engineR5Seed(protocolstate.OperationRunning, "step-1", protocolstate.StepRunning)},
			wantStatus: protocolstate.OperationReconciling, wantStep: protocolstate.StepOutcomeUnknown, wantCurrent: "step-1", wantOp: noResponse, wantStepErr: noResponse},
		{name: "running step of a reconciling operation", options: engineR5Options{seed: engineR5Seed(protocolstate.OperationReconciling, "step-1", protocolstate.StepRunning)},
			wantStatus: protocolstate.OperationReconciling, wantStep: protocolstate.StepOutcomeUnknown, wantCurrent: "step-1", wantOp: noResponse, wantStepErr: noResponse},
		{name: "mutating call fails", options: engineR5Options{mutating: true, replies: []fakeadapter.Reply{{Err: errors.New("connection reset")}}}, write: 2,
			wantStatus: protocolstate.OperationReconciling, wantStep: protocolstate.StepOutcomeUnknown, wantCurrent: "step-1", wantOp: mayHaveRun, wantStepErr: mayHaveRun},
		{name: "adapter reports unknown", options: engineR5Options{replies: []fakeadapter.Reply{{Outcome: adapter.OutcomeUnknown, Output: json.RawMessage(testExternalRef), ObservedAt: now}}}, write: 2,
			wantStatus: protocolstate.OperationReconciling, wantStep: protocolstate.StepOutcomeUnknown, wantCurrent: "step-1", wantOp: reportedUnknown, wantStepErr: reportedUnknown},
		{name: "external reference changes", options: engineR5Options{seed: func(r *store.OperationRecord) { r.Steps[0].ExternalOperationRef = json.RawMessage(testExternalRef) }},
			setup: engineR5Invoke(adapter.InvokeResponse{JSONRPC: "2.0", Result: adapter.InvokeResult{
				Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ExternalOperationRef: otherRef, ObservedAt: now,
			}}, nil), write: 2,
			wantStatus: protocolstate.OperationReconciling, wantStep: protocolstate.StepOutcomeUnknown, wantCurrent: "step-1", wantOp: refChanged, wantStepErr: refChanged},
		{name: "malformed response", setup: engineR5Invoke(adapter.InvokeResponse{JSONRPC: "1.0", Result: adapter.InvokeResult{
			Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ObservedAt: now,
		}}, nil), write: 2,
			wantStatus: protocolstate.OperationReconciling, wantStep: protocolstate.StepOutcomeUnknown, wantCurrent: "step-1", wantOp: malformed, wantStepErr: malformed},
		{name: "recovery after restart", options: engineR5Options{recoverable: true, seed: engineR5Seed(protocolstate.OperationRunning, "step-1", protocolstate.StepRunning)},
			run: func(env *engineR5Env) (store.StoredOperation, error) {
				_, err := env.engine.Recover(env.ctx)
				return store.StoredOperation{}, err
			},
			wantStatus: protocolstate.OperationReconciling, wantStep: protocolstate.StepOutcomeUnknown, wantCurrent: "step-1", wantOp: restarted, wantStepErr: restarted},
	} {
		t.Run(tc.name, func(t *testing.T) { engineR5RunErrorCase(t, tc) })
	}
}

// engineR5GuardDefinition refuses dispatch in its guard.
type engineR5GuardDefinition struct{ *testDefinition }

func (engineR5GuardDefinition) BeforeAdapterDispatch(context.Context, store.OperationRecord, store.OperationStep) error {
	return errors.New("guard says no")
}

func TestEngineR5FailedStepsRecordTheirExactError(t *testing.T) {
	guard := engineR5Refused("core.workflow.dispatch_guard_failed", "safety_blocked", "A controller safety check failed immediately before adapter dispatch.")
	secrets := engineR5Refused("core.workflow.secret_reference_failed", "safety_blocked", "The controller could not authorize the secret references required by this adapter step.")
	streams := engineR5Refused("core.workflow.stream_transfer_failed", "safety_blocked", "The controller could not prepare the bounded stream inputs required by this adapter step.")
	deadline := engineR5Refused("core.operation.deadline_exceeded", "timeout", "The operation deadline expired before adapter dispatch.")
	invocation := protocolError("core.adapter.invocation_failed", "provider_failure", "The adapter call failed before a side effect was confirmed.", "engine-r5", false, "inspect_operation", true)
	postcondition := protocolError("core.workflow.postcondition_unmet", "safety_blocked", "Completed adapter steps did not satisfy the operation's required postconditions.", "engine-r5", false, "inspect_operation", false)
	definitive := advanceRefactorDefinitiveError("engine-r5")
	replace := func(definition func(*testDefinition) Definition) func(*testing.T, *engineR5Env) {
		return func(t *testing.T, env *engineR5Env) { engineR5Replace(t, env, definition(env.fixture.definition)) }
	}
	for _, tc := range []engineR5ErrorCase{
		{name: "dispatch guard refuses", setup: replace(func(d *testDefinition) Definition { return engineR5GuardDefinition{d} }), write: 2,
			wantStatus: protocolstate.OperationFailed, wantStep: protocolstate.StepFailed, wantOp: guard, wantStepErr: guard},
		{name: "secret references refused", setup: replace(func(d *testDefinition) Definition { return secretFailingDefinition{d} }), write: 2,
			wantStatus: protocolstate.OperationFailed, wantStep: protocolstate.StepFailed, wantOp: secrets, wantStepErr: secrets},
		{name: "stream transfer malformed", setup: func(_ *testing.T, env *engineR5Env) {
			env.fixture.definition.streamTransfers = []adapter.StreamTransfer{{Reference: streamReference("", "controller_to_adapter")}}
		}, write: 2, wantStatus: protocolstate.OperationFailed, wantStep: protocolstate.StepFailed, wantOp: streams, wantStepErr: streams},
		{name: "deadline passed before the claim", options: engineR5Options{seed: func(r *store.OperationRecord) {
			past := time.Now().Add(-time.Hour).UTC()
			r.Deadline = &past
		}}, write: 1, wantStatus: protocolstate.OperationFailed, wantStep: protocolstate.StepPending, wantOp: deadline},
		{name: "non-mutating call fails", options: engineR5Options{replies: []fakeadapter.Reply{{Err: errors.New("refused")}}}, write: 2,
			wantStatus: protocolstate.OperationFailed, wantStep: protocolstate.StepFailed, wantOp: invocation, wantStepErr: invocation},
		{name: "postcondition unmet", options: engineR5Options{seed: engineR5Seed(protocolstate.OperationRunning, "", protocolstate.StepSucceeded)},
			setup:      replace(func(d *testDefinition) Definition { return engineR5CompletionDefinition{d, errors.New("not ready")} }),
			wantStatus: protocolstate.OperationFailed, wantStep: protocolstate.StepSucceeded, wantOp: postcondition},
		{name: "precondition handler fails a pending step", setup: func(t *testing.T, env *engineR5Env) {
			env.fixture.definition.preconditionErr = errors.New("no")
			engineR5Replace(t, env, engineR5HandlerDefinition{env.fixture.definition, Resolution{State: ResolutionDefinitiveError, Error: definitive}, nil})
		}, write: 1, wantStatus: protocolstate.OperationFailed, wantStep: protocolstate.StepPending, wantOp: definitive},
		{name: "reconciliation fails definitively", options: engineR5Options{seed: engineR5Seed(protocolstate.OperationRunning, "step-1", protocolstate.StepOutcomeUnknown)},
			setup: func(_ *testing.T, env *engineR5Env) {
				env.fixture.definition.resolution = Resolution{State: ResolutionDefinitiveError, Error: definitive}
			}, wantStatus: protocolstate.OperationFailed, wantStep: protocolstate.StepFailed, wantOp: definitive, wantStepErr: definitive},
	} {
		t.Run(tc.name, func(t *testing.T) { engineR5RunErrorCase(t, tc) })
	}
}

func TestEngineR5FailureRecordsDoNotShareTheCallerError(t *testing.T) {
	definitive := advanceRefactorDefinitiveError("engine-r5")
	env := engineR5NewEnv(t, engineR5Options{seed: engineR5Seed(protocolstate.OperationRunning, "step-1", protocolstate.StepOutcomeUnknown)})
	env.fixture.definition.resolution = Resolution{State: ResolutionDefinitiveError, Error: definitive}
	if _, err := env.engine.Advance(env.ctx, env.id); err != nil {
		t.Fatal(err)
	}
	record := env.store.engineR5Record(t, 0)
	if &record.Error[0] == &definitive[0] || &record.Steps[0].Error[0] == &definitive[0] {
		t.Fatal("persisted failure shares the reconciliation error buffer")
	}
	if len(record.Result) == 0 {
		t.Fatal("failure result was not recorded")
	}
}

func TestEngineR5PreconditionHandlerSuspendsWhenItCannotDecide(t *testing.T) {
	definitive := advanceRefactorDefinitiveError("engine-r5")
	unmet := advanceRefactorSuspended("core.safety.precondition_unmet", "Workflow preconditions or authorization are not currently satisfied.", " | step-1:pending#0")
	for _, tc := range []struct {
		name       string
		resolution Resolution
		err        error
	}{
		{name: "handler error", resolution: Resolution{State: ResolutionDefinitiveError, Error: definitive}, err: errors.New("lookup failed")},
		{name: "not a definitive error", resolution: Resolution{State: ResolutionNeedsOperator}},
		{name: "invalid error record", resolution: Resolution{State: ResolutionDefinitiveError, Error: json.RawMessage(`{"code":""}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := engineR5NewEnv(t, engineR5Options{})
			env.fixture.definition.preconditionErr = errors.New("no")
			engineR5Replace(t, env, engineR5HandlerDefinition{env.fixture.definition, tc.resolution, tc.err})
			stored, err := env.engine.Advance(env.ctx, env.id)
			engineR5ExpectErr(t, err, "")
			if stored.Record.Status != protocolstate.OperationNeedsOperator {
				t.Errorf("status = %q", stored.Record.Status)
			}
			engineR5ExpectWrites(t, env, advanceRefactorRunning, unmet)
		})
	}
}

func TestEngineR5PreflightNeedsACompletePlan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*store.OperationStep)
		steps  string
	}{
		{name: "no adapter", change: func(s *store.OperationStep) { s.AdapterID = "" }},
		{name: "no capability", change: func(s *store.OperationStep) { s.CapabilityName = "" }},
		{name: "no capability version", change: func(s *store.OperationStep) { s.CapabilityVersion = "" }},
		{name: "empty input", change: func(s *store.OperationStep) { s.Input = nil }},
		{name: "resolved input replaces input", change: func(s *store.OperationStep) { s.ResolvedInput = json.RawMessage(`[]`) }, steps: " in=[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := engineR5NewEnv(t, engineR5Options{seed: func(r *store.OperationRecord) { tc.change(&r.Steps[0]) }})
			if _, err := env.engine.Advance(env.ctx, env.id); err != nil {
				t.Fatal(err)
			}
			steps := " | step-1:pending#0" + tc.steps
			engineR5ExpectWrites(t, env, "running cur=- err=-"+steps, advanceRefactorSuspended("core.compatibility.capability_unavailable",
				"The exact adapter capability recorded for this step is unavailable or incompatible.", steps))
		})
	}
}

func TestEngineR5RecoverOnlyMarksARunningCurrentStep(t *testing.T) {
	marked := "reconciling cur=step-1 err=" + advanceRefactorError(engineR5Reconcile("core.adapter.outcome_unknown", "The controller restarted while this adapter step was running.")) +
		" | step-1:outcome_unknown#1 err=" + advanceRefactorError(engineR5Reconcile("core.adapter.outcome_unknown", "The controller restarted while this adapter step was running."))
	for _, tc := range []struct {
		name       string
		seed       func(*store.OperationRecord)
		wantStatus protocolstate.OperationStatus
		wantWrites []string
	}{
		{name: "running", seed: engineR5Seed(protocolstate.OperationRunning, "step-1", protocolstate.StepRunning), wantStatus: protocolstate.OperationReconciling, wantWrites: []string{marked}},
		{name: "waiting", seed: engineR5Seed(protocolstate.OperationWaiting, "step-1", protocolstate.StepRunning), wantStatus: protocolstate.OperationReconciling, wantWrites: []string{marked}},
		{name: "reconciling", seed: engineR5Seed(protocolstate.OperationReconciling, "step-1", protocolstate.StepRunning), wantStatus: protocolstate.OperationReconciling, wantWrites: []string{marked}},
		{name: "pending operation", seed: engineR5Seed(protocolstate.OperationPending, "step-1", protocolstate.StepRunning), wantStatus: protocolstate.OperationPending},
		{name: "needs operator", seed: engineR5Seed(protocolstate.OperationNeedsOperator, "step-1", protocolstate.StepRunning), wantStatus: protocolstate.OperationNeedsOperator},
		{name: "no current step", seed: engineR5Seed(protocolstate.OperationRunning, "", protocolstate.StepRunning), wantStatus: protocolstate.OperationRunning},
		{name: "unknown current step", seed: engineR5Seed(protocolstate.OperationRunning, "missing", protocolstate.StepRunning), wantStatus: protocolstate.OperationRunning},
		{name: "current step not running", seed: engineR5Seed(protocolstate.OperationRunning, "step-1", protocolstate.StepOutcomeUnknown), wantStatus: protocolstate.OperationRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := engineR5NewEnv(t, engineR5Options{recoverable: true, seed: tc.seed})
			recovered, err := env.engine.Recover(env.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(recovered) != 1 || recovered[0].Record.Status != tc.wantStatus {
				t.Fatalf("recovered = %+v, want one %s operation", recovered, tc.wantStatus)
			}
			engineR5ExpectWrites(t, env, tc.wantWrites...)
		})
	}
}

// engineR5CountingHandler counts precondition-failure decisions.
type engineR5CountingHandler struct {
	*testDefinition
	calls *int
}

func (d engineR5CountingHandler) HandlePreconditionFailure(context.Context, store.OperationRecord, store.OperationStep, error) (Resolution, error) {
	*d.calls++
	return Resolution{State: ResolutionDefinitiveError, Error: advanceRefactorDefinitiveError("engine-r5")}, nil
}

func TestEngineR5PreconditionHandlerIsNotAskedAboutAnUnresolvedAttempt(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failure   preconditionFailure
		wantCalls int
		want      protocolstate.OperationStatus
	}{
		{name: "pending step", failure: preconditionFailure{step: store.OperationStep{Status: protocolstate.StepPending}}, wantCalls: 1, want: protocolstate.OperationFailed},
		{name: "resolved earlier attempt", failure: preconditionFailure{step: store.OperationStep{Status: protocolstate.StepOutcomeUnknown}, priorEffectResolved: true}, wantCalls: 1, want: protocolstate.OperationFailed},
		{name: "unresolved earlier attempt", failure: preconditionFailure{step: store.OperationStep{Status: protocolstate.StepOutcomeUnknown}}, want: protocolstate.OperationNeedsOperator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := engineR5NewEnv(t, engineR5Options{seed: engineR5Seed(protocolstate.OperationRunning, "")})
			calls := 0
			definition := engineR5CountingHandler{env.fixture.definition, &calls}
			current, err := env.store.GetOperation(env.id)
			if err != nil {
				t.Fatal(err)
			}
			tc.failure.cause = errors.New("no")
			stored, err := env.engine.handlePreconditionFailure(env.ctx, current, definition, tc.failure)
			if err != nil || stored.Record.Status != tc.want || calls != tc.wantCalls {
				t.Fatalf("status = %q, err = %v, calls = %d; want %q and %d calls", stored.Record.Status, err, calls, tc.want, tc.wantCalls)
			}
		})
	}
}
