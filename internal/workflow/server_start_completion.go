package workflow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// ValidateCompletion enforces the runtime and readiness postconditions after
// all adapter steps have completed. A failed game check is a terminal failed
// start operation, not a falsely successful workflow.
func (w *ServerStartWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.start" {
		return errors.New("server.start completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !unitStartCompleted(operation) {
		return errors.New("execution unit start postcondition was not confirmed")
	}
	if !startUnitConfirmedRunning(operation) {
		return errors.New("execution unit is not confirmed running at the pinned reference")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	_, err = BuildReadinessAssertion(w.schemas, operation, stored.Record, startReadinessStep, w.ttl)
	return err
}

// unitStartCompleted reports whether the succeeded start step reported its
// own planned unit completed and running.
func unitStartCompleted(operation store.OperationRecord) bool {
	for _, step := range operation.Steps {
		if step.StepID != startRuntimeStep || step.Status != protocolstate.StepSucceeded {
			continue
		}
		var result executionUnitStartResult
		return json.Unmarshal(step.Output, &result) == nil && result.Outcome == "completed" && result.State == "running" && equalJSON(result.UnitRef, stepUnitRef(step))
	}
	return false
}

// startUnitConfirmedRunning reports whether the runtime inspection observed
// the started unit running.
func startUnitConfirmedRunning(operation store.OperationRecord) bool {
	inspection, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	return err == nil && inspection.State == "running" && equalJSON(inspection.UnitRef, stepUnitRef(operationStep(operation, startRuntimeStep)))
}

func (w *ServerStartWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	stored, err := loadCompletedServer(ctx, operation, w.ValidateCompletion, w.servers)
	if err != nil {
		return nil, err
	}
	assertion, err := BuildReadinessAssertion(w.schemas, operation, stored.Record, startReadinessStep, w.ttl)
	if err != nil {
		return nil, err
	}
	return json.Marshal(lifecycleSuccess{
		operationType: "server.start", summary: "Server runtime started and all required game readiness checks passed.",
		state: protocolstate.ServerReady, data: map[string]any{"readiness_assertion": assertion},
	}.document(operation, stored.Record))
}

func (w *ServerStartWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	result := lifecycleResult{
		operationType: "server.start", summary: "Server start did not satisfy all runtime and readiness postconditions.",
		failed:     failedStepSummaries(step, failure),
		unresolved: startUnresolvedConditions(operation, step),
		partial:    operationStep(operation, startRuntimeStep).Attempt > 0,
	}
	if instanceRef := w.failedStartInstance(operation); len(instanceRef) > 0 {
		result.affected = []any{instanceRef}
	}
	return json.Marshal(result.document(operation))
}

// startUnresolvedConditions explains an operation that failed without a
// failed step: a proven-unmet precondition when the stored error says so,
// otherwise unmet postconditions.
func startUnresolvedConditions(operation store.OperationRecord, step store.OperationStep) []any {
	if step.StepID != "" {
		return []any{}
	}
	conditionID := "server.start.postcondition_unmet"
	description := "The adapter calls completed, but the exact running and readiness postconditions were not satisfied."
	var protocolFailure struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(operation.Error, &protocolFailure) == nil && protocolFailure.Code == "core.safety.precondition_unmet" && protocolFailure.Message != "" {
		conditionID = "server.start.precondition_unmet"
		description = protocolFailure.Message
	}
	return []any{map[string]any{"condition_id": conditionID, "description": description}}
}

// failedStartInstance names the instance a failed start affected: the
// targeted one, else the server's current one.
func (w *ServerStartWorkflow) failedStartInstance(operation store.OperationRecord) json.RawMessage {
	if len(operation.Target.InstanceRef) > 0 {
		return operation.Target.InstanceRef
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		// A failure result must still be written when the server cannot be
		// loaded; it then lists no affected resource.
		return nil
	}
	return server.Record.InstanceRef
}
