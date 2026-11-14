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
	if ctx == nil || !w.provisionWorkflowReady() {
		return nil, errors.New("initialized server.provision workflow and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan, err := w.loadProvisionPlan(ctx, operation)
	if err != nil {
		return nil, err
	}
	if plan.request.RestoreBackup != "" {
		if plan.backupResult, plan.backupManifest, err = w.resolveProvisionRestoreSource(ctx, plan); err != nil {
			return nil, err
		}
	}
	requirements := provisionRequirements(plan.request)
	capabilities, err := w.pinProvisionCapabilities(requirements)
	if err != nil {
		return nil, err
	}
	if err := w.verifyProvisionReadinessSet(plan); err != nil {
		return nil, err
	}
	return provisionPlanSteps(requirements, capabilities, provisionStepInputs(plan))
}

// provisionPlan is what BuildPlan resolves before pinning capabilities and
// inputs: the accepted operation, its request, the stored server, the
// trusted profile and, when restoring, the verified backup.
type provisionPlan struct {
	operation      store.OperationRecord
	request        provisionRequest
	server         store.LogicalServer
	profile        ServerProvisionProfile
	backupResult   json.RawMessage
	backupManifest json.RawMessage
}

// provisionRequirement is one planned step and the capability it pins.
type provisionRequirement struct {
	step, adapterID     string
	category            adapter.Category
	name, input, output string
	mutating            bool
}

// provisionStepInput is a step's trusted input template and its bindings.
type provisionStepInput struct {
	value   any
	binding []inputbinding.Binding
}

func (w *ServerProvisionWorkflow) provisionWorkflowReady() bool {
	return w != nil && w.provisionStoresReady() && w.provisionRuntimesReady()
}

func (w *ServerProvisionWorkflow) provisionStoresReady() bool {
	return w.servers != nil && w.capacity != nil
}

func (w *ServerProvisionWorkflow) provisionRuntimesReady() bool {
	return w.adapters != nil && w.schemas != nil && w.profiles != nil
}

// isProvisionOperation reports whether the operation is a server.provision
// for the loaded protocol, with a named target and an object request.
func isProvisionOperation(operation store.OperationRecord, version string) bool {
	return operation.OperationType == "server.provision" && operation.ProtocolVersion == version &&
		provisionTargetNamed(operation.Target) && provisionSnapshotUsable(operation.RequestSnapshot)
}

func provisionTargetNamed(target store.OperationTarget) bool {
	return target.ServerID != "" && target.EnvironmentID != ""
}

func provisionSnapshotUsable(snapshot store.RequestSnapshot) bool {
	return snapshot.OperationType == "server.provision" && isObject(snapshot.Parameters)
}

// provisionDefinitionMatches reports whether the request selects the
// persisted game adapter, execution adapter and execution profile.
func provisionDefinitionMatches(server store.LogicalServer, request provisionRequest) bool {
	return server.GameAdapterID == request.Game.AdapterID && server.ExecutionAdapterID == request.Execution.AdapterID &&
		server.ExecutionProfile == request.Execution.Profile
}

// loadProvisionPlan checks the operation and request, loads the stored
// server, and resolves and checks the trusted profile.
func (w *ServerProvisionWorkflow) loadProvisionPlan(ctx context.Context, operation store.OperationRecord) (provisionPlan, error) {
	request, err := w.decodeProvisionPlanRequest(operation)
	if err != nil {
		return provisionPlan{}, err
	}
	plan := provisionPlan{operation: operation, request: request}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return provisionPlan{}, fmt.Errorf("load logical server for provision plan: %w", err)
	}
	plan.server = stored.Record
	if plan.server.LifecycleState != protocolstate.ServerUnprovisioned || len(plan.server.InstanceRef) != 0 {
		return provisionPlan{}, errors.New("server.provision requires an unprovisioned logical server without an attached instance")
	}
	if !provisionDefinitionMatches(plan.server, plan.request) {
		return provisionPlan{}, errors.New("server.provision request does not match the persisted game and execution definition")
	}
	plan.profile, err = w.profiles.ResolveServerProvision(ctx, plan.server, append(json.RawMessage(nil), operation.RequestSnapshot.Parameters...))
	if err != nil {
		return provisionPlan{}, fmt.Errorf("resolve trusted server provision profile: %w", err)
	}
	if err := validateProvisionProfile(plan.server, plan.request, plan.profile); err != nil {
		return provisionPlan{}, err
	}
	return plan, nil
}

// decodeProvisionPlanRequest checks the operation and decodes its request,
// which must name the operation's target.
func (w *ServerProvisionWorkflow) decodeProvisionPlanRequest(operation store.OperationRecord) (provisionRequest, error) {
	if !isProvisionOperation(operation, w.schemas.ProtocolVersion()) {
		return provisionRequest{}, errors.New("server.provision operation and loaded protocol version are required")
	}
	var request provisionRequest
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &request); err != nil {
		return provisionRequest{}, errors.New("decode server.provision parameters")
	}
	if request.ServerID != operation.Target.ServerID || request.EnvironmentID != operation.Target.EnvironmentID {
		return provisionRequest{}, errors.New("server.provision request identity does not match its operation target")
	}
	return request, nil
}

// resolveProvisionRestoreSource checks the restore profile and target
// release, then resolves and verifies the selected backup. It returns the
// verified backup result and its manifest.
func (w *ServerProvisionWorkflow) resolveProvisionRestoreSource(ctx context.Context, plan provisionPlan) (json.RawMessage, json.RawMessage, error) {
	if w.backups == nil {
		return nil, nil, errors.New("server.provision restore requires a trusted backup resolver")
	}
	if err := validateProvisionRestoreProfile(plan.server, plan.profile); err != nil {
		return nil, nil, err
	}
	if w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":game:release-describe-output", plan.profile.GameReleaseDescription) != nil {
		return nil, nil, errors.New("trusted target game release metadata failed schema validation")
	}
	if !provisionReleaseDescribes(plan.profile, plan.request) {
		return nil, nil, errors.New("trusted target release metadata does not match the selected game adapter and release")
	}
	verified, err := w.backups.ResolveServerProvisionBackup(ctx, plan.server, plan.profile.BackupStorageProfileID, plan.request.RestoreBackup)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve verified server backup: %w", err)
	}
	return w.validateVerifiedProvisionBackup(plan.server, plan.request.RestoreBackup, plan.profile, verified)
}

// provisionReleaseDescribes reports whether the trusted release metadata
// describes the selected game adapter and the profile's release.
func provisionReleaseDescribes(profile ServerProvisionProfile, request provisionRequest) bool {
	var release struct {
		GameAdapterID string `json:"game_adapter_id"`
		ReleaseID     string `json:"release_id"`
	}
	if json.Unmarshal(profile.GameReleaseDescription, &release) != nil {
		return false
	}
	return release.GameAdapterID == request.Game.AdapterID && release.ReleaseID == profile.GameReleaseID
}

// provisionRequirements lists the planned steps in order. A restore checks
// compatibility before the provider create and restores right after the
// game install.
func provisionRequirements(request provisionRequest) []provisionRequirement {
	hosting, game, execution := request.HostingAdapter, request.Game.AdapterID, request.Execution.AdapterID
	host := []provisionRequirement{
		{provisionCreateStep, hosting, adapter.CategoryHosting, "hosting.instance.create", "hosting:instance-create-input", "hosting:instance-create-output", true},
		{provisionWaitStep, hosting, adapter.CategoryHosting, "hosting.instance.wait", "hosting:instance-wait-input", "hosting:instance-wait-output", false},
		{provisionBootstrapStep, hosting, adapter.CategoryHosting, "hosting.bootstrap.deliver", "hosting:bootstrap-deliver-input", "hosting:bootstrap-execution-output", true},
		{provisionBootstrapInspect, hosting, adapter.CategoryHosting, "hosting.bootstrap.inspect", "hosting:bootstrap-inspect-input", "hosting:bootstrap-execution-output", false},
		{provisionInstallStep, game, adapter.CategoryGame, "game.install", "game:install-input", "game:install-output", true},
	}
	runtime := []provisionRequirement{
		{provisionCreateUnitStep, execution, adapter.CategoryExecution, "execution.unit.create", "execution:systemd-unit-create-input", "execution:systemd-unit-create-output", true},
		{provisionStartUnitStep, execution, adapter.CategoryExecution, "execution.unit.start", "execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true},
		{provisionInspectUnitStep, execution, adapter.CategoryExecution, "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false},
		{provisionReadinessStep, game, adapter.CategoryGame, "game.readiness.check", "game:readiness-check-input", "game:readiness-check-output", false},
	}
	if request.RestoreBackup == "" {
		return append(host, runtime...)
	}
	requirements := []provisionRequirement{
		{provisionCompatibilityStep, game, adapter.CategoryGame, "game.compatibility.check", "game:compatibility-check-input", "game:compatibility-check-output", false},
	}
	requirements = append(requirements, host...)
	requirements = append(requirements,
		provisionRequirement{provisionRestoreStep, game, adapter.CategoryGame, "game.restore", "game:restore-input", "game:restore-output", true},
		provisionRequirement{provisionRestoreInspectStep, game, adapter.CategoryGame, "game.restore.inspect", "game:restore-inspect-input", "game:restore-output", false})
	return append(requirements, runtime...)
}

// pinProvisionCapabilities resolves the exact capability for each step.
func (w *ServerProvisionWorkflow) pinProvisionCapabilities(requirements []provisionRequirement) ([]adapter.Capability, error) {
	version := w.schemas.ProtocolVersion()
	capabilities := make([]adapter.Capability, len(requirements))
	for i, requirement := range requirements {
		capability, err := exactCapability(w.adapters, requirement.adapterID, requirement.category, requirement.name,
			"urn:gameserver-controller:"+version+":"+requirement.input,
			"urn:gameserver-controller:"+version+":"+requirement.output, requirement.mutating)
		if err != nil {
			return nil, err
		}
		capabilities[i] = capability
	}
	return capabilities, nil
}

// verifyProvisionReadinessSet requires the game adapter to declare the
// profile's readiness check set for this release and environment.
func (w *ServerProvisionWorkflow) verifyProvisionReadinessSet(plan provisionPlan) error {
	gameDescriptor, err := w.adapters.Descriptor(plan.request.Game.AdapterID)
	if err != nil {
		return fmt.Errorf("resolve game adapter %q: %w", plan.request.Game.AdapterID, err)
	}
	return verifyReadinessSetDeclared(gameDescriptor, plan.server.EnvironmentID, ServerStartProfile{
		GameReleaseID: plan.profile.GameReleaseID, ReadinessCheckSetID: plan.profile.ReadinessCheckSetID, RequiredChecks: plan.profile.RequiredChecks,
	})
}

// provisionStepInputs builds each step's trusted input and bindings.
func provisionStepInputs(plan provisionPlan) map[string]provisionStepInput {
	server, profile := plan.server, plan.profile
	resourceKey := provisionResourceKey(plan.operation.OperationID, plan.operation.RequestFingerprint)
	target := map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID}
	instanceBinding := []inputbinding.Binding{{TargetPointer: "/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"}}
	targetBinding := []inputbinding.Binding{{TargetPointer: "/target/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"}}
	inputs := map[string]provisionStepInput{
		provisionCreateStep:    {provisionCreateInput(server, plan.request, profile, resourceKey), nil},
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
	if plan.request.RestoreBackup != "" {
		addProvisionRestoreInputs(inputs, plan, target)
	}
	return inputs
}

// addProvisionRestoreInputs adds the compatibility, restore and restore
// inspection inputs, which bind the verified backup to a fresh generation.
func addProvisionRestoreInputs(inputs map[string]provisionStepInput, plan provisionPlan, target map[string]any) {
	profile := plan.profile
	generation := provisionDataGeneration(plan.operation.OperationID)
	inputs[provisionCompatibilityStep] = provisionStepInput{map[string]any{"source_manifest": json.RawMessage(plan.backupManifest), "target_release": json.RawMessage(profile.GameReleaseDescription),
		"required_check_ids": profile.RequiredCompatibilityCheckIDs}, nil}
	inputs[provisionRestoreStep] = provisionStepInput{map[string]any{"source_backup": json.RawMessage(plan.backupResult), "target": target,
		"target_release_id": profile.GameReleaseID, "target_data_generation_id": generation,
		"required_check_ids": profile.RequiredRestoreCheckIDs,
		"object_streams":     provisionObjectStreams(plan.backupManifest)}, []inputbinding.Binding{
		{TargetPointer: "/compatibility_result", SourceStepID: provisionCompatibilityStep, SourcePointer: ""},
		{TargetPointer: "/target/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"},
	}}
	inputs[provisionRestoreInspectStep] = provisionStepInput{map[string]any{"backup_id": plan.request.RestoreBackup, "target": target,
		"target_data_generation_id": generation}, []inputbinding.Binding{
		{TargetPointer: "/target/instance_ref", SourceStepID: provisionCreateStep, SourcePointer: "/instance_ref"},
		{TargetPointer: "/restore_execution_ref", SourceStepID: provisionRestoreStep, SourcePointer: "/restore_execution_ref"},
	}}
}

// provisionPlanSteps encodes each step's input and pins its capability.
func provisionPlanSteps(requirements []provisionRequirement, capabilities []adapter.Capability, inputs map[string]provisionStepInput) ([]store.OperationStep, error) {
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
	if !provisionProfileSatisfies(request, profile) {
		return errors.New("trusted server provision profile does not exactly satisfy the selected request")
	}
	if !provisionUnitBindsServer(server, profile.UnitRef) {
		return errors.New("trusted provision unit reference does not bind the configured execution adapter and environment")
	}
	return nil
}

// provisionProfileSatisfies reports whether the trusted profile pins
// capacity and exactly the placement, image, release and readiness checks
// the request selected.
func provisionProfileSatisfies(request provisionRequest, profile ServerProvisionProfile) bool {
	return provisionCapacityPinned(profile) && provisionPlacementMatches(request, profile) && provisionArtifactsMatch(request, profile)
}

func provisionCapacityPinned(profile ServerProvisionProfile) bool {
	return profile.Provider != "" && profile.CapacityGroup != "" && profile.CapacityLimit >= 1 && profile.CapacityGroupLimit >= 1
}

func provisionPlacementMatches(request provisionRequest, profile ServerProvisionProfile) bool {
	return profile.RegionID != "" && profile.RegionID == request.RegionID && profile.PlanID != "" && profile.PlanID == request.PlanID
}

func provisionArtifactsMatch(request provisionRequest, profile ServerProvisionProfile) bool {
	return provisionImageMatches(request, profile) && provisionReleasePinned(request, profile) && provisionReadinessPinned(profile)
}

func provisionImageMatches(request provisionRequest, profile ServerProvisionProfile) bool {
	return isObject(profile.OS) && equalJSON(profile.OS, request.OS) && isObject(profile.Bootstrap) && equalJSON(profile.Bootstrap, request.Bootstrap)
}

func provisionReleasePinned(request provisionRequest, profile ServerProvisionProfile) bool {
	return profile.GameReleaseID != "" && profile.GameReleaseID == request.Game.Release && profile.ArtifactURI != "" && len(profile.ArtifactChecksum) != 0
}

func provisionReadinessPinned(profile ServerProvisionProfile) bool {
	return profile.ReadinessCheckSetID != "" && len(profile.RequiredChecks) != 0
}

// provisionUnitBindsServer reports whether the unit reference is an object
// naming a unit of the server's execution adapter and environment.
func provisionUnitBindsServer(server store.LogicalServer, raw json.RawMessage) bool {
	var unit provisionRef
	if json.Unmarshal(raw, &unit) != nil || !isObject(raw) {
		return false
	}
	return unit.AdapterID == server.ExecutionAdapterID && unit.EnvironmentID == server.EnvironmentID && provisionRefNamed(unit)
}

// provisionRef is an adapter-owned external resource reference.
type provisionRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}

// provisionRefNamed reports whether the reference names a kind and resource.
func provisionRefNamed(ref provisionRef) bool {
	return ref.ResourceKind != "" && ref.ExternalID != ""
}

// provisionRefIs reports whether the reference names a resource of the
// given adapter, environment and kind.
func provisionRefIs(ref provisionRef, adapterID, environmentID, kind string) bool {
	return ref.AdapterID == adapterID && ref.EnvironmentID == environmentID && ref.ResourceKind == kind
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
	if !ok || !provisionRefIs(instance, request.HostingAdapter, operation.Target.EnvironmentID, "instance") {
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
	server, request, err := w.provisionPreconditionState(ctx, operation)
	if err != nil {
		return err
	}
	switch step.StepID {
	case provisionCompatibilityStep:
		if request.RestoreBackup == "" {
			return errors.New("backup compatibility step is not part of this provision request")
		}
		return nil
	case provisionCreateStep:
		return w.checkProvisionCreate(ctx, operation, server, request, step)
	}
	instanceRef, err := provisionInstanceRef(operation, server)
	if err != nil {
		return err
	}
	if err := checkProvisionStepBinding(operation, step, instanceRef); err != nil {
		return err
	}
	rule, ok := provisionPrerequisites[step.StepID]
	if !ok {
		return fmt.Errorf("unsupported server.provision step %q", step.StepID)
	}
	return rule(provisionPrerequisiteCheck{ctx: ctx, workflow: w, operation: operation, server: server, restoring: request.RestoreBackup != ""})
}

// provisionPreconditionState checks the context and operation, then loads
// the target server and decodes the persisted request.
func (w *ServerProvisionWorkflow) provisionPreconditionState(ctx context.Context, operation store.OperationRecord) (store.LogicalServer, provisionRequest, error) {
	if ctx == nil || operation.OperationType != "server.provision" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return store.LogicalServer{}, provisionRequest{}, errors.New("server.provision operation and context are required")
	}
	if err := ctx.Err(); err != nil {
		return store.LogicalServer{}, provisionRequest{}, err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.LogicalServer{}, provisionRequest{}, err
	}
	server := stored.Record
	if server.ServerID != operation.Target.ServerID || server.EnvironmentID != operation.Target.EnvironmentID {
		return store.LogicalServer{}, provisionRequest{}, store.ErrLogicalServerTargetMismatch
	}
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return store.LogicalServer{}, provisionRequest{}, errors.New("persisted provision request cannot be decoded")
	}
	return server, request, nil
}

// checkProvisionCreate allows the provider create only once, for a server
// with no instance yet, and after a compatible backup when restoring.
func (w *ServerProvisionWorkflow) checkProvisionCreate(ctx context.Context, operation store.OperationRecord, server store.LogicalServer, request provisionRequest, step store.OperationStep) error {
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

// provisionInstanceRef is the server's instance, or the one the provider
// create step verifiably returned.
func provisionInstanceRef(operation store.OperationRecord, server store.LogicalServer) (json.RawMessage, error) {
	if len(server.InstanceRef) != 0 {
		return server.InstanceRef, nil
	}
	created, err := provisionCreateStepOutput(operation)
	if err != nil {
		return nil, errors.New("provision step requires a verified created instance")
	}
	return created.InstanceRef, nil
}

// checkProvisionStepBinding requires a unit step to use the created unit on
// the created instance, and any other step to name the created instance.
func checkProvisionStepBinding(operation store.OperationRecord, step store.OperationStep, instanceRef json.RawMessage) error {
	if step.StepID == provisionStartUnitStep || step.StepID == provisionInspectUnitStep {
		createdUnit := operationStep(operation, provisionCreateUnitStep)
		if !equalJSON(stepField(step.Input, "unit_ref"), stepField(createdUnit.Input, "unit_ref")) ||
			!provisionTargetBinds(createdUnit, instanceRef) {
			return fmt.Errorf("provision step %q is not bound to the exact created unit and instance", step.StepID)
		}
		return nil
	}
	if !provisionStepBinds(step, "instance_ref", instanceRef) && !provisionTargetBinds(step, instanceRef) {
		return fmt.Errorf("provision step %q is not bound to the exact created instance", step.StepID)
	}
	return nil
}

// provisionPrerequisiteCheck is what a step's prerequisite rule reads.
type provisionPrerequisiteCheck struct {
	ctx       context.Context
	workflow  *ServerProvisionWorkflow
	operation store.OperationRecord
	server    store.LogicalServer
	restoring bool
}

func (c provisionPrerequisiteCheck) succeeded(stepID string) bool {
	return stepSucceeded(c.operation, stepID)
}

// requirePrerequisite returns message as an error unless ok.
func requirePrerequisite(ok bool, message string) error {
	if ok {
		return nil
	}
	return errors.New(message)
}

// provisionPrerequisites maps each step after the provider create to the
// earlier results it needs before it may be dispatched.
var provisionPrerequisites = map[string]func(provisionPrerequisiteCheck) error{
	provisionWaitStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionCreateStep), "instance wait requires a completed provider create")
	},
	provisionBootstrapStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionWaitStep) && provisionWaitSucceeded(c.operation), "bootstrap requires the exact instance to be confirmed active")
	},
	provisionBootstrapInspect: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionBootstrapStep), "bootstrap inspection requires a completed delivery request")
	},
	provisionInstallStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionBootstrapInspect) && provisionBootstrapSucceeded(c.operation), "game installation requires successful inspection of the pinned bootstrap")
	},
	provisionRestoreStep: func(c provisionPrerequisiteCheck) error {
		profile, err := c.workflow.profiles.ResolveServerProvision(c.ctx, c.server, c.operation.RequestSnapshot.Parameters)
		ok := err == nil && c.succeeded(provisionInstallStep) && provisionInstallSucceeded(c.operation) &&
			provisionCompatibilitySucceeded(c.operation, c.workflow.schemas, profile)
		return requirePrerequisite(ok, "backup restore requires verified installation and a compatible exact backup")
	},
	provisionRestoreInspectStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionRestoreStep), "restore inspection requires the recorded restore call")
	},
	provisionCreateUnitStep: func(c provisionPrerequisiteCheck) error {
		if !c.succeeded(provisionInstallStep) || !provisionInstallSucceeded(c.operation) {
			return errors.New("execution unit creation requires verified installation of the pinned game release")
		}
		return requirePrerequisite(!c.restoring || provisionRestoreSucceeded(c.operation), "execution unit creation requires verified completion of the requested backup restore")
	},
	provisionStartUnitStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionCreateUnitStep) && provisionUnitCreated(c.operation), "execution unit start requires a verified stopped or unchanged unit")
	},
	provisionInspectUnitStep: func(c provisionPrerequisiteCheck) error {
		return requirePrerequisite(c.succeeded(provisionStartUnitStep) && provisionUnitStarted(c.operation), "unit inspection requires a completed start")
	},
	provisionReadinessStep: func(c provisionPrerequisiteCheck) error {
		ok := c.succeeded(provisionInspectUnitStep) && provisionUnitRunning(c.operation) && (!c.restoring || provisionRestoreSucceeded(c.operation))
		return requirePrerequisite(ok, "game readiness requires the exact execution unit to be running")
	},
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
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil || !valid ||
		!provisionRefIs(ref, request.HostingAdapter, operation.Target.EnvironmentID, "instance") {
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
		ReleaseID       string                  `json:"release_id"`
		Outcome         string                  `json:"outcome"`
		Verification    string                  `json:"artifact_verification"`
		Started         bool                    `json:"installation_started"`
		ArtifactResults []installArtifactResult `json:"artifact_results"`
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
		(out.Outcome == "installed" || out.Outcome == "already_installed") && out.Verification == "verified" && installArtifactsVerified(out.ArtifactResults) &&
		out.ReleaseID == expected.ReleaseID && out.Target.ServerID == expected.Target.ServerID && out.Target.EnvironmentID == expected.Target.EnvironmentID && equalJSON(out.Target.InstanceRef, created.InstanceRef)
}

type installArtifactResult struct {
	ArtifactID string          `json:"artifact_id"`
	Result     string          `json:"result"`
	Expected   json.RawMessage `json:"expected_checksum"`
	Observed   json.RawMessage `json:"observed_checksum"`
}

// installArtifactsVerified requires at least one artifact result, each verified with matching pinned and observed checksums.
func installArtifactsVerified(results []installArtifactResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, item := range results {
		if item.ArtifactID == "" || item.Result != "verified" || len(item.Expected) == 0 || !equalJSON(item.Expected, item.Observed) {
			return false
		}
	}
	return true
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

// decodeProvisionRef decodes an object reference that names its adapter,
// environment, kind and resource.
func decodeProvisionRef(raw json.RawMessage) (provisionRef, bool) {
	var ref provisionRef
	if json.Unmarshal(raw, &ref) != nil || !isObject(raw) {
		return ref, false
	}
	return ref, ref.AdapterID != "" && ref.EnvironmentID != "" && provisionRefNamed(ref)
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
	needsOperator := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	resourceKey, ok := provisionCreateResourceKey(operation, step)
	if !ok {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	capability, err := w.provisionReadCapability(step.AdapterID, adapter.CategoryHosting, "hosting.instance.find_by_resource_key",
		"hosting:instance-find-by-resource-key-input", "hosting:instance-find-by-resource-key-output")
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	found, ok := w.invokeProvisionRead(ctx, operation, step, capability, provisionReadInput(map[string]any{"resource_key": resourceKey}))
	if !ok {
		return needsOperator, nil
	}
	lookup, ok := decodeProvisionCreateLookup(found, resourceKey)
	if !ok || !provisionCreateMatchUsable(lookup.Matches[0], step.AdapterID, operation.Target.EnvironmentID) {
		return needsOperator, nil
	}
	if _, err := time.Parse(time.RFC3339Nano, lookup.ObservedAt); err != nil {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	match := lookup.Matches[0]
	// Marshal cannot fail: the instance reference was decoded from JSON.
	output, _ := json.Marshal(provisionCreateOutput{InstanceRef: match.InstanceRef, State: match.State, ObservedAt: lookup.ObservedAt})
	if err := w.schemas.ValidateJSON("urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:instance-create-output", output); err != nil {
		return needsOperator, nil
	}
	return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}, nil
}

// provisionCreateLookup is a provider inventory lookup by resource key.
type provisionCreateLookup struct {
	ResourceKey       string                 `json:"resource_key"`
	InventoryComplete bool                   `json:"inventory_complete"`
	Matches           []provisionCreateMatch `json:"matches"`
	ObservedAt        string                 `json:"observed_at"`
}

// provisionCreateMatch is one instance found by resource key.
type provisionCreateMatch struct {
	InstanceRef json.RawMessage `json:"instance_ref"`
	State       string          `json:"state"`
}

// provisionCreateResourceKey returns the pinned create step's resource key
// when it is the one this operation derives.
func provisionCreateResourceKey(operation store.OperationRecord, step store.OperationStep) (string, bool) {
	if step.CapabilityName != "hosting.instance.create" || step.AdapterID == "" {
		return "", false
	}
	var createInput struct {
		ResourceKey string `json:"resource_key"`
	}
	if json.Unmarshal(step.Input, &createInput) != nil || createInput.ResourceKey != provisionResourceKey(operation.OperationID, operation.RequestFingerprint) {
		return "", false
	}
	return createInput.ResourceKey, true
}

// decodeProvisionCreateLookup decodes a complete inventory lookup for the
// resource key with exactly one match.
func decodeProvisionCreateLookup(output json.RawMessage, resourceKey string) (provisionCreateLookup, bool) {
	var lookup provisionCreateLookup
	if json.Unmarshal(output, &lookup) != nil || lookup.ResourceKey != resourceKey || !lookup.InventoryComplete {
		return lookup, false
	}
	return lookup, len(lookup.Matches) == 1
}

// provisionCreateMatchUsable reports whether the match is an instance of
// the adapter and environment in a state a create can leave it in.
func provisionCreateMatchUsable(match provisionCreateMatch, adapterID, environmentID string) bool {
	ref, ok := decodeProvisionRef(match.InstanceRef)
	return ok && provisionRefIs(ref, adapterID, environmentID, "instance") && provisionCreatedState(match.State)
}

func provisionCreatedState(state string) bool {
	return state == "creating" || state == "active" || state == "stopped"
}

func (w *ServerProvisionWorkflow) reconcileBootstrap(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	needsOperator := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	if step.CapabilityName != "hosting.bootstrap.deliver" {
		return needsOperator, nil
	}
	created, err := provisionCreateStepOutput(operation)
	if err != nil {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	var delivered provisionBootstrapOutput
	if json.Unmarshal(step.Output, &delivered) != nil {
		return needsOperator, nil
	}
	executionRef := provisionExecutionRef(delivered.BootstrapExecutionRef, step.ExternalOperationRef)
	ref, ok := decodeProvisionRef(executionRef)
	if !ok || !provisionRefIs(ref, step.AdapterID, operation.Target.EnvironmentID, "bootstrap-execution") {
		return needsOperator, nil
	}
	capability, err := w.provisionReadCapability(step.AdapterID, adapter.CategoryHosting, "hosting.bootstrap.inspect",
		"hosting:bootstrap-inspect-input", "hosting:bootstrap-execution-output")
	if err != nil {
		return needsOperator, nil
	}
	output, ok := w.invokeProvisionRead(ctx, operation, step, capability,
		provisionReadInput(map[string]any{"instance_ref": created.InstanceRef, "bootstrap_execution_ref": executionRef}))
	if !ok {
		return needsOperator, nil
	}
	inspected, ok := decodeProvisionBootstrapInspection(output, created.InstanceRef, executionRef, delivered)
	if !ok {
		return needsOperator, nil
	}
	return provisionBootstrapResolution(operation, step, inspected.State, output), nil
}

// provisionExecutionRef is the execution reference a step recorded in its
// output, or else its external operation reference.
func provisionExecutionRef(fromOutput, external json.RawMessage) json.RawMessage {
	if len(fromOutput) == 0 {
		return external
	}
	return fromOutput
}

// decodeProvisionBootstrapInspection decodes an inspection of exactly the
// delivered bootstrap execution on the created instance.
func decodeProvisionBootstrapInspection(output, instanceRef, executionRef json.RawMessage, delivered provisionBootstrapOutput) (provisionBootstrapInspection, bool) {
	var inspected provisionBootstrapInspection
	if json.Unmarshal(output, &inspected) != nil {
		return inspected, false
	}
	return inspected, equalJSON(inspected.InstanceRef, instanceRef) && equalJSON(inspected.BootstrapExecutionRef, executionRef) &&
		inspected.Revision == delivered.Revision && inspected.Digest == delivered.Digest
}

// provisionBootstrapResolution maps the inspected bootstrap state.
func provisionBootstrapResolution(operation store.OperationRecord, step store.OperationStep, state string, output json.RawMessage) Resolution {
	switch state {
	case "succeeded":
		return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}
	case "running":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}
	case "failed":
		return Resolution{State: ResolutionDefinitiveError, ExternalOperationRef: step.ExternalOperationRef,
			Error: protocolError("core.bootstrap.failed", "provider_failure", "The exact pinned bootstrap execution is confirmed failed.", operation.OperationID, false, "operator_review", false)}
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	}
}

func (w *ServerProvisionWorkflow) reconcileUnitStart(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	needsOperator := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	if step.CapabilityName != "execution.unit.start" {
		return needsOperator, nil
	}
	inspect := operationStep(operation, provisionInspectUnitStep)
	if inspect.CapabilityName != "execution.unit.inspect" || inspect.AdapterID != step.AdapterID {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	capability, err := w.provisionReadCapability(inspect.AdapterID, adapter.CategoryExecution, "execution.unit.inspect",
		"execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output")
	if err != nil {
		return needsOperator, nil
	}
	unitRef := stepField(step.Input, "unit_ref")
	output, ok := w.invokeProvisionRead(ctx, operation, step, capability, provisionReadInput(map[string]any{"unit_ref": unitRef}))
	if !ok {
		return needsOperator, nil
	}
	var observed provisionUnitInspection
	if json.Unmarshal(output, &observed) != nil || !equalJSON(observed.UnitRef, unitRef) {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	return provisionUnitStartResolution(step, observed), nil
}

// provisionUnitStartResolution maps the inspected unit state.
func provisionUnitStartResolution(step store.OperationStep, observed provisionUnitInspection) Resolution {
	switch observed.State {
	case "running":
		// Marshal cannot fail: the unit reference was decoded from JSON.
		output, _ := json.Marshal(executionUnitStartResult{UnitRef: observed.UnitRef, Outcome: "completed", State: "running", ObservedAt: observed.ObservedAt})
		return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}
	case "stopped", "absent":
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}
	case "starting":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	}
}

// provisionReadCapability resolves the adapter's exact read-only capability
// with the given input and output schemas.
func (w *ServerProvisionWorkflow) provisionReadCapability(adapterID string, category adapter.Category, name, input, output string) (adapter.Capability, error) {
	version := w.schemas.ProtocolVersion()
	return exactCapability(w.adapters, adapterID, category, name,
		"urn:gameserver-controller:"+version+":"+input, "urn:gameserver-controller:"+version+":"+output, false)
}

// provisionReadInput encodes a reconciliation input. Marshal cannot fail:
// the values are strings and JSON references decoded from persisted steps.
func provisionReadInput(fields map[string]any) json.RawMessage {
	input, _ := json.Marshal(fields)
	return input
}

// invokeProvisionRead invokes a reconciliation capability and returns its
// output when the call completed with a schema-valid result.
func (w *ServerProvisionWorkflow) invokeProvisionRead(ctx context.Context, operation store.OperationRecord, step store.OperationStep, capability adapter.Capability, input json.RawMessage) (json.RawMessage, bool) {
	response, err := w.invokeProvision(ctx, operation, step, capability, input)
	if err != nil || response.Result.Outcome != adapter.OutcomeCompleted || w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil {
		return nil, false
	}
	return response.Result.Output, true
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
	if err != nil || !provisionPostconditionsConfirmed(operation) {
		return errors.New("server.provision exact instance, bootstrap, game, or execution postconditions were not confirmed")
	}
	server, err := w.loadProvisionCompletionServer(ctx, operation)
	if err != nil {
		return err
	}
	if err := w.checkProvisionCompletionServer(operation, server, created); err != nil {
		return err
	}
	return w.recordProvisionCapacity(operation, created)
}

// provisionPostconditionsConfirmed reports whether every host, game and
// execution step after the provider create is verified.
func provisionPostconditionsConfirmed(operation store.OperationRecord) bool {
	return provisionHostPostconditions(operation) && provisionUnitPostconditions(operation)
}

func provisionHostPostconditions(operation store.OperationRecord) bool {
	return provisionWaitSucceeded(operation) && provisionBootstrapSucceeded(operation) && provisionInstallSucceeded(operation)
}

func provisionUnitPostconditions(operation store.OperationRecord) bool {
	return provisionUnitCreated(operation) && provisionUnitStarted(operation) && provisionUnitRunning(operation)
}

// loadProvisionCompletionServer loads the target server and, for a
// restore, requires compatible-source and verified-restore evidence.
func (w *ServerProvisionWorkflow) loadProvisionCompletionServer(ctx context.Context, operation store.OperationRecord) (store.LogicalServer, error) {
	var request provisionRequest
	if json.Unmarshal(operation.RequestSnapshot.Parameters, &request) != nil {
		return store.LogicalServer{}, errors.New("server.provision request cannot be decoded")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.LogicalServer{}, err
	}
	if request.RestoreBackup != "" {
		profile, err := w.profiles.ResolveServerProvision(ctx, stored.Record, operation.RequestSnapshot.Parameters)
		if err != nil || !provisionCompatibilitySucceeded(operation, w.schemas, profile) || !provisionRestoreSucceeded(operation) {
			return store.LogicalServer{}, errors.New("server.provision requested restore lacks exact compatible source and verified restore evidence")
		}
	}
	return stored.Record, nil
}

// checkProvisionCompletionServer requires the server to be bound to the
// created instance and the controller-owned generation, with a valid
// readiness assertion.
func (w *ServerProvisionWorkflow) checkProvisionCompletionServer(operation store.OperationRecord, server store.LogicalServer, created provisionCreateOutput) error {
	if !equalJSON(server.InstanceRef, created.InstanceRef) {
		return errors.New("server.provision logical server is not bound to the exact created instance")
	}
	if server.DataGenerationStatus != "known" || server.DataGenerationID != provisionDataGeneration(operation.OperationID) {
		return errors.New("server.provision has no controller-owned data generation for the installed world")
	}
	_, err := BuildReadinessAssertion(w.schemas, operation, server, provisionReadinessStep, w.assertionTTL)
	return err
}

// recordProvisionCapacity makes the reservation active for the exact
// instance, then records its provision-to-ready evidence.
func (w *ServerProvisionWorkflow) recordProvisionCapacity(operation store.OperationRecord, created provisionCreateOutput) error {
	ref, ok := decodeProvisionRef(created.InstanceRef)
	if !ok {
		return errors.New("server.provision provider instance reference is invalid")
	}
	createTime, err := time.Parse(time.RFC3339Nano, created.ObservedAt)
	if err != nil {
		return errors.New("server.provision create observation time is invalid")
	}
	if err := w.activateProvisionCapacity(operation, ref, createTime); err != nil {
		return err
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

// activateProvisionCapacity records the exact instance on a reservation
// whose create outcome is unknown, then requires it to be active for it.
func (w *ServerProvisionWorkflow) activateProvisionCapacity(operation store.OperationRecord, ref provisionRef, createTime time.Time) error {
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
