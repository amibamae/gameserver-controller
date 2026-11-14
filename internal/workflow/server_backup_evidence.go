package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type backupDataSetDescription struct {
	DataSetID                 string   `json:"data_set_id"`
	RelativePaths             []string `json:"relative_paths"`
	Required                  bool     `json:"required"`
	SupportedConsistencyModes []string `json:"supported_consistency_modes"`
	DataFormat                string   `json:"data_format,omitempty"`
}

func backupDataDescriptionMatches(operation store.OperationRecord, profile ServerBackupProfile, server store.LogicalServer) bool {
	step := operationStep(operation, backupDescribeDataStep)
	if !backupEvidenceStepSucceeded(step) {
		return false
	}
	var input struct {
		Target    json.RawMessage `json:"target"`
		ReleaseID string          `json:"release_id"`
	}
	var output struct {
		Target     json.RawMessage            `json:"target"`
		ReleaseID  string                     `json:"release_id"`
		DataSets   []backupDataSetDescription `json:"data_sets"`
		ObservedAt string                     `json:"observed_at"`
	}
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || json.Unmarshal(step.Output, &output) != nil {
		return false
	}
	if !backupDescriptionMatchesRequest(input.Target, output.Target, input.ReleaseID, output.ReleaseID, profile.GameReleaseID) {
		return false
	}
	target := mustJSON(map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID, "instance_ref": server.InstanceRef})
	if !equalJSON(output.Target, target) {
		return false
	}
	return backupDataSetDescriptionsMatch(output.DataSets, profile.DataSets, output.ObservedAt)
}

func backupDataSetDescriptionsMatch(actual []backupDataSetDescription, trusted []ServerBackupDataSet, observedAt string) bool {
	if len(actual) != len(trusted) {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, observedAt); err != nil {
		return false
	}
	for i, description := range actual {
		if !backupDataSetDescriptionMatches(description, trusted[i]) {
			return false
		}
	}
	return true
}

func backupEvidenceStepSucceeded(step store.OperationStep) bool {
	return step.StepID != "" && step.Status == protocolstate.StepSucceeded
}

func backupDescriptionMatchesRequest(inputTarget, outputTarget json.RawMessage, inputRelease, outputRelease, expectedRelease string) bool {
	return equalJSON(inputTarget, outputTarget) && inputRelease == expectedRelease && outputRelease == expectedRelease
}

func backupDataSetDescriptionMatches(actual backupDataSetDescription, trusted ServerBackupDataSet) bool {
	if actual.DataSetID != trusted.DataSetID || actual.Required != trusted.Required || actual.DataFormat != trusted.DataFormat {
		return false
	}
	if !sameStringSlice(actual.RelativePaths, trusted.RelativePaths) ||
		!sameStringSlice(actual.SupportedConsistencyModes, trusted.SupportedConsistencyModes) {
		return false
	}
	for _, relative := range actual.RelativePaths {
		if !validBackupRelativePath(relative) {
			return false
		}
	}
	return true
}

func runtimeExpectedForBackup(operation store.OperationRecord, unit stopUnitInspection, err error) bool {
	if err != nil || !equalJSON(unit.UnitRef, stepField(operationStep(operation, backupInspectRuntimeStep).Input, "unit_ref")) {
		return false
	}
	if backupInitiallyActive(operation) {
		stopped, stopErr := backupRuntimeStopOutput(operation)
		return stopErr == nil && stopped.State == "stopped" && unit.State == "running"
	}
	return unit.State == "stopped" || unit.State == "absent"
}

func backupInitiallyActive(operation store.OperationRecord) bool {
	state := backupInitialLifecycle(operation)
	return state == protocolstate.ServerReady || state == protocolstate.ServerDegraded
}

func backupInitialLifecycle(operation store.OperationRecord) protocolstate.ServerState {
	if operation.BackupInitialLifecycleState != "" {
		return operation.BackupInitialLifecycleState
	}
	if operationStep(operation, backupStopRuntimeStep).StepID != "" {
		return protocolstate.ServerReady
	}
	return protocolstate.ServerStopped
}

func backupProviderInspection(operation store.OperationRecord) (instanceInspection, error) {
	return instanceStepOutput(operation, backupInspectInstanceStep)
}

func backupRuntimeInspection(operation store.OperationRecord, stepID string) (stopUnitInspection, error) {
	var output stopUnitInspection
	step := operationStep(operation, stepID)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("execution unit inspection output is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return stopUnitInspection{}, err
	}
	return output, nil
}

func backupRuntimeStopOutput(operation store.OperationRecord) (executionUnitStopResult, error) {
	var output executionUnitStopResult
	step := operationStep(operation, backupStopRuntimeStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil ||
		!backupStopOutputMatchesUnit(step, output) {
		return executionUnitStopResult{}, errors.New("runtime stop output does not confirm the exact unit stopped")
	}
	return output, nil
}

func backupStopOutputMatchesUnit(step store.OperationStep, output executionUnitStopResult) bool {
	return output.Outcome == "completed" && output.State == "stopped" && stepBindsRef(step.Input, "unit_ref", output.UnitRef)
}

func gameBackupSaveConfirmed(operation store.OperationRecord) bool {
	step := operationStep(operation, backupSaveGameStep)
	if !backupEvidenceStepSucceeded(step) {
		return false
	}
	var input, output struct {
		Target struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		ReleaseID  string `json:"release_id"`
		Outcome    string `json:"outcome"`
		ObservedAt string `json:"observed_at"`
		SavedAt    string `json:"saved_at"`
	}
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || json.Unmarshal(step.Output, &output) != nil {
		return false
	}
	if !backupSaveMatchesTarget(operation, output.Outcome, output.Target.ServerID, output.Target.EnvironmentID) || output.ReleaseID != input.ReleaseID {
		return false
	}
	if !equalJSON(output.Target.InstanceRef, input.Target.InstanceRef) ||
		!equalJSON(input.Target.InstanceRef, stepField(operationStep(operation, backupInspectInstanceStep).Input, "instance_ref")) {
		return false
	}
	_, observedErr := time.Parse(time.RFC3339Nano, output.ObservedAt)
	_, savedErr := time.Parse(time.RFC3339Nano, output.SavedAt)
	return observedErr == nil && savedErr == nil
}

func backupSaveMatchesTarget(operation store.OperationRecord, outcome, serverID, environmentID string) bool {
	return outcome == "confirmed" && serverID == operation.Target.ServerID && environmentID == operation.Target.EnvironmentID
}

func backupReadbacksComplete(operation store.OperationRecord, profile ServerBackupProfile) bool {
	for _, dataSet := range profile.DataSets {
		capture, captureErr := backupCaptureOutput(operation, dataSet.DataSetID)
		put, putErr := backupPutOutput(operation, dataSet.DataSetID)
		get, getErr := backupGetOutput(operation, dataSet.DataSetID)
		if captureErr != nil || putErr != nil || getErr != nil {
			return false
		}
		if capture.Outcome != "captured" || !backupUploadMatchesCapture(put, capture) || !backupReadbackMatchesCapture(get, put, capture) {
			return false
		}
	}
	return true
}

func backupDataDescribeInputMatches(operation store.OperationRecord, profile ServerBackupProfile, server store.LogicalServer) bool {
	step := operationStep(operation, backupDescribeDataStep)
	var input struct {
		Target struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		ReleaseID string `json:"release_id"`
	}
	if step.StepID == "" || json.Unmarshal(effectiveStepInput(step), &input) != nil {
		return false
	}
	return input.Target.ServerID == server.ServerID && input.Target.EnvironmentID == server.EnvironmentID &&
		equalJSON(input.Target.InstanceRef, server.InstanceRef) && input.ReleaseID == profile.GameReleaseID
}

func backupValidationConfirmed(operation store.OperationRecord, profile ServerBackupProfile) bool {
	step := operationStep(operation, backupValidateStep)
	if !backupEvidenceStepSucceeded(step) {
		return false
	}
	var output backupValidationObservation
	if json.Unmarshal(step.Output, &output) != nil {
		return false
	}
	if !backupValidationMatchesOperation(operation, output, len(profile.ValidationChecks)) {
		return false
	}
	var input struct {
		Manifest struct {
			BackupID         string `json:"backup_id"`
			EnvironmentID    string `json:"environment_id"`
			DataGenerationID string `json:"data_generation_id"`
		} `json:"manifest"`
	}
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || input.Manifest.BackupID != output.BackupID ||
		input.Manifest.EnvironmentID != output.EnvironmentID || input.Manifest.DataGenerationID != output.DataGenerationID {
		return false
	}
	if !backupValidationChecksPassed(profile, output) {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, output.ObservedAt)
	return err == nil
}

func backupValidationMatchesOperation(operation store.OperationRecord, output backupValidationObservation, checkCount int) bool {
	return output.ValidationResult == "valid" && output.BackupID == operation.OperationID &&
		output.EnvironmentID == operation.Target.EnvironmentID && len(output.CheckResults) == checkCount
}

func backupValidationChecksPassed(profile ServerBackupProfile, output backupValidationObservation) bool {
	requested := make(map[string]string, len(profile.ValidationChecks))
	for _, check := range profile.ValidationChecks {
		requested[check.CheckID] = check.CheckVersion
	}
	for _, result := range output.CheckResults {
		version, ok := requested[result.CheckID]
		if !ok || version != result.CheckVersion || result.Result != "pass" {
			return false
		}
		delete(requested, result.CheckID)
		if _, err := time.Parse(time.RFC3339Nano, result.ObservedAt); err != nil {
			return false
		}
	}
	return len(requested) == 0
}

func (w *ServerBackupWorkflow) verifyBackupValidationStreams(ctx context.Context, operation store.OperationRecord, profile ServerBackupProfile) error {
	expectations := make([]ServerBackupStreamExpectation, 0, len(profile.DataSets))
	for _, dataSet := range profile.DataSets {
		capture, err := backupCaptureOutput(operation, dataSet.DataSetID)
		if err != nil {
			return err
		}
		expectations = append(expectations, ServerBackupStreamExpectation{
			DataSetID: dataSet.DataSetID, SizeBytes: capture.SizeBytes, MaximumBytes: maxInt64(capture.SizeBytes, 1), SHA256: capture.Checksum.Digest,
		})
	}
	return w.verifyStreams(ctx, operation, backupValidateStep, expectations)
}

func backupRuntimeStartCompleted(operation store.OperationRecord) bool {
	step := operationStep(operation, backupStartRuntimeStep)
	if !backupEvidenceStepSucceeded(step) {
		return false
	}
	var output executionUnitStartResult
	return json.Unmarshal(step.Output, &output) == nil && output.Outcome == "completed" && output.State == "running" &&
		equalJSON(output.UnitRef, stepField(step.Input, "unit_ref"))
}

func backupRuntimeRunning(operation store.OperationRecord) bool {
	output, err := backupRuntimeInspection(operation, backupVerifyRuntimeStep)
	return err == nil && output.State == "running" && equalJSON(output.UnitRef, stepField(operationStep(operation, backupStartRuntimeStep).Input, "unit_ref"))
}

func backupDataSetIDFromStep(operation store.OperationRecord, stepID, action string) (string, bool) {
	if operationStep(operation, stepID).StepID == "" {
		return "", false
	}
	for _, dataSetID := range backupPlannedDataSetIDs(operation) {
		if backupDataStepID(action, dataSetID) == stepID {
			return dataSetID, true
		}
	}
	return "", false
}

func backupDataSetForOperation(operation store.OperationRecord, stepID, action string) (ServerBackupDataSet, bool) {
	dataSetID, ok := backupDataSetIDFromStep(operation, stepID, action)
	return ServerBackupDataSet{DataSetID: dataSetID}, ok
}

func backupTargetMatchesOperation(raw json.RawMessage, operation store.OperationRecord) bool {
	var target struct {
		ServerID      string          `json:"server_id"`
		EnvironmentID string          `json:"environment_id"`
		InstanceRef   json.RawMessage `json:"instance_ref"`
	}
	if json.Unmarshal(raw, &target) != nil || target.ServerID != operation.Target.ServerID ||
		target.EnvironmentID != operation.Target.EnvironmentID {
		return false
	}
	return equalJSON(target.InstanceRef, stepField(operationStep(operation, backupInspectInstanceStep).Input, "instance_ref"))
}

func validObservedAt(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && !parsed.IsZero()
}

func sameJSONList(left, right any) bool {
	return equalJSON(mustJSON(left), mustJSON(right))
}

func decodeObject(raw json.RawMessage) map[string]any {
	var output map[string]any
	if json.Unmarshal(raw, &output) != nil || output == nil {
		return map[string]any{}
	}
	return output
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}
