package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type provisionBackupResult struct {
	OperationType string `json:"operation_type"`
	Partial bool `json:"partial"`
	FailedSteps []json.RawMessage `json:"failed_steps"`
	UnresolvedConditions []json.RawMessage `json:"unresolved_conditions"`
	OperationData struct {
		BackupID       string          `json:"backup_id"`
		EnvironmentID  string          `json:"environment_id"`
		DataGeneration string          `json:"data_generation_id"`
		Manifest       json.RawMessage `json:"manifest"`
		VerifiedAt     string          `json:"verified_at"`
	} `json:"operation_data"`
}

type provisionBackupManifest struct {
	BackupID       string `json:"backup_id"`
	ServerID       string `json:"server_id"`
	EnvironmentID  string `json:"environment_id"`
	DataGeneration string `json:"data_generation_id"`
	Game           struct {
		AdapterID string `json:"adapter_id"`
		Version   string `json:"version"`
	} `json:"game"`
	Objects []struct {
		ObjectRef         json.RawMessage `json:"object_ref"`
		SizeBytes         int64           `json:"size_bytes"`
		ChecksumAlgorithm string          `json:"checksum_algorithm"`
		Checksum          string          `json:"checksum"`
	} `json:"objects"`
}

func validateProvisionRestoreProfile(profile ServerProvisionProfile) error {
	if profile.BackupStorageProfileID == "" || profile.BackupStorageAdapterID == "" ||
		!isObject(profile.GameReleaseDescription) || len(profile.RequiredCompatibilityCheckIDs) == 0 || len(profile.RequiredRestoreCheckIDs) == 0 {
		return errors.New("trusted provision profile must pin backup storage, target release metadata, and required compatibility and restore checks")
	}
	return nil
}

func (w *ServerProvisionWorkflow) validateVerifiedProvisionBackup(server store.LogicalServer, backupID string, profile ServerProvisionProfile, verified VerifiedProvisionBackup) (json.RawMessage, json.RawMessage, error) {
	resultSchema := "urn:gameserver-controller:" + w.schemas.ProtocolVersion() + ":operations:server-backup-result"
	if !verified.IntegrityVerified || w.schemas.ValidateJSON(resultSchema, verified.Result) != nil {
		return nil, nil, errors.New("selected backup has no valid controller-verified server.backup result")
	}
	var result provisionBackupResult
	if json.Unmarshal(verified.Result, &result) != nil || result.OperationType != "server.backup" || result.Partial ||
		len(result.FailedSteps) != 0 || len(result.UnresolvedConditions) != 0 ||
		result.OperationData.BackupID != backupID || result.OperationData.EnvironmentID != server.EnvironmentID ||
		result.OperationData.DataGeneration == "" || result.OperationData.Manifest == nil {
		return nil, nil, errors.New("selected backup result does not bind the requested backup, environment, and source generation")
	}
	if _, err := time.Parse(time.RFC3339Nano, result.OperationData.VerifiedAt); err != nil {
		return nil, nil, errors.New("selected backup result has an invalid verification time")
	}
	var manifest provisionBackupManifest
	manifestSchema := "urn:gameserver-controller:" + w.schemas.ProtocolVersion() + ":storage:backup-manifest"
	if w.schemas.ValidateJSON(manifestSchema, result.OperationData.Manifest) != nil || json.Unmarshal(result.OperationData.Manifest, &manifest) != nil ||
		manifest.BackupID != backupID || manifest.ServerID != server.ServerID || manifest.EnvironmentID != server.EnvironmentID ||
		manifest.DataGeneration != result.OperationData.DataGeneration || len(manifest.Objects) == 0 || len(manifest.Objects) > 64 {
		return nil, nil, errors.New("selected backup manifest does not bind the exact server, environment, backup, and source generation")
	}
	objectSchema := "urn:gameserver-controller:" + w.schemas.ProtocolVersion() + ":storage:storage-object-reference"
	for _, object := range manifest.Objects {
		var ref struct {
			AdapterID        string `json:"adapter_id"`
			StorageProfileID string `json:"storage_profile_id"`
			EnvironmentID    string `json:"environment_id"`
			VersionID        string `json:"version_id"`
		}
		if w.schemas.ValidateJSON(objectSchema, object.ObjectRef) != nil || json.Unmarshal(object.ObjectRef, &ref) != nil ||
			ref.AdapterID != profile.BackupStorageAdapterID || ref.StorageProfileID != profile.BackupStorageProfileID ||
			ref.EnvironmentID != server.EnvironmentID || object.SizeBytes < 0 || object.ChecksumAlgorithm != "sha256" ||
			len(object.Checksum) != sha256.Size*2 || strings.TrimSpace(object.Checksum) != object.Checksum {
			return nil, nil, errors.New("selected backup contains an invalid or cross-profile object reference")
		}
		if ref.VersionID == "" {
			return nil, nil, errors.New("selected backup object lacks an immutable version reference")
		}
		if _, err := hex.DecodeString(object.Checksum); err != nil {
			return nil, nil, errors.New("selected backup object checksum is invalid")
		}
	}
	return append(json.RawMessage(nil), verified.Result...), append(json.RawMessage(nil), result.OperationData.Manifest...), nil
}

func provisionObjectStreams(rawManifest json.RawMessage) []map[string]any {
	var manifest provisionBackupManifest
	if json.Unmarshal(rawManifest, &manifest) != nil {
		return nil
	}
	objects := make([]map[string]any, len(manifest.Objects))
	for i, object := range manifest.Objects {
		objects[i] = map[string]any{"object_ref": object.ObjectRef}
	}
	return objects
}

func (w *ServerProvisionWorkflow) StreamTransfersForStep(ctx context.Context, operation store.OperationRecord, step store.OperationStep, deadline time.Time) ([]adapter.StreamTransfer, error) {
	if ctx == nil || operation.OperationType != "server.provision" || step.StepID != provisionRestoreStep || w.backups == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var input struct {
		SourceBackup json.RawMessage `json:"source_backup"`
	}
	if json.Unmarshal(effectiveStepInput(step), &input) != nil || !isObject(input.SourceBackup) {
		return nil, errors.New("persisted restore input has no verified source backup")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	profile, err := w.profiles.ResolveServerProvision(ctx, stored.Record, operation.RequestSnapshot.Parameters)
	if err != nil || validateProvisionRestoreProfile(profile) != nil {
		return nil, errors.New("trusted restore storage profile is unavailable")
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil || request.RestoreBackup == "" {
		return nil, errors.New("persisted restore request is invalid")
	}
	backupResult, manifestRaw, err := w.validateVerifiedProvisionBackup(stored.Record, request.RestoreBackup, profile,
		VerifiedProvisionBackup{Result: input.SourceBackup, IntegrityVerified: true})
	if err != nil || !equalJSON(backupResult, input.SourceBackup) {
		return nil, errors.New("persisted restore backup no longer satisfies its exact trusted profile")
	}
	created, err := provisionCreateStepOutput(operation)
	if err != nil || !provisionCompatibilitySucceeded(operation, w.schemas, profile) ||
		!stepBindsRef(stepField(effectiveStepInput(step), "target"), "instance_ref", created.InstanceRef) ||
		!stepSucceeded(operation, provisionRestoreStep) && step.Status != protocolstate.StepRunning {
		return nil, errors.New("restore streams require exact compatible backup and installed target bindings")
	}
	var manifest provisionBackupManifest
	if w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":storage:backup-manifest", manifestRaw) != nil ||
		json.Unmarshal(manifestRaw, &manifest) != nil {
		return nil, errors.New("persisted restore manifest is invalid")
	}
	transfers := make([]adapter.StreamTransfer, 0, len(manifest.Objects))
	closePrepared := func() {
		for _, transfer := range transfers {
			_ = transfer.Source.Close()
		}
	}
	for _, object := range manifest.Objects {
		if err := ctx.Err(); err != nil {
			closePrepared()
			return nil, err
		}
		source, err := w.backups.OpenServerProvisionBackupObject(ctx, stored.Record, profile.BackupStorageProfileID, object.ObjectRef)
		if err != nil || source == nil {
			closePrepared()
			return nil, errors.New("controller could not open an exact verified backup object")
		}
		var idBytes [32]byte
		if _, err := rand.Read(idBytes[:]); err != nil {
			_ = source.Close()
			closePrepared()
			return nil, errors.New("controller could not prepare a one-use restore stream")
		}
		transferID := hex.EncodeToString(idBytes[:])
		expires := time.Now().UTC().Add(5 * time.Minute)
		if deadline.Before(expires) {
			expires = deadline.UTC()
		}
		if !expires.After(time.Now()) {
			_ = source.Close()
			closePrepared()
			return nil, errors.New("restore stream deadline has expired")
		}
		maximumBytes := object.SizeBytes
		if maximumBytes == 0 {
			maximumBytes = 1
		}
		verifiedSource := &provisionVerifiedSource{source: source, maximum: object.SizeBytes, expected: strings.ToLower(object.Checksum), digest: sha256.New()}
		transfers = append(transfers, adapter.StreamTransfer{
			Reference: capturestream.Reference{TransferID: transferID, Direction: "controller_to_adapter", MaximumBytes: maximumBytes, ExpiresAt: expires},
			Source:    verifiedSource,
			Verify:    verifiedSource.Verify,
		})
	}
	return transfers, nil
}

type provisionVerifiedSource struct {
	source   io.ReadCloser
	maximum  int64
	expected string
	digest   hash.Hash
	read     int64
	eof      bool
	failure  error
}

func (r *provisionVerifiedSource) Read(p []byte) (int, error) {
	if r.failure != nil {
		return 0, r.failure
	}
	if r.read > r.maximum {
		return 0, errors.New("backup object exceeds manifest size")
	}
	remaining := r.maximum + 1 - r.read
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.source.Read(p)
	if n > 0 {
		r.read += int64(n)
		_, _ = r.digest.Write(p[:n])
		if r.read > r.maximum {
			r.failure = errors.New("backup object exceeds manifest size")
			return n, r.failure
		}
	}
	if err == io.EOF {
		r.eof = true
	}
	if err != nil && err != io.EOF {
		r.failure = errors.New("backup object stream read failed")
	}
	return n, err
}

func (r *provisionVerifiedSource) Close() error { return r.source.Close() }

func (r *provisionVerifiedSource) Verify() error {
	if r.failure != nil || !r.eof || r.read != r.maximum || hex.EncodeToString(r.digest.Sum(nil)) != r.expected {
		return errors.New("backup object did not match its verified manifest")
	}
	return nil
}

func provisionRestoreSucceeded(operation store.OperationRecord) bool {
	if !stepSucceeded(operation, provisionRestoreStep) || !stepSucceeded(operation, provisionRestoreInspectStep) {
		return false
	}
	var initial, inspected struct {
		BackupID               string          `json:"backup_id"`
		EnvironmentID          string          `json:"environment_id"`
		SourceDataGenerationID string          `json:"source_data_generation_id"`
		Target                 json.RawMessage `json:"target"`
		TargetDataGenerationID string          `json:"target_data_generation_id"`
		RestoreExecutionRef    json.RawMessage `json:"restore_execution_ref"`
		Outcome                string          `json:"outcome"`
		ValidationChecks       []struct {
			CheckID string `json:"check_id"`
			Result  string `json:"result"`
		} `json:"validation_checks"`
	}
	call := operationStep(operation, provisionRestoreStep)
	inspect := operationStep(operation, provisionRestoreInspectStep)
	created, err := provisionCreateStepOutput(operation)
	if err != nil || json.Unmarshal(call.Output, &initial) != nil || json.Unmarshal(inspect.Output, &inspected) != nil {
		return false
	}
	var expected struct {
		TargetDataGenerationID string `json:"target_data_generation_id"`
		SourceBackup           struct {
			OperationData struct {
				BackupID         string `json:"backup_id"`
				EnvironmentID    string `json:"environment_id"`
				DataGenerationID string `json:"data_generation_id"`
			} `json:"operation_data"`
		} `json:"source_backup"`
		RequiredCheckIDs []string `json:"required_check_ids"`
	}
	if json.Unmarshal(call.ResolvedInput, &expected) != nil || (initial.Outcome != "restored" && initial.Outcome != "outcome_unknown") || inspected.Outcome != "restored" ||
		initial.BackupID != expected.SourceBackup.OperationData.BackupID || inspected.BackupID != initial.BackupID ||
		initial.EnvironmentID != operation.Target.EnvironmentID || inspected.EnvironmentID != initial.EnvironmentID ||
		initial.SourceDataGenerationID != expected.SourceBackup.OperationData.DataGenerationID || inspected.SourceDataGenerationID != initial.SourceDataGenerationID ||
		initial.TargetDataGenerationID != expected.TargetDataGenerationID || inspected.TargetDataGenerationID != initial.TargetDataGenerationID ||
		initial.TargetDataGenerationID != provisionDataGeneration(operation.OperationID) ||
		!stepBindsRef(stepField(call.ResolvedInput, "target"), "instance_ref", created.InstanceRef) ||
		!equalJSON(initial.Target, stepField(call.ResolvedInput, "target")) || !equalJSON(inspected.Target, initial.Target) ||
		!equalJSON(initial.RestoreExecutionRef, inspected.RestoreExecutionRef) || !sameRequiredRestoreChecks(inspected.ValidationChecks, expected.RequiredCheckIDs) {
		return false
	}
	return true
}

func sameRequiredRestoreChecks(checks []struct {
	CheckID string `json:"check_id"`
	Result  string `json:"result"`
}, required []string) bool {
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

func provisionCompatibilitySucceeded(operation store.OperationRecord, schemas schemaRuntime, profile ServerProvisionProfile) bool {
	step := operationStep(operation, provisionCompatibilityStep)
	if step.Status != protocolstate.StepSucceeded || schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":game:compatibility-check-output", step.Output) != nil {
		return false
	}
	var output struct {
		BackupID          string `json:"backup_id"`
		EnvironmentID     string `json:"environment_id"`
		DataGenerationID  string `json:"data_generation_id"`
		SourceAdapterID   string `json:"source_game_adapter_id"`
		SourceGameVersion string `json:"source_game_version"`
		TargetAdapterID   string `json:"target_game_adapter_id"`
		TargetReleaseID   string `json:"target_release_id"`
		TargetGameVersion string `json:"target_game_version"`
		Status            string `json:"compatibility_status"`
		CheckResults      []struct {
			CheckID string `json:"check_id"`
			Result  string `json:"result"`
		} `json:"check_results"`
	}
	var input struct {
		SourceManifest struct {
			BackupID         string `json:"backup_id"`
			EnvironmentID    string `json:"environment_id"`
			DataGenerationID string `json:"data_generation_id"`
			Game             struct {
				AdapterID string `json:"adapter_id"`
				Version   string `json:"version"`
			} `json:"game"`
		} `json:"source_manifest"`
		TargetRelease struct {
			GameAdapterID string `json:"game_adapter_id"`
			ReleaseID     string `json:"release_id"`
			GameVersion   string `json:"game_version"`
		} `json:"target_release"`
	}
	if json.Unmarshal(step.Output, &output) != nil || json.Unmarshal(step.Input, &input) != nil || output.Status != "compatible" ||
		output.BackupID != input.SourceManifest.BackupID || output.EnvironmentID != operation.Target.EnvironmentID ||
		output.DataGenerationID != input.SourceManifest.DataGenerationID || output.SourceAdapterID != input.SourceManifest.Game.AdapterID ||
		output.SourceGameVersion != input.SourceManifest.Game.Version || output.TargetAdapterID != input.TargetRelease.GameAdapterID ||
		output.TargetReleaseID != profile.GameReleaseID || output.TargetReleaseID != input.TargetRelease.ReleaseID ||
		output.TargetGameVersion != input.TargetRelease.GameVersion || len(output.CheckResults) != len(profile.RequiredCompatibilityCheckIDs) {
		return false
	}
	results := make(map[string]string, len(output.CheckResults))
	for _, check := range output.CheckResults {
		if _, exists := results[check.CheckID]; exists {
			return false
		}
		results[check.CheckID] = check.Result
	}
	for _, id := range profile.RequiredCompatibilityCheckIDs {
		if results[id] != "compatible" {
			return false
		}
	}
	return true
}

func (w *ServerProvisionWorkflow) reconcileRestore(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if step.CapabilityName != "game.restore" || step.AdapterID == "" {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	created, err := provisionCreateStepOutput(operation)
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var input struct {
		SourceBackup struct {
			OperationData struct {
				BackupID string `json:"backup_id"`
				EnvironmentID string `json:"environment_id"`
				DataGenerationID string `json:"data_generation_id"`
			} `json:"operation_data"`
		} `json:"source_backup"`
		Target           json.RawMessage `json:"target"`
		TargetGeneration string          `json:"target_data_generation_id"`
		RequiredCheckIDs []string        `json:"required_check_ids"`
	}
	if json.Unmarshal(step.ResolvedInput, &input) != nil || input.SourceBackup.OperationData.BackupID == "" ||
		!stepBindsRef(input.Target, "instance_ref", created.InstanceRef) {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	executionRef := step.ExternalOperationRef
	if len(step.Output) > 0 {
		if fromOutput := stepField(step.Output, "restore_execution_ref"); isObject(fromOutput) {
			executionRef = fromOutput
		}
	}
	if len(executionRef) == 0 {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	ref, ok := decodeProvisionRef(executionRef)
	if !ok || ref.AdapterID != step.AdapterID || ref.EnvironmentID != operation.Target.EnvironmentID || ref.ResourceKind != "restore-execution" {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	capability, err := exactCapability(w.adapters, step.AdapterID, adapter.CategoryGame, "game.restore.inspect",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":game:restore-inspect-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":game:restore-output", false)
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	inspectInput, _ := json.Marshal(map[string]any{"backup_id": input.SourceBackup.OperationData.BackupID, "target": input.Target,
		"target_data_generation_id": input.TargetGeneration, "restore_execution_ref": executionRef})
	response, err := w.invokeProvision(ctx, operation, step, capability, inspectInput)
	if err != nil || response.Result.Outcome != adapter.OutcomeCompleted || w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var output struct {
		EnvironmentID          string          `json:"environment_id"`
		SourceDataGenerationID string          `json:"source_data_generation_id"`
		Outcome                string          `json:"outcome"`
		BackupID               string          `json:"backup_id"`
		Target                 json.RawMessage `json:"target"`
		TargetDataGenerationID string          `json:"target_data_generation_id"`
		RestoreExecutionRef    json.RawMessage `json:"restore_execution_ref"`
		ValidationChecks       []struct {
			CheckID string `json:"check_id"`
			Result  string `json:"result"`
		} `json:"validation_checks"`
	}
	if json.Unmarshal(response.Result.Output, &output) != nil || output.BackupID != input.SourceBackup.OperationData.BackupID ||
		output.EnvironmentID != operation.Target.EnvironmentID || output.EnvironmentID != input.SourceBackup.OperationData.EnvironmentID ||
		output.SourceDataGenerationID != input.SourceBackup.OperationData.DataGenerationID ||
		output.TargetDataGenerationID != input.TargetGeneration || !equalJSON(output.Target, input.Target) || !equalJSON(output.RestoreExecutionRef, executionRef) {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	switch output.Outcome {
	case "restored":
		if !sameRequiredRestoreChecks(output.ValidationChecks, input.RequiredCheckIDs) {
			return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: executionRef}, nil
		}
		return Resolution{State: ResolutionCompleted, Output: response.Result.Output, ExternalOperationRef: executionRef}, nil
	case "rejected":
		return Resolution{State: ResolutionDefinitiveError, Error: protocolError("core.restore.rejected", "provider_failure", "The exact restore execution was confirmed rejected.", operation.OperationID, false, "operator_review", false), ExternalOperationRef: executionRef}, nil
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: executionRef}, nil
	}
}
