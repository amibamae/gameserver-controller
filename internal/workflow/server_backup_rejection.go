package workflow

import (
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func backupSaveRejected(operation store.OperationRecord) bool {
	step := operationStep(operation, backupSaveGameStep)
	input := decodeObject(effectiveStepInput(step))
	output := decodeObject(step.Output)
	if !backupStepReportedRejection(step, output) ||
		!backupRejectionFieldsMatch(input, output, "release_id") ||
		!backupRejectionTargetMatches(operation, input, output) {
		return false
	}
	return validObservedAt(stringValueMap(output, "observed_at"))
}

func backupRuntimeStopOutputSucceeded(operation store.OperationRecord) bool {
	_, err := backupRuntimeStopOutput(operation)
	return err == nil
}

func backupPreparationRejected(operation store.OperationRecord) bool {
	step := operationStep(operation, backupPrepareStep)
	input := decodeObject(effectiveStepInput(step))
	output := decodeObject(step.Output)
	if !backupStepReportedRejection(step, output) ||
		!backupRejectionFieldsMatch(input, output, "backup_id", "data_generation_id", "release_id", "consistency_mode") ||
		!sameJSONList(output["data_set_ids"], input["data_set_ids"]) ||
		!backupRejectionTargetMatches(operation, input, output) {
		return false
	}
	return validObservedAt(stringValueMap(output, "observed_at"))
}

func backupCaptureRejected(operation store.OperationRecord, dataSetID string) bool {
	step := operationStep(operation, backupDataStepID("capture", dataSetID))
	input := decodeObject(effectiveStepInput(step))
	output := decodeObject(step.Output)
	if !backupStepReportedRejection(step, output) ||
		!backupRejectionFieldsMatch(input, output, "backup_id", "data_generation_id", "preparation_id") ||
		!backupCaptureRejectionBound(operation, input, output, dataSetID) {
		return false
	}
	return validObservedAt(stringValueMap(output, "observed_at"))
}

func backupCaptureRejectionBound(operation store.OperationRecord, input, output map[string]any, dataSetID string) bool {
	return stringValueMap(output, "data_set_id") == dataSetID &&
		backupRejectionFieldsMatch(input, output, "data_set_id", "capture_id", "release_id", "consistency_mode") &&
		backupRejectionTargetMatches(operation, input, output)
}

func backupRejectionTargetMatches(operation store.OperationRecord, input, output map[string]any) bool {
	target := mustJSON(output["target"])
	return equalJSON(target, mustJSON(input["target"])) && backupTargetMatchesOperation(target, operation)
}

func backupValidationRejected(operation store.OperationRecord) bool {
	step := operationStep(operation, backupValidateStep)
	input := decodeObject(effectiveStepInput(step))
	output := decodeObject(step.Output)
	manifest := asStringMap(input["manifest"])
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || stringValueMap(output, "validation_result") != "invalid" {
		return false
	}
	if !backupRejectionMatchesOperation(operation, input) || !backupRejectionMatchesOperation(operation, output) ||
		stringValueMap(output, "data_generation_id") != stringValueMap(manifest, "data_generation_id") ||
		!validObservedAt(stringValueMap(output, "observed_at")) {
		return false
	}
	return backupValidationChecksFailed(output)
}

func backupRejectionMatchesOperation(operation store.OperationRecord, record map[string]any) bool {
	return stringValueMap(record, "backup_id") == operation.OperationID &&
		stringValueMap(record, "environment_id") == operation.Target.EnvironmentID
}

func backupValidationChecksFailed(output map[string]any) bool {
	results, ok := output["check_results"].([]any)
	if !ok || len(results) == 0 {
		return false
	}
	hasFailure := false
	for _, rawResult := range results {
		result := asStringMap(rawResult)
		if stringValueMap(result, "result") == "unknown" || !validObservedAt(stringValueMap(result, "observed_at")) {
			return false
		}
		if stringValueMap(result, "result") == "fail" {
			hasFailure = true
		}
	}
	return hasFailure
}

func backupStepReportedRejection(step store.OperationStep, output map[string]any) bool {
	return step.StepID != "" && step.Status == protocolstate.StepSucceeded &&
		stringValueMap(output, "outcome") == "rejected" && stringValueMap(output, "reason_code") != ""
}

func backupRejectionFieldsMatch(input, output map[string]any, fields ...string) bool {
	for _, field := range fields {
		if stringValueMap(output, field) != stringValueMap(input, field) {
			return false
		}
	}
	return true
}
