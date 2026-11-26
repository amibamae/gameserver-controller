package workflow

import (
	"context"
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

// planSchemas returns the schemas when w holds every dependency planning
// needs, and nil otherwise, which checkPlanRequest refuses.
func (w *ServerStopWorkflow) planSchemas() schemaRuntime {
	if w == nil || w.profiles == nil || !lifecycleRuntimeReady(w.servers, w.adapters, w.schemas) {
		return nil
	}
	return w.schemas
}

// drainRequested reports whether the request asks for proxy drain, which is
// the default.
func (p serverStopParameters) drainRequested() bool {
	return p.Drain == nil || *p.Drain
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

var stopPlanRule = runtimePlanRule{
	operationType:  "server.stop",
	initError:      "initialized stop workflow and context are required",
	operationError: "server.stop operation and loaded protocol version are required",
	label:          "stop plan",
	state:          protocolstate.ServerReady,
	stateError:     "server.stop requires a ready logical server with a managed instance",
}

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

// stopStepCheck rechecks one planned server.stop step against the stored
// server and the evidence of the steps before it.
type stopStepCheck func(store.OperationRecord, store.LogicalServer) error

var stopStepChecks = map[string]stopStepCheck{
	stopInspectInstanceStep: checkStopInspectInstance,
	stopInspectUnitStep:     checkStopInspectUnit,
	stopSaveGameStep:        checkStopSaveGame,
	stopRuntimeStep:         checkStopRuntime,
}

func (w *ServerStopWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || w == nil || operation.OperationType != "server.stop" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.stop operation and context are required")
	}
	server, err := loadPreconditionServer(ctx, w.servers, operation)
	if err != nil {
		return err
	}
	if !stepBindsRef(operationStep(operation, stopInspectInstanceStep).Input, "instance_ref", server.InstanceRef) {
		return errors.New("server.stop plan is not bound to the current managed instance")
	}
	check, supported := stopStepChecks[step.StepID]
	if !supported {
		return fmt.Errorf("unsupported server.stop step %q", step.StepID)
	}
	return check(operation, server)
}

func checkStopInspectInstance(_ store.OperationRecord, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerReady {
		return errors.New("server must remain ready until the exact provider instance is inspected")
	}
	return nil
}

func checkStopInspectUnit(operation store.OperationRecord, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerReady || !stepSucceeded(operation, stopInspectInstanceStep) {
		return errors.New("runtime inspection requires the ready server and successful provider inspection")
	}
	if !instanceObservedActive(operation, stopInspectInstanceStep, server.InstanceRef) {
		return errors.New("runtime inspection requires the exact provider instance to be active")
	}
	return nil
}

func checkStopSaveGame(operation store.OperationRecord, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerReady || !stepSucceeded(operation, stopInspectUnitStep) {
		return errors.New("game save requires a ready server and successful runtime inspection")
	}
	if !instanceObservedActive(operation, stopInspectInstanceStep, server.InstanceRef) {
		return errors.New("game save requires the exact provider instance to remain active")
	}
	if !stopUnitObservedRunning(operation) {
		return errors.New("game save requires the exact execution unit to be running")
	}
	return nil
}

func checkStopRuntime(operation store.OperationRecord, server store.LogicalServer) error {
	if server.LifecycleState != protocolstate.ServerStopping || !stepSucceeded(operation, stopSaveGameStep) || !gameSaveConfirmed(operation) {
		return errors.New("graceful stop requires a confirmed game save and stopping lifecycle state")
	}
	if !sameStopStepReference(operation, stopInspectUnitStep, stopRuntimeStep, "unit_ref") {
		return errors.New("graceful stop must target the exact inspected execution unit")
	}
	return nil
}

func (w *ServerStopWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	message, proven := stopPreconditionFailure(operation, step.StepID)
	if !proven {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	return preconditionUnmet(operation.OperationID, message), nil
}

// stopPreconditionFailure reports whether stored, target-bound evidence
// proves the step's precondition unmet, and the message to record.
func stopPreconditionFailure(operation store.OperationRecord, stepID string) (string, bool) {
	switch stepID {
	case stopInspectUnitStep, stopSaveGameStep:
		return stopInactiveTargetFailure(operation, stepID)
	case stopRuntimeStep:
		if gameSaveRejected(operation) {
			return "The game adapter rejected its shutdown save; the execution unit was not stopped.", true
		}
	}
	return "", false
}

// stopInactiveTargetFailure proves that the provider instance, or for the
// game save the execution unit, cannot serve the next stop step.
func stopInactiveTargetFailure(operation store.OperationRecord, stepID string) (string, bool) {
	observation, observed := plannedInstanceObservation(operation, stopInspectInstanceStep)
	if !observed {
		return "", false
	}
	if observation.State != "active" {
		if instanceDefinitivelyInactive(observation.State) {
			return "The exact provider instance is not active; no game-save or runtime-stop call was issued.", true
		}
		return "", false
	}
	if stepID != stopSaveGameStep {
		// Kept verbatim: an active instance before runtime inspection has
		// always been recorded as a terminal failure with an empty message.
		return "", true
	}
	return stopUnitNotRunningFailure(operation)
}

func stopUnitNotRunningFailure(operation store.OperationRecord) (string, bool) {
	runtime, err := stopUnitInspectOutput(operation, stopInspectUnitStep)
	if err != nil || !equalJSON(runtime.UnitRef, stopUnitRef(operation)) {
		return "", false
	}
	switch runtime.State {
	case "absent", "stopped", "failed":
		return "The exact execution unit is not running; no game-save or stop call was issued.", true
	}
	return "", false
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

// reconcileRuntimeStop resolves an ambiguous graceful stop by inspecting the
// exact planned unit with the inspection capability pinned in the plan.
func (w *ServerStopWorkflow) reconcileRuntimeStop(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	needsOperator := Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	inspect := operationStep(operation, stopInspectUnitStep)
	unitRef := stopUnitRef(operation)
	if !pinnedUnitInspect(inspect, step) || !stepBindsRef(inspect.Input, "unit_ref", unitRef) {
		return needsOperator, nil
	}
	observed, ok := observeUnit(ctx, w.adapters, w.schemas, unitProbe{
		operation: operation, step: step, inspect: inspect, unitRef: unitRef,
		requestID: stopReconcileRequestID(operation.OperationID, step.StepID, step.Attempt),
	})
	if !ok {
		return needsOperator, nil
	}
	return stopUnitResolution(step, observed), nil
}

// stopUnitResolution maps the observed state of the exact unit to the
// resolution of its ambiguous graceful stop. An absent unit needs an
// operator because a graceful stop leaves the unit stopped, not absent.
func stopUnitResolution(step store.OperationStep, observed executionUnitInspection) Resolution {
	switch observed.State {
	case "stopped":
		return Resolution{State: ResolutionCompleted, Output: stoppedUnitOutput(observed), ExternalOperationRef: step.ExternalOperationRef}
	case "running":
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}
	case "stopping":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
	}
}

func stoppedUnitOutput(observed executionUnitInspection) json.RawMessage {
	// The map holds strings and a unit reference decoded from valid JSON, so
	// encoding it cannot fail.
	output, _ := json.Marshal(map[string]any{"unit_ref": observed.UnitRef, "outcome": "completed", "state": "stopped", "observed_at": observed.ObservedAt})
	return output
}

func stopReconcileRequestID(operationID, stepID string, attempt int) string {
	return reconcileRequestID(operationID, stepID, "reconcile-stop", attempt)
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

func (w *ServerStopWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if !projectionTargets(operation, "server.stop", current.Record) {
		return store.LogicalServer{}, false, errors.New("server.stop lifecycle projection target mismatch")
	}
	next := current.Record
	saveStep := operationStep(operation, stopSaveGameStep)
	switch current.Record.LifecycleState {
	case protocolstate.ServerReady:
		return w.handleLifecycleReady(next, operation, saveStep)
	case protocolstate.ServerStopping:
		return w.handleLifecycleStopping(next, operation, saveStep)
	}
	return current.Record, false, nil
}

// handleLifecycleReady handles the ready state for server stop.
func (w *ServerStopWorkflow) handleLifecycleReady(next store.LogicalServer, operation store.OperationRecord, saveStep store.OperationStep) (store.LogicalServer, bool, error) {
	if operation.Status == protocolstate.OperationFailed {
		next.LifecycleState = protocolstate.ServerDegraded
		return next, true, nil
	}
	if saveStep.Status != protocolstate.StepPending {
		next.LifecycleState = protocolstate.ServerStopping
		next.DesiredState = "stopped"
		return next, true, nil
	}
	return next, false, nil
}

// handleLifecycleStopping handles the stopping state for server stop.
func (w *ServerStopWorkflow) handleLifecycleStopping(next store.LogicalServer, operation store.OperationRecord, saveStep store.OperationStep) (store.LogicalServer, bool, error) {
	if operation.Status == protocolstate.OperationSucceeded {
		if !hasValidStopResult(operation.Result, operation.Target, next.InstanceRef, w.schemas) {
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
	return next, false, nil
}

func hasValidStopResult(raw json.RawMessage, target store.OperationTarget, instanceRef json.RawMessage, schemas schemaRuntime) bool {
	var result lifecycleResultDocument
	if json.Unmarshal(raw, &result) != nil || !resultNamesTarget(result, "server.stop", protocolstate.ServerStopped, target) {
		return false
	}
	return resultBindsInstance(result.OperationData.InstanceRef, target.InstanceRef, instanceRef) &&
		schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-stop-result", raw) == nil
}

// This file reads the stored server.stop step evidence: the provider and
// runtime inspections, the game save and the graceful stop result.

func stopUnitInspectOutput(operation store.OperationRecord, id string) (stopUnitInspection, error) {
	return plannedStepOutput[stopUnitInspection](operation, id, "runtime inspection output is missing")
}

// stopUnitObservedRunning reports whether the runtime inspection observed
// the exact planned unit running.
func stopUnitObservedRunning(operation store.OperationRecord) bool {
	observation, err := stopUnitInspectOutput(operation, stopInspectUnitStep)
	return err == nil && equalJSON(observation.UnitRef, stopUnitRef(operation)) && observation.State == "running"
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

// stopSaveBinds reports whether a save observation names the operation's
// server, the planned release and the instance the plan inspected.
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
	plannedInstance := stepField(operationStep(operation, stopInspectInstanceStep).Input, "instance_ref")
	return saveNamesTarget(save, operation.Target) && save.ReleaseID == input.ReleaseID &&
		savedPlannedInstance(save.Target.InstanceRef, input.Target.InstanceRef, plannedInstance)
}

func saveNamesTarget(save gameSaveObservation, target store.OperationTarget) bool {
	return save.Target.ServerID == target.ServerID && save.Target.EnvironmentID == target.EnvironmentID
}

// savedPlannedInstance reports whether the saved instance is the one the
// save request named, and that one is the instance the plan inspected.
func savedPlannedInstance(saved, requested, planned json.RawMessage) bool {
	return equalJSON(saved, requested) && equalJSON(requested, planned)
}

func stopSaveOutput(operation store.OperationRecord) (gameSaveObservation, error) {
	output, err := plannedStepOutput[gameSaveObservation](operation, stopSaveGameStep, "game save output is missing")
	if err != nil {
		return output, err
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
