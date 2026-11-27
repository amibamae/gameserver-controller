package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// markOutcomeUnknown moves the operation to reconciliation with the step's
// outcome unknown, recording errValue on both.
func (e *Engine) markOutcomeUnknown(running store.StoredOperation, stepIndex int, errValue json.RawMessage) (store.StoredOperation, error) {
	unknown := running.Record
	markStepOutcomeUnknown(&unknown, stepIndex, errValue)
	return e.updateOperation(running.Revision, unknown)
}

// markStepOutcomeUnknown moves record to reconciliation with the step's
// outcome unknown, recording errValue on both.
func markStepOutcomeUnknown(record *store.OperationRecord, stepIndex int, errValue json.RawMessage) {
	record.Status = protocolstate.OperationReconciling
	record.Steps[stepIndex].Status = protocolstate.StepOutcomeUnknown
	record.Steps[stepIndex].Error = errValue
	record.Error = json.RawMessage(errValue)
}

// reconcileRequired is the internal error recorded on a step whose external
// outcome must be reconciled before anything else happens to it.
func reconcileRequired(code, message, operationID string) json.RawMessage {
	return protocolError(code, "internal", message, operationID, false, "reconcile", false)
}

// failStep fails the step with errValue and finishes the operation.
func (e *Engine) failStep(ctx context.Context, running store.StoredOperation, stepIndex int, definition Definition, errValue json.RawMessage) (store.StoredOperation, error) {
	failed := running.Record
	failed.Steps[stepIndex].Status = protocolstate.StepFailed
	failed.Steps[stepIndex].Error = errValue
	return e.fail(ctx, running, failed, stepIndex, definition, errValue)
}

func (e *Engine) finishSuccess(ctx context.Context, current store.StoredOperation, definition Definition) (store.StoredOperation, error) {
	if !allStepsComplete(current.Record) {
		return e.suspend(current, "core.workflow.incomplete_steps", "The workflow cannot succeed while any step remains unresolved.")
	}
	if validator, ok := definition.(CompletionValidator); ok {
		if err := validator.ValidateCompletion(ctx, current.Record); err != nil {
			failure := protocolError("core.workflow.postcondition_unmet", "safety_blocked", "Completed adapter steps did not satisfy the operation's required postconditions.", current.Record.OperationID, false, "inspect_operation", false)
			return e.fail(ctx, current, current.Record, -1, definition, failure)
		}
	}
	result, err := definition.BuildSuccessResult(ctx, current.Record)
	if err != nil || !validOperationResult(e.schemas, current.Record, result, true) {
		return e.suspend(current, "core.workflow.result_invalid", "The workflow could not produce a schema-valid success result.")
	}
	next := current.Record
	next.Status = protocolstate.OperationSucceeded
	next.Error = nil
	next.Result = append(json.RawMessage(nil), result...)
	next.CurrentStepID = nil
	return e.updateOperation(current.Revision, next)
}

// failBeforeDispatch fails the operation for a step the controller refused
// to send to its adapter. fail itself sets the failed status, the error and
// the cleared current step, so the record is passed unchanged.
func (e *Engine) failBeforeDispatch(ctx context.Context, current store.StoredOperation, stepIndex int, definition Definition, refusal *dispatchRefusal) (store.StoredOperation, error) {
	errValue := protocolError(refusal.code, refusal.category, refusal.message, current.Record.OperationID, false, "inspect_operation", false)
	return e.fail(ctx, current, current.Record, stepIndex, definition, errValue)
}

func (e *Engine) fail(ctx context.Context, current store.StoredOperation, failed store.OperationRecord, stepIndex int, definition Definition, failure json.RawMessage) (store.StoredOperation, error) {
	failed.Status = protocolstate.OperationFailed
	if stepIndex >= 0 && stepIndex < len(failed.Steps) && failed.Steps[stepIndex].Status != protocolstate.StepPending {
		failed.Steps[stepIndex].Status = protocolstate.StepFailed
		failed.Steps[stepIndex].Error = append(json.RawMessage(nil), failure...)
	}
	failed.Error = append(json.RawMessage(nil), failure...)
	failed.CurrentStepID = nil
	var failedStep store.OperationStep
	if stepIndex >= 0 && stepIndex < len(failed.Steps) {
		failedStep = failed.Steps[stepIndex]
	}
	if partial, err := definition.BuildFailureResult(ctx, failed, failedStep, failure); err == nil && validOperationResult(e.schemas, failed, partial, false) {
		failed.Result = append(json.RawMessage(nil), partial...)
	} else {
		failed.Result = nil
	}
	return e.updateOperation(current.Revision, failed)
}

func (e *Engine) suspend(current store.StoredOperation, code, message string) (store.StoredOperation, error) {
	if current.Record.Status == protocolstate.OperationNeedsOperator {
		return current, ErrNeedsOperator
	}
	next := current.Record
	if next.Status == protocolstate.OperationPending {
		return current, fmt.Errorf("%w: pending operation cannot be suspended without starting", ErrNotRecoverable)
	}
	next.Status = protocolstate.OperationNeedsOperator
	next.Error = protocolError(code, "safety_blocked", message, next.OperationID, false, "operator_review", true)
	for i := range next.Steps {
		if next.Steps[i].Status == protocolstate.StepRunning {
			next.Steps[i].Status = protocolstate.StepOutcomeUnknown
			next.Steps[i].Error = append(json.RawMessage(nil), next.Error...)
		}
	}
	return e.updateOperation(current.Revision, next)
}
