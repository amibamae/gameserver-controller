package store

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// Err* variables define error conditions for the store.
var (
	ErrNotFound          = errors.New("operation not found")
	ErrAlreadyExists     = errors.New("operation already exists")
	ErrRevisionConflict  = errors.New("operation revision conflict")
	ErrImmutableFields   = errors.New("accepted operation fields are immutable")
	ErrTerminalOperation = errors.New("terminal operation cannot be changed")
	ErrUnresolvedEffect  = errors.New("operation has an unresolved external effect")
	ErrInvalidRecord     = errors.New("invalid operation record")
	ErrCorruptStore      = errors.New("corrupt operation store")
)

// OperationRecord is the persisted protocol operation resource. The request
// snapshot must already be schema-validated and contain secret references,
// never resolved secret values.
type OperationRecord struct {
	BackupInitialLifecycleState protocolstate.ServerState     `json:"backup_initial_lifecycle_state,omitempty"`
	OperationID                 string                        `json:"operation_id"`
	OperationType               string                        `json:"operation_type"`
	ProtocolVersion             string                        `json:"protocol_version"`
	SchemaPackageRevision       string                        `json:"schema_package_revision,omitempty"`
	Target                      OperationTarget               `json:"target"`
	Status                      protocolstate.OperationStatus `json:"status"`
	CurrentStepID               *string                       `json:"current_step_id,omitempty"`
	ActorID                     string                        `json:"actor_id"`
	CorrelationID               string                        `json:"correlation_id,omitempty"`
	IdempotencyKey              string                        `json:"idempotency_key,omitempty"`
	RequestFingerprint          string                        `json:"request_fingerprint,omitempty"`
	RequestSnapshot             RequestSnapshot               `json:"request_snapshot"`
	CreatedAt                   time.Time                     `json:"created_at"`
	UpdatedAt                   time.Time                     `json:"updated_at"`
	StartedAt                   *time.Time                    `json:"started_at,omitempty"`
	CompletedAt                 *time.Time                    `json:"completed_at,omitempty"`
	Deadline                    *time.Time                    `json:"deadline,omitempty"`
	Steps                       []OperationStep               `json:"steps"`
	Progress                    json.RawMessage               `json:"progress,omitempty"`
	Result                      json.RawMessage               `json:"result,omitempty"`
	Error                       json.RawMessage               `json:"error,omitempty"`
}

// OperationTarget identifies the server and environment for an operation.
type OperationTarget struct {
	ServerID      string          `json:"server_id"`
	EnvironmentID string          `json:"environment_id"`
	InstanceRef   json.RawMessage `json:"instance_ref,omitempty"`
}

// RequestSnapshot captures the request parameters with secret references.
type RequestSnapshot struct {
	OperationType string            `json:"operation_type"`
	Parameters    json.RawMessage   `json:"parameters"`
	SecretRefs    []SecretReference `json:"secret_refs,omitempty"`
}

// SecretReference points to a secret used in the request.
type SecretReference struct {
	SecretRef       string   `json:"secret_ref"`
	Purpose         string   `json:"purpose"`
	PermissionScope []string `json:"permission_scope,omitempty"`
	VersionRef      string   `json:"version_ref,omitempty"`
}

// OperationStep represents a single step in an operation's execution plan.
type OperationStep struct {
	StepID               string                   `json:"step_id"`
	StepType             string                   `json:"step_type"`
	Status               protocolstate.StepStatus `json:"status"`
	Attempt              int                      `json:"attempt,omitempty"`
	AdapterID            string                   `json:"adapter_id,omitempty"`
	CapabilityName       string                   `json:"capability_name,omitempty"`
	CapabilityVersion    string                   `json:"capability_version,omitempty"`
	Input                json.RawMessage          `json:"input,omitempty"`
	InputBindings        []inputbinding.Binding   `json:"input_bindings,omitempty"`
	ResolvedInput        json.RawMessage          `json:"resolved_input,omitempty"`
	Output               json.RawMessage          `json:"output,omitempty"`
	StartedAt            *time.Time               `json:"started_at,omitempty"`
	CompletedAt          *time.Time               `json:"completed_at,omitempty"`
	ExternalOperationRef json.RawMessage          `json:"external_operation_ref,omitempty"`
	Error                json.RawMessage          `json:"error,omitempty"`
}

// StoredOperation represents a versioned operation record in the store.
type StoredOperation struct {
	Record   OperationRecord
	Revision uint64
}

// prepareNewRecord initializes a new operation record with defaults and
// rejects invalid initial states. Timestamps are set to now, steps are
// initialized if nil, and validation is performed.
func prepareNewRecord(record OperationRecord, now time.Time) (OperationRecord, error) {
	// Reject invalid backup initial lifecycle state for new records
	if record.BackupInitialLifecycleState != "" {
		return OperationRecord{}, fmt.Errorf("%w: backup initial state is core-owned", ErrInvalidRecord)
	}

	// Set default status to pending if not specified
	if record.Status == "" {
		record.Status = protocolstate.OperationPending
	}

	// Reject non-pending status for new operations
	if record.Status != protocolstate.OperationPending {
		return OperationRecord{}, fmt.Errorf("%w: new operations must start pending", ErrInvalidRecord)
	}

	// Initialize steps if nil
	if record.Steps == nil {
		record.Steps = []OperationStep{}
	}

	// Set default status for each step
	for i := range record.Steps {
		if record.Steps[i].Status == "" {
			record.Steps[i].Status = protocolstate.StepPending
		}

		// Reject non-pending step status for new operations
		if record.Steps[i].Status != protocolstate.StepPending {
			return OperationRecord{}, fmt.Errorf("%w: new operation steps must start pending", ErrInvalidRecord)
		}

		// Reject pre-resolved inputs in new records
		if len(record.Steps[i].ResolvedInput) > 0 {
			return OperationRecord{}, fmt.Errorf("%w: resolved step inputs are written by the engine before dispatch", ErrInvalidRecord)
		}
	}

	// Reject execution outcomes in new operations
	if record.StartedAt != nil || record.CompletedAt != nil || len(record.Result) != 0 || len(record.Error) != 0 {
		return OperationRecord{}, fmt.Errorf("%w: new operation cannot contain an execution outcome", ErrInvalidRecord)
	}

	// Set timestamps
	record.CreatedAt = now.UTC()
	record.UpdatedAt = record.CreatedAt

	// Validate the fully initialized record
	if err := validateRecord(record); err != nil {
		return OperationRecord{}, err
	}

	return record, nil
}

// validateRecord runs all validators in a fixed order and returns the first
// error encountered. This ensures consistent validation error ordering for
// both runtime checks and tests.
func validateRecord(record OperationRecord) error {
	validators := []struct {
		name string
		fn   func(OperationRecord) error
	}{
		{"backup_initial_lifecycle_state", validateBackupInitialState},
		{"identity", validateRecordIdentity},
		{"status", validateRecordStatus},
		{"request_snapshot", validateRecordDocuments},
		{"execution_times", validateRecordExecutionTimes},
		{"steps", validateRecordSteps},
	}

	for _, v := range validators {
		if err := v.fn(record); err != nil {
			return err
		}
	}

	return nil
}

// validateBackupInitialState requires backup operations to record their
// initial lifecycle state before the operation runs. The field is only valid
// on server.backup operations with a ready, degraded, or stopped state.
func validateBackupInitialState(record OperationRecord) error {
	// Require backup_initial_lifecycle_state for succeeded backup operations
	if record.OperationType == "server.backup" && record.Status == protocolstate.OperationSucceeded && record.BackupInitialLifecycleState == "" {
		return invalidField("backup_initial_lifecycle_state")
	}

	// Validate that the state value is valid if present
	state := record.BackupInitialLifecycleState
	if state != "" {
		switch state {
		case protocolstate.ServerReady, protocolstate.ServerDegraded, protocolstate.ServerStopped:
			// Valid state
		default:
			return invalidField("backup_initial_lifecycle_state")
		}
	}

	return nil
}

// validateRecordIdentity checks all identity fields are non-empty and within
// length limits. This includes operation IDs, types, versions, target info,
// actor ID, correlation ID, idempotency key, and request fingerprint.
func validateRecordIdentity(record OperationRecord) error {
	// OperationID must be a valid identifier
	if !validIdentifier(record.OperationID, 200) {
		return invalidField("operation_id")
	}

	// OperationType must be a qualified name
	if !validQualifiedName(record.OperationType, 160) {
		return invalidField("operation_type")
	}

	// ProtocolVersion must be present and within limits
	if record.ProtocolVersion == "" || len(record.ProtocolVersion) > 100 {
		return invalidField("protocol_version")
	}

	// SchemaPackageRevision has length limit
	if len(record.SchemaPackageRevision) > 200 {
		return invalidField("schema_package_revision")
	}

	// Target fields must be valid identifiers
	if !validIdentifier(record.Target.ServerID, 200) || !validIdentifier(record.Target.EnvironmentID, 200) {
		return invalidField("target")
	}

	// ActorID must be present and within length limits
	if record.ActorID == "" || len(record.ActorID) > 200 {
		return invalidField("actor_id")
	}

	// CorrelationID can be empty, but if present must be within limits
	if record.CorrelationID != "" && len(record.CorrelationID) > 200 {
		return invalidField("correlation_id")
	}

	// IdempotencyKey must match the expected format if present
	if record.IdempotencyKey != "" && !validIdempotencyKey(record.IdempotencyKey) {
		return invalidField("idempotency_key")
	}

	// RequestFingerprint must match the sha256:hex format if present
	if record.RequestFingerprint != "" && !validFingerprint(record.RequestFingerprint) {
		return invalidField("request_fingerprint")
	}

	return nil
}

// validateRecordStatus checks timestamp ordering and status enum validity.
func validateRecordStatus(record OperationRecord) error {
	// Timestamps must be in valid order
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return invalidField("timestamps")
	}

	// Status must be a known enum value
	if !knownOperationStatus(record.Status) {
		return invalidField("status")
	}

	return nil
}

// validateRecordDocuments checks the request snapshot and that every stored
// JSON document on the record is an object, not an array or primitive.
func validateRecordDocuments(record OperationRecord) error {
	// RequestSnapshot.OperationType must match record.OperationType
	if record.RequestSnapshot.OperationType != record.OperationType {
		return invalidField("request_snapshot")
	}

	// RequestSnapshot.Parameters must be a JSON object
	if !isJSONObject(record.RequestSnapshot.Parameters) {
		return invalidField("request_snapshot")
	}

	// Validate each secret reference
	for _, ref := range record.RequestSnapshot.SecretRefs {
		if !validSecretReference(ref) {
			return invalidField("request_snapshot.secret_refs")
		}
	}

	// Validate that optional JSON fields are objects when present
	for _, document := range []struct {
		field string
		value json.RawMessage
	}{
		{"target.instance_ref", record.Target.InstanceRef},
		{"progress", record.Progress},
		{"result", record.Result},
		{"error", record.Error},
	} {
		if len(document.value) > 0 && !isJSONObject(document.value) {
			return invalidField(document.field)
		}
	}

	return nil
}

// validateRecordExecutionTimes checks that execution timestamps fall within
// the record's lifetime and maintain logical ordering.
func validateRecordExecutionTimes(record OperationRecord) error {
	// StartedAt must be after created_at and before updated_at
	if record.StartedAt != nil && (record.StartedAt.Before(record.CreatedAt) || record.StartedAt.After(record.UpdatedAt)) {
		return invalidField("started_at")
	}

	// CompletedAt must be after created_at, after started_at if present,
	// and equal to updated_at for completed operations
	if record.CompletedAt != nil && (record.CompletedAt.Before(record.CreatedAt) || (record.StartedAt != nil && record.CompletedAt.Before(*record.StartedAt)) || record.CompletedAt.After(record.UpdatedAt)) {
		return invalidField("completed_at")
	}

	return nil
}

// validateRecordSteps validates every step in order and rejects repeated step
// IDs. A current step, if set, must name one of the steps.
func validateRecordSteps(record OperationRecord) error {
	seenSteps := make(map[string]struct{}, len(record.Steps))

	for i, step := range record.Steps {
		// Validate step identity first (before checking for duplicates)
		if err := validateStepIdentity(record, i, step); err != nil {
			return err
		}

		// Check for duplicate step IDs
		if _, exists := seenSteps[step.StepID]; exists {
			return invalidField(fmt.Sprintf("duplicate step_id in steps[%d]", i))
		}
		seenSteps[step.StepID] = struct{}{}

		// Validate step input, outcome, and times
		for _, validate := range []func(OperationRecord, int, OperationStep) error{
			validateStepInput,
			validateStepOutcome,
			validateStepTimes,
		} {
			if err := validate(record, i, step); err != nil {
				return err
			}
		}
	}

	return nil
}

// validateStepIdentity checks a step's ID, type, status, and attempt count. A
// server.backup must record its initial lifecycle state before any step runs.
func validateStepIdentity(record OperationRecord, i int, step OperationStep) error {
	// For backup operations, require the backup_initial_lifecycle_state field
	if record.OperationType == "server.backup" && record.BackupInitialLifecycleState == "" &&
		(step.Attempt != 0 || step.Status == protocolstate.StepRunning ||
			step.Status == protocolstate.StepSucceeded ||
			step.Status == protocolstate.StepWaiting ||
			step.Status == protocolstate.StepOutcomeUnknown) {
		return invalidField("backup_initial_lifecycle_state must precede dispatch")
	}

	// Validate step ID, type, status, and attempt
	if !validIdentifier(step.StepID, 200) || !validCapabilityName(step.StepType, 160) ||
		!knownStepStatus(step.Status) || step.Attempt < 0 {
		return invalidField(fmt.Sprintf("steps[%d]", i))
	}

	// Validate attempt vs status combination
	if (step.Status == protocolstate.StepPending && step.Attempt != 0) ||
		(step.Status != protocolstate.StepPending && step.Status != protocolstate.StepSkipped &&
			step.Status != protocolstate.StepCancelled && step.Attempt == 0) {
		return invalidField(fmt.Sprintf("steps[%d].attempt", i))
	}

	return nil
}

// validateStepInput checks the step's capability identity, its input object,
// and its input bindings. Resolved input exists only for bound steps and must
// be persisted before a bound step is dispatched.
func validateStepInput(_ OperationRecord, i int, step OperationStep) error {
	// Validate capability identity if present
	if step.CapabilityName != "" && (!validCapabilityName(step.CapabilityName, 160) || step.AdapterID == "" || step.CapabilityVersion == "") {
		return invalidField(fmt.Sprintf("steps[%d].capability", i))
	}

	// CapabilityVersion requires capability identity and adapter ID
	if step.CapabilityVersion != "" && (step.AdapterID == "" || step.CapabilityName == "") {
		return invalidField(fmt.Sprintf("steps[%d].capability", i))
	}

	// Input must be a JSON object when present
	if (step.CapabilityName != "" || len(step.Input) > 0) && !isJSONObject(step.Input) {
		return invalidField(fmt.Sprintf("steps[%d].input", i))
	}

	// InputBindings requires input to be a JSON object
	if len(step.InputBindings) > 0 && (step.CapabilityName == "" || !isJSONObject(step.Input)) {
		return invalidField(fmt.Sprintf("steps[%d].input_bindings", i))
	}

	// ResolvedInput must be a JSON object when present
	if len(step.ResolvedInput) > 0 && !isJSONObject(step.ResolvedInput) {
		return invalidField(fmt.Sprintf("steps[%d].resolved_input", i))
	}

	// ResolvedInput without bindings is invalid
	if len(step.InputBindings) == 0 && len(step.ResolvedInput) > 0 {
		return invalidField(fmt.Sprintf("steps[%d].resolved_input has no bindings", i))
	}

	// With bindings, resolved input must be persisted before dispatch
	if len(step.InputBindings) > 0 && step.Attempt > 0 && len(step.ResolvedInput) == 0 {
		return invalidField(fmt.Sprintf("steps[%d].resolved_input must be persisted before dispatch", i))
	}

	return nil
}

// validateStepOutcome checks the step's output, external operation reference,
// and error documents. Only a succeeded step has output, and a succeeded
// adapter step must have it.
func validateStepOutcome(_ OperationRecord, i int, step OperationStep) error {
	// Output is only valid for succeeded steps
	if len(step.Output) > 0 && (!isJSONObject(step.Output) || step.Status != protocolstate.StepSucceeded) {
		return invalidField(fmt.Sprintf("steps[%d].output", i))
	}

	// Succeeded adapter steps must have output
	if step.Status == protocolstate.StepSucceeded && step.CapabilityName != "" && len(step.Output) == 0 {
		return invalidField(fmt.Sprintf("steps[%d].output", i))
	}

	// ExternalOperationRef must be a JSON object when present
	if len(step.ExternalOperationRef) > 0 && !isJSONObject(step.ExternalOperationRef) {
		return invalidField(fmt.Sprintf("steps[%d].external_operation_ref", i))
	}

	// Error must be a JSON object when present
	if len(step.Error) > 0 && !isJSONObject(step.Error) {
		return invalidField(fmt.Sprintf("steps[%d].error", i))
	}

	return nil
}

// validateStepTimes requires step timestamps to fall within the record's
// lifetime, with completion no earlier than the step's start.
func validateStepTimes(record OperationRecord, i int, step OperationStep) error {
	// StartedAt must be within record lifetime
	if step.StartedAt != nil && (step.StartedAt.Before(record.CreatedAt) || step.StartedAt.After(record.UpdatedAt)) {
		return invalidField(fmt.Sprintf("steps[%d].started_at", i))
	}

	// CompletedAt must be within record lifetime and after started_at if present
	if step.CompletedAt != nil && (step.CompletedAt.Before(record.CreatedAt) || (step.StartedAt != nil && step.CompletedAt.Before(*step.StartedAt)) || step.CompletedAt.After(record.UpdatedAt)) {
		return invalidField(fmt.Sprintf("steps[%d].completed_at", i))
	}

	return nil
}

// validateRecordStepPlan checks the input bindings across steps and that the
// current step, if set, is one of the record's steps.
func validateRecordStepPlan(record OperationRecord) error {
	// Build binding steps for validation
	bindingSteps := make([]inputbinding.Step, len(record.Steps))
	for i, step := range record.Steps {
		bindingSteps[i] = inputbinding.Step{
			StepID:   step.StepID,
			Status:   string(step.Status),
			Input:    step.Input,
			Output:   step.Output,
			Binding:  step.InputBindings,
		}
	}

	if err := inputbinding.ValidatePlan(bindingSteps); err != nil {
		return invalidField("step input_bindings")
	}

	// Validate current_step_id if present
	if record.CurrentStepID != nil {
		if *record.CurrentStepID == "" {
			return invalidField("current_step_id")
		}
		if !containsStepID(record.Steps, *record.CurrentStepID) {
			return invalidField("current_step_id")
		}
	}

	return nil
}

// containsStepID returns true if the given step ID exists in the steps slice.
func containsStepID(steps []OperationStep, stepID string) bool {
	for _, step := range steps {
		if step.StepID == stepID {
			return true
		}
	}
	return false
}

// validSecretReference returns true if the secret reference is well-formed.
func validSecretReference(ref SecretReference) bool {
	// SecretRef must be present and within length limits
	if ref.SecretRef == "" || len(ref.SecretRef) > 1024 || ref.Purpose == "" || len(ref.Purpose) > 100 {
		return false
	}

	// SecretRef must use http:// or https:// scheme
	separator := strings.Index(ref.SecretRef, "://")
	if separator < 1 || ref.SecretRef[0] < 'a' || ref.SecretRef[0] > 'z' {
		return false
	}

	// Validate characters in the host portion
	for _, ch := range ref.SecretRef[1:separator] {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '+' && ch != '.' && ch != '-' {
			return false
		}
	}

	// VersionRef has length limit
	if len(ref.VersionRef) > 500 {
		return false
	}

	// PermissionScope must have unique, non-empty entries
	seen := make(map[string]struct{}, len(ref.PermissionScope))
	for _, scope := range ref.PermissionScope {
		if scope == "" || len(scope) > 160 {
			return false
		}
		if _, exists := seen[scope]; exists {
			return false
		}
		seen[scope] = struct{}{}
	}

	return true
}

// validIdentifier returns true if the value is non-empty and within max length.
func validIdentifier(value string, max int) bool {
	return value != "" && len(value) <= max
}

// validQualifiedName returns true if the value starts with a lowercase letter,
// contains only lowercase letters, digits, or dots, and is within length limits.
func validQualifiedName(value string, max int) bool {
	if len(value) < 2 || len(value) > max || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, ch := range value[1:] {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '.' {
			return false
		}
	}
	return true
}

// validCapabilityName returns true if the name starts with a lowercase letter,
// contains only lowercase letters, digits, dots, underscores, or hyphens.
func validCapabilityName(value string, max int) bool {
	if len(value) < 2 || len(value) > max || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, ch := range value[1:] {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '.' && ch != '_' && ch != '-' {
			return false
		}
	}
	return true
}

// validIdempotencyKey returns true if the key is within length limits and
// contains only printable ASCII characters except space, tab, newline, carriage return.
func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

// validFingerprint returns true if the fingerprint is in sha256:hex format
// with a valid SHA-256 hash (32 bytes).
func validFingerprint(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && len(decoded) == 32
}

// isJSONObject returns true if the value is a valid JSON object (not array or primitive).
func isJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return false
	}
	return object != nil
}

// knownOperationStatus returns true if the status is a valid protocolstate.OperationStatus.
func knownOperationStatus(status protocolstate.OperationStatus) bool {
	switch status {
	case protocolstate.OperationPending, protocolstate.OperationRunning, protocolstate.OperationWaiting,
		protocolstate.OperationCancellationRequested, protocolstate.OperationReconciling,
		protocolstate.OperationNeedsOperator, protocolstate.OperationSucceeded,
		protocolstate.OperationFailed, protocolstate.OperationCancelled:
		return true
	default:
		return false
	}
}

// knownStepStatus returns true if the status is a valid protocolstate.StepStatus.
func knownStepStatus(status protocolstate.StepStatus) bool {
	switch status {
	case protocolstate.StepPending, protocolstate.StepRunning, protocolstate.StepWaiting,
		protocolstate.StepOutcomeUnknown, protocolstate.StepSucceeded, protocolstate.StepFailed,
		protocolstate.StepSkipped, protocolstate.StepCancelled:
		return true
	default:
		return false
	}
}

// invalidField returns an error with the specified field name for validation failures.
func invalidField(field string) error {
	return fmt.Errorf("%w: invalid %s", ErrInvalidRecord, field)
}
