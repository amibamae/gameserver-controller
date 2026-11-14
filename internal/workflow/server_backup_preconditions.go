package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerBackupWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || w == nil || operation.OperationType != "server.backup" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.backup operation and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	evidence, err := w.backupPreconditionEvidence(ctx, operation, step)
	if err != nil {
		return err
	}
	if check, ok := backupFixedStepChecks[step.StepID]; ok {
		return check(w, ctx, evidence)
	}
	if step.StepID == "" {
		return errors.New("backup workflow step is missing an identifier")
	}
	return w.checkBackupLaterStep(ctx, evidence)
}

// backupPreconditionEvidence is what CheckPreconditions gathers once, before
// any step-specific rule, from the stored server, the trusted profile and the
// outputs of the inspection steps.
type backupPreconditionEvidence struct {
	operation       store.OperationRecord
	step            store.OperationStep
	server          store.LogicalServer
	profile         ServerBackupProfile
	unitRef         json.RawMessage
	initialState    protocolstate.ServerState
	initiallyActive bool
	unit            stopUnitInspection
	unitErr         error
	providerActive  bool
	runtimeExpected bool
	dataDescribed   bool
}

// backupStepCheck is one step's precondition rule.
type backupStepCheck func(*ServerBackupWorkflow, context.Context, *backupPreconditionEvidence) error

// backupFixedStepChecks holds the rules for the steps checked before the
// per-data-set steps are looked up.
var backupFixedStepChecks = map[string]backupStepCheck{
	backupInspectInstanceStep: (*ServerBackupWorkflow).checkBackupInspectInstance,
	backupInspectRuntimeStep:  (*ServerBackupWorkflow).checkBackupInspectRuntime,
	backupDescribeDataStep:    (*ServerBackupWorkflow).checkBackupDescribeData,
	backupSaveGameStep:        (*ServerBackupWorkflow).checkBackupSaveGame,
	backupStopRuntimeStep:     (*ServerBackupWorkflow).checkBackupStopRuntime,
	backupPrepareStep:         (*ServerBackupWorkflow).checkBackupPrepare,
}

// backupLateStepChecks holds the rules for the fixed steps checked after the
// per-data-set steps are looked up.
var backupLateStepChecks = map[string]backupStepCheck{
	backupValidateStep:      (*ServerBackupWorkflow).checkBackupValidate,
	backupStartRuntimeStep:  (*ServerBackupWorkflow).checkBackupStartRuntime,
	backupVerifyRuntimeStep: (*ServerBackupWorkflow).checkBackupVerifyRuntime,
	backupReadinessStep:     (*ServerBackupWorkflow).checkBackupReadiness,
	backupVerifyStoppedStep: (*ServerBackupWorkflow).checkBackupVerifyStopped,
}

func (w *ServerBackupWorkflow) backupPreconditionEvidence(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (*backupPreconditionEvidence, error) {
	stored, profile, err := w.loadProfile(ctx, operation)
	if err != nil {
		return nil, err
	}
	server := stored.Record
	if server.NetworkProfile != "" {
		return nil, errors.New("server.backup is blocked until configured proxy drain is integrated")
	}
	instanceRef := stepField(operationStep(operation, backupInspectInstanceStep).Input, "instance_ref")
	if !backupInstanceRefBound(operation, server, instanceRef) {
		return nil, store.ErrLogicalServerTargetMismatch
	}
	evidence := &backupPreconditionEvidence{
		operation:    operation,
		step:         step,
		server:       server,
		profile:      profile,
		unitRef:      profile.UnitRef,
		initialState: backupInitialLifecycle(operation),
	}
	evidence.initiallyActive = evidence.initialState == protocolstate.ServerReady || evidence.initialState == protocolstate.ServerDegraded
	provider, providerErr := backupProviderInspection(operation)
	evidence.unit, evidence.unitErr = backupRuntimeInspection(operation, backupInspectRuntimeStep)
	evidence.providerActive = providerErr == nil && equalJSON(provider.InstanceRef, instanceRef) && provider.State == "active"
	evidence.runtimeExpected = evidence.unitErr == nil && equalJSON(evidence.unit.UnitRef, evidence.unitRef) &&
		backupRuntimeStateExpected(evidence.initiallyActive, evidence.unit.State)
	evidence.dataDescribed = backupDataDescriptionMatches(operation, profile, server)
	return evidence, nil
}

// backupInstanceRefBound reports whether the planned provider inspection, and
// the operation target when it names one, bind the server's managed instance.
func backupInstanceRefBound(operation store.OperationRecord, server store.LogicalServer, instanceRef json.RawMessage) bool {
	if len(instanceRef) == 0 || !equalJSON(instanceRef, server.InstanceRef) {
		return false
	}
	return len(operation.Target.InstanceRef) == 0 || equalJSON(operation.Target.InstanceRef, server.InstanceRef)
}

// backupRuntimeStateExpected reports whether the inspected runtime state
// matches the server's original lifecycle: running when it was active,
// stopped or absent otherwise.
func backupRuntimeStateExpected(initiallyActive bool, state string) bool {
	if initiallyActive {
		return state == "running"
	}
	return state == "stopped" || state == "absent"
}

func (w *ServerBackupWorkflow) checkBackupInspectInstance(_ context.Context, e *backupPreconditionEvidence) error {
	state := e.server.LifecycleState
	if state != protocolstate.ServerReady && state != protocolstate.ServerDegraded && state != protocolstate.ServerStopped {
		return errors.New("provider inspection requires the original ready, degraded, or stopped lifecycle state")
	}
	if !stepBindsRef(e.step.Input, "instance_ref", e.server.InstanceRef) {
		return errors.New("provider inspection is not bound to the current managed instance")
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupInspectRuntime(_ context.Context, e *backupPreconditionEvidence) error {
	if !e.providerActive || e.server.LifecycleState != backupInitialLifecycle(e.operation) || !stepBindsRef(e.step.Input, "unit_ref", e.unitRef) {
		return errors.New("runtime inspection requires the exact active provider instance and original lifecycle state")
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupDescribeData(_ context.Context, e *backupPreconditionEvidence) error {
	if !e.providerActive || !e.runtimeExpected || e.server.LifecycleState != backupInitialLifecycle(e.operation) ||
		!backupDataDescribeInputMatches(e.operation, e.profile, e.server) {
		return errors.New("data-set description requires the exact instance, runtime state, and trusted release")
	}
	return nil
}

func (e *backupPreconditionEvidence) activeProviderDescribed() bool {
	return e.providerActive && e.dataDescribed
}

func (e *backupPreconditionEvidence) activeRuntimeDescribed() bool {
	return e.providerActive && e.runtimeExpected && e.dataDescribed
}

func (w *ServerBackupWorkflow) checkBackupSaveGame(_ context.Context, e *backupPreconditionEvidence) error {
	if !e.initiallyActive || e.server.LifecycleState != e.initialState || !e.activeRuntimeDescribed() {
		return errors.New("offline backup save requires the exact ready or degraded running server and declared data sets")
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupStopRuntime(_ context.Context, e *backupPreconditionEvidence) error {
	lifecycleHeld := e.server.LifecycleState == e.initialState || e.server.LifecycleState == protocolstate.ServerStopping
	if !e.initiallyActive || !lifecycleHeld || !gameBackupSaveConfirmed(e.operation) || !e.activeProviderDescribed() {
		return errors.New("runtime stop requires a confirmed game save and exact ready or degraded server evidence")
	}
	if !stepBindsRef(e.step.Input, "unit_ref", e.unitRef) {
		return errors.New("runtime stop is not bound to the trusted execution unit")
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupPrepare(_ context.Context, e *backupPreconditionEvidence) error {
	if e.server.LifecycleState != protocolstate.ServerStopped && e.server.LifecycleState != protocolstate.ServerBackingUp {
		return errors.New("backup preparation requires a stopped runtime")
	}
	if !e.providerActive || !e.dataDescribed || !runtimeExpectedForBackup(e.operation, e.unit, e.unitErr) {
		return errors.New("backup preparation requires exact provider, runtime, and data-set evidence")
	}
	if e.initiallyActive {
		stopped, err := backupRuntimeStopOutput(e.operation)
		if err != nil || stopped.State != "stopped" || !equalJSON(stopped.UnitRef, e.unitRef) {
			return errors.New("backup preparation requires the exact runtime to be confirmed stopped")
		}
	}
	return nil
}

// checkBackupLaterStep checks a per-data-set capture, upload or readback step,
// then the fixed steps after them, in that order.
func (w *ServerBackupWorkflow) checkBackupLaterStep(ctx context.Context, e *backupPreconditionEvidence) error {
	stepID := e.step.StepID
	if dataSet, ok := w.dataSetForStep(ctx, e.operation, stepID, "capture"); ok {
		return checkBackupCapture(e, dataSet)
	}
	if dataSet, ok := w.dataSetForStep(ctx, e.operation, stepID, "upload"); ok {
		return w.checkBackupUpload(ctx, e, dataSet)
	}
	if dataSet, ok := w.dataSetForStep(ctx, e.operation, stepID, "readback"); ok {
		return w.checkBackupReadback(ctx, e, dataSet)
	}
	if check, ok := backupLateStepChecks[stepID]; ok {
		return check(w, ctx, e)
	}
	return fmt.Errorf("unsupported server.backup step %q", stepID)
}

func checkBackupCapture(e *backupPreconditionEvidence, dataSet ServerBackupDataSet) error {
	if e.server.LifecycleState != protocolstate.ServerBackingUp || !e.dataDescribed {
		return errors.New("capture requires backing_up state and exact trusted data-set evidence")
	}
	prepared, err := backupPrepareOutput(e.operation)
	if err != nil || !containsString(prepared.DataSetIDs, dataSet.DataSetID) {
		return errors.New("capture requires an exact completed preparation for the data set")
	}
	captureInput := decodeObject(effectiveStepInput(e.step))
	if stringValueMap(captureInput, "preparation_id") != prepared.PreparationID ||
		stringValueMap(captureInput, "data_set_id") != dataSet.DataSetID ||
		stringValueMap(captureInput, "data_generation_id") != prepared.DataGenerationID ||
		stringValueMap(captureInput, "backup_id") != prepared.BackupID {
		return errors.New("capture input is not bound to the exact preparation")
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupUpload(ctx context.Context, e *backupPreconditionEvidence, dataSet ServerBackupDataSet) error {
	if e.server.LifecycleState != protocolstate.ServerBackingUp {
		return errors.New("storage upload requires the server to remain stopped during backup")
	}
	capture, err := backupCaptureOutput(e.operation, dataSet.DataSetID)
	if err != nil || capture.Outcome != "captured" || capture.SizeBytes > dataSet.MaximumBytes {
		return errors.New("storage upload requires a complete bounded capture")
	}
	return w.verifyStreams(ctx, e.operation, backupDataStepID("capture", dataSet.DataSetID), []ServerBackupStreamExpectation{{
		DataSetID: dataSet.DataSetID, SizeBytes: capture.SizeBytes, MaximumBytes: dataSet.MaximumBytes, SHA256: capture.Checksum.Digest,
	}})
}

func (w *ServerBackupWorkflow) checkBackupReadback(ctx context.Context, e *backupPreconditionEvidence, dataSet ServerBackupDataSet) error {
	if e.server.LifecycleState != protocolstate.ServerBackingUp {
		return errors.New("storage readback requires the server to remain stopped during backup")
	}
	capture, captureErr := backupCaptureOutput(e.operation, dataSet.DataSetID)
	put, putErr := backupPutOutput(e.operation, dataSet.DataSetID)
	if captureErr != nil || putErr != nil || !backupUploadMatchesCapture(put, capture) {
		return errors.New("storage readback requires an exact completed upload")
	}
	return w.verifyStreams(ctx, e.operation, backupDataStepID("upload", dataSet.DataSetID), []ServerBackupStreamExpectation{{
		DataSetID: dataSet.DataSetID, SizeBytes: capture.SizeBytes, MaximumBytes: maxInt64(capture.SizeBytes, 1), SHA256: capture.Checksum.Digest,
	}})
}

func (w *ServerBackupWorkflow) checkBackupValidate(ctx context.Context, e *backupPreconditionEvidence) error {
	if e.server.LifecycleState != protocolstate.ServerBackingUp || !backupReadbacksComplete(e.operation, e.profile) {
		return errors.New("game backup validation requires exact completed storage readbacks")
	}
	for _, dataSet := range e.profile.DataSets {
		capture, err := backupCaptureOutput(e.operation, dataSet.DataSetID)
		if err != nil {
			return err
		}
		if err := w.verifyStreams(ctx, e.operation, backupDataStepID("readback", dataSet.DataSetID), []ServerBackupStreamExpectation{{
			DataSetID: dataSet.DataSetID, SizeBytes: capture.SizeBytes, MaximumBytes: maxInt64(capture.SizeBytes, 1), SHA256: capture.Checksum.Digest,
		}}); err != nil {
			return err
		}
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupStartRuntime(ctx context.Context, e *backupPreconditionEvidence) error {
	if !backupResumeAfterBackup(e.operation) || e.server.LifecycleState != protocolstate.ServerBackingUp || !backupValidationConfirmed(e.operation, e.profile) {
		return errors.New("runtime resume requires a fully validated backup of the exact data generation")
	}
	return w.verifyBackupValidationStreams(ctx, e.operation, e.profile)
}

func (w *ServerBackupWorkflow) checkBackupVerifyRuntime(_ context.Context, e *backupPreconditionEvidence) error {
	if e.server.LifecycleState != protocolstate.ServerStarting || !backupRuntimeStartCompleted(e.operation) || !stepBindsRef(e.step.Input, "unit_ref", e.unitRef) {
		return errors.New("runtime verification requires the exact completed start command")
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupReadiness(_ context.Context, e *backupPreconditionEvidence) error {
	if e.server.LifecycleState != protocolstate.ServerStarting || !backupRuntimeRunning(e.operation) || !stepBindsRef(e.step.Input, "instance_ref", e.server.InstanceRef) {
		return errors.New("readiness checks require the exact running runtime and managed instance")
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupVerifyStopped(_ context.Context, e *backupPreconditionEvidence) error {
	if e.server.LifecycleState != protocolstate.ServerBackingUp || backupResumeAfterBackup(e.operation) ||
		!backupValidationConfirmed(e.operation, e.profile) || !stepBindsRef(e.step.Input, "unit_ref", e.unitRef) {
		return errors.New("final stopped-state inspection requires the validated backup and exact execution unit")
	}
	return nil
}
