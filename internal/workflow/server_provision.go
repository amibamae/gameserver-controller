package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	provisionCreateStep         = "create-instance"
	provisionWaitStep           = "wait-instance-active"
	provisionBootstrapStep      = "deliver-bootstrap"
	provisionBootstrapInspect   = "inspect-bootstrap"
	provisionInstallStep        = "install-game"
	provisionCompatibilityStep  = "check-restore-compatibility"
	provisionRestoreStep        = "restore-backup"
	provisionRestoreInspectStep = "inspect-restore"
	provisionCreateUnitStep     = "create-unit"
	provisionStartUnitStep      = "start-unit"
	provisionInspectUnitStep    = "inspect-unit"
	provisionReadinessStep      = "check-readiness"
)

// ServerProvisionProfile contains immutable deployment choices resolved from
// controller-owned catalogs. It is deliberately separate from request fields:
// a caller may select from a profile, but cannot supply adapter step inputs.
type ServerProvisionProfile struct {
	Provider                       string
	CapacityGroup                  string
	CapacityLimit                  int
	CapacityGroupLimit             int
	RegionID                       string
	PlanID                         string
	OS                             json.RawMessage
	Bootstrap                      json.RawMessage
	GameReleaseID                  string
	ArtifactURI                    string
	ArtifactChecksum               json.RawMessage
	UnitRef                        json.RawMessage
	ReadinessCheckSetID            string
	RequiredChecks                 []ReadinessRequirement
	DataGenerationID               string
	BackupStorageProfileID         string
	BackupStorageAdapterID         string
	BackupStorageVersioningEnabled bool
	GameReleaseDescription         json.RawMessage
	RequiredCompatibilityCheckIDs  []string
	RequiredRestoreCheckIDs        []string
}

// ServerProvisionProfileResolver resolves an allow-listed deployment using
// the persisted server definition and schema-validated request. Implementations
// must return controller-owned immutable data, not copy the request into a plan.
type ServerProvisionProfileResolver interface {
	ResolveServerProvision(context.Context, store.LogicalServer, json.RawMessage) (ServerProvisionProfile, error)
}

// ServerProvisionBackupResolver supplies only controller-verified backup
// records and exact manifest-object streams from an allow-listed storage profile.
type ServerProvisionBackupResolver interface {
	ResolveServerProvisionBackup(context.Context, store.LogicalServer, string, string) (VerifiedProvisionBackup, error)
	OpenServerProvisionBackupObject(context.Context, store.LogicalServer, string, json.RawMessage) (io.ReadCloser, error)
}

type VerifiedProvisionBackup struct {
	Result            json.RawMessage
	IntegrityVerified bool
}

// ProvisionCapacityStore is implemented by the durable store. Capacity is
// reserved and create is claimed immediately before the provider side effect.
type ProvisionCapacityStore interface {
	ReserveCapacity(store.CapacityReservation, int, int) (store.CapacityReservation, bool, error)
	MarkCapacityCreateSent(string) (store.CapacityReservation, error)
	ObserveCapacity(string, store.CapacityProviderObservation) (store.CapacityReservation, error)
	GetCapacityReservation(string) (store.CapacityReservation, error)
	RecordCapacityReady(string, string, string, time.Time) (store.CapacityReservation, error)
}

type ServerProvisionWorkflow struct {
	servers      store.LifecycleOperationStore
	capacity     ProvisionCapacityStore
	adapters     adapterRuntime
	schemas      schemaRuntime
	profiles     ServerProvisionProfileResolver
	backups      ServerProvisionBackupResolver
	assertionTTL time.Duration
}

func NewServerProvisionWorkflow(servers store.LifecycleOperationStore, capacity ProvisionCapacityStore,
	adapters adapterRuntime, schemas schemaRuntime, profiles ServerProvisionProfileResolver, assertionTTL time.Duration, backupResolvers ...ServerProvisionBackupResolver) (*ServerProvisionWorkflow, error) {
	if servers == nil || capacity == nil || adapters == nil || schemas == nil || profiles == nil {
		return nil, errors.New("lifecycle store, capacity store, adapter registry, schemas, and trusted provision-profile resolver are required")
	}
	if assertionTTL <= 0 || assertionTTL > maxReadinessAssertionTTL {
		return nil, errors.New("readiness assertion TTL must be in the range 1ns..15m")
	}
	if len(backupResolvers) > 1 {
		return nil, errors.New("at most one trusted server provision backup resolver may be configured")
	}
	var backups ServerProvisionBackupResolver
	if len(backupResolvers) == 1 {
		backups = backupResolvers[0]
	}
	return &ServerProvisionWorkflow{servers: servers, capacity: capacity, adapters: adapters, schemas: schemas, profiles: profiles, backups: backups, assertionTTL: assertionTTL}, nil
}

type provisionRequest struct {
	ServerID       string          `json:"server_id"`
	EnvironmentID  string          `json:"environment_id"`
	HostingAdapter string          `json:"hosting_adapter_id"`
	RegionID       string          `json:"region_id"`
	PlanID         string          `json:"plan_id"`
	OS             json.RawMessage `json:"os"`
	Bootstrap      json.RawMessage `json:"bootstrap"`
	Game           struct {
		AdapterID string `json:"adapter_id"`
		Release   string `json:"release_ref"`
	} `json:"game"`
	Execution struct {
		AdapterID string `json:"adapter_id"`
		Profile   string `json:"profile"`
	} `json:"execution"`
	SecretRefs     []store.SecretReference `json:"secret_refs,omitempty"`
	NetworkProfile string                  `json:"network_profile,omitempty"`
	FirewallPolicy string                  `json:"firewall_policy_ref,omitempty"`
	SSHKeyRefs     []string                `json:"ssh_key_refs,omitempty"`
	Labels         map[string]string       `json:"labels,omitempty"`
	RestoreBackup  string                  `json:"restore_backup_id,omitempty"`
}

type provisionCreateOutput struct {
	InstanceRef          json.RawMessage `json:"instance_ref"`
	State                string          `json:"state"`
	ObservedAt           string          `json:"observed_at"`
	ProviderOperationRef json.RawMessage `json:"provider_operation_ref,omitempty"`
}

type provisionWaitOutput struct {
	InstanceRef          json.RawMessage `json:"instance_ref"`
	ProviderOperationRef json.RawMessage `json:"provider_operation_ref,omitempty"`
	OperationStatus      string          `json:"operation_status"`
	InstanceState        string          `json:"instance_state"`
	ObservedAt           string          `json:"observed_at"`
}

type provisionBootstrapOutput struct {
	InstanceRef           json.RawMessage `json:"instance_ref"`
	BootstrapExecutionRef json.RawMessage `json:"bootstrap_execution_ref"`
	Revision              string          `json:"revision"`
	Digest                string          `json:"digest"`
	State                 string          `json:"state"`
	ObservedAt            string          `json:"observed_at"`
}

type provisionBootstrapInspection struct {
	InstanceRef           json.RawMessage `json:"instance_ref"`
	BootstrapExecutionRef json.RawMessage `json:"bootstrap_execution_ref"`
	Revision              string          `json:"revision"`
	Digest                string          `json:"digest"`
	State                 string          `json:"state"`
	ObservedAt            string          `json:"observed_at"`
}

type provisionUnitInspection struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

// BuildPlan resolves and pins all adapter identities, capability versions,
// inputs, and cross-step resource bindings before any provider call.
func (w *ServerProvisionWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if ctx == nil || w == nil || w.servers == nil || w.capacity == nil || w.adapters == nil || w.schemas == nil || w.profiles == nil {
		return nil, errors.New("initialized server.provision workflow and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if operation.OperationType != "server.provision" || operation.ProtocolVersion != w.schemas.ProtocolVersion() ||
		operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" ||
		operation.RequestSnapshot.OperationType != "server.provision" || !isObject(operation.RequestSnapshot.Parameters) {
		return nil, errors.New("server.provision operation and loaded protocol version are required")
	}
	var request provisionRequest
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &request); err != nil {
		return nil, errors.New("decode server.provision parameters")
	}
	if request.ServerID != operation.Target.ServerID || request.EnvironmentID != operation.Target.EnvironmentID {
		return nil, errors.New("server.provision request identity does not match its operation target")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, fmt.Errorf("load logical server for provision plan: %w", err)
	}
	server := stored.Record
	if server.LifecycleState != protocolstate.ServerUnprovisioned || len(server.InstanceRef) != 0 {
		return nil, errors.New("server.provision requires an unprovisioned logical server without an attached instance")
	}
	if server.GameAdapterID != request.Game.AdapterID || server.ExecutionAdapterID != request.Execution.AdapterID ||
		server.ExecutionProfile != request.Execution.Profile {
		return nil, errors.New("server.provision request does not match the persisted game and execution definition")
	}
	profile, err := w.profiles.ResolveServerProvision(ctx, server, append(json.RawMessage(nil), operation.RequestSnapshot.Parameters...))
	if err != nil {
		return nil, fmt.Errorf("resolve trusted server provision profile: %w", err)
	}
	if err := validateProvisionProfile(server, request, profile); err != nil {
		return nil, err
	}
	var backupResult json.RawMessage
	var backupManifest json.RawMessage
	if request.RestoreBackup != "" {
		if w.backups == nil {
			return nil, errors.New("server.provision restore requires a trusted backup resolver")
		}
		if err := validateProvisionRestoreProfile(server, profile); err != nil {
			return nil, err
		}
		if w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":game:release-describe-output", profile.GameReleaseDescription) != nil {
			return nil, errors.New("trusted target game release metadata failed schema validation")
		}
		var release struct {
			GameAdapterID string `json:"game_adapter_id"`
			ReleaseID     string `json:"release_id"`
		}
		if json.Unmarshal(profile.GameReleaseDescription, &release) != nil || release.GameAdapterID != request.Game.AdapterID || release.ReleaseID != profile.GameReleaseID {
			return nil, errors.New("trusted target release metadata does not match the selected game adapter and release")
		}
		verified, err := w.backups.ResolveServerProvisionBackup(ctx, server, profile.BackupStorageProfileID, request.RestoreBackup)
		if err != nil {
			return nil, fmt.Errorf("resolve verified server backup: %w", err)
		}
		backupResult, backupManifest, err = w.validateVerifiedProvisionBackup(server, request.RestoreBackup, profile, verified)
		if err != nil {
			return nil, err
		}
	}

	version := w.schemas.ProtocolVersion()
	type requirement struct {
		step, adapterID     string
		category            adapter.Category
		name, input, output string
		mutating            bool
	}
	requirements := []requirement{
		{provisionCreateStep, request.HostingAdapter, adapter.CategoryHosting, "hosting.instance.create", "hosting:instance-create-input", "hosting:instance-create-output", true},
		{provisionWaitStep, request.HostingAdapter, adapter.CategoryHosting, "hosting.instance.wait", "hosting:instance-wait-input", "hosting:instance-wait-output", false},
		{provisionBootstrapStep, request.HostingAdapter, adapter.CategoryHosting, "hosting.bootstrap.deliver", "hosting:bootstrap-deliver-input", "hosting:bootstrap-execution-output", true},
		{provisionBootstrapInspect, request.HostingAdapter, adapter.CategoryHosting, "hosting.bootstrap.inspect", "hosting:bootstrap-inspect-input", "hosting:bootstrap-execution-output", false},
		{provisionInstallStep, request.Game.AdapterID, adapter.CategoryGame, "game.install", "game:install-input", "game:install-output", true},
		{provisionCreateUnitStep, request.Execution.AdapterID, adapter.CategoryExecution, "execution.unit.create", "execution:systemd-unit-create-input", "execution:systemd-unit-create-output", true},
		{provisionStartUnitStep, request.Execution.AdapterID, adapter.CategoryExecution, "execution.unit.start", "execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true},
		{provisionInspectUnitStep, request.Execution.AdapterID, adapter.CategoryExecution, "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false},
		{provisionReadinessStep, request.Game.AdapterID, adapter.CategoryGame, "game.readiness.check", "game:readiness-check-input", "game:readiness-check-output", false},
	}
	if request.RestoreBackup != "" {
		requirements = append([]requirement{
			{provisionCompatibilityStep, request.Game.AdapterID, adapter.CategoryGame, "game.compatibility.check", "game:compatibility-check-input", "game:compatibility-check-output", false},
		}, requirements...)
		insertAt := 6 // immediately after game.install
		requirements = append(requirements, requirement{})
		copy(requirements[insertAt+1:], requirements[insertAt:])
		requirements[insertAt] = requirement{provisionRestoreStep, request.Game.AdapterID, adapter.CategoryGame, "game.restore", "game:restore-input", "game:restore-output", true}
		requirements = append(requirements[:insertAt+1], append([]requirement{
			{provisionRestoreInspectStep, request.Game.AdapterID, adapter.CategoryGame, "game.restore.inspect", "game:restore-inspect-input", "game:restore-output", false},
		}, requirements[insertAt+1:]...)...)
	}
	capabilities := make([]adapter.Capability, len(requirements))
	for i, requirement := range requirements {
		capabilities[i], err = exactCapability(w.adapters, requirement.adapterID, requirement.category, requirement.name,
			"urn:gameserver-controller:"+version+":"+requirement.input,
			"urn:gameserver-controller:"+version+":"+requirement.output, requirement.mutating)
		if err != nil {
			return nil, err
		}
	}
	gameDescriptor, err := w.adapters.Descriptor(request.Game.AdapterID)
	if err != nil {
		return nil, fmt.Errorf("resolve game adapter %q: %w", request.Game.AdapterID, err)
	}
	if err := verifyReadinessSetDeclared(gameDescriptor, server.EnvironmentID, ServerStartProfile{
		GameReleaseID: profile.GameReleaseID, ReadinessCheckSetID: profile.ReadinessCheckSetID, RequiredChecks: profile.RequiredChecks,
	}); err != nil {
		return nil, err
	}

	resourceKey := provisionResourceKey(operation.OperationID, operation.RequestFingerprint)
	target := map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID}
	instanceBinding := []inputbinding.Binding{{TargetPointer: "/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"}}
	targetBinding := []inputbinding.Binding{{TargetPointer: "/target/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"}}
	inputs := map[string]struct {
		value   any
		binding []inputbinding.Binding
	}{
		provisionCreateStep:    {provisionCreateInput(server, request, profile, resourceKey), nil},
		provisionWaitStep:      {map[string]any{"expected_state": "active", "wait_timeout_seconds": 300}, instanceBinding},
		provisionBootstrapStep: {map[string]any{"bootstrap": profile.Bootstrap}, instanceBinding},
		provisionBootstrapInspect: {map[string]any{}, []inputbinding.Binding{
			{TargetPointer: "/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"},
			{TargetPointer: "/bootstrap_execution_ref", SourceStepID: provisionBootstrapStep, SourcePointer: "/bootstrap_execution_ref"},
		}},
		provisionInstallStep: {map[string]any{"target": target, "release_id": profile.GameReleaseID}, targetBinding},
		provisionCreateUnitStep: {map[string]any{"unit_ref": profile.UnitRef, "target": target, "release_id": profile.GameReleaseID,
			"unit_definition": map[string]any{"artifact_uri": profile.ArtifactURI, "checksum": profile.ArtifactChecksum}, "start": false}, targetBinding},
		provisionStartUnitStep:   {map[string]any{"unit_ref": profile.UnitRef}, nil},
		provisionInspectUnitStep: {map[string]any{"unit_ref": profile.UnitRef}, nil},
		provisionReadinessStep: {map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID,
			"game_release_id": profile.GameReleaseID, "readiness_check_set_id": profile.ReadinessCheckSetID, "required_checks": profile.RequiredChecks}, instanceBinding},
	}
	if request.RestoreBackup != "" {
		inputs[provisionCompatibilityStep] = struct {
			value   any
			binding []inputbinding.Binding
		}{map[string]any{"source_manifest": json.RawMessage(backupManifest), "target_release": json.RawMessage(profile.GameReleaseDescription),
			"required_check_ids": profile.RequiredCompatibilityCheckIDs}, nil}
		inputs[provisionRestoreStep] = struct {
			value   any
			binding []inputbinding.Binding
		}{map[string]any{"source_backup": json.RawMessage(backupResult), "target": target,
			"target_release_id": profile.GameReleaseID, "target_data_generation_id": provisionDataGeneration(operation.OperationID),
			"required_check_ids": profile.RequiredRestoreCheckIDs,
			"object_streams":     provisionObjectStreams(backupManifest)}, []inputbinding.Binding{
			{TargetPointer: "/compatibility_result", SourceStepID: provisionCompatibilityStep, SourcePointer: ""},
			{TargetPointer: "/target/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"},
		}}
		inputs[provisionRestoreInspectStep] = struct {
			value   any
			binding []inputbinding.Binding
		}{map[string]any{"backup_id": request.RestoreBackup, "target": target,
			"target_data_generation_id": provisionDataGeneration(operation.OperationID)}, []inputbinding.Binding{
			{TargetPointer: "/target/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"},
			{TargetPointer: "/restore_execution_ref", SourceStepID: provisionRestoreStep, SourcePointer: "/restore_execution_ref"},
		}}
	}
	steps := make([]store.OperationStep, len(requirements))
	for i, requirement := range requirements {
		input := inputs[requirement.step]
		encoded, err := json.Marshal(input.value)
		if err != nil {
			return nil, fmt.Errorf("encode %s input: %w", requirement.step, err)
		}
		steps[i] = store.OperationStep{StepID: requirement.step, StepType: capabilities[i].Name, Status: protocolstate.StepPending,
			AdapterID: requirement.adapterID, CapabilityName: capabilities[i].Name, CapabilityVersion: capabilities[i].Version,
			Input: encoded, InputBindings: input.binding}
	}
	return steps, nil
}

func provisionCreateInput(server store.LogicalServer, request provisionRequest, profile ServerProvisionProfile, resourceKey string) map[string]any {
	input := map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID, "resource_key": resourceKey,
		"region_id": profile.RegionID, "plan_id": profile.PlanID, "os": profile.OS}
	if request.NetworkProfile != "" {
		input["network_profile"] = request.NetworkProfile
	}
	if request.FirewallPolicy != "" {
		input["firewall_policy_ref"] = request.FirewallPolicy
	}
	if len(request.SSHKeyRefs) > 0 {
		input["ssh_key_refs"] = request.SSHKeyRefs
	}
	if request.Labels != nil {
		input["labels"] = request.Labels
	}
	return input
}

func validateProvisionProfile(server store.LogicalServer, request provisionRequest, profile ServerProvisionProfile) error {
	if profile.Provider == "" || profile.CapacityGroup == "" || profile.CapacityLimit < 1 || profile.CapacityGroupLimit < 1 ||
		profile.RegionID == "" || profile.RegionID != request.RegionID || profile.PlanID == "" || profile.PlanID != request.PlanID ||
		!isObject(profile.OS) || !equalJSON(profile.OS, request.OS) || !isObject(profile.Bootstrap) || !equalJSON(profile.Bootstrap, request.Bootstrap) ||
		profile.GameReleaseID == "" || profile.GameReleaseID != request.Game.Release || profile.ArtifactURI == "" || len(profile.ArtifactChecksum) == 0 ||
		profile.ReadinessCheckSetID == "" || len(profile.RequiredChecks) == 0 {
		return errors.New("trusted server provision profile does not exactly satisfy the selected request")
	}
	var unit struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if json.Unmarshal(profile.UnitRef, &unit) != nil || !isObject(profile.UnitRef) || unit.AdapterID != server.ExecutionAdapterID ||
		unit.EnvironmentID != server.EnvironmentID || unit.ResourceKind == "" || unit.ExternalID == "" {
		return errors.New("trusted provision unit reference does not bind the configured execution adapter and environment")
	}
	return nil
}

func provisionResourceKey(operationID, fingerprint string) string {
	digest := sha256.Sum256([]byte("server.provision\x00" + operationID + "\x00" + fingerprint))
	return "gsc-" + hex.EncodeToString(digest[:])
}

func (w *ServerProvisionWorkflow) BeforeAdapterDispatch(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if step.StepID != provisionCreateStep {
		return w.observeCreatedCapacity(operation)
	}
	var input struct {
		ResourceKey string          `json:"resource_key"`
		RegionID    string          `json:"region_id"`
		PlanID      string          `json:"plan_id"`
		OS          json.RawMessage `json:"os"`
	}
	if json.Unmarshal(step.Input, &input) != nil || input.ResourceKey == "" {
		return errors.New("persisted provider create step has invalid trusted input")
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return errors.New("persisted provision request cannot be decoded")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	profile, err := w.profiles.ResolveServerProvision(ctx, stored.Record, operation.RequestSnapshot.Parameters)
	if err != nil {
		return err
	}
	if err := validateProvisionProfile(stored.Record, request, profile); err != nil {
		return err
	}
	if input.ResourceKey != provisionResourceKey(operation.OperationID, operation.RequestFingerprint) {
		return errors.New("provider create resource key does not match the accepted operation")
	}
	reservation := store.CapacityReservation{ProviderAdapterID: request.HostingAdapter, OperationID: operation.OperationID,
		ServerID: operation.Target.ServerID, EnvironmentID: operation.Target.EnvironmentID, CapacityGroup: profile.CapacityGroup,
		Provider: profile.Provider, ResourceKey: input.ResourceKey}
	if _, _, err := w.capacity.ReserveCapacity(reservation, profile.CapacityLimit, profile.CapacityGroupLimit); err != nil {
		return fmt.Errorf("reserve managed server capacity before provider create: %w", err)
	}
	if _, err := w.capacity.MarkCapacityCreateSent(operation.OperationID); err != nil {
		return fmt.Errorf("claim one-time provider create dispatch: %w", err)
	}
	return nil
}

// SecretReferencesForStep sends operation-scoped references only to the
// bootstrap delivery adapter, the sole provision step that may need external
// credentials. The operation record stores references, never values.
func (w *ServerProvisionWorkflow) SecretReferencesForStep(ctx context.Context, operation store.OperationRecord, step store.OperationStep) ([]adapter.SecretReference, error) {
	if ctx == nil || operation.OperationType != "server.provision" {
		return nil, errors.New("server.provision operation context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if step.StepID != provisionBootstrapStep {
		return nil, nil
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil || step.AdapterID != request.HostingAdapter || step.CapabilityName != "hosting.bootstrap.deliver" {
		return nil, errors.New("provision secret references are not bound to the pinned bootstrap delivery capability")
	}
	refs := request.SecretRefs
	if len(refs) > 0 && len(operation.RequestSnapshot.SecretRefs) > 0 {
		requestRefsJSON, _ := json.Marshal(refs)
		snapshotRefsJSON, _ := json.Marshal(operation.RequestSnapshot.SecretRefs)
		if !equalJSON(requestRefsJSON, snapshotRefsJSON) {
			return nil, errors.New("provision parameter and operation snapshot secret references differ")
		}
	}
	if len(refs) == 0 {
		refs = operation.RequestSnapshot.SecretRefs
	}
	result := make([]adapter.SecretReference, 0, len(refs))
	for _, ref := range refs {
		if ref.SecretRef == "" || ref.Purpose == "" {
			return nil, errors.New("persisted provision secret reference is incomplete")
		}
		result = append(result, adapter.SecretReference{SecretRef: ref.SecretRef, Purpose: ref.Purpose,
			PermissionScope: append([]string(nil), ref.PermissionScope...), VersionRef: ref.VersionRef})
	}
	return result, nil
}

func (w *ServerProvisionWorkflow) observeCreatedCapacity(operation store.OperationRecord) error {
	created, err := provisionCreateStepOutput(operation)
	if err != nil {
		return nil
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return errors.New("persisted provision request cannot be decoded")
	}
	reservation, err := w.capacity.GetCapacityReservation(operation.OperationID)
	if err != nil {
		return fmt.Errorf("load provider create capacity reservation: %w", err)
	}
	instance, ok := decodeProvisionRef(created.InstanceRef)
	if !ok || instance.AdapterID != request.HostingAdapter || instance.EnvironmentID != operation.Target.EnvironmentID || instance.ResourceKind != "instance" {
		return errors.New("provider create output does not identify the expected exact instance")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, created.ObservedAt)
	if err != nil {
		return errors.New("provider create output has an invalid observation time")
	}
	if reservation.State == store.CapacityCreateOutcomeUnknown {
		_, err = w.capacity.ObserveCapacity(operation.OperationID, store.CapacityProviderObservation{
			Provider: reservation.Provider, ResourceKey: reservation.ResourceKey,
			ProviderInstanceID: instance.ExternalID, State: "present", ObservedAt: observedAt,
			ProvisionedAt: observedAt,
		})
		if err != nil {
			return fmt.Errorf("record exact provider create observation: %w", err)
		}
	}
	return nil
}

func (w *ServerProvisionWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || operation.OperationType != "server.provision" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.provision operation and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	server := stored.Record
	if server.ServerID != operation.Target.ServerID || server.EnvironmentID != operation.Target.EnvironmentID {
		return store.ErrLogicalServerTargetMismatch
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return errors.New("persisted provision request cannot be decoded")
	}
	if step.StepID == provisionCompatibilityStep {
		if request.RestoreBackup == "" {
			return errors.New("backup compatibility step is not part of this provision request")
		}
		return nil
	}
	if step.StepID == provisionCreateStep {
		if request.RestoreBackup != "" {
			profile, err := w.profiles.ResolveServerProvision(ctx, server, operation.RequestSnapshot.Parameters)
			if err != nil || !provisionCompatibilitySucceeded(operation, w.schemas, profile) {
				return errors.New("provider create requires a complete compatible-backup result")
			}
		}
		initial := server.LifecycleState == protocolstate.ServerUnprovisioned || server.LifecycleState == protocolstate.ServerProvisioning
		if !initial || len(server.InstanceRef) != 0 || step.Attempt != 0 {
			return errors.New("provider create requires an unprovisioned logical server")
		}
		return nil
	}
	instanceRef := server.InstanceRef
	if len(instanceRef) == 0 {
		created, err := provisionCreateStepOutput(operation)
		if err != nil {
			return errors.New("provision step requires a verified created instance")
		}
		instanceRef = created.InstanceRef
	}
	switch step.StepID {
	case provisionStartUnitStep, provisionInspectUnitStep:
		createdUnit := operationStep(operation, provisionCreateUnitStep)
		if !equalJSON(stepField(step.Input, "unit_ref"), stepField(createdUnit.Input, "unit_ref")) ||
			!provisionTargetBinds(createdUnit, instanceRef) {
			return fmt.Errorf("provision step %q is not bound to the exact created unit and instance", step.StepID)
		}
	default:
		if !provisionStepBinds(step, "instance_ref", instanceRef) && !provisionTargetBinds(step, instanceRef) {
			return fmt.Errorf("provision step %q is not bound to the exact created instance", step.StepID)
		}
	}
	switch step.StepID {
	case provisionWaitStep:
		if !stepSucceeded(operation, provisionCreateStep) {
			return errors.New("instance wait requires a completed provider create")
		}
	case provisionBootstrapStep:
		if !stepSucceeded(operation, provisionWaitStep) || !provisionWaitSucceeded(operation) {
			return errors.New("bootstrap requires the exact instance to be confirmed active")
		}
	case provisionBootstrapInspect:
		if !stepSucceeded(operation, provisionBootstrapStep) {
			return errors.New("bootstrap inspection requires a completed delivery request")
		}
	case provisionInstallStep:
		if !stepSucceeded(operation, provisionBootstrapInspect) || !provisionBootstrapSucceeded(operation) {
			return errors.New("game installation requires successful inspection of the pinned bootstrap")
		}
	case provisionRestoreStep:
		profile, err := w.profiles.ResolveServerProvision(ctx, server, operation.RequestSnapshot.Parameters)
		if err != nil || !stepSucceeded(operation, provisionInstallStep) || !provisionInstallSucceeded(operation) ||
			!provisionCompatibilitySucceeded(operation, w.schemas, profile) {
			return errors.New("backup restore requires verified installation and a compatible exact backup")
		}
	case provisionRestoreInspectStep:
		if !stepSucceeded(operation, provisionRestoreStep) {
			return errors.New("restore inspection requires the recorded restore call")
		}
	case provisionCreateUnitStep:
		if !stepSucceeded(operation, provisionInstallStep) || !provisionInstallSucceeded(operation) {
			return errors.New("execution unit creation requires verified installation of the pinned game release")
		}
		if request.RestoreBackup != "" && !provisionRestoreSucceeded(operation) {
			return errors.New("execution unit creation requires verified completion of the requested backup restore")
		}
	case provisionStartUnitStep:
		if !stepSucceeded(operation, provisionCreateUnitStep) || !provisionUnitCreated(operation) {
			return errors.New("execution unit start requires a verified stopped or unchanged unit")
		}
	case provisionInspectUnitStep:
		if !stepSucceeded(operation, provisionStartUnitStep) || !provisionUnitStarted(operation) {
			return errors.New("unit inspection requires a completed start")
		}
	case provisionReadinessStep:
		if !stepSucceeded(operation, provisionInspectUnitStep) || !provisionUnitRunning(operation) || (request.RestoreBackup != "" && !provisionRestoreSucceeded(operation)) {
			return errors.New("game readiness requires the exact execution unit to be running")
		}
	default:
		return fmt.Errorf("unsupported server.provision step %q", step.StepID)
	}
	return nil
}

func provisionStepBinds(step store.OperationStep, path string, expected json.RawMessage) bool {
	input := step.Input
	if len(step.ResolvedInput) > 0 {
		input = step.ResolvedInput
	}
	return stepBindsRef(input, path, expected)
}

func provisionTargetBinds(step store.OperationStep, expected json.RawMessage) bool {
	input := step.Input
	if len(step.ResolvedInput) > 0 {
		input = step.ResolvedInput
	}
	return stepBindsRef(stepField(input, "target"), "instance_ref", expected)
}

func provisionCreateStepOutput(operation store.OperationRecord) (provisionCreateOutput, error) {
	var output provisionCreateOutput
	step := operationStep(operation, provisionCreateStep)
	if step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil || !isObject(output.InstanceRef) {
		return provisionCreateOutput{}, errors.New("provider create output missing")
	}
	var request provisionRequest
	ref, valid := decodeProvisionRef(output.InstanceRef)
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil || !valid || ref.AdapterID != request.HostingAdapter ||
		ref.EnvironmentID != operation.Target.EnvironmentID || ref.ResourceKind != "instance" {
		return provisionCreateOutput{}, errors.New("provider create output is not bound to the expected adapter, environment, and instance kind")
	}
	return output, nil
}

func provisionWaitSucceeded(operation store.OperationRecord) bool {
	var out provisionWaitOutput
	step := operationStep(operation, provisionWaitStep)
	created, err := provisionCreateStepOutput(operation)
	return err == nil && step.Status == protocolstate.StepSucceeded && json.Unmarshal(step.Output, &out) == nil && out.OperationStatus == "succeeded" && out.InstanceState == "active" && equalJSON(out.InstanceRef, created.InstanceRef)
}
func provisionBootstrapSucceeded(operation store.OperationRecord) bool {
	var out provisionBootstrapInspection
	step := operationStep(operation, provisionBootstrapInspect)
	return step.Status == protocolstate.StepSucceeded && json.Unmarshal(step.Output, &out) == nil && out.State == "succeeded" && provisionBootstrapMatches(operation, out)
}
func provisionBootstrapMatches(operation store.OperationRecord, out provisionBootstrapInspection) bool {
	var expected struct {
		Bootstrap struct {
			Revision string `json:"revision"`
			Digest   string `json:"digest"`
		} `json:"bootstrap"`
	}
	step := operationStep(operation, provisionBootstrapStep)
	return json.Unmarshal(step.Input, &expected) == nil && out.Revision == expected.Bootstrap.Revision && out.Digest == expected.Bootstrap.Digest && equalJSON(out.InstanceRef, stepField(step.ResolvedInput, "instance_ref")) && equalJSON(out.BootstrapExecutionRef, stepField(step.Output, "bootstrap_execution_ref"))
}
func provisionInstallSucceeded(operation store.OperationRecord) bool {
	var out struct {
		Target struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		ReleaseID    string   `json:"release_id"`
		Outcome      string   `json:"outcome"`
		Verification string   `json:"artifact_verification"`
		Started      bool     `json:"installation_started"`
		Verified     []string `json:"verified_artifact_ids"`
	}
	step := operationStep(operation, provisionInstallStep)
	created, err := provisionCreateStepOutput(operation)
	var expected struct {
		Target struct {
			ServerID      string `json:"server_id"`
			EnvironmentID string `json:"environment_id"`
		} `json:"target"`
		ReleaseID string `json:"release_id"`
	}
	return err == nil && step.Status == protocolstate.StepSucceeded && json.Unmarshal(step.Output, &out) == nil && json.Unmarshal(step.ResolvedInput, &expected) == nil &&
		(out.Outcome == "installed" || out.Outcome == "already_installed") && out.Verification == "verified" && len(out.Verified) > 0 &&
		out.ReleaseID == expected.ReleaseID && out.Target.ServerID == expected.Target.ServerID && out.Target.EnvironmentID == expected.Target.EnvironmentID && equalJSON(out.Target.InstanceRef, created.InstanceRef)
}
func provisionUnitCreated(operation store.OperationRecord) bool {
	var out struct {
		UnitRef json.RawMessage `json:"unit_ref"`
		Target  struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		ReleaseID string          `json:"release_id"`
		Checksum  json.RawMessage `json:"definition_checksum"`
		Outcome   string          `json:"outcome"`
		State     string          `json:"state"`
		Started   bool            `json:"started_by_request"`
		Preserved bool            `json:"persistent_data_preserved"`
	}
	step := operationStep(operation, provisionCreateUnitStep)
	created, err := provisionCreateStepOutput(operation)
	var expected struct {
		UnitRef json.RawMessage `json:"unit_ref"`
		Target  struct {
			ServerID      string `json:"server_id"`
			EnvironmentID string `json:"environment_id"`
		} `json:"target"`
		ReleaseID      string `json:"release_id"`
		UnitDefinition struct {
			Checksum json.RawMessage `json:"checksum"`
		} `json:"unit_definition"`
	}
	return err == nil && step.Status == protocolstate.StepSucceeded && json.Unmarshal(step.Output, &out) == nil && json.Unmarshal(step.ResolvedInput, &expected) == nil &&
		isObject(out.UnitRef) && equalJSON(out.UnitRef, expected.UnitRef) && (out.Outcome == "created" || out.Outcome == "unchanged") &&
		(out.State == "stopped" || out.State == "running") && !out.Started && out.Preserved && out.ReleaseID == expected.ReleaseID &&
		out.Target.ServerID == expected.Target.ServerID && out.Target.EnvironmentID == expected.Target.EnvironmentID && equalJSON(out.Target.InstanceRef, created.InstanceRef) && equalJSON(out.Checksum, expected.UnitDefinition.Checksum)
}
func provisionUnitStarted(operation store.OperationRecord) bool {
	var out executionUnitStartResult
	step := operationStep(operation, provisionStartUnitStep)
	created := operationStep(operation, provisionCreateUnitStep)
	return step.Status == protocolstate.StepSucceeded && json.Unmarshal(step.Output, &out) == nil && out.Outcome == "completed" && out.State == "running" && equalJSON(out.UnitRef, stepField(created.Output, "unit_ref")) && equalJSON(out.UnitRef, stepField(step.Input, "unit_ref"))
}
func provisionUnitRunning(operation store.OperationRecord) bool {
	out, err := unitInspectStepOutput(operation, provisionInspectUnitStep)
	return err == nil && out.State == "running" && equalJSON(out.UnitRef, stepField(operationStep(operation, provisionStartUnitStep).Input, "unit_ref"))
}

func decodeProvisionRef(raw json.RawMessage) (struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}, bool) {
	var ref struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if json.Unmarshal(raw, &ref) != nil || !isObject(raw) || ref.AdapterID == "" || ref.EnvironmentID == "" || ref.ResourceKind == "" || ref.ExternalID == "" {
		return ref, false
	}
	return ref, true
}

func (w *ServerProvisionWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil || operation.OperationType != "server.provision" {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	switch step.StepID {
	case provisionCreateStep:
		return w.reconcileCreate(ctx, operation, step)
	case provisionBootstrapStep:
		return w.reconcileBootstrap(ctx, operation, step)
	case provisionStartUnitStep:
		return w.reconcileUnitStart(ctx, operation, step)
	case provisionRestoreStep:
		return w.reconcileRestore(ctx, operation, step)
	case provisionCompatibilityStep, provisionRestoreInspectStep:
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	case provisionReadinessStep:
		// This is a read-only observation. Repeating the exact pinned checks is safe.
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

func (w *ServerProvisionWorkflow) reconcileCreate(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if step.CapabilityName != "hosting.instance.create" || step.AdapterID == "" {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	var createInput struct {
		ResourceKey string `json:"resource_key"`
	}
	if json.Unmarshal(step.Input, &createInput) != nil || createInput.ResourceKey != provisionResourceKey(operation.OperationID, operation.RequestFingerprint) {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	capability, err := exactCapability(w.adapters, step.AdapterID, adapter.CategoryHosting, "hosting.instance.find_by_resource_key",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:instance-find-by-resource-key-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:instance-find-by-resource-key-output", false)
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	input, _ := json.Marshal(map[string]any{"resource_key": createInput.ResourceKey})
	response, err := w.invokeProvision(ctx, operation, step, capability, input)
	if err != nil || response.Result.Outcome != adapter.OutcomeCompleted || w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var lookup struct {
		ResourceKey       string `json:"resource_key"`
		InventoryComplete bool   `json:"inventory_complete"`
		Matches           []struct {
			InstanceRef json.RawMessage `json:"instance_ref"`
			State       string          `json:"state"`
		} `json:"matches"`
		ObservedAt string `json:"observed_at"`
	}
	if json.Unmarshal(response.Result.Output, &lookup) != nil || lookup.ResourceKey != createInput.ResourceKey || !lookup.InventoryComplete {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	if len(lookup.Matches) != 1 {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	match := lookup.Matches[0]
	ref, ok := decodeProvisionRef(match.InstanceRef)
	if !ok || ref.AdapterID != step.AdapterID || ref.EnvironmentID != operation.Target.EnvironmentID || ref.ResourceKind != "instance" ||
		(match.State != "creating" && match.State != "active" && match.State != "stopped") {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	if _, err := time.Parse(time.RFC3339Nano, lookup.ObservedAt); err != nil {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	output, _ := json.Marshal(provisionCreateOutput{InstanceRef: match.InstanceRef, State: match.State, ObservedAt: lookup.ObservedAt})
	if err := w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:instance-create-output", output); err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}, nil
}

func (w *ServerProvisionWorkflow) reconcileBootstrap(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if step.CapabilityName != "hosting.bootstrap.deliver" {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	created, err := provisionCreateStepOutput(operation)
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	var delivered provisionBootstrapOutput
	if json.Unmarshal(step.Output, &delivered) != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	executionRef := delivered.BootstrapExecutionRef
	if len(executionRef) == 0 {
		executionRef = step.ExternalOperationRef
	}
	ref, ok := decodeProvisionRef(executionRef)
	if !ok || ref.AdapterID != step.AdapterID || ref.EnvironmentID != operation.Target.EnvironmentID || ref.ResourceKind != "bootstrap-execution" {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	capability, err := exactCapability(w.adapters, step.AdapterID, adapter.CategoryHosting, "hosting.bootstrap.inspect",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:bootstrap-inspect-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:bootstrap-execution-output", false)
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	input, _ := json.Marshal(map[string]any{"instance_ref": created.InstanceRef, "bootstrap_execution_ref": executionRef})
	response, err := w.invokeProvision(ctx, operation, step, capability, input)
	if err != nil || response.Result.Outcome != adapter.OutcomeCompleted || w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var inspected provisionBootstrapInspection
	if json.Unmarshal(response.Result.Output, &inspected) != nil || !equalJSON(inspected.InstanceRef, created.InstanceRef) ||
		!equalJSON(inspected.BootstrapExecutionRef, executionRef) || inspected.Revision != delivered.Revision || inspected.Digest != delivered.Digest {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	switch inspected.State {
	case "succeeded":
		return Resolution{State: ResolutionCompleted, Output: response.Result.Output, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "running":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "failed":
		return Resolution{State: ResolutionDefinitiveError, ExternalOperationRef: step.ExternalOperationRef,
			Error: protocolError("core.bootstrap.failed", "provider_failure", "The exact pinned bootstrap execution is confirmed failed.", operation.OperationID, false, "operator_review", false)}, nil
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

func (w *ServerProvisionWorkflow) reconcileUnitStart(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if step.CapabilityName != "execution.unit.start" {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	inspect := operationStep(operation, provisionInspectUnitStep)
	if inspect.CapabilityName != "execution.unit.inspect" || inspect.AdapterID != step.AdapterID {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	capability, err := exactCapability(w.adapters, inspect.AdapterID, adapter.CategoryExecution, "execution.unit.inspect",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-output", false)
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	input := stepField(step.Input, "unit_ref")
	request, _ := json.Marshal(map[string]any{"unit_ref": input})
	response, err := w.invokeProvision(ctx, operation, step, capability, request)
	if err != nil || response.Result.Outcome != adapter.OutcomeCompleted || w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var observed provisionUnitInspection
	if json.Unmarshal(response.Result.Output, &observed) != nil || !equalJSON(observed.UnitRef, input) {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	switch observed.State {
	case "running":
		output, _ := json.Marshal(executionUnitStartResult{UnitRef: observed.UnitRef, Outcome: "completed", State: "running", ObservedAt: observed.ObservedAt})
		return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "stopped", "absent":
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "starting":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}, nil
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

func (w *ServerProvisionWorkflow) invokeProvision(ctx context.Context, operation store.OperationRecord, original store.OperationStep, capability adapter.Capability, input json.RawMessage) (adapter.InvokeResponse, error) {
	requestID := startReconcileRequestID(operation.OperationID, original.StepID, original.Attempt)
	deadline := time.Now().UTC().Add(15 * time.Second)
	correlation := operation.CorrelationID
	if correlation == "" {
		correlation = operation.OperationID
	}
	callContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response, err := w.adapters.Invoke(callContext, original.AdapterID, adapter.InvokeRequest{JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke",
		Params: adapter.InvokeParams{ProtocolVersion: operation.ProtocolVersion, Capability: capability.Name,
			RequestContext: adapter.RequestContext{RequestID: requestID, CorrelationID: correlation, OperationID: operation.OperationID,
				StepID: original.StepID, ServerID: operation.Target.ServerID, EnvironmentID: operation.Target.EnvironmentID, Deadline: deadline}, Input: input}})
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	if response.JSONRPC != "2.0" || response.ID != requestID {
		return adapter.InvokeResponse{}, errors.New("reconciliation adapter returned a mismatched JSON-RPC response")
	}
	return response, nil
}

func (w *ServerProvisionWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	if step.StepID == provisionCreateStep {
		var request provisionRequest
		if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) == nil && request.RestoreBackup != "" {
			if status := stepField(operationStep(operation, provisionCompatibilityStep).Output, "compatibility_status"); string(status) != `"compatible"` {
				return Resolution{State: ResolutionDefinitiveError, Error: protocolError("core.restore.incompatible", "safety_blocked",
					"The selected backup did not pass every required compatibility check; no provider instance was created.", operation.OperationID, false, "inspect_operation", false)}, nil
			}
		}
	}
	if step.StepID == provisionWaitStep && stepSucceeded(operation, provisionCreateStep) {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	if step.StepID == provisionBootstrapStep && stepSucceeded(operation, provisionWaitStep) && !provisionWaitSucceeded(operation) {
		return Resolution{State: ResolutionDefinitiveError, Error: protocolError("core.safety.precondition_unmet", "safety_blocked",
			"The exact provider instance did not reach active state; no bootstrap or game changes were issued.", operation.OperationID, false, "inspect_operation", false)}, nil
	}
	return Resolution{State: ResolutionNeedsOperator}, nil
}

func (w *ServerProvisionWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.provision" {
		return errors.New("server.provision completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	created, err := provisionCreateStepOutput(operation)
	if err != nil || !provisionWaitSucceeded(operation) || !provisionBootstrapSucceeded(operation) || !provisionInstallSucceeded(operation) ||
		!provisionUnitCreated(operation) || !provisionUnitStarted(operation) || !provisionUnitRunning(operation) {
		return errors.New("server.provision exact instance, bootstrap, game, or execution postconditions were not confirmed")
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return errors.New("server.provision request cannot be decoded")
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	if request.RestoreBackup != "" {
		profile, err := w.profiles.ResolveServerProvision(ctx, server.Record, operation.RequestSnapshot.Parameters)
		if err != nil || !provisionCompatibilitySucceeded(operation, w.schemas, profile) || !provisionRestoreSucceeded(operation) {
			return errors.New("server.provision requested restore lacks exact compatible source and verified restore evidence")
		}
	}
	if !equalJSON(server.Record.InstanceRef, created.InstanceRef) {
		return errors.New("server.provision logical server is not bound to the exact created instance")
	}
	if server.Record.DataGenerationStatus != "known" || server.Record.DataGenerationID != provisionDataGeneration(operation.OperationID) {
		return errors.New("server.provision has no controller-owned data generation for the installed world")
	}
	if _, err := BuildReadinessAssertion(w.schemas, operation, server.Record, provisionReadinessStep, w.assertionTTL); err != nil {
		return err
	}
	ref, ok := decodeProvisionRef(created.InstanceRef)
	if !ok {
		return errors.New("server.provision provider instance reference is invalid")
	}
	createTime, err := time.Parse(time.RFC3339Nano, created.ObservedAt)
	if err != nil {
		return errors.New("server.provision create observation time is invalid")
	}
	reservation, err := w.capacity.GetCapacityReservation(operation.OperationID)
	if err != nil {
		return fmt.Errorf("load provision capacity reservation: %w", err)
	}
	if reservation.State == store.CapacityCreateOutcomeUnknown {
		reservation, err = w.capacity.ObserveCapacity(operation.OperationID, store.CapacityProviderObservation{Provider: reservation.Provider,
			ResourceKey: reservation.ResourceKey, ProviderInstanceID: ref.ExternalID, State: "present", ObservedAt: time.Now().UTC(), ProvisionedAt: createTime})
		if err != nil {
			return fmt.Errorf("record exact provisioned capacity: %w", err)
		}
	}
	if reservation.State != store.CapacityActive || reservation.ProviderInstanceID != ref.ExternalID {
		return errors.New("server.provision capacity reservation is not active for the exact instance")
	}
	var readiness readinessCheckOutput
	readinessStep := operationStep(operation, provisionReadinessStep)
	if readinessStep.Status != protocolstate.StepSucceeded || json.Unmarshal(readinessStep.Output, &readiness) != nil {
		return errors.New("server.provision readiness output is missing or invalid")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, readiness.ObservedAt)
	if err != nil {
		return errors.New("server.provision readiness observation time is invalid")
	}
	if _, err := w.capacity.RecordCapacityReady(operation.Target.ServerID, operation.Target.EnvironmentID, ref.ExternalID, observedAt); err != nil {
		return fmt.Errorf("record exact provision-to-ready capacity evidence: %w", err)
	}
	return nil
}

func (w *ServerProvisionWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	assertion, err := BuildReadinessAssertion(w.schemas, operation, server.Record, provisionReadinessStep, w.assertionTTL)
	if err != nil {
		return nil, err
	}
	created, _ := provisionCreateStepOutput(operation)
	bootstrap := stepField(operationStep(operation, provisionBootstrapStep).Input, "bootstrap")
	var pinnedBootstrap struct {
		Revision string `json:"revision"`
		Digest   string `json:"digest"`
	}
	if json.Unmarshal(bootstrap, &pinnedBootstrap) != nil {
		return nil, errors.New("persisted bootstrap input cannot be decoded")
	}
	unit := stepField(operationStep(operation, provisionCreateUnitStep).Input, "unit_ref")
	var gameInput struct {
		ReleaseID string `json:"release_id"`
	}
	if json.Unmarshal(operationStep(operation, provisionInstallStep).Input, &gameInput) != nil {
		return nil, errors.New("persisted game install input cannot be decoded")
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return nil, errors.New("persisted provision request cannot be decoded")
	}
	data := map[string]any{"server_id": server.Record.ServerID, "environment_id": server.Record.EnvironmentID,
		"instance_ref": created.InstanceRef, "bootstrap": map[string]string{"revision": pinnedBootstrap.Revision, "digest": pinnedBootstrap.Digest},
		"game":               map[string]any{"adapter_id": request.Game.AdapterID, "release_ref": gameInput.ReleaseID},
		"execution":          map[string]any{"adapter_id": request.Execution.AdapterID, "unit_ref": unit},
		"data_generation_id": server.Record.DataGenerationID, "lifecycle_state": string(protocolstate.ServerReady), "readiness_assertion": assertion}
	if request.RestoreBackup != "" {
		data["restored_backup_id"] = request.RestoreBackup
	}
	result := map[string]any{"outcome_summary": "Server provisioned and all required game readiness checks passed.",
		"operation_type": "server.provision", "affected_resources": []any{created.InstanceRef, unit},
		"completed_steps": completedStepSummaries(operation), "failed_steps": []any{}, "warnings": []string{},
		"unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": false, "operation_data": data}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if err := w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":operations:server-provision-result", encoded); err != nil {
		return nil, fmt.Errorf("server.provision typed result is invalid: %w", err)
	}
	return encoded, nil
}

func (w *ServerProvisionWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	failed := []any{}
	if step.StepID != "" && len(failure) > 0 && step.Status == protocolstate.StepFailed {
		failed = append(failed, map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure})
	}
	partial := operationStep(operation, provisionCreateStep).Attempt > 0
	instance := json.RawMessage(nil)
	if created, err := provisionCreateStepOutput(operation); err == nil {
		instance = created.InstanceRef
	}
	affected := []any{}
	if len(instance) > 0 {
		affected = append(affected, instance)
	}
	unresolved := []any{}
	if step.StepID == "" {
		unresolved = append(unresolved, map[string]any{"condition_id": "server.provision.postcondition_unmet", "description": "The workflow did not prove every exact provisioning and readiness postcondition."})
	}
	result := map[string]any{"outcome_summary": "Server provisioning did not satisfy all runtime and readiness postconditions.",
		"operation_type": "server.provision", "affected_resources": affected, "completed_steps": completedStepSummaries(operation),
		"failed_steps": failed, "warnings": []string{}, "unresolved_conditions": unresolved, "audit_event_ids": []string{}, "partial": partial}
	return json.Marshal(result)
}

func (w *ServerProvisionWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if operation.OperationType != "server.provision" || current.Record.ServerID != operation.Target.ServerID || current.Record.EnvironmentID != operation.Target.EnvironmentID {
		return store.LogicalServer{}, false, errors.New("server.provision lifecycle projection target mismatch")
	}
	next := current.Record
	created, createErr := provisionCreateStepOutput(operation)
	switch current.Record.LifecycleState {
	case protocolstate.ServerUnprovisioned:
		if operation.Status != protocolstate.OperationPending {
			next.LifecycleState = protocolstate.ServerProvisioning
			return next, true, nil
		}
	case protocolstate.ServerProvisioning:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			return next, true, nil
		}
		if createErr == nil {
			next.InstanceRef = append(json.RawMessage(nil), created.InstanceRef...)
			next.LifecycleState = protocolstate.ServerPreparing
			next.DataGenerationID = provisionDataGeneration(operation.OperationID)
			next.DataGenerationStatus = "unknown"
			return next, true, nil
		}
	case protocolstate.ServerPreparing:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			return next, true, nil
		}
		if operationStep(operation, provisionStartUnitStep).Status != protocolstate.StepPending {
			if !provisionInstallSucceeded(operation) {
				return store.LogicalServer{}, false, errors.New("server.provision cannot start before the exact game release is installed")
			}
			next.LifecycleState = protocolstate.ServerStarting
			next.DataGenerationID = provisionDataGeneration(operation.OperationID)
			next.DataGenerationStatus = "known"
			return next, true, nil
		}
	case protocolstate.ServerStarting:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			return next, true, nil
		}
		if operationStep(operation, provisionReadinessStep).Status != protocolstate.StepPending {
			next.LifecycleState = protocolstate.ServerValidating
			return next, true, nil
		}
	case protocolstate.ServerValidating:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerDegraded
			return next, true, nil
		}
		if operation.Status == protocolstate.OperationSucceeded {
			if !hasValidProvisionResult(operation.Result, operation.Target, current.Record.InstanceRef, w.schemas) {
				return store.LogicalServer{}, false, errors.New("terminal server.provision result lacks exact instance and readiness evidence")
			}
			next.LifecycleState = protocolstate.ServerReady
			next.DesiredState = "running"
			return next, true, nil
		}
	}
	return current.Record, false, nil
}

func provisionDataGeneration(operationID string) string {
	digest := sha256.Sum256([]byte("server.provision.data-generation\x00" + operationID))
	return "gen-" + hex.EncodeToString(digest[:16])
}

func hasValidProvisionResult(raw json.RawMessage, target store.OperationTarget, instanceRef json.RawMessage, schemas schemaRuntime) bool {
	if schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-provision-result", raw) != nil {
		return false
	}
	var result struct {
		OperationType string `json:"operation_type"`
		Data          struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
			State         string          `json:"lifecycle_state"`
			Assertion     json.RawMessage `json:"readiness_assertion"`
		} `json:"operation_data"`
	}
	return json.Unmarshal(raw, &result) == nil && result.OperationType == "server.provision" && result.Data.ServerID == target.ServerID && result.Data.EnvironmentID == target.EnvironmentID && result.Data.State == string(protocolstate.ServerReady) && equalJSON(result.Data.InstanceRef, instanceRef) && isObject(result.Data.Assertion)
}

var _ interface {
	BuildPlan(context.Context, store.OperationRecord) ([]store.OperationStep, error)
	Definition
	LifecycleProjector
	DispatchGuard
	CompletionValidator
} = (*ServerProvisionWorkflow)(nil)
