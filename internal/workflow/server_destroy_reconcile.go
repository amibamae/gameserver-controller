package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (w *ServerDestroyWorkflow) Reconcile(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, error) {
	instanceRef, blocked, ok := w.destroyReconcileTarget(ctx, operation, step)
	if !ok {
		return blocked, nil
	}
	if len(step.ExternalOperationRef) > 0 {
		if resolution, handled := w.reconcileWithProviderWait(ctx, operation, step); handled {
			return resolution, nil
		}
	}
	observed, observedAt, inspectErr := w.inspectProviderForReconciliation(ctx, operation, step, instanceRef)
	if inspectErr != nil {
		return destroyNeedsOperator(step), nil
	}
	if resolution, decided := destroyProviderStateResolution(step, observed, observedAt); decided {
		return resolution, nil
	}
	return destroyNeedsOperator(step), nil
}

// destroyReconcileTarget rechecks the operation, current logical server, and
// exact destroy capability before any read-only provider call. It returns the
// current instance reference, or the resolution to report when reconciliation
// cannot proceed.
func (w *ServerDestroyWorkflow) destroyReconcileTarget(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (json.RawMessage, Resolution, bool) {
	if ctx == nil || w == nil || operation.OperationType != "server.destroy" || step.StepID != destroyProviderStep {
		return nil, Resolution{State: ResolutionNeedsOperator}, false
	}
	if err := ctx.Err(); err != nil {
		return nil, Resolution{State: ResolutionNeedsOperator}, false
	}
	parameters, err := serverDestroyRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return nil, Resolution{State: ResolutionNeedsOperator}, false
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil || validateServerDestroyTarget(operation, parameters, stored.Record) != nil ||
		!stepBindsRef(step.Input, "instance_ref", stored.Record.InstanceRef) {
		return nil, destroyNeedsOperator(step), false
	}
	capability, err := exactCapability(w.adapters, step.AdapterID, adapter.CategoryHosting, "hosting.instance.destroy",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:instance-destroy-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:instance-destroy-output", true)
	if err != nil || capability.Version != step.CapabilityVersion {
		return nil, destroyNeedsOperator(step), false
	}
	return stored.Record.InstanceRef, Resolution{}, true
}

func (w *ServerDestroyWorkflow) reconcileWithProviderWait(ctx context.Context, operation store.OperationRecord, step store.OperationStep) (Resolution, bool) {
	instanceRef := stepField(step.Input, "instance_ref")
	descriptor, err := w.adapters.Descriptor(step.AdapterID)
	if err != nil || descriptor.AdapterID != step.AdapterID || descriptor.Category != adapter.CategoryHosting {
		return destroyNeedsOperator(step), true
	}
	version := w.schemas.ProtocolVersion()
	waitCapability, err := optionalExactCapability(w.adapters, step.AdapterID, adapter.CategoryHosting, "hosting.instance.wait",
		"urn:gameserver-controller:"+version+":hosting:instance-wait-input",
		"urn:gameserver-controller:"+version+":hosting:instance-wait-output", false)
	if err != nil {
		return Resolution{}, false
	}
	observed, ok := w.destroyWaitObservation(ctx, operation, step, waitCapability, instanceRef)
	if !ok {
		return destroyNeedsOperator(step), true
	}
	switch observed.OperationStatus {
	case "pending":
		return destroyPending(step), true
	case "succeeded":
		return destroyWaitSucceededResolution(step, observed), true
	case "failed", "unknown":
		return w.reconcileAfterUnconfirmedWait(ctx, operation, step, instanceRef, observed.OperationStatus), true
	default:
		return destroyNeedsOperator(step), true
	}
}

// destroyWaitObservation calls the exact provider wait and accepts only a
// fresh observation of the same instance and provider operation.
func (w *ServerDestroyWorkflow) destroyWaitObservation(ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	waitCapability adapter.Capability, instanceRef json.RawMessage) (serverDestroyWaitOutput, bool) {
	input, _ := json.Marshal(map[string]any{
		"instance_ref": instanceRef, "provider_operation_ref": step.ExternalOperationRef,
		"expected_state": "destroyed", "wait_timeout_seconds": 15,
	})
	output, _, err := w.invokeReadOnly(ctx, operation, step, waitCapability, input, "destroy-wait")
	if err != nil {
		return serverDestroyWaitOutput{}, false
	}
	var observed serverDestroyWaitOutput
	if json.Unmarshal(output, &observed) != nil || !equalJSON(observed.InstanceRef, instanceRef) ||
		!equalJSON(observed.ProviderOperationRef, step.ExternalOperationRef) || !observationIsFreshForStep(observed.ObservedAt, step) {
		return serverDestroyWaitOutput{}, false
	}
	return observed, true
}

// destroyWaitSucceededResolution trusts a succeeded wait only when it observed
// the instance destroyed, and keeps waiting while it is still destroying.
func destroyWaitSucceededResolution(step store.OperationStep, observed serverDestroyWaitOutput) Resolution {
	switch observed.InstanceState {
	case "destroyed":
		return destroyCompletedResolution(step, observed.InstanceRef, observed.ObservedAt)
	case "destroying":
		return destroyPending(step)
	default:
		return destroyNeedsOperator(step)
	}
}

// reconcileAfterUnconfirmedWait inspects the exact instance after a failed or
// unknown wait. Only a failed provider operation that left the instance
// active or stopped is safe to retry.
func (w *ServerDestroyWorkflow) reconcileAfterUnconfirmedWait(ctx context.Context, operation store.OperationRecord, step store.OperationStep,
	instanceRef json.RawMessage, operationStatus string) Resolution {
	provider, at, inspectErr := w.inspectProviderForReconciliation(ctx, operation, step, instanceRef)
	if inspectErr != nil {
		return destroyNeedsOperator(step)
	}
	if resolution, decided := destroyProviderStateResolution(step, provider, at); decided {
		return resolution
	}
	if activeOrStopped(provider.State) && operationStatus == "failed" {
		return Resolution{State: ResolutionSafeToRetry, ExternalOperationRef: nil}
	}
	return destroyNeedsOperator(step)
}

// destroyProviderStateResolution maps an exact provider inspection that shows
// the instance destroyed or destroying. Other states are left to the caller.
func destroyProviderStateResolution(step store.OperationStep, observed serverDestroyInstanceInspection, observedAt time.Time) (Resolution, bool) {
	switch observed.State {
	case "destroyed":
		return destroyCompletedResolution(step, observed.InstanceRef, observedAt.Format(time.RFC3339Nano)), true
	case "destroying":
		return destroyPending(step), true
	default:
		return Resolution{}, false
	}
}

// destroyCompletedResolution records the normalized completed destroy output
// for an observed provider absence. A marshal failure leaves Output nil.
func destroyCompletedResolution(step store.OperationStep, instanceRef json.RawMessage, observedAt string) Resolution {
	output, _ := json.Marshal(serverDestroyOutput{InstanceRef: instanceRef, Outcome: "completed", State: "destroyed", ObservedAt: observedAt})
	return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: step.ExternalOperationRef}
}

func destroyPending(step store.OperationStep) Resolution {
	return Resolution{State: ResolutionPending, ExternalOperationRef: step.ExternalOperationRef}
}

func destroyNeedsOperator(step store.OperationStep) Resolution {
	return Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: step.ExternalOperationRef}
}

func optionalExactCapability(registry adapterRuntime, adapterID string, category adapter.Category, name, inputURI, outputURI string, mutating bool) (adapter.Capability, error) {
	descriptor, err := registry.Descriptor(adapterID)
	if err != nil {
		return adapter.Capability{}, fmt.Errorf("resolve adapter %q for optional %s capability: %w", adapterID, name, err)
	}
	if descriptor.AdapterID != adapterID || descriptor.Category != category {
		return adapter.Capability{}, fmt.Errorf("adapter %q identity or category does not match optional %s capability", adapterID, name)
	}
	for _, capability := range descriptor.Capabilities {
		if capability.Name == name {
			if capability.Version == "" || capability.InputSchemaURI != inputURI || capability.OutputSchemaURI != outputURI || capability.Mutating != mutating {
				return adapter.Capability{}, fmt.Errorf("adapter %q declares an incompatible %s capability", adapterID, name)
			}
			return capability, nil
		}
	}
	return adapter.Capability{}, fmt.Errorf("adapter %q does not advertise optional capability %s", adapterID, name)
}

func (w *ServerDestroyWorkflow) inspectProviderForReconciliation(ctx context.Context, operation store.OperationRecord, destroyStep store.OperationStep, instanceRef json.RawMessage) (serverDestroyInstanceInspection, time.Time, error) {
	var observed serverDestroyInstanceInspection
	inspect := operationStep(operation, destroyInspectInstanceStep)
	if inspect.StepID == "" || inspect.CapabilityName != "hosting.instance.inspect" || inspect.AdapterID != destroyStep.AdapterID ||
		inspect.CapabilityVersion == "" || !stepBindsRef(inspect.Input, "instance_ref", instanceRef) {
		return observed, time.Time{}, errors.New("destroy reconciliation has no exact-instance inspection step")
	}
	capability, err := exactCapability(w.adapters, inspect.AdapterID, adapter.CategoryHosting, inspect.CapabilityName,
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:instance-inspect-input",
		"urn:gameserver-controller:"+w.schemas.ProtocolVersion()+":hosting:instance-inspect-output", false)
	if err != nil || capability.Version != inspect.CapabilityVersion {
		return observed, time.Time{}, errors.New("destroy reconciliation inspection capability changed")
	}
	output, at, err := w.invokeReadOnly(ctx, operation, destroyStep, capability, inspect.Input, "destroy-inspect")
	if err != nil || w.schemas.ValidateJSON(capability.OutputSchemaURI, output) != nil || json.Unmarshal(output, &observed) != nil ||
		!equalJSON(observed.InstanceRef, instanceRef) || !observationIsFreshForStep(observed.ObservedAt, destroyStep) {
		return serverDestroyInstanceInspection{}, time.Time{}, errors.New("destroy reconciliation inspection is stale or not bound to the exact instance")
	}
	return observed, at, nil
}

func (w *ServerDestroyWorkflow) invokeReadOnly(ctx context.Context, operation store.OperationRecord, step store.OperationStep, capability adapter.Capability, input json.RawMessage, label string) (json.RawMessage, time.Time, error) {
	deadline := time.Now().UTC().Add(15 * time.Second)
	requestID := destroyReconcileRequestID(operation.OperationID, step.StepID, step.Attempt, label)
	correlation := operation.CorrelationID
	if correlation == "" {
		correlation = operation.OperationID
	}
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: operation.ProtocolVersion, Capability: capability.Name,
		RequestContext: adapter.RequestContext{RequestID: requestID, CorrelationID: correlation, OperationID: operation.OperationID,
			StepID: step.StepID, ServerID: operation.Target.ServerID, EnvironmentID: operation.Target.EnvironmentID, Deadline: deadline},
		Input: append(json.RawMessage(nil), input...),
	}}
	callContext, cancel := context.WithDeadline(ctx, deadline)
	response, err := w.adapters.Invoke(callContext, step.AdapterID, request)
	cancel()
	if err != nil || response.JSONRPC != "2.0" || response.ID != requestID || response.Result.Outcome != adapter.OutcomeCompleted {
		return nil, time.Time{}, errors.New("read-only provider reconciliation call did not complete")
	}
	if err := w.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output); err != nil {
		return nil, time.Time{}, err
	}
	return append(json.RawMessage(nil), response.Result.Output...), response.Result.ObservedAt, nil
}
