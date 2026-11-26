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

// plannedRuntimeStep is one row of a runtime lifecycle plan: the step ID, the
// exact capability it pins and the input it sends. server.start and
// server.stop describe their plans as tables of these rows.
type plannedRuntimeStep struct {
	id         string
	adapterID  string
	category   adapter.Category
	capability string
	schemaStem string
	mutating   bool
	input      any
}

// capabilitySchemaURI names one protocol schema of a capability, for example
// stem "hosting:instance-inspect" with suffix "-input".
func capabilitySchemaURI(version, stem, suffix string) string {
	return "urn:gameserver-controller:" + version + ":" + stem + suffix
}

// resolvePlannedCapabilities resolves every row's exact capability in plan
// order and returns the first resolution error.
func resolvePlannedCapabilities(registry adapterRuntime, version string, rows []plannedRuntimeStep) ([]adapter.Capability, error) {
	resolved := make([]adapter.Capability, len(rows))
	for i, row := range rows {
		capability, err := exactCapability(registry, row.adapterID, row.category, row.capability,
			capabilitySchemaURI(version, row.schemaStem, "-input"),
			capabilitySchemaURI(version, row.schemaStem, "-output"), row.mutating)
		if err != nil {
			return nil, err
		}
		resolved[i] = capability
	}
	return resolved, nil
}

// encodePlannedSteps encodes every row's input, checks it against the
// resolved capability's input schema and returns the pending durable steps.
func encodePlannedSteps(schemas schemaRuntime, rows []plannedRuntimeStep, capabilities []adapter.Capability) ([]store.OperationStep, error) {
	steps := make([]store.OperationStep, len(rows))
	for i, row := range rows {
		encoded, err := json.Marshal(row.input)
		if err != nil {
			return nil, fmt.Errorf("encode %s input: %w", row.id, err)
		}
		if err := schemas.ValidateJSON(capabilities[i].InputSchemaURI, encoded); err != nil {
			return nil, fmt.Errorf("planned %s input does not match its declared capability schema: %w", row.id, err)
		}
		steps[i] = store.OperationStep{
			StepID: row.id, StepType: capabilities[i].Name, Status: protocolstate.StepPending,
			AdapterID: row.adapterID, CapabilityName: capabilities[i].Name,
			CapabilityVersion: capabilities[i].Version, Input: encoded,
		}
	}
	return steps, nil
}

// lifecycleRuntimeReady reports whether the dependencies every runtime
// lifecycle workflow shares are all present.
func lifecycleRuntimeReady(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime) bool {
	return servers != nil && adapters != nil && schemas != nil
}

// plannableOperation reports whether operation is a well-formed request of
// operationType at the loaded protocol version, with object parameters.
func plannableOperation(operation store.OperationRecord, operationType, version string) bool {
	return operationNamesType(operation, operationType, version) &&
		operationTargetsServer(operation.Target) && isObject(operation.RequestSnapshot.Parameters)
}

func operationNamesType(operation store.OperationRecord, operationType, version string) bool {
	return operation.OperationType == operationType && operation.ProtocolVersion == version &&
		operation.RequestSnapshot.OperationType == operationType
}

func operationTargetsServer(target store.OperationTarget) bool {
	return target.ServerID != "" && target.EnvironmentID != ""
}

// runtimePlanRule names the operation a runtime lifecycle plan accepts, the
// lifecycle state it must start from, and the wording of its refusals.
type runtimePlanRule struct {
	operationType  string
	initError      string
	operationError string
	label          string
	state          protocolstate.ServerState
	stateError     string
}

// newRuntimePlanRule derives a plan rule's wording from the workflow verb,
// for example "start", and the lifecycle state the plan starts from.
func newRuntimePlanRule(verb string, state protocolstate.ServerState) runtimePlanRule {
	operationType := "server." + verb
	return runtimePlanRule{
		operationType:  operationType,
		initError:      "initialized " + verb + " workflow and context are required",
		operationError: operationType + " operation and loaded protocol version are required",
		label:          verb + " plan",
		state:          state,
		stateError:     operationType + " requires a " + string(state) + " logical server with a managed instance",
	}
}

// checkPlanRequest refuses a plan request, in order, for a missing context
// or dependencies (schemas is nil then), a cancelled context, or an operation
// that is not a well-formed request of the rule's type.
func checkPlanRequest(ctx context.Context, schemas schemaRuntime, operation store.OperationRecord, rule runtimePlanRule) error {
	if ctx == nil || schemas == nil {
		return errors.New(rule.initError)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !plannableOperation(operation, rule.operationType, schemas.ProtocolVersion()) {
		return errors.New(rule.operationError)
	}
	return nil
}

// resolveTrustedProfile resolves a trusted profile for server and validates
// it, wrapping a resolver error with the operation's name.
func resolveTrustedProfile[P any](ctx context.Context, server store.LogicalServer, resolve func(context.Context, store.LogicalServer) (P, error),
	validate func(store.LogicalServer, P) error, operation string) (P, error) {
	var zero P
	profile, err := resolve(ctx, server)
	if err != nil {
		return zero, fmt.Errorf("resolve trusted server %s profile: %w", operation, err)
	}
	if err := validate(server, profile); err != nil {
		return zero, err
	}
	return profile, nil
}

// loadPlannedServer loads the target server, requires the rule's lifecycle
// state and a managed instance, and binds the target and expected instances.
func loadPlannedServer(servers store.LifecycleOperationStore, operation store.OperationRecord, expectedInstance json.RawMessage, rule runtimePlanRule) (store.LogicalServer, error) {
	stored, err := servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return store.LogicalServer{}, fmt.Errorf("load logical server for %s: %w", rule.label, err)
	}
	server := stored.Record
	if server.LifecycleState != rule.state || len(server.InstanceRef) == 0 {
		return store.LogicalServer{}, errors.New(rule.stateError)
	}
	if err := bindPlannedInstance(operation.Target.InstanceRef, expectedInstance, server.InstanceRef); err != nil {
		return store.LogicalServer{}, err
	}
	return server, nil
}

// bindPlannedInstance refuses a target or expected instance that is set and
// differs from the current managed instance, in that order.
func bindPlannedInstance(target, expected, current json.RawMessage) error {
	if len(target) > 0 && !equalJSON(target, current) {
		return store.ErrLogicalServerTargetMismatch
	}
	if len(expected) > 0 && !equalJSON(expected, current) {
		return errors.New("expected_instance_ref does not match the current managed instance")
	}
	return nil
}

// boundUnitRef is the part of a trusted execution unit reference that plans
// check before they pin it.
type boundUnitRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}

// decodeBoundUnitRef decodes a trusted unit reference and reports whether it
// is an object on the server's execution adapter and environment with an
// external ID. Callers add their own resource-kind rule.
func decodeBoundUnitRef(raw json.RawMessage, server store.LogicalServer) (boundUnitRef, bool) {
	var ref boundUnitRef
	if err := json.Unmarshal(raw, &ref); err != nil || !isObject(raw) {
		return ref, false
	}
	return ref, unitRefOnServer(ref, server) && ref.ExternalID != ""
}

func unitRefOnServer(ref boundUnitRef, server store.LogicalServer) bool {
	return ref.AdapterID == server.ExecutionAdapterID && ref.EnvironmentID == server.EnvironmentID
}

// boundedProfileID reports whether a trusted profile identifier is set and
// at most limit bytes long.
func boundedProfileID(value string, limit int) bool {
	return value != "" && len(value) <= limit
}

func adapterIDFromRef(value json.RawMessage) string {
	var ref struct {
		AdapterID string `json:"adapter_id"`
	}
	// An undecodable reference leaves the ID empty. No adapter registers the
	// empty ID, so the caller's registry lookup fails closed.
	_ = json.Unmarshal(value, &ref)
	return ref.AdapterID
}

// exactCapability resolves one exact capability declaration. It keeps its
// seven parameters because workflows outside server.start and server.stop
// call it; resolvePlannedCapabilities is the table-driven form.
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
