package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerBackupWorkflow) ResolveStepInput(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (json.RawMessage, error) {
	if ctx == nil || w == nil || operation.OperationType != "server.backup" {
		return nil, errors.New("server.backup input resolution requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resolved := make(map[string]any)
	if err := json.Unmarshal(step.Input, &resolved); err != nil || resolved == nil {
		return nil, fmt.Errorf("decode planned %s input", step.StepID)
	}
	_, profile, err := w.loadProfile(ctx, operation)
	if err != nil {
		return nil, err
	}
	if err := w.bindBackupStepInput(ctx, operation, step, profile, resolved); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("encode resolved %s input: %w", step.StepID, err)
	}
	return encoded, nil
}

func (w *ServerBackupWorkflow) bindBackupStepInput(ctx context.Context, operation store.OperationRecord, step store.OperationStep, profile ServerBackupProfile, resolved map[string]any) error {
	switch step.CapabilityName {
	case "game.backup.capture":
		return w.bindBackupCaptureInput(ctx, operation, step.StepID, resolved)
	case "storage.object.put":
		return w.bindBackupUploadInput(ctx, operation, step.StepID, resolved)
	case "storage.object.get":
		return w.bindBackupReadbackInput(ctx, operation, step.StepID, resolved)
	case "game.backup.validate":
		return resolveBackupValidationInput(operation, profile, resolved)
	default:
		return nil
	}
}

func (w *ServerBackupWorkflow) bindBackupCaptureInput(ctx context.Context, operation store.OperationRecord, stepID string, resolved map[string]any) error {
	dataSet, ok := w.dataSetForStep(ctx, operation, stepID, "capture")
	if !ok {
		return errors.New("capture step is not part of the trusted backup plan")
	}
	prepare, err := backupPrepareOutput(operation)
	if err != nil || !backupPreparationMatchesCapture(prepare, resolved, operation) {
		return errors.New("capture input requires an exact completed backup preparation")
	}
	if !containsString(prepare.DataSetIDs, dataSet.DataSetID) {
		return errors.New("prepared backup does not include the planned data set")
	}
	resolved["preparation_id"] = prepare.PreparationID
	return nil
}

func (w *ServerBackupWorkflow) bindBackupUploadInput(ctx context.Context, operation store.OperationRecord, stepID string, resolved map[string]any) error {
	dataSet, ok := w.dataSetForStep(ctx, operation, stepID, "upload")
	if !ok {
		return errors.New("upload step is not part of the trusted backup plan")
	}
	capture, err := backupCaptureOutput(operation, dataSet.DataSetID)
	if err != nil || !backupCaptureIsBounded(capture, dataSet) || capture.SizeBytes < 0 {
		return errors.New("storage upload input requires a complete bounded capture")
	}
	resolved["expected_size_bytes"] = capture.SizeBytes
	resolved["expected_checksum"] = map[string]any{"algorithm": capture.Checksum.Algorithm, "digest": capture.Checksum.Digest}
	return nil
}

func (w *ServerBackupWorkflow) bindBackupReadbackInput(ctx context.Context, operation store.OperationRecord, stepID string, resolved map[string]any) error {
	dataSet, ok := w.dataSetForStep(ctx, operation, stepID, "readback")
	if !ok {
		return errors.New("readback step is not part of the trusted backup plan")
	}
	capture, err := backupCaptureOutput(operation, dataSet.DataSetID)
	if err != nil || capture.Outcome != "captured" {
		return errors.New("storage readback input requires a completed capture")
	}
	put, err := backupPutOutput(operation, dataSet.DataSetID)
	if err != nil || put.UploadState != "uploaded" || !backupObjectRefMatchesTarget(put.ObjectRef, put.Target) {
		return errors.New("storage readback input requires the exact uploaded object")
	}
	resolved["object_ref"] = put.ObjectRef
	resolved["expected_size_bytes"] = capture.SizeBytes
	resolved["expected_checksum"] = map[string]any{"algorithm": capture.Checksum.Algorithm, "digest": capture.Checksum.Digest}
	return nil
}

func backupPreparationMatchesCapture(prepare backupPreparationObservation, input map[string]any, operation store.OperationRecord) bool {
	if prepare.Outcome != "prepared" || prepare.PreparationID == "" {
		return false
	}
	return prepare.BackupID == stringValueMap(input, "backup_id") &&
		prepare.DataGenerationID == stringValueMap(input, "data_generation_id") &&
		sameStringSlice(prepare.DataSetIDs, backupPlannedDataSetIDs(operation))
}

func backupCaptureIsBounded(capture backupCaptureObservation, dataSet ServerBackupDataSet) bool {
	return capture.Outcome == "captured" && capture.StreamCompleted && capture.SizeBytes <= dataSet.MaximumBytes
}

func resolveBackupValidationInput(operation store.OperationRecord, profile ServerBackupProfile, input map[string]any) error {
	prepare, err := backupPrepareOutput(operation)
	if err != nil || prepare.Outcome != "prepared" || prepare.ConsistencyEvidence == "" {
		return errors.New("backup validation requires a completed, evidence-bearing preparation")
	}
	manifest := asStringMap(input["manifest"])
	consistency := asStringMap(manifest["consistency"])
	consistency["evidence"] = prepare.ConsistencyEvidence
	manifestObjects := asAnySlice(manifest["objects"])
	streamInputs := asAnySlice(input["object_streams"])
	if len(manifestObjects) != len(profile.DataSets) || len(streamInputs) != len(profile.DataSets) {
		return errors.New("planned manifest and stream lists do not cover the trusted data sets")
	}
	for index, dataSet := range profile.DataSets {
		err := bindBackupValidationObject(operation, dataSet, index, asStringMap(manifestObjects[index]), asStringMap(streamInputs[index]))
		if err != nil {
			return err
		}
	}
	input["manifest"] = manifest
	input["object_streams"] = streamInputs
	return nil
}

func bindBackupValidationObject(operation store.OperationRecord, dataSet ServerBackupDataSet, index int, object, stream map[string]any) error {
	capture, err := backupCaptureOutput(operation, dataSet.DataSetID)
	if err != nil || !backupCaptureIsBounded(capture, dataSet) {
		return fmt.Errorf("data set %q has no complete, bounded capture output", dataSet.DataSetID)
	}
	put, err := backupPutOutput(operation, dataSet.DataSetID)
	if err != nil || !backupUploadMatchesCapture(put, capture) {
		return fmt.Errorf("data set %q has no exact completed upload", dataSet.DataSetID)
	}
	get, err := backupGetOutput(operation, dataSet.DataSetID)
	if err != nil || !backupReadbackMatchesCapture(get, put, capture) {
		return fmt.Errorf("data set %q has no exact completed storage readback", dataSet.DataSetID)
	}
	object["size_bytes"] = capture.SizeBytes
	object["checksum_algorithm"] = capture.Checksum.Algorithm
	object["checksum"] = capture.Checksum.Digest
	object["object_ref"] = put.ObjectRef
	if stringValueMap(object, "data_set_id") != dataSet.DataSetID {
		return fmt.Errorf("manifest object %d is bound to the wrong data set", index)
	}
	stream["object_ref"] = put.ObjectRef
	return nil
}
