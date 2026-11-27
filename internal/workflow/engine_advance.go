package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// advanceLocked runs one advancement in fixed stages. A stage that reports
// done has produced the final result: either the operation cannot move
// further now or the stage already made its last durable write.
func (e *Engine) advanceLocked(ctx context.Context, operationID string) (store.StoredOperation, error) {
	current, definition, done, err := e.loadForAdvance(ctx, operationID)
	if done {
		return current, err
	}
	if current, done, err = e.captureBackupInitialState(current); done {
		return current, err
	}
	if current, done, err = e.startOperation(current); done {
		return current, err
	}
	if current, done, err = e.resumeSuspendedProgress(ctx, current, definition); done {
		return current, err
	}
	if current, done, err = e.settleActiveStep(ctx, current, definition); done {
		return current, err
	}
	return e.dispatchNextStep(ctx, current, definition)
}

// loadForAdvance reads the operation and its definition and stops, without
// writing, when the operation cannot advance.
func (e *Engine) loadForAdvance(ctx context.Context, operationID string) (store.StoredOperation, Definition, bool, error) {
	current, err := e.store.GetOperation(operationID)
	if err != nil {
		return store.StoredOperation{}, nil, true, err
	}
	if protocolstate.IsTerminalOperation(current.Record.Status) {
		return current, nil, true, nil
	}
	if current.Record.Status == protocolstate.OperationNeedsOperator {
		return current, nil, true, ErrNeedsOperator
	}
	definition, ok := e.definitions.get(current.Record.OperationType)
	if !ok {
		return current, nil, true, fmt.Errorf("%w: %s", ErrNotRecoverable, current.Record.OperationType)
	}
	if err := ctx.Err(); err != nil {
		return current, nil, true, err
	}
	return current, definition, false, nil
}

// captureBackupInitialState durably records a backup's starting lifecycle
// state before any of its steps can run.
func (e *Engine) captureBackupInitialState(current store.StoredOperation) (store.StoredOperation, bool, error) {
	if current.Record.OperationType != "server.backup" || current.Record.BackupInitialLifecycleState != "" {
		return current, false, nil
	}
	backupStore, ok := e.store.(store.BackupContextStore)
	if !ok {
		if current.Record.Status == protocolstate.OperationPending {
			return current, true, errors.New("operation store cannot persist backup lifecycle context; no workflow step was dispatched")
		}
		updated, err := e.suspend(current, "core.backup.initial_state_unavailable", "The controller cannot durably capture the server state before backup dispatch.")
		return updated, true, err
	}
	captured, err := backupStore.CaptureBackupInitialState(current.Record.OperationID, current.Revision)
	if err != nil {
		if current.Record.Status == protocolstate.OperationPending {
			return current, true, errors.New("backup initial lifecycle state could not be persisted; no workflow step was dispatched")
		}
		updated, err := e.suspend(current, "core.backup.initial_state_unavailable", "The server's initial lifecycle state could not be durably captured before backup dispatch.")
		return updated, true, err
	}
	return captured, false, nil
}

// startOperation moves a pending operation to running, then suspends it if
// this controller cannot safely continue its recorded plan.
func (e *Engine) startOperation(current store.StoredOperation) (store.StoredOperation, bool, error) {
	if current.Record.Status == protocolstate.OperationPending {
		next := current.Record
		next.Status = protocolstate.OperationRunning
		var err error
		current, err = e.updateOperation(current.Revision, next)
		if err != nil {
			return store.StoredOperation{}, true, err
		}
	}
	if current.Record.ProtocolVersion != e.schemas.ProtocolVersion() || current.Record.ProtocolVersion != e.adapters.ProtocolVersion() {
		updated, err := e.suspend(current, "core.compatibility.protocol_version_unavailable", "The operation's recorded protocol version is not loaded by this controller.")
		return updated, true, err
	}
	if !stepOrderValid(current.Record) {
		updated, err := e.suspend(current, "core.workflow.invalid_step_order", "The persisted workflow steps are not in a safe sequential order.")
		return updated, true, err
	}
	return current, false, nil
}

// resumeSuspendedProgress returns a waiting or reconciling operation to
// running, or reconciles its active step.
func (e *Engine) resumeSuspendedProgress(ctx context.Context, current store.StoredOperation, definition Definition) (store.StoredOperation, bool, error) {
	if current.Record.Status != protocolstate.OperationWaiting && current.Record.Status != protocolstate.OperationReconciling {
		return current, false, nil
	}
	stepIndex := activeStepIndex(current.Record)
	if stepIndex < 0 {
		return e.resumeWithoutActiveStep(ctx, current, definition)
	}
	step := current.Record.Steps[stepIndex]
	if step.Status == protocolstate.StepPending {
		next := current.Record
		next.Status = protocolstate.OperationRunning
		next.Error = nil
		updated, err := e.updateOperation(current.Revision, next)
		if err != nil {
			return store.StoredOperation{}, true, err
		}
		return updated, false, nil
	}
	updated, err := e.reconcile(ctx, current, stepIndex, definition)
	return updated, true, err
}

// resumeWithoutActiveStep returns to running when steps remain or all are
// complete, and otherwise lets finishSuccess decide.
func (e *Engine) resumeWithoutActiveStep(ctx context.Context, current store.StoredOperation, definition Definition) (store.StoredOperation, bool, error) {
	next := current.Record
	switch {
	case hasPendingSteps(current.Record):
		next.Status = protocolstate.OperationRunning
		next.Error = nil
		next.CurrentStepID = nil
	case allStepsComplete(current.Record):
		next.Status = protocolstate.OperationRunning
		next.CurrentStepID = nil
	default:
		updated, err := e.finishSuccess(ctx, current, definition)
		return updated, true, err
	}
	updated, err := e.updateOperation(current.Revision, next)
	if err != nil {
		return store.StoredOperation{}, true, err
	}
	return updated, false, nil
}

// settleActiveStep handles a running operation's step that was already
// dispatched: an interrupted call becomes outcome_unknown and is reconciled.
func (e *Engine) settleActiveStep(ctx context.Context, current store.StoredOperation, definition Definition) (store.StoredOperation, bool, error) {
	if current.Record.Status != protocolstate.OperationRunning {
		return current, true, ErrNotRecoverable
	}
	stepIndex := activeStepIndex(current.Record)
	if stepIndex < 0 {
		return current, false, nil
	}
	var err error
	step := current.Record.Steps[stepIndex]
	switch step.Status {
	case protocolstate.StepRunning, protocolstate.StepOutcomeUnknown, protocolstate.StepWaiting:
		// reconcile first records a running step's outcome as unknown.
		current, err = e.reconcile(ctx, current, stepIndex, definition)
		return current, true, err
	case protocolstate.StepSucceeded:
		current, err = e.clearCurrentStep(current)
		if err != nil {
			return store.StoredOperation{}, true, err
		}
	case protocolstate.StepFailed, protocolstate.StepSkipped, protocolstate.StepCancelled:
		return current, true, ErrNotRecoverable
	}
	return current, false, nil
}

// dispatchNextStep selects the first pending step, or finishes the operation
// when none remains, and claims and dispatches that step.
func (e *Engine) dispatchNextStep(ctx context.Context, current store.StoredOperation, definition Definition) (store.StoredOperation, error) {
	if current.Record.Status != protocolstate.OperationRunning {
		return current, ErrNotRecoverable
	}
	stepIndex := firstPendingStep(current.Record)
	if stepIndex < 0 {
		if !allStepsComplete(current.Record) {
			return e.suspend(current, "core.workflow.incomplete_steps", "The workflow contains unresolved steps without a current dispatch target.")
		}
		return e.finishSuccess(ctx, current, definition)
	}
	if ctx.Err() != nil {
		return current, ctx.Err()
	}
	current, step, done, err := e.resolveBoundInput(current, stepIndex)
	if done {
		return current, err
	}
	return e.claimAndDispatch(ctx, current, stepIndex, step, definition)
}

// resolveBoundInput persists a step's input resolved from earlier outputs
// once, after checking it against the recorded capability.
func (e *Engine) resolveBoundInput(current store.StoredOperation, stepIndex int) (store.StoredOperation, store.OperationStep, bool, error) {
	step := current.Record.Steps[stepIndex]
	if len(step.InputBindings) == 0 || len(step.ResolvedInput) > 0 {
		return current, step, false, nil
	}
	resolved, resolveErr := resolveStepInput(current.Record.Steps, stepIndex)
	if resolveErr != nil {
		updated, err := e.suspend(current, "core.workflow.input_binding_unavailable", "A workflow input could not be safely resolved from an earlier completed step.")
		return updated, step, true, err
	}
	step.ResolvedInput = resolved
	_, preflightErr := e.preflight(current.Record, step)
	if preflightErr != nil {
		updated, err := e.suspend(current, "core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.")
		return updated, step, true, err
	}
	resolvedRecord := current.Record
	resolvedRecord.Steps[stepIndex].ResolvedInput = append(json.RawMessage(nil), resolved...)
	current, err := e.updateOperation(current.Revision, resolvedRecord)
	if err != nil {
		return store.StoredOperation{}, step, true, err
	}
	return current, current.Record.Steps[stepIndex], false, nil
}

// claimAndDispatch runs the last checks, durably claims the step and only
// then calls its adapter.
func (e *Engine) claimAndDispatch(ctx context.Context, current store.StoredOperation, stepIndex int, step store.OperationStep, definition Definition) (store.StoredOperation, error) {
	capability, err := e.preflight(current.Record, step)
	if err != nil {
		return e.suspend(current, "core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.")
	}
	if err := definition.CheckPreconditions(ctx, current.Record, step); err != nil {
		return e.handlePreconditionFailure(ctx, current, definition, preconditionFailure{step: step, cause: err})
	}
	if ctx.Err() != nil {
		return current, ctx.Err()
	}
	deadline := e.stepDeadline(ctx, current.Record)
	if !deadline.After(time.Now()) {
		return e.failBeforeDispatch(ctx, current, stepIndex, definition, refusedByDeadline)
	}
	return e.claimStep(ctx, current, current.Record, stepCall{index: stepIndex, step: step, definition: definition, capability: capability})
}

// stepCall is one step about to be dispatched or reconciled: its index, the
// step as last persisted, its definition and its preflighted capability.
type stepCall struct {
	index      int
	step       store.OperationStep
	definition Definition
	capability adapter.Capability
}

// claimStep durably marks the step running for its next attempt in claimed,
// which the caller derived from current, and only then dispatches it.
func (e *Engine) claimStep(ctx context.Context, current store.StoredOperation, claimed store.OperationRecord, call stepCall) (store.StoredOperation, error) {
	claimed.Steps[call.index].Status = protocolstate.StepRunning
	claimed.Steps[call.index].Attempt++
	claimed.CurrentStepID = stringPointer(claimed.Steps[call.index].StepID)
	running, err := e.updateOperation(current.Revision, claimed)
	if err != nil {
		return store.StoredOperation{}, err
	}
	return e.dispatch(ctx, running, call.index, call.definition, call.capability)
}

// preconditionFailure is a step whose precondition check failed with cause.
// priorEffectResolved is true when reconciliation has already settled the
// step's earlier attempt.
type preconditionFailure struct {
	step                store.OperationStep
	cause               error
	priorEffectResolved bool
}

// mayFailDefinitively reports whether a definition may turn the failure
// into a definitive error: the step never ran, or its earlier effect is
// already resolved.
func (f preconditionFailure) mayFailDefinitively() bool {
	return f.step.Status == protocolstate.StepPending || f.priorEffectResolved
}

// handlePreconditionFailure fails the operation when the definition reports
// a definitive, schema-valid error for the failure, and otherwise suspends
// it for an operator.
func (e *Engine) handlePreconditionFailure(ctx context.Context, current store.StoredOperation, definition Definition, failure preconditionFailure) (store.StoredOperation, error) {
	handler, ok := definition.(PreconditionFailureHandler)
	if !ok || !failure.mayFailDefinitively() {
		return e.suspendPreconditionUnmet(current)
	}
	resolution, err := handler.HandlePreconditionFailure(ctx, current.Record, failure.step, failure.cause)
	if err != nil || resolution.State != ResolutionDefinitiveError || !validProtocolError(e.schemas, resolution.Error) {
		return e.suspendPreconditionUnmet(current)
	}
	return e.fail(ctx, current, current.Record, -1, definition, resolution.Error)
}

func (e *Engine) suspendPreconditionUnmet(current store.StoredOperation) (store.StoredOperation, error) {
	return e.suspend(current, "core.safety.precondition_unmet", "Workflow preconditions or authorization are not currently satisfied.")
}

func (e *Engine) clearCurrentStep(current store.StoredOperation) (store.StoredOperation, error) {
	next := current.Record
	next.CurrentStepID = nil
	return e.updateOperation(current.Revision, next)
}
