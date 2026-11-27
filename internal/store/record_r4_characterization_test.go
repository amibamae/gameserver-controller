package store

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// These characterization tests pin prepareNewRecord, validateStepOutcome and
// validateBackupInitialState before the r4 refactor splits them: every
// branch, the order the checks run in, the exact error text, and the
// ErrInvalidRecord sentinel.

// recordR4AssertError requires err to wrap ErrInvalidRecord with exactly the
// given text, or to be nil when want is empty.
func recordR4AssertError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if !errors.Is(err, ErrInvalidRecord) || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// recordR4Reason is the error text of a prepareNewRecord refusal.
func recordR4Reason(reason string) string {
	return ErrInvalidRecord.Error() + ": " + reason
}

// recordR4Field is the error text of an invalidField refusal.
func recordR4Field(field string) string {
	return ErrInvalidRecord.Error() + ": invalid " + field
}

// recordR4Time returns a pointer to a fixed time offset by d.
func recordR4Time(d time.Duration) *time.Time {
	value := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(d)
	return &value
}

// recordR4TwoSteps returns a new record with two pending steps.
func recordR4TwoSteps() OperationRecord {
	record := testOperation("op-prepare")
	record.Steps = append(record.Steps, OperationStep{StepID: "apply", StepType: "request.apply", Status: protocolstate.StepPending})
	return record
}

func TestRecordR4PrepareNewRecordRefusalsCharacterization(t *testing.T) {
	const (
		backupState = "backup initial state is core-owned"
		pending     = "new operations must start pending"
		stepPending = "new operation steps must start pending"
		resolved    = "resolved step inputs are written by the engine before dispatch"
		outcome     = "new operation cannot contain an execution outcome"
	)
	object := json.RawMessage(`{}`)
	tests := []struct {
		name   string
		mutate func(*OperationRecord)
		want   string
	}{
		{"backup state", func(r *OperationRecord) { r.BackupInitialLifecycleState = protocolstate.ServerReady }, recordR4Reason(backupState)},
		{"backup state on server.backup", func(r *OperationRecord) {
			r.OperationType, r.RequestSnapshot.OperationType = "server.backup", "server.backup"
			r.BackupInitialLifecycleState = protocolstate.ServerStopped
		}, recordR4Reason(backupState)},
		{"backup state before status", func(r *OperationRecord) {
			r.BackupInitialLifecycleState, r.Status = protocolstate.ServerReady, protocolstate.OperationRunning
		}, recordR4Reason(backupState)},
		{"running status", func(r *OperationRecord) { r.Status = protocolstate.OperationRunning }, recordR4Reason(pending)},
		{"succeeded status", func(r *OperationRecord) { r.Status = protocolstate.OperationSucceeded }, recordR4Reason(pending)},
		{"unknown status", func(r *OperationRecord) { r.Status = "paused" }, recordR4Reason(pending)},
		{"status before steps", func(r *OperationRecord) {
			r.Status, r.Steps[0].Status = protocolstate.OperationRunning, protocolstate.StepRunning
		}, recordR4Reason(pending)},
		{"running step", func(r *OperationRecord) { r.Steps[0].Status = protocolstate.StepRunning }, recordR4Reason(stepPending)},
		{"skipped step", func(r *OperationRecord) { r.Steps[1].Status = protocolstate.StepSkipped }, recordR4Reason(stepPending)},
		{"unknown step status", func(r *OperationRecord) { r.Steps[1].Status = "paused" }, recordR4Reason(stepPending)},
		{"resolved input", func(r *OperationRecord) { r.Steps[1].ResolvedInput = object }, recordR4Reason(resolved)},
		{"invalid resolved input", func(r *OperationRecord) { r.Steps[0].ResolvedInput = json.RawMessage(`[]`) }, recordR4Reason(resolved)},
		{"step status before its resolved input", func(r *OperationRecord) {
			r.Steps[0].Status, r.Steps[0].ResolvedInput = protocolstate.StepRunning, object
		}, recordR4Reason(stepPending)},
		{"first step resolved input before second step status", func(r *OperationRecord) {
			r.Steps[0].ResolvedInput, r.Steps[1].Status = object, protocolstate.StepRunning
		}, recordR4Reason(resolved)},
		{"first step status before second step resolved input", func(r *OperationRecord) {
			r.Steps[0].Status, r.Steps[1].ResolvedInput = protocolstate.StepFailed, object
		}, recordR4Reason(stepPending)},
		{"started at", func(r *OperationRecord) { r.StartedAt = recordR4Time(0) }, recordR4Reason(outcome)},
		{"completed at", func(r *OperationRecord) { r.CompletedAt = recordR4Time(0) }, recordR4Reason(outcome)},
		{"result", func(r *OperationRecord) { r.Result = object }, recordR4Reason(outcome)},
		{"error", func(r *OperationRecord) { r.Error = object }, recordR4Reason(outcome)},
		{"invalid result", func(r *OperationRecord) { r.Result = json.RawMessage(`[]`) }, recordR4Reason(outcome)},
		{"steps before outcome", func(r *OperationRecord) {
			r.Steps[1].Status, r.Result = protocolstate.StepRunning, object
		}, recordR4Reason(stepPending)},
		{"outcome before record validation", func(r *OperationRecord) { r.OperationID, r.Error = "", object }, recordR4Reason(outcome)},
		{"record validation", func(r *OperationRecord) { r.OperationID = "" }, recordR4Field("operation_id")},
		{"record validation of steps", func(r *OperationRecord) { r.Steps[1].StepID = r.Steps[0].StepID }, recordR4Field("duplicate step_id")},
		{"record validation of progress", func(r *OperationRecord) { r.Progress = json.RawMessage(`[]`) }, recordR4Field("progress")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := recordR4TwoSteps()
			test.mutate(&record)
			prepared, err := prepareNewRecord(record, time.Now())
			recordR4AssertError(t, err, test.want)
			if prepared.OperationID != "" || prepared.Steps != nil || !prepared.CreatedAt.IsZero() {
				t.Fatalf("refused record = %+v, want the zero record", prepared)
			}
		})
	}
}

func TestRecordR4PrepareNewRecordAcceptsCharacterization(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 8, time.FixedZone("UTC+2", 2*60*60))
	tests := []struct {
		name   string
		mutate func(*OperationRecord)
		steps  int
	}{
		{"pending record", func(*OperationRecord) {}, 2},
		{"empty status", func(r *OperationRecord) { r.Status = "" }, 2},
		{"empty step status", func(r *OperationRecord) { r.Steps[0].Status, r.Steps[1].Status = "", "" }, 2},
		{"nil steps", func(r *OperationRecord) { r.Steps = nil }, 0},
		{"empty steps", func(r *OperationRecord) { r.Steps = []OperationStep{} }, 0},
		{"empty result and error", func(r *OperationRecord) { r.Result, r.Error = json.RawMessage{}, json.RawMessage{} }, 2},
		{"empty resolved input", func(r *OperationRecord) { r.Steps[0].ResolvedInput = json.RawMessage{} }, 2},
		{"caller timestamps replaced", func(r *OperationRecord) {
			r.CreatedAt, r.UpdatedAt = *recordR4Time(-time.Hour), *recordR4Time(time.Hour)
		}, 2},
		{"progress and deadline kept", func(r *OperationRecord) {
			r.Progress, r.Deadline = json.RawMessage(`{"percent":0}`), recordR4Time(time.Hour)
		}, 2},
		{"bound step without resolved input", func(r *OperationRecord) {
			r.Steps[1].AdapterID, r.Steps[1].CapabilityName, r.Steps[1].CapabilityVersion = "game", "game.apply", "1.0.0"
			r.Steps[1].Input = json.RawMessage(`{}`)
			r.Steps[1].InputBindings = []inputbinding.Binding{{TargetPointer: "/v", SourceStepID: "validate", SourcePointer: "/value"}}
		}, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := recordR4TwoSteps()
			test.mutate(&record)
			prepared, err := prepareNewRecord(record, now)
			if err != nil {
				t.Fatalf("prepareNewRecord: %v", err)
			}
			recordR4AssertPrepared(t, prepared, now, test.steps)
		})
	}
}

// recordR4AssertPrepared checks the defaults and timestamps prepareNewRecord
// writes on an accepted record.
func recordR4AssertPrepared(t *testing.T, prepared OperationRecord, now time.Time, steps int) {
	t.Helper()
	if prepared.Status != protocolstate.OperationPending {
		t.Fatalf("status = %q, want pending", prepared.Status)
	}
	if prepared.Steps == nil || len(prepared.Steps) != steps {
		t.Fatalf("steps = %#v, want %d non-nil steps", prepared.Steps, steps)
	}
	for i, step := range prepared.Steps {
		if step.Status != protocolstate.StepPending {
			t.Fatalf("steps[%d].status = %q, want pending", i, step.Status)
		}
	}
	if !prepared.CreatedAt.Equal(now) || prepared.CreatedAt.Location() != time.UTC {
		t.Fatalf("created_at = %v, want %v in UTC", prepared.CreatedAt, now.UTC())
	}
	if prepared.UpdatedAt != prepared.CreatedAt {
		t.Fatalf("updated_at = %v, want created_at %v", prepared.UpdatedAt, prepared.CreatedAt)
	}
}

func TestRecordR4PrepareNewRecordDurableFormatCharacterization(t *testing.T) {
	record := recordR4TwoSteps()
	record.Status, record.Steps = "", nil
	record.IdempotencyKey = ""
	prepared, err := prepareNewRecord(record, time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))
	if err != nil {
		t.Fatalf("prepareNewRecord: %v", err)
	}
	encoded, err := json.Marshal(prepared)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"operation_id":"op-prepare","operation_type":"test.operation","protocol_version":"0.21.0-draft",` +
		`"schema_package_revision":"0.21.0-draft","target":{"server_id":"server-1","environment_id":"staging"},` +
		`"status":"pending","actor_id":"operator-1","request_snapshot":{"operation_type":"test.operation",` +
		`"parameters":{"plan":"small"}},"created_at":"2026-03-04T05:06:07Z","updated_at":"2026-03-04T05:06:07Z","steps":[]}`
	if string(encoded) != want {
		t.Fatalf("encoded record =\n%s\nwant\n%s", encoded, want)
	}
}

func TestRecordR4PrepareNewRecordDefaultsStepsInPlaceCharacterization(t *testing.T) {
	record := recordR4TwoSteps()
	record.Steps[0].Status, record.Steps[1].Status = "", ""
	prepared, err := prepareNewRecord(record, time.Now())
	if err != nil {
		t.Fatalf("prepareNewRecord: %v", err)
	}
	if &prepared.Steps[0] != &record.Steps[0] {
		t.Fatal("prepared steps do not share the caller's slice")
	}
	if record.Steps[0].Status != protocolstate.StepPending || record.Steps[1].Status != protocolstate.StepPending {
		t.Fatalf("caller steps = %+v, want the defaulted pending status", record.Steps)
	}
	if record.Status != protocolstate.OperationPending || !record.CreatedAt.IsZero() {
		t.Fatalf("caller record changed: status %q, created_at %v", record.Status, record.CreatedAt)
	}
}

func TestRecordR4ValidateStepOutcomeCharacterization(t *testing.T) {
	object := json.RawMessage(`{}`)
	tests := []struct {
		name   string
		mutate func(*OperationStep)
		field  string
	}{
		{"succeeded adapter step with output", func(*OperationStep) {}, ""},
		{"succeeded adapter step without output", func(s *OperationStep) { s.Output = nil }, "steps[3].output"},
		{"succeeded adapter step with empty output", func(s *OperationStep) { s.Output = json.RawMessage{} }, "steps[3].output"},
		{"succeeded adapter step with array output", func(s *OperationStep) { s.Output = json.RawMessage(`[]`) }, "steps[3].output"},
		{"succeeded adapter step with null output", func(s *OperationStep) { s.Output = json.RawMessage(`null`) }, "steps[3].output"},
		{"succeeded adapter step with invalid output", func(s *OperationStep) { s.Output = json.RawMessage(`{`) }, "steps[3].output"},
		{"succeeded plain step without output", func(s *OperationStep) { s.CapabilityName, s.Output = "", nil }, ""},
		{"succeeded plain step with output", func(s *OperationStep) { s.CapabilityName = "" }, ""},
		{"succeeded plain step with array output", func(s *OperationStep) { s.CapabilityName, s.Output = "", json.RawMessage(`[]`) }, "steps[3].output"},
		{"failed step with output", func(s *OperationStep) { s.Status = protocolstate.StepFailed }, "steps[3].output"},
		{"running step with output", func(s *OperationStep) { s.Status = protocolstate.StepRunning }, "steps[3].output"},
		{"failed adapter step without output", func(s *OperationStep) { s.Status, s.Output = protocolstate.StepFailed, nil }, ""},
		{"pending adapter step without output", func(s *OperationStep) { s.Status, s.Output = protocolstate.StepPending, nil }, ""},
		{"external reference object", func(s *OperationStep) { s.ExternalOperationRef = object }, ""},
		{"external reference array", func(s *OperationStep) { s.ExternalOperationRef = json.RawMessage(`[]`) }, "steps[3].external_operation_ref"},
		{"external reference null", func(s *OperationStep) { s.ExternalOperationRef = json.RawMessage(`null`) }, "steps[3].external_operation_ref"},
		{"empty external reference", func(s *OperationStep) { s.ExternalOperationRef = json.RawMessage{} }, ""},
		{"error object", func(s *OperationStep) { s.Error = object }, ""},
		{"error string", func(s *OperationStep) { s.Error = json.RawMessage(`"failed"`) }, "steps[3].error"},
		{"empty error", func(s *OperationStep) { s.Error = json.RawMessage{} }, ""},
		{"output before external reference", func(s *OperationStep) {
			s.Output, s.ExternalOperationRef = nil, json.RawMessage(`[]`)
		}, "steps[3].output"},
		{"output on failed step before external reference", func(s *OperationStep) {
			s.Status, s.ExternalOperationRef = protocolstate.StepFailed, json.RawMessage(`[]`)
		}, "steps[3].output"},
		{"external reference before error", func(s *OperationStep) {
			s.ExternalOperationRef, s.Error = json.RawMessage(`[]`), json.RawMessage(`[]`)
		}, "steps[3].external_operation_ref"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := OperationStep{
				StepID: "check", StepType: "game.readiness.check", Status: protocolstate.StepSucceeded, Attempt: 1,
				AdapterID: "game", CapabilityName: "game.readiness.check", CapabilityVersion: "1.0.0",
				Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"ready":true}`),
			}
			test.mutate(&step)
			want := ""
			if test.field != "" {
				want = recordR4Field(test.field)
			}
			recordR4AssertError(t, validateStepOutcome(OperationRecord{}, 3, step), want)
		})
	}
}

func TestRecordR4ValidateBackupInitialStateCharacterization(t *testing.T) {
	const backup = "server.backup"
	tests := []struct {
		name          string
		operationType string
		status        protocolstate.OperationStatus
		state         protocolstate.ServerState
		invalid       bool
	}{
		{"succeeded backup without state", backup, protocolstate.OperationSucceeded, "", true},
		{"succeeded backup from ready", backup, protocolstate.OperationSucceeded, protocolstate.ServerReady, false},
		{"succeeded backup from degraded", backup, protocolstate.OperationSucceeded, protocolstate.ServerDegraded, false},
		{"succeeded backup from stopped", backup, protocolstate.OperationSucceeded, protocolstate.ServerStopped, false},
		{"succeeded backup from backing up", backup, protocolstate.OperationSucceeded, protocolstate.ServerBackingUp, true},
		{"pending backup without state", backup, protocolstate.OperationPending, "", false},
		{"running backup without state", backup, protocolstate.OperationRunning, "", false},
		{"failed backup without state", backup, protocolstate.OperationFailed, "", false},
		{"running backup from ready", backup, protocolstate.OperationRunning, protocolstate.ServerReady, false},
		{"running backup from degraded", backup, protocolstate.OperationRunning, protocolstate.ServerDegraded, false},
		{"running backup from stopped", backup, protocolstate.OperationRunning, protocolstate.ServerStopped, false},
		{"running backup from starting", backup, protocolstate.OperationRunning, protocolstate.ServerStarting, true},
		{"running backup from failed", backup, protocolstate.OperationRunning, protocolstate.ServerFailed, true},
		{"running backup from unknown state", backup, protocolstate.OperationRunning, "paused", true},
		{"succeeded restore without state", "server.restore", protocolstate.OperationSucceeded, "", false},
		{"succeeded restore from ready", "server.restore", protocolstate.OperationSucceeded, protocolstate.ServerReady, true},
		{"running test operation from stopped", "test.operation", protocolstate.OperationRunning, protocolstate.ServerStopped, true},
		{"running test operation from destroyed", "test.operation", protocolstate.OperationRunning, protocolstate.ServerDestroyed, true},
		{"backup prefix is not server.backup", "server.backup.full", protocolstate.OperationSucceeded, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := OperationRecord{OperationType: test.operationType, Status: test.status, BackupInitialLifecycleState: test.state}
			want := ""
			if test.invalid {
				want = recordR4Field("backup_initial_lifecycle_state")
			}
			recordR4AssertError(t, validateBackupInitialState(record), want)
		})
	}
}
