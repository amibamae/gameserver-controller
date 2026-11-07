package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	stopInspectInstanceStep = "inspect-instance"
	stopInspectUnitStep     = "inspect-runtime"
	stopSaveGameStep        = "save-game-data"
	stopRuntimeStep         = "stop-runtime"
)

const defaultGracefulStopTimeout = 2 * time.Minute

// ServerStopProfile contains trusted release and execution references selected
// from controller-owned deployment metadata.
type ServerStopProfile struct {
	GameReleaseID string
	UnitRef       json.RawMessage
}

// ServerStopProfileResolver must resolve immutable trusted metadata. Client
// parameters cannot choose the game release or execution unit.
type ServerStopProfileResolver interface {
	ResolveServerStop(context.Context, store.LogicalServer) (ServerStopProfile, error)
}

type serverStopParameters struct {
	DrainTimeoutMS    *int64          `json:"drain_timeout_ms,omitempty"`
	Drain             *bool           `json:"drain,omitempty"`
	GracefulTimeoutMS *int64          `json:"graceful_timeout_ms,omitempty"`
	Force             bool            `json:"force,omitempty"`
	ApprovalID        string          `json:"approval_id,omitempty"`
	ExpectedInstance  json.RawMessage `json:"expected_instance_ref,omitempty"`
}

type ServerStopWorkflow struct {
	servers  store.LifecycleOperationStore
	adapters adapterRuntime
	schemas  schemaRuntime
	profiles ServerStopProfileResolver
}

func NewServerStopWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime, profiles ServerStopProfileResolver) (*ServerStopWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, and trusted stop-profile resolver are required")
	}
	return &ServerStopWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles}, nil
}

// BuildPlan pins the exact instance, runtime unit, release, and capabilities
// before the first dispatch. The first implementation supports graceful stop;
// emergency force-stop requires an approval validator that is not wired yet.
func (w *ServerStopWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if ctx == nil || w == nil || w.servers == nil || w.adapters == nil || w.schemas == nil || w.profiles == nil {
		return nil, errors.New("initialized stop workflow and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if operation.OperationType != "server.stop" || operation.ProtocolVersion != w.schemas.ProtocolVersion() ||
		operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" ||
		operation.RequestSnapshot.OperationType != "server.stop" || !isObject(operation.RequestSnapshot.Parameters) {
		return nil, errors.New("server.stop operation and loaded protocol version are required")
	}
	var parameters serverStopParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return nil, errors.New("decode server.stop parameters")
	}
	if parameters.DrainTimeoutMS != nil {
		return nil, errors.New("drain_timeout_ms is unavailable until proxy drain is integrated")
	}
	if parameters.Force {
		return nil, errors.New("force stop is unavailable until exact-target approval validation is wired")
	}
	if parameters.ApprovalID != "" {
		return nil, errors.New("approval_id is only valid for an approved force-stop policy")
	}
	gracePeriod, err := gracefulStopPeriod(parameters.GracefulTimeoutMS)
	if err != nil {
		return nil, err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, fmt.Errorf("load logical server for stop plan: %w", err)
	}
	server := stored.Record
	if server.LifecycleState != protocolstate.ServerReady || len(server.InstanceRef) == 0 {
		return nil, errors.New("server.stop requires a ready logical server with a managed instance")
	}
	if len(operation.Target.InstanceRef) > 0 && !equalJSON(operation.Target.InstanceRef, server.InstanceRef) {
		return nil, store.ErrLogicalServerTargetMismatch
	}
	if len(parameters.ExpectedInstance) > 0 && !equalJSON(parameters.ExpectedInstance, server.InstanceRef) {
		return nil, errors.New("expected_instance_ref does not match the current managed instance")
	}
	drain := true
	if parameters.Drain != nil {
		drain = *parameters.Drain
	}
	if server.NetworkProfile != "" && drain {
		return nil, errors.New("configured proxy drain requires a trusted proxy profile and adapter integration")
	}
	profile, err := w.profiles.ResolveServerStop(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("resolve trusted server stop profile: %w", err)
	}
	if err := validateStopProfile(server, profile); err != nil {
		return nil, err
	}

	version := w.schemas.ProtocolVersion()
	requirements := []struct {
		id, adapterID string
		category      adapter.Category
		name          string
		input, output string
		mutating      bool
	}{
		{stopInspectInstanceStep, adapterIDFromRef(server.InstanceRef), adapter.CategoryHosting,
			"hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false},
		{stopInspectUnitStep, server.ExecutionAdapterID, adapter.CategoryExecution,
			"execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false},
		{stopSaveGameStep, server.GameAdapterID, adapter.CategoryGame,
			"game.save.request", "game:save-request-input", "game:save-request-output", true},
		{stopRuntimeStep, server.ExecutionAdapterID, adapter.CategoryExecution,
			"execution.unit.graceful_stop", "execution:systemd-unit-graceful-stop-input", "execution:systemd-unit-graceful-stop-output", true},
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

	inputs := []any{
		map[string]any{"instance_ref": server.InstanceRef},
		map[string]any{"unit_ref": profile.UnitRef},
		map[string]any{
			"target":     map[string]any{"server_id": server.ServerID, "environment_id": server.EnvironmentID, "instance_ref": server.InstanceRef},
			"release_id": profile.GameReleaseID, "reason": "shutdown",
		},
		map[string]any{"unit_ref": profile.UnitRef, "grace_period_seconds": int64(gracePeriod / time.Second)},
	}
	steps := make([]store.OperationStep, len(requirements))
	for i, requirement := range requirements {
		encoded, err := json.Marshal(inputs[i])
		if err != nil {
			return nil, fmt.Errorf("encode %s input: %w", requirement.id, err)
		}
		if err := w.schemas.ValidateJSON(capabilities[i].InputSchemaURI, encoded); err != nil {
			return nil, fmt.Errorf("planned %s input does not match its declared capability schema: %w", requirement.id, err)
		}
		steps[i] = store.OperationStep{
			StepID: requirement.id, StepType: capabilities[i].Name, Status: protocolstate.StepPending,
			AdapterID: requirement.adapterID, CapabilityName: capabilities[i].Name,
			CapabilityVersion: capabilities[i].Version, Input: encoded,
		}
	}
	return steps, nil
}

func gracefulStopPeriod(timeoutMS *int64) (time.Duration, error) {
	if timeoutMS == nil {
		return defaultGracefulStopTimeout, nil
	}
	if *timeoutMS < 1 || *timeoutMS > int64(time.Hour/time.Millisecond) {
		return 0, errors.New("graceful_timeout_ms must be between 1 and 3600000 for the systemd stop adapter")
	}
	seconds := (*timeoutMS + 999) / 1000
	return time.Duration(seconds) * time.Second, nil
}

func stopProxyDrainOutcome(networkProfile string, requested bool) string {
	if !requested {
		return "not_requested"
	}
	if networkProfile == "" {
		return "not_configured"
	}
	return "unsupported"
}

func validateStopProfile(server store.LogicalServer, profile ServerStopProfile) error {
	if profile.GameReleaseID == "" || len(profile.GameReleaseID) > 500 {
		return errors.New("trusted stop profile must resolve a bounded game release")
	}
	var ref struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if err := json.Unmarshal(profile.UnitRef, &ref); err != nil || !isObject(profile.UnitRef) ||
		ref.AdapterID != server.ExecutionAdapterID || ref.EnvironmentID != server.EnvironmentID ||
		ref.ResourceKind != "systemd-unit" || ref.ExternalID == "" {
		return errors.New("trusted stop profile unit reference does not bind the configured execution adapter and environment")
	}
	return nil
}

func (w *ServerStopWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || w == nil || operation.OperationType != "server.stop" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.stop operation and context are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	server := stored.Record
	if len(server.InstanceRef) == 0 || (len(operation.Target.InstanceRef) > 0 && !equalJSON(operation.Target.InstanceRef, server.InstanceRef)) {
		return store.ErrLogicalServerTargetMismatch
	}
	if !stepBindsRef(operationStep(operation, stopInspectInstanceStep).Input, "instance_ref", server.InstanceRef) {
		return errors.New("server.stop plan is not bound to the current managed instance")
	}
	switch step.StepID {
	case stopInspectInstanceStep:
		if server.LifecycleState != protocolstate.ServerReady {
			return errors.New("server must remain ready until the exact provider instance is inspected")
		}
	case stopInspectUnitStep:
		if server.LifecycleState != protocolstate.ServerReady || !stepSucceeded(operation, stopInspectInstanceStep) {
			return errors.New("runtime inspection requires the ready server and successful provider inspection")
		}
		if observation, err := stopInstanceOutput(operation); err != nil || !equalJSON(observation.InstanceRef, server.InstanceRef) || observation.State != "active" {
			return errors.New("runtime inspection requires the exact provider instance to be active")
		}
	case stopSaveGameStep:
		if server.LifecycleState != protocolstate.ServerReady || !stepSucceeded(operation, stopInspectUnitStep) {
			return errors.New("game save requires a ready server and successful runtime inspection")
		}
		if observation, err := stopInstanceOutput(operation); err != nil || !equalJSON(observation.InstanceRef, server.InstanceRef) || observation.State != "active" {
			return errors.New("game save requires the exact provider instance to remain active")
		}
		if observation, err := stopUnitInspectOutput(operation, stopInspectUnitStep); err != nil || !equalJSON(observation.UnitRef, stopUnitRef(operation)) || observation.State != "running" {
			return errors.New("game save requires the exact execution unit to be running")
		}
	case stopRuntimeStep:
		if server.LifecycleState != protocolstate.ServerStopping || !stepSucceeded(operation, stopSaveGameStep) || !gameSaveConfirmed(operation) {
			return errors.New("graceful stop requires a confirmed game save and stopping lifecycle state")
		}
		if !sameStopStepReference(operation, stopInspectUnitStep, stopRuntimeStep, "unit_ref") {
			return errors.New("graceful stop must target the exact inspected execution unit")
		}
	default:
		return fmt.Errorf("unsupported server.stop step %q", step.StepID)
	}
	return nil
}

func (w *ServerStopWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	switch step.StepID {
	case stopSaveGameStep:
		// The current game capability has no read-only save-status capability.
		// An accepted or unknown save cannot be inferred from process state.
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	case stopRuntimeStep:
		return w.reconcileRuntimeStop(ctx, operation, step)
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

func (w *ServerStopWorkflow) reconcileRuntimeStop(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	inspect := operationStep(operation, stopInspectUnitStep)
	if inspect.StepID == "" || inspect.CapabilityName != "execution.unit.inspect" || inspect.AdapterID != step.AdapterID ||
		inspect.CapabilityVersion == "" || !stepBindsRef(inspect.Input, "unit_ref", stopUnitRef(operation)) {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	capability, err := exactCapability(w.adapters, inspect.AdapterID, adapter.CategoryExecution, inspect.CapabilityName,
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-output", false)
	if err != nil || capability.Version != inspect.CapabilityVersion {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	deadline := time.Now().UTC().Add(15 * time.Second)
	requestID := stopReconcileRequestID(operation.OperationID, step.StepID, step.Attempt)
	correlation := operation.CorrelationID
	if correlation == "" {
		correlation = operation.OperationID
	}
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: operation.ProtocolVersion, Capability: capability.Name,
		RequestContext: adapter.RequestContext{RequestID: requestID, CorrelationID: correlation, OperationID: operation.OperationID,
			StepID: step.StepID, ServerID: operation.Target.ServerID, EnvironmentID: operation.Target.EnvironmentID, Deadline: deadline},
		Input: append(json.RawMessage(nil), inspect.Input...),
	}}
	callContext, cancel := context.WithDeadline(ctx, deadline)
	response, invokeErr := w.adapters.Invoke(callContext, inspect.AdapterID, request)
	cancel()
	if invokeErr != nil || response.JSONRPC != "2.0" || response.ID != requestID || response.Result.Outcome != adapter.OutcomeCompleted {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	if err := w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output); err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var observed stopUnitInspection
	if err := json.Unmarshal(response.Result.Output, &observed); err != nil || !equalJSON(observed.UnitRef, stopUnitRef(operation)) {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	if _, err := time.Parse(time.RFC3339Nano, observed.ObservedAt); err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	switch observed.State {
	case "stopped", "absent":
		if observed.State != "stopped" {
			return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
		}
		output, _ := json.Marshal(map[string]any{"unit_ref": observed.UnitRef, "outcome": "completed", "state": "stopped", "observed_at": observed.ObservedAt})
		return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "running":
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "stopping":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}, nil
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

func (w *ServerStopWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	var message string
	switch step.StepID {
	case stopInspectUnitStep, stopSaveGameStep:
		observation, err := stopInstanceOutput(operation)
		if err != nil || !equalJSON(observation.InstanceRef, stepField(operationStep(operation, stopInspectInstanceStep).Input, "instance_ref")) {
			return Resolution{State: ResolutionNeedsOperator}, nil
		}
		if observation.State != "active" {
			switch observation.State {
			case "creating", "stopped", "destroying", "destroyed", "failed":
				message = "The exact provider instance is not active; no game-save or runtime-stop call was issued."
			default:
				return Resolution{State: ResolutionNeedsOperator}, nil
			}
		} else if step.StepID == stopSaveGameStep {
			runtime, err := stopUnitInspectOutput(operation, stopInspectUnitStep)
			if err != nil || !equalJSON(runtime.UnitRef, stopUnitRef(operation)) {
				return Resolution{State: ResolutionNeedsOperator}, nil
			}
			switch runtime.State {
			case "absent", "stopped", "failed":
				message = "The exact execution unit is not running; no game-save or stop call was issued."
			default:
				return Resolution{State: ResolutionNeedsOperator}, nil
			}
		}
	case stopRuntimeStep:
		if gameSaveRejected(operation) {
			message = "The game adapter rejected its shutdown save; the execution unit was not stopped."
		} else {
			return Resolution{State: ResolutionNeedsOperator}, nil
		}
	default:
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	return Resolution{State: ResolutionDefinitiveError, Error: protocolError("core.safety.precondition_unmet", "safety_blocked", message, operation.OperationID, false, "inspect_operation", false)}, nil
}

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
	var stopped executionUnitStopResult
	step := operationStep(operation, stopRuntimeStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &stopped) != nil ||
		stopped.Outcome != "completed" || stopped.State != "stopped" || !equalJSON(stopped.UnitRef, stopUnitRef(operation)) {
		return errors.New("exact execution unit is not confirmed stopped")
	}
	if _, err := time.Parse(time.RFC3339Nano, stopped.ObservedAt); err != nil {
		return errors.New("execution stop output has an invalid observation time")
	}
	return nil
}

func (w *ServerStopWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	save, _ := stopSaveOutput(operation)
	stop, _ := stopRuntimeOutput(operation)
	parameters := serverStopParameters{}
	_ = json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters)
	result := map[string]any{
		"outcome_summary": "Game data was saved and the exact runtime unit was confirmed stopped.",
		"operation_type":  "server.stop", "affected_resources": []any{server.Record.InstanceRef},
		"completed_steps": completedStepSummaries(operation), "failed_steps": []any{},
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": false,
		"operation_data": map[string]any{
			"server_id": server.Record.ServerID, "environment_id": server.Record.EnvironmentID,
			"instance_ref": server.Record.InstanceRef, "lifecycle_state": string(protocolstate.ServerStopped),
			"game_save":   map[string]any{"outcome": "confirmed", "observed_at": save.ObservedAt},
			"execution":   map[string]any{"unit_ref": stop.UnitRef, "outcome": "stopped", "observed_at": stop.ObservedAt},
			"proxy_drain": map[string]any{"outcome": stopProxyDrainOutcome(server.Record.NetworkProfile, parameters.Drain == nil || *parameters.Drain)},
		},
	}
	return json.Marshal(result)
}

func (w *ServerStopWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	failed := []any{}
	if step.StepID != "" && len(failure) > 0 && step.Status == protocolstate.StepFailed {
		failed = append(failed, map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure})
	}
	partial := gameSaveConfirmed(operation) || operationStep(operation, stopRuntimeStep).Attempt > 0
	result := map[string]any{
		"outcome_summary": "Server stop did not confirm both the game save and runtime termination.",
		"operation_type":  "server.stop", "affected_resources": []any{},
		"completed_steps": completedStepSummaries(operation), "failed_steps": failed,
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": partial,
	}
	if len(operation.Target.InstanceRef) > 0 {
		result["affected_resources"] = []any{operation.Target.InstanceRef}
	}
	if step.StepID == "" {
		result["unresolved_conditions"] = []any{map[string]any{
			"condition_id": "server.stop.postcondition_unmet",
			"description":  "The operation did not establish exact game-save and stopped-unit evidence.",
		}}
	}
	return json.Marshal(result)
}

func (w *ServerStopWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if operation.OperationType != "server.stop" || current.Record.ServerID != operation.Target.ServerID || current.Record.EnvironmentID != operation.Target.EnvironmentID {
		return store.LogicalServer{}, false, errors.New("server.stop lifecycle projection target mismatch")
	}
	next := current.Record
	saveStep := operationStep(operation, stopSaveGameStep)
	switch current.Record.LifecycleState {
	case protocolstate.ServerReady:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerDegraded
			return next, true, nil
		}
		if saveStep.Status != protocolstate.StepPending {
			next.LifecycleState = protocolstate.ServerStopping
			next.DesiredState = "stopped"
			return next, true, nil
		}
	case protocolstate.ServerStopping:
		if operation.Status == protocolstate.OperationSucceeded {
			if !hasValidStopResult(operation.Result, operation.Target, current.Record.InstanceRef, w.schemas) {
				return store.LogicalServer{}, false, errors.New("terminal server.stop result lacks exact save and termination evidence")
			}
			next.LifecycleState = protocolstate.ServerStopped
			next.DesiredState = "stopped"
			return next, true, nil
		}
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			return next, true, nil
		}
	}
	return current.Record, false, nil
}

func hasValidStopResult(raw json.RawMessage, target store.OperationTarget, instanceRef json.RawMessage, schemas schemaRuntime) bool {
	var result struct {
		OperationType string `json:"operation_type"`
		OperationData struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
			State         string          `json:"lifecycle_state"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.OperationType != "server.stop" ||
		result.OperationData.ServerID != target.ServerID || result.OperationData.EnvironmentID != target.EnvironmentID ||
		result.OperationData.State != string(protocolstate.ServerStopped) || !equalJSON(result.OperationData.InstanceRef, instanceRef) {
		return false
	}
	return (len(target.InstanceRef) == 0 || equalJSON(result.OperationData.InstanceRef, target.InstanceRef)) &&
		schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-stop-result", raw) == nil
}

type stopUnitInspection struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

type gameSaveObservation struct {
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

type executionUnitStopResult struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	Outcome    string          `json:"outcome"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

func stopInstanceOutput(operation store.OperationRecord) (instanceInspection, error) {
	return instanceStepOutput(operation, stopInspectInstanceStep)
}

func stopUnitInspectOutput(operation store.OperationRecord, id string) (stopUnitInspection, error) {
	var output stopUnitInspection
	step := operationStep(operation, id)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
		return output, errors.New("runtime inspection output is missing")
	}
	if err := json.Unmarshal(step.Output, &output); err != nil {
		return stopUnitInspection{}, err
	}
	return output, nil
}

func stopUnitRef(operation store.OperationRecord) json.RawMessage {
	return stepField(operationStep(operation, stopRuntimeStep).Input, "unit_ref")
}

func sameStopStepReference(operation store.OperationRecord, first, second, field string) bool {
	return stepBindsRef(operationStep(operation, first).Input, field, stepField(operationStep(operation, second).Input, field))
}

func gameSaveConfirmed(operation store.OperationRecord) bool {
	save, err := stopSaveOutput(operation)
	return err == nil && save.Outcome == "confirmed" && save.SavedAt != "" &&
		stopSaveBinds(operation, save)
}

func gameSaveRejected(operation store.OperationRecord) bool {
	save, err := stopSaveOutput(operation)
	return err == nil && save.Outcome == "rejected" && stopSaveBinds(operation, save)
}

func stopSaveBinds(operation store.OperationRecord, save gameSaveObservation) bool {
	var input struct {
		Target struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		ReleaseID string `json:"release_id"`
	}
	if json.Unmarshal(operationStep(operation, stopSaveGameStep).Input, &input) != nil {
		return false
	}
	target := operation.Target
	plannedInstance := stepField(operationStep(operation, stopInspectInstanceStep).Input, "instance_ref")
	return save.Target.ServerID == target.ServerID && save.Target.EnvironmentID == target.EnvironmentID &&
		save.ReleaseID == input.ReleaseID && equalJSON(save.Target.InstanceRef, input.Target.InstanceRef) &&
		equalJSON(input.Target.InstanceRef, plannedInstance)
}

func stopSaveOutput(operation store.OperationRecord) (gameSaveObservation, error) {
	var output gameSaveObservation
	step := operationStep(operation, stopSaveGameStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded {
		return output, errors.New("game save output is missing")
	}
	if err := json.Unmarshal(step.Output, &output); err != nil {
		return gameSaveObservation{}, err
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return gameSaveObservation{}, err
	}
	if output.Outcome == "confirmed" {
		if _, err := time.Parse(time.RFC3339Nano, output.SavedAt); err != nil {
			return gameSaveObservation{}, err
		}
	}
	return output, nil
}

func stopRuntimeOutput(operation store.OperationRecord) (executionUnitStopResult, error) {
	var output executionUnitStopResult
	step := operationStep(operation, stopRuntimeStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return output, errors.New("graceful stop output is missing")
	}
	return output, nil
}

func stopReconcileRequestID(operationID, stepID string, attempt int) string {
	digest := sha256.Sum256([]byte(operationID + "\x00" + stepID + "\x00reconcile-stop\x00" + fmt.Sprint(attempt)))
	return "reconcile-" + hex.EncodeToString(digest[:16])
}
