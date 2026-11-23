package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	if !destroyRuntimeDepsConfigured(servers, adapters, schemas, profiles, backups, authorizer) {
		return nil, errors.New("lifecycle store, adapter registry, schemas, destroy profile, verified backup resolver, and destroy authorizer are required")
	}
	return &ServerDestroyWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles, backups: backups, authorizer: authorizer}, nil
}

// destroyRuntimeDepsConfigured reports whether every dependency the destroy
// workflow needs to run is present.
func destroyRuntimeDepsConfigured(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime,
	profiles ServerDestroyProfileResolver, backups ServerDestroyBackupResolver, authorizer ServerDestroyAuthorizer) bool {
	if servers == nil || adapters == nil || schemas == nil {
		return false
	}
	if profiles == nil || backups == nil || authorizer == nil {
		return false
	}
	return true
}

func serverDestroyRequest(operation store.OperationRecord, protocolVersion string) (serverDestroyParameters, error) {
	var parameters serverDestroyParameters
	if !destroyRequestOperationMatches(operation, protocolVersion) {
		return parameters, errors.New("server.destroy operation and loaded protocol version are required")
	}
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return parameters, errors.New("decode server.destroy parameters")
	}
	if !destroyRequestParametersComplete(parameters) {
		return parameters, errors.New("server.destroy requires an exact instance, backup profile, verified backup ID, and approval ID")
	}
	return parameters, nil
}

// destroyRequestOperationMatches reports whether the stored operation is a
// server.destroy that binds the loaded protocol version and a non-empty target.
func destroyRequestOperationMatches(operation store.OperationRecord, protocolVersion string) bool {
	if operation.OperationType != "server.destroy" || operation.ProtocolVersion != protocolVersion || protocolVersion == "" {
		return false
	}
	if operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" ||
		operation.RequestSnapshot.OperationType != "server.destroy" {
		return false
	}
	return isObject(operation.RequestSnapshot.Parameters)
}

// destroyRequestParametersComplete reports whether the decoded parameters carry
// the exact instance, backup profile, verified backup ID, and approval ID.
func destroyRequestParametersComplete(parameters serverDestroyParameters) bool {
	if parameters.BackupProfile == "" || parameters.VerifiedBackupID == "" || parameters.ApprovalID == "" {
		return false
	}
	return len(parameters.ExpectedInstanceRef) > 0 && isObject(parameters.ExpectedInstanceRef)
}

func validateServerDestroyTarget(operation store.OperationRecord, parameters serverDestroyParameters, server store.LogicalServer) error {
	if !destroyTargetMatchesServer(operation, parameters, server) {
		return store.ErrLogicalServerTargetMismatch
	}
	if server.DataGenerationStatus != "known" || server.DataGenerationID == "" {
		return errors.New("server.destroy requires a known current data generation")
	}
	return nil
}

// destroyTargetMatchesServer reports whether the operation target and the
// requested instance both identify the exact current logical server.
func destroyTargetMatchesServer(operation store.OperationRecord, parameters serverDestroyParameters, server store.LogicalServer) bool {
	if server.ServerID != operation.Target.ServerID || server.EnvironmentID != operation.Target.EnvironmentID || len(server.InstanceRef) == 0 {
		return false
	}
	return equalJSON(operation.Target.InstanceRef, server.InstanceRef) && equalJSON(parameters.ExpectedInstanceRef, server.InstanceRef)
}

func validateServerDestroyProfile(server store.LogicalServer, profile ServerDestroyProfile) error {
	var ref struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		ResourceKind  string `json:"resource_kind"`
		ExternalID    string `json:"external_id"`
	}
	if err := json.Unmarshal(profile.UnitRef, &ref); err != nil {
		return errors.New("trusted destroy profile unit reference does not bind the configured execution adapter and environment")
	}
	if !isObject(profile.UnitRef) || !destroyProfileUnitRefBindsServer(ref, server) {
		return errors.New("trusted destroy profile unit reference does not bind the configured execution adapter and environment")
	}
	return nil
}

// destroyProfileUnitRefBindsServer reports whether a parsed profile unit
// reference is a systemd unit owned by the server's execution adapter and
// environment.
func destroyProfileUnitRefBindsServer(ref struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	ResourceKind  string `json:"resource_kind"`
	ExternalID    string `json:"external_id"`
}, server store.LogicalServer) bool {
	return ref.AdapterID == server.ExecutionAdapterID && ref.EnvironmentID == server.EnvironmentID &&
		ref.ResourceKind == "systemd-unit" && ref.ExternalID != ""
}

func (w *ServerDestroyWorkflow) verifiedBackup(ctx context.Context, server store.LogicalServer, parameters serverDestroyParameters) (VerifiedServerBackup, error) {
	backup, err := w.backups.ResolveVerifiedServerBackup(ctx, server, parameters.BackupProfile, parameters.VerifiedBackupID)
	if err != nil {
		return VerifiedServerBackup{}, fmt.Errorf("resolve verified backup for server.destroy: %w", err)
	}
	if !verifiedBackupBindsServer(backup, server, parameters) {
		return VerifiedServerBackup{}, errors.New("verified backup does not bind the exact server, environment, storage profile, instance, and current data generation")
	}
	return backup, nil
}

// verifiedBackupBindsServer reports whether the resolved backup is the exact
// verified backup for the server's current data generation and instance.
func verifiedBackupBindsServer(backup VerifiedServerBackup, server store.LogicalServer, parameters serverDestroyParameters) bool {
	if !backup.IntegrityVerified || backup.BackupID != parameters.VerifiedBackupID || backup.ServerID != server.ServerID {
		return false
	}
	if backup.EnvironmentID != server.EnvironmentID || backup.StorageProfile != parameters.BackupProfile ||
		backup.DataGenerationID != server.DataGenerationID {
		return false
	}
	return equalJSON(backup.SourceInstanceRef, server.InstanceRef)
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
		if destroyedReservationBindsInstance(reservation, instanceAdapterID, instanceID) {
			if match != nil {
				return store.CapacityReservation{}, false, errors.New("multiple managed VM reservations match one exact server instance")
			}
			copyReservation := reservation
			match = &copyReservation
			continue
		}
		if destroyedCapacityState(reservation) {
			continue
		}
		if !reservationIdentifiesServerInstance(reservation, instanceAdapterID, instanceID) {
			return store.CapacityReservation{}, false, reservationMismatchError(reservation, instanceAdapterID, instanceID)
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

// destroyedCapacityState reports whether a reservation already reflects a
// destroyed or absent provider instance.
func destroyedCapacityState(reservation store.CapacityReservation) bool {
	return reservation.State == store.CapacityAbsent || reservation.State == store.CapacityDestroyed
}

// destroyedReservationBindsInstance reports whether a destroyed or absent
// reservation identifies the exact server instance, allowing it to carry no
// adapter identity.
func destroyedReservationBindsInstance(reservation store.CapacityReservation, instanceAdapterID, instanceID string) bool {
	if !destroyedCapacityState(reservation) {
		return false
	}
	return reservation.ProviderInstanceID == instanceID &&
		(reservation.ProviderAdapterID == "" || reservation.ProviderAdapterID == instanceAdapterID)
}

// reservationIdentifiesServerInstance reports whether an active reservation
// carries the adapter and instance identity of the server being destroyed.
// An active reservation may omit the adapter only when it identifies no
// provider at all.
func reservationIdentifiesServerInstance(reservation store.CapacityReservation, instanceAdapterID, instanceID string) bool {
	adapterKnown := reservation.ProviderAdapterID != ""
	adapterMatches := !adapterKnown || reservation.ProviderAdapterID == instanceAdapterID
	instanceMatches := reservation.ProviderInstanceID == instanceID
	return adapterMatches && instanceMatches
}

// reservationMismatchError explains why an active reservation is not the one
// for this server.
func reservationMismatchError(reservation store.CapacityReservation, instanceAdapterID, instanceID string) error {
	if reservation.ProviderAdapterID != "" && reservation.ProviderAdapterID != instanceAdapterID {
		return errors.New("managed VM reservation belongs to a different hosting adapter")
	}
	return errors.New("managed VM reservation does not identify the exact server instance")
}

func externalInstanceIdentity(raw json.RawMessage) (string, string, error) {
	var ref struct {
		AdapterID    string `json:"adapter_id"`
		ResourceKind string `json:"resource_kind"`
		ExternalID   string `json:"external_id"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil {
		return "", "", errors.New("managed VM capacity requires an exact provider instance reference")
	}
	if !isObject(raw) || !exactInstanceReference(ref) {
		return "", "", errors.New("managed VM capacity requires an exact provider instance reference")
	}
	return ref.AdapterID, ref.ExternalID, nil
}

// exactInstanceReference reports whether a parsed instance reference names an
// instance owned by a specific adapter.
func exactInstanceReference(ref struct {
	AdapterID    string `json:"adapter_id"`
	ResourceKind string `json:"resource_kind"`
	ExternalID   string `json:"external_id"`
}) bool {
	return ref.AdapterID != "" && ref.ResourceKind == "instance" && ref.ExternalID != ""
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
		return w.handleLifecycleStoppedOrFailed(next, operation, destroyStep)
	case protocolstate.ServerDestroying:
		return w.handleLifecycleDestroying(next, operation, destroyStep)
	}
	return current.Record, false, nil
}

// handleLifecycleStoppedOrFailed handles the stopped or failed state for server destroy.
func (w *ServerDestroyWorkflow) handleLifecycleStoppedOrFailed(next store.LogicalServer, operation store.OperationRecord, destroyStep store.OperationStep) (store.LogicalServer, bool, error) {
	if destroyStep.Attempt > 0 || destroyStep.Status != protocolstate.StepPending {
		next.LifecycleState = protocolstate.ServerDestroying
		next.DesiredState = "destroyed"
		return next, true, nil
	}
	return next, false, nil
}

// handleLifecycleDestroying handles the destroying state for server destroy.
func (w *ServerDestroyWorkflow) handleLifecycleDestroying(next store.LogicalServer, operation store.OperationRecord, destroyStep store.OperationStep) (store.LogicalServer, bool, error) {
	if operation.Status == protocolstate.OperationSucceeded {
		if !hasValidServerDestroyResult(operation, next, w.schemas) {
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
	return next, false, nil
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
	if json.Unmarshal(operation.Result, &result) != nil || json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters) != nil {
		return false
	}
	if !validServerDestroyEvidence(result, server, parameters) {
		return false
	}
	if !validServerDestroyTargetRef(result, operation) {
		return false
	}
	schemaURI := "urn:gameserver-controller:" + schemas.ProtocolVersion() + ":operations:server-destroy-result"
	return schemas.ValidateJSON(schemaURI, operation.Result) == nil
}

// validServerDestroyEvidence reports whether the recorded result carries the
// exact backup, instance, data generation, provider absence, and terminal
// state that prove destruction.
func validServerDestroyEvidence(result struct {
	OperationType string `json:"operation_type"`
	OperationData struct {
		InstanceRef         json.RawMessage `json:"instance_ref"`
		VerifiedBackupID    string          `json:"verified_backup_id"`
		DataGenerationID    string          `json:"data_generation_id"`
		ProviderAbsence     bool            `json:"provider_absence_confirmed"`
		FinalLifecycleState string          `json:"final_lifecycle_state"`
	} `json:"operation_data"`
}, server store.LogicalServer, parameters serverDestroyParameters) bool {
	if result.OperationType != "server.destroy" || !equalJSON(result.OperationData.InstanceRef, server.InstanceRef) ||
		result.OperationData.VerifiedBackupID != parameters.VerifiedBackupID {
		return false
	}
	if result.OperationData.DataGenerationID != server.DataGenerationID || !result.OperationData.ProviderAbsence ||
		result.OperationData.FinalLifecycleState != string(protocolstate.ServerDestroyed) {
		return false
	}
	return true
}

// validServerDestroyTargetRef reports whether the result instance matches the
// operation target when the target names an instance.
func validServerDestroyTargetRef(result struct {
	OperationType string `json:"operation_type"`
	OperationData struct {
		InstanceRef         json.RawMessage `json:"instance_ref"`
		VerifiedBackupID    string          `json:"verified_backup_id"`
		DataGenerationID    string          `json:"data_generation_id"`
		ProviderAbsence     bool            `json:"provider_absence_confirmed"`
		FinalLifecycleState string          `json:"final_lifecycle_state"`
	} `json:"operation_data"`
}, operation store.OperationRecord) bool {
	if len(operation.Target.InstanceRef) == 0 {
		return true
	}
	return equalJSON(result.OperationData.InstanceRef, operation.Target.InstanceRef)
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
