package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// Recover marks a step that was still running at process shutdown as
// outcome_unknown. It makes no adapter calls; callers may later Run each
// returned recoverable operation to reconcile or resume it.
func (e *Engine) Recover(ctx context.Context) ([]store.StoredOperation, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	operations, err := e.store.ListRecoverableOperations()
	if err != nil {
		return nil, err
	}
	result := make([]store.StoredOperation, 0, len(operations))
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.begin(operation.Record.OperationID) {
			continue
		}
		updated, updateErr := e.recoverOne(operation)
		e.end(operation.Record.OperationID)
		if updateErr != nil {
			return nil, updateErr
		}
		result = append(result, updated)
	}
	return result, nil
}

// Resume leaves needs_operator only after the injected authorizer approves
// this exact durable operation. It resumes at reconciliation; it never
// replays a possibly dispatched step directly.
func (e *Engine) Resume(ctx context.Context, authenticatedOperatorID, operationID string) (store.StoredOperation, error) {
	if ctx == nil || authenticatedOperatorID == "" || operationID == "" {
		return store.StoredOperation{}, errors.New("context, authenticated operator ID, and operation ID are required")
	}
	if !e.begin(operationID) {
		return store.StoredOperation{}, ErrOperationInProgress
	}
	defer e.end(operationID)
	current, err := e.store.GetOperation(operationID)
	if err != nil {
		return store.StoredOperation{}, err
	}
	if current.Record.Status != protocolstate.OperationNeedsOperator {
		return current, ErrNotRecoverable
	}
	if err := e.resumeAuthorizer.AuthorizeResume(ctx, authenticatedOperatorID, current.Record); err != nil {
		return current, fmt.Errorf("authorize operation resume: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return current, err
	}
	next := current.Record
	next.Status = protocolstate.OperationReconciling
	next.Error = nil
	return e.updateOperation(current.Revision, next)
}

func (e *Engine) recoverOne(current store.StoredOperation) (store.StoredOperation, error) {
	if current.Record.Status != protocolstate.OperationRunning && current.Record.Status != protocolstate.OperationWaiting && current.Record.Status != protocolstate.OperationReconciling {
		return current, nil
	}
	index := currentStepIndex(current.Record)
	if index < 0 || current.Record.Steps[index].Status != protocolstate.StepRunning {
		return current, nil
	}
	return e.markOutcomeUnknown(current, index, reconcileRequired("core.adapter.outcome_unknown", "The controller restarted while this adapter step was running.", current.Record.OperationID))
}
