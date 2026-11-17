package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

const (
	verifyInspectInstanceStep = "inspect-instance"
	verifyInspectRuntimeStep  = "inspect-runtime"
	verifyProviderStep        = "destroy-instance"
	verifyVerifyInstanceStep  = "verify-instance-absence"
)

// serverDestroyProfile contains trusted execution references selected from
// controller-owned deployment metadata. Client parameters cannot choose the
// unit inspected before destruction.
type serverDestroyProfile struct {
	UnitRef json.RawMessage
}

// serverDestroyParameters carries exact-target data for server.destroy requests.
type serverDestroyParameters struct {
	ExpectedInstanceRef json.RawMessage `json:"expected_instance_ref"`
	BackupProfile       string          `json:"backup_profile"`
	VerifiedBackupID    string          `json:"verified_backup_id"`
	ApprovalID          string          `json:"approval_id"`
}

// serverDestroyCapacityStore is optional so the workflow can also run on
// stores that do not implement the pilot's managed-VM reservation policy.
type serverDestroyCapacityStore interface {
	ListCapacityReservations() ([]store.CapacityReservation, error)
	MarkCapacityDestroySent(string) (store.CapacityReservation, error)
}

// ServerDestroyWorkflow orchestrates destruction of a logical server.
type ServerDestroyWorkflow struct {
	servers    store.LifecycleOperationStore
	adapters   adapterRuntime
	schemas    schemaRuntime
	profiles   ServerDestroyProfileResolver
	backups    ServerDestroyBackupResolver
	authorizer ServerDestroyAuthorizer
}

// NewServerDestroyWorkflow constructs a new workflow with its required dependencies.
func NewServerDestroyWorkflow(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime,
	profiles ServerDestroyProfileResolver, backups ServerDestroyBackupResolver, authorizer ServerDestroyAuthorizer) (*ServerDestroyWorkflow, error) {
	if !verifyRuntimeDepsConfigured(servers, adapters, schemas, profiles, backups, authorizer) {
		return nil, errors.New("lifecycle store, adapter registry, schemas, destroy profile, verified backup resolver, and destroy authorizer are required")
	}
	return &ServerDestroyWorkflow{servers: servers, adapters: adapters, schemas: schemas, profiles: profiles, backups: backups, authorizer: authorizer}, nil
}

// verifyRuntimeDepsConfigured reports whether every dependency the destroy workflow needs is present.
func verifyRuntimeDepsConfigured(servers store.LifecycleOperationStore, adapters adapterRuntime, schemas schemaRuntime,
	profiles ServerDestroyProfileResolver, backups ServerDestroyBackupResolver, authorizer ServerDestroyAuthorizer) bool {
	return servers != nil && adapters != nil && schemas != nil && profiles != nil && backups != nil && authorizer != nil
}

// serverDestroyRequest decodes operation parameters and validates completeness.
func serverDestroyRequest(operation store.OperationRecord, protocolVersion string) (serverDestroyParameters, error) {
	if !verifyRequestOperationMatches(operation, protocolVersion) {
		return serverDestroyParameters{}, errors.New("server.destroy operation and loaded protocol version are required")
	}
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &serverDestroyParameters{}); err != nil {
		return serverDestroyParameters{}, fmt.Errorf("decode server.destroy parameters: %w", err)
	}
	if !verifyRequestParametersComplete(serverDestroyParameters{}) { // will be set in caller context
		return serverDestroyParameters{}, errors.New("server.destroy requires an exact instance, backup profile, verified backup ID, and approval ID")
	}
	var parameters serverDestroyParameters
	if err := json.Unmarshal(operation.RequestSnapshot.Parameters, &parameters); err != nil {
		return serverDestroyParameters{}, fmt.Errorf("decode server.destroy parameters: %w", err)
	}
	if !verifyRequestParametersComplete(parameters) {
		return serverDestroyParameters{}, errors.New("server.destroy requires an exact instance, backup profile, verified backup ID, and approval ID")
	}
	return parameters, nil
}

// verifyRequestOperationMatches reports whether the stored operation is a server.destroy with valid target.
func verifyRequestOperationMatches(operation store.OperationRecord, protocolVersion string) bool {
	if operation.OperationType != "server.destroy" || operation.ProtocolVersion != protocolVersion || protocolVersion == "" {
		return false
	}
	if operation.Target.ServerID == "" || operation.Target.EnvironmentID == "" {
		return false
	}
	if operation.RequestSnapshot.OperationType != "server.destroy" {
		return false
	}
	return isObject(operation.RequestSnapshot.Parameters)
}

// verifyRequestParametersComplete reports whether the decoded parameters carry all required fields.
func verifyRequestParametersComplete(parameters serverDestroyParameters) bool {
	if parameters.BackupProfile == "" || parameters.VerifiedBackupID == "" || parameters.ApprovalID == "" {
		return false
	}
	return len(parameters.ExpectedInstanceRef) > 0 && isObject(parameters.ExpectedInstanceRef)
}

// validateVerifyServerTarget verifies the operation target matches the logical server.
func validateVerifyServerTarget(operation store.OperationRecord, parameters serverDestroyParameters, server store.LogicalServer) error {
	if !verifyTargetMatchesServer(operation, parameters, server) {
		return store.ErrLogicalServerTargetMismatch
	}
	if server.DataGenerationStatus != "known" || server.DataGenerationID == "" {
		return errors.New("server.destroy requires a known current data generation")
	}
	return nil
}

// verifyTargetMatchesServer reports whether the operation target matches the logical server.
func verifyTargetMatchesServer(operation store.OperationRecord, parameters serverDestroyParameters, server store.LogicalServer) bool {
	if server.ServerID != operation.Target.ServerID || server.EnvironmentID != operation.Target.EnvironmentID || len(server.InstanceRef) == 0 {
		return false
	}
	return equalJSON(operation.Target.InstanceRef, server.InstanceRef) && equalJSON(parameters.ExpectedInstanceRef, server.InstanceRef)
}

// verifyServerTarget loads the current logical server and verifies it matches the operation target.
func (w *ServerDestroyWorkflow) verifyServerTarget(ctx context.Context, operation store.OperationRecord) (*verifyServerTargetResult, error) {
	if ctx == nil || w == nil || operation.OperationType != "server.destroy" || operation.ProtocolVersion != w.schemas.ProtocolVersion() {
		return nil, errors.New("server.destroy operation and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parameters, err := serverDestroyRequest(operation, w.schemas.ProtocolVersion())
	if err != nil {
		return nil, fmt.Errorf("parse server.destroy parameters: %w", err)
	}
	stored, err := w.servers.GetLogicalServer(operation.Target.EnvironmentID, operation.Target.ServerID)
	if err != nil {
		return nil, fmt.Errorf("get logical server: %w", err)
	}
	server := stored.Record
	if resultErr := validateVerifyServerTarget(operation, parameters, server); resultErr != nil {
		return nil, resultErr
	}
	if !verifyPlanBindsInstance(operation, server.InstanceRef) {
		return nil, errors.New("server.destroy plan is not bound to the current managed instance")
	}
	return &verifyServerTargetResult{
		Parameters: parameters,
		Server:     server,
	}, nil
}

// verifyServerTargetResult holds validated server destruction target information.
type verifyServerTargetResult struct {
	Parameters serverDestroyParameters
	Server     store.LogicalServer
}

// verifyPlanBindsInstance reports whether all destroy steps bind their input to the operation target.
func verifyPlanBindsInstance(operation store.OperationRecord, instanceRef json.RawMessage) bool {
	for _, stepID := range []string{verifyInspectInstanceStep, verifyProviderStep, verifyVerifyInstanceStep} {
		if !stepBindsRef(operationStep(operation, stepID).Input, "instance_ref", instanceRef) {
			return false
		}
	}
	return true
}

// stepField extracts a field from the step input.
func stepField(stepInput json.RawMessage, fieldName string) string {
	var field struct {
		FieldName string `json:"-"`
	}
	if err := json.Unmarshal(stepInput, &field); err == nil {
		return field.FieldName
	}
	return ""
}

// operationStep returns the stored OperationStep for a step ID.
func operationStep(operation store.OperationRecord, stepID string) store.OperationStep {
	for _, step := range operation.Steps {
		if step.StepID == stepID {
			return step
		}
	}
	return store.OperationStep{}
}

// verifyProviderInspectionPrecondition requires a fresh provider inspection before mutating steps.
func verifyProviderInspectionPrecondition(operation store.OperationRecord, step store.OperationStep, server store.LogicalServer) error {
	provider, err := verifyInstanceStepOutput(operation, verifyInspectInstanceStep, protocolstate.StepSucceeded)
	if err != nil || !equalJSON(provider.InstanceRef, server.InstanceRef) || !activeOrStopped(provider.State) {
		return errors.New("server.destroy requires a successful exact-provider inspection in active or stopped state")
	}
	return nil
}

// verifyInstanceStepOutput extracts and validates the inspect-instance step output.
func verifyInstanceStepOutput(operation store.OperationRecord, stepID string, expectedState protocolstate.StepState) (*stepOutput, error) {
	for _, step := range operation.Steps {
		if step.StepID == stepID && step.Status == expectedState {
			var out struct {
				InstanceRef json.RawMessage `json:"instance_ref"`
				State       string          `json:"state"`
			}
			if err := json.Unmarshal(step.Output, &out); err != nil {
				return nil, fmt.Errorf("parse inspect-instance output: %w", err)
			}
			return &stepOutput{InstanceRef: out.InstanceRef, State: out.State}, nil
		}
	}
	return nil, errors.New("inspect-instance step did not succeed")
}

// stepOutput represents a successful step result.
type stepOutput struct {
	InstanceRef json.RawMessage
	State       string
}

// activeOrStopped reports whether the step output indicates an executable or halted state.
func activeOrStopped(state string) bool {
	return state == "active" || state == "stopped"
}

// checkVerifyInspectInstance validates the initial inspect-instance step precondition.
func (w *ServerDestroyWorkflow) checkVerifyInspectInstance(_ context.Context, check verifyServerState) error {
	if err := w.validateInspectInstance(check.server.LifecycleState, check.step.Status, check.server.InstanceRef); err != nil {
		return err
	}
	return nil
}

// validateInspectInstance checks the inspect-instance precondition.
func (w *ServerDestroyWorkflow) validateInspectInstance(lifecycleState string, stepStatus protocolstate.StepState, serverInstanceRef json.RawMessage) error {
	if !isStoppedOrFailedLifecycle(lifecycleState) || stepStatus != protocolstate.StepPending ||
		!stepBindsRef(serverOperationInput(server.InstanceRef), "instance_ref", serverInstanceRef) {
		return errors.New("server.destroy requires a stopped or failed server and exact initial provider inspection")
	}
	return nil
}

// checkVerifyInspectRuntime validates the inspect-runtime step precondition.
func (w *ServerDestroyWorkflow) checkVerifyInspectRuntime(_ context.Context, check verifyServerState) error {
	if err := w.validateInspectRuntime(check.server.LifecycleState, check.server.InstanceRef); err != nil {
		return err
	}
	return nil
}

// validateInspectRuntime checks the inspect-runtime precondition.
func (w *ServerDestroyWorkflow) validateInspectRuntime(lifecycleState string, serverInstanceRef json.RawMessage) error {
	if !isStoppedOrFailedLifecycle(lifecycleState) || !stepSucceeded(checkOperationStep(server.InstanceRef), verifyInspectInstanceStep) {
		return errors.New("runtime inspection requires a stopped or failed server and successful provider inspection")
	}
	return nil
}

// checkVerifyProvider validates the provider destruction step precondition.
func (w *ServerDestroyWorkflow) checkVerifyProvider(ctx context.Context, check verifyServerState) error {
	if err := w.validateServerLifecycle(check.server.LifecycleState); err != nil {
		return err
	}
	if !stepSucceeded(check.operation, verifyInspectRuntimeStep) {
		return errors.New("provider destruction requires successful exact-instance and runtime inspections")
	}
	if !stepBindsRef(check.step.Input, "instance_ref", check.server.InstanceRef) {
		return errors.New("provider destruction requires binding to the correct instance")
	}
	if !verifyRuntimeStoppedOrAbsent(check.operation) {
		return errors.New("provider destruction requires the exact execution unit to be stopped or absent")
	}
	if check.server.LifecycleState == protocolstate.ServerDestroying && check.step.Attempt == 0 {
		return errors.New("new destroy dispatch cannot start from an unexplained destroying state")
	}
	if authErr := w.authorizer.AuthorizeServerDestroy(ctx, check.operation.ActorID, check.parameters.ApprovalID, check.operation, check.server, check.backup); authErr != nil {
		return fmt.Errorf("revalidate exact server.destroy approval before provider call: %w", authErr)
	}
	return w.markCapacityDestroySent(check.server)
}

// validateServerLifecycle checks the server lifecycle state precondition.
func (w *ServerDestroyWorkflow) validateServerLifecycle(lifecycleState string) error {
	if !isStoppedOrFailedLifecycle(lifecycleState) && lifecycleState != protocolstate.ServerDestroying {
		return errors.New("provider destruction requires a stopped, failed, or already-destroying server")
	}
	return nil
}

// checkVerifyVerifyInstance validates the verify-instance-absence step precondition.
func (w *ServerDestroyWorkflow) checkVerifyVerifyInstance(_ context.Context, check verifyServerState) error {
	if err := w.validateVerifyInstancePreconditions(check.server.LifecycleState, check.server.InstanceRef); err != nil {
		return err
	}
	result, err := verifyProviderOutput(check.operation)
	if err != nil || result.Outcome != "completed" || result.State != "destroyed" || !equalJSON(result.InstanceRef, check.server.InstanceRef) {
		return errors.New("provider absence verification requires the exact completed destroy output")
	}
	return nil
}

// validateVerifyInstancePreconditions checks preconditions for verify-instance-absence.
func (w *ServerDestroyWorkflow) validateVerifyInstancePreconditions(lifecycleState string, serverInstanceRef json.RawMessage) error {
	if lifecycleState != protocolstate.ServerDestroying || !stepSucceeded(checkOperationStep(server.InstanceRef), verifyProviderStep) {
		return errors.New("provider absence verification requires a dispatched destroy and destroying lifecycle state")
	}
	return nil
}

// isStoppedOrFailedLifecycle reports whether the server has reached terminal states.
func isStoppedOrFailedLifecycle(lifecycleState string) bool {
	return lifecycleState == protocolstate.ServerStopped || lifecycleState == protocolstate.ServerFailed
}

// stepSucceeded reports whether a given step completed successfully.
func stepSucceeded(operation store.OperationRecord, stepID string) bool {
	for _, step := range operation.Steps {
		if step.StepID == stepID && step.Status == protocolstate.StepSucceeded {
			return true
		}
	}
	return false
}

// serverOperationInput constructs a function to check the inspect-instance input.
func serverOperationInput(serverInstanceRef json.RawMessage) struct {
	InstanceRef json.RawMessage `json:"instance_ref"`
} {
	return struct {
		InstanceRef json.RawMessage `json:"instance_ref"`
	}{InstanceRef: serverInstanceRef}
}

// stepBindsRef reports whether a step input binds to the given instance ref.
func stepBindsRef(stepInput json.RawMessage, fieldName string, instanceRef json.RawMessage) bool {
	var field struct {
		Name string `json:"-"`
	}
	if err := json.Unmarshal(stepInput, &field); err != nil || field.Name != "instance_ref" {
		return false
	}
	return equalJSON(field.Name, instanceRef)
}

// serverOperationStep returns the operation step for a given step ID.
func checkOperationStep(serverInstanceRef json.RawMessage) store.OperationStep {
	return store.OperationStep{} // simplified for extraction
}

// verifyRuntimeStoppedOrAbsent reports whether the execution unit is stopped or absent.
func verifyRuntimeStoppedOrAbsent(operation store.OperationRecord) bool {
	runtime, err := verifyRuntimeStepOutput(operation)
	return err == nil && equalJSON(runtime.UnitRef, stepField(operationStep(operation, verifyInspectRuntimeStep).Input, "unit_ref")) &&
		(runtime.State == "stopped" || runtime.State == "absent")
}

// verifyRuntimeStepOutput extracts the inspect-runtime step output.
func verifyRuntimeStepOutput(operation store.OperationRecord) (*runtimeOutput, error) {
	for _, step := range operation.Steps {
		if step.StepID == verifyInspectRuntimeStep {
			var out runtimeOutput
			if err := json.Unmarshal(step.Output, &out); err != nil {
				return nil, fmt.Errorf("parse inspect-runtime output: %w", err)
			}
			return &out, nil
		}
	}
	return nil, errors.New("inspect-runtime step not found")
}

// runtimeOutput represents the inspect-runtime step result.
type runtimeOutput struct {
	UnitRef  json.RawMessage `json:"unit_ref"`
	State    string          `json:"state"`
}

// verifyProviderOutput extracts and validates the provider destroy output.
func verifyProviderOutput(operation store.OperationRecord) (*providerDestroyOutput, error) {
	for _, step := range operation.Steps {
		if step.StepID == verifyProviderStep {
			var out providerDestroyOutput
			if err := json.Unmarshal(step.Output, &out); err != nil {
				return nil, fmt.Errorf("parse destroy output: %w", err)
			}
			return &out, nil
		}
	}
	return nil, errors.New("destroy step not found")
}

// providerDestroyOutput represents the provider destruction result.
type providerDestroyOutput struct {
	Outcome    string          `json:"outcome"`
	State      string          `json:"state"`
	InstanceRef json.RawMessage `json:"instance_ref"`
}

// markCapacityDestroySent claims managed VM capacity for destruction.
func (w *ServerDestroyWorkflow) markCapacityDestroySent(server store.LogicalServer) error {
	capacityStore, ok := w.servers.(serverDestroyCapacityStore)
	if !ok {
		return nil
	}
	reservation, found, err := matchingVerifyCapacityReservation(capacityStore, server)
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

// matchingVerifyCapacityReservation finds the active managed VM reservation for this server.
func matchingVerifyCapacityReservation(capacityStore serverDestroyCapacityStore, server store.LogicalServer) (store.CapacityReservation, bool, error) {
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
		if verifyDestroyedReservationBindsInstance(reservation, instanceAdapterID, instanceID) {
			if match != nil {
				return store.CapacityReservation{}, false, errors.New("multiple managed VM reservations match one exact server instance")
			}
			copyReservation := reservation
			match = &copyReservation
			continue
		}
		if verifyDestroyedCapacityState(reservation) {
			continue
		}
		if !reservationIdentifiesServerInstance(reservation, instanceAdapterID, instanceID) {
			return store.CapacityReservation{}, false, verifyReservationMismatchError(reservation, instanceAdapterID, instanceID)
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

// verifyDestroyedCapacityState reports whether a reservation reflects an absent/destroyed instance.
func verifyDestroyedCapacityState(reservation store.CapacityReservation) bool {
	return reservation.State == store.CapacityAbsent || reservation.State == store.CapacityDestroyed
}

// verifyDestroyedReservationBindsInstance reports whether a destroyed/absent reservation identifies the exact server instance.
func verifyDestroyedReservationBindsInstance(reservation store.CapacityReservation, instanceAdapterID, instanceID string) bool {
	if !verifyDestroyedCapacityState(reservation) {
		return false
	}
	return reservation.ProviderInstanceID == instanceID &&
		(reservation.ProviderAdapterID == "" || reservation.ProviderAdapterID == instanceAdapterID)
}

// reservationIdentifiesServerInstance reports whether an active reservation identifies this server.
func reservationIdentifiesServerInstance(reservation store.CapacityReservation, instanceAdapterID, instanceID string) bool {
	adapterKnown := reservation.ProviderAdapterID != ""
	adapterMatches := !adapterKnown || reservation.ProviderAdapterID == instanceAdapterID
	instanceMatches := reservation.ProviderInstanceID == instanceID
	return adapterMatches && instanceMatches
}

// verifyReservationMismatchError explains why an active reservation does not match this server.
func verifyReservationMismatchError(reservation store.CapacityReservation, instanceAdapterID, instanceID string) error {
	if reservation.ProviderAdapterID != "" && reservation.ProviderAdapterID != instanceAdapterID {
		return errors.New("managed VM reservation belongs to a different hosting adapter")
	}
	return errors.New("managed VM reservation does not identify the exact server instance")
}

// externalInstanceIdentity extracts the identity from an instance reference.
func externalInstanceIdentity(instanceRef json.RawMessage) (adapterID, instanceID string, err error) {
	var ref struct {
		AdapterID   string `json:"adapter_id"`
		InstanceID  string `json:"instance_id"`
		EnvironmentID string `json:"environment_id"`
	}
	if err := json.Unmarshal(instanceRef, &ref); err != nil {
		return "", "", fmt.Errorf("parse instance reference: %w", err)
	}
	return ref.AdapterID, ref.InstanceID, nil
}

// isObject reports whether the raw message contains a JSON object.
func isObject(rawMessage json.RawMessage) bool {
	if len(rawMessage) == 0 {
		return false
	}
	s := string(rawMessage)
	for i, r := range s {
		if r != '{' && r != '[' {
			return true
		}
		if i > 0 {
			return true
		}
	}
	return len(rawMessage) > 0 // assume valid JSON object if it starts with brace/bracket
}

// equalJSON reports whether two raw messages represent the same JSON value.
func equalJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var va, vb interface{}
	if err := json.Unmarshal(a, &va); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return false
	}
	return va == vb
}

// verifyStepPreconditions maps each planned step to its precondition rule.
var verifyStepPreconditions = map[string]func(*ServerDestroyWorkflow, context.Context, verifyServerState) error{
	verifyInspectInstanceStep: (*ServerDestroyWorkflow).checkVerifyInspectInstance,
	verifyInspectRuntimeStep:  (*ServerDestroyWorkflow).checkVerifyInspectRuntime,
	verifyProviderStep:        (*ServerDestroyWorkflow).checkVerifyProvider,
	verifyVerifyInstanceStep:  (*ServerDestroyWorkflow).checkVerifyVerifyInstance,
}

// verifyServerState carries evidence for precondition checks.
type verifyServerState struct {
	operation  store.OperationRecord
	step       store.OperationStep
	parameters serverDestroyParameters
	server     store.LogicalServer
	backup     VerifiedServerBackup
}
