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

// BuildPlan pins the exact provider instance, runtime unit, backup, and
// capability identities before the first dispatch. The request must name a
// verified backup for the current data generation and an exact-target approval.
func (w *ServerDestroyWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if ctx == nil || !w.destroyPlanInitialized() {
		return nil, errors.New("initialized server.destroy workflow and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parameters, err := serverDestroyRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return nil, err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, fmt.Errorf("load logical server for destroy plan: %w", err)
	}
	server := stored.Record
	if err := validateServerDestroyTarget(operation, parameters, server); err != nil {
		return nil, err
	}
	if !stoppedOrFailedLifecycle(server.LifecycleState) {
		return nil, errors.New("server.destroy requires a stopped or failed logical server")
	}
	if err := w.authorizeDestroyPlan(ctx, operation, parameters, server); err != nil {
		return nil, err
	}
	profile, err := w.profiles.ResolveServerDestroy(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("resolve trusted server destroy profile: %w", err)
	}
	if err := validateServerDestroyProfile(server, profile); err != nil {
		return nil, err
	}

	return w.buildDestroySteps(server, profile)
}

func (w *ServerDestroyWorkflow) destroyPlanInitialized() bool {
	return w != nil && w.destroyPlanRuntimeConfigured() && w.destroyPlanResolversConfigured()
}

func (w *ServerDestroyWorkflow) destroyPlanRuntimeConfigured() bool {
	return w.servers != nil && w.adapters != nil && w.schemas != nil
}

func (w *ServerDestroyWorkflow) destroyPlanResolversConfigured() bool {
	return w.profiles != nil && w.backups != nil && w.authorizer != nil
}

func (w *ServerDestroyWorkflow) authorizeDestroyPlan(ctx context.Context, operation store.OperationRecord, parameters serverDestroyParameters, server store.LogicalServer) error {
	backup, err := w.verifiedBackup(ctx, server, parameters)
	if err != nil {
		return err
	}
	if err := w.authorizer.AuthorizeServerDestroy(ctx, operation.ActorID, parameters.ApprovalID, operation, server, backup); err != nil {
		return fmt.Errorf("authorize exact server.destroy request: %w", err)
	}
	return nil
}

type destroyStepSpec struct {
	stepID, adapterID   string
	category            adapter.Category
	name, input, output string
	mutating            bool
}

func (w *ServerDestroyWorkflow) buildDestroySteps(server store.LogicalServer, profile ServerDestroyProfile) ([]store.OperationStep, error) {
	version := w.schemas.ProtocolVersion()
	hostingID := adapterIDFromRef(server.InstanceRef)
	specs := []destroyStepSpec{
		{destroyInspectInstanceStep, hostingID, adapter.CategoryHosting, "hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false},
		{destroyInspectRuntimeStep, server.ExecutionAdapterID, adapter.CategoryExecution, "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false},
		{destroyProviderStep, hostingID, adapter.CategoryHosting, "hosting.instance.destroy", "hosting:instance-destroy-input", "hosting:instance-destroy-output", true},
		{destroyVerifyInstanceStep, hostingID, adapter.CategoryHosting, "hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false},
	}
	resolved, err := w.resolveDestroyCapabilities(version, specs)
	if err != nil {
		return nil, err
	}
	steps := make([]store.OperationStep, len(specs))
	for i, spec := range specs {
		input := map[string]json.RawMessage{"instance_ref": server.InstanceRef}
		if spec.stepID == destroyInspectRuntimeStep {
			input = map[string]json.RawMessage{"unit_ref": profile.UnitRef}
		}
		step, err := w.buildDestroyStep(spec, resolved[i], input)
		if err != nil {
			return nil, err
		}
		steps[i] = step
	}
	return steps, nil
}

func (w *ServerDestroyWorkflow) resolveDestroyCapabilities(version string, specs []destroyStepSpec) ([]adapter.Capability, error) {
	prefix := "urn:gameserver-controller:" + version + ":"
	resolved := make([]adapter.Capability, len(specs))
	for i, spec := range specs {
		capability, err := exactCapability(w.adapters, spec.adapterID, spec.category, spec.name,
			prefix+spec.input, prefix+spec.output, spec.mutating)
		if err != nil {
			return nil, err
		}
		resolved[i] = capability
	}
	return resolved, nil
}

func (w *ServerDestroyWorkflow) buildDestroyStep(spec destroyStepSpec, capability adapter.Capability, input map[string]json.RawMessage) (store.OperationStep, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return store.OperationStep{}, fmt.Errorf("encode %s input: %w", spec.stepID, err)
	}
	if err := w.schemas.ValidateJSON(capability.InputSchemaURI, encoded); err != nil {
		return store.OperationStep{}, fmt.Errorf("planned %s input does not match its declared capability schema: %w", spec.stepID, err)
	}
	return store.OperationStep{
		StepID: spec.stepID, StepType: capability.Name, Status: protocolstate.StepPending,
		AdapterID: spec.adapterID, CapabilityName: capability.Name,
		CapabilityVersion: capability.Version, Input: encoded,
	}, nil
}
