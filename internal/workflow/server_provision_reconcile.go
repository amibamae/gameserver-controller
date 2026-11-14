package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/store"
	"time"
)

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
