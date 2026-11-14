package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func backupResumeAfterBackup(operation store.OperationRecord) bool {
	return operationStep(operation, backupStartRuntimeStep).StepID != ""
}

func backupFinalRuntimeStopped(operation store.OperationRecord) bool {
	step := operationStep(operation, backupVerifyStoppedStep)
	unit, err := backupRuntimeInspection(operation, backupVerifyStoppedStep)
	return err == nil && (unit.State == "stopped" || unit.State == "absent") &&
		stepBindsRef(step.Input, "unit_ref", unit.UnitRef)
}

func (w *ServerBackupWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	stored, _, err := w.loadProfile(ctx, operation)
	if err != nil {
		return nil, err
	}
	validationStep := operationStep(operation, backupValidateStep)
	var validation backupValidationObservation
	if json.Unmarshal(validationStep.Output, &validation) != nil {
		return nil, errors.New("decode completed backup validation output")
	}
	var validationInput struct {
		Manifest json.RawMessage `json:"manifest"`
	}
	if json.Unmarshal(effectiveStepInput(validationStep), &validationInput) != nil || !isObject(validationInput.Manifest) {
		return nil, errors.New("completed backup validation has no resolved manifest")
	}
	manifest := decodeObject(validationInput.Manifest)
	dataGenerationID := stringValueMap(manifest, "data_generation_id")
	prepared, prepareErr := backupPrepareOutput(operation)
	if prepareErr != nil || !backupManifestGenerationBound(operation, dataGenerationID, prepared.DataGenerationID, stored.Record.DataGenerationID) {
		return nil, errors.New("verified manifest is not bound to the accepted data generation")
	}
	finalState := string(protocolstate.ServerStopped)
	operationData := map[string]any{
		"backup_id": operation.OperationID, "environment_id": operation.Target.EnvironmentID,
		"data_generation_id": dataGenerationID, "manifest": manifest,
		"verified_at": validation.ObservedAt, "final_lifecycle_state": finalState,
	}
	if backupResumeAfterBackup(operation) {
		finalState = string(protocolstate.ServerReady)
		assertion, err := BuildReadinessAssertion(w.schemas, operation, stored.Record, backupReadinessStep, w.readinessTTL)
		if err != nil {
			return nil, err
		}
		operationData["final_lifecycle_state"] = finalState
		operationData["readiness_assertion"] = json.RawMessage(assertion)
	}
	result := map[string]any{
		"outcome_summary": "Backup objects were captured, read back, and game-validated for the recorded data generation.",
		"operation_type":  "server.backup", "affected_resources": []any{stored.Record.InstanceRef},
		"completed_steps": completedStepSummaries(operation), "failed_steps": []any{},
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": false,
		"operation_data": operationData,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":operations:server-backup-result", encoded); err != nil {
		return nil, fmt.Errorf("server.backup result does not match protocol schema: %w", err)
	}
	return encoded, nil
}

// A resumed server can already have a new generation. Its manifest must
// still name the generation recorded when the backup was prepared.
func backupManifestGenerationBound(operation store.OperationRecord, manifest, prepared, current string) bool {
	return manifest != "" && manifest == prepared && (manifest == current || backupResumeAfterBackup(operation))
}

func (w *ServerBackupWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	failed := []any{}
	if step.StepID != "" && len(failure) > 0 && step.Status == protocolstate.StepFailed {
		failed = append(failed, map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure})
	}
	partial := false
	for _, planned := range operation.Steps {
		if planned.Attempt > 0 && backupStepMayMutate(planned.CapabilityName) {
			partial = true
			break
		}
	}
	result := map[string]any{
		"outcome_summary": "Server backup did not establish a complete, verified backup result.",
		"operation_type":  "server.backup", "affected_resources": []any{},
		"completed_steps": completedStepSummaries(operation), "failed_steps": failed,
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": partial,
	}
	instanceRef := operation.Target.InstanceRef
	if len(instanceRef) == 0 && w != nil && w.servers != nil {
		if server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID); err == nil {
			instanceRef = server.Record.InstanceRef
		}
	}
	if len(instanceRef) > 0 {
		result["affected_resources"] = []any{instanceRef}
	}
	if step.StepID == "" {
		result["unresolved_conditions"] = []any{map[string]any{
			"condition_id": "server.backup.postcondition_unmet",
			"description":  "The operation did not establish exact capture, storage readback, and game-validation evidence.",
		}}
	}
	return json.Marshal(result)
}

func backupStepMayMutate(capability string) bool {
	switch capability {
	case "game.save.request", "execution.unit.graceful_stop", "game.backup.prepare", "game.backup.capture",
		"storage.object.put", "execution.unit.start":
		return true
	default:
		return false
	}
}

func (w *ServerBackupWorkflow) FinalizeOperation(operation store.OperationRecord) {
	if w == nil || w.transfers == nil || operation.OperationType != "server.backup" ||
		!protocolstate.IsTerminalOperation(operation.Status) {
		return
	}
	if cleaner, ok := w.transfers.(serverBackupTransferCleaner); ok {
		_ = cleaner.CleanupOperation(operation.OperationID)
	}
}

func backupNextGenerationID(operationID, previous string) string {
	digest := sha256.Sum256([]byte("server.backup\x00" + operationID + "\x00" + previous))
	return "generation-" + hex.EncodeToString(digest[:16])
}

type backupResultData struct {
	BackupID         string          `json:"backup_id"`
	EnvironmentID    string          `json:"environment_id"`
	DataGenerationID string          `json:"data_generation_id"`
	Manifest         json.RawMessage `json:"manifest"`
	FinalState       string          `json:"final_lifecycle_state"`
	Readiness        json.RawMessage `json:"readiness_assertion"`
}

type backupResultManifest struct {
	ManifestVersion   string          `json:"manifest_version"`
	BackupID          string          `json:"backup_id"`
	ServerID          string          `json:"server_id"`
	EnvironmentID     string          `json:"environment_id"`
	DataGenerationID  string          `json:"data_generation_id"`
	SourceInstanceRef json.RawMessage `json:"source_instance_ref"`
	SourceOperationID string          `json:"source_operation_id"`
}

func hasValidBackupResult(raw json.RawMessage, operation store.OperationRecord, server store.LogicalServer, schemas schemaRuntime, expectedState protocolstate.ServerState) bool {
	if schemas == nil || schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-backup-result", raw) != nil {
		return false
	}
	var result struct {
		OperationType string           `json:"operation_type"`
		OperationData backupResultData `json:"operation_data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.OperationType != "server.backup" {
		return false
	}
	data := result.OperationData
	if !backupResultBindsOperation(data, operation, expectedState) || !backupResultManifestBound(data, operation, server) {
		return false
	}
	if expectedState == protocolstate.ServerReady {
		return backupResultReadinessBound(data.Readiness, server, schemas)
	}
	return backupFinalRuntimeStopped(operation)
}

func backupResultBindsOperation(data backupResultData, operation store.OperationRecord, expectedState protocolstate.ServerState) bool {
	return data.BackupID == operation.OperationID && data.EnvironmentID == operation.Target.EnvironmentID &&
		data.FinalState == string(expectedState) && isObject(data.Manifest)
}

func backupResultManifestBound(data backupResultData, operation store.OperationRecord, server store.LogicalServer) bool {
	var manifest backupResultManifest
	if json.Unmarshal(data.Manifest, &manifest) != nil || manifest.ManifestVersion != backupManifestVersion {
		return false
	}
	if manifest.BackupID != operation.OperationID || manifest.ServerID != operation.Target.ServerID ||
		manifest.EnvironmentID != operation.Target.EnvironmentID {
		return false
	}
	return manifest.DataGenerationID == data.DataGenerationID && manifest.SourceOperationID == operation.OperationID &&
		equalJSON(manifest.SourceInstanceRef, server.InstanceRef)
}

func backupResultReadinessBound(raw json.RawMessage, server store.LogicalServer, schemas schemaRuntime) bool {
	var assertion readinessAssertion
	if json.Unmarshal(raw, &assertion) != nil || assertion.ServerID != server.ServerID ||
		assertion.EnvironmentID != server.EnvironmentID || !equalJSON(assertion.InstanceRef, server.InstanceRef) {
		return false
	}
	return schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":lifecycle:readiness-assertion", raw) == nil
}
