package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
	"time"
)

type serverBackupParameters struct {
	BackupProfile       string          `json:"backup_profile"`
	ResumeAfterBackup   *bool           `json:"resume_after_backup,omitempty"`
	ExpectedInstanceRef json.RawMessage `json:"expected_instance_ref,omitempty"`
}

func (w *ServerBackupWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	parameters, err := w.backupRequest(ctx, operation)
	if err != nil {
		return nil, err
	}
	server, err := w.backupServer(operation, parameters)
	if err != nil {
		return nil, err
	}
	profile, err := w.profiles.ResolveServerBackup(ctx, server, parameters.BackupProfile)
	if err != nil {
		return nil, fmt.Errorf("resolve trusted server backup profile: %w", err)
	}
	if err := validateServerBackupProfile(server, parameters.BackupProfile, profile); err != nil {
		return nil, err
	}
	initiallyActive, shouldResume := backupResumePlan(server.LifecycleState, parameters.ResumeAfterBackup)
	if err := w.checkBackupGameDeclarations(server, profile, shouldResume); err != nil {
		return nil, err
	}
	plan := &backupPlanner{workflow: w, version: w.schemas.ProtocolVersion(), operation: operation, server: server, profile: profile,
		steps: make([]store.OperationStep, 0, 8+len(profile.DataSets)*3)}
	for _, group := range []func() error{
		plan.inspectionSteps,
		func() error { return plan.quiesceSteps(initiallyActive) },
		plan.prepareStep,
		plan.dataSetSteps,
		plan.validateStep,
		func() error { return plan.finishSteps(shouldResume) },
	} {
		if err := group(); err != nil {
			return nil, err
		}
	}
	return plan.steps, nil
}

// backupRequest checks the workflow, context and request, and decodes the
// request parameters.
func (w *ServerBackupWorkflow) backupRequest(ctx context.Context, operation store.OperationRecord) (serverBackupParameters, error) {
	if ctx == nil || !w.initialized() {
		return serverBackupParameters{}, errors.New("initialized backup workflow and context are required")
	}
	if err := ctx.Err(); err != nil {
		return serverBackupParameters{}, err
	}
	if !isServerBackupRequest(operation, w.schemas.ProtocolVersion()) {
		return serverBackupParameters{}, errors.New("server.backup operation and loaded protocol version are required")
	}
	var parameters serverBackupParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil || parameters.BackupProfile == "" {
		return serverBackupParameters{}, errors.New("decode server.backup parameters")
	}
	return parameters, nil
}

// initialized reports whether the workflow has every dependency it uses.
func (w *ServerBackupWorkflow) initialized() bool {
	if w == nil || w.servers == nil || w.adapters == nil {
		return false
	}
	return w.schemas != nil && w.profiles != nil && w.transfers != nil
}

// isServerBackupRequest reports whether operation is a server.backup request
// for the loaded protocol version with a target and object parameters.
func isServerBackupRequest(operation store.OperationRecord, protocolVersion string) bool {
	if operation.OperationType != "server.backup" || operation.ProtocolVersion != protocolVersion ||
		operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" {
		return false
	}
	return operation.RequestSnapshot.OperationType == "server.backup" && isObject(operation.RequestSnapshot.Parameters)
}

// backupServer loads the target server and checks that it can be backed up
// and is the instance the request names.
func (w *ServerBackupWorkflow) backupServer(operation store.OperationRecord, parameters serverBackupParameters) (store.LogicalServer, error) {
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.LogicalServer{}, fmt.Errorf("load logical server for backup plan: %w", err)
	}
	server := stored.Record
	switch server.LifecycleState {
	case protocolstate.ServerReady, protocolstate.ServerDegraded, protocolstate.ServerStopped:
	default:
		return store.LogicalServer{}, errors.New("server.backup requires a ready, degraded, or stopped logical server")
	}
	if server.NetworkProfile != "" {
		return store.LogicalServer{}, errors.New("server.backup cannot stop a network-routed server until proxy drain is integrated")
	}
	if server.DataGenerationStatus != "known" || server.DataGenerationID == "" || len(server.InstanceRef) == 0 {
		return store.LogicalServer{}, errors.New("server.backup requires an exact managed instance and known data generation")
	}
	if len(operation.Target.InstanceRef) > 0 && !equalJSON(operation.Target.InstanceRef, server.InstanceRef) {
		return store.LogicalServer{}, store.ErrLogicalServerTargetMismatch
	}
	if len(parameters.ExpectedInstanceRef) > 0 && !equalJSON(parameters.ExpectedInstanceRef, server.InstanceRef) {
		return store.LogicalServer{}, errors.New("expected_instance_ref does not match the current managed instance")
	}
	return server, nil
}

// backupResumePlan reports whether the server is running before the backup
// (so it must be saved and stopped) and whether to start it afterwards: a
// ready server resumes unless the request says not to, a degraded one only
// when the request asks, and a stopped one never.
func backupResumePlan(state protocolstate.ServerState, resumeAfterBackup *bool) (initiallyActive, shouldResume bool) {
	initiallyReady := state == protocolstate.ServerReady
	initiallyDegraded := state == protocolstate.ServerDegraded
	resume := resumeAfterBackup == nil || *resumeAfterBackup
	explicitResume := resumeAfterBackup != nil && *resumeAfterBackup
	return initiallyReady || initiallyDegraded, (initiallyReady && resume) || (initiallyDegraded && explicitResume)
}

// checkBackupGameDeclarations checks that the game adapter declares the
// profile's data sets and, when the server will resume, its readiness set.
func (w *ServerBackupWorkflow) checkBackupGameDeclarations(server store.LogicalServer, profile ServerBackupProfile, shouldResume bool) error {
	gameDescriptor, err := w.adapters.Descriptor(server.GameAdapterID)
	if err != nil || gameDescriptor.AdapterID != server.GameAdapterID || gameDescriptor.Category != adapter.CategoryGame {
		return errors.New("configured game adapter is unavailable or has the wrong category")
	}
	if err := verifyBackupDataSetsDeclared(gameDescriptor, profile); err != nil {
		return err
	}
	if !shouldResume {
		return nil
	}
	return verifyReadinessSetDeclared(gameDescriptor, server.EnvironmentID, ServerStartProfile{
		GameReleaseID: profile.GameReleaseID, ReadinessCheckSetID: profile.ReadinessCheckSetID,
		RequiredChecks: profile.RequiredReadinessChecks,
	})
}

// backupPlanner appends the backup's steps in order.
type backupPlanner struct {
	workflow  *ServerBackupWorkflow
	version   string
	operation store.OperationRecord
	server    store.LogicalServer
	profile   ServerBackupProfile
	steps     []store.OperationStep
}

// backupCapabilityRef names one adapter capability and its schemas.
type backupCapabilityRef struct {
	category            adapter.Category
	name, input, output string
	mutating            bool
}

var (
	backupInstanceInspect = backupCapabilityRef{adapter.CategoryHosting, "hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false}
	backupUnitInspect     = backupCapabilityRef{adapter.CategoryExecution, "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false}
	backupUnitStop        = backupCapabilityRef{adapter.CategoryExecution, "execution.unit.graceful_stop", "execution:systemd-unit-graceful-stop-input", "execution:systemd-unit-graceful-stop-output", true}
	backupUnitStart       = backupCapabilityRef{adapter.CategoryExecution, "execution.unit.start", "execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true}
	backupDataDescribe    = backupCapabilityRef{adapter.CategoryGame, "game.data.describe", "game:data-describe-input", "game:data-describe-output", false}
	backupSaveRequest     = backupCapabilityRef{adapter.CategoryGame, "game.save.request", "game:save-request-input", "game:save-request-output", true}
	backupPrepare         = backupCapabilityRef{adapter.CategoryGame, "game.backup.prepare", "game:backup-prepare-input", "game:backup-prepare-output", true}
	backupCapture         = backupCapabilityRef{adapter.CategoryGame, "game.backup.capture", "game:backup-capture-input", "game:backup-capture-output", true}
	backupValidate        = backupCapabilityRef{adapter.CategoryGame, "game.backup.validate", "game:backup-validate-input", "game:backup-validate-output", false}
	backupReadinessCheck  = backupCapabilityRef{adapter.CategoryGame, "game.readiness.check", "game:readiness-check-input", "game:readiness-check-output", false}
	backupObjectPut       = backupCapabilityRef{adapter.CategoryStorage, "storage.object.put", "storage:object-put-input", "storage:object-put-output", true}
	backupObjectGet       = backupCapabilityRef{adapter.CategoryStorage, "storage.object.get", "storage:object-get-input", "storage:object-get-output", false}
)

// add appends a step whose input is checked against its capability's input
// schema now.
func (p *backupPlanner) add(stepID, adapterID string, capability backupCapabilityRef, input any) error {
	return p.appendStep(stepID, adapterID, capability, input, false)
}

// addDynamic appends a step whose input is completed from earlier outputs
// at dispatch time, so it is not checked against the schema now.
func (p *backupPlanner) addDynamic(stepID, adapterID string, capability backupCapabilityRef, input any) error {
	return p.appendStep(stepID, adapterID, capability, input, true)
}

func (p *backupPlanner) appendStep(stepID, adapterID string, ref backupCapabilityRef, input any, dynamic bool) error {
	capability, err := exactCapability(p.workflow.adapters, adapterID, ref.category, ref.name,
		"urn:gameserver-controller:"+p.version+":"+ref.input,
		"urn:gameserver-controller:"+p.version+":"+ref.output, ref.mutating)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode %s input: %w", stepID, err)
	}
	if !dynamic {
		if err := p.workflow.schemas.ValidateJSON(capability.InputSchemaURI, encoded); err != nil {
			return fmt.Errorf("planned %s input does not match its declared capability schema: %w", stepID, err)
		}
	}
	p.steps = append(p.steps, store.OperationStep{
		StepID: stepID, StepType: capability.Name, Status: protocolstate.StepPending,
		AdapterID: adapterID, CapabilityName: capability.Name, CapabilityVersion: capability.Version,
		Input: encoded,
	})
	return nil
}

func (p *backupPlanner) gameTarget() map[string]any {
	return map[string]any{"server_id": p.server.ServerID, "environment_id": p.server.EnvironmentID, "instance_ref": p.server.InstanceRef}
}

func (p *backupPlanner) unitInput() map[string]any {
	return map[string]any{"unit_ref": p.profile.UnitRef}
}

// objectTarget is the storage object that holds one data set's capture.
func (p *backupPlanner) objectTarget(dataSetID string) map[string]any {
	return map[string]any{
		"adapter_id": p.profile.StorageAdapterID, "storage_profile_id": p.profile.StorageProfileID,
		"environment_id": p.server.EnvironmentID, "container": p.profile.StorageContainer,
		"key": backupObjectKey(p.server.ServerID, p.operation.OperationID, dataSetID),
	}
}

// inspectionSteps observe the instance, the unit and the game's data first.
func (p *backupPlanner) inspectionSteps() error {
	if err := p.add(backupInspectInstanceStep, adapterIDFromRef(p.server.InstanceRef), backupInstanceInspect,
		map[string]any{"instance_ref": p.server.InstanceRef}); err != nil {
		return err
	}
	if err := p.add(backupInspectRuntimeStep, p.server.ExecutionAdapterID, backupUnitInspect, p.unitInput()); err != nil {
		return err
	}
	return p.add(backupDescribeDataStep, p.server.GameAdapterID, backupDataDescribe,
		map[string]any{"target": p.gameTarget(), "release_id": p.profile.GameReleaseID})
}

// quiesceSteps save and stop a running server before its data is captured.
func (p *backupPlanner) quiesceSteps(initiallyActive bool) error {
	if !initiallyActive {
		return nil
	}
	if err := p.add(backupSaveGameStep, p.server.GameAdapterID, backupSaveRequest,
		map[string]any{"target": p.gameTarget(), "release_id": p.profile.GameReleaseID, "reason": "backup"}); err != nil {
		return err
	}
	return p.add(backupStopRuntimeStep, p.server.ExecutionAdapterID, backupUnitStop,
		map[string]any{"unit_ref": p.profile.UnitRef, "grace_period_seconds": backupGracePeriodSeconds})
}

func (p *backupPlanner) prepareStep() error {
	dataSetIDs := make([]string, 0, len(p.profile.DataSets))
	for _, dataSet := range p.profile.DataSets {
		dataSetIDs = append(dataSetIDs, dataSet.DataSetID)
	}
	return p.add(backupPrepareStep, p.server.GameAdapterID, backupPrepare, map[string]any{
		"target": p.gameTarget(), "release_id": p.profile.GameReleaseID,
		"backup_id": p.operation.OperationID, "data_generation_id": p.server.DataGenerationID,
		"data_set_ids": dataSetIDs, "consistency_mode": p.profile.ConsistencyMode,
	})
}

// dataSetSteps capture, upload and read back each data set in order.
func (p *backupPlanner) dataSetSteps() error {
	for _, dataSet := range p.profile.DataSets {
		objectTarget := p.objectTarget(dataSet.DataSetID)
		captureInput := map[string]any{
			"target": p.gameTarget(), "release_id": p.profile.GameReleaseID,
			"backup_id": p.operation.OperationID, "data_generation_id": p.server.DataGenerationID,
			"data_set_id": dataSet.DataSetID, "capture_id": backupCaptureID(p.operation.OperationID, dataSet.DataSetID),
			"consistency_mode": p.profile.ConsistencyMode,
		}
		if err := p.addDynamic(backupDataStepID("capture", dataSet.DataSetID), p.server.GameAdapterID, backupCapture, captureInput); err != nil {
			return err
		}
		if err := p.addDynamic(backupDataStepID("upload", dataSet.DataSetID), p.profile.StorageAdapterID, backupObjectPut,
			map[string]any{"target": objectTarget}); err != nil {
			return err
		}
		if err := p.addDynamic(backupDataStepID("readback", dataSet.DataSetID), p.profile.StorageAdapterID, backupObjectGet,
			map[string]any{"object_ref": cloneStringAnyMap(objectTarget)}); err != nil {
			return err
		}
	}
	return nil
}

// validateStep has the game adapter validate the manifest and every object.
func (p *backupPlanner) validateStep() error {
	manifestObjects := make([]any, 0, len(p.profile.DataSets))
	objectStreams := make([]any, 0, len(p.profile.DataSets))
	for _, dataSet := range p.profile.DataSets {
		objectRef := cloneStringAnyMap(p.objectTarget(dataSet.DataSetID))
		manifestObjects = append(manifestObjects, map[string]any{"data_set_id": dataSet.DataSetID, "object_ref": objectRef})
		objectStreams = append(objectStreams, map[string]any{"object_ref": cloneStringAnyMap(objectRef)})
	}
	manifest := map[string]any{
		"manifest_version": backupManifestVersion, "backup_id": p.operation.OperationID,
		"server_id": p.server.ServerID, "environment_id": p.server.EnvironmentID,
		"data_generation_id": p.server.DataGenerationID, "source_instance_ref": p.server.InstanceRef,
		"created_at": p.operation.CreatedAt.UTC().Format(time.RFC3339Nano), "source_operation_id": p.operation.OperationID,
		"game": map[string]any{
			"adapter_id": p.server.GameAdapterID, "name": p.profile.GameName,
			"version": p.profile.GameVersion, "world_format": p.profile.WorldFormat,
		},
		"consistency": map[string]any{"mode": p.profile.ConsistencyMode},
		"objects":     manifestObjects,
	}
	checkIDs := make([]string, 0, len(p.profile.ValidationChecks))
	for _, requirement := range p.profile.ValidationChecks {
		checkIDs = append(checkIDs, requirement.CheckID)
	}
	return p.addDynamic(backupValidateStep, p.server.GameAdapterID, backupValidate, map[string]any{
		"backup_id": p.operation.OperationID, "environment_id": p.server.EnvironmentID,
		"manifest": manifest, "object_streams": objectStreams, "required_check_ids": checkIDs,
	})
}

// finishSteps either restart the server and confirm readiness, or confirm
// it is left stopped.
func (p *backupPlanner) finishSteps(shouldResume bool) error {
	if !shouldResume {
		return p.add(backupVerifyStoppedStep, p.server.ExecutionAdapterID, backupUnitInspect, p.unitInput())
	}
	if err := p.add(backupStartRuntimeStep, p.server.ExecutionAdapterID, backupUnitStart, p.unitInput()); err != nil {
		return err
	}
	if err := p.add(backupVerifyRuntimeStep, p.server.ExecutionAdapterID, backupUnitInspect, p.unitInput()); err != nil {
		return err
	}
	return p.add(backupReadinessStep, p.server.GameAdapterID, backupReadinessCheck, map[string]any{
		"server_id": p.server.ServerID, "environment_id": p.server.EnvironmentID,
		"instance_ref": p.server.InstanceRef, "game_release_id": p.profile.GameReleaseID,
		"readiness_check_set_id": p.profile.ReadinessCheckSetID,
		"required_checks":        p.profile.RequiredReadinessChecks,
	})
}
