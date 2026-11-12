package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// runningRecordForValidation returns a valid running record with one
// succeeded adapter step, for tests that break one field at a time.
func runningRecordForValidation() OperationRecord {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	started, completed := created.Add(time.Minute), created.Add(2*time.Minute)
	record := testOperation("op-validate")
	record.Status = protocolstate.OperationRunning
	record.CreatedAt, record.UpdatedAt = created, created.Add(10*time.Minute)
	record.StartedAt = &started
	record.Steps = []OperationStep{{
		StepID: "check", StepType: "game.readiness.check", Status: protocolstate.StepSucceeded, Attempt: 1,
		AdapterID: "game", CapabilityName: "game.readiness.check", CapabilityVersion: "1.0.0",
		Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`), StartedAt: &started, CompletedAt: &completed,
	}}
	return record
}

func TestValidateRecordReportsFirstInvalidField(t *testing.T) {
	at := func(offset time.Duration) *time.Time {
		value := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(offset)
		return &value
	}
	tests := []struct {
		name   string
		mutate func(*OperationRecord)
		field  string
	}{
		{"backup state on another operation type", func(r *OperationRecord) { r.BackupInitialLifecycleState = protocolstate.ServerReady }, "backup_initial_lifecycle_state"},
		{"backup state checked before identity", func(r *OperationRecord) {
			r.BackupInitialLifecycleState = protocolstate.ServerReady
			r.OperationID = ""
		}, "backup_initial_lifecycle_state"},
		{"missing operation ID", func(r *OperationRecord) { r.OperationID = "" }, "operation_id"},
		{"identity checked before timestamps", func(r *OperationRecord) {
			r.ActorID = ""
			r.UpdatedAt = r.CreatedAt.Add(-time.Second)
		}, "actor_id"},
		{"updated before created", func(r *OperationRecord) { r.UpdatedAt = r.CreatedAt.Add(-time.Second) }, "timestamps"},
		{"unknown status", func(r *OperationRecord) { r.Status = "paused" }, "status"},
		{"snapshot for another type", func(r *OperationRecord) { r.RequestSnapshot.OperationType = "other.operation" }, "request_snapshot"},
		{"instance ref checked before progress", func(r *OperationRecord) {
			r.Target.InstanceRef = json.RawMessage(`[]`)
			r.Progress = json.RawMessage(`[]`)
		}, "target.instance_ref"},
		{"progress is not an object", func(r *OperationRecord) { r.Progress = json.RawMessage(`[]`) }, "progress"},
		{"error is not an object", func(r *OperationRecord) { r.Error = json.RawMessage(`"failed"`) }, "error"},
		{"started before created", func(r *OperationRecord) { r.StartedAt = at(-time.Second) }, "started_at"},
		{"started after updated", func(r *OperationRecord) { r.StartedAt = at(11 * time.Minute) }, "started_at"},
		{"completed without matching update", func(r *OperationRecord) { r.CompletedAt = at(9 * time.Minute) }, "completed_at"},
		{"record checked before steps", func(r *OperationRecord) {
			r.CompletedAt = at(9 * time.Minute)
			r.Steps[0].StepID = ""
		}, "completed_at"},
		{"invalid step ID", func(r *OperationRecord) { r.Steps[0].StepID = "" }, "steps[0]"},
		{"started step without attempt", func(r *OperationRecord) { r.Steps[0].Attempt = 0 }, "steps[0].attempt"},
		{"duplicate step ID", func(r *OperationRecord) { r.Steps = append(r.Steps, r.Steps[0]) }, "duplicate step_id"},
		{"duplicate checked before second step's capability", func(r *OperationRecord) {
			duplicate := r.Steps[0]
			duplicate.AdapterID = ""
			r.Steps = append(r.Steps, duplicate)
		}, "duplicate step_id"},
		{"capability without adapter", func(r *OperationRecord) { r.Steps[0].AdapterID = "" }, "steps[0].capability"},
		{"adapter step without input", func(r *OperationRecord) { r.Steps[0].Input = nil }, "steps[0].input"},
		{"non-adapter step with array input", func(r *OperationRecord) {
			r.Steps[0].CapabilityName, r.Steps[0].CapabilityVersion, r.Steps[0].Output = "", "", nil
			r.Steps[0].Input = json.RawMessage(`[]`)
		}, "steps[0].input"},
		{"resolved input without bindings", func(r *OperationRecord) { r.Steps[0].ResolvedInput = json.RawMessage(`{}`) }, "steps[0].resolved_input has no bindings"},
		{"succeeded adapter step without output", func(r *OperationRecord) { r.Steps[0].Output = nil }, "steps[0].output"},
		{"external reference is not an object", func(r *OperationRecord) { r.Steps[0].ExternalOperationRef = json.RawMessage(`[]`) }, "steps[0].external_operation_ref"},
		{"step started before record", func(r *OperationRecord) { r.Steps[0].StartedAt = at(-time.Second) }, "steps[0].started_at"},
		{"step started after update", func(r *OperationRecord) { r.Steps[0].StartedAt = at(11 * time.Minute) }, "steps[0].started_at"},
		{"step completed before it started", func(r *OperationRecord) { r.Steps[0].CompletedAt = at(30 * time.Second) }, "steps[0].completed_at"},
		{"empty current step", func(r *OperationRecord) { id := ""; r.CurrentStepID = &id }, "current_step_id"},
		{"unknown current step", func(r *OperationRecord) { id := "missing"; r.CurrentStepID = &id }, "current_step_id"},
	}
	if err := validateRecord(runningRecordForValidation()); err != nil {
		t.Fatalf("baseline record: %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := runningRecordForValidation()
			test.mutate(&record)
			err := validateRecord(record)
			if !errors.Is(err, ErrInvalidRecord) || err.Error() != ErrInvalidRecord.Error()+": invalid "+test.field {
				t.Fatalf("validateRecord error = %v, want invalid %s", err, test.field)
			}
		})
	}
}

func TestValidateRecordAcceptsCurrentStep(t *testing.T) {
	record := runningRecordForValidation()
	id := "check"
	record.CurrentStepID = &id
	if err := validateRecord(record); err != nil {
		t.Fatalf("validateRecord: %v", err)
	}
}
