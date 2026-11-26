package workflow

import (
	"context"
	"encoding/json"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerStopWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	switch step.StepID {
	case stopSaveGameStep:
		// The current game capability has no read-only save-status capability.
		// An accepted or unknown save cannot be inferred from process state.
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	case stopRuntimeStep:
		return w.reconcileRuntimeStop(ctx, operation, step)
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

// reconcileRuntimeStop resolves an ambiguous graceful stop by inspecting the
// exact planned unit with the inspection capability pinned in the plan.
func (w *ServerStopWorkflow) reconcileRuntimeStop(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	needsOperator := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	inspect := operationStep(operation, stopInspectUnitStep)
	unitRef := stopUnitRef(operation)
	if !pinnedUnitInspect(inspect, step) || !stepBindsRef(inspect.Input, "unit_ref", unitRef) {
		return needsOperator, nil
	}
	observed, ok := observeUnit(ctx, w.adapters, w.schemas, unitProbe{
		operation: operation, step: step, inspect: inspect, unitRef: unitRef,
		requestID: stopReconcileRequestID(operation.OperationID, step.StepID, step.Attempt),
	})
	if !ok {
		return needsOperator, nil
	}
	return stopUnitResolution(step, observed), nil
}

// stopUnitResolution maps the observed state of the exact unit to the
// resolution of its ambiguous graceful stop. An absent unit needs an
// operator because a graceful stop leaves the unit stopped, not absent.
func stopUnitResolution(step store.OperationStep, observed executionUnitInspection) Resolution {
	switch observed.State {
	case "stopped":
		return Resolution{State: ResolutionCompleted, Output: stoppedUnitOutput(observed), ExternalOperationRef: step.ExternalOperationRef}
	case "running":
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}
	case "stopping":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	}
}

func stoppedUnitOutput(observed executionUnitInspection) json.RawMessage {
	// The map holds strings and a unit reference decoded from valid JSON, so
	// encoding it cannot fail.
	output, _ := json.Marshal(map[string]any{"unit_ref": observed.UnitRef, "outcome": "completed", "state": "stopped", "observed_at": observed.ObservedAt})
	return output
}

func stopReconcileRequestID(operationID, stepID string, attempt int) string {
	return reconcileRequestID(operationID, stepID, "reconcile-stop", attempt)
}
