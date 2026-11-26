package workflow

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// This file holds the step-evidence readers and checks that server.start,
// server.stop and the other lifecycle workflows share.

func operationStep(operation store.OperationRecord, id string) store.OperationStep {
	for _, step := range operation.Steps {
		if step.StepID == id {
			return step
		}
	}
	return store.OperationStep{}
}

func stepBindsRef(input json.RawMessage, field string, expected json.RawMessage) bool {
	var decoded map[string]json.RawMessage
	if json.Unmarshal(input, &decoded) != nil || decoded == nil {
		return false
	}
	return equalJSON(decoded[field], expected)
}

func sameStepReference(operation store.OperationRecord, firstID, secondID, field string) bool {
	first := operationStep(operation, firstID)
	second := operationStep(operation, secondID)
	return first.StepID != "" && second.StepID != "" && stepBindsRef(first.Input, field, stepField(second.Input, field))
}

func stepField(input json.RawMessage, field string) json.RawMessage {
	var decoded map[string]json.RawMessage
	if json.Unmarshal(input, &decoded) != nil {
		return nil
	}
	return decoded[field]
}

func stepSucceeded(operation store.OperationRecord, stepID string) bool {
	for _, step := range operation.Steps {
		if step.StepID == stepID {
			return step.Status == protocolstate.StepSucceeded
		}
	}
	return false
}

// succeededStepOutput decodes the output of the first succeeded step with
// stepID. It returns missing as the error when no such step exists, and the
// zero value with the decode error when the output is not a T.
func succeededStepOutput[T any](operation store.OperationRecord, stepID, missing string) (T, error) {
	var output T
	for _, step := range operation.Steps {
		if step.StepID != stepID || step.Status != protocolstate.StepSucceeded {
			continue
		}
		if err := json.Unmarshal(step.Output, &output); err != nil {
			var zero T
			return zero, err
		}
		return output, nil
	}
	return output, errors.New(missing)
}

// plannedStepOutput decodes the output of the first step with id, which must
// have succeeded. It returns missing as the error otherwise, and the zero
// value with the decode error when the output is not a T.
func plannedStepOutput[T any](operation store.OperationRecord, id, missing string) (T, error) {
	var output T
	step := operationStep(operation, id)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
		return output, errors.New(missing)
	}
	if err := json.Unmarshal(step.Output, &output); err != nil {
		var zero T
		return zero, err
	}
	return output, nil
}

func instanceStepOutput(operation store.OperationRecord, stepID string) (instanceInspection, error) {
	return succeededStepOutput[instanceInspection](operation, stepID, "provider inspection output is missing")
}

func unitInspectStepOutput(operation store.OperationRecord, stepID string) (executionUnitInspection, error) {
	return succeededStepOutput[executionUnitInspection](operation, stepID, "execution unit inspection output is missing")
}

// instanceObservedActive reports whether the succeeded provider inspection
// in stepID observed exactly instanceRef in the active state.
func instanceObservedActive(operation store.OperationRecord, stepID string, instanceRef json.RawMessage) bool {
	observation, err := instanceStepOutput(operation, stepID)
	return err == nil && equalJSON(observation.InstanceRef, instanceRef) && observation.State == "active"
}

// plannedInstanceObservation returns the succeeded provider inspection in
// stepID when it observed the instance that step was planned to inspect.
func plannedInstanceObservation(operation store.OperationRecord, stepID string) (instanceInspection, bool) {
	observation, err := instanceStepOutput(operation, stepID)
	if err != nil || !equalJSON(observation.InstanceRef, stepField(operationStep(operation, stepID).Input, "instance_ref")) {
		return instanceInspection{}, false
	}
	return observation, true
}

// instanceDefinitivelyInactive lists the provider states that prove an
// instance cannot serve the next runtime side effect.
func instanceDefinitivelyInactive(state string) bool {
	switch state {
	case "creating", "stopped", "destroying", "destroyed", "failed":
		return true
	}
	return false
}

// preconditionUnmet is the terminal resolution for a precondition that a
// stored, target-bound inspection proves cannot be met.
func preconditionUnmet(operationID, message string) Resolution {
	return Resolution{
		State: ResolutionDefinitiveError,
		Error: protocolError("core.safety.precondition_unmet", "safety_blocked", message, operationID, false, "inspect_operation", false),
	}
}

// loadPreconditionServer rechecks the context, loads the target server and
// requires that it still has the managed instance the operation targets.
func loadPreconditionServer(ctx context.Context, servers store.LifecycleOperationStore, operation store.OperationRecord) (store.LogicalServer, error) {
	if err := ctx.Err(); err != nil {
		return store.LogicalServer{}, err
	}
	stored, err := servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.LogicalServer{}, err
	}
	if serverTargetMismatch(stored.Record, operation.Target) {
		return store.LogicalServer{}, store.ErrLogicalServerTargetMismatch
	}
	return stored.Record, nil
}

func serverTargetMismatch(server store.LogicalServer, target store.OperationTarget) bool {
	return len(server.InstanceRef) == 0 || (len(target.InstanceRef) > 0 && !equalJSON(target.InstanceRef, server.InstanceRef))
}

// projectionTargets reports whether a lifecycle projection of operationType
// is applied to the server the operation targets.
func projectionTargets(operation store.OperationRecord, operationType string, server store.LogicalServer) bool {
	return operation.OperationType == operationType && server.ServerID == operation.Target.ServerID &&
		server.EnvironmentID == operation.Target.EnvironmentID
}

func completedStepSummaries(operation store.OperationRecord) []any {
	result := make([]any, 0, len(operation.Steps))
	for _, step := range operation.Steps {
		if step.Status == protocolstate.StepSucceeded {
			result = append(result, map[string]any{"step_id": step.StepID, "step_type": step.StepType})
		}
	}
	return result
}

// failedStepSummaries reports step as failed when it carries a failure.
func failedStepSummaries(step store.OperationStep, failure json.RawMessage) []any {
	if step.StepID == "" || len(failure) == 0 || step.Status != protocolstate.StepFailed {
		return []any{}
	}
	return []any{map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure}}
}

// lifecycleResult holds the fields of a runtime lifecycle result document
// that differ between workflows and outcomes.
type lifecycleResult struct {
	operationType string
	summary       string
	affected      []any
	failed        []any
	unresolved    []any
	partial       bool
}

// document returns the result fields every lifecycle result carries. Nil
// lists are written as empty lists.
func (r lifecycleResult) document(operation store.OperationRecord) map[string]any {
	return map[string]any{
		"outcome_summary": r.summary,
		"operation_type":  r.operationType, "affected_resources": nonNilList(r.affected),
		"completed_steps": completedStepSummaries(operation), "failed_steps": nonNilList(r.failed),
		"warnings": []string{}, "unresolved_conditions": nonNilList(r.unresolved), "audit_event_ids": []string{}, "partial": r.partial,
	}
}

func nonNilList(values []any) []any {
	if values == nil {
		return []any{}
	}
	return values
}

// lifecycleResultDocument is the part of a terminal lifecycle result that a
// projection checks before it trusts the result.
type lifecycleResultDocument struct {
	OperationType string `json:"operation_type"`
	OperationData struct {
		ServerID      string          `json:"server_id"`
		EnvironmentID string          `json:"environment_id"`
		InstanceRef   json.RawMessage `json:"instance_ref"`
		State         string          `json:"lifecycle_state"`
		Assertion     json.RawMessage `json:"readiness_assertion"`
	} `json:"operation_data"`
}

// resultNamesTarget reports whether a decoded result is of operationType and
// reports the target server in the given lifecycle state.
func resultNamesTarget(result lifecycleResultDocument, operationType string, state protocolstate.ServerState, target store.OperationTarget) bool {
	data := result.OperationData
	return result.OperationType == operationType && data.ServerID == target.ServerID &&
		data.EnvironmentID == target.EnvironmentID && data.State == string(state)
}

// resultBindsInstance reports whether a result's instance is the current
// instance and, when the operation pinned one, the targeted instance.
func resultBindsInstance(reported, targeted, current json.RawMessage) bool {
	return (len(targeted) == 0 || equalJSON(reported, targeted)) && equalJSON(reported, current)
}
