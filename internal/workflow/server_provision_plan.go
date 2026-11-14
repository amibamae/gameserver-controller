package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

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
