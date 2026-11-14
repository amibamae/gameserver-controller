package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	backupInspectInstanceStep = "inspect-instance"
	backupInspectRuntimeStep  = "inspect-runtime"
	backupDescribeDataStep    = "describe-data"
	backupSaveGameStep        = "save-game-data"
	backupStopRuntimeStep     = "stop-runtime"
	backupPrepareStep         = "prepare-backup"
	backupValidateStep        = "validate-backup"
	backupStartRuntimeStep    = "start-runtime"
	backupVerifyRuntimeStep   = "verify-runtime"
	backupVerifyStoppedStep   = "verify-stopped"
	backupReadinessStep       = "check-readiness"
	backupManifestVersion     = "2"
	backupGracePeriodSeconds  = int64(120)
)

// ServerBackupDataSet is the trusted release-catalog declaration for one
// persistent data set. The transfer limit is controller policy, not adapter
// output, and is used to bound the capture stream before its size is known.
type ServerBackupDataSet struct {
	DataSetID                 string   `json:"data_set_id"`
	RelativePaths             []string `json:"relative_paths"`
	Required                  bool     `json:"required"`
	SupportedConsistencyModes []string `json:"supported_consistency_modes"`
	DataFormat                string   `json:"data_format,omitempty"`
	MaximumBytes              int64    `json:"maximum_bytes"`
}

// ServerBackupProfile contains immutable trusted release and storage metadata.
// The resolver must return the same profile for a profile ID throughout an
// accepted operation's lifetime.
type ServerBackupProfile struct {
	ProfileID               string
	GameReleaseID           string
	GameName                string
	GameVersion             string
	WorldFormat             string
	ConsistencyMode         string
	DataSets                []ServerBackupDataSet
	ValidationChecks        []ReadinessRequirement
	StorageAdapterID        string
	StorageProfileID        string
	StorageContainer        string
	UnitRef                 json.RawMessage
	ReadinessCheckSetID     string
	RequiredReadinessChecks []ReadinessRequirement
}

// ServerBackupProfileResolver selects immutable release, validation, and
// storage policy using a controller-approved profile ID.
type ServerBackupProfileResolver interface {
	ResolveServerBackup(context.Context, store.LogicalServer, string) (ServerBackupProfile, error)
}

// ServerBackupTransferBroker issues the transport-bound handles and verifies
// completed streams independently. Its staging area must be operation/step/
// attempt scoped and must never persist bearer references.
type ServerBackupTransferBroker interface {
	IssueStepStreamTransfers(context.Context, store.OperationRecord, store.OperationStep, []ServerBackupStreamExpectation, string) ([]adapter.StreamTransfer, error)
	VerifyStepStreams(context.Context, store.OperationRecord, store.OperationStep, []ServerBackupStreamExpectation) error
}

type serverBackupTransferCleaner interface {
	CleanupOperation(string) error
}

type ServerBackupStreamExpectation struct {
	DataSetID    string
	SizeBytes    int64
	MaximumBytes int64
	SHA256       string
}

// ServerBackupWorkflow plans and advances an offline, exact-generation backup.
// Network-routed servers are rejected until proxy drain is part of this flow.
type ServerBackupWorkflow struct {
	servers      store.LifecycleOperationStore
	adapters     adapterRuntime
	schemas      schemaRuntime
	profiles     ServerBackupProfileResolver
	transfers    ServerBackupTransferBroker
	readinessTTL time.Duration
}

func NewServerBackupWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime, profiles ServerBackupProfileResolver, transfers ServerBackupTransferBroker, readinessTTL time.Duration) (*ServerBackupWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil || transfers == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, trusted backup-profile resolver, and stream broker are required")
	}
	if readinessTTL <= 0 || readinessTTL > maxReadinessAssertionTTL {
		return nil, errors.New("readiness assertion TTL must be in the range 1ns..15m")
	}
	return &ServerBackupWorkflow{
		servers: servers, adapters: adapters, schemas: schemas, profiles: profiles,
		transfers: transfers, readinessTTL: readinessTTL,
	}, nil
}

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
	return w != nil && w.servers != nil && w.adapters != nil && w.schemas != nil && w.profiles != nil && w.transfers != nil
}

// isServerBackupRequest reports whether operation is a server.backup request
// for the loaded protocol version with a target and object parameters.
func isServerBackupRequest(operation store.OperationRecord, protocolVersion string) bool {
	return operation.OperationType == "server.backup" && operation.ProtocolVersion == protocolVersion &&
		operation.Target.ServerID != "" && operation.Target.EnvironmentID != "" &&
		operation.RequestSnapshot.OperationType == "server.backup" && isObject(operation.RequestSnapshot.Parameters)
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

func validateServerBackupProfile(server store.LogicalServer, requestedID string, profile ServerBackupProfile) error {
	if !backupProfileReleaseBound(requestedID, profile) || !backupProfileSetsBounded(profile) || !backupProfileStorageBound(server, profile) {
		return errors.New("trusted backup profile must bind a release, offline consistency, bounded data sets/checks, and the server storage profile")
	}
	if !backupProfileUnitBound(server, profile.UnitRef) {
		return errors.New("trusted backup profile unit reference does not bind the configured execution adapter and environment")
	}
	if err := validateBackupProfileDataSets(profile); err != nil {
		return err
	}
	return validateBackupProfileChecks(profile.ValidationChecks)
}

// backupProfileReleaseBound reports whether the profile is the requested one
// and names a bounded game release, name, version and optional world format.
func backupProfileReleaseBound(requestedID string, profile ServerBackupProfile) bool {
	if profile.ProfileID != requestedID || profile.ProfileID == "" {
		return false
	}
	return backupBoundedText(profile.GameReleaseID, 500) && backupBoundedText(profile.GameName, 200) &&
		backupBoundedText(profile.GameVersion, 200) && len(profile.WorldFormat) <= 200
}

// backupProfileSetsBounded reports whether the profile selects offline
// consistency with 1-128 data sets and 1-64 validation checks.
func backupProfileSetsBounded(profile ServerBackupProfile) bool {
	return profile.ConsistencyMode == "offline" &&
		len(profile.DataSets) > 0 && len(profile.DataSets) <= 128 &&
		len(profile.ValidationChecks) > 0 && len(profile.ValidationChecks) <= 64
}

// backupProfileStorageBound reports whether the profile names a storage
// adapter, the server's storage profile and a bounded container.
func backupProfileStorageBound(server store.LogicalServer, profile ServerBackupProfile) bool {
	return profile.StorageAdapterID != "" && profile.StorageProfileID == server.StorageProfile &&
		backupBoundedText(profile.StorageContainer, 500)
}

// backupBoundedText reports whether value is non-empty and at most limit bytes.
func backupBoundedText(value string, limit int) bool {
	return value != "" && len(value) <= limit
}

// backupProfileUnitBound reports whether the profile's unit reference is a
// systemd unit of the server's execution adapter and environment.
func backupProfileUnitBound(server store.LogicalServer, raw json.RawMessage) bool {
	var unitRef struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if err := json.Unmarshal(raw, &unitRef); err != nil || !isObject(raw) {
		return false
	}
	return unitRef.AdapterID == server.ExecutionAdapterID && unitRef.EnvironmentID == server.EnvironmentID &&
		unitRef.ResourceKind == "systemd-unit" && unitRef.ExternalID != ""
}

// validateBackupProfileDataSets checks each data set in order: a complete
// declaration, a unique ID, the selected consistency mode exactly once, then
// safe unique paths.
func validateBackupProfileDataSets(profile ServerBackupProfile) error {
	seenDataSets := make(map[string]struct{}, len(profile.DataSets))
	for _, dataSet := range profile.DataSets {
		if !backupDataSetDeclarationComplete(dataSet) {
			return fmt.Errorf("trusted backup profile contains an incomplete data-set declaration %q", dataSet.DataSetID)
		}
		if _, duplicate := seenDataSets[dataSet.DataSetID]; duplicate {
			return fmt.Errorf("trusted backup profile repeats data set %q", dataSet.DataSetID)
		}
		seenDataSets[dataSet.DataSetID] = struct{}{}
		if !containsString(dataSet.SupportedConsistencyModes, profile.ConsistencyMode) || !uniqueBackupStrings(dataSet.SupportedConsistencyModes) {
			return fmt.Errorf("trusted data set %q does not declare the selected consistency mode exactly once", dataSet.DataSetID)
		}
		if err := validateBackupDataSetPaths(dataSet); err != nil {
			return err
		}
	}
	return nil
}

// backupDataSetDeclarationComplete reports whether a data set has a bounded
// ID, a size limit within JSON's safe integer range, 1-128 relative paths and
// at least one consistency mode.
func backupDataSetDeclarationComplete(dataSet ServerBackupDataSet) bool {
	return backupBoundedText(dataSet.DataSetID, 200) &&
		dataSet.MaximumBytes >= 1 && dataSet.MaximumBytes <= 9007199254740991 &&
		len(dataSet.RelativePaths) > 0 && len(dataSet.RelativePaths) <= 128 &&
		len(dataSet.SupportedConsistencyModes) > 0
}

func validateBackupDataSetPaths(dataSet ServerBackupDataSet) error {
	paths := make(map[string]struct{}, len(dataSet.RelativePaths))
	for _, relative := range dataSet.RelativePaths {
		if !validBackupRelativePath(relative) {
			return fmt.Errorf("trusted data set %q contains an unsafe relative path", dataSet.DataSetID)
		}
		if _, duplicate := paths[relative]; duplicate {
			return fmt.Errorf("trusted data set %q repeats a path", dataSet.DataSetID)
		}
		paths[relative] = struct{}{}
	}
	return nil
}

// validateBackupProfileChecks checks each validation check in order for a
// bounded ID and version, then a unique ID.
func validateBackupProfileChecks(checks []ReadinessRequirement) error {
	seenChecks := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if !backupBoundedText(check.CheckID, 160) || !backupBoundedText(check.CheckVersion, 40) {
			return errors.New("trusted backup validation checks require bounded IDs and versions")
		}
		if _, duplicate := seenChecks[check.CheckID]; duplicate {
			return fmt.Errorf("trusted backup profile repeats validation check %q", check.CheckID)
		}
		seenChecks[check.CheckID] = struct{}{}
	}
	return nil
}

func validBackupRelativePath(value string) bool {
	if value == "" || len(value) > 512 || strings.Contains(value, "\\") || path.IsAbs(value) || path.Clean(value) != value || value == "." {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func verifyBackupDataSetsDeclared(descriptor adapter.Descriptor, profile ServerBackupProfile) error {
	var described adapter.Capability
	for _, capability := range descriptor.Capabilities {
		if capability.Name == "game.data.describe" {
			described = capability
			break
		}
	}
	if described.Name == "" {
		return errors.New("game adapter does not declare game.data.describe")
	}
	encoded, err := json.Marshal(described.Constraints)
	if err != nil {
		return err
	}
	var constraints struct {
		PersistentDataSets []struct {
			GameReleaseID             string   `json:"game_release_id"`
			DataSetIDs                []string `json:"data_set_ids"`
			SupportedConsistencyModes []string `json:"supported_consistency_modes"`
		} `json:"persistent_data_sets"`
	}
	if err := json.Unmarshal(encoded, &constraints); err != nil {
		return fmt.Errorf("decode game adapter persistent-data declarations: %w", err)
	}
	expected := make([]string, 0, len(profile.DataSets))
	for _, dataSet := range profile.DataSets {
		expected = append(expected, dataSet.DataSetID)
	}
	for _, declared := range constraints.PersistentDataSets {
		if declared.GameReleaseID == profile.GameReleaseID && sameBackupStringSet(declared.DataSetIDs, expected) &&
			containsString(declared.SupportedConsistencyModes, profile.ConsistencyMode) {
			return nil
		}
	}
	return errors.New("trusted backup data sets or consistency mode do not match the game adapter release declaration")
}

func sameBackupStringSet(left, right []string) bool {
	if len(left) != len(right) || !uniqueBackupStrings(left) || !uniqueBackupStrings(right) {
		return false
	}
	set := make(map[string]struct{}, len(left))
	for _, value := range left {
		set[value] = struct{}{}
	}
	for _, value := range right {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func uniqueBackupStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func backupObjectKey(serverID, backupID, dataSetID string) string {
	return url.PathEscape(serverID) + "/" + url.PathEscape(backupID) + "/" + url.PathEscape(dataSetID)
}

func backupDataStepID(action, dataSetID string) string {
	digest := sha256.Sum256([]byte(dataSetID))
	return action + "-" + hex.EncodeToString(digest[:])
}

func backupCaptureID(operationID, dataSetID string) string {
	digest := sha256.Sum256([]byte(operationID + "\x00" + dataSetID))
	return "capture-" + hex.EncodeToString(digest[:])
}

func cloneStringAnyMap(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}

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
	switch step.CapabilityName {
	case "game.backup.capture":
		dataSet, ok := w.dataSetForStep(ctx, operation, step.StepID, "capture")
		if !ok {
			return nil, errors.New("capture step is not part of the trusted backup plan")
		}
		prepare, err := backupPrepareOutput(operation)
		if err != nil || prepare.Outcome != "prepared" || prepare.PreparationID == "" ||
			prepare.BackupID != stringValueMap(resolved, "backup_id") || prepare.DataGenerationID != stringValueMap(resolved, "data_generation_id") ||
			!sameStringSlice(prepare.DataSetIDs, backupPlannedDataSetIDs(operation)) {
			return nil, errors.New("capture input requires an exact completed backup preparation")
		}
		if !containsString(prepare.DataSetIDs, dataSet.DataSetID) {
			return nil, errors.New("prepared backup does not include the planned data set")
		}
		resolved["preparation_id"] = prepare.PreparationID
	case "storage.object.put":
		dataSet, ok := w.dataSetForStep(ctx, operation, step.StepID, "upload")
		if !ok {
			return nil, errors.New("upload step is not part of the trusted backup plan")
		}
		capture, err := backupCaptureOutput(operation, dataSet.DataSetID)
		if err != nil || capture.Outcome != "captured" || !capture.StreamCompleted || capture.SizeBytes < 0 || capture.SizeBytes > dataSet.MaximumBytes {
			return nil, errors.New("storage upload input requires a complete bounded capture")
		}
		resolved["expected_size_bytes"] = capture.SizeBytes
		resolved["expected_checksum"] = map[string]any{"algorithm": capture.Checksum.Algorithm, "digest": capture.Checksum.Digest}
	case "storage.object.get":
		dataSet, ok := w.dataSetForStep(ctx, operation, step.StepID, "readback")
		if !ok {
			return nil, errors.New("readback step is not part of the trusted backup plan")
		}
		capture, err := backupCaptureOutput(operation, dataSet.DataSetID)
		if err != nil || capture.Outcome != "captured" {
			return nil, errors.New("storage readback input requires a completed capture")
		}
		put, err := backupPutOutput(operation, dataSet.DataSetID)
		if err != nil || put.UploadState != "uploaded" || !backupObjectRefMatchesTarget(put.ObjectRef, put.Target) {
			return nil, errors.New("storage readback input requires the exact uploaded object")
		}
		resolved["object_ref"] = put.ObjectRef
		resolved["expected_size_bytes"] = capture.SizeBytes
		resolved["expected_checksum"] = map[string]any{"algorithm": capture.Checksum.Algorithm, "digest": capture.Checksum.Digest}
	case "game.backup.validate":
		if err := w.resolveBackupValidationInput(operation, step, profile, resolved); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("encode resolved %s input: %w", step.StepID, err)
	}
	return encoded, nil
}

func (w *ServerBackupWorkflow) resolveBackupValidationInput(operation store.OperationRecord, step store.OperationStep, profile ServerBackupProfile, input map[string]any) error {
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
		capture, err := backupCaptureOutput(operation, dataSet.DataSetID)
		if err != nil || capture.Outcome != "captured" || !capture.StreamCompleted || capture.SizeBytes > dataSet.MaximumBytes {
			return fmt.Errorf("data set %q has no complete, bounded capture output", dataSet.DataSetID)
		}
		put, err := backupPutOutput(operation, dataSet.DataSetID)
		if err != nil || put.UploadState != "uploaded" || !backupObjectRefMatchesTarget(put.ObjectRef, put.Target) || put.BytesReceived != capture.SizeBytes {
			return fmt.Errorf("data set %q has no exact completed upload", dataSet.DataSetID)
		}
		get, err := backupGetOutput(operation, dataSet.DataSetID)
		if err != nil || get.TransferState != "complete" || get.BytesSent == nil || *get.BytesSent != capture.SizeBytes || !equalJSON(get.ObjectRef, put.ObjectRef) ||
			get.ComputedChecksum.Algorithm != capture.Checksum.Algorithm || get.ComputedChecksum.Digest != capture.Checksum.Digest {
			return fmt.Errorf("data set %q has no exact completed storage readback", dataSet.DataSetID)
		}
		object := asStringMap(manifestObjects[index])
		object["size_bytes"] = capture.SizeBytes
		object["checksum_algorithm"] = capture.Checksum.Algorithm
		object["checksum"] = capture.Checksum.Digest
		object["object_ref"] = put.ObjectRef
		if stringValueMap(object, "data_set_id") != dataSet.DataSetID {
			return fmt.Errorf("manifest object %d is bound to the wrong data set", index)
		}
		stream := asStringMap(streamInputs[index])
		stream["object_ref"] = put.ObjectRef
	}
	input["manifest"] = manifest
	input["object_streams"] = streamInputs
	return nil
}

func (w *ServerBackupWorkflow) loadProfile(ctx context.Context, operation store.OperationRecord) (store.StoredLogicalServer, ServerBackupProfile, error) {
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, err
	}
	var parameters serverBackupParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, errors.New("decode accepted backup profile")
	}
	profile, err := w.profiles.ResolveServerBackup(ctx, stored.Record, parameters.BackupProfile)
	if err != nil {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, err
	}
	if err := validateServerBackupProfile(stored.Record, parameters.BackupProfile, profile); err != nil {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, err
	}
	if operation.RequestSnapshot.OperationType != "server.backup" || parameters.BackupProfile == "" {
		return store.StoredLogicalServer{}, ServerBackupProfile{}, errors.New("accepted operation does not bind a backup profile")
	}
	return stored, profile, nil
}

func (w *ServerBackupWorkflow) dataSetForStep(ctx context.Context, operation store.OperationRecord, stepID, action string) (ServerBackupDataSet, bool) {
	_, profile, err := w.loadProfile(ctx, operation)
	if err != nil {
		return ServerBackupDataSet{}, false
	}
	plannedIDs := backupPlannedDataSetIDs(operation)
	for _, dataSet := range profile.DataSets {
		if backupDataStepID(action, dataSet.DataSetID) == stepID && containsString(plannedIDs, dataSet.DataSetID) {
			return dataSet, true
		}
	}
	return ServerBackupDataSet{}, false
}

func backupPlannedDataSetIDs(operation store.OperationRecord) []string {
	step := operationStep(operation, backupPrepareStep)
	var input struct {
		DataSetIDs []string `json:"data_set_ids"`
	}
	if json.Unmarshal(step.Input, &input) != nil {
		return nil
	}
	return input.DataSetIDs
}

func (w *ServerBackupWorkflow) StreamTransfersForStep(ctx context.Context, operation store.OperationRecord, step store.OperationStep, deadline time.Time) ([]adapter.StreamTransfer, error) {
	if ctx == nil || w == nil || w.transfers == nil {
		return nil, errors.New("initialized backup workflow and stream context are required")
	}
	expectations, direction, err := w.expectedStreamTransfers(ctx, operation, step)
	if err != nil {
		return nil, err
	}
	if len(expectations) == 0 {
		return nil, nil
	}
	stepContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	transfers, err := w.transfers.IssueStepStreamTransfers(stepContext, operation, step, expectations, direction)
	if err != nil {
		return nil, err
	}
	if len(transfers) != len(expectations) {
		return nil, errors.New("stream broker did not issue exactly one transfer for every planned stream")
	}
	if deadline.IsZero() || !deadline.After(time.Now()) {
		return nil, errors.New("stream transfer issuance has no live invocation deadline")
	}
	now := time.Now().UTC()
	seen := make(map[string]struct{}, len(transfers))
	for i := range transfers {
		transfer := &transfers[i]
		encoded, err := json.Marshal(transfer.Reference)
		if err != nil || w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":storage:storage-transfer-reference", encoded) != nil {
			return nil, errors.New("stream broker issued a handle that does not match the registered schema")
		}
		if transfer.Reference.Direction != direction || transfer.Reference.MaximumBytes != expectations[i].MaximumBytes || transfer.Reference.ExpiresAt.IsZero() ||
			!transfer.Reference.ExpiresAt.After(now) || transfer.Reference.ExpiresAt.After(deadline) || transfer.Reference.ExpiresAt.After(now.Add(5*time.Minute)) {
			return nil, errors.New("stream broker issued a handle with an unsafe direction, bound, or expiry")
		}
		if _, duplicate := seen[transfer.Reference.TransferID]; duplicate {
			return nil, errors.New("stream broker reused a transfer ID within one invocation")
		}
		seen[transfer.Reference.TransferID] = struct{}{}
		if transfer.Verify == nil || (direction == "controller_to_adapter" && (transfer.Source == nil || transfer.Sink != nil)) ||
			(direction == "adapter_to_controller" && (transfer.Sink == nil || transfer.Source != nil)) {
			return nil, errors.New("stream broker did not bind its handle to exactly one correct-direction stream")
		}
	}
	return transfers, nil
}

func (w *ServerBackupWorkflow) expectedStreamTransfers(ctx context.Context, operation store.OperationRecord, step store.OperationStep) ([]ServerBackupStreamExpectation, string, error) {
	input := effectiveStepInput(step)
	switch step.CapabilityName {
	case "game.backup.capture":
		dataSet, ok := w.dataSetForStep(ctx, operation, step.StepID, "capture")
		if !ok {
			return nil, "", errors.New("capture stream is outside the trusted backup plan")
		}
		return []ServerBackupStreamExpectation{{DataSetID: dataSet.DataSetID, SizeBytes: -1, MaximumBytes: dataSet.MaximumBytes}}, "adapter_to_controller", nil
	case "storage.object.put", "storage.object.get":
		action := "upload"
		if step.CapabilityName == "storage.object.get" {
			action = "readback"
		}
		dataSet, ok := w.dataSetForStep(ctx, operation, step.StepID, action)
		if !ok {
			return nil, "", errors.New("storage stream is outside the trusted backup plan")
		}
		var decoded map[string]any
		if err := json.Unmarshal(input, &decoded); err != nil {
			return nil, "", err
		}
		size, err := integerFromAny(decoded["expected_size_bytes"])
		if err != nil {
			return nil, "", errors.New("storage stream input has no exact expected size")
		}
		checksum := asStringMap(decoded["expected_checksum"])
		direction := "controller_to_adapter"
		if step.CapabilityName == "storage.object.get" {
			direction = "adapter_to_controller"
		}
		return []ServerBackupStreamExpectation{{DataSetID: dataSet.DataSetID, SizeBytes: size, MaximumBytes: maxInt64(size, 1), SHA256: stringValueMap(checksum, "digest")}}, direction, nil
	case "game.backup.validate":
		var decoded map[string]any
		if err := json.Unmarshal(input, &decoded); err != nil {
			return nil, "", err
		}
		manifest := asStringMap(decoded["manifest"])
		expectations := make([]ServerBackupStreamExpectation, 0, len(asAnySlice(manifest["objects"])))
		for _, rawObject := range asAnySlice(manifest["objects"]) {
			object := asStringMap(rawObject)
			size, err := integerFromAny(object["size_bytes"])
			if err != nil {
				return nil, "", errors.New("validation manifest has an invalid object size")
			}
			dataSetID := stringValueMap(object, "data_set_id")
			checksum := stringValueMap(object, "checksum")
			if dataSetID == "" {
				return nil, "", errors.New("validation manifest has an object without a data set ID")
			}
			expectations = append(expectations, ServerBackupStreamExpectation{DataSetID: dataSetID, SizeBytes: size, MaximumBytes: maxInt64(size, 1), SHA256: checksum})
		}
		return expectations, "controller_to_adapter", nil
	default:
		return nil, "", nil
	}
}

func (w *ServerBackupWorkflow) verifyStreams(ctx context.Context, operation store.OperationRecord, stepID string, expectations []ServerBackupStreamExpectation) error {
	step := operationStep(operation, stepID)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
		return errors.New("completed stream step is missing")
	}
	if err := w.transfers.VerifyStepStreams(ctx, operation, step, expectations); err != nil {
		return fmt.Errorf("controller-side stream verification failed for %s: %w", stepID, err)
	}
	return nil
}

func asStringMap(value any) map[string]any {
	if result, ok := value.(map[string]any); ok && result != nil {
		return result
	}
	return map[string]any{}
}

func asAnySlice(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	return nil
}

func stringValueMap(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func integerFromAny(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Int64()
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	case float64:
		return int64(typed), nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func maxInt64(value, minimum int64) int64 {
	if value < minimum {
		return minimum
	}
	return value
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

type backupPreparationObservation struct {
	Target              json.RawMessage `json:"target"`
	ReleaseID           string          `json:"release_id"`
	BackupID            string          `json:"backup_id"`
	DataGenerationID    string          `json:"data_generation_id"`
	DataSetIDs          []string        `json:"data_set_ids"`
	ConsistencyMode     string          `json:"consistency_mode"`
	Outcome             string          `json:"outcome"`
	PreparationID       string          `json:"preparation_id"`
	ConsistencyEvidence string          `json:"consistency_evidence"`
	ObservedAt          string          `json:"observed_at"`
}

type backupCaptureObservation struct {
	Target           json.RawMessage `json:"target"`
	ReleaseID        string          `json:"release_id"`
	BackupID         string          `json:"backup_id"`
	DataGenerationID string          `json:"data_generation_id"`
	PreparationID    string          `json:"preparation_id"`
	DataSetID        string          `json:"data_set_id"`
	CaptureID        string          `json:"capture_id"`
	ConsistencyMode  string          `json:"consistency_mode"`
	Outcome          string          `json:"outcome"`
	StreamCompleted  bool            `json:"stream_completed"`
	SizeBytes        int64           `json:"size_bytes"`
	Checksum         struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"checksum"`
	ObservedAt string `json:"observed_at"`
}

type backupPutObservation struct {
	Target        json.RawMessage `json:"target"`
	UploadState   string          `json:"upload_state"`
	ObjectRef     json.RawMessage `json:"object_ref"`
	BytesReceived int64           `json:"bytes_received"`
	ObservedAt    string          `json:"observed_at"`
}

type backupGetObservation struct {
	ObjectRef        json.RawMessage `json:"object_ref"`
	TransferState    string          `json:"transfer_state"`
	BytesSent        *int64          `json:"bytes_sent"`
	ComputedChecksum struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"computed_checksum"`
	ObservedAt string `json:"observed_at"`
}

type backupValidationObservation struct {
	BackupID         string `json:"backup_id"`
	EnvironmentID    string `json:"environment_id"`
	DataGenerationID string `json:"data_generation_id"`
	ValidationResult string `json:"validation_result"`
	ObservedAt       string `json:"observed_at"`
	CheckResults     []struct {
		CheckID      string `json:"check_id"`
		CheckVersion string `json:"check_version"`
		Result       string `json:"result"`
		ObservedAt   string `json:"observed_at"`
	} `json:"check_results"`
}

func backupPrepareOutput(operation store.OperationRecord) (backupPreparationObservation, error) {
	var output backupPreparationObservation
	step := operationStep(operation, backupPrepareStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("backup preparation output is missing")
	}
	var input backupPreparationObservation
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || output.Outcome != "prepared" ||
		!equalJSON(output.Target, input.Target) || output.ReleaseID != input.ReleaseID || output.BackupID != input.BackupID ||
		output.DataGenerationID != input.DataGenerationID || output.ConsistencyMode != input.ConsistencyMode ||
		!sameStringSlice(output.DataSetIDs, input.DataSetIDs) || output.PreparationID == "" || output.ConsistencyEvidence == "" {
		return backupPreparationObservation{}, errors.New("backup preparation output does not bind the exact request")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return backupPreparationObservation{}, err
	}
	return output, nil
}

func backupCaptureOutput(operation store.OperationRecord, dataSetID string) (backupCaptureObservation, error) {
	var output backupCaptureObservation
	step := operationStep(operation, backupDataStepID("capture", dataSetID))
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("capture output is missing")
	}
	var input backupCaptureObservation
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || output.Outcome != "captured" || !output.StreamCompleted ||
		!equalJSON(output.Target, input.Target) || output.ReleaseID != input.ReleaseID || output.BackupID != input.BackupID ||
		output.DataGenerationID != input.DataGenerationID || output.PreparationID != input.PreparationID ||
		output.DataSetID != input.DataSetID || output.CaptureID != input.CaptureID || output.ConsistencyMode != input.ConsistencyMode ||
		output.SizeBytes < 0 || output.Checksum.Algorithm != "sha256" || len(output.Checksum.Digest) != 64 {
		return backupCaptureObservation{}, errors.New("capture output does not bind the exact prepared data set")
	}
	if _, err := hex.DecodeString(output.Checksum.Digest); err != nil {
		return backupCaptureObservation{}, errors.New("capture output checksum is not hexadecimal SHA-256")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return backupCaptureObservation{}, err
	}
	return output, nil
}

func backupPutOutput(operation store.OperationRecord, dataSetID string) (backupPutObservation, error) {
	var output backupPutObservation
	step := operationStep(operation, backupDataStepID("upload", dataSetID))
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("object upload output is missing")
	}
	var input struct {
		Target json.RawMessage `json:"target"`
	}
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || !equalJSON(output.Target, input.Target) ||
		!backupObjectRefMatchesTarget(output.ObjectRef, input.Target) || output.BytesReceived < 0 {
		return backupPutObservation{}, errors.New("object upload output does not bind the exact target")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return backupPutObservation{}, err
	}
	return output, nil
}

func backupGetOutput(operation store.OperationRecord, dataSetID string) (backupGetObservation, error) {
	var output backupGetObservation
	step := operationStep(operation, backupDataStepID("readback", dataSetID))
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("object readback output is missing")
	}
	var input struct {
		ObjectRef json.RawMessage `json:"object_ref"`
	}
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || !equalJSON(output.ObjectRef, input.ObjectRef) || output.BytesSent == nil {
		return backupGetObservation{}, errors.New("object readback output does not bind the exact object")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return backupGetObservation{}, err
	}
	return output, nil
}

func backupObjectRefMatchesTarget(objectRef, target json.RawMessage) bool {
	var ref, destination map[string]any
	if json.Unmarshal(objectRef, &ref) != nil || json.Unmarshal(target, &destination) != nil || len(ref) == 0 || len(destination) == 0 {
		return false
	}
	for _, key := range []string{"adapter_id", "storage_profile_id", "environment_id", "container", "key"} {
		if !reflect.DeepEqual(ref[key], destination[key]) {
			return false
		}
	}
	return true
}

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

func (w *ServerBackupWorkflow) checkBackupSaveGame(_ context.Context, e *backupPreconditionEvidence) error {
	if !e.initiallyActive || e.server.LifecycleState != e.initialState || !e.providerActive || !e.runtimeExpected || !e.dataDescribed {
		return errors.New("offline backup save requires the exact ready or degraded running server and declared data sets")
	}
	return nil
}

func (w *ServerBackupWorkflow) checkBackupStopRuntime(_ context.Context, e *backupPreconditionEvidence) error {
	lifecycleHeld := e.server.LifecycleState == e.initialState || e.server.LifecycleState == protocolstate.ServerStopping
	if !e.initiallyActive || !lifecycleHeld || !gameBackupSaveConfirmed(e.operation) || !e.providerActive || !e.dataDescribed {
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
	if captureErr != nil || putErr != nil || put.UploadState != "uploaded" || put.BytesReceived != capture.SizeBytes ||
		!backupObjectRefMatchesTarget(put.ObjectRef, put.Target) {
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

type backupDataSetDescription struct {
	DataSetID                 string   `json:"data_set_id"`
	RelativePaths             []string `json:"relative_paths"`
	Required                  bool     `json:"required"`
	SupportedConsistencyModes []string `json:"supported_consistency_modes"`
	DataFormat                string   `json:"data_format,omitempty"`
}

func backupDataDescriptionMatches(operation store.OperationRecord, profile ServerBackupProfile, server store.LogicalServer) bool {
	step := operationStep(operation, backupDescribeDataStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
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
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || json.Unmarshal(step.Output, &output) != nil ||
		!equalJSON(input.Target, output.Target) || input.ReleaseID != profile.GameReleaseID || output.ReleaseID != profile.GameReleaseID ||
		!equalJSON(output.Target, mustJSON(map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID, "instance_ref": server.InstanceRef})) ||
		len(output.DataSets) != len(profile.DataSets) {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return false
	}
	for i, actual := range output.DataSets {
		trusted := profile.DataSets[i]
		if actual.DataSetID != trusted.DataSetID || actual.Required != trusted.Required || actual.DataFormat != trusted.DataFormat ||
			!sameStringSlice(actual.RelativePaths, trusted.RelativePaths) || !sameStringSlice(actual.SupportedConsistencyModes, trusted.SupportedConsistencyModes) {
			return false
		}
		for _, relative := range actual.RelativePaths {
			if !validBackupRelativePath(relative) {
				return false
			}
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
		output.Outcome != "completed" || output.State != "stopped" || !stepBindsRef(step.Input, "unit_ref", output.UnitRef) {
		return executionUnitStopResult{}, errors.New("runtime stop output does not confirm the exact unit stopped")
	}
	return output, nil
}

func gameBackupSaveConfirmed(operation store.OperationRecord) bool {
	step := operationStep(operation, backupSaveGameStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
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
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || json.Unmarshal(step.Output, &output) != nil ||
		output.Outcome != "confirmed" || output.Target.ServerID != operation.Target.ServerID ||
		output.Target.EnvironmentID != operation.Target.EnvironmentID || output.ReleaseID != input.ReleaseID ||
		!equalJSON(output.Target.InstanceRef, input.Target.InstanceRef) ||
		!equalJSON(input.Target.InstanceRef, stepField(operationStep(operation, backupInspectInstanceStep).Input, "instance_ref")) {
		return false
	}
	_, observedErr := time.Parse(time.RFC3339Nano, output.ObservedAt)
	_, savedErr := time.Parse(time.RFC3339Nano, output.SavedAt)
	return observedErr == nil && savedErr == nil
}

func backupReadbacksComplete(operation store.OperationRecord, profile ServerBackupProfile) bool {
	for _, dataSet := range profile.DataSets {
		capture, captureErr := backupCaptureOutput(operation, dataSet.DataSetID)
		put, putErr := backupPutOutput(operation, dataSet.DataSetID)
		get, getErr := backupGetOutput(operation, dataSet.DataSetID)
		if captureErr != nil || putErr != nil || getErr != nil || capture.Outcome != "captured" ||
			put.UploadState != "uploaded" || put.BytesReceived != capture.SizeBytes ||
			!backupObjectRefMatchesTarget(put.ObjectRef, put.Target) || get.TransferState != "complete" ||
			get.BytesSent == nil || *get.BytesSent != capture.SizeBytes || !equalJSON(get.ObjectRef, put.ObjectRef) ||
			get.ComputedChecksum.Algorithm != capture.Checksum.Algorithm || get.ComputedChecksum.Digest != capture.Checksum.Digest {
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
	return step.StepID != "" && json.Unmarshal(effectiveStepInput(step), &input) == nil &&
		input.Target.ServerID == server.ServerID && input.Target.EnvironmentID == server.EnvironmentID &&
		equalJSON(input.Target.InstanceRef, server.InstanceRef) && input.ReleaseID == profile.GameReleaseID
}

func backupValidationConfirmed(operation store.OperationRecord, profile ServerBackupProfile) bool {
	step := operationStep(operation, backupValidateStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
		return false
	}
	var output backupValidationObservation
	if json.Unmarshal(step.Output, &output) != nil || output.ValidationResult != "valid" ||
		output.BackupID != operation.OperationID || output.EnvironmentID != operation.Target.EnvironmentID ||
		len(output.CheckResults) != len(profile.ValidationChecks) {
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
	if len(requested) != 0 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, output.ObservedAt)
	return err == nil
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
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
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

func backupSaveRejected(operation store.OperationRecord) bool {
	step := operationStep(operation, backupSaveGameStep)
	input := decodeObject(effectiveStepInput(step))
	output := decodeObject(step.Output)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded ||
		stringValueMap(output, "outcome") != "rejected" || stringValueMap(output, "reason_code") == "" ||
		stringValueMap(output, "release_id") != stringValueMap(input, "release_id") ||
		!equalJSON(mustJSON(output["target"]), mustJSON(input["target"])) ||
		!backupTargetMatchesOperation(mustJSON(output["target"]), operation) {
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
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded ||
		stringValueMap(output, "outcome") != "rejected" || stringValueMap(output, "reason_code") == "" ||
		stringValueMap(output, "backup_id") != stringValueMap(input, "backup_id") ||
		stringValueMap(output, "data_generation_id") != stringValueMap(input, "data_generation_id") ||
		stringValueMap(output, "release_id") != stringValueMap(input, "release_id") ||
		stringValueMap(output, "consistency_mode") != stringValueMap(input, "consistency_mode") ||
		!sameJSONList(output["data_set_ids"], input["data_set_ids"]) ||
		!equalJSON(mustJSON(output["target"]), mustJSON(input["target"])) ||
		!backupTargetMatchesOperation(mustJSON(output["target"]), operation) {
		return false
	}
	return validObservedAt(stringValueMap(output, "observed_at"))
}

func backupCaptureRejected(operation store.OperationRecord, dataSetID string) bool {
	step := operationStep(operation, backupDataStepID("capture", dataSetID))
	input := decodeObject(effectiveStepInput(step))
	output := decodeObject(step.Output)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded ||
		stringValueMap(output, "outcome") != "rejected" || stringValueMap(output, "reason_code") == "" ||
		stringValueMap(output, "backup_id") != stringValueMap(input, "backup_id") ||
		stringValueMap(output, "data_generation_id") != stringValueMap(input, "data_generation_id") ||
		stringValueMap(output, "preparation_id") != stringValueMap(input, "preparation_id") ||
		stringValueMap(output, "data_set_id") != dataSetID || stringValueMap(output, "data_set_id") != stringValueMap(input, "data_set_id") ||
		stringValueMap(output, "capture_id") != stringValueMap(input, "capture_id") ||
		stringValueMap(output, "release_id") != stringValueMap(input, "release_id") ||
		stringValueMap(output, "consistency_mode") != stringValueMap(input, "consistency_mode") ||
		!equalJSON(mustJSON(output["target"]), mustJSON(input["target"])) ||
		!backupTargetMatchesOperation(mustJSON(output["target"]), operation) {
		return false
	}
	return validObservedAt(stringValueMap(output, "observed_at"))
}

func backupValidationRejected(operation store.OperationRecord) bool {
	step := operationStep(operation, backupValidateStep)
	input := decodeObject(effectiveStepInput(step))
	output := decodeObject(step.Output)
	manifest := asStringMap(input["manifest"])
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded ||
		stringValueMap(output, "validation_result") != "invalid" ||
		stringValueMap(input, "backup_id") != operation.OperationID ||
		stringValueMap(input, "environment_id") != operation.Target.EnvironmentID ||
		stringValueMap(output, "backup_id") != operation.OperationID ||
		stringValueMap(output, "environment_id") != operation.Target.EnvironmentID ||
		stringValueMap(output, "data_generation_id") != stringValueMap(manifest, "data_generation_id") ||
		!validObservedAt(stringValueMap(output, "observed_at")) {
		return false
	}
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

func (w *ServerBackupWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	code := "core.safety.backup_precondition_unmet"
	message := "Backup evidence did not establish the exact safe precondition; no next adapter call was issued."
	switch step.StepID {
	case backupStopRuntimeStep:
		if backupSaveRejected(operation) {
			message = "The game adapter rejected the backup save; the runtime was not stopped."
			return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
		}
	case backupPrepareStep:
		if operationStep(operation, backupStopRuntimeStep).StepID != "" && backupRuntimeStopOutputSucceeded(operation) {
			break
		}
		return Resolution{State: ResolutionNeedsOperator}, nil
	default:
		if dataSet, ok := backupDataSetForOperation(operation, step.StepID, "capture"); ok && backupPreparationRejected(operation) {
			message = fmt.Sprintf("The game adapter rejected backup preparation for data set %q.", dataSet.DataSetID)
			return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
		}
		if dataSet, ok := backupDataSetForOperation(operation, step.StepID, "upload"); ok && backupCaptureRejected(operation, dataSet.DataSetID) {
			message = fmt.Sprintf("The game adapter rejected capture of data set %q.", dataSet.DataSetID)
			return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
		}
		if dataSet, ok := backupDataSetForOperation(operation, step.StepID, "readback"); ok {
			put, err := backupPutOutput(operation, dataSet.DataSetID)
			if err == nil && put.UploadState == "not_uploaded" {
				message = fmt.Sprintf("Storage confirmed that data set %q was not uploaded.", dataSet.DataSetID)
				return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "provider_failure", message, operation.OperationID, false, "inspect_operation", true)}, nil
			}
		}
		if step.StepID == backupStartRuntimeStep && backupValidationRejected(operation) {
			message = "Game-specific backup validation did not confirm every required check."
			return Resolution{State: ResolutionDefinitiveError, Error: protocolError(code, "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
		}
	}
	return Resolution{State: ResolutionNeedsOperator}, nil
}

func (w *ServerBackupWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil || ctx.Err() != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	if _, ok := backupDataSetIDFromStep(operation, step.StepID, "readback"); ok {
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	switch step.StepID {
	case backupInspectInstanceStep, backupInspectRuntimeStep, backupDescribeDataStep, backupValidateStep, backupVerifyRuntimeStep, backupVerifyStoppedStep, backupReadinessStep:
		// These are read-only. Any stream read is reissued with a fresh handle
		// and a new attempt-scoped staging slot before the safe retry.
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	case backupStopRuntimeStep:
		observation, err := w.inspectRuntimeForReconciliation(ctx, operation, step)
		if err != nil {
			return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
		}
		switch observation.State {
		case "stopped":
			output, _ := json.Marshal(executionUnitStopResult{UnitRef: observation.UnitRef, Outcome: "completed", State: "stopped", ObservedAt: observation.ObservedAt})
			return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}, nil
		case "running":
			return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
		case "stopping":
			return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}, nil
		default:
			return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
		}
	case backupStartRuntimeStep:
		observation, err := w.inspectRuntimeForReconciliation(ctx, operation, step)
		if err != nil {
			return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
		}
		switch observation.State {
		case "running":
			output, _ := json.Marshal(executionUnitStartResult{UnitRef: observation.UnitRef, Outcome: "completed", State: "running", ObservedAt: observation.ObservedAt})
			return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}, nil
		case "stopped", "absent":
			return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
		case "starting":
			return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}, nil
		case "failed":
			return Resolution{State: ResolutionDefinitiveError, ExternalOperationRef: step.ExternalOperationRef,
				Error: protocolError("core.execution.unit_failed", "provider_failure", "The exact execution unit is confirmed failed after backup resume was attempted.", operation.OperationID, false, "operator_review", true)}, nil
		default:
			return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
		}
	default:
		// Save, prepare, capture, and upload are mutating. The protocol has no
		// read-only status capability that proves an unknown outcome did not
		// take effect, so the workflow never retries these calls blindly.
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

func (w *ServerBackupWorkflow) inspectRuntimeForReconciliation(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (stopUnitInspection, error) {
	inspect := operationStep(operation, backupInspectRuntimeStep)
	if step.StepID == backupStartRuntimeStep {
		inspect = operationStep(operation, backupVerifyRuntimeStep)
	}
	if inspect.StepID == "" || inspect.CapabilityName != "execution.unit.inspect" || inspect.AdapterID != step.AdapterID ||
		inspect.CapabilityVersion == "" || !stepBindsRef(inspect.Input, "unit_ref", stepField(step.Input, "unit_ref")) {
		return stopUnitInspection{}, errors.New("no exact runtime inspection step is available for reconciliation")
	}
	capability, err := exactCapability(w.adapters, inspect.AdapterID, adapter.CategoryExecution, inspect.CapabilityName,
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-output", false)
	if err != nil || capability.Version != inspect.CapabilityVersion {
		return stopUnitInspection{}, errors.New("pinned runtime inspection capability is unavailable")
	}
	deadline := time.Now().UTC().Add(15 * time.Second)
	requestID := backupReconcileRequestID(operation.OperationID, step.StepID, step.Attempt)
	correlation := operation.CorrelationID
	if correlation == "" {
		correlation = operation.OperationID
	}
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: operation.ProtocolVersion, Capability: capability.Name,
		RequestContext: adapter.RequestContext{RequestID: requestID, CorrelationID: correlation, OperationID: operation.OperationID,
			StepID: step.StepID, ServerID: operation.Target.ServerID, EnvironmentID: operation.Target.EnvironmentID, Deadline: deadline},
		Input: append(json.RawMessage(nil), effectiveStepInput(inspect)...),
	}}
	callContext, cancel := context.WithDeadline(ctx, deadline)
	response, invokeErr := w.adapters.Invoke(callContext, inspect.AdapterID, request)
	cancel()
	if invokeErr != nil || response.JSONRPC != "2.0" || response.ID != requestID || response.Result.Outcome != adapter.OutcomeCompleted ||
		w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		return stopUnitInspection{}, errors.New("runtime inspection did not complete during reconciliation")
	}
	var observation stopUnitInspection
	if json.Unmarshal(response.Result.Output, &observation) != nil || !equalJSON(observation.UnitRef, stepField(step.Input, "unit_ref")) {
		return stopUnitInspection{}, errors.New("runtime reconciliation inspection is not bound to the exact unit")
	}
	if _, err := time.Parse(time.RFC3339Nano, observation.ObservedAt); err != nil {
		return stopUnitInspection{}, err
	}
	return observation, nil
}

func backupReconcileRequestID(operationID, stepID string, attempt int) string {
	digest := sha256.Sum256([]byte(operationID + "\x00" + stepID + "\x00reconcile-backup\x00" + fmt.Sprint(attempt)))
	return "reconcile-" + hex.EncodeToString(digest[:16])
}

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
	if dataGenerationID == "" || prepareErr != nil || dataGenerationID != prepared.DataGenerationID ||
		(dataGenerationID != stored.Record.DataGenerationID && !backupResumeAfterBackup(operation)) {
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

func hasValidBackupResult(raw json.RawMessage, operation store.OperationRecord, server store.LogicalServer, schemas schemaRuntime, expectedState protocolstate.ServerState) bool {
	if schemas == nil || schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-backup-result", raw) != nil {
		return false
	}
	var result struct {
		OperationType string `json:"operation_type"`
		OperationData struct {
			BackupID         string          `json:"backup_id"`
			EnvironmentID    string          `json:"environment_id"`
			DataGenerationID string          `json:"data_generation_id"`
			Manifest         json.RawMessage `json:"manifest"`
			FinalState       string          `json:"final_lifecycle_state"`
			Readiness        json.RawMessage `json:"readiness_assertion"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.OperationType != "server.backup" ||
		result.OperationData.BackupID != operation.OperationID || result.OperationData.EnvironmentID != operation.Target.EnvironmentID ||
		result.OperationData.FinalState != string(expectedState) || !isObject(result.OperationData.Manifest) {
		return false
	}
	var manifest struct {
		ManifestVersion   string          `json:"manifest_version"`
		BackupID          string          `json:"backup_id"`
		ServerID          string          `json:"server_id"`
		EnvironmentID     string          `json:"environment_id"`
		DataGenerationID  string          `json:"data_generation_id"`
		SourceInstanceRef json.RawMessage `json:"source_instance_ref"`
		SourceOperationID string          `json:"source_operation_id"`
	}
	if json.Unmarshal(result.OperationData.Manifest, &manifest) != nil ||
		manifest.ManifestVersion != backupManifestVersion || manifest.BackupID != operation.OperationID ||
		manifest.ServerID != operation.Target.ServerID || manifest.EnvironmentID != operation.Target.EnvironmentID ||
		manifest.DataGenerationID != result.OperationData.DataGenerationID || manifest.SourceOperationID != operation.OperationID ||
		!equalJSON(manifest.SourceInstanceRef, server.InstanceRef) {
		return false
	}
	if expectedState == protocolstate.ServerReady {
		var assertion readinessAssertion
		return json.Unmarshal(result.OperationData.Readiness, &assertion) == nil &&
			assertion.ServerID == server.ServerID && assertion.EnvironmentID == server.EnvironmentID &&
			equalJSON(assertion.InstanceRef, server.InstanceRef) &&
			schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":lifecycle:readiness-assertion", result.OperationData.Readiness) == nil
	}
	return backupFinalRuntimeStopped(operation)
}
