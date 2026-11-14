package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerBackupWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.backup" {
		return errors.New("server.backup completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stored, profile, err := w.loadProfile(ctx, operation)
	if err != nil {
		return err
	}
	if !backupDataDescriptionMatches(operation, profile, stored.Record) {
		return errors.New("game data description does not match the trusted release profile")
	}
	if _, err := backupPrepareOutput(operation); err != nil {
		return err
	}
	for _, dataSet := range profile.DataSets {
		if err := w.verifyCompletedBackupDataSet(ctx, operation, dataSet); err != nil {
			return err
		}
	}
	if !backupValidationConfirmed(operation, profile) {
		return errors.New("game-specific backup validation did not pass the complete trusted check set")
	}
	if err := w.verifyBackupValidationStreams(ctx, operation, profile); err != nil {
		return err
	}
	if backupResumeAfterBackup(operation) {
		return w.validateBackupResumeCompletion(operation)
	}
	return validateBackupStoppedCompletion(operation)
}

func (w *ServerBackupWorkflow) verifyCompletedBackupDataSet(ctx context.Context, operation store.OperationRecord, dataSet ServerBackupDataSet) error {
	capture, err := backupCaptureOutput(operation, dataSet.DataSetID)
	if err != nil || capture.SizeBytes > dataSet.MaximumBytes {
		return fmt.Errorf("capture for data set %q is missing, invalid, or oversized", dataSet.DataSetID)
	}
	expectations := []ServerBackupStreamExpectation{{
		DataSetID: dataSet.DataSetID, SizeBytes: capture.SizeBytes, MaximumBytes: dataSet.MaximumBytes, SHA256: capture.Checksum.Digest,
	}}
	if err := w.verifyStreams(ctx, operation, backupDataStepID("capture", dataSet.DataSetID), expectations); err != nil {
		return err
	}
	put, err := backupPutOutput(operation, dataSet.DataSetID)
	if err != nil || !backupUploadMatchesCapture(put, capture) {
		return fmt.Errorf("upload for data set %q does not bind the exact capture and target", dataSet.DataSetID)
	}
	transferExpectation := ServerBackupStreamExpectation{
		DataSetID: dataSet.DataSetID, SizeBytes: capture.SizeBytes, MaximumBytes: maxInt64(capture.SizeBytes, 1), SHA256: capture.Checksum.Digest,
	}
	if err := w.verifyStreams(ctx, operation, backupDataStepID("upload", dataSet.DataSetID), []ServerBackupStreamExpectation{transferExpectation}); err != nil {
		return err
	}
	get, err := backupGetOutput(operation, dataSet.DataSetID)
	if err != nil || !backupReadbackMatchesCapture(get, put, capture) {
		return fmt.Errorf("storage readback for data set %q does not match the captured bytes", dataSet.DataSetID)
	}
	return w.verifyStreams(ctx, operation, backupDataStepID("readback", dataSet.DataSetID), []ServerBackupStreamExpectation{transferExpectation})
}

func backupUploadMatchesCapture(put backupPutObservation, capture backupCaptureObservation) bool {
	return put.UploadState == "uploaded" && put.BytesReceived == capture.SizeBytes &&
		backupObjectRefMatchesTarget(put.ObjectRef, put.Target)
}

func backupReadbackMatchesCapture(get backupGetObservation, put backupPutObservation, capture backupCaptureObservation) bool {
	if get.TransferState != "complete" || get.BytesSent == nil || *get.BytesSent != capture.SizeBytes {
		return false
	}
	return equalJSON(get.ObjectRef, put.ObjectRef) && get.ComputedChecksum.Algorithm == capture.Checksum.Algorithm &&
		get.ComputedChecksum.Digest == capture.Checksum.Digest
}

func (w *ServerBackupWorkflow) validateBackupResumeCompletion(operation store.OperationRecord) error {
	if !backupRuntimeStartCompleted(operation) || !backupRuntimeRunning(operation) {
		return errors.New("resumed runtime is not confirmed running at the exact unit")
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	if _, err := BuildReadinessAssertion(w.schemas, operation, server.Record, backupReadinessStep, w.readinessTTL); err != nil {
		return err
	}
	return nil
}

func validateBackupStoppedCompletion(operation store.OperationRecord) error {
	if backupInitiallyActive(operation) {
		if _, err := backupRuntimeStopOutput(operation); err != nil {
			return err
		}
	}
	if !backupFinalRuntimeStopped(operation) {
		return errors.New("final exact-unit inspection did not confirm the runtime stopped")
	}
	return nil
}
