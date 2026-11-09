package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// ServerRestoreProfile contains controller-owned policy for restoring a
// verified backup onto the logical server's current managed instance.
type ServerRestoreProfile struct {
	ProfileID                     string
	GameReleaseID                 string
	GameAdapterID                 string
	GameVersion                   string
	GameReleaseDescription        json.RawMessage
	RequiredCompatibilityCheckIDs []string
	RequiredRestoreCheckIDs       []string
	StorageProfileID              string
	StorageAdapterID              string
	StorageVersioningEnabled      bool
	// TargetDataKnownEmpty is true only when controller-owned deployment
	// metadata proves the exact target has no world data to replace.
	TargetDataKnownEmpty    bool
	UnitRef                 json.RawMessage
	ReadinessCheckSetID     string
	RequiredReadinessChecks []ReadinessRequirement
}

// ServerRestoreProfileResolver selects immutable release and storage policy
// from controller configuration, never from client-supplied adapter steps.
type ServerRestoreProfileResolver interface {
	ResolveServerRestore(context.Context, store.LogicalServer) (ServerRestoreProfile, error)
}

// ServerRestoreBackupResolver returns only a controller-verified backup and
// opens the exact immutable objects named by its manifest.
type ServerRestoreBackupResolver interface {
	ResolveServerRestoreBackup(context.Context, store.LogicalServer, string, string) (VerifiedProvisionBackup, error)
	OpenServerRestoreBackupObject(context.Context, store.LogicalServer, string, json.RawMessage) (io.ReadCloser, error)
}

// ServerRestoreAuthorizer checks overwrite approval against the exact request.
// Implementations must be idempotent and non-consuming because the engine can
// recheck preconditions when it retries a step after a transient failure.
type ServerRestoreAuthorizer interface {
	AuthorizeServerRestore(context.Context, string, string, store.OperationRecord, store.LogicalServer, string) error
}

type ServerRestoreWorkflow struct {
	servers      store.LifecycleOperationStore
	adapters     adapterRuntime
	schemas      schemaRuntime
	profiles     ServerRestoreProfileResolver
	backups      ServerRestoreBackupResolver
	authorizer   ServerRestoreAuthorizer
	assertionTTL time.Duration
}

func NewServerRestoreWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime,
	profiles ServerRestoreProfileResolver, backups ServerRestoreBackupResolver, authorizer ServerRestoreAuthorizer, assertionTTL time.Duration) (*ServerRestoreWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil || backups == nil || authorizer == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, trusted restore profile, verified backup resolver, and restore authorizer are required")
	}
	if assertionTTL <= 0 || assertionTTL > maxReadinessAssertionTTL {
		return nil, errors.New("readiness assertion TTL must be in the range 1ns..15m")
	}
	return &ServerRestoreWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles,
		backups: backups, authorizer: authorizer, assertionTTL: assertionTTL}, nil
}

type serverRestoreParameters struct {
	BackupID          string          `json:"backup_id"`
	ReplaceExisting   bool            `json:"replace_existing_data"`
	ApprovalID        string          `json:"approval_id"`
	StartAfterRestore bool            `json:"start_after_restore"`
	TargetInstanceRef json.RawMessage `json:"target_instance_ref"`
}

const (
	restoreInspectInstanceStep = "inspect-instance"
	restoreInspectRuntimeStep  = "inspect-runtime"
	restoreCompatibilityStep   = "check-restore-compatibility"
	restoreApplyStep           = "restore-backup"
	restoreInspectStep         = "inspect-restore"
	restoreStartStep           = "start-runtime"
	restoreVerifyRuntimeStep   = "verify-runtime"
	restoreReadinessStep       = "check-readiness"
)

type restoreGameResult struct {
	BackupID            string                   `json:"backup_id"`
	EnvironmentID       string                   `json:"environment_id"`
	SourceGenerationID  string                   `json:"source_data_generation_id"`
	Target              json.RawMessage          `json:"target"`
	TargetGenerationID  string                   `json:"target_data_generation_id"`
	RestoreExecutionRef json.RawMessage          `json:"restore_execution_ref"`
	Outcome             string                   `json:"outcome"`
	ObservedAt          string                   `json:"observed_at"`
	ValidationChecks    []restoreValidationCheck `json:"validation_checks"`
}

type restoreValidationCheck struct {
	CheckID    string `json:"check_id"`
	Result     string `json:"result"`
	ObservedAt string `json:"observed_at"`
}

// BuildPlan pins every adapter capability and exact resource before dispatch.
func (w *ServerRestoreWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if ctx == nil || w == nil || w.servers == nil || w.adapters == nil || w.schemas == nil || w.profiles == nil || w.backups == nil || w.authorizer == nil {
		return nil, errors.New("initialized server.restore workflow and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if operation.OperationType != "server.restore" || operation.ProtocolVersion != w.schemas.ProtocolVersion() ||
		operation.RequestSnapshot.OperationType != "server.restore" || operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" || !isObject(operation.RequestSnapshot.Parameters) {
		return nil, errors.New("server.restore operation and loaded protocol version are required")
	}
	var parameters serverRestoreParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil || parameters.BackupID == "" {
		return nil, errors.New("decode server.restore parameters")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, fmt.Errorf("load logical server for restore plan: %w", err)
	}
	server := stored.Record
	if (server.LifecycleState != protocolstate.ServerStopped && server.LifecycleState != protocolstate.ServerFailed) || len(server.InstanceRef) == 0 {
		return nil, errors.New("server.restore requires a stopped or failed logical server with a managed instance")
	}
	if server.NetworkProfile != "" {
		return nil, errors.New("server.restore cannot mutate a network-routed server until proxy isolation is integrated")
	}
	if (len(operation.Target.InstanceRef) > 0 && !equalJSON(operation.Target.InstanceRef, server.InstanceRef)) ||
		(len(parameters.TargetInstanceRef) > 0 && !equalJSON(parameters.TargetInstanceRef, server.InstanceRef)) {
		return nil, store.ErrLogicalServerTargetMismatch
	}
	profile, err := w.profiles.ResolveServerRestore(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("resolve trusted server restore profile: %w", err)
	}
	if err := validateServerRestoreProfile(server, profile); err != nil {
		return nil, err
	}
	if restoreRequiresOverwriteApproval(server, profile) && !parameters.ReplaceExisting {
		return nil, errors.New("restore target is not confirmed empty; replace_existing_data=true and explicit approval are required")
	}
	if err := validateServerRestoreRelease(w.schemas, server, profile); err != nil {
		return nil, err
	}
	backup, err := w.backups.ResolveServerRestoreBackup(ctx, server, profile.StorageProfileID, parameters.BackupID)
	if err != nil {
		return nil, fmt.Errorf("resolve verified restore backup: %w", err)
	}
	backupResult, manifestRaw, manifest, err := validateServerRestoreBackup(w.schemas, server, parameters.BackupID, profile, backup)
	if err != nil {
		return nil, err
	}
	if parameters.ReplaceExisting {
		if parameters.ApprovalID == "" {
			return nil, errors.New("replace_existing_data requires an approval_id")
		}
	}
	if parameters.StartAfterRestore {
		descriptor, err := w.adapters.Descriptor(server.GameAdapterID)
		if err != nil || verifyReadinessSetDeclared(descriptor, server.EnvironmentID, ServerStartProfile{
			GameReleaseID: profile.GameReleaseID, ReadinessCheckSetID: profile.ReadinessCheckSetID, RequiredChecks: profile.RequiredReadinessChecks,
		}) != nil {
			return nil, errors.New("restore start policy lacks the declared exact readiness check set")
		}
	}

	version := w.schemas.ProtocolVersion()
	type requirement struct {
		id, adapterID       string
		category            adapter.Category
		name, input, output string
		mutating            bool
	}
	requirements := []requirement{
		{restoreInspectInstanceStep, adapterIDFromRef(server.InstanceRef), adapter.CategoryHosting, "hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false},
		{restoreCompatibilityStep, server.GameAdapterID, adapter.CategoryGame, "game.compatibility.check", "game:compatibility-check-input", "game:compatibility-check-output", false},
		{restoreInspectRuntimeStep, server.ExecutionAdapterID, adapter.CategoryExecution, "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false},
		{restoreApplyStep, server.GameAdapterID, adapter.CategoryGame, "game.restore", "game:restore-input", "game:restore-output", true},
		{restoreInspectStep, server.GameAdapterID, adapter.CategoryGame, "game.restore.inspect", "game:restore-inspect-input", "game:restore-output", false},
	}
	if parameters.StartAfterRestore {
		requirements = append(requirements,
			requirement{restoreStartStep, server.ExecutionAdapterID, adapter.CategoryExecution, "execution.unit.start", "execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true},
			requirement{restoreVerifyRuntimeStep, server.ExecutionAdapterID, adapter.CategoryExecution, "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false},
			requirement{restoreReadinessStep, server.GameAdapterID, adapter.CategoryGame, "game.readiness.check", "game:readiness-check-input", "game:readiness-check-output", false})
	}
	target := map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID, "instance_ref": server.InstanceRef}
	steps := make([]store.OperationStep, 0, len(requirements))
	for _, req := range requirements {
		capability, err := exactCapability(w.adapters, req.adapterID, req.category, req.name,
			"urn:gameserver-controller:"+version+":"+req.input, "urn:gameserver-controller:"+version+":"+req.output, req.mutating)
		if err != nil {
			return nil, err
		}
		var input any
		switch req.id {
		case restoreInspectInstanceStep:
			input = map[string]any{"instance_ref": server.InstanceRef}
		case restoreInspectRuntimeStep, restoreVerifyRuntimeStep:
			input = map[string]any{"unit_ref": profile.UnitRef}
		case restoreCompatibilityStep:
			input = map[string]any{"source_manifest": manifestRaw, "target_release": profile.GameReleaseDescription, "required_check_ids": profile.RequiredCompatibilityCheckIDs}
		case restoreApplyStep:
			objects := make([]map[string]any, 0, len(manifest.Objects))
			for _, object := range manifest.Objects {
				objects = append(objects, map[string]any{"object_ref": object.ObjectRef})
			}
			input = map[string]any{"source_backup": backupResult, "target": target, "target_release_id": profile.GameReleaseID,
				"target_data_generation_id": restoreDataGeneration(operation.OperationID), "required_check_ids": profile.RequiredRestoreCheckIDs, "object_streams": objects}
		case restoreInspectStep:
			input = map[string]any{"backup_id": parameters.BackupID, "target": target, "target_data_generation_id": restoreDataGeneration(operation.OperationID)}
		case restoreStartStep:
			input = map[string]any{"unit_ref": profile.UnitRef}
		case restoreReadinessStep:
			input = map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID, "instance_ref": server.InstanceRef,
				"game_release_id": profile.GameReleaseID, "readiness_check_set_id": profile.ReadinessCheckSetID, "required_checks": profile.RequiredReadinessChecks}
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("encode %s input: %w", req.id, err)
		}
		bindings := []inputbinding.Binding(nil)
		if req.id == restoreApplyStep {
			bindings = []inputbinding.Binding{{TargetPointer: "/compatibility_result", SourceStepID: restoreCompatibilityStep, SourcePointer: ""}}
		}
		if req.id == restoreInspectStep {
			bindings = []inputbinding.Binding{{TargetPointer: "/restore_execution_ref", SourceStepID: restoreApplyStep, SourcePointer: "/restore_execution_ref"}}
		}
		if len(bindings) == 0 {
			if err := w.schemas.ValidateJSON(capability.InputSchemaURI, encoded); err != nil {
				return nil, fmt.Errorf("planned %s input does not match its declared capability schema: %w", req.id, err)
			}
		}
		steps = append(steps, store.OperationStep{StepID: req.id, StepType: capability.Name, Status: protocolstate.StepPending,
			AdapterID: req.adapterID, CapabilityName: capability.Name, CapabilityVersion: capability.Version, Input: encoded, InputBindings: bindings})
	}
	return steps, nil
}

func validateServerRestoreProfile(server store.LogicalServer, profile ServerRestoreProfile) error {
	if profile.ProfileID == "" || profile.GameReleaseID == "" || profile.GameAdapterID != server.GameAdapterID || profile.GameVersion == "" ||
		profile.StorageProfileID == "" || profile.StorageProfileID != server.StorageProfile || profile.StorageAdapterID == "" ||
		!isObject(profile.GameReleaseDescription) || len(profile.RequiredCompatibilityCheckIDs) == 0 || len(profile.RequiredRestoreCheckIDs) == 0 || len(profile.UnitRef) == 0 {
		return errors.New("trusted restore profile must pin target release, matching storage, runtime, compatibility, and restore policies")
	}
	if err := validateStartProfile(server, ServerStartProfile{GameReleaseID: profile.GameReleaseID, UnitRef: profile.UnitRef,
		ReadinessCheckSetID: profile.ReadinessCheckSetID, RequiredChecks: profile.RequiredReadinessChecks}); err != nil {
		return fmt.Errorf("invalid trusted restore runtime/readiness profile: %w", err)
	}
	if len(profile.RequiredCompatibilityCheckIDs) > 64 || len(profile.RequiredRestoreCheckIDs) > 64 {
		return errors.New("trusted restore check sets exceed protocol limits")
	}
	for _, ids := range [][]string{profile.RequiredCompatibilityCheckIDs, profile.RequiredRestoreCheckIDs} {
		seen := make(map[string]struct{}, len(ids))
		for _, id := range ids {
			if id == "" {
				return errors.New("trusted restore check IDs must not be empty")
			}
			if _, exists := seen[id]; exists {
				return fmt.Errorf("trusted restore check ID %q is duplicated", id)
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func validateServerRestoreRelease(schemas schemaRuntime, server store.LogicalServer, profile ServerRestoreProfile) error {
	if schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":game:release-describe-output", profile.GameReleaseDescription) != nil {
		return errors.New("trusted target game release metadata failed schema validation")
	}
	var release struct {
		GameAdapterID string `json:"game_adapter_id"`
		ReleaseID     string `json:"release_id"`
		GameVersion   string `json:"game_version"`
	}
	if json.Unmarshal(profile.GameReleaseDescription, &release) != nil || release.GameAdapterID != server.GameAdapterID || release.GameAdapterID != profile.GameAdapterID ||
		release.ReleaseID != profile.GameReleaseID || release.GameVersion != profile.GameVersion {
		return errors.New("trusted target release metadata does not match the configured game adapter, release, and version")
	}
	return nil
}

func validateServerRestoreBackup(schemas schemaRuntime, server store.LogicalServer, backupID string, profile ServerRestoreProfile, verified VerifiedProvisionBackup) (json.RawMessage, json.RawMessage, provisionBackupManifest, error) {
	var manifest provisionBackupManifest
	if !verified.IntegrityVerified || schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-backup-result", verified.Result) != nil {
		return nil, nil, manifest, errors.New("selected backup is not an integrity-verified server.backup result")
	}
	var result provisionBackupResult
	if json.Unmarshal(verified.Result, &result) != nil || result.OperationType != "server.backup" || result.Partial || len(result.FailedSteps) != 0 || len(result.UnresolvedConditions) != 0 ||
		result.OperationData.BackupID != backupID || result.OperationData.EnvironmentID != server.EnvironmentID || result.OperationData.DataGeneration == "" || len(result.OperationData.Manifest) == 0 {
		return nil, nil, manifest, errors.New("selected backup result does not bind the exact backup, environment, and source generation")
	}
	if _, err := time.Parse(time.RFC3339Nano, result.OperationData.VerifiedAt); err != nil {
		return nil, nil, manifest, errors.New("selected backup result has an invalid verification time")
	}
	if schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":storage:backup-manifest", result.OperationData.Manifest) != nil ||
		json.Unmarshal(result.OperationData.Manifest, &manifest) != nil || manifest.BackupID != backupID || manifest.ServerID != server.ServerID || manifest.EnvironmentID != server.EnvironmentID ||
		manifest.DataGeneration != result.OperationData.DataGeneration || len(manifest.Objects) == 0 || len(manifest.Objects) > 64 {
		return nil, nil, manifest, errors.New("selected backup manifest does not bind the exact server, environment, backup, and source generation")
	}
	for _, object := range manifest.Objects {
		var ref struct {
			AdapterID        string `json:"adapter_id"`
			StorageProfileID string `json:"storage_profile_id"`
			EnvironmentID    string `json:"environment_id"`
			VersionID        string `json:"version_id"`
		}
		if schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":storage:storage-object-reference", object.ObjectRef) != nil || json.Unmarshal(object.ObjectRef, &ref) != nil ||
			ref.AdapterID != profile.StorageAdapterID || ref.StorageProfileID != profile.StorageProfileID || ref.EnvironmentID != server.EnvironmentID || object.SizeBytes < 0 || object.ChecksumAlgorithm != "sha256" || len(object.Checksum) != sha256.Size*2 {
			return nil, nil, manifest, errors.New("selected backup has an invalid or cross-environment object reference")
		}
		if profile.StorageVersioningEnabled && ref.VersionID == "" {
			return nil, nil, manifest, errors.New("selected backup object lacks an immutable version reference")
		}
		if len(object.Checksum) != sha256.Size*2 {
			return nil, nil, manifest, errors.New("selected backup object checksum is malformed")
		}
		if _, err := hex.DecodeString(object.Checksum); err != nil {
			return nil, nil, manifest, errors.New("selected backup object checksum is not hexadecimal")
		}
	}
	return append(json.RawMessage(nil), verified.Result...), append(json.RawMessage(nil), result.OperationData.Manifest...), manifest, nil
}

func restoreDataGeneration(operationID string) string { return "restore-" + operationID }

func restoreRequiresOverwriteApproval(server store.LogicalServer, profile ServerRestoreProfile) bool {
	return server.DataGenerationStatus == "unknown" || !profile.TargetDataKnownEmpty
}

func observationIsCurrent(value string, maximumAge time.Duration, now time.Time) bool {
	observed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || maximumAge <= 0 {
		return false
	}
	const toleratedClockSkew = 30 * time.Second
	observed = observed.UTC()
	now = now.UTC()
	return !observed.Before(now.Add(-maximumAge)) && !observed.After(now.Add(toleratedClockSkew))
}

func restoreValidationChecksCurrentAt(checks []restoreValidationCheck, observedAt string, maximumAge time.Duration) bool {
	outer, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil || maximumAge <= 0 {
		return false
	}
	outer = outer.UTC()
	const toleratedClockSkew = 30 * time.Second
	for _, check := range checks {
		observed, err := time.Parse(time.RFC3339Nano, check.ObservedAt)
		if err != nil || observed.UTC().Before(outer.Add(-maximumAge)) || observed.UTC().After(outer.Add(toleratedClockSkew)) {
			return false
		}
	}
	return true
}

func restoreRequiredChecksPass(checks []restoreValidationCheck, required []string) bool {
	if len(required) == 0 || len(checks) != len(required) {
		return false
	}
	results := make(map[string]string, len(checks))
	for _, check := range checks {
		if _, exists := results[check.CheckID]; exists {
			return false
		}
		results[check.CheckID] = check.Result
	}
	for _, id := range required {
		if results[id] != "pass" {
			return false
		}
	}
	return true
}

func (w *ServerRestoreWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || w == nil || operation.OperationType != "server.restore" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.restore operation and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parameters, err := restoreRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	server := stored.Record
	if len(server.InstanceRef) == 0 || (len(operation.Target.InstanceRef) > 0 && !equalJSON(operation.Target.InstanceRef, server.InstanceRef)) ||
		(len(parameters.TargetInstanceRef) > 0 && !equalJSON(parameters.TargetInstanceRef, server.InstanceRef)) {
		return store.ErrLogicalServerTargetMismatch
	}
	profile, err := w.profiles.ResolveServerRestore(ctx, server)
	if err != nil || validateServerRestoreProfile(server, profile) != nil {
		return errors.New("trusted restore profile is unavailable or invalid")
	}
	if restoreRequiresOverwriteApproval(server, profile) && !parameters.ReplaceExisting {
		return errors.New("restore target is not confirmed empty; explicit overwrite intent is required")
	}
	if err := validateServerRestoreRelease(w.schemas, server, profile); err != nil {
		return err
	}
	switch step.StepID {
	case restoreInspectInstanceStep:
		if !stoppedOrFailedLifecycle(server.LifecycleState) || !stepBindsRef(step.Input, "instance_ref", server.InstanceRef) {
			return errors.New("restore requires a stopped or failed server and exact provider inspection")
		}
	case restoreInspectRuntimeStep:
		if !stepSucceeded(operation, restoreInspectInstanceStep) || !stepSucceeded(operation, restoreCompatibilityStep) {
			return errors.New("runtime inspection requires successful provider and compatibility inspection")
		}
		provider, err := restoreProviderInspection(operation)
		if err != nil || !equalJSON(provider.InstanceRef, server.InstanceRef) || (provider.State != "active" && provider.State != "stopped") ||
			!observationIsFreshForStep(provider.ObservedAt, operationStep(operation, restoreInspectInstanceStep)) {
			return errors.New("runtime inspection requires the exact active or stopped provider instance")
		}
	case restoreCompatibilityStep:
		if !stepSucceeded(operation, restoreInspectInstanceStep) {
			return errors.New("compatibility check requires successful provider inspection")
		}
		provider, err := restoreProviderInspection(operation)
		if err != nil || !equalJSON(provider.InstanceRef, server.InstanceRef) || (provider.State != "active" && provider.State != "stopped") ||
			!observationIsFreshForStep(provider.ObservedAt, operationStep(operation, restoreInspectInstanceStep)) {
			return errors.New("restore compatibility check requires the exact provider instance")
		}
	case restoreApplyStep:
		// The engine checks dispatch preconditions before it records the restore
		// step as running. Until that durable transition, the lifecycle projector
		// correctly leaves a stopped or failed server in its existing state.
		if (!stoppedOrFailedLifecycle(server.LifecycleState) && server.LifecycleState != protocolstate.ServerRestoring) ||
			!stepSucceeded(operation, restoreCompatibilityStep) || !restoreCompatible(operation, w.schemas, profile, parameters.BackupID, server.EnvironmentID) ||
			!observationIsFreshForStep(stepFieldString(operationStep(operation, restoreCompatibilityStep).Output, "observed_at"), operationStep(operation, restoreCompatibilityStep)) {
			return errors.New("restore dispatch requires the exact backup to pass compatibility checks")
		}
		unit, err := restoreRuntimeInspection(operation, restoreInspectRuntimeStep)
		if err != nil || (unit.State != "stopped" && unit.State != "absent") || !equalJSON(unit.UnitRef, profile.UnitRef) ||
			!observationIsCurrent(unit.ObservedAt, w.assertionTTL, time.Now().UTC()) {
			return errors.New("restore dispatch requires the runtime to remain stopped")
		}
		backup, err := w.backups.ResolveServerRestoreBackup(ctx, server, profile.StorageProfileID, parameters.BackupID)
		if err != nil {
			return errors.New("exact verified restore backup is no longer available")
		}
		_, _, manifest, err := validateServerRestoreBackup(w.schemas, server, parameters.BackupID, profile, backup)
		if err != nil {
			return err
		}
		if err := validateRestoreStreamPlan(step, backup.Result, manifest, server, profile, operation); err != nil {
			return err
		}
		if parameters.ReplaceExisting {
			if parameters.ApprovalID == "" {
				return errors.New("replace_existing_data requires an approval_id")
			}
			if err := w.authorizer.AuthorizeServerRestore(ctx, operation.ActorID, parameters.ApprovalID, operation, server, parameters.BackupID); err != nil {
				return fmt.Errorf("revalidate exact server.restore approval: %w", err)
			}
		}
	case restoreInspectStep:
		if !stepSucceeded(operation, restoreApplyStep) || !restoreApplySucceeded(operation, w.schemas, parameters.BackupID, server) {
			return errors.New("restore inspection requires an exact completed restore call")
		}
	case restoreStartStep:
		if !parameters.StartAfterRestore || server.LifecycleState != protocolstate.ServerRestoring || !restoreInspectionSucceeded(operation, w.schemas, parameters.BackupID, server) {
			return errors.New("runtime start requires an explicitly requested and verified restore")
		}
		descriptor, err := w.adapters.Descriptor(server.GameAdapterID)
		if err != nil || verifyReadinessSetDeclared(descriptor, server.EnvironmentID, ServerStartProfile{GameReleaseID: profile.GameReleaseID,
			ReadinessCheckSetID: profile.ReadinessCheckSetID, RequiredChecks: profile.RequiredReadinessChecks}) != nil {
			return errors.New("runtime start requires the current trusted readiness policy")
		}
	case restoreVerifyRuntimeStep:
		start := operationStep(operation, restoreStartStep)
		var started struct {
			UnitRef json.RawMessage `json:"unit_ref"`
			Outcome string          `json:"outcome"`
			State   string          `json:"state"`
		}
		if start.Status != protocolstate.StepSucceeded || json.Unmarshal(start.Output, &started) != nil || started.Outcome != "completed" || started.State != "running" ||
			!equalJSON(started.UnitRef, profile.UnitRef) {
			return errors.New("runtime verification requires a completed start of the exact unit")
		}
	case restoreReadinessStep:
		unit, err := restoreRuntimeInspection(operation, restoreVerifyRuntimeStep)
		if err != nil || unit.State != "running" || !equalJSON(unit.UnitRef, profile.UnitRef) {
			return errors.New("readiness check requires the exact runtime to be confirmed running")
		}
	default:
		return fmt.Errorf("unsupported server.restore step %q", step.StepID)
	}
	return nil
}

func restoreRequest(operation store.OperationRecord, version string) (serverRestoreParameters, error) {
	var parameters serverRestoreParameters
	if operation.OperationType != "server.restore" || operation.ProtocolVersion != version || operation.RequestSnapshot.OperationType != "server.restore" ||
		json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters) != nil || parameters.BackupID == "" {
		return parameters, errors.New("persisted server.restore request is invalid")
	}
	return parameters, nil
}

type restoreProviderState struct {
	InstanceRef json.RawMessage `json:"instance_ref"`
	State       string          `json:"state"`
	ObservedAt  string          `json:"observed_at"`
}
type restoreUnitState struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

func stepFieldString(raw json.RawMessage, key string) string {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ""
	}
	var value string
	_ = json.Unmarshal(object[key], &value)
	return value
}

func restoreProviderInspection(operation store.OperationRecord) (restoreProviderState, error) {
	var output restoreProviderState
	step := operationStep(operation, restoreInspectInstanceStep)
	if step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("provider inspection is missing")
	}
	return output, nil
}
func restoreRuntimeInspection(operation store.OperationRecord, id string) (restoreUnitState, error) {
	var output restoreUnitState
	step := operationStep(operation, id)
	if step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("runtime inspection is missing")
	}
	return output, nil
}

func restoreCompatible(operation store.OperationRecord, schemas schemaRuntime, profile ServerRestoreProfile, backupID, environmentID string) bool {
	step := operationStep(operation, restoreCompatibilityStep)
	if step.Status != protocolstate.StepSucceeded || schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":game:compatibility-check-output", step.Output) != nil {
		return false
	}
	var planned struct {
		Manifest struct {
			BackupID         string `json:"backup_id"`
			EnvironmentID    string `json:"environment_id"`
			DataGenerationID string `json:"data_generation_id"`
			Game             struct {
				AdapterID string `json:"adapter_id"`
				Version   string `json:"version"`
			} `json:"game"`
		} `json:"source_manifest"`
		Release struct {
			GameAdapterID string `json:"game_adapter_id"`
			ReleaseID     string `json:"release_id"`
			GameVersion   string `json:"game_version"`
		} `json:"target_release"`
		Required []string `json:"required_check_ids"`
	}
	if json.Unmarshal(step.Input, &planned) != nil || planned.Manifest.BackupID != backupID || planned.Manifest.EnvironmentID != environmentID ||
		planned.Manifest.DataGenerationID == "" || planned.Manifest.Game.AdapterID == "" || planned.Manifest.Game.Version == "" ||
		planned.Release.GameAdapterID != profile.GameAdapterID || planned.Release.ReleaseID != profile.GameReleaseID || planned.Release.GameVersion != profile.GameVersion ||
		!sameStringSlice(planned.Required, profile.RequiredCompatibilityCheckIDs) {
		return false
	}
	var output struct {
		BackupID          string `json:"backup_id"`
		EnvironmentID     string `json:"environment_id"`
		DataGenerationID  string `json:"data_generation_id"`
		SourceAdapterID   string `json:"source_game_adapter_id"`
		SourceVersion     string `json:"source_game_version"`
		TargetAdapterID   string `json:"target_game_adapter_id"`
		TargetReleaseID   string `json:"target_release_id"`
		TargetGameVersion string `json:"target_game_version"`
		Status            string `json:"compatibility_status"`
		CheckResults      []struct {
			CheckID string `json:"check_id"`
			Version string `json:"check_version"`
			Result  string `json:"result"`
		} `json:"check_results"`
	}
	if json.Unmarshal(step.Output, &output) != nil || output.BackupID != backupID || output.EnvironmentID != environmentID ||
		output.DataGenerationID != planned.Manifest.DataGenerationID || output.SourceAdapterID != planned.Manifest.Game.AdapterID || output.SourceVersion != planned.Manifest.Game.Version || output.TargetAdapterID != profile.GameAdapterID ||
		output.TargetReleaseID != profile.GameReleaseID || output.TargetGameVersion != profile.GameVersion || output.Status != "compatible" || len(output.CheckResults) != len(profile.RequiredCompatibilityCheckIDs) {
		return false
	}
	results := make(map[string]string, len(output.CheckResults))
	for _, item := range output.CheckResults {
		if _, ok := results[item.CheckID]; ok || item.Version == "" {
			return false
		}
		results[item.CheckID] = item.Result
	}
	for _, id := range profile.RequiredCompatibilityCheckIDs {
		if results[id] != "compatible" {
			return false
		}
	}
	return true
}

func restoreApplySucceeded(operation store.OperationRecord, schemas schemaRuntime, backupID string, server store.LogicalServer) bool {
	step := operationStep(operation, restoreApplyStep)
	if step.Status != protocolstate.StepSucceeded || schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":game:restore-output", step.Output) != nil {
		return false
	}
	var output restoreGameResult
	if json.Unmarshal(step.Output, &output) != nil || output.BackupID != backupID || output.EnvironmentID != server.EnvironmentID || output.Outcome != "restored" ||
		output.TargetGenerationID != restoreDataGeneration(operation.OperationID) || !restoreTargetMatches(stepField(step.Input, "target"), server) ||
		!equalJSON(stepField(step.Input, "target"), output.Target) ||
		!validateRestoreExecutionRef(schemas, output.RestoreExecutionRef, server.GameAdapterID, server.EnvironmentID) {
		return false
	}
	if !observationIsFreshForStep(output.ObservedAt, step) || !restoreValidationChecksCurrentAt(output.ValidationChecks, output.ObservedAt, maxReadinessAssertionTTL) {
		return false
	}
	var source struct {
		OperationData struct {
			DataGenerationID string `json:"data_generation_id"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(stepField(step.Input, "source_backup"), &source) != nil || output.SourceGenerationID == "" || output.SourceGenerationID != source.OperationData.DataGenerationID {
		return false
	}
	return true
}

func restoreInspectionSucceeded(operation store.OperationRecord, schemas schemaRuntime, backupID string, server store.LogicalServer) bool {
	step := operationStep(operation, restoreInspectStep)
	if step.Status != protocolstate.StepSucceeded || schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":game:restore-output", step.Output) != nil {
		return false
	}
	var output restoreGameResult
	apply := operationStep(operation, restoreApplyStep)
	var applied restoreGameResult
	if json.Unmarshal(step.Output, &output) != nil || output.BackupID != backupID || output.EnvironmentID != server.EnvironmentID || output.Outcome != "restored" ||
		output.TargetGenerationID != restoreDataGeneration(operation.OperationID) || !restoreTargetMatches(stepField(apply.Input, "target"), server) ||
		!equalJSON(stepField(apply.Input, "target"), output.Target) ||
		!equalJSON(output.RestoreExecutionRef, stepField(apply.Output, "restore_execution_ref")) ||
		json.Unmarshal(apply.Output, &applied) != nil || output.SourceGenerationID == "" || output.SourceGenerationID != applied.SourceGenerationID {
		return false
	}
	if !observationIsFreshForStep(output.ObservedAt, step) || !restoreValidationChecksCurrentAt(output.ValidationChecks, output.ObservedAt, maxReadinessAssertionTTL) {
		return false
	}
	for _, item := range output.ValidationChecks {
		if item.Result != "pass" {
			return false
		}
	}
	return restoreRequiredChecksPass(output.ValidationChecks, restoreRequiredChecks(operation))
}

func restoreRequiredChecks(operation store.OperationRecord) []string {
	var input struct {
		Required []string `json:"required_check_ids"`
	}
	_ = json.Unmarshal(operationStep(operation, restoreApplyStep).ResolvedInput, &input)
	if len(input.Required) == 0 {
		_ = json.Unmarshal(operationStep(operation, restoreApplyStep).Input, &input)
	}
	return input.Required
}

func (w *ServerRestoreWorkflow) StreamTransfersForStep(ctx context.Context, operation store.OperationRecord, step store.OperationStep, deadline time.Time) ([]adapter.StreamTransfer, error) {
	if ctx == nil || w == nil || operation.OperationType != "server.restore" || step.StepID != restoreApplyStep {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	parameters, err := restoreRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return nil, err
	}
	profile, err := w.profiles.ResolveServerRestore(ctx, stored.Record)
	if err != nil || validateServerRestoreProfile(stored.Record, profile) != nil {
		return nil, errors.New("trusted restore profile is unavailable")
	}
	verified, err := w.backups.ResolveServerRestoreBackup(ctx, stored.Record, profile.StorageProfileID, parameters.BackupID)
	if err != nil {
		return nil, err
	}
	_, _, manifest, err := validateServerRestoreBackup(w.schemas, stored.Record, parameters.BackupID, profile, verified)
	if err != nil {
		return nil, err
	}
	if !restoreCompatible(operation, w.schemas, profile, parameters.BackupID, stored.Record.EnvironmentID) {
		return nil, errors.New("restore streams require a compatible exact backup")
	}
	if err := validateRestoreStreamPlan(step, verified.Result, manifest, stored.Record, profile, operation); err != nil {
		return nil, err
	}
	transfers := make([]adapter.StreamTransfer, 0, len(manifest.Objects))
	closePrepared := func() {
		for _, transfer := range transfers {
			_ = transfer.Source.Close()
		}
	}
	for _, object := range manifest.Objects {
		source, err := w.backups.OpenServerRestoreBackupObject(ctx, stored.Record, profile.StorageProfileID, object.ObjectRef)
		if err != nil || source == nil {
			closePrepared()
			return nil, errors.New("could not open exact verified backup object")
		}
		var nonce [32]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			_ = source.Close()
			closePrepared()
			return nil, errors.New("could not create one-use restore transfer")
		}
		expires := time.Now().UTC().Add(5 * time.Minute)
		if deadline.Before(expires) {
			expires = deadline.UTC()
		}
		if !expires.After(time.Now()) {
			_ = source.Close()
			closePrepared()
			return nil, errors.New("restore stream deadline expired")
		}
		maximum := object.SizeBytes
		if maximum == 0 {
			maximum = 1
		}
		verifiedSource := &provisionVerifiedSource{source: source, maximum: object.SizeBytes, expected: strings.ToLower(object.Checksum), digest: sha256.New()}
		transfers = append(transfers, adapter.StreamTransfer{Reference: capturestream.Reference{TransferID: hex.EncodeToString(nonce[:]), Direction: "controller_to_adapter", MaximumBytes: maximum, ExpiresAt: expires}, Source: verifiedSource, Verify: verifiedSource.Verify})
	}
	return transfers, nil
}

func validateRestoreStreamPlan(step store.OperationStep, verifiedResult json.RawMessage, manifest provisionBackupManifest, server store.LogicalServer, profile ServerRestoreProfile, operation store.OperationRecord) error {
	var planned struct {
		SourceBackup     json.RawMessage `json:"source_backup"`
		Target           json.RawMessage `json:"target"`
		TargetReleaseID  string          `json:"target_release_id"`
		TargetGeneration string          `json:"target_data_generation_id"`
		ObjectStreams    []struct {
			ObjectRef json.RawMessage `json:"object_ref"`
		} `json:"object_streams"`
	}
	if json.Unmarshal(effectiveStepInput(step), &planned) != nil || !equalJSON(planned.SourceBackup, verifiedResult) ||
		!restoreTargetMatches(planned.Target, server) || planned.TargetReleaseID != profile.GameReleaseID ||
		planned.TargetGeneration != restoreDataGeneration(operation.OperationID) || len(planned.ObjectStreams) != len(manifest.Objects) {
		return errors.New("restore streams no longer match the immutable validated plan")
	}
	for i, object := range manifest.Objects {
		if !equalJSON(planned.ObjectStreams[i].ObjectRef, object.ObjectRef) {
			return errors.New("restore stream object order or reference differs from the validated plan")
		}
	}
	return nil
}

func restoreTargetMatches(raw json.RawMessage, server store.LogicalServer) bool {
	var target struct {
		ServerID      string          `json:"server_id"`
		EnvironmentID string          `json:"environment_id"`
		InstanceRef   json.RawMessage `json:"instance_ref"`
	}
	return json.Unmarshal(raw, &target) == nil && target.ServerID == server.ServerID && target.EnvironmentID == server.EnvironmentID &&
		equalJSON(target.InstanceRef, server.InstanceRef)
}

func validateRestoreExecutionRef(schemas schemaRuntime, raw json.RawMessage, adapterID, environmentID string) bool {
	if !isObject(raw) || schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":common:external-resource-ref", raw) != nil {
		return false
	}
	ref, ok := decodeProvisionRef(raw)
	return ok && ref.AdapterID == adapterID && ref.EnvironmentID == environmentID && ref.ResourceKind == "restore-execution"
}

func (w *ServerRestoreWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil || w == nil || operation.OperationType != "server.restore" || step.StepID != restoreApplyStep || step.CapabilityName != "game.restore" {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	if len(step.ExternalOperationRef) == 0 {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	if !validateRestoreExecutionRef(w.schemas, step.ExternalOperationRef, step.AdapterID, operation.Target.EnvironmentID) {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	if outputRef := stepField(step.Output, "restore_execution_ref"); len(outputRef) > 0 &&
		(!validateRestoreExecutionRef(w.schemas, outputRef, step.AdapterID, operation.Target.EnvironmentID) || !equalJSON(outputRef, step.ExternalOperationRef)) {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	capability, err := exactCapability(w.adapters, step.AdapterID, adapter.CategoryGame, "game.restore.inspect",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":game:restore-inspect-input", "urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":game:restore-output", false)
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	input := step.ResolvedInput
	if len(input) == 0 {
		input = step.Input
	}
	var planned struct {
		SourceBackup     json.RawMessage `json:"source_backup"`
		Target           json.RawMessage `json:"target"`
		TargetGeneration string          `json:"target_data_generation_id"`
	}
	if json.Unmarshal(input, &planned) != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var source struct {
		OperationData struct {
			BackupID         string `json:"backup_id"`
			EnvironmentID    string `json:"environment_id"`
			DataGenerationID string `json:"data_generation_id"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(planned.SourceBackup, &source) != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil || !restoreTargetMatches(planned.Target, stored.Record) || planned.TargetGeneration != restoreDataGeneration(operation.OperationID) ||
		source.OperationData.BackupID == "" || source.OperationData.EnvironmentID != operation.Target.EnvironmentID || source.OperationData.DataGenerationID == "" {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	inspectInput, _ := json.Marshal(map[string]any{"backup_id": source.OperationData.BackupID, "target": planned.Target,
		"target_data_generation_id": planned.TargetGeneration, "restore_execution_ref": step.ExternalOperationRef})
	response, err := w.invoke(ctx, operation, step, capability, inspectInput, "restore-reconcile")
	if err != nil || response.Result.Outcome != adapter.OutcomeCompleted || w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var output restoreGameResult
	if json.Unmarshal(response.Result.Output, &output) != nil || output.BackupID != source.OperationData.BackupID || output.EnvironmentID != operation.Target.EnvironmentID ||
		output.SourceGenerationID != source.OperationData.DataGenerationID || output.TargetGenerationID != planned.TargetGeneration || !equalJSON(output.Target, planned.Target) ||
		!equalJSON(output.RestoreExecutionRef, step.ExternalOperationRef) || !observationIsCurrent(output.ObservedAt, w.assertionTTL, time.Now().UTC()) ||
		!restoreValidationChecksCurrentAt(output.ValidationChecks, output.ObservedAt, w.assertionTTL) {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	switch output.Outcome {
	case "restored":
		if !restoreRequiredChecksPass(output.ValidationChecks, restoreRequiredChecks(operation)) {
			return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
		}
		return Resolution{State: ResolutionCompleted, Output: response.Result.Output, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "rejected":
		return Resolution{State: ResolutionDefinitiveError, Error: protocolError("core.restore.rejected", "provider_failure", "The exact restore execution was confirmed rejected.", operation.OperationID, false, "operator_review", false), ExternalOperationRef: step.ExternalOperationRef}, nil
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

func (w *ServerRestoreWorkflow) invoke(ctx context.Context, operation store.OperationRecord, step store.OperationStep, capability adapter.Capability, input json.RawMessage, label string) (adapter.InvokeResponse, error) {
	deadline := time.Now().UTC().Add(15 * time.Second)
	requestID := startReconcileRequestID(operation.OperationID, step.StepID, step.Attempt)
	correlationID := operation.CorrelationID
	if correlationID == "" {
		correlationID = operation.OperationID
	}
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke", Params: adapter.InvokeParams{ProtocolVersion: operation.ProtocolVersion, Capability: capability.Name,
		RequestContext: adapter.RequestContext{RequestID: requestID, CorrelationID: correlationID, OperationID: operation.OperationID, StepID: step.StepID,
			ServerID: operation.Target.ServerID, EnvironmentID: operation.Target.EnvironmentID, Deadline: deadline}, Input: input}}
	callCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response, err := w.adapters.Invoke(callCtx, step.AdapterID, request)
	if err != nil || response.JSONRPC != "2.0" || response.ID != requestID {
		return adapter.InvokeResponse{}, errors.New(label + " adapter invocation failed")
	}
	return response, nil
}

func (w *ServerRestoreWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.restore" {
		return errors.New("server.restore completion requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parameters, err := restoreRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return err
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	profile, err := w.profiles.ResolveServerRestore(ctx, server.Record)
	if err != nil || validateServerRestoreProfile(server.Record, profile) != nil {
		return errors.New("trusted restore profile is unavailable")
	}
	if !restoreApplySucceeded(operation, w.schemas, parameters.BackupID, server.Record) || !restoreInspectionSucceeded(operation, w.schemas, parameters.BackupID, server.Record) {
		return errors.New("server.restore exact backup, target generation, or game validation postconditions were not confirmed")
	}
	if parameters.StartAfterRestore {
		start := operationStep(operation, restoreStartStep)
		verify, err := restoreRuntimeInspection(operation, restoreVerifyRuntimeStep)
		if start.Status != protocolstate.StepSucceeded || err != nil || verify.State != "running" || !equalJSON(verify.UnitRef, profile.UnitRef) {
			return errors.New("server.restore runtime start was not confirmed on the exact unit")
		}
		_, err = BuildReadinessAssertion(w.schemas, operation, server.Record, restoreReadinessStep, w.assertionTTL)
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *ServerRestoreWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	parameters, _ := restoreRequest(operation, w.schemas.ProtocolVersion())
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"backup_id": parameters.BackupID, "target_instance_ref": server.Record.InstanceRef,
		"data_generation_id": restoreDataGeneration(operation.OperationID), "restore_verified": true, "final_lifecycle_state": string(protocolstate.ServerStopped)}
	if parameters.StartAfterRestore {
		assertion, err := BuildReadinessAssertion(w.schemas, operation, server.Record, restoreReadinessStep, w.assertionTTL)
		if err != nil {
			return nil, err
		}
		data["final_lifecycle_state"] = string(protocolstate.ServerReady)
		data["readiness_assertion"] = json.RawMessage(assertion)
	}
	result := map[string]any{"outcome_summary": "The selected verified backup was restored and validated on the exact managed instance.",
		"operation_type": "server.restore", "affected_resources": []any{server.Record.InstanceRef}, "completed_steps": completedStepSummaries(operation),
		"failed_steps": []any{}, "warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": false, "operation_data": data}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":operations:server-restore-result", encoded); err != nil {
		return nil, fmt.Errorf("server.restore typed result is invalid: %w", err)
	}
	return encoded, nil
}

func (w *ServerRestoreWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	failed := []any{}
	if step.StepID != "" && len(failure) > 0 && step.Status == protocolstate.StepFailed {
		failed = append(failed, map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure})
	}
	affected := []any{}
	if len(operation.Target.InstanceRef) > 0 {
		affected = append(affected, operation.Target.InstanceRef)
	}
	unresolved := []any{}
	if step.StepID == "" {
		unresolved = append(unresolved, map[string]any{"condition_id": "server.restore.postcondition_unmet", "description": "The restore outcome or exact data-generation postconditions remain unresolved."})
	}
	result := map[string]any{"outcome_summary": "The selected backup was not proven restored to the exact target.", "operation_type": "server.restore",
		"affected_resources": affected, "completed_steps": completedStepSummaries(operation), "failed_steps": failed, "warnings": []string{},
		"unresolved_conditions": unresolved, "audit_event_ids": []string{}, "partial": operationStep(operation, restoreApplyStep).Attempt > 0}
	return json.Marshal(result)
}

func (w *ServerRestoreWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if operation.OperationType != "server.restore" || current.Record.ServerID != operation.Target.ServerID || current.Record.EnvironmentID != operation.Target.EnvironmentID ||
		(len(operation.Target.InstanceRef) > 0 && !equalJSON(operation.Target.InstanceRef, current.Record.InstanceRef)) {
		return store.LogicalServer{}, false, errors.New("server.restore lifecycle projection target mismatch")
	}
	next := current.Record
	apply := operationStep(operation, restoreApplyStep)
	if current.Record.LifecycleState == protocolstate.ServerStopped || current.Record.LifecycleState == protocolstate.ServerFailed {
		if apply.Status != protocolstate.StepPending || apply.Attempt > 0 {
			next.LifecycleState = protocolstate.ServerRestoring
			next.DesiredState = "stopped"
			return next, true, nil
		}
		return current.Record, false, nil
	}
	if current.Record.LifecycleState != protocolstate.ServerRestoring && current.Record.LifecycleState != protocolstate.ServerStarting && current.Record.LifecycleState != protocolstate.ServerValidating {
		return store.LogicalServer{}, false, fmt.Errorf("server.restore cannot project lifecycle from %q", current.Record.LifecycleState)
	}
	if operation.Status == protocolstate.OperationSucceeded && restoreApplySucceeded(operation, w.schemas, restoreBackupID(operation), current.Record) &&
		restoreInspectionSucceeded(operation, w.schemas, restoreBackupID(operation), current.Record) {
		next.DataGenerationID = restoreDataGeneration(operation.OperationID)
		next.DataGenerationStatus = "known"
	}
	if apply.Attempt > 0 && (!restoreApplySucceeded(operation, w.schemas, restoreBackupID(operation), current.Record) || !restoreInspectionSucceeded(operation, w.schemas, restoreBackupID(operation), current.Record)) {
		next.DataGenerationID = ""
		next.DataGenerationStatus = "unknown"
	}
	if operation.Status == protocolstate.OperationFailed {
		next.LifecycleState = protocolstate.ServerFailed
		return next, true, nil
	}
	parameters, err := restoreRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return store.LogicalServer{}, false, err
	}
	if parameters.StartAfterRestore {
		if operationStep(operation, restoreReadinessStep).Status != protocolstate.StepPending {
			next.LifecycleState = protocolstate.ServerValidating
			next.DesiredState = "running"
			return next, true, nil
		}
		if operationStep(operation, restoreStartStep).Status != protocolstate.StepPending {
			next.LifecycleState = protocolstate.ServerStarting
			next.DesiredState = "running"
			return next, true, nil
		}
	}
	if operation.Status == protocolstate.OperationSucceeded {
		if !hasValidRestoreResult(operation, current.Record, w.schemas) {
			return store.LogicalServer{}, false, errors.New("terminal server.restore result does not match exact target and data generation")
		}
		if parameters.StartAfterRestore {
			next.LifecycleState = protocolstate.ServerReady
			next.DesiredState = "running"
		} else {
			next.LifecycleState = protocolstate.ServerStopped
			next.DesiredState = "stopped"
		}
		return next, true, nil
	}
	return next, next.DataGenerationID != current.Record.DataGenerationID || next.DataGenerationStatus != current.Record.DataGenerationStatus, nil
}

func restoreBackupID(operation store.OperationRecord) string {
	parameters, _ := restoreRequest(operation, operation.ProtocolVersion)
	return parameters.BackupID
}

func hasValidRestoreResult(operation store.OperationRecord, server store.LogicalServer, schemas schemaRuntime) bool {
	var result struct {
		OperationType string `json:"operation_type"`
		OperationData struct {
			BackupID   string          `json:"backup_id"`
			Instance   json.RawMessage `json:"target_instance_ref"`
			Generation string          `json:"data_generation_id"`
			Verified   bool            `json:"restore_verified"`
			Final      string          `json:"final_lifecycle_state"`
			Assertion  json.RawMessage `json:"readiness_assertion"`
		} `json:"operation_data"`
	}
	parameters, err := restoreRequest(operation, schemas.ProtocolVersion())
	if err != nil || json.Unmarshal(operation.Result, &result) != nil || result.OperationType != "server.restore" || result.OperationData.BackupID != parameters.BackupID ||
		!equalJSON(result.OperationData.Instance, server.InstanceRef) || result.OperationData.Generation != restoreDataGeneration(operation.OperationID) || !result.OperationData.Verified {
		return false
	}
	if parameters.StartAfterRestore && (result.OperationData.Final != string(protocolstate.ServerReady) || !isObject(result.OperationData.Assertion) ||
		schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":lifecycle:readiness-assertion", result.OperationData.Assertion) != nil) {
		return false
	}
	if !parameters.StartAfterRestore && result.OperationData.Final != string(protocolstate.ServerStopped) {
		return false
	}
	return schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-restore-result", operation.Result) == nil
}

var _ Definition = (*ServerRestoreWorkflow)(nil)
var _ LifecycleProjector = (*ServerRestoreWorkflow)(nil)
var _ StepStreamTransfers = (*ServerRestoreWorkflow)(nil)
