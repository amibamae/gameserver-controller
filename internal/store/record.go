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

type OperationTarget struct {
	ServerID      string          `json:"server_id"`
	EnvironmentID string          `json:"environment_id"`
	InstanceRef   json.RawMessage `json:"instance_ref,omitempty"`
}

type RequestSnapshot struct {
	OperationType string            `json:"operation_type"`
	Parameters    json.RawMessage   `json:"parameters"`
	SecretRefs    []SecretReference `json:"secret_refs,omitempty"`
}

type SecretReference struct {
	SecretRef       string   `json:"secret_ref"`
	Purpose         string   `json:"purpose"`
	PermissionScope []string `json:"permission_scope,omitempty"`
	VersionRef      string   `json:"version_ref,omitempty"`
}

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

type StoredOperation struct {
	Record   OperationRecord
	Revision uint64
}

func prepareNewRecord(record OperationRecord, now time.Time) (OperationRecord, error) {
	if record.BackupInitialLifecycleState != "" {
		return OperationRecord{}, fmt.Errorf("%w: backup initial state is core-owned", ErrInvalidRecord)
	}
	if record.Status == "" {
		record.Status = protocolstate.OperationPending
	}
	if record.Status != protocolstate.OperationPending {
		return OperationRecord{}, fmt.Errorf("%w: new operations must start pending", ErrInvalidRecord)
	}
	if record.Steps == nil {
		record.Steps = []OperationStep{}
	}
	for i := range record.Steps {
		if record.Steps[i].Status == "" {
			record.Steps[i].Status = protocolstate.StepPending
		}
		if record.Steps[i].Status != protocolstate.StepPending {
			return OperationRecord{}, fmt.Errorf("%w: new operation steps must start pending", ErrInvalidRecord)
		}
		if len(record.Steps[i].ResolvedInput) > 0 {
			return OperationRecord{}, fmt.Errorf("%w: resolved step inputs are written by the engine before dispatch", ErrInvalidRecord)
		}
	}
	if record.StartedAt != nil || record.CompletedAt != nil || len(record.Result) != 0 || len(record.Error) != 0 {
		return OperationRecord{}, fmt.Errorf("%w: new operation cannot contain an execution outcome", ErrInvalidRecord)
	}
	record.CreatedAt = now.UTC()
	record.UpdatedAt = record.CreatedAt
	if err := validateRecord(record); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

// validateRecord checks an operation record before it is stored or after it
// is loaded. The checks run in a fixed order and the first failure is
// returned, so callers and tests see the same field name for the same record.
func validateRecord(record OperationRecord) error {
	for _, validate := range []func(OperationRecord) error{
		validateBackupInitialState,
		validateRecordIdentity,
		validateRecordStatus,
		validateRecordDocuments,
		validateRecordExecutionTimes,
		validateRecordSteps,
		validateRecordStepPlan,
	} {
		if err := validate(record); err != nil {
			return err
		}
	}
	return nil
}

// validateBackupInitialState requires a succeeded server.backup to record the
// lifecycle state it started from, and allows that field only on server.backup
// with a ready, degraded, or stopped state.
func validateBackupInitialState(record OperationRecord) error {
	if record.OperationType == "server.backup" && record.Status == protocolstate.OperationSucceeded && record.BackupInitialLifecycleState == "" {
		return invalidField("backup_initial_lifecycle_state")
	}
	if state := record.BackupInitialLifecycleState; state != "" {
		if record.OperationType != "server.backup" || (state != protocolstate.ServerReady && state != protocolstate.ServerDegraded && state != protocolstate.ServerStopped) {
			return invalidField("backup_initial_lifecycle_state")
		}
	}
	return nil
}

func validateRecordIdentity(record OperationRecord) error {
	if !validIdentifier(record.OperationID, 200) {
		return invalidField("operation_id")
	}
	if !validQualifiedName(record.OperationType, 160) {
		return invalidField("operation_type")
	}
	if record.ProtocolVersion == "" || len(record.ProtocolVersion) > 100 {
		return invalidField("protocol_version")
	}
	if len(record.SchemaPackageRevision) > 200 {
		return invalidField("schema_package_revision")
	}
	if !validIdentifier(record.Target.ServerID, 200) || !validIdentifier(record.Target.EnvironmentID, 200) {
		return invalidField("target")
	}
	if record.ActorID == "" || len(record.ActorID) > 200 {
		return invalidField("actor_id")
	}
	if record.CorrelationID != "" && len(record.CorrelationID) > 200 {
		return invalidField("correlation_id")
	}
	if record.IdempotencyKey != "" && !validIdempotencyKey(record.IdempotencyKey) {
		return invalidField("idempotency_key")
	}
	if record.RequestFingerprint != "" && !validFingerprint(record.RequestFingerprint) {
		return invalidField("request_fingerprint")
	}
	return nil
}

func validateRecordStatus(record OperationRecord) error {
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) {
		return invalidField("timestamps")
	}
	if !knownOperationStatus(record.Status) {
		return invalidField("status")
	}
	return nil
}

// validateRecordDocuments checks the request snapshot and that every stored
// JSON document on the record is an object.
func validateRecordDocuments(record OperationRecord) error {
	if record.RequestSnapshot.OperationType != record.OperationType || !isJSONObject(record.RequestSnapshot.Parameters) {
		return invalidField("request_snapshot")
	}
	for _, ref := range record.RequestSnapshot.SecretRefs {
		if !validSecretReference(ref) {
			return invalidField("request_snapshot.secret_refs")
		}
	}
	for _, document := range []struct {
		field string
		value []byte
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

func validateRecordExecutionTimes(record OperationRecord) error {
	if record.StartedAt != nil && (record.StartedAt.Before(record.CreatedAt) || record.StartedAt.After(record.UpdatedAt)) {
		return invalidField("started_at")
	}
	if record.CompletedAt != nil && (record.CompletedAt.Before(record.CreatedAt) || !record.CompletedAt.Equal(record.UpdatedAt)) {
		return invalidField("completed_at")
	}
	return nil
}

// validateRecordSteps checks every step in order and rejects repeated step
// IDs. A current step, if set, must name one of the steps.
func validateRecordSteps(record OperationRecord) error {
	seenSteps := make(map[string]struct{}, len(record.Steps))
	for i, step := range record.Steps {
		if err := validateStepIdentity(record, i, step); err != nil {
			return err
		}
		if _, exists := seenSteps[step.StepID]; exists {
			return invalidField("duplicate step_id")
		}
		seenSteps[step.StepID] = struct{}{}
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
	if record.OperationType == "server.backup" && record.BackupInitialLifecycleState == "" && (step.Attempt != 0 || step.Status == protocolstate.StepRunning || step.Status == protocolstate.StepSucceeded || step.Status == protocolstate.StepWaiting || step.Status == protocolstate.StepOutcomeUnknown) {
		return invalidField("backup_initial_lifecycle_state must precede dispatch")
	}
	if !validIdentifier(step.StepID, 200) || !validCapabilityName(step.StepType, 160) || !knownStepStatus(step.Status) || step.Attempt < 0 {
		return invalidField(fmt.Sprintf("steps[%d]", i))
	}
	if (step.Status == protocolstate.StepPending && step.Attempt != 0) ||
		(step.Status != protocolstate.StepPending && step.Status != protocolstate.StepSkipped && step.Status != protocolstate.StepCancelled && step.Attempt == 0) {
		return invalidField(fmt.Sprintf("steps[%d].attempt", i))
	}
	return nil
}

// validateStepInput checks the step's capability identity, its input object,
// and its input bindings. Resolved input exists only for bound steps and must
// be persisted before a bound step is dispatched.
func validateStepInput(_ OperationRecord, i int, step OperationStep) error {
	if step.CapabilityName != "" && (!validCapabilityName(step.CapabilityName, 160) || step.AdapterID == "" || step.CapabilityVersion == "") {
		return invalidField(fmt.Sprintf("steps[%d].capability", i))
	}
	if step.CapabilityVersion != "" && (step.AdapterID == "" || step.CapabilityName == "") {
		return invalidField(fmt.Sprintf("steps[%d].capability", i))
	}
	if (step.CapabilityName != "" || len(step.Input) > 0) && !isJSONObject(step.Input) {
		return invalidField(fmt.Sprintf("steps[%d].input", i))
	}
	if len(step.InputBindings) > 0 && (step.CapabilityName == "" || !isJSONObject(step.Input)) {
		return invalidField(fmt.Sprintf("steps[%d].input_bindings", i))
	}
	if len(step.ResolvedInput) > 0 && !isJSONObject(step.ResolvedInput) {
		return invalidField(fmt.Sprintf("steps[%d].resolved_input", i))
	}
	if len(step.InputBindings) == 0 && len(step.ResolvedInput) > 0 {
		return invalidField(fmt.Sprintf("steps[%d].resolved_input has no bindings", i))
	}
	if len(step.InputBindings) > 0 && step.Attempt > 0 && len(step.ResolvedInput) == 0 {
		return invalidField(fmt.Sprintf("steps[%d].resolved_input must be persisted before dispatch", i))
	}
	return nil
}

// validateStepOutcome checks the step's output, external operation reference,
// and error documents. Only a succeeded step has output, and a succeeded
// adapter step must have it.
func validateStepOutcome(_ OperationRecord, i int, step OperationStep) error {
	if len(step.Output) > 0 && (!isJSONObject(step.Output) || step.Status != protocolstate.StepSucceeded) {
		return invalidField(fmt.Sprintf("steps[%d].output", i))
	}
	if step.Status == protocolstate.StepSucceeded && step.CapabilityName != "" && len(step.Output) == 0 {
		return invalidField(fmt.Sprintf("steps[%d].output", i))
	}
	if len(step.ExternalOperationRef) > 0 && !isJSONObject(step.ExternalOperationRef) {
		return invalidField(fmt.Sprintf("steps[%d].external_operation_ref", i))
	}
	if len(step.Error) > 0 && !isJSONObject(step.Error) {
		return invalidField(fmt.Sprintf("steps[%d].error", i))
	}
	return nil
}

// validateStepTimes requires step timestamps to fall within the record's
// lifetime, with completion no earlier than the step's start.
func validateStepTimes(record OperationRecord, i int, step OperationStep) error {
	if step.StartedAt != nil && (step.StartedAt.Before(record.CreatedAt) || step.StartedAt.After(record.UpdatedAt)) {
		return invalidField(fmt.Sprintf("steps[%d].started_at", i))
	}
	if step.CompletedAt != nil && (step.CompletedAt.Before(record.CreatedAt) || (step.StartedAt != nil && step.CompletedAt.Before(*step.StartedAt)) || step.CompletedAt.After(record.UpdatedAt)) {
		return invalidField(fmt.Sprintf("steps[%d].completed_at", i))
	}
	return nil
}

// validateRecordStepPlan checks the input bindings across steps and that the
// current step, if set, is one of the record's steps.
func validateRecordStepPlan(record OperationRecord) error {
	bindingSteps := make([]inputbinding.Step, len(record.Steps))
	for i, step := range record.Steps {
		bindingSteps[i] = inputbinding.Step{StepID: step.StepID, Status: string(step.Status), Input: step.Input, Output: step.Output, Binding: step.InputBindings}
	}
	if err := inputbinding.ValidatePlan(bindingSteps); err != nil {
		return invalidField("step input_bindings")
	}
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

func containsStepID(steps []OperationStep, stepID string) bool {
	for _, step := range steps {
		if step.StepID == stepID {
			return true
		}
	}
	return false
}

func validSecretReference(ref SecretReference) bool {
	if ref.SecretRef == "" || len(ref.SecretRef) > 1024 || ref.Purpose == "" || len(ref.Purpose) > 100 {
		return false
	}
	separator := strings.Index(ref.SecretRef, "://")
	if separator < 1 || ref.SecretRef[0] < 'a' || ref.SecretRef[0] > 'z' {
		return false
	}
	for _, ch := range ref.SecretRef[1:separator] {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '+' && ch != '.' && ch != '-' {
			return false
		}
	}
	if len(ref.VersionRef) > 500 {
		return false
	}
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

func validIdentifier(value string, max int) bool {
	return value != "" && len(value) <= max
}

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

func validFingerprint(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && len(decoded) == 32
}

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

func invalidField(field string) error {
	return fmt.Errorf("%w: invalid %s", ErrInvalidRecord, field)
}
