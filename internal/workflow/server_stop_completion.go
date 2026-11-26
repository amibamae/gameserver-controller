package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerStopWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.stop" {
		return errors.New("server.stop completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !gameSaveConfirmed(operation) {
		return errors.New("game-native save was not confirmed")
	}
	stopped, err := stopRuntimeOutput(operation)
	if err != nil || !unitStopConfirmed(stopped, stopUnitRef(operation)) {
		return errors.New("exact execution unit is not confirmed stopped")
	}
	if _, err := time.Parse(time.RFC3339Nano, stopped.ObservedAt); err != nil {
		return errors.New("execution stop output has an invalid observation time")
	}
	return nil
}

// unitStopConfirmed reports whether a graceful stop result confirms the
// exact planned unit stopped.
func unitStopConfirmed(stopped executionUnitStopResult, unitRef json.RawMessage) bool {
	return stopped.Outcome == "completed" && stopped.State == "stopped" && equalJSON(stopped.UnitRef, unitRef)
}

func (w *ServerStopWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	// ValidateCompletion has just proved that both outputs decode.
	save, _ := stopSaveOutput(operation)
	stop, _ := stopRuntimeOutput(operation)
	parameters := serverStopParameters{}
	// Planning refused undecodable parameters; the fields that decode here
	// only select the reported proxy-drain outcome.
	_ = json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters)
	result := lifecycleResult{
		operationType: "server.stop", summary: "Game data was saved and the exact runtime unit was confirmed stopped.",
		affected: []any{server.Record.InstanceRef},
	}.document(operation)
	result["operation_data"] = map[string]any{
		"server_id": server.Record.ServerID, "environment_id": server.Record.EnvironmentID,
		"instance_ref": server.Record.InstanceRef, "lifecycle_state": string(protocolstate.ServerStopped),
		"game_save":   map[string]any{"outcome": "confirmed", "observed_at": save.ObservedAt},
		"execution":   map[string]any{"unit_ref": stop.UnitRef, "outcome": "stopped", "observed_at": stop.ObservedAt},
		"proxy_drain": map[string]any{"outcome": stopProxyDrainOutcome(server.Record.NetworkProfile, parameters.drainRequested())},
	}
	return json.Marshal(result)
}

func (w *ServerStopWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	result := lifecycleResult{
		operationType: "server.stop", summary: "Server stop did not confirm both the game save and runtime termination.",
		failed:  failedStepSummaries(step, failure),
		partial: gameSaveConfirmed(operation) || operationStep(operation, stopRuntimeStep).Attempt > 0,
	}
	if len(operation.Target.InstanceRef) > 0 {
		result.affected = []any{operation.Target.InstanceRef}
	}
	if step.StepID == "" {
		result.unresolved = []any{map[string]any{
			"condition_id": "server.stop.postcondition_unmet",
			"description":  "The operation did not establish exact game-save and stopped-unit evidence.",
		}}
	}
	return json.Marshal(result.document(operation))
}
