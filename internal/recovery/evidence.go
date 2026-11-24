package recovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// EvidenceBinding identifies the exact trusted operation and adapter steps.
// It is deliberately independent of a specific game or hosting provider.
type EvidenceBinding struct {
	ServerID      string
	EnvironmentID string
	InstanceRef   json.RawMessage
	UnitRef       json.RawMessage
	ReleaseID     string
	InspectStepID string
	DrainStepID   string
	SaveStepID    string
	RestartStepID string
}

var ErrUnsafeRestart = errors.New("recovery restart has no complete, fresh, exact-target save and drain evidence")

// RestartProof is a derived, non-secret summary. It does not itself dispatch
// any action; the workflow engine must persist and preflight the adapter input.
type RestartProof struct {
	RuntimeID string
	SavedAt   time.Time
}

// CheckRestartDispatch must run immediately before an execution.unit.restart
// attempt. The source is the persisted, already accepted operation, NOT a
// client-supplied restart request or a current process probe alone.
func CheckRestartDispatch(record store.OperationRecord, binding EvidenceBinding, now time.Time) (RestartProof, error) {
	if !validRecoveryBinding(record, binding) || now.IsZero() {
		return RestartProof{}, ErrUnsafeRestart
	}
	proof, save, err := confirmedRestartDependencies(record, binding, now)
	if err != nil {
		return RestartProof{}, ErrUnsafeRestart
	}
	restart := operationStepByID(record, binding.RestartStepID)
	if restart.StepID == "" || restart.CapabilityName != "execution.unit.restart" {
		return RestartProof{}, ErrUnsafeRestart
	}
	if !restartInputMatches(restart, proof.RuntimeID, binding, save) {
		return RestartProof{}, ErrUnsafeRestart
	}
	return proof, nil
}

func confirmedRestartDependencies(record store.OperationRecord, binding EvidenceBinding, now time.Time) (RestartProof, json.RawMessage, error) {
	inspect, ok := succeededStep(record, binding.InspectStepID, "execution.unit.inspect")
	if !ok {
		return RestartProof{}, nil, ErrUnsafeRestart
	}
	runtimeID, inspectedAt, err := checkedUnitInspect(inspect.Output, binding)
	if err != nil || runtimeID == "" {
		return RestartProof{}, nil, ErrUnsafeRestart
	}
	drain, ok := succeededStep(record, binding.DrainStepID, "integration.proxy.backend.drain")
	if !ok || !confirmedDrain(drain.Output, binding, now) {
		return RestartProof{}, nil, ErrUnsafeRestart
	}
	saved, ok := succeededStep(record, binding.SaveStepID, "game.save.request")
	if !ok {
		return RestartProof{}, nil, ErrUnsafeRestart
	}
	savedAt, err := confirmedSave(saved.Output, binding)
	if err != nil || savedAt.Before(inspectedAt) || savedAt.Before(record.CreatedAt) {
		return RestartProof{}, nil, ErrUnsafeRestart
	}
	if savedAt.After(now) || now.Sub(savedAt) > 3*time.Minute {
		return RestartProof{}, nil, ErrUnsafeRestart
	}
	return RestartProof{RuntimeID: runtimeID, SavedAt: savedAt}, saved.Output, nil
}

func validRecoveryBinding(record store.OperationRecord, binding EvidenceBinding) bool {
	if record.OperationType != "server.recover" || record.ProtocolVersion == "" {
		return false
	}
	if binding.ServerID == "" || binding.EnvironmentID == "" || binding.ReleaseID == "" {
		return false
	}
	if record.Target.ServerID != binding.ServerID || record.Target.EnvironmentID != binding.EnvironmentID {
		return false
	}
	if !equalDocument(record.Target.InstanceRef, binding.InstanceRef) {
		return false
	}
	if len(binding.UnitRef) == 0 || binding.InspectStepID == "" {
		return false
	}
	return binding.DrainStepID != "" && binding.SaveStepID != "" && binding.RestartStepID != ""
}

func succeededStep(record store.OperationRecord, id, capability string) (store.OperationStep, bool) {
	step := operationStepByID(record, id)
	return step, step.StepID != "" && step.Status == protocolstate.StepSucceeded &&
		step.CapabilityName == capability && len(step.Output) != 0
}

func operationStepByID(record store.OperationRecord, id string) store.OperationStep {
	for _, step := range record.Steps {
		if step.StepID == id {
			return step
		}
	}
	return store.OperationStep{}
}

func equalDocument(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	la, _ := json.Marshal(left)
	rb, _ := json.Marshal(right)
	return bytes.Equal(la, rb) && len(la) > 0
}

type targetIdentity struct {
	ServerID      string          `json:"server_id"`
	EnvironmentID string          `json:"environment_id"`
	InstanceRef   json.RawMessage `json:"instance_ref"`
}

func (t targetIdentity) matches(binding EvidenceBinding) bool {
	return t.ServerID == binding.ServerID && t.EnvironmentID == binding.EnvironmentID &&
		equalDocument(t.InstanceRef, binding.InstanceRef)
}

func checkedUnitInspect(raw json.RawMessage, binding EvidenceBinding) (string, time.Time, error) {
	var output struct {
		UnitRef    json.RawMessage `json:"unit_ref"`
		Target     targetIdentity  `json:"target"`
		ReleaseRef string          `json:"release_ref"`
		RuntimeID  string          `json:"runtime_id"`
		State      string          `json:"state"`
		ObservedAt string          `json:"observed_at"`
	}
	if json.Unmarshal(raw, &output) != nil || !equalDocument(output.UnitRef, binding.UnitRef) {
		return "", time.Time{}, ErrUnsafeRestart
	}
	if !output.Target.matches(binding) || output.ReleaseRef != binding.ReleaseID ||
		output.State != "running" || output.RuntimeID == "" {
		return "", time.Time{}, ErrUnsafeRestart
	}
	observed, err := time.Parse(time.RFC3339Nano, output.ObservedAt)
	return output.RuntimeID, observed, err
}

func confirmedDrain(raw json.RawMessage, binding EvidenceBinding, now time.Time) bool {
	var output struct {
		Target struct {
			ServerID      string          `json:"server_id"`
			EnvironmentID string          `json:"environment_id"`
			InstanceRef   json.RawMessage `json:"instance_ref"`
		} `json:"target"`
		RequestOutcome    string `json:"request_outcome"`
		RegistrationState string `json:"registration_state"`
		Routability       string `json:"routability"`
		ObservedAt        string `json:"observed_at"`
	}
	if json.Unmarshal(raw, &output) != nil ||
		!(targetIdentity(output.Target).matches(binding)) {
		return false
	}
	if output.RequestOutcome != "confirmed" || output.RegistrationState != "draining" ||
		output.Routability != "not_routable" {
		return false
	}
	timestamp, err := time.Parse(time.RFC3339Nano, output.ObservedAt)
	return err == nil && !timestamp.After(now) && now.Sub(timestamp) <= 5*time.Minute
}

func confirmedSave(raw json.RawMessage, binding EvidenceBinding) (time.Time, error) {
	var output struct {
		Target    targetIdentity `json:"target"`
		ReleaseID string         `json:"release_id"`
		Outcome   string         `json:"outcome"`
		SavedAt   string         `json:"saved_at"`
	}
	if json.Unmarshal(raw, &output) != nil || !output.Target.matches(binding) ||
		output.ReleaseID != binding.ReleaseID || output.Outcome != "confirmed" {
		return time.Time{}, ErrUnsafeRestart
	}
	return time.Parse(time.RFC3339Nano, output.SavedAt)
}

func restartInputMatches(step store.OperationStep, runtimeID string, binding EvidenceBinding, save json.RawMessage) bool {
	raw := step.ResolvedInput
	if len(raw) == 0 {
		return false
	}
	var input struct {
		UnitRef           json.RawMessage `json:"unit_ref"`
		Target            targetIdentity  `json:"target"`
		ReleaseID         string          `json:"release_id"`
		ExpectedRuntimeID string          `json:"expected_runtime_id"`
		SaveConfirmation  json.RawMessage `json:"save_confirmation"`
		SaveStepID        string          `json:"save_step_id"`
		Force             bool            `json:"force"`
		GraceSeconds      int             `json:"grace_period_seconds"`
	}
	if json.Unmarshal(raw, &input) != nil || !equalDocument(input.UnitRef, binding.UnitRef) ||
		!input.Target.matches(binding) {
		return false
	}
	if input.ReleaseID != binding.ReleaseID || input.ExpectedRuntimeID != runtimeID {
		return false
	}
	if input.SaveStepID != binding.SaveStepID || input.Force {
		return false
	}
	if input.GraceSeconds < 1 || input.GraceSeconds > 120 {
		return false
	}
	return equalDocument(input.SaveConfirmation, save)
}

// CheckNewRuntimeProof rejects an apparently completed restart that did not
// replace the exact Paper process cleanly. A healthy TCP port is not proof.
func CheckNewRuntimeProof(record store.OperationRecord, binding EvidenceBinding, newInspectStep string, now time.Time) (string, error) {
	old, err := CheckRestartDispatch(record, binding, now)
	if err != nil {
		return "", err
	}
	restarted, ok := succeededStep(record, binding.RestartStepID, "execution.unit.restart")
	if !ok {
		return "", errors.New("restart did not complete in the durable operation")
	}
	newID, err := verifiedRestartOutput(restarted.Output, binding, old.RuntimeID)
	if err != nil {
		return "", err
	}
	return verifyNewRuntimeObservation(record, binding, newInspectStep, newID, now)
}

func verifiedRestartOutput(raw json.RawMessage, binding EvidenceBinding, oldID string) (string, error) {
	var output struct {
		UnitRef                 json.RawMessage `json:"unit_ref"`
		Target                  targetIdentity  `json:"target"`
		ReleaseID               string          `json:"release_id"`
		PreviousRuntimeID       string          `json:"previous_runtime_id"`
		RuntimeID               string          `json:"runtime_id"`
		Outcome                 string          `json:"outcome"`
		State                   string          `json:"state"`
		CleanStopConfirmed      bool            `json:"clean_stop_confirmed"`
		ForceUsed               bool            `json:"force_used"`
		PersistentDataPreserved bool            `json:"persistent_data_preserved"`
	}
	if json.Unmarshal(raw, &output) != nil || !equalDocument(output.UnitRef, binding.UnitRef) {
		return "", errors.New("restart output does not bind exact unit")
	}
	if !output.Target.matches(binding) || output.ReleaseID != binding.ReleaseID {
		return "", errors.New("restart output does not bind exact host and release")
	}
	if output.PreviousRuntimeID != oldID || output.RuntimeID == "" || output.RuntimeID == oldID {
		return "", errors.New("restart did not yield a new runtime on the same release")
	}
	if output.Outcome != "completed" || output.State != "running" {
		return "", errors.New("restart outcome was not completed and running")
	}
	if !output.CleanStopConfirmed || output.ForceUsed || !output.PersistentDataPreserved {
		return "", errors.New("restart lacks confirmed graceful stop and data preservation")
	}
	return output.RuntimeID, nil
}

func verifyNewRuntimeObservation(record store.OperationRecord, binding EvidenceBinding, stepID, expectedID string, now time.Time) (string, error) {
	next, ok := succeededStep(record, stepID, "execution.unit.inspect")
	if !ok {
		return "", errors.New("new runtime inspection was not confirmed")
	}
	observedID, observedAt, err := checkedUnitInspect(next.Output, binding)
	if err != nil || observedID != expectedID {
		return "", errors.New("fresh new runtime inspection does not match completed restart")
	}
	if observedAt.After(now) || now.Sub(observedAt) > 2*time.Minute {
		return "", errors.New("new runtime inspection is stale or from the future")
	}
	return observedID, nil
}

// DescribeSafetyFailure deliberately omits raw log lines, SSH credentials,
// world data, and adapter responses from outward-facing diagnostics.
func DescribeSafetyFailure(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("minecraft_recovery_rejected:%s", "insufficient_verified_evidence")
}
