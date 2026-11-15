package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerProvisionWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	assertion, err := BuildReadinessAssertion(w.schemas, operation, server.Record, provisionReadinessStep, w.assertionTTL)
	if err != nil {
		return nil, err
	}
	created, _ := provisionCreateStepOutput(operation)
	bootstrap := stepField(operationStep(operation, provisionBootstrapStep).Input, "bootstrap")
	var pinnedBootstrap struct {
		Revision string `json:"revision"`
		Digest   string `json:"digest"`
	}
	if json.Unmarshal(bootstrap, &pinnedBootstrap) != nil {
		return nil, errors.New("persisted bootstrap input cannot be decoded")
	}
	unit := stepField(operationStep(operation, provisionCreateUnitStep).Input, "unit_ref")
	var gameInput struct {
		ReleaseID string `json:"release_id"`
	}
	if json.Unmarshal(operationStep(operation, provisionInstallStep).Input, &gameInput) != nil {
		return nil, errors.New("persisted game install input cannot be decoded")
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return nil, errors.New("persisted provision request cannot be decoded")
	}
	data := map[string]any{"server_id": server.Record.ServerID, "environment_id": server.Record.EnvironmentID,
		"instance_ref": created.InstanceRef, "bootstrap": map[string]string{"revision": pinnedBootstrap.Revision, "digest": pinnedBootstrap.Digest},
		"game":               map[string]any{"adapter_id": request.Game.AdapterID, "release_ref": gameInput.ReleaseID},
		"execution":          map[string]any{"adapter_id": request.Execution.AdapterID, "unit_ref": unit},
		"data_generation_id": server.Record.DataGenerationID, "lifecycle_state": string(protocolstate.ServerReady), "readiness_assertion": assertion}
	if request.RestoreBackup != "" {
		data["restored_backup_id"] = request.RestoreBackup
	}
	result := map[string]any{"outcome_summary": "Server provisioned and all required game readiness checks passed.",
		"operation_type": "server.provision", "affected_resources": []any{created.InstanceRef, unit},
		"completed_steps": completedStepSummaries(operation), "failed_steps": []any{}, "warnings": []string{},
		"unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": false, "operation_data": data}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":operations:server-provision-result", encoded); err != nil {
		return nil, fmt.Errorf("server.provision typed result is invalid: %w", err)
	}
	return encoded, nil
}

func (w *ServerProvisionWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	failed := []any{}
	if step.StepID != "" && len(failure) > 0 && step.Status == protocolstate.StepFailed {
		failed = append(failed, map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure})
	}
	partial := operationStep(operation, provisionCreateStep).Attempt > 0
	instance := json.RawMessage(nil)
	if created, err := provisionCreateStepOutput(operation); err == nil {
		instance = created.InstanceRef
	}
	affected := []any{}
	if len(instance) > 0 {
		affected = append(affected, instance)
	}
	unresolved := []any{}
	if step.StepID == "" {
		unresolved = append(unresolved, map[string]any{"condition_id": "server.provision.postcondition_unmet", "description": "The workflow did not prove every exact provisioning and readiness postcondition."})
	}
	result := map[string]any{"outcome_summary": "Server provisioning did not satisfy all runtime and readiness postconditions.",
		"operation_type": "server.provision", "affected_resources": affected, "completed_steps": completedStepSummaries(operation),
		"failed_steps": failed, "warnings": []string{}, "unresolved_conditions": unresolved, "audit_event_ids": []string{}, "partial": partial}
	return json.Marshal(result)
}

func hasValidProvisionResult(raw json.RawMessage, target store.OperationTarget, instanceRef json.RawMessage, schemas schemaRuntime) bool {
	if schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-provision-result", raw) != nil {
		return false
	}
	var result struct {
		OperationType string `json:"operation_type"`
		Data          struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
			State         string          `json:"lifecycle_state"`
			Assertion     json.RawMessage `json:"readiness_assertion"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.OperationType != "server.provision" ||
		result.Data.ServerID != target.ServerID || result.Data.EnvironmentID != target.EnvironmentID {
		return false
	}
	return result.Data.State == string(protocolstate.ServerReady) &&
		equalJSON(result.Data.InstanceRef, instanceRef) && isObject(result.Data.Assertion)
}
