package workflow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (e *Engine) reconcile(ctx context.Context, current store.StoredOperation, stepIndex int, definition Definition) (store.StoredOperation, error) {
	step := current.Record.Steps[stepIndex]
	if step.Status == protocolstate.StepRunning {
		var err error
		current, err = e.markOutcomeUnknown(current, stepIndex, reconcileRequired("core.adapter.outcome_unknown", "The previous adapter invocation has no recorded response.", current.Record.OperationID))
		if err != nil {
			return store.StoredOperation{}, err
		}
		step = current.Record.Steps[stepIndex]
	}
	capability, err := e.preflight(current.Record, step)
	if err != nil {
		return e.suspend(current, "core.compatibility.capability_unavailable", "The exact adapter capability recorded for this step is unavailable or incompatible.")
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	reconcileContext, cancel := context.WithTimeout(ctx, e.stepTimeout)
	resolution, err := definition.Reconcile(reconcileContext, current.Record, step)
	cancel()
	if err != nil {
		return e.suspend(current, "core.reconciliation.unavailable", "The external outcome could not be reconciled automatically.")
	}
	if err := e.validateExternalOperationRef(step, resolution.ExternalOperationRef); err != nil {
		return e.suspend(current, "core.reconciliation.invalid_result", "Reconciliation returned an invalid or conflicting external operation reference.")
	}
	if len(resolution.ExternalOperationRef) > 0 && len(step.ExternalOperationRef) == 0 {
		updated := current.Record
		updated.Steps[stepIndex].ExternalOperationRef = append(json.RawMessage(nil), resolution.ExternalOperationRef...)
		current, err = e.updateOperation(current.Revision, updated)
		if err != nil {
			return store.StoredOperation{}, err
		}
		step = current.Record.Steps[stepIndex]
	}
	return e.applyResolution(ctx, current, stepCall{index: stepIndex, step: step, definition: definition, capability: capability}, resolution)
}

// applyResolution records what reconciliation determined about the step.
func (e *Engine) applyResolution(ctx context.Context, current store.StoredOperation, call stepCall, resolution Resolution) (store.StoredOperation, error) {
	next := current.Record
	next.Error = nil
	switch resolution.State {
	case ResolutionCompleted:
		return e.completeReconciledStep(current, next, call.index, call.capability, resolution)
	case ResolutionPending:
		next.Status = protocolstate.OperationWaiting
		next.Steps[call.index].Status = protocolstate.StepWaiting
		next.Steps[call.index].Error = nil
		return e.updateOperation(current.Revision, next)
	case ResolutionSafeToRetry:
		return e.retryReconciledStep(ctx, current, next, call)
	case ResolutionDefinitiveError:
		if !validProtocolError(e.schemas, resolution.Error) {
			return e.suspend(current, "core.reconciliation.invalid_result", "Reconciliation returned an invalid error record.")
		}
		return e.failStep(ctx, current, call.index, call.definition, resolution.Error)
	case ResolutionNeedsOperator:
		return e.suspend(current, "core.reconciliation.operator_required", "Automatic reconciliation cannot safely determine the external outcome.")
	default:
		return e.suspend(current, "core.reconciliation.invalid_result", "Reconciliation returned an unsupported resolution state.")
	}
}

// completeReconciledStep records a reconciled output that matches the
// recorded capability as the step's success.
func (e *Engine) completeReconciledStep(current store.StoredOperation, next store.OperationRecord, stepIndex int, capability adapter.Capability, resolution Resolution) (store.StoredOperation, error) {
	if !isObject(resolution.Output) {
		return e.suspend(current, "core.reconciliation.invalid_result", "Reconciliation did not provide a valid completed output.")
	}
	if err := e.schemas.ValidateJSON(capability.OutputSchemaURI, resolution.Output); err != nil {
		return e.suspend(current, "core.reconciliation.invalid_result", "Reconciled output failed the recorded capability schema.")
	}
	if current.Record.Status != protocolstate.OperationRunning {
		next.Status = protocolstate.OperationRunning
	}
	next.Steps[stepIndex].Status = protocolstate.StepSucceeded
	next.Steps[stepIndex].Output = append(json.RawMessage(nil), resolution.Output...)
	next.Steps[stepIndex].Error = nil
	next.CurrentStepID = nil
	return e.updateOperation(current.Revision, next)
}

// retryReconciledStep durably claims a step that reconciliation reported
// safe to retry, within the attempt limit, and dispatches it again.
func (e *Engine) retryReconciledStep(ctx context.Context, current store.StoredOperation, next store.OperationRecord, call stepCall) (store.StoredOperation, error) {
	if call.step.Attempt >= e.maxStepAttempts {
		return e.suspend(current, "core.operation.retry_limit", "The safe retry limit for this step has been reached.")
	}
	if err := call.definition.CheckPreconditions(ctx, current.Record, call.step); err != nil {
		return e.handlePreconditionFailure(ctx, current, call.definition, preconditionFailure{step: call.step, cause: err, priorEffectResolved: true})
	}
	next.Status = protocolstate.OperationRunning
	next.Steps[call.index].Error = nil
	return e.claimStep(ctx, current, next, call)
}

func (e *Engine) validateExternalOperationRef(step store.OperationStep, value json.RawMessage) error {
	if len(value) == 0 {
		return nil
	}
	schemaID := "urn:gameserver-controller:" + e.schemas.ProtocolVersion() + ":common:external-resource-ref"
	if !isObject(value) || e.schemas.ValidateJSON(schemaID, value) != nil {
		return errors.New("external operation reference does not match protocol schema")
	}
	if len(step.ExternalOperationRef) > 0 && !equalJSON(step.ExternalOperationRef, value) {
		return errors.New("external operation reference conflicts with the recorded reference")
	}
	return nil
}
