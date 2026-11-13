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
	destroyInspectInstanceStep = "inspect-instance"
	destroyInspectRuntimeStep  = "inspect-runtime"
	destroyProviderStep        = "destroy-instance"
	destroyVerifyInstanceStep  = "verify-instance-absence"
)

// ServerDestroyProfile contains trusted execution references selected from
// controller-owned deployment metadata. Client parameters cannot choose the
// unit inspected before destruction.
type ServerDestroyProfile struct {
	UnitRef json.RawMessage
}

// ServerDestroyProfileResolver resolves the exact configured runtime unit for
// a logical server without using request-supplied resource references.
type ServerDestroyProfileResolver interface {
	ResolveServerDestroy(context.Context, store.LogicalServer) (ServerDestroyProfile, error)
}

// VerifiedServerBackup is returned only after the resolver has checked the
// immutable manifest, every object and checksum, environment ownership, and
// current data generation. SourceInstanceRef binds the backup to the exact
// managed instance being destroyed.
type VerifiedServerBackup struct {
	BackupID          string
	ServerID          string
	EnvironmentID     string
	StorageProfile    string
	DataGenerationID  string
	SourceInstanceRef json.RawMessage
	IntegrityVerified bool
}

// ServerDestroyBackupResolver must return a durable, integrity-verified
// backup for the requested profile and backup ID. An empty returned backup ID
// is never accepted as evidence that destruction is safe.
type ServerDestroyBackupResolver interface {
	ResolveVerifiedServerBackup(context.Context, store.LogicalServer, string, string) (VerifiedServerBackup, error)
}

// ServerDestroyAuthorizer revalidates a destructive approval for the exact
// actor, operation, server, instance, and verified backup. Implementations
// must reject expired, consumed, or mismatched approvals.
type ServerDestroyAuthorizer interface {
	AuthorizeServerDestroy(context.Context, string, string, store.OperationRecord, store.LogicalServer, VerifiedServerBackup) error
}

// serverDestroyCapacityStore is optional so the workflow can also run on
// stores that do not implement the pilot's managed-VM reservation policy.
// When present, an exact destroy claim stays occupied until provider absence
// is confirmed and persisted.
type serverDestroyCapacityStore interface {
	ListCapacityReservations() ([]store.CapacityReservation, error)
	MarkCapacityDestroySent(string) (store.CapacityReservation, error)
}

type serverDestroyParameters struct {
	ExpectedInstanceRef json.RawMessage `json:"expected_instance_ref"`
	BackupProfile       string          `json:"backup_profile"`
	VerifiedBackupID    string          `json:"verified_backup_id"`
	ApprovalID          string          `json:"approval_id"`
}

type serverDestroyInstanceInspection struct {
	InstanceRef json.RawMessage `json:"instance_ref"`
	State       string          `json:"state"`
	ObservedAt  string          `json:"observed_at"`
}

type serverDestroyUnitInspection struct {
	UnitRef    json.RawMessage `json:"unit_ref"`
	State      string          `json:"state"`
	ObservedAt string          `json:"observed_at"`
}

type serverDestroyOutput struct {
	InstanceRef          json.RawMessage `json:"instance_ref"`
	Outcome              string          `json:"outcome"`
	State                string          `json:"state"`
	ObservedAt           string          `json:"observed_at"`
	ProviderOperationRef json.RawMessage `json:"provider_operation_ref,omitempty"`
}

type serverDestroyWaitOutput struct {
	InstanceRef          json.RawMessage `json:"instance_ref"`
	ProviderOperationRef json.RawMessage `json:"provider_operation_ref"`
	OperationStatus      string          `json:"operation_status"`
	InstanceState        string          `json:"instance_state,omitempty"`
	ObservedAt           string          `json:"observed_at"`
}

type ServerDestroyWorkflow struct {
	servers    store.LifecycleOperationStore
	adapters   adapterRuntime
	schemas    schemaRuntime
	profiles   ServerDestroyProfileResolver
	backups    ServerDestroyBackupResolver
	authorizer ServerDestroyAuthorizer
}

func NewServerDestroyWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime,
	profiles ServerDestroyProfileResolver, backups ServerDestroyBackupResolver, authorizer ServerDestroyAuthorizer) (*ServerDestroyWorkflow, error) {
	if servers == nil || adapters == nil || schemas == nil || profiles == nil || backups == nil || authorizer == nil {
		return nil, errors.New("lifecycle store, adapter registry, schemas, destroy profile, verified backup resolver, and destroy authorizer are required")
	}
	return &ServerDestroyWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles, backups: backups, authorizer: authorizer}, nil
}

// BuildPlan pins the exact provider instance, runtime unit, backup, and
// capability identities before the first dispatch. The request must name a
// verified backup for the current data generation and an exact-target approval.
func (w *ServerDestroyWorkflow) BuildPlan(ctx context.Context, operation store.OperationRecord) ([]store.OperationStep, error) {
	if ctx == nil || w == nil || w.servers == nil || w.adapters == nil || w.schemas == nil || w.profiles == nil || w.backups == nil || w.authorizer == nil {
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
	if server.LifecycleState != protocolstate.ServerStopped && server.LifecycleState != protocolstate.ServerFailed {
		return nil, errors.New("server.destroy requires a stopped or failed logical server")
	}
	backup, err := w.verifiedBackup(ctx, server, parameters)
	if err != nil {
		return nil, err
	}
	if err := w.authorizer.AuthorizeServerDestroy(ctx, operation.ActorID, parameters.ApprovalID, operation, server, backup); err != nil {
		return nil, fmt.Errorf("authorize exact server.destroy request: %w", err)
	}
	profile, err := w.profiles.ResolveServerDestroy(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("resolve trusted server destroy profile: %w", err)
	}
	if err := validateServerDestroyProfile(server, profile); err != nil {
		return nil, err
	}

	version := w.schemas.ProtocolVersion()
	hostingID := adapterIDFromRef(server.InstanceRef)
	capabilities := []struct {
		stepID, adapterID   string
		category            adapter.Category
		name, input, output string
		mutating            bool
	}{
		{destroyInspectInstanceStep, hostingID, adapter.CategoryHosting, "hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false},
		{destroyInspectRuntimeStep, server.ExecutionAdapterID, adapter.CategoryExecution, "execution.unit.inspect", "execution:systemd-unit-inspect-input", "execution:systemd-unit-inspect-output", false},
		{destroyProviderStep, hostingID, adapter.CategoryHosting, "hosting.instance.destroy", "hosting:instance-destroy-input", "hosting:instance-destroy-output", true},
		{destroyVerifyInstanceStep, hostingID, adapter.CategoryHosting, "hosting.instance.inspect", "hosting:instance-inspect-input", "hosting:instance-inspect-output", false},
	}
	resolved := make([]adapter.Capability, len(capabilities))
	for i, required := range capabilities {
		resolved[i], err = exactCapability(w.adapters, required.adapterID, required.category, required.name,
			"urn:gameserver-controller:"+version+":"+required.input,
			"urn:gameserver-controller:"+version+":"+required.output, required.mutating)
		if err != nil {
			return nil, err
		}
	}

	inputs := []any{
		map[string]any{"instance_ref": server.InstanceRef},
		map[string]any{"unit_ref": profile.UnitRef},
		map[string]any{"instance_ref": server.InstanceRef},
		map[string]any{"instance_ref": server.InstanceRef},
	}
	steps := make([]store.OperationStep, len(capabilities))
	for i, required := range capabilities {
		encoded, err := json.Marshal(inputs[i])
		if err != nil {
			return nil, fmt.Errorf("encode %s input: %w", required.stepID, err)
		}
		if err := w.schemas.ValidateJSON(resolved[i].InputSchemaURI, encoded); err != nil {
			return nil, fmt.Errorf("planned %s input does not match its declared capability schema: %w", required.stepID, err)
		}
		steps[i] = store.OperationStep{
			StepID: required.stepID, StepType: resolved[i].Name, Status: protocolstate.StepPending,
			AdapterID: required.adapterID, CapabilityName: resolved[i].Name,
			CapabilityVersion: resolved[i].Version, Input: encoded,
		}
	}
	return steps, nil
}

func serverDestroyRequest(operation store.OperationRecord, protocolVersion string) (serverDestroyParameters, error) {
	var parameters serverDestroyParameters
	if operation.OperationType != "server.destroy" || operation.ProtocolVersion != protocolVersion || protocolVersion == "" ||
		operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" || operation.RequestSnapshot.OperationType != "server.destroy" ||
		!isObject(operation.RequestSnapshot.Parameters) {
		return parameters, errors.New("server.destroy operation and loaded protocol version are required")
	}
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return parameters, errors.New("decode server.destroy parameters")
	}
	if parameters.BackupProfile == "" || parameters.VerifiedBackupID == "" || parameters.ApprovalID == "" ||
		len(parameters.ExpectedInstanceRef) == 0 || !isObject(parameters.ExpectedInstanceRef) {
		return parameters, errors.New("server.destroy requires an exact instance, backup profile, verified backup ID, and approval ID")
	}
	return parameters, nil
}

func validateServerDestroyTarget(operation store.OperationRecord, parameters serverDestroyParameters, server store.LogicalServer) error {
	if server.ServerID != operation.Target.ServerID || server.EnvironmentID != operation.Target.EnvironmentID || len(server.InstanceRef) == 0 ||
		!equalJSON(operation.Target.InstanceRef, server.InstanceRef) || !equalJSON(parameters.ExpectedInstanceRef, server.InstanceRef) {
		return store.ErrLogicalServerTargetMismatch
	}
	if server.DataGenerationStatus != "known" || server.DataGenerationID == "" {
		return errors.New("server.destroy requires a known current data generation")
	}
	return nil
}

func validateServerDestroyProfile(server store.LogicalServer, profile ServerDestroyProfile) error {
	var ref struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if err := json.Unmarshal(profile.UnitRef, &ref); err != nil || !isObject(profile.UnitRef) ||
		ref.AdapterID != server.ExecutionAdapterID || ref.EnvironmentID != server.EnvironmentID ||
		ref.ResourceKind != "systemd-unit" || ref.ExternalID == "" {
		return errors.New("trusted destroy profile unit reference does not bind the configured execution adapter and environment")
	}
	return nil
}

func (w *ServerDestroyWorkflow) verifiedBackup(ctx context.Context, server store.LogicalServer, parameters serverDestroyParameters) (VerifiedServerBackup, error) {
	backup, err := w.backups.ResolveVerifiedServerBackup(ctx, server, parameters.BackupProfile, parameters.VerifiedBackupID)
	if err != nil {
		return VerifiedServerBackup{}, fmt.Errorf("resolve verified backup for server.destroy: %w", err)
	}
	if !backup.IntegrityVerified || backup.BackupID != parameters.VerifiedBackupID || backup.ServerID != server.ServerID ||
		backup.EnvironmentID != server.EnvironmentID || backup.StorageProfile != parameters.BackupProfile ||
		backup.DataGenerationID != server.DataGenerationID || !equalJSON(backup.SourceInstanceRef, server.InstanceRef) {
		return VerifiedServerBackup{}, errors.New("verified backup does not bind the exact server, environment, storage profile, instance, and current data generation")
	}
	return backup, nil
}

// CheckPreconditions rechecks the exact managed resources before each planned
// step and revalidates backup integrity and approval immediately before provider
// destruction.
func (w *ServerDestroyWorkflow) CheckPreconditions(ctx context.Context, operation store.OperationRecord, step store.OperationStep) error {
	parameters, server, err := w.destroyPreconditionTarget(ctx, operation)
	if err != nil {
		return err
	}
	backup, err := w.verifiedBackup(ctx, server, parameters)
	if err != nil {
		return err
	}
	if err := destroyProviderInspectionPrecondition(operation, step, server); err != nil {
		return err
	}
	rule, ok := destroyStepPreconditions[step.StepID]
	if !ok {
		return fmt.Errorf("unsupported server.destroy step %q", step.StepID)
	}
	return rule(w, ctx, destroyPreconditionState{operation: operation, step: step, parameters: parameters, server: server, backup: backup})
}

// destroyPreconditionState is the evidence a step rule checks once the shared
// target, plan, backup, and provider-inspection stages have passed.
type destroyPreconditionState struct {
	operation  store.OperationRecord
	step       store.OperationStep
	parameters serverDestroyParameters
	server     store.LogicalServer
	backup     VerifiedServerBackup
}

// destroyStepPreconditions is the rule for each planned server.destroy step.
var destroyStepPreconditions = map[string]func(*ServerDestroyWorkflow, context.Context, destroyPreconditionState) error{
	destroyInspectInstanceStep: (*ServerDestroyWorkflow).checkDestroyInspectInstance,
	destroyInspectRuntimeStep:  (*ServerDestroyWorkflow).checkDestroyInspectRuntime,
	destroyProviderStep:        (*ServerDestroyWorkflow).checkDestroyProvider,
	destroyVerifyInstanceStep:  (*ServerDestroyWorkflow).checkDestroyVerifyInstance,
}

// destroyPreconditionTarget loads the current logical server and checks that
// the operation and every instance-bound step still target it exactly.
func (w *ServerDestroyWorkflow) destroyPreconditionTarget(ctx context.Context, operation store.OperationRecord) (serverDestroyParameters, store.LogicalServer, error) {
	if ctx == nil || w == nil || operation.OperationType != "server.destroy" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return serverDestroyParameters{}, store.LogicalServer{}, errors.New("server.destroy operation and context are required")
	}
	if err := ctx.Err(); err != nil {
		return serverDestroyParameters{}, store.LogicalServer{}, err
	}
	parameters, err := serverDestroyRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return serverDestroyParameters{}, store.LogicalServer{}, err
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return serverDestroyParameters{}, store.LogicalServer{}, err
	}
	server := stored.Record
	if err := validateServerDestroyTarget(operation, parameters, server); err != nil {
		return serverDestroyParameters{}, store.LogicalServer{}, err
	}
	if !destroyPlanBindsInstance(operation, server.InstanceRef) {
		return serverDestroyParameters{}, store.LogicalServer{}, errors.New("server.destroy plan is not bound to the current managed instance")
	}
	return parameters, server, nil
}

func destroyPlanBindsInstance(operation store.OperationRecord, instanceRef json.RawMessage) bool {
	for _, stepID := range []string{destroyInspectInstanceStep, destroyProviderStep, destroyVerifyInstanceStep} {
		if !stepBindsRef(operationStep(operation, stepID).Input, "instance_ref", instanceRef) {
			return false
		}
	}
	return true
}

// destroyProviderInspectionPrecondition requires every step after the initial
// inspection to follow a fresh exact-provider inspection in active or stopped state.
func destroyProviderInspectionPrecondition(operation store.OperationRecord, step store.OperationStep, server store.LogicalServer) error {
	provider, err := destroyInstanceStepOutput(operation, destroyInspectInstanceStep, protocolstate.StepSucceeded)
	if step.StepID != destroyInspectInstanceStep && (err != nil || !equalJSON(provider.InstanceRef, server.InstanceRef) || !activeOrStopped(provider.State)) {
		return errors.New("server.destroy requires a successful exact-provider inspection in active or stopped state")
	}
	return nil
}

func (w *ServerDestroyWorkflow) checkDestroyInspectInstance(_ context.Context, check destroyPreconditionState) error {
	if !stoppedOrFailedLifecycle(check.server.LifecycleState) || check.step.Status != protocolstate.StepPending ||
		!stepBindsRef(check.step.Input, "instance_ref", check.server.InstanceRef) {
		return errors.New("server.destroy requires a stopped or failed server and exact initial provider inspection")
	}
	return nil
}

func (w *ServerDestroyWorkflow) checkDestroyInspectRuntime(_ context.Context, check destroyPreconditionState) error {
	if !stoppedOrFailedLifecycle(check.server.LifecycleState) || !stepSucceeded(check.operation, destroyInspectInstanceStep) {
		return errors.New("runtime inspection requires a stopped or failed server and successful provider inspection")
	}
	return nil
}

// checkDestroyProvider guards the only mutating step: it requires a stopped
// runtime, revalidates the exact approval, and claims capacity last.
func (w *ServerDestroyWorkflow) checkDestroyProvider(ctx context.Context, check destroyPreconditionState) error {
	server := check.server
	if !stoppedOrFailedLifecycle(server.LifecycleState) && server.LifecycleState != protocolstate.ServerDestroying {
		return errors.New("provider destruction requires a stopped, failed, or already-destroying server")
	}
	if !stepSucceeded(check.operation, destroyInspectRuntimeStep) || !stepBindsRef(check.step.Input, "instance_ref", server.InstanceRef) {
		return errors.New("provider destruction requires successful exact-instance and runtime inspections")
	}
	if !destroyRuntimeStoppedOrAbsent(check.operation) {
		return errors.New("provider destruction requires the exact execution unit to be stopped or absent")
	}
	if server.LifecycleState == protocolstate.ServerDestroying && check.step.Attempt == 0 {
		return errors.New("new destroy dispatch cannot start from an unexplained destroying state")
	}
	if err := w.authorizer.AuthorizeServerDestroy(ctx, check.operation.ActorID, check.parameters.ApprovalID, check.operation, server, check.backup); err != nil {
		return fmt.Errorf("revalidate exact server.destroy approval before provider call: %w", err)
	}
	return w.markCapacityDestroySent(server)
}

// destroyRuntimeStoppedOrAbsent reports whether the recorded runtime
// inspection observed the planned execution unit stopped or absent.
func destroyRuntimeStoppedOrAbsent(operation store.OperationRecord) bool {
	runtime, err := destroyRuntimeStepOutput(operation)
	return err == nil && equalJSON(runtime.UnitRef, stepField(operationStep(operation, destroyInspectRuntimeStep).Input, "unit_ref")) &&
		(runtime.State == "stopped" || runtime.State == "absent")
}

func (w *ServerDestroyWorkflow) checkDestroyVerifyInstance(_ context.Context, check destroyPreconditionState) error {
	if check.server.LifecycleState != protocolstate.ServerDestroying || !stepSucceeded(check.operation, destroyProviderStep) {
		return errors.New("provider absence verification requires a dispatched destroy and destroying lifecycle state")
	}
	result, err := destroyProviderOutput(check.operation)
	if err != nil || result.Outcome != "completed" || result.State != "destroyed" || !equalJSON(result.InstanceRef, check.server.InstanceRef) {
		return errors.New("provider absence verification requires the exact completed destroy output")
	}
	return nil
}

func activeOrStopped(state string) bool { return state == "active" || state == "stopped" }

func stoppedOrFailedLifecycle(state protocolstate.ServerState) bool {
	return state == protocolstate.ServerStopped || state == protocolstate.ServerFailed
}

func (w *ServerDestroyWorkflow) markCapacityDestroySent(server store.LogicalServer) error {
	capacityStore, ok := w.servers.(serverDestroyCapacityStore)
	if !ok {
		return nil
	}
	reservation, found, err := matchingDestroyCapacityReservation(capacityStore, server)
	if err != nil || !found {
		return err
	}
	switch reservation.State {
	case store.CapacityActive:
		if _, err := capacityStore.MarkCapacityDestroySent(reservation.OperationID); err != nil {
			return fmt.Errorf("claim managed VM capacity before provider destroy: %w", err)
		}
	case store.CapacityDestroyOutcomeUnknown:
		// The prior claim may belong to this operation after a crash, or to a
		// previous destroy attempt that reconciled to a safe retry. The slot
		// remains occupied and the exact live provider/runtime inspections above
		// are required before another destroy call.
	default:
		return fmt.Errorf("managed VM capacity reservation is %q; refusing provider destroy", reservation.State)
	}
	return nil
}

func matchingDestroyCapacityReservation(capacityStore serverDestroyCapacityStore, server store.LogicalServer) (store.CapacityReservation, bool, error) {
	reservations, err := capacityStore.ListCapacityReservations()
	if err != nil {
		return store.CapacityReservation{}, false, fmt.Errorf("list managed VM capacity reservations: %w", err)
	}
	instanceAdapterID, instanceID, err := externalInstanceIdentity(server.InstanceRef)
	if err != nil {
		return store.CapacityReservation{}, false, err
	}
	var match *store.CapacityReservation
	for _, reservation := range reservations {
		if reservation.ServerID != server.ServerID || reservation.EnvironmentID != server.EnvironmentID {
			continue
		}
		if reservation.ProviderInstanceID == instanceID &&
			(reservation.ProviderAdapterID == "" || reservation.ProviderAdapterID == instanceAdapterID) &&
			(reservation.State == store.CapacityAbsent || reservation.State == store.CapacityDestroyed) {
			if match != nil {
				return store.CapacityReservation{}, false, errors.New("multiple managed VM reservations match one exact server instance")
			}
			copyReservation := reservation
			match = &copyReservation
			continue
		}
		if reservation.State == store.CapacityAbsent || reservation.State == store.CapacityDestroyed {
			continue
		}
		if reservation.ProviderAdapterID != "" && reservation.ProviderAdapterID != instanceAdapterID {
			return store.CapacityReservation{}, false, errors.New("managed VM reservation belongs to a different hosting adapter")
		}
		if reservation.ProviderInstanceID != instanceID {
			return store.CapacityReservation{}, false, errors.New("managed VM reservation does not identify the exact server instance")
		}
		if match != nil {
			return store.CapacityReservation{}, false, errors.New("multiple active managed VM reservations match one logical server")
		}
		copyReservation := reservation
		match = &copyReservation
	}
	if match == nil {
		return store.CapacityReservation{}, false, nil
	}
	return *match, true, nil
}

func externalInstanceIdentity(raw json.RawMessage) (string, string, error) {
	var ref struct {
		AdapterID    string `json:"adapter_id"`
		ResourceKind string `json:"resource_kind"`
		ExternalID   string `json:"external_id"`
	}
	if !isObject(raw) || json.Unmarshal(raw, &ref) != nil || ref.AdapterID == "" || ref.ResourceKind != "instance" || ref.ExternalID == "" {
		return "", "", errors.New("managed VM capacity requires an exact provider instance reference")
	}
	return ref.AdapterID, ref.ExternalID, nil
}

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

func (w *ServerDestroyWorkflow) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if ctx == nil || operation.OperationType != "server.destroy" {
		return errors.New("server.destroy completion validation requires an operation and context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	parameters, err := serverDestroyRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return err
	}
	providerBefore, err := destroyInstanceStepOutput(operation, destroyInspectInstanceStep, protocolstate.StepSucceeded)
	if err != nil || !equalJSON(providerBefore.InstanceRef, parameters.ExpectedInstanceRef) || !activeOrStopped(providerBefore.State) {
		return errors.New("server.destroy lacks exact pre-destruction provider evidence")
	}
	runtime, err := destroyRuntimeStepOutput(operation)
	if err != nil || !equalJSON(runtime.UnitRef, stepField(operationStep(operation, destroyInspectRuntimeStep).Input, "unit_ref")) ||
		(runtime.State != "stopped" && runtime.State != "absent") {
		return errors.New("server.destroy lacks exact stopped-runtime evidence")
	}
	destroyed, err := destroyProviderOutput(operation)
	if err != nil || destroyed.Outcome != "completed" || destroyed.State != "destroyed" || !equalJSON(destroyed.InstanceRef, parameters.ExpectedInstanceRef) {
		return errors.New("server.destroy lacks an exact completed provider destruction")
	}
	absence, err := destroyInstanceStepOutput(operation, destroyVerifyInstanceStep, protocolstate.StepSucceeded)
	if err != nil || absence.State != "destroyed" || !equalJSON(absence.InstanceRef, parameters.ExpectedInstanceRef) {
		return errors.New("server.destroy lacks exact provider-absence verification")
	}
	if err := observationAfter(destroyed.ObservedAt, absence.ObservedAt); err != nil {
		return fmt.Errorf("provider absence observation predates destroy completion: %w", err)
	}
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return err
	}
	if err := validateServerDestroyTarget(operation, parameters, server.Record); err != nil {
		return err
	}
	if _, err := w.verifiedBackup(ctx, server.Record, parameters); err != nil {
		return err
	}
	return nil
}

func (w *ServerDestroyWorkflow) BuildSuccessResult(ctx context.Context, operation store.OperationRecord) (json.RawMessage, error) {
	if err := w.ValidateCompletion(ctx, operation); err != nil {
		return nil, err
	}
	parameters, _ := serverDestroyRequest(operation, w.schemas.ProtocolVersion())
	server, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"outcome_summary": "The selected backup was verified and the exact provider instance is confirmed absent.",
		"operation_type":  "server.destroy", "affected_resources": []any{parameters.ExpectedInstanceRef},
		"completed_steps": completedStepSummaries(operation), "failed_steps": []any{},
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": false,
		"operation_data": map[string]any{
			"instance_ref": parameters.ExpectedInstanceRef, "verified_backup_id": parameters.VerifiedBackupID,
			"data_generation_id": server.Record.DataGenerationID, "provider_absence_confirmed": true,
			"final_lifecycle_state": string(protocolstate.ServerDestroyed),
		},
	}
	return json.Marshal(result)
}

func (w *ServerDestroyWorkflow) BuildFailureResult(_ context.Context, operation store.OperationRecord, step store.OperationStep, failure json.RawMessage) (json.RawMessage, error) {
	failed := []any{}
	if step.StepID != "" && len(failure) > 0 && step.Status == protocolstate.StepFailed {
		failed = append(failed, map[string]any{"step_id": step.StepID, "step_type": step.StepType, "error": failure})
	}
	partial := operationStep(operation, destroyProviderStep).Attempt > 0
	affected := []any{}
	if partial && len(operation.Target.InstanceRef) > 0 {
		affected = append(affected, operation.Target.InstanceRef)
	}
	return json.Marshal(map[string]any{
		"outcome_summary": "Server destruction did not confirm provider absence; known effects remain attached to this operation.",
		"operation_type":  "server.destroy", "affected_resources": affected,
		"completed_steps": completedStepSummaries(operation), "failed_steps": failed,
		"warnings": []string{}, "unresolved_conditions": []any{}, "audit_event_ids": []string{}, "partial": partial,
	})
}

func (w *ServerDestroyWorkflow) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	if operation.OperationType != "server.destroy" || current.Record.ServerID != operation.Target.ServerID || current.Record.EnvironmentID != operation.Target.EnvironmentID ||
		!equalJSON(operation.Target.InstanceRef, current.Record.InstanceRef) {
		return store.LogicalServer{}, false, errors.New("server.destroy lifecycle projection target mismatch")
	}
	next := current.Record
	destroyStep := operationStep(operation, destroyProviderStep)
	switch current.Record.LifecycleState {
	case protocolstate.ServerStopped, protocolstate.ServerFailed:
		if destroyStep.Attempt > 0 || destroyStep.Status != protocolstate.StepPending {
			next.LifecycleState = protocolstate.ServerDestroying
			next.DesiredState = "destroyed"
			return next, true, nil
		}
	case protocolstate.ServerDestroying:
		if operation.Status == protocolstate.OperationSucceeded {
			if !hasValidServerDestroyResult(operation, current.Record, w.schemas) {
				return store.LogicalServer{}, false, errors.New("terminal server.destroy result lacks exact backup and provider-absence evidence")
			}
			next.LifecycleState = protocolstate.ServerDestroyed
			next.DesiredState = "destroyed"
			next.InstanceRef = nil
			return next, true, nil
		}
		if operation.Status == protocolstate.OperationFailed {
			next.LifecycleState = protocolstate.ServerFailed
			next.DesiredState = "destroyed"
			return next, true, nil
		}
	}
	return current.Record, false, nil
}

func hasValidServerDestroyResult(operation store.OperationRecord, server store.LogicalServer, schemas schemaRuntime) bool {
	var result struct {
		OperationType string `json:"operation_type"`
		OperationData struct {
			InstanceRef         json.RawMessage `json:"instance_ref"`
			VerifiedBackupID    string          `json:"verified_backup_id"`
			DataGenerationID    string          `json:"data_generation_id"`
			ProviderAbsence     bool            `json:"provider_absence_confirmed"`
			FinalLifecycleState string          `json:"final_lifecycle_state"`
		} `json:"operation_data"`
	}
	var parameters serverDestroyParameters
	if json.Unmarshal(operation.Result, &result) != nil || json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters) != nil ||
		result.OperationType != "server.destroy" || !equalJSON(result.OperationData.InstanceRef, server.InstanceRef) ||
		result.OperationData.VerifiedBackupID != parameters.VerifiedBackupID || result.OperationData.DataGenerationID != server.DataGenerationID ||
		!result.OperationData.ProviderAbsence || result.OperationData.FinalLifecycleState != string(protocolstate.ServerDestroyed) {
		return false
	}
	return (len(operation.Target.InstanceRef) == 0 || equalJSON(result.OperationData.InstanceRef, operation.Target.InstanceRef)) &&
		schemas.ValidateJSON("urn:gameserver-controller:"+schemas.ProtocolVersion()+":operations:server-destroy-result", operation.Result) == nil
}

func destroyInstanceStepOutput(operation store.OperationRecord, stepID string, requiredStatus protocolstate.StepStatus) (serverDestroyInstanceInspection, error) {
	var output serverDestroyInstanceInspection
	step := operationStep(operation, stepID)
	if step.StepID == "" || step.Status != requiredStatus || json.Unmarshal(step.Output, &output) != nil {
		return serverDestroyInstanceInspection{}, errors.New("provider instance inspection output is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return serverDestroyInstanceInspection{}, err
	}
	if !observationIsFreshForStep(output.ObservedAt, step) {
		return serverDestroyInstanceInspection{}, errors.New("provider observation is stale or outside the tolerated clock window")
	}
	return output, nil
}

func destroyRuntimeStepOutput(operation store.OperationRecord) (serverDestroyUnitInspection, error) {
	var output serverDestroyUnitInspection
	step := operationStep(operation, destroyInspectRuntimeStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return serverDestroyUnitInspection{}, errors.New("runtime inspection output is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return serverDestroyUnitInspection{}, err
	}
	if !observationIsFreshForStep(output.ObservedAt, step) {
		return serverDestroyUnitInspection{}, errors.New("runtime observation is stale or outside the tolerated clock window")
	}
	return output, nil
}

func destroyProviderOutput(operation store.OperationRecord) (serverDestroyOutput, error) {
	var output serverDestroyOutput
	step := operationStep(operation, destroyProviderStep)
	if step.StepID == "" || step.Status != protocolstate.StepSucceeded || json.Unmarshal(step.Output, &output) != nil {
		return serverDestroyOutput{}, errors.New("provider destruction output is missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, output.ObservedAt); err != nil {
		return serverDestroyOutput{}, err
	}
	if !observationIsFreshForStep(output.ObservedAt, step) {
		return serverDestroyOutput{}, errors.New("provider destroy result is stale or outside the tolerated clock window")
	}
	return output, nil
}

func observationIsFreshForStep(value string, step store.OperationStep) bool {
	observed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || step.StartedAt == nil {
		return false
	}
	// Permit small provider clock drift, but reject evidence that could predate
	// the inspection or implausibly claim to come from the future.
	const toleratedClockSkew = 30 * time.Second
	if observed.Before(step.StartedAt.Add(-toleratedClockSkew)) || observed.After(time.Now().UTC().Add(5*time.Minute)) {
		return false
	}
	return true
}

func observationAfter(before, after string) error {
	first, err := time.Parse(time.RFC3339Nano, before)
	if err != nil {
		return err
	}
	second, err := time.Parse(time.RFC3339Nano, after)
	if err != nil {
		return err
	}
	if second.Before(first) {
		return errors.New("observation timestamp moved backwards")
	}
	return nil
}

func destroyReconcileRequestID(operationID, stepID string, attempt int, label string) string {
	return fmt.Sprintf("destroy-%s-%s-%d-%s", operationID, stepID, attempt, label)
}
