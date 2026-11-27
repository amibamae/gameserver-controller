package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (e *Engine) preflight(record store.OperationRecord, step store.OperationStep) (adapter.Capability, error) {
	if record.ProtocolVersion != e.adapters.ProtocolVersion() || record.ProtocolVersion != e.schemas.ProtocolVersion() {
		return adapter.Capability{}, errors.New("operation protocol version differs from adapter registry")
	}
	input := effectiveStepInput(step)
	if !invocationPlanComplete(step, input) {
		return adapter.Capability{}, errors.New("step does not contain a complete adapter invocation plan")
	}
	descriptor, err := e.adapters.Descriptor(step.AdapterID)
	if err != nil || descriptor.AdapterID != step.AdapterID {
		return adapter.Capability{}, errors.New("recorded adapter is unavailable")
	}
	if !containsProtocol(descriptor.ProtocolVersions, record.ProtocolVersion) {
		return adapter.Capability{}, errors.New("recorded adapter does not support operation protocol version")
	}
	return e.recordedCapability(descriptor, step, input)
}

// invocationPlanComplete reports whether a step names its adapter and exact
// capability and has an object input.
func invocationPlanComplete(step store.OperationStep, input json.RawMessage) bool {
	if step.AdapterID == "" || step.CapabilityName == "" || step.CapabilityVersion == "" {
		return false
	}
	return isObject(input)
}

// recordedCapability finds the step's exact capability in the adapter
// descriptor and checks the step input against its loaded schemas.
func (e *Engine) recordedCapability(descriptor adapter.Descriptor, step store.OperationStep, input json.RawMessage) (adapter.Capability, error) {
	for _, capability := range descriptor.Capabilities {
		if capability.Name != step.CapabilityName {
			continue
		}
		if capability.Version != step.CapabilityVersion {
			return adapter.Capability{}, errors.New("recorded capability version is unavailable")
		}
		if !e.schemas.HasSchema(capability.InputSchemaURI) || !e.schemas.HasSchema(capability.OutputSchemaURI) {
			return adapter.Capability{}, errors.New("recorded capability schemas are unavailable")
		}
		if err := e.schemas.ValidateJSON(capability.InputSchemaURI, input); err != nil {
			return adapter.Capability{}, fmt.Errorf("step input does not match recorded capability: %w", err)
		}
		return capability, nil
	}
	return adapter.Capability{}, errors.New("recorded capability is unavailable")
}

func effectiveStepInput(step store.OperationStep) json.RawMessage {
	if len(step.ResolvedInput) > 0 {
		return step.ResolvedInput
	}
	return step.Input
}

func resolveStepInput(steps []store.OperationStep, targetIndex int) (json.RawMessage, error) {
	bindings := make([]inputbinding.Step, len(steps))
	for i, step := range steps {
		bindings[i] = inputbinding.Step{
			StepID: step.StepID, Status: string(step.Status), Input: step.Input,
			Output: step.Output, Binding: step.InputBindings,
		}
	}
	return inputbinding.Resolve(bindings, targetIndex)
}

func (e *Engine) stepDeadline(ctx context.Context, record store.OperationRecord) time.Time {
	deadline := time.Now().Add(e.stepTimeout)
	if record.Deadline != nil && record.Deadline.Before(deadline) {
		deadline = *record.Deadline
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	return deadline
}
