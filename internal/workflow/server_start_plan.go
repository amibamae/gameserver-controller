package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

var startPlanRule = newRuntimePlanRule("start", protocolstate.ServerStopped)

// BuildPlan resolves the server's current instance and trusted release profile
// into a durable, schema-checked sequence. All resource references, check IDs,
// versions, and adapter capability versions are stored before dispatch.
func (w *ServerStartWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if err := checkPlanRequest(ctx, w.planSchemas(), operation, startPlanRule); err != nil {
		return nil, err
	}
	var parameters serverStartParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return nil, errors.New("decode server.start parameters")
	}
	server, err := loadPlannedServer(w.servers, operation, parameters.ExpectedInstanceRef, startPlanRule)
	if err != nil {
		return nil, err
	}
	profile, err := resolveTrustedProfile(ctx, server, w.profiles.ResolveServerStart, validateStartProfile, "start")
	if err != nil {
		return nil, err
	}
	rows := startPlanRows(operation, server, profile)
	capabilities, err := resolvePlannedCapabilities(w.adapters, w.schemas.ProtocolVersion(), rows)
	if err != nil {
		return nil, err
	}
	if err := w.verifyGameReadiness(server, profile); err != nil {
		return nil, err
	}
	return requireRuntimeIdentity(encodePlannedSteps(w.schemas, rows, capabilities))
}

// startPlanRows is the server.start plan: inspect the provider instance,
// start the pinned unit, inspect it, then check game readiness.
func startPlanRows(operation store.OperationRecord, server store.LogicalServer, profile ServerStartProfile) []plannedRuntimeStep {
	return []plannedRuntimeStep{
		{id: startInspectInstanceStep, adapterID: adapterIDFromRef(server.InstanceRef), category: adapter.CategoryHosting,
			capability: "hosting.instance.inspect", schemaStem: "hosting:instance-inspect",
			input: map[string]any{"instance_ref": server.InstanceRef}},
		{id: startRuntimeStep, adapterID: server.ExecutionAdapterID, category: adapter.CategoryExecution,
			capability: "execution.unit.start", schemaStem: "execution:systemd-unit-start", mutating: true,
			input: map[string]any{"unit_ref": profile.UnitRef}},
		{id: startVerifyRuntimeStep, adapterID: server.ExecutionAdapterID, category: adapter.CategoryExecution,
			capability: "execution.unit.inspect", schemaStem: "execution:systemd-unit-inspect",
			input: map[string]any{"unit_ref": profile.UnitRef}},
		{id: startReadinessStep, adapterID: server.GameAdapterID, category: adapter.CategoryGame,
			capability: "game.readiness.check", schemaStem: "game:readiness-check",
			input: map[string]any{
				"server_id": operation.Target.ServerID, "environment_id": operation.Target.EnvironmentID,
				"instance_ref": server.InstanceRef, "game_release_id": profile.GameReleaseID,
				"readiness_check_set_id": profile.ReadinessCheckSetID, "required_checks": profile.RequiredChecks,
			}},
	}
}

// verifyGameReadiness requires the configured game adapter to declare the
// profile's exact readiness check set for the server's environment.
func (w *ServerStartWorkflow) verifyGameReadiness(server store.LogicalServer, profile ServerStartProfile) error {
	gameDescriptor, err := w.adapters.Descriptor(server.GameAdapterID)
	if err != nil {
		return fmt.Errorf("resolve game adapter %q: %w", server.GameAdapterID, err)
	}
	if gameDescriptor.Category != adapter.CategoryGame {
		return errors.New("configured game adapter has the wrong category")
	}
	return verifyReadinessSetDeclared(gameDescriptor, server.EnvironmentID, profile)
}

func validateStartProfile(server store.LogicalServer, profile ServerStartProfile) error {
	if !startProfileSelectsBoundedChecks(profile) {
		return errors.New("trusted start profile must select a release and bounded readiness check set")
	}
	if ref, bound := decodeBoundUnitRef(profile.UnitRef, server); !bound || ref.ResourceKind == "" {
		return errors.New("trusted start profile unit reference does not bind the configured execution adapter and environment")
	}
	return validateReadinessChecks(profile.RequiredChecks)
}

// startProfileSelectsBoundedChecks reports whether the profile names a
// bounded release and check set with between 1 and 64 required checks.
func startProfileSelectsBoundedChecks(profile ServerStartProfile) bool {
	return boundedProfileID(profile.GameReleaseID, 200) && boundedProfileID(profile.ReadinessCheckSetID, 200) &&
		len(profile.RequiredChecks) > 0 && len(profile.RequiredChecks) <= 64
}

func validateReadinessChecks(checks []ReadinessRequirement) error {
	seen := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if check.CheckID == "" || check.CheckVersion == "" {
			return errors.New("trusted readiness checks require non-empty IDs and versions")
		}
		if _, duplicate := seen[check.CheckID]; duplicate {
			return fmt.Errorf("trusted readiness profile repeats check %q", check.CheckID)
		}
		seen[check.CheckID] = struct{}{}
	}
	return nil
}

func verifyReadinessSetDeclared(descriptor adapter.Descriptor, environmentID string, profile ServerStartProfile) error {
	gameCapability, declared := readinessCapability(descriptor)
	if !declared {
		return errors.New("game adapter does not declare game.readiness.check")
	}
	// The declarations stay an anonymous struct: its type name is part of the
	// pinned decode error message.
	encoded, err := json.Marshal(gameCapability.Constraints)
	if err != nil {
		return err
	}
	var constraints struct {
		ReadinessCheckSets []struct {
			GameReleaseID       string                 `json:"game_release_id"`
			EnvironmentIDs      []string               `json:"environment_ids"`
			ReadinessCheckSetID string                 `json:"readiness_check_set_id"`
			RequiredChecks      []ReadinessRequirement `json:"required_checks"`
		} `json:"readiness_check_sets"`
	}
	if err := json.Unmarshal(encoded, &constraints); err != nil {
		return fmt.Errorf("decode game adapter readiness declarations: %w", err)
	}
	for _, declared := range constraints.ReadinessCheckSets {
		if declared.GameReleaseID != profile.GameReleaseID || declared.ReadinessCheckSetID != profile.ReadinessCheckSetID ||
			!containsString(declared.EnvironmentIDs, environmentID) || !sameReadinessChecks(declared.RequiredChecks, profile.RequiredChecks) {
			continue
		}
		return nil
	}
	return errors.New("trusted readiness profile does not match an exact game-adapter release, environment, set, and check-version declaration")
}

// readinessCapability returns the descriptor's first game.readiness.check
// declaration, if any.
func readinessCapability(descriptor adapter.Descriptor) (adapter.Capability, bool) {
	for _, capability := range descriptor.Capabilities {
		if capability.Name == "game.readiness.check" {
			return capability, true
		}
	}
	return adapter.Capability{}, false
}

func sameReadinessChecks(left, right []ReadinessRequirement) bool {
	if len(left) != len(right) {
		return false
	}
	versions := make(map[string]string, len(left))
	for _, check := range left {
		if check.CheckID == "" || check.CheckVersion == "" {
			return false
		}
		if _, duplicate := versions[check.CheckID]; duplicate {
			return false
		}
		versions[check.CheckID] = check.CheckVersion
	}
	for _, check := range right {
		version, exists := versions[check.CheckID]
		if !exists || version != check.CheckVersion {
			return false
		}
		delete(versions, check.CheckID)
	}
	return len(versions) == 0
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
