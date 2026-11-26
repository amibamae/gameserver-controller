package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

var stopPlanRule = newRuntimePlanRule("stop", protocolstate.ServerReady)

// BuildPlan pins the exact instance, runtime unit, release, and capabilities
// before the first dispatch. The first implementation supports graceful stop;
// emergency force-stop requires an approval validator that is not wired yet.
func (w *ServerStopWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if err := checkPlanRequest(ctx, w.planSchemas(), operation, stopPlanRule); err != nil {
		return nil, err
	}
	parameters, gracePeriod, err := decodeStopParameters(operation.RequestSnapshot.Parameters)
	if err != nil {
		return nil, err
	}
	server, err := loadPlannedServer(w.servers, operation, parameters.ExpectedInstance, stopPlanRule)
	if err != nil {
		return nil, err
	}
	if stopDrainUnsupported(server, parameters) {
		return nil, errors.New("configured proxy drain requires a trusted proxy profile and adapter integration")
	}
	profile, err := resolveTrustedProfile(ctx, server, w.profiles.ResolveServerStop, validateStopProfile, "stop")
	if err != nil {
		return nil, err
	}
	rows := stopPlanRows(server, profile, gracePeriod)
	capabilities, err := resolvePlannedCapabilities(w.adapters, w.schemas.ProtocolVersion(), rows)
	if err != nil {
		return nil, err
	}
	return encodePlannedSteps(w.schemas, rows, capabilities)
}

// stopDrainUnsupported reports whether the request needs a proxy drain on a
// server with a network profile; proxy drain is not integrated yet.
func stopDrainUnsupported(server store.LogicalServer, parameters serverStopParameters) bool {
	return server.NetworkProfile != "" && parameters.drainRequested()
}

// decodeStopParameters decodes the request and refuses options the graceful
// stop cannot honour yet, in a fixed order, before it resolves the grace period.
func decodeStopParameters(raw json.RawMessage) (serverStopParameters, time.Duration, error) {
	var parameters serverStopParameters
	if err := json.Unmarshal(raw, &parameters); err != nil {
		return serverStopParameters{}, 0, errors.New("decode server.stop parameters")
	}
	if err := refuseUnsupportedStopOptions(parameters); err != nil {
		return serverStopParameters{}, 0, err
	}
	gracePeriod, err := gracefulStopPeriod(parameters.GracefulTimeoutMS)
	if err != nil {
		return serverStopParameters{}, 0, err
	}
	return parameters, gracePeriod, nil
}

func refuseUnsupportedStopOptions(parameters serverStopParameters) error {
	if parameters.DrainTimeoutMS != nil {
		return errors.New("drain_timeout_ms is unavailable until proxy drain is integrated")
	}
	if parameters.Force {
		return errors.New("force stop is unavailable until exact-target approval validation is wired")
	}
	if parameters.ApprovalID != "" {
		return errors.New("approval_id is only valid for an approved force-stop policy")
	}
	return nil
}

// stopPlanRows is the server.stop plan: inspect the provider instance and the
// runtime unit, request a game save, then stop the pinned unit gracefully.
func stopPlanRows(server store.LogicalServer, profile ServerStopProfile, gracePeriod time.Duration) []plannedRuntimeStep {
	return []plannedRuntimeStep{
		{id: stopInspectInstanceStep, adapterID: adapterIDFromRef(server.InstanceRef), category: adapter.CategoryHosting,
			capability: "hosting.instance.inspect", schemaStem: "hosting:instance-inspect",
			input: map[string]any{"instance_ref": server.InstanceRef}},
		{id: stopInspectUnitStep, adapterID: server.ExecutionAdapterID, category: adapter.CategoryExecution,
			capability: "execution.unit.inspect", schemaStem: "execution:systemd-unit-inspect",
			input: map[string]any{"unit_ref": profile.UnitRef}},
		{id: stopSaveGameStep, adapterID: server.GameAdapterID, category: adapter.CategoryGame,
			capability: "game.save.request", schemaStem: "game:save-request", mutating: true,
			input: map[string]any{
				"target":     map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID, "instance_ref": server.InstanceRef},
				"release_id": profile.GameReleaseID, "reason": "shutdown",
			}},
		{id: stopRuntimeStep, adapterID: server.ExecutionAdapterID, category: adapter.CategoryExecution,
			capability: "execution.unit.graceful_stop", schemaStem: "execution:systemd-unit-graceful-stop", mutating: true,
			input: map[string]any{"unit_ref": profile.UnitRef, "grace_period_seconds": int64(gracePeriod / time.Second)}},
	}
}

func validateStopProfile(server store.LogicalServer, profile ServerStopProfile) error {
	if !boundedProfileID(profile.GameReleaseID, 500) {
		return errors.New("trusted stop profile must resolve a bounded game release")
	}
	if ref, bound := decodeBoundUnitRef(profile.UnitRef, server); !bound || ref.ResourceKind != "systemd-unit" {
		return errors.New("trusted stop profile unit reference does not bind the configured execution adapter and environment")
	}
	return nil
}
