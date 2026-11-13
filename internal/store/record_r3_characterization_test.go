package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// These characterization tests pin the record validators that the r3 store
// refactor splits into named stages: the exact field each rule reports, the
// order the rules run in, and the ErrInvalidRecord sentinel.

func storeR3AssertInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	if field == "" {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	want := ErrInvalidRecord.Error() + ": invalid " + field
	if !errors.Is(err, ErrInvalidRecord) || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestStoreR3ValidSecretReferenceCharacterization(t *testing.T) {
	valid := func() SecretReference {
		return SecretReference{SecretRef: "vault://kv/game", Purpose: "rcon", PermissionScope: []string{"read", "write"}, VersionRef: "v1"}
	}
	tests := []struct {
		name   string
		mutate func(*SecretReference)
		want   bool
	}{
		{"valid", func(*SecretReference) {}, true},
		{"no scope or version", func(r *SecretReference) { r.PermissionScope, r.VersionRef = nil, "" }, true},
		{"empty reference", func(r *SecretReference) { r.SecretRef = "" }, false},
		{"reference at limit", func(r *SecretReference) { r.SecretRef = "a://" + strings.Repeat("x", 1020) }, true},
		{"reference over limit", func(r *SecretReference) { r.SecretRef = "a://" + strings.Repeat("x", 1021) }, false},
		{"empty purpose", func(r *SecretReference) { r.Purpose = "" }, false},
		{"purpose at limit", func(r *SecretReference) { r.Purpose = strings.Repeat("p", 100) }, true},
		{"purpose over limit", func(r *SecretReference) { r.Purpose = strings.Repeat("p", 101) }, false},
		{"no scheme separator", func(r *SecretReference) { r.SecretRef = "vault/kv" }, false},
		{"empty scheme", func(r *SecretReference) { r.SecretRef = "://kv" }, false},
		{"single letter scheme", func(r *SecretReference) { r.SecretRef = "v://kv" }, true},
		{"uppercase scheme start", func(r *SecretReference) { r.SecretRef = "Vault://kv" }, false},
		{"digit scheme start", func(r *SecretReference) { r.SecretRef = "1vault://kv" }, false},
		{"scheme punctuation", func(r *SecretReference) { r.SecretRef = "a0+b.c-d://kv" }, true},
		{"scheme underscore", func(r *SecretReference) { r.SecretRef = "a_b://kv" }, false},
		{"scheme uppercase later", func(r *SecretReference) { r.SecretRef = "aB://kv" }, false},
		{"first separator decides", func(r *SecretReference) { r.SecretRef = "a://B_://x" }, true},
		{"version at limit", func(r *SecretReference) { r.VersionRef = strings.Repeat("v", 500) }, true},
		{"version over limit", func(r *SecretReference) { r.VersionRef = strings.Repeat("v", 501) }, false},
		{"empty scope", func(r *SecretReference) { r.PermissionScope = []string{"read", ""} }, false},
		{"scope at limit", func(r *SecretReference) { r.PermissionScope = []string{strings.Repeat("s", 160)} }, true},
		{"scope over limit", func(r *SecretReference) { r.PermissionScope = []string{strings.Repeat("s", 161)} }, false},
		{"duplicate scope", func(r *SecretReference) { r.PermissionScope = []string{"read", "write", "read"} }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ref := valid()
			test.mutate(&ref)
			if got := validSecretReference(ref); got != test.want {
				t.Fatalf("validSecretReference(%+v) = %v, want %v", ref, got, test.want)
			}
		})
	}
	record := runningRecordForValidation()
	record.RequestSnapshot.SecretRefs = []SecretReference{valid(), {SecretRef: "bad", Purpose: "rcon"}}
	storeR3AssertInvalidField(t, validateRecord(record), "request_snapshot.secret_refs")
}

func TestStoreR3ValidateRecordIdentityCharacterization(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name   string
		mutate func(*OperationRecord)
		field  string
	}{
		{"valid", func(*OperationRecord) {}, ""},
		{"operation ID at limit", func(r *OperationRecord) { r.OperationID = long(200) }, ""},
		{"empty operation ID", func(r *OperationRecord) { r.OperationID = "" }, "operation_id"},
		{"long operation ID", func(r *OperationRecord) { r.OperationID = long(201) }, "operation_id"},
		{"one-letter operation type", func(r *OperationRecord) { r.OperationType = "a" }, "operation_type"},
		{"uppercase operation type", func(r *OperationRecord) { r.OperationType = "Test.op" }, "operation_type"},
		{"underscore operation type", func(r *OperationRecord) { r.OperationType = "test_op" }, "operation_type"},
		{"long operation type", func(r *OperationRecord) { r.OperationType = long(161) }, "operation_type"},
		{"empty protocol version", func(r *OperationRecord) { r.ProtocolVersion = "" }, "protocol_version"},
		{"protocol version at limit", func(r *OperationRecord) { r.ProtocolVersion = long(100) }, ""},
		{"long protocol version", func(r *OperationRecord) { r.ProtocolVersion = long(101) }, "protocol_version"},
		{"empty schema revision", func(r *OperationRecord) { r.SchemaPackageRevision = "" }, ""},
		{"long schema revision", func(r *OperationRecord) { r.SchemaPackageRevision = long(201) }, "schema_package_revision"},
		{"empty server", func(r *OperationRecord) { r.Target.ServerID = "" }, "target"},
		{"long server", func(r *OperationRecord) { r.Target.ServerID = long(201) }, "target"},
		{"empty environment", func(r *OperationRecord) { r.Target.EnvironmentID = "" }, "target"},
		{"long environment", func(r *OperationRecord) { r.Target.EnvironmentID = long(201) }, "target"},
		{"empty actor", func(r *OperationRecord) { r.ActorID = "" }, "actor_id"},
		{"long actor", func(r *OperationRecord) { r.ActorID = long(201) }, "actor_id"},
		{"correlation at limit", func(r *OperationRecord) { r.CorrelationID = long(200) }, ""},
		{"long correlation", func(r *OperationRecord) { r.CorrelationID = long(201) }, "correlation_id"},
		{"empty idempotency key", func(r *OperationRecord) { r.IdempotencyKey = "" }, ""},
		{"idempotency key with space", func(r *OperationRecord) { r.IdempotencyKey = "a b" }, "idempotency_key"},
		{"idempotency key with control", func(r *OperationRecord) { r.IdempotencyKey = "a\x7f" }, "idempotency_key"},
		{"long idempotency key", func(r *OperationRecord) { r.IdempotencyKey = long(129) }, "idempotency_key"},
		{"valid fingerprint", func(r *OperationRecord) { r.RequestFingerprint = "sha256:" + strings.Repeat("0f", 32) }, ""},
		{"short fingerprint", func(r *OperationRecord) { r.RequestFingerprint = "sha256:abc" }, "request_fingerprint"},
		{"non-hex fingerprint", func(r *OperationRecord) { r.RequestFingerprint = "sha256:" + strings.Repeat("g", 64) }, "request_fingerprint"},
		{"wrong fingerprint prefix", func(r *OperationRecord) { r.RequestFingerprint = "sha512:" + strings.Repeat("0", 64) }, "request_fingerprint"},
		{"operation ID before type", func(r *OperationRecord) { r.OperationID, r.OperationType = "", "" }, "operation_id"},
		{"type before protocol", func(r *OperationRecord) { r.OperationType, r.ProtocolVersion = "", "" }, "operation_type"},
		{"protocol before schema", func(r *OperationRecord) { r.ProtocolVersion, r.SchemaPackageRevision = "", long(201) }, "protocol_version"},
		{"schema before target", func(r *OperationRecord) { r.SchemaPackageRevision, r.Target.ServerID = long(201), "" }, "schema_package_revision"},
		{"target before actor", func(r *OperationRecord) { r.Target.ServerID, r.ActorID = "", "" }, "target"},
		{"actor before correlation", func(r *OperationRecord) { r.ActorID, r.CorrelationID = "", long(201) }, "actor_id"},
		{"correlation before key", func(r *OperationRecord) { r.CorrelationID, r.IdempotencyKey = long(201), "a b" }, "correlation_id"},
		{"key before fingerprint", func(r *OperationRecord) { r.IdempotencyKey, r.RequestFingerprint = "a b", "bad" }, "idempotency_key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := runningRecordForValidation()
			test.mutate(&record)
			storeR3AssertInvalidField(t, validateRecordIdentity(record), test.field)
		})
	}
}

func TestStoreR3ValidateStepIdentityCharacterization(t *testing.T) {
	const dispatch = "backup_initial_lifecycle_state must precede dispatch"
	tests := []struct {
		name        string
		backup      bool
		backupState protocolstate.ServerState
		status      protocolstate.StepStatus
		attempt     int
		mutate      func(*OperationStep)
		field       string
	}{
		{name: "backup pending without state", backup: true, status: protocolstate.StepPending},
		{name: "backup skipped without state", backup: true, status: protocolstate.StepSkipped},
		{name: "backup cancelled without state", backup: true, status: protocolstate.StepCancelled},
		{name: "backup failed without attempt or state", backup: true, status: protocolstate.StepFailed, field: "steps[2].attempt"},
		{name: "backup pending with attempt", backup: true, status: protocolstate.StepPending, attempt: 1, field: dispatch},
		{name: "backup negative attempt", backup: true, status: protocolstate.StepPending, attempt: -1, field: dispatch},
		{name: "backup failed with attempt", backup: true, status: protocolstate.StepFailed, attempt: 1, field: dispatch},
		{name: "backup running", backup: true, status: protocolstate.StepRunning, field: dispatch},
		{name: "backup succeeded", backup: true, status: protocolstate.StepSucceeded, field: dispatch},
		{name: "backup waiting", backup: true, status: protocolstate.StepWaiting, field: dispatch},
		{name: "backup outcome unknown", backup: true, status: protocolstate.StepOutcomeUnknown, field: dispatch},
		{name: "backup dispatch before step ID", backup: true, status: protocolstate.StepRunning, mutate: func(s *OperationStep) { s.StepID = "" }, field: dispatch},
		{name: "backup with state running", backup: true, backupState: protocolstate.ServerReady, status: protocolstate.StepRunning, attempt: 1},
		{name: "backup with state attempt rule", backup: true, backupState: protocolstate.ServerReady, status: protocolstate.StepRunning, field: "steps[2].attempt"},
		{name: "pending", status: protocolstate.StepPending},
		{name: "running", status: protocolstate.StepRunning, attempt: 1},
		{name: "failed later attempt", status: protocolstate.StepFailed, attempt: 3},
		{name: "skipped without attempt", status: protocolstate.StepSkipped},
		{name: "skipped with attempt", status: protocolstate.StepSkipped, attempt: 2},
		{name: "cancelled without attempt", status: protocolstate.StepCancelled},
		{name: "cancelled with attempt", status: protocolstate.StepCancelled, attempt: 1},
		{name: "empty step ID", status: protocolstate.StepPending, mutate: func(s *OperationStep) { s.StepID = "" }, field: "steps[2]"},
		{name: "long step ID", status: protocolstate.StepPending, mutate: func(s *OperationStep) { s.StepID = strings.Repeat("s", 201) }, field: "steps[2]"},
		{name: "invalid step type", status: protocolstate.StepPending, mutate: func(s *OperationStep) { s.StepType = "Request" }, field: "steps[2]"},
		{name: "long step type", status: protocolstate.StepPending, mutate: func(s *OperationStep) { s.StepType = strings.Repeat("s", 161) }, field: "steps[2]"},
		{name: "step type with capability characters", status: protocolstate.StepPending, mutate: func(s *OperationStep) { s.StepType = "game_ready-check.v1" }},
		{name: "unknown status", status: "paused", field: "steps[2]"},
		{name: "negative attempt", status: protocolstate.StepPending, attempt: -1, field: "steps[2]"},
		{name: "pending with attempt", status: protocolstate.StepPending, attempt: 1, field: "steps[2].attempt"},
		{name: "running without attempt", status: protocolstate.StepRunning, field: "steps[2].attempt"},
		{name: "waiting without attempt", status: protocolstate.StepWaiting, field: "steps[2].attempt"},
		{name: "outcome unknown without attempt", status: protocolstate.StepOutcomeUnknown, field: "steps[2].attempt"},
		{name: "succeeded without attempt", status: protocolstate.StepSucceeded, field: "steps[2].attempt"},
		{name: "failed without attempt", status: protocolstate.StepFailed, field: "steps[2].attempt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := runningRecordForValidation()
			if test.backup {
				record.OperationType = "server.backup"
			}
			record.BackupInitialLifecycleState = test.backupState
			step := OperationStep{StepID: "step", StepType: "request.validate", Status: test.status, Attempt: test.attempt}
			if test.mutate != nil {
				test.mutate(&step)
			}
			storeR3AssertInvalidField(t, validateStepIdentity(record, 2, step), test.field)
		})
	}
}

func TestStoreR3ValidateStepInputCharacterization(t *testing.T) {
	object := json.RawMessage(`{}`)
	bindings := []inputbinding.Binding{{TargetPointer: "/v", SourceStepID: "source", SourcePointer: "/value"}}
	adapter := func(s *OperationStep) {
		s.AdapterID, s.CapabilityName, s.CapabilityVersion, s.Input = "game", "game.readiness.check", "1.0.0", object
	}
	tests := []struct {
		name   string
		mutate func(*OperationStep)
		field  string
	}{
		{"plain step", func(*OperationStep) {}, ""},
		{"plain step with object input", func(s *OperationStep) { s.Input = object }, ""},
		{"plain step with array input", func(s *OperationStep) { s.Input = json.RawMessage(`[]`) }, "steps[1].input"},
		{"plain step with adapter only", func(s *OperationStep) { s.AdapterID = "game" }, ""},
		{"adapter step", adapter, ""},
		{"invalid capability name", func(s *OperationStep) { adapter(s); s.CapabilityName = "Game" }, "steps[1].capability"},
		{"long capability name", func(s *OperationStep) { adapter(s); s.CapabilityName = strings.Repeat("c", 161) }, "steps[1].capability"},
		{"capability without adapter", func(s *OperationStep) { adapter(s); s.AdapterID = "" }, "steps[1].capability"},
		{"capability without version", func(s *OperationStep) { adapter(s); s.CapabilityVersion = "" }, "steps[1].capability"},
		{"version without capability", func(s *OperationStep) { adapter(s); s.CapabilityName = "" }, "steps[1].capability"},
		{"version without adapter or capability", func(s *OperationStep) { s.CapabilityVersion = "1.0.0" }, "steps[1].capability"},
		{"capability checked before input", func(s *OperationStep) { adapter(s); s.AdapterID, s.Input = "", nil }, "steps[1].capability"},
		{"adapter step without input", func(s *OperationStep) { adapter(s); s.Input = nil }, "steps[1].input"},
		{"adapter step with null input", func(s *OperationStep) { adapter(s); s.Input = json.RawMessage(`null`) }, "steps[1].input"},
		{"bindings without capability", func(s *OperationStep) { s.Input, s.InputBindings = object, bindings }, "steps[1].input_bindings"},
		{"bindings without capability or input", func(s *OperationStep) { s.InputBindings = bindings }, "steps[1].input_bindings"},
		{"input checked before bindings", func(s *OperationStep) { s.Input, s.InputBindings = json.RawMessage(`[]`), bindings }, "steps[1].input"},
		{"bound step", func(s *OperationStep) { adapter(s); s.InputBindings = bindings }, ""},
		{"bound step with resolved input", func(s *OperationStep) { adapter(s); s.InputBindings, s.ResolvedInput = bindings, object }, ""},
		{"bound step with array resolved input", func(s *OperationStep) {
			adapter(s)
			s.InputBindings, s.ResolvedInput = bindings, json.RawMessage(`[]`)
		}, "steps[1].resolved_input"},
		{"resolved input without bindings", func(s *OperationStep) { adapter(s); s.ResolvedInput = object }, "steps[1].resolved_input has no bindings"},
		{"array resolved input without bindings", func(s *OperationStep) { adapter(s); s.ResolvedInput = json.RawMessage(`[]`) }, "steps[1].resolved_input"},
		{"dispatched bound step without resolved input", func(s *OperationStep) { adapter(s); s.InputBindings, s.Attempt = bindings, 1 }, "steps[1].resolved_input must be persisted before dispatch"},
		{"dispatched bound step with resolved input", func(s *OperationStep) {
			adapter(s)
			s.InputBindings, s.Attempt, s.ResolvedInput = bindings, 1, object
		}, ""},
		{"dispatched unbound step", func(s *OperationStep) { adapter(s); s.Attempt = 1 }, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step := OperationStep{StepID: "step", StepType: "request.validate", Status: protocolstate.StepPending}
			test.mutate(&step)
			storeR3AssertInvalidField(t, validateStepInput(OperationRecord{}, 1, step), test.field)
		})
	}
}
