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
	startInspectInstanceStep = "inspect-instance"
	startRuntimeStep         = "start-runtime"
	startVerifyRuntimeStep   = "verify-runtime"
	startReadinessStep       = "check-readiness"
)

// ServerStartProfile contains trusted release-specific values that do not
// belong in a client request. A resolver selects it from the stored logical
// server definition and controller-owned catalogs.
type ServerStartProfile struct {
	GameReleaseID       string
	UnitRef             json.RawMessage
	ReadinessCheckSetID string
	RequiredChecks      []ReadinessRequirement
}

// ServerStartProfileResolver must resolve immutable, trusted deployment
// metadata. It must not derive adapter steps from client-supplied fields.
type ServerStartProfileResolver interface {
	ResolveServerStart(context.Context, store.LogicalServer) (ServerStartProfile, error)
}

type serverStartParameters struct {
	ExpectedInstanceRef json.RawMessage `json:"expected_instance_ref,omitempty"`
}

type instanceInspection struct {
	InstanceRef json.RawMessage `json:"instance_ref"`
	State       string          `json:"state"`
	ObservedAt  string          `json:"observed_at"`
}

type executionUnitInspection struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

type executionUnitStartResult struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	Outcome    string          `json:"outcome"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

// ServerStartWorkflow is both the trusted planner and the durable definition
// for server.start. It uses the same injected object during acceptance and
// execution so profile selection and lifecycle policy share one source.
type ServerStartWorkflow struct {
	servers  store.LifecycleOperationStore
	adapters adapterRuntime
	schemas  schemaRuntime
	profiles ServerStartProfileResolver
	ttl      time.Duration
}

func NewServerStartWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime, profiles ServerStartProfileResolver, assertionTTL time.Duration) (*ServerStartWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, and trusted start-profile resolver are required")
	}
	if assertionTTL <= 0 || assertionTTL > maxReadinessAssertionTTL {
		return nil, errors.New("readiness assertion TTL must be in the range 1ns..15m")
	}
	return &ServerStartWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles, ttl: assertionTTL}, nil
}

// BuildPlan resolves the server's current instance and trusted release profile
// into a durable, schema-checked sequence. All resource references, check IDs,
// versions, and adapter capability versions are stored before dispatch.
func (w *ServerStartWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if ctx == nil || w == nil || w.servers == nil || w.adapters == nil || w.schemas == nil || w.profiles == nil {
		return nil, errors.New("initialized start workflow and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if operation.OperationType != "server.start" || operation.ProtocolVersion != w.schemas.ProtocolVersion() ||
		operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" ||
		operation.RequestSnapshot.OperationType != "server.start" || !isObject(operation.RequestSnapshot.Parameters) {
		return nil, errors.New("server.start operation and loaded protocol version are required")
	}
	var parameters serverStartParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return nil, errors.New("decode server.start parameters")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, fmt.Errorf("load logical server for start plan: %w", err)
	}
	server := stored.Record
	if server.LifecycleState != protocolstate.ServerStopped || len(server.InstanceRef) == 0 {
		return nil, errors.New("server.start requires a stopped logical server with a managed instance")
	}
	if len(operation.Target.InstanceRef) > 0 && !equalJSON(operation.Target.InstanceRef, server.InstanceRef) {
		return nil, store.ErrLogicalServerTargetMismatch
	}
	if len(parameters.ExpectedInstanceRef) > 0 && !equalJSON(parameters.ExpectedInstanceRef, server.InstanceRef) {
		return nil, errors.New("expected_instance_ref does not match the current managed instance")
	}
	profile, err := w.profiles.ResolveServerStart(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("resolve trusted server start profile: %w", err)
	}
	if err := validateStartProfile(server, profile); err != nil {
		return nil, err
	}

	version := w.schemas.ProtocolVersion()
	capabilities := []struct {
		adapterID string
		category  adapter.Category
		name      string
		inputURI  string
		outputURI string
		mutating  bool
	}{
		{adapterIDFromRef(server.InstanceRef), adapter.CategoryHosting, "hosting.instance.inspect",
			"hosting:instance-inspect-input", "hosting:instance-inspect-output", false},
		{server.ExecutionAdapterID, adapter.CategoryExecution, "execution.unit.start",
			"execution:systemd-unit-start-input", "execution:systemd-unit-start-output", true},
		{server.ExecutionAdapterID, adapter.CategoryExecution, "execution.unit.inspect",
			"execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false},
		{server.GameAdapterID, adapter.CategoryGame, "game.readiness.check",
			"game:readiness-check-input", "game:readiness-check-output", false},
	}
	resolved := make([]adapter.Capability, len(capabilities))
	for i, requirement := range capabilities {
		resolved[i], err = exactCapability(w.adapters, requirement.adapterID, requirement.category, requirement.name,
			"urn:gameserver-controller:"+version+":"+requirement.inputURI,
			"urn:gameserver-controller:"+version+":"+requirement.outputURI, requirement.mutating)
		if err != nil {
			return nil, err
		}
	}
	gameDescriptor, err := w.adapters.Descriptor(server.GameAdapterID)
	if err != nil {
		return nil, fmt.Errorf("resolve game adapter %q: %w", server.GameAdapterID, err)
	}
	if gameDescriptor.Category != adapter.CategoryGame {
		return nil, errors.New("configured game adapter has the wrong category")
	}
	if err := verifyReadinessSetDeclared(gameDescriptor, server.EnvironmentID, profile); err != nil {
		return nil, err
	}

	inputs := []struct {
		name  string
		value any
	}{
		{startInspectInstanceStep, map[string]any{"instance_ref": server.InstanceRef}},
		{startRuntimeStep, map[string]any{"unit_ref": profile.UnitRef}},
		{startVerifyRuntimeStep, map[string]any{"unit_ref": profile.UnitRef}},
		{startReadinessStep, map[string]any{
			"server_id": operation.Target.ServerID, "environment_id": operation.Target.EnvironmentID,
			"instance_ref": server.InstanceRef, "game_release_id": profile.GameReleaseID,
			"readiness_check_set_id": profile.ReadinessCheckSetID, "required_checks": profile.RequiredChecks,
		}},
	}
	steps := make([]store.OperationStep, len(inputs))
	for i, item := range inputs {
		encoded, err := json.Marshal(item.value)
		if err != nil {
			return nil, fmt.Errorf("encode %s input: %w", item.name, err)
		}
		if err := w.schemas.ValidateJSON(resolved[i].InputSchemaURI, encoded); err != nil {
			return nil, fmt.Errorf("planned %s input does not match its declared capability schema: %w", item.name, err)
		}
		steps[i] = store.OperationStep{
			StepID: item.name, StepType: resolved[i].Name, Status: protocolstate.StepPending,
			AdapterID: capabilities[i].adapterID, CapabilityName: resolved[i].Name,
			CapabilityVersion: resolved[i].Version, Input: encoded,
		}
	}
	return steps, nil
}

func validateStartProfile(server store.LogicalServer, profile ServerStartProfile) error {
	if profile.GameReleaseID == "" || len(profile.GameReleaseID) > 200 || profile.ReadinessCheckSetID == "" ||
		len(profile.ReadinessCheckSetID) > 200 || len(profile.RequiredChecks) == 0 || len(profile.RequiredChecks) > 64 {
		return errors.New("trusted start profile must select a release and bounded readiness check set")
	}
	var unitRef struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if err := json.Unmarshal(profile.UnitRef, &unitRef); err != nil || !isObject(profile.UnitRef) ||
		unitRef.AdapterID != server.ExecutionAdapterID || unitRef.EnvironmentID != server.EnvironmentID ||
		unitRef.ResourceKind == "" || unitRef.ExternalID == "" {
		return errors.New("trusted start profile unit reference does not bind the configured execution adapter and environment")
	}
	seen := make(map[string]struct{}, len(profile.RequiredChecks))
	for _, check := range profile.RequiredChecks {
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

func adapterIDFromRef(value json.RawMessage) string {
	var ref struct {
		AdapterID string `json:"adapter_id"`
	}
	_ = json.Unmarshal(value, &ref)
	return ref.AdapterID
}

func exactCapability(registry adapterRuntime, adapterID string, category adapter.Category, name, inputURI, outputURI string, mutating bool) (adapter.Capability, error) {
	descriptor, err := registry.Descriptor(adapterID)
	if err != nil {
		return adapter.Capability{}, fmt.Errorf("resolve adapter %q for %s: %w", adapterID, name, err)
	}
	if descriptor.AdapterID != adapterID || descriptor.Category != category {
		return adapter.Capability{}, fmt.Errorf("adapter %q has the wrong identity or category for %s", adapterID, name)
	}
	for _, capability := range descriptor.Capabilities {
		if capability.Name != name {
			continue
		}
		if capability.Version == "" || capability.InputSchemaURI != inputURI || capability.OutputSchemaURI != outputURI || capability.Mutating != mutating {
			return adapter.Capability{}, fmt.Errorf("adapter %q declares an incompatible %s capability", adapterID, name)
		}
		return capability, nil
	}
	return adapter.Capability{}, fmt.Errorf("adapter %q does not advertise required capability %s", adapterID, name)
}

// CheckPreconditions rechecks server identity and all prior-step evidence just
// before each planned dispatch.
func (w *ServerStartWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	if ctx == nil || w == nil || operation.OperationType != "server.start" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return errors.New("server.start operation and context are required")
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
	if step.StepID == startInspectInstanceStep && !stepBindsRef(step.Input, "instance_ref", server.InstanceRef) {
		return errors.New("provider inspection is not bound to the current managed instance")
	}
	switch step.StepID {
	case startInspectInstanceStep:
		if server.LifecycleState != protocolstate.ServerStopped {
			return errors.New("server must remain stopped until the provider instance is confirmed active")
		}
	case startRuntimeStep:
		initialAttempt := step.Attempt == 0 && server.LifecycleState == protocolstate.ServerStopped
		confirmedRetry := step.Attempt > 0 && server.LifecycleState == protocolstate.ServerStarting
		if (!initialAttempt && !confirmedRetry) || !stepSucceeded(operation, startInspectInstanceStep) {
			return errors.New("runtime start requires a stopped server or confirmed start retry and a successful provider inspection")
		}
		observation, err := instanceStepOutput(operation, startInspectInstanceStep)
		if err != nil || !equalJSON(observation.InstanceRef, server.InstanceRef) || observation.State != "active" {
			return errors.New("runtime start requires the exact provider instance to be active")
		}
		if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") {
			return errors.New("runtime start and verification must target the same planned unit")
		}
	case startVerifyRuntimeStep:
		if server.LifecycleState != protocolstate.ServerStarting || !unitStartCompleted(operation) {
			return errors.New("runtime inspection requires a completed start command")
		}
		if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") {
			return errors.New("runtime verification must target the exact started unit")
		}
	case startReadinessStep:
		if server.LifecycleState != protocolstate.ServerStarting || !stepSucceeded(operation, startVerifyRuntimeStep) {
			return errors.New("game readiness requires a successful exact-unit runtime inspection")
		}
		inspection, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
		if err != nil || inspection.State != "running" {
			return errors.New("game readiness requires the execution unit to be confirmed running")
		}
		if !sameStepReference(operation, startRuntimeStep, startVerifyRuntimeStep, "unit_ref") || !stepBindsRef(step.Input, "instance_ref", server.InstanceRef) {
			return errors.New("readiness request must be bound to the verified unit and current instance")
		}
	default:
		return fmt.Errorf("unsupported server.start step %q", step.StepID)
	}
	return nil
}

func stepBindsRef(input json.RawMessage, field string, expected json.RawMessage) bool {
	var decoded map[string]json.RawMessage
	if json.Unmarshal(input, &decoded) != nil || decoded == nil {
		return false
	}
	return equalJSON(decoded[field], expected)
}

func sameStepReference(operation store.OperationRecord, firstID, secondID, field string) bool {
	first := operationStep(operation, firstID)
	second := operationStep(operation, secondID)
	return first.StepID != "" && second.StepID != "" && stepBindsRef(first.Input, field, stepField(second.Input, field))
}

func stepField(input json.RawMessage, field string) json.RawMessage {
	var decoded map[string]json.RawMessage
	if json.Unmarshal(input, &decoded) != nil {
		return nil
	}
	return decoded[field]
}

func stepSucceeded(operation store.OperationRecord, stepID string) bool {
	for _, step := range operation.Steps {
		if step.StepID == stepID {
			return step.Status == protocolstate.StepSucceeded
		}
	}
	return false
}

func instanceStepOutput(operation store.OperationRecord, stepID string) (instanceInspection, error) {
	var output instanceInspection
	for _, step := range operation.Steps {
		if step.StepID == stepID && step.Status == protocolstate.StepSucceeded {
			if err := json.Unmarshal(step.Output, &output); err != nil {
				return instanceInspection{}, err
			}
			return output, nil
		}
	}
	return instanceInspection{}, errors.New("provider inspection output is missing")
}

func unitStartCompleted(operation store.OperationRecord) bool {
	for _, step := range operation.Steps {
		if step.StepID != startRuntimeStep || step.Status != protocolstate.StepSucceeded {
			continue
		}
		var result executionUnitStartResult
		return json.Unmarshal(step.Output, &result) == nil && result.Outcome == "completed" && result.State == "running" && equalJSON(result.UnitRef, stepUnitRef(step))
	}
	return false
}

func unitInspectStepOutput(operation store.OperationRecord, stepID string) (executionUnitInspection, error) {
	var output executionUnitInspection
	for _, step := range operation.Steps {
		if step.StepID == stepID && step.Status == protocolstate.StepSucceeded {
			if err := json.Unmarshal(step.Output, &output); err != nil {
				return executionUnitInspection{}, err
			}
			return output, nil
		}
	}
	return executionUnitInspection{}, errors.New("execution unit inspection output is missing")
}

// Reconcile resolves an ambiguous unit start by using the exact read-only
// inspection capability pinned in the same durable plan. It never retries
// until inspection confirms the unit is absent or stopped.
func (w *ServerStartWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	if ctx == nil || step.StepID != startRuntimeStep || step.CapabilityName != "execution.unit.start" {
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	var inspect store.OperationStep
	for _, candidate := range operation.Steps {
		if candidate.StepID == startVerifyRuntimeStep {
			inspect = candidate
			break
		}
	}
	if inspect.StepID == "" || inspect.CapabilityName != "execution.unit.inspect" || inspect.AdapterID != step.AdapterID || inspect.CapabilityVersion == "" {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	capability, err := exactCapability(w.adapters, inspect.AdapterID, adapter.CategoryExecution, inspect.CapabilityName,
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":execution:systemd-unit-inspect-output", false)
	if err != nil || capability.Version != inspect.CapabilityVersion {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	deadline := time.Now().UTC().Add(15 * time.Second)
	requestID := startReconcileRequestID(operation.OperationID, step.StepID, step.Attempt)
	correlation := operation.CorrelationID
	if correlation == "" {
		correlation = operation.OperationID
	}
	request := adapter.InvokeRequest{
		JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke",
		Params: adapter.InvokeParams{
			ProtocolVersion: operation.ProtocolVersion, Capability: capability.Name,
			RequestContext: adapter.RequestContext{
				RequestID: requestID, CorrelationID: correlation, OperationID: operation.OperationID,
				StepID: step.StepID, ServerID: operation.Target.ServerID,
				EnvironmentID: operation.Target.EnvironmentID, Deadline: deadline,
			},
			Input: append(json.RawMessage(nil), inspect.Input...),
		},
	}
	callContext, cancel := context.WithDeadline(ctx, deadline)
	response, invokeErr := w.adapters.Invoke(callContext, inspect.AdapterID, request)
	cancel()
	if invokeErr != nil || response.JSONRPC != "2.0" || response.ID != requestID || response.Result.Outcome != adapter.OutcomeCompleted {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	if err := w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output); err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	var observation executionUnitInspection
	if err := json.Unmarshal(response.Result.Output, &observation); err != nil || !equalJSON(observation.UnitRef, stepUnitRef(step)) {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	if _, err := time.Parse(time.RFC3339Nano, observation.ObservedAt); err != nil {
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
	switch observation.State {
	case "running":
		output, _ := json.Marshal(executionUnitStartResult{
			UnitRef: append(json.RawMessage(nil), observation.UnitRef...), Outcome: "completed",
			State: "running", ObservedAt: observation.ObservedAt,
		})
		return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "absent", "stopped":
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "starting":
		return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}, nil
	case "failed":
		return Resolution{
			State: ResolutionDefinitiveError, ExternalOperationRef: step.ExternalOperationRef,
			Error: protocolError("core.execution.unit_failed", "provider_failure", "The exact execution unit is confirmed failed after a start attempt.", operation.OperationID, false, "operator_review", true),
		}, nil
	default:
		return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}, nil
	}
}

// HandlePreconditionFailure terminally fails only when a stored, target-bound
// inspection proves the next side effect cannot proceed. Unknown observations
// remain suspended so the controller never guesses about provider state.
func (w *ServerStartWorkflow) HandlePreconditionFailure(_ context.Context, operation store.OperationRecord, step store.OperationStep, _ error) (Resolution, error) {
	var message string
	switch step.StepID {
	case startRuntimeStep:
		observation, err := instanceStepOutput(operation, startInspectInstanceStep)
		if err != nil || !equalJSON(observation.InstanceRef, stepField(operationStep(operation, startInspectInstanceStep).Input, "instance_ref")) {
			return Resolution{State: ResolutionNeedsOperator}, nil
		}
		switch observation.State {
		case "creating", "stopped", "destroying", "destroyed", "failed":
			message = "The exact provider instance is not active; no runtime start was issued."
		default:
			return Resolution{State: ResolutionNeedsOperator}, nil
		}
	case startReadinessStep:
		observation, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
		if err != nil || !equalJSON(observation.UnitRef, stepField(operationStep(operation, startRuntimeStep).Input, "unit_ref")) {
			return Resolution{State: ResolutionNeedsOperator}, nil
		}
		switch observation.State {
		case "absent", "stopped", "starting", "stopping", "failed":
			message = "The exact execution unit is not confirmed running; game readiness was not checked."
		default:
			return Resolution{State: ResolutionNeedsOperator}, nil
		}
	default:
		return Resolution{State: ResolutionNeedsOperator}, nil
	}
	return Resolution{
		State: ResolutionDefinitiveError,
		Error: protocolError("core.safety.precondition_unmet", "safety_blocked", message, operation.OperationID, false, "inspect_operation", false),
	}, nil
}

func stepUnitRef(step store.OperationStep) json.RawMessage {
	var input map[string]json.RawMessage
	if json.Unmarshal(step.Input, &input) != nil {
		return nil
	}
	return input["unit_ref"]
}

func startReconcileRequestID(operationID, stepID string, attempt int) string {
	digest := sha256.Sum256([]byte(operationID + "\x00" + stepID + "\x00reconcile\x00" + fmt.Sprint(attempt)))
	return "reconcile-" + hex.EncodeToString(digest[:16])
}

// ValidateCompletion enforces the runtime and readiness postconditions after
// all adapter steps have completed. A failed game check is a terminal failed
// start operation, not a falsely successful workflow.
func (w *ServerStartWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.start" {
		return errors.New("server.start completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !unitStartCompleted(operation) {
		return errors.New("execution unit start postcondition was not confirmed")
	}
	inspection, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	if err != nil || inspection.State != "running" || !equalJSON(inspection.UnitRef, stepUnitRef(operationStep(operation, startRuntimeStep))) {
		return errors.New("execution unit is not confirmed running at the pinned reference")
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	_, err = BuildReadinessAssertion(w.schemas, operation, stored.Record, startReadinessStep, w.ttl)
	return err
}

func operationStep(operation store.OperationRecord, id string) store.OperationStep {
	for _, step := range operation.Steps {
		if step.StepID == id {
			return step
		}
	}
	return store.OperationStep{}
}

func (w *ServerStartWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	assertion, err := BuildReadinessAssertion(w.schemas, operation, stored.Record, startReadinessStep, w.ttl)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"outcome_summary": "Server runtime started and all required game readiness checks passed.",
		"operation_type":  "server.start", "affected_resources": []any{stored.Record.InstanceRef},
		"completed_steps": completedStepSummaries(operation), "failed_steps": []any{},
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": false,
		"operation_data": map[string]any{
			"server_id": stored.Record.ServerID, "environment_id": stored.Record.EnvironmentID,
			"instance_ref": stored.Record.InstanceRef, "lifecycle_state": string(protocolstate.ServerReady),
			"readiness_assertion": assertion,
		},
	}
	return json.Marshal(result)
}

func (w *ServerStartWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	failed := []any{}
	if step.StepID != "" && len(failure) > 0 && step.Status == protocolstate.StepFailed {
		failed = append(failed, map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure})
	}
	unresolved := []any{}
	if step.StepID == "" {
		conditionID := "server.start.postcondition_unmet"
		description := "The adapter calls completed, but the exact running and readiness postconditions were not satisfied."
		var protocolFailure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(operation.Error, &protocolFailure) == nil && protocolFailure.Code == "core.safety.precondition_unmet" && protocolFailure.Message != "" {
			conditionID = "server.start.precondition_unmet"
			description = protocolFailure.Message
		}
		unresolved = append(unresolved, map[string]any{
			"condition_id": conditionID, "description": description,
		})
	}
	partial := operationStep(operation, startRuntimeStep).Attempt > 0
	result := map[string]any{
		"outcome_summary": "Server start did not satisfy all runtime and readiness postconditions.",
		"operation_type":  "server.start", "affected_resources": []any{},
		"completed_steps": completedStepSummaries(operation), "failed_steps": failed,
		"warnings": []string{}, "unresolved_conditions": unresolved, "audit_event_ids": []string{}, "partial": partial,
	}
	instanceRef := operation.Target.InstanceRef
	if len(instanceRef) == 0 {
		if server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID); err == nil {
			instanceRef = server.Record.InstanceRef
		}
	}
	if len(instanceRef) > 0 {
		result["affected_resources"] = []any{instanceRef}
	}
	return json.Marshal(result)
}

func completedStepSummaries(operation store.OperationRecord) []any {
	result := make([]any, 0, len(operation.Steps))
	for _, step := range operation.Steps {
		if step.Status == protocolstate.StepSucceeded {
			result = append(result, map[string]any{"step_id": step.StepID, "step_type": step.StepType})
		}
	}
	return result
}

// ProjectLifecycle derives start's durable states only from immutable steps.
// The ready transition occurs atomically with the terminal success result.
func (w *ServerStartWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if operation.OperationType != "server.start" || current.Record.ServerID != operation.Target.ServerID || current.Record.EnvironmentID != operation.Target.EnvironmentID {
		return store.LogicalServer{}, false, errors.New("server.start lifecycle projection target mismatch")
	}
	next := current.Record
	startStep := operationStep(operation, startRuntimeStep)
	verifyStep := operationStep(operation, startVerifyRuntimeStep)
	readinessStep := operationStep(operation, startReadinessStep)
	switch current.Record.LifecycleState {
	case protocolstate.ServerStopped:
		if startStep.Status != protocolstate.StepPending {
			next.LifecycleState = protocolstate.ServerStarting
			next.DesiredState = "running"
			return next, true, nil
		}
	case protocolstate.ServerStarting:
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			return next, true, nil
		}
		if verifyStep.Status == protocolstate.StepSucceeded {
			var observed executionUnitInspection
			if json.Unmarshal(verifyStep.Output, &observed) != nil {
				return store.LogicalServer{}, false, errors.New("execution unit verification output cannot be decoded")
			}
			if observed.State == "failed" {
				next.LifecycleState = protocolstate.ServerFailed
				return next, true, nil
			}
		}
		if readinessStep.Status != protocolstate.StepPending {
			next.LifecycleState = protocolstate.ServerValidating
			next.DesiredState = "running"
			return next, true, nil
		}
	case protocolstate.ServerValidating:
		if operation.Status == protocolstate.OperationSucceeded {
			if !hasValidStartResult(operation.Result, operation.Target, current.Record.InstanceRef, w.schemas) {
				return store.LogicalServer{}, false, errors.New("terminal server.start result lacks its exact readiness assertion")
			}
			next.LifecycleState = protocolstate.ServerReady
			next.DesiredState = "running"
			return next, true, nil
		}
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerDegraded
			return next, true, nil
		}
	}
	return current.Record, false, nil
}

func hasValidStartResult(raw json.RawMessage, target store.OperationTarget, instanceRef json.RawMessage, schemas schemaRuntime) bool {
	var result struct {
		OperationType string `json:"operation_type"`
		OperationData struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
			State         string          `json:"lifecycle_state"`
			Assertion     json.RawMessage `json:"readiness_assertion"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.OperationType != "server.start" || result.OperationData.ServerID != target.ServerID ||
		result.OperationData.EnvironmentID != target.EnvironmentID || result.OperationData.State != string(protocolstate.ServerReady) ||
		!isObject(result.OperationData.Assertion) {
		return false
	}
	if len(target.InstanceRef) > 0 && !equalJSON(result.OperationData.InstanceRef, target.InstanceRef) {
		return false
	}
	if !equalJSON(result.OperationData.InstanceRef, instanceRef) {
		return false
	}
	return schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":lifecycle:readiness-assertion", result.OperationData.Assertion) == nil
}

func verifyReadinessSetDeclared(descriptor adapter.Descriptor, environmentID string, profile ServerStartProfile) error {
	var gameCapability adapter.Capability
	for _, capability := range descriptor.Capabilities {
		if capability.Name == "game.readiness.check" {
			gameCapability = capability
			break
		}
	}
	if gameCapability.Name == "" {
		return errors.New("game adapter does not declare game.readiness.check")
	}
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

var _ interface {
	BuildPlan(context.Context, store.OperationRecord) ([]store.OperationStep, error)
	Definition
	LifecycleProjector
	interface {
		ValidateCompletion(context.Context, store.OperationRecord) error
	}
} = (*ServerStartWorkflow)(nil)
