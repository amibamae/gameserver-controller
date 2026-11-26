package workflow

import (
	"context"
	"encoding/json"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// Reconcile resolves an ambiguous unit start by using the exact read-only
// inspection capability pinned in the same durable plan. It never retries
// until inspection confirms the unit is absent or stopped.
func (w *ServerStartWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil || step.StepID != startRuntimeStep || step.CapabilityName != "execution.unit.start" {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	needsOperator := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	inspect := operationStep(operation, startVerifyRuntimeStep)
	if !pinnedUnitInspect(inspect, step) {
		return needsOperator, nil
	}
	observation, observed := observeUnit(ctx, w.adapters, w.schemas, unitProbe{
		operation: operation, step: step, inspect: inspect, unitRef: stepUnitRef(step),
		requestID: startReconcileRequestID(operation.OperationID, step.StepID, step.Attempt),
	})
	if !observed {
		return needsOperator, nil
	}
	return startUnitResolution(operation, step, observation), nil
}

// startUnitResolution maps the observed state of the exact unit to the
// resolution of its ambiguous start.
func startUnitResolution(operation store.OperationRecord, step store.OperationStep, observation executionUnitInspection) Resolution {
	switch observation.State {
	case "running":
		return Resolution{State: ResolutionCompleted, Output: startedUnitOutput(observation), ExternalOperationRef: step.ExternalOperationRef}
	case "absent", "stopped":
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}
	case "starting":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}
	case "failed":
		return Resolution{
			State: ResolutionDefinitiveError, ExternalOperationRef: step.ExternalOperationRef,
			Error: protocolError("core.execution.unit_failed", "provider_failure", "The exact execution unit is confirmed failed after a start attempt.", operation.OperationID, false, "operator_review", true),
		}
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	}
}

func startedUnitOutput(observation executionUnitInspection) json.RawMessage {
	// The struct holds strings and a unit reference decoded from valid JSON,
	// so encoding it cannot fail.
	output, _ := json.Marshal(executionUnitStartResult{
		UnitRef: append(json.RawMessage(nil), observation.UnitRef...), Outcome: "completed",
		State: "running", ObservedAt: observation.ObservedAt,
	})
	return output
}

func stepUnitRef(step store.OperationStep) json.RawMessage {
	return stepField(step.Input, "unit_ref")
}

func startReconcileRequestID(operationID, stepID string, attempt int) string {
	return reconcileRequestID(operationID, stepID, "reconcile", attempt)
}
