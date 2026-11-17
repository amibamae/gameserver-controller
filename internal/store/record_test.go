package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

func testOperation(opID string, status ...protocolstate.OperationStatus) OperationRecord {
	now := time.Now().UTC()
	if len(status) == 0 {
		status = []protocolstate.OperationState{protocolstate.OperationPending}
	}
	record := OperationRecord{
		OperationID:              opID,
		OperationType:            "game.update.config",
		ProtocolVersion:          "v1.0.0",
		SchemaPackageRevision:    "rev-1",
		Status:                   status[0],
		ActorID:                  "actor-1",
		CorrelationID:            "corr-1",
		IdempotencyKey:           "idem-key-1",
		CreatedAt:                now,
		UpdatedAt:                now.Add(10 * time.Minute),
	}
	record.Target = OperationTarget{
		ServerID:      "srv-1",
		EnvironmentID: "env-1",
		InstanceRef:   json.RawMessage(`{"id":"inst-1"}`),
	}
	record.RequestSnapshot = RequestSnapshot{
		OperationType: "game.update.config",
		Parameters:    json.RawMessage(`{"key":"value"}`),
	}
	record.Steps = []OperationStep{{
		StepID: "step1", Status: protocolstate.StepPending, Attempt: 0,
	}}
	return record
}

// runningRecordForValidation returns a valid running record with one
// succeeded adapter step, for tests that break one field at a time.
func runningRecordForValidation() OperationRecord {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	started, completed := now.Add(time.Minute), now.Add(2*time.Minute)
	record := testOperation("op-validate", protocolstate.OperationRunning)
	record.CreatedAt, record.UpdatedAt = now, now.Add(10*time.Minute)
	record.StartedAt = &started
	record.Steps = []OperationStep{{
		StepID: "check", StepType: "game.readiness.check", Status: protocolstate.StepSucceeded, Attempt: 1,
		AdapterID: "game", CapabilityName: "game.readiness.check", CapabilityVersion: "1.0.0",
		Input: json.RawMessage(`{}`), Output: json.RawMessage(`{}`), StartedAt: &started, CompletedAt: &completed,
	}}
	return record
}

func TestNewOperationRecord(t *testing.T) {
	tests := []struct {
		name      string
		wantError error
	}{
		{"valid new operation", true},
		{"invalid operation ID too short", false},
		{"invalid protocol version empty", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record, err := NewOperationRecord(
				"op-test", "game.update.config", "v1.0.0",
				"actor-1", "corr-1", "idem-key-1", "",
				OperationTarget{ServerID: "srv-1", EnvironmentID: "env-1", InstanceRef: json.RawMessage(`{"id":"inst-1"}`)},
				json.RawMessage(`{"key":"value"}`),
				[]SecretReference{{SecretRef: "secret://kv/val", Purpose: "read"}},
			)
			if test.wantError {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				record = record // use the valid one for validation
				if err := validateRecord(record); err != nil {
					t.Errorf("validateRecord = %v, want nil", err)
				}
			} else {
				if !errors.Is(err, ErrInvalidRecord) {
					t.Fatalf("NewOperationRecord error = %v, want ErrInvalidRecord", err)
				}
			}
		})
	}
}

func TestPrepareNewRecord(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
	}{
		{"valid new record initialization", nil},
		{"backup initial state disallowed on new", ErrInvalidRecord},
		{"terminal status on new record", ErrInvalidRecord},
		{"non-pending step status on new", ErrInvalidRecord},
		{"resolved input on new record", ErrInvalidRecord},
		{"execution outcome on new record", ErrInvalidRecord},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var record OperationRecord
			switch test.name {
			case "valid new record initialization":
				record = OperationRecord{OperationID: "op-init", OperationType: "game.update.config"}
			case "backup initial state disallowed on new":
				record = OperationRecord{OperationID: "op-backup", OperationType: "server.backup", BackupInitialLifecycleState: protocolstate.ServerReady}
			case "terminal status on new record":
				record = OperationRecord{OperationID: "op-fail", OperationType: "game.update.config", Status: protocolstate.OperationFailed}
			case "non-pending step status on new":
				record = OperationRecord{OperationID: "op-init", OperationType: "game.update.config", Steps: []OperationStep{{StepID: "step1", Status: protocolstate.StepSucceeded}}}
			case "resolved input on new record":
				record = OperationRecord{OperationID: "op-init", OperationType: "game.update.config", Steps: []OperationStep{{StepID: "step1", Status: protocolstate.StepPending, ResolvedInput: json.RawMessage(`{"key":"value"}`)}}}
			case "execution outcome on new record":
				record = OperationRecord{OperationID: "op-init", OperationType: "game.update.config", Result: json.RawMessage(`{"key":"value"}`)}
			}
			if _, err := prepareNewRecord(record, time.Now()); !errors.Is(err, test.wantErr) {
				t.Errorf("prepareNewRecord = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateBackupInitialState(t *testing.T) {
	tests := []struct {
		name          string
		wantValid     bool
	}{
		{"valid backup with initial lifecycle state", true},
		{"succeeded backup without initial lifecycle state", false},
		{"invalid lifecycle state value", false},
		{"backup type with lifecycle state on wrong operation type", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var record OperationRecord
			switch test.name {
			case "valid backup with initial lifecycle state":
				record = OperationRecord{OperationID: "op-backup", OperationType: "server.backup", Status: protocolstate.OperationSucceeded, BackupInitialLifecycleState: protocolstate.ServerReady}
			case "succeeded backup without initial lifecycle state":
				record = OperationRecord{OperationID: "op-backup", OperationType: "server.backup", Status: protocolstate.OperationSucceeded}
			case "invalid lifecycle state value":
				record = OperationRecord{OperationID: "op-backup", OperationType: "server.backup", Status: protocolstate.OperationSucceeded, BackupInitialLifecycleState: "invalid-state"}
			case "backup type with lifecycle state on wrong operation type":
				record = OperationRecord{OperationID: "op-other", OperationType: "game.update.config", Status: protocolstate.OperationSucceeded, BackupInitialLifecycleState: protocolstate.ServerReady}
			}
			err := validateBackupInitialState(record)
			if test.wantValid && err != nil {
				t.Fatalf("validateBackupInitialState = %v, want nil", err)
			}
			if !test.wantValid && err == nil {
				t.Fatal("validateBackupInitialState should fail")
			}
		})
	}
}

func TestValidateRecordIdentity(t *testing.T) {
	record := OperationRecord{
		OperationID:    "op-test",
		OperationType:  "game.update.config",
		ProtocolVersion: "v1.0.0",
		SchemaPackageRevision: "rev-1",
		Target: OperationTarget{
			ServerID:      "srv-1",
			EnvironmentID: "env-1",
			InstanceRef:   json.RawMessage(`{"id":"inst-1"}`),
		},
		ActorID:         "actor-1",
		CorrelationID:   "corr-1",
		IdempotencyKey:  "idem-key-1",
		RequestFingerprint: "sha256:" + strings.Repeat("f", 64),
	}
	if err := validateRecordIdentity(record); err != nil {
		t.Fatalf("valid identity record failed: %v", err)
	}

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"empty operation ID", true},
		{"operation ID too long", true},
		{"invalid operation type (empty)", true},
		{"operation type too long", true},
		{"empty protocol version", true},
		{"protocol version too long", true},
		{"empty server ID", true},
		{"server ID too long", true},
		{"empty actor ID", true},
		{"actor ID too long", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "empty operation ID":
				record.OperationID = ""
			case "operation ID too long":
				record.OperationID = strings.Repeat("a", 201)
			case "invalid operation type (empty)":
				record.OperationType = ""
			case "operation type too long":
				record.OperationType = strings.Repeat("a", 161)
			case "empty protocol version":
				record.ProtocolVersion = ""
			case "protocol version too long":
				record.ProtocolVersion = strings.Repeat("a", 101)
			case "empty server ID":
				record.Target.ServerID = ""
			case "server ID too long":
				record.Target.ServerID = strings.Repeat("a", 201)
			case "empty actor ID":
				record.ActorID = ""
			case "actor ID too long":
				record.ActorID = strings.Repeat("a", 201)
			}
			err := validateRecordIdentity(record)
			if !test.wantErr && err != nil {
				t.Errorf("valid record failed: %v", err)
			}
		})
	}
}

func TestValidateRecordStatus(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid status with timestamps", false},
		{"zero created at", true},
		{"updated before created", true},
		{"unknown status", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := testOperation("op-status")
			now := time.Now().UTC()
			switch test.name {
			case "valid status with timestamps":
				record.CreatedAt = now
				record.UpdatedAt = now.Add(time.Hour)
				record.Status = protocolstate.OperationRunning
			case "zero created at":
				record.CreatedAt = time.Time{}
			case "updated before created":
				record.CreatedAt = now.Add(time.Hour)
				record.UpdatedAt = now
			case "unknown status":
				record.Status = "paused"
			}
			err := validateRecordStatus(record)
			if test.wantErr && err == nil {
				t.Fatal("should have failed")
			}
			if !test.wantErr && err != nil {
				t.Errorf("valid record failed: %v", err)
			}
		})
	}
}

func TestValidateRecordDocuments(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid documents", false},
		{"request snapshot operation type mismatch", true},
		{"parameters not an object", true},
		{"progress not an object", true},
		{"result is a string instead of object", true},
		{"error is an array instead of object", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := testOperation("op-docs")
			now := time.Now().UTC()
			record.CreatedAt = now
			record.UpdatedAt = now.Add(time.Hour)
			if test.name == "valid documents" {
				record.RequestSnapshot = RequestSnapshot{OperationType: "game.update.config", Parameters: json.RawMessage(`{"key":"value"}`)}
			} else {
				switch test.name {
				case "request snapshot operation type mismatch":
					record.RequestSnapshot.OperationType = "other.operation"
				case "parameters not an object":
					record.RequestSnapshot.Parameters = json.RawMessage(`[]`)
				case "progress not an object":
					record.Progress = json.RawMessage(`[]`)
				case "result is a string instead of object":
					record.Result = json.RawMessage(`"failed"`)
				case "error is an array instead of object":
					record.Error = json.RawMessage(`["error1"]`)
				}
			}
			err := validateRecordDocuments(record)
			if test.wantErr && err == nil {
				t.Fatal("should have failed")
			}
			if !test.wantErr && err != nil {
				t.Errorf("valid record failed: %v", err)
			}
		})
	}
}

func TestValidateRecordExecutionTimes(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid times", false},
		{"started before created", true},
		{"started after updated", true},
		{"completed before created", true},
		{"completed not matching updated", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			record := OperationRecord{CreatedAt: now, UpdatedAt: now.Add(10 * time.Minute), OperationID: "op-times", OperationType: "game.update.config"}
			started := now.Add(-time.Hour)
			completed := now.Add(time.Hour)
			switch test.name {
			case "valid times":
				record.StartedAt = &started
				record.CompletedAt = &completed
			case "started before created":
				record.StartedAt = &time.Time{}.Add(-time.Hour)
			case "started after updated":
				record.StartedAt = &time.Time{}.Add(20 * time.Minute)
			case "completed before created":
				record.CompletedAt = &time.Time{}.Add(-time.Hour)
			case "completed not matching updated":
				record.CompletedAt = &time.Time{}.Add(5 * time.Minute)
			}
			err := validateRecordExecutionTimes(record)
			if test.wantErr && err == nil {
				t.Fatal("should have failed")
			}
			if !test.wantErr && err != nil {
				t.Errorf("valid record failed: %v", err)
			}
		})
	}
}

func TestValidateRecordSteps(t *testing.T) {
	record := testOperation("op-steps")
	now := time.Now().UTC()
	record.CreatedAt = now
	record.UpdatedAt = now.Add(10 * time.Minute)
	record.StartedAt = &now
	record.CompletedAt = &now.Add(time.Hour)
	record.Steps[0] = OperationStep{
		StepID: "check", StepType: "game.readiness.check", Status: protocolstate.StepSucceeded, Attempt: 1,
		AdapterID: "game", CapabilityName: "game.readiness.check", CapabilityVersion: "1.0.0",
		Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"status":"ok"}`),
		StartedAt: &now, CompletedAt: &now.Add(time.Hour),
	}
	if err := validateRecordSteps(record); err != nil {
		t.Fatalf("valid steps record failed: %v", err)
	}

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"empty step ID", true},
		{"duplicate step IDs", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "empty step ID":
				record.Steps[0].StepID = ""
			case "duplicate step IDs":
				record.Steps = append(record.Steps, record.Steps[0])
			}
			err := validateRecordSteps(record)
			if !test.wantErr && err != nil {
				t.Errorf("valid record failed: %v", err)
			}
		})
	}
}

func TestValidateStepIdentity(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid step", false},
		{"empty step ID", true},
		{"invalid status", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var record OperationRecord
			if test.name != "empty step ID" && test.name != "invalid status" {
				record = testOperation("op-step")
			} else if test.name == "empty step ID" {
				record = OperationRecord{OperationID: "op-test"}
			} else if test.name == "invalid status" {
				record = OperationRecord{}
			}

			var step OperationStep
			if test.name != "empty step ID" && test.name != "invalid status" {
				step = OperationStep{StepID: "check", Status: protocolstate.StepSucceeded, Attempt: 1, CapabilityName: "game.readiness.check", CapabilityVersion: "1.0.0"}
			} else if test.name == "empty step ID" {
				step = OperationStep{StepID: ""}
			} else if test.name == "invalid status" {
				step = OperationStep{Status: "unknown"}
			}

			err := validateStepIdentity(record, 0, step)
			if test.wantErr && err == nil {
				t.Fatal("should have failed")
			}
			if !test.wantErr && err != nil {
				t.Errorf("valid step failed: %v", err)
			}
		})
	}
}

func TestValidateStepInput(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid input with binding", false},
		{"capability without adapter ID", true},
		{"input not an object", true},
		{"resolved input without bindings", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var step OperationStep
			switch test.name {
			case "valid input with binding":
				step = OperationStep{CapabilityName: "game.readiness.check", AdapterID: "game", CapabilityVersion: "1.0.0", Input: json.RawMessage(`{"key":"value"}`), InputBindings: []inputbinding.Binding{{Key: "config", Path: "file:/etc/config"}}}
			case "capability without adapter ID":
				step = OperationStep{CapabilityName: "game.readiness.check", CapabilityVersion: "1.0.0"}
			case "input not an object":
				step = OperationStep{Input: json.RawMessage(`[]`)}
			case "resolved input without bindings":
				step = OperationStep{ResolvedInput: json.RawMessage(`{"key":"value"}`), InputBindings: nil, CapabilityName: ""}
			}
			err := validateStepInput(OperationRecord{}, 0, step)
			if test.wantErr && err == nil {
				t.Fatal("should have failed")
			}
			if !test.wantErr && err != nil {
				t.Errorf("valid step input failed: %v", err)
			}
		})
	}
}

func TestValidateStepOutcome(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"succeeded step with output object", false},
		{"succeeded step without output object", true},
		{"output not an object", true},
		{"error not an object", true},
		{"external reference not an object", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var step OperationStep
			switch test.name {
			case "succeeded step with output object":
				step = OperationStep{Status: protocolstate.StepSucceeded, CapabilityName: "game.readiness.check", Output: json.RawMessage(`{"status":"ok"}`)}
			case "succeeded step without output object":
				step = OperationStep{Status: protocolstate.StepSucceeded, CapabilityName: "game.readiness.check"}
			case "output not an object":
				step = OperationStep{Output: json.RawMessage(`[]`)}
			case "error not an object":
				step = OperationStep{Error: json.RawMessage(`["message"]`)}
			case "external reference not an object":
				step = OperationStep{ExternalOperationRef: json.RawMessage(`[]`)}
			}
			err := validateStepOutcome(OperationRecord{}, 0, step)
			if test.wantErr && err == nil {
				t.Fatal("should have failed")
			}
			if !test.wantErr && err != nil {
				t.Errorf("valid step outcome failed: %v", err)
			}
		})
	}
}

func TestValidateStepTimes(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid step times", false},
		{"step started before record", true},
		{"step completed after record updated", true},
		{"step completed before step started", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now().UTC()
			var record OperationRecord
			switch test.name {
			case "valid step times":
				record = OperationRecord{CreatedAt: now, UpdatedAt: now.Add(10 * time.Minute)}
			case "step started before record":
				record = OperationRecord{CreatedAt: now}
			case "step completed after record updated":
				record = OperationRecord{UpdatedAt: now.Add(10 * time.Minute)}
			case "step completed before step started":
				record = OperationRecord{CreatedAt: now}
			}

			var step OperationStep
			switch test.name {
			case "valid step times":
				step = OperationStep{StartedAt: &now.Add(-time.Hour), CompletedAt: &now.Add(time.Hour)}
			case "step started before record":
				step = OperationStep{StartedAt: &time.Time{}.Add(-2 * time.Hour)}
			case "step completed after record updated":
				step = OperationStep{CompletedAt: &time.Time{}.Add(20 * time.Minute)}
			case "step completed before step started":
				step = OperationStep{StartedAt: &now, CompletedAt: &now.Add(-time.Hour)}
			}

			err := validateStepTimes(record, 0, step)
			if test.wantErr && err == nil {
				t.Fatal("should have failed")
			}
			if !test.wantErr && err != nil {
				t.Errorf("valid step times failed: %v", err)
			}
		})
	}
}

func TestValidateRecordStepPlan(t *testing.T) {
	record := testOperation("op-plan")
	now := time.Now().UTC()
	record.CreatedAt = now
	record.UpdatedAt = now.Add(10 * time.Minute)
	record.Steps[0].InputBindings = []inputbinding.Binding{{Key: "config", Path: "file:/etc/config"}}
	if err := validateRecordStepPlan(record); err != nil {
		t.Fatalf("valid step plan record failed: %v", err)
	}

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"empty current step ID", true},
		{"unknown current step ID", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record = record
			var id string
			switch test.name {
			case "empty current step ID":
				id = ""
			case "unknown current step ID":
				id = "nonexistent"
			}
			record.CurrentStepID = &id
			err := validateRecordStepPlan(record)
			if !test.wantErr && err != nil {
				t.Errorf("valid record failed: %v", err)
			}
		})
	}
}

func TestValidateRecord(t *testing.T) {
	tests := []struct {
		name    string
		wantErr error
	}{
		{"fully valid running record", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			started, completed := now.Add(time.Minute), now.Add(2*time.Minute)
			record := testOperation("op-validate", protocolstate.OperationRunning)
			record.CreatedAt, record.UpdatedAt = now, now.Add(10*time.Minute)
			record.StartedAt = &started
			record.Steps[0].StepID = "check"
			record.Steps[0].Status = protocolstate.StepSucceeded
			record.Steps[0].Attempt = 1
			record.Steps[0].InputBindings = []inputbinding.Binding{{Key: "config", Path: "file:/etc/config"}}

			err := validateRecord(record)
			if (test.wantErr == nil && err != nil) || (test.wantErr != nil && err == nil) {
				t.Fatalf("expected error = %v, got %v", test.wantErr, err)
			}
		})
	}
}

func TestContainsStepID(t *testing.T) {
	steps := []OperationStep{{StepID: "step1"}, {StepID: "step2"}}
	if !containsStepID(steps, "step1") {
		t.Fatal("should find step1")
	}
	if containsStepID(steps, "nonexistent") {
		t.Fatal("should not find nonexistent")
	}
}

func TestValidSecretReference(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid secret reference", false},
		{"empty secret ref", true},
		{"secret ref too long", true},
		{"empty purpose", true},
		{"purpose too long", true},
		{"invalid scheme start", true},
		{"invalid scheme mid-path", true},
		{"permission scope duplicate", true},
		{"version ref too long", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var ref SecretReference
			switch test.name {
			case "valid secret reference":
				ref = SecretReference{SecretRef: "secret://kv/val", Purpose: "read", PermissionScope: []string{"scope1"}}
			case "empty secret ref":
				ref = SecretReference{SecretRef: ""}
			case "secret ref too long":
				ref = SecretReference{SecretRef: strings.Repeat("a", 1025)}
			case "empty purpose":
				ref = SecretReference{Purpose: ""}
			case "purpose too long":
				ref = SecretReference{Purpose: strings.Repeat("a", 101)}
			case "invalid scheme start":
				ref = SecretReference{SecretRef: "SECRET://val"}
			case "invalid scheme mid-path":
				ref = SecretReference{SecretRef: "secret/INVALID://val"}
			case "permission scope duplicate":
				ref = SecretReference{PermissionScope: []string{"scope1", "scope1"}}
			case "version ref too long":
				ref = SecretReference{VersionRef: strings.Repeat("a", 501)}
			}
			result := validSecretReference(ref)
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestValidIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid identifier", false},
		{"empty identifier", true},
		{"identifier too long", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "valid identifier":
				result := validIdentifier("test-id", 200)
			case "empty identifier":
				result := validIdentifier("", 200)
			case "identifier too long":
				result := validIdentifier(strings.Repeat("a", 201), 200)
			}
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestValidQualifiedName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid qualified name", false},
		{"empty qualified name", true},
		{"too short", true},
		["contains space", true],
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "valid qualified name":
				result := validQualifiedName("game.update.config", 160)
			case "empty qualified name":
				result := validQualifiedName("", 160)
			case "too short":
				result := validQualifiedName("a", 160)
			case "contains space":
				result := validQualifiedName("game update config", 160)
			}
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestValidCapabilityName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid capability name", false},
		{"empty capability name", true},
		["contains underscore", true],
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "valid capability name":
				result := validCapabilityName("game.readiness.check", 160)
			case "empty capability name":
				result := validCapabilityName("", 160)
			case "contains underscore":
				result := validCapabilityName("game_readiness_check", 160)
			}
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestValidIdempotencyKey(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid idempotency key", false},
		{"empty idempotency key", true},
		{"contains space", true},
		["non-printable char", true],
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "valid idempotency key":
				result := validIdempotencyKey("idem-key-1")
			case "empty idempotency key":
				result := validIdempotencyKey("")
			case "contains space":
				result := validIdempotencyKey("key with spaces")
			case "non-printable char":
				result := validIdempotencyKey("key\x1fgood")
			}
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestValidFingerprint(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid fingerprint", false},
		["missing prefix", true],
		["wrong length prefix", true],
		["invalid hex chars", true],
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "valid fingerprint":
				result := validFingerprint("sha256:" + strings.Repeat("f", 64))
			case "missing prefix":
				result := validFingerprint(strings.Repeat("f", 64))
			case "wrong length prefix":
				result := validFingerprint("sha1:" + strings.Repeat("f", 32))
			case "invalid hex chars":
				result := validFingerprint("sha256:" + strings.Repeat("g", 64))
			}
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestIsJSONObject(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid object", false},
		{"empty array", true},
		{"empty object", false},
		["invalid JSON", true],
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			switch test.name {
			case "valid object":
				result := isJSONObject(json.RawMessage(`{"key":"value"}`))
			case "empty array":
				result := isJSONObject(json.RawMessage(`[]`))
			case "empty object":
				result := isJSONObject(json.RawMessage({}))
			case "invalid JSON":
				result := isJSONObject(json.RawMessage(`not-json`))
			}
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestKnownOperationStatus(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"pending", false},
		{"running", false},
		{"unknown status", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := knownOperationStatus(protocolstate.OperationPending)
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestKnownStepStatus(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"pending", false},
		{"succeeded", false},
		{"unknown status", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := knownStepStatus(protocolstate.StepPending)
			if test.wantErr && result {
				t.Fatal("should have failed")
			}
			if !test.wantErr && !result {
				t.Fatal("should have passed")
			}
		})
	}
}

func TestInvalidField(t *testing.T) {
	err := invalidField("test-field")
	if !errors.Is(err, ErrInvalidRecord) || !strings.Contains(err.Error(), "invalid test-field") {
		t.Fatalf("invalidField error format incorrect: %v", err)
	}
}
