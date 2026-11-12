package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

var recordTestTime = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// validRecordForTest is a running record with one completed and one pending
// adapter step; every validateRecord rule passes for it.
func validRecordForTest() OperationRecord {
	started := recordTestTime.Add(time.Second)
	completed := recordTestTime.Add(2 * time.Second)
	current := "step-2"
	return OperationRecord{
		OperationID: "op-1", OperationType: "server.start", ProtocolVersion: "0.21.0-draft",
		Target:          OperationTarget{ServerID: "server-1", EnvironmentID: "staging"},
		Status:          protocolstate.OperationRunning,
		CurrentStepID:   &current,
		ActorID:         "actor-1",
		IdempotencyKey:  "key-1",
		RequestSnapshot: RequestSnapshot{OperationType: "server.start", Parameters: json.RawMessage(`{}`)},
		CreatedAt:       recordTestTime, UpdatedAt: recordTestTime.Add(3 * time.Second), StartedAt: &started,
		Steps: []OperationStep{
			{StepID: "step-1", StepType: "adapter.invoke", Status: protocolstate.StepSucceeded, Attempt: 1, AdapterID: "execution.local",
				CapabilityName: "execution.unit.start", CapabilityVersion: "1", Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"ok":true}`),
				StartedAt: &started, CompletedAt: &completed},
			{StepID: "step-2", StepType: "adapter.invoke", Status: protocolstate.StepPending, AdapterID: "execution.local",
				CapabilityName: "execution.unit.inspect", CapabilityVersion: "1", Input: json.RawMessage(`{}`)},
		},
	}
}

func timeRef(t time.Time) *time.Time { return &t }

// TestValidateRecordReportsEachRuleInOrder pins every validateRecord rule and
// the field it reports, so refactoring cannot change which error a record
// gets.
func TestValidateRecordReportsEachRuleInOrder(t *testing.T) {
	if err := validateRecord(validRecordForTest()); err != nil {
		t.Fatalf("base record is invalid: %v", err)
	}
	cases := []struct {
		name, field string
		mutate      func(*OperationRecord)
	}{
		{"succeeded backup without initial state", "backup_initial_lifecycle_state", func(r *OperationRecord) {
			r.OperationType, r.RequestSnapshot.OperationType, r.Status = "server.backup", "server.backup", protocolstate.OperationSucceeded
		}},
		{"initial state on another type", "backup_initial_lifecycle_state", func(r *OperationRecord) { r.BackupInitialLifecycleState = protocolstate.ServerReady }},
		{"initial state not resumable", "backup_initial_lifecycle_state", func(r *OperationRecord) {
			r.OperationType, r.RequestSnapshot.OperationType, r.BackupInitialLifecycleState = "server.backup", "server.backup", protocolstate.ServerDestroyed
		}},
		{"empty operation id", "operation_id", func(r *OperationRecord) { r.OperationID = "" }},
		{"bad operation type", "operation_type", func(r *OperationRecord) { r.OperationType = "Server.Start" }},
		{"empty protocol version", "protocol_version", func(r *OperationRecord) { r.ProtocolVersion = "" }},
		{"long protocol version", "protocol_version", func(r *OperationRecord) { r.ProtocolVersion = strings.Repeat("v", 101) }},
		{"long schema revision", "schema_package_revision", func(r *OperationRecord) { r.SchemaPackageRevision = strings.Repeat("r", 201) }},
		{"empty server", "target", func(r *OperationRecord) { r.Target.ServerID = "" }},
		{"empty environment", "target", func(r *OperationRecord) { r.Target.EnvironmentID = "" }},
		{"empty actor", "actor_id", func(r *OperationRecord) { r.ActorID = "" }},
		{"long correlation", "correlation_id", func(r *OperationRecord) { r.CorrelationID = strings.Repeat("c", 201) }},
		{"bad idempotency key", "idempotency_key", func(r *OperationRecord) { r.IdempotencyKey = "has space" }},
		{"bad fingerprint", "request_fingerprint", func(r *OperationRecord) { r.RequestFingerprint = "sha256:zz" }},
		{"zero created", "timestamps", func(r *OperationRecord) { r.CreatedAt = time.Time{} }},
		{"updated before created", "timestamps", func(r *OperationRecord) { r.UpdatedAt = recordTestTime.Add(-time.Second) }},
		{"unknown status", "status", func(r *OperationRecord) { r.Status = "paused" }},
		{"snapshot type mismatch", "request_snapshot", func(r *OperationRecord) { r.RequestSnapshot.OperationType = "server.stop" }},
		{"snapshot parameters not object", "request_snapshot", func(r *OperationRecord) { r.RequestSnapshot.Parameters = json.RawMessage(`[]`) }},
		{"bad secret ref", "request_snapshot.secret_refs", func(r *OperationRecord) {
			r.RequestSnapshot.SecretRefs = []SecretReference{{SecretRef: "no-scheme", Purpose: "p"}}
		}},
		{"instance ref not object", "target.instance_ref", func(r *OperationRecord) { r.Target.InstanceRef = json.RawMessage(`"i-1"`) }},
		{"progress not object", "progress", func(r *OperationRecord) { r.Progress = json.RawMessage(`1`) }},
		{"result not object", "result", func(r *OperationRecord) { r.Result = json.RawMessage(`1`) }},
		{"error not object", "error", func(r *OperationRecord) { r.Error = json.RawMessage(`1`) }},
		{"started before created", "started_at", func(r *OperationRecord) { r.StartedAt = timeRef(recordTestTime.Add(-time.Second)) }},
		{"started after updated", "started_at", func(r *OperationRecord) { r.StartedAt = timeRef(recordTestTime.Add(time.Hour)) }},
		{"completed not at updated", "completed_at", func(r *OperationRecord) { r.CompletedAt = timeRef(recordTestTime.Add(time.Second)) }},
		{"backup dispatched before initial state", "backup_initial_lifecycle_state must precede dispatch", func(r *OperationRecord) {
			r.OperationType, r.RequestSnapshot.OperationType = "server.backup", "server.backup"
		}},
		{"bad step id", "steps[0]", func(r *OperationRecord) { r.Steps[0].StepID = "" }},
		{"bad step type", "steps[0]", func(r *OperationRecord) { r.Steps[0].StepType = "X" }},
		{"unknown step status", "steps[1]", func(r *OperationRecord) { r.Steps[1].Status = "paused" }},
		{"negative attempt", "steps[1]", func(r *OperationRecord) { r.Steps[1].Attempt = -1 }},
		{"pending with attempt", "steps[1].attempt", func(r *OperationRecord) { r.Steps[1].Attempt = 1 }},
		{"dispatched without attempt", "steps[0].attempt", func(r *OperationRecord) { r.Steps[0].Attempt = 0 }},
		{"duplicate step", "duplicate step_id", func(r *OperationRecord) { r.Steps[1].StepID = "step-1" }},
		{"bad capability name", "steps[1].capability", func(r *OperationRecord) { r.Steps[1].CapabilityName = "X" }},
		{"capability without adapter", "steps[1].capability", func(r *OperationRecord) { r.Steps[1].AdapterID = "" }},
		{"version without capability", "steps[1].capability", func(r *OperationRecord) { r.Steps[1].CapabilityName = "" }},
		{"capability without input", "steps[1].input", func(r *OperationRecord) { r.Steps[1].Input = nil }},
		{"input not object", "steps[1].input", func(r *OperationRecord) {
			r.Steps[1].CapabilityName, r.Steps[1].CapabilityVersion, r.Steps[1].AdapterID, r.Steps[1].Input = "", "", "", json.RawMessage(`1`)
		}},
		{"bindings without capability", "steps[1].input_bindings", func(r *OperationRecord) {
			r.Steps[1].CapabilityName, r.Steps[1].CapabilityVersion, r.Steps[1].AdapterID = "", "", ""
			r.Steps[1].InputBindings = []inputbinding.Binding{{TargetPointer: "/a", SourceStepID: "step-1", SourcePointer: "/ok"}}
		}},
		{"resolved input not object", "steps[1].resolved_input", func(r *OperationRecord) { r.Steps[1].ResolvedInput = json.RawMessage(`1`) }},
		{"resolved input without bindings", "steps[1].resolved_input has no bindings", func(r *OperationRecord) { r.Steps[1].ResolvedInput = json.RawMessage(`{}`) }},
		{"bound step dispatched without resolved input", "steps[0].resolved_input must be persisted before dispatch", func(r *OperationRecord) {
			r.Steps[0].InputBindings = []inputbinding.Binding{{TargetPointer: "/a", SourceStepID: "step-2", SourcePointer: "/ok"}}
		}},
		{"output not object", "steps[0].output", func(r *OperationRecord) { r.Steps[0].Output = json.RawMessage(`1`) }},
		{"output on unfinished step", "steps[1].output", func(r *OperationRecord) { r.Steps[1].Output = json.RawMessage(`{}`) }},
		{"succeeded capability without output", "steps[0].output", func(r *OperationRecord) { r.Steps[0].Output = nil }},
		{"external ref not object", "steps[0].external_operation_ref", func(r *OperationRecord) { r.Steps[0].ExternalOperationRef = json.RawMessage(`1`) }},
		{"step error not object", "steps[0].error", func(r *OperationRecord) { r.Steps[0].Error = json.RawMessage(`1`) }},
		{"step started before created", "steps[0].started_at", func(r *OperationRecord) { r.Steps[0].StartedAt = timeRef(recordTestTime.Add(-time.Second)) }},
		{"step started after updated", "steps[0].started_at", func(r *OperationRecord) { r.Steps[0].StartedAt = timeRef(recordTestTime.Add(time.Hour)) }},
		{"step completed before started", "steps[0].completed_at", func(r *OperationRecord) { r.Steps[0].CompletedAt = timeRef(recordTestTime.Add(500 * time.Millisecond)) }},
		{"step completed after updated", "steps[0].completed_at", func(r *OperationRecord) { r.Steps[0].CompletedAt = timeRef(recordTestTime.Add(time.Hour)) }},
		{"binding plan invalid", "step input_bindings", func(r *OperationRecord) {
			r.Steps[1].InputBindings = []inputbinding.Binding{{TargetPointer: "/a", SourceStepID: "missing-step", SourcePointer: "/ok"}}
		}},
		{"empty current step", "current_step_id", func(r *OperationRecord) { empty := ""; r.CurrentStepID = &empty }},
		{"unknown current step", "current_step_id", func(r *OperationRecord) { other := "step-9"; r.CurrentStepID = &other }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			record := validRecordForTest()
			test.mutate(&record)
			err := validateRecord(record)
			if !errors.Is(err, ErrInvalidRecord) || err.Error() != ErrInvalidRecord.Error()+": invalid "+test.field {
				t.Fatalf("validateRecord = %v, want invalid %s", err, test.field)
			}
		})
	}
}

func TestValidateRecordAcceptsResumableBackupStates(t *testing.T) {
	for _, state := range []protocolstate.ServerState{protocolstate.ServerReady, protocolstate.ServerDegraded, protocolstate.ServerStopped} {
		record := validRecordForTest()
		record.OperationType, record.RequestSnapshot.OperationType, record.BackupInitialLifecycleState = "server.backup", "server.backup", state
		if err := validateRecord(record); err != nil {
			t.Errorf("backup from %s = %v", state, err)
		}
	}
}
