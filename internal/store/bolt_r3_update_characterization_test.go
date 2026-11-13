package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// These characterization tests pin sameAcceptedRequest, validateStepChanges,
// and UpdateOperation: exact errors and the stored bytes after each call.

func TestStoreR3SameAcceptedRequestCharacterization(t *testing.T) {
	deadline := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	base := func() OperationRecord {
		record := testOperation("same")
		record.BackupInitialLifecycleState = protocolstate.ServerReady
		record.CorrelationID = "corr"
		record.RequestFingerprint = "sha256:" + strings.Repeat("a", 64)
		record.Target.InstanceRef = json.RawMessage(`{"id":"i-1"}`)
		record.RequestSnapshot.SecretRefs = []SecretReference{{SecretRef: "vault://a", Purpose: "p", PermissionScope: []string{"r", "w"}, VersionRef: "v"}}
		d := deadline
		record.Deadline = &d
		return record
	}
	tests := []struct {
		name   string
		mutate func(*OperationRecord)
		want   bool
	}{
		{"identical", func(*OperationRecord) {}, true},
		{"mutable fields differ", func(r *OperationRecord) {
			r.Status = protocolstate.OperationRunning
			r.Progress = json.RawMessage(`{"p":1}`)
			r.Steps = nil
			r.UpdatedAt = deadline
		}, true},
		{"deadline in another zone", func(r *OperationRecord) { d := deadline.In(time.FixedZone("x", 3600)); r.Deadline = &d }, true},
		{"backup state", func(r *OperationRecord) { r.BackupInitialLifecycleState = "" }, false},
		{"operation ID", func(r *OperationRecord) { r.OperationID = "other" }, false},
		{"operation type", func(r *OperationRecord) { r.OperationType = "other.op" }, false},
		{"protocol version", func(r *OperationRecord) { r.ProtocolVersion = "x" }, false},
		{"schema revision", func(r *OperationRecord) { r.SchemaPackageRevision = "x" }, false},
		{"server", func(r *OperationRecord) { r.Target.ServerID = "x" }, false},
		{"environment", func(r *OperationRecord) { r.Target.EnvironmentID = "x" }, false},
		{"instance ref bytes", func(r *OperationRecord) { r.Target.InstanceRef = json.RawMessage(`{"id": "i-1"}`) }, false},
		{"actor", func(r *OperationRecord) { r.ActorID = "x" }, false},
		{"correlation", func(r *OperationRecord) { r.CorrelationID = "x" }, false},
		{"idempotency key", func(r *OperationRecord) { r.IdempotencyKey = "x" }, false},
		{"fingerprint", func(r *OperationRecord) { r.RequestFingerprint = "x" }, false},
		{"snapshot type", func(r *OperationRecord) { r.RequestSnapshot.OperationType = "x" }, false},
		{"snapshot parameters", func(r *OperationRecord) { r.RequestSnapshot.Parameters = json.RawMessage(`{"plan":"large"}`) }, false},
		{"secret count", func(r *OperationRecord) { r.RequestSnapshot.SecretRefs = nil }, false},
		{"secret ref", func(r *OperationRecord) { r.RequestSnapshot.SecretRefs[0].SecretRef = "vault://b" }, false},
		{"secret purpose", func(r *OperationRecord) { r.RequestSnapshot.SecretRefs[0].Purpose = "q" }, false},
		{"secret version", func(r *OperationRecord) { r.RequestSnapshot.SecretRefs[0].VersionRef = "w" }, false},
		{"secret scope count", func(r *OperationRecord) { r.RequestSnapshot.SecretRefs[0].PermissionScope = []string{"r"} }, false},
		{"secret scope value", func(r *OperationRecord) { r.RequestSnapshot.SecretRefs[0].PermissionScope = []string{"r", "x"} }, false},
		{"deadline removed", func(r *OperationRecord) { r.Deadline = nil }, false},
		{"deadline moved", func(r *OperationRecord) { d := deadline.Add(time.Second); r.Deadline = &d }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next := base()
			test.mutate(&next)
			if got := sameAcceptedRequest(base(), next); got != test.want {
				t.Fatalf("sameAcceptedRequest = %v, want %v", got, test.want)
			}
		})
	}
	left, right := base(), base()
	left.Deadline, right.Deadline = nil, nil
	left.RequestSnapshot.SecretRefs, right.RequestSnapshot.SecretRefs = nil, []SecretReference{}
	left.Target.InstanceRef, right.Target.InstanceRef = nil, json.RawMessage{}
	if !sameAcceptedRequest(left, right) {
		t.Fatal("nil and empty request values must compare equal")
	}
}

func TestStoreR3ValidateStepChangesCharacterization(t *testing.T) {
	const (
		running = protocolstate.OperationRunning
		pending = protocolstate.OperationPending
	)
	binding := []inputbinding.Binding{{TargetPointer: "/v", SourceStepID: "source", SourcePointer: "/value"}}
	steps := func() []OperationStep {
		return []OperationStep{
			{StepID: "source", StepType: "game.source", Status: protocolstate.StepSucceeded, Attempt: 1, AdapterID: "game", CapabilityName: "game.source", CapabilityVersion: "1",
				Input: json.RawMessage(`{}`), Output: json.RawMessage(`{"value":"x"}`), ExternalOperationRef: json.RawMessage(`{"id":"e"}`)},
			{StepID: "bound", StepType: "game.bound", Status: protocolstate.StepPending, AdapterID: "game", CapabilityName: "game.bound", CapabilityVersion: "1",
				Input: json.RawMessage(`{}`), InputBindings: binding},
		}
	}
	immutable := func(message string) (error, string) {
		return ErrImmutableFields, "accepted operation fields are immutable: " + message
	}
	invalid := func(message string) (error, string) {
		return ErrInvalidRecord, "invalid operation record: " + message
	}
	type expectation struct {
		err     error
		message string
	}
	expect := func(err error, message string) expectation { return expectation{err, message} }
	const resolveFailure = "resolved input does not match the immutable bindings and recorded outputs"
	const pendingOnly = "resolved input must be committed while the bound step is pending"
	tests := []struct {
		name    string
		current func([]OperationStep) []OperationStep
		next    func([]OperationStep) []OperationStep
		status  protocolstate.OperationStatus
		want    expectation
	}{
		{name: "unchanged", status: running},
		{name: "unchanged while pending", status: pending},
		{name: "step added", next: func(s []OperationStep) []OperationStep { return append(s, OperationStep{StepID: "x"}) }, status: running,
			want: expect(immutable("accepted operation steps cannot be added or removed"))},
		{name: "step removed", next: func(s []OperationStep) []OperationStep { return s[:1] }, status: running,
			want: expect(immutable("accepted operation steps cannot be added or removed"))},
		{name: "step ID", next: func(s []OperationStep) []OperationStep { s[1].StepID = "x"; return s }, status: running,
			want: expect(immutable("step order and identity cannot change"))},
		{name: "step type", next: func(s []OperationStep) []OperationStep { s[0].StepType = "x"; return s }, status: running,
			want: expect(immutable("step order and identity cannot change"))},
		{name: "adapter", next: func(s []OperationStep) []OperationStep { s[0].AdapterID = "x"; return s }, status: running,
			want: expect(immutable("recorded adapter capability identity cannot change"))},
		{name: "capability name", next: func(s []OperationStep) []OperationStep { s[0].CapabilityName = "x"; return s }, status: running,
			want: expect(immutable("recorded adapter capability identity cannot change"))},
		{name: "capability version", next: func(s []OperationStep) []OperationStep { s[0].CapabilityVersion = ""; return s }, status: running,
			want: expect(immutable("recorded adapter capability identity cannot change"))},
		{name: "capability recorded later", current: func(s []OperationStep) []OperationStep {
			s[0].AdapterID, s[0].CapabilityName, s[0].CapabilityVersion = "", "", ""
			return s
		}, status: running},
		{name: "bindings changed", next: func(s []OperationStep) []OperationStep {
			s[1].InputBindings = []inputbinding.Binding{{TargetPointer: "/w", SourceStepID: "source", SourcePointer: "/value"}}
			return s
		}, status: running, want: expect(immutable("recorded step input bindings cannot change"))},
		{name: "bindings removed", next: func(s []OperationStep) []OperationStep { s[1].InputBindings = nil; return s }, status: running,
			want: expect(immutable("recorded step input bindings cannot change"))},
		{name: "input", next: func(s []OperationStep) []OperationStep { s[0].Input = json.RawMessage(`{"a":1}`); return s }, status: running,
			want: expect(immutable("recorded step input cannot change"))},
		{name: "input checked before output", next: func(s []OperationStep) []OperationStep {
			s[0].Input, s[0].Output = json.RawMessage(`{"a":1}`), nil
			return s
		}, status: running, want: expect(immutable("recorded step input cannot change"))},
		{name: "resolved input changed", current: func(s []OperationStep) []OperationStep { s[1].ResolvedInput = json.RawMessage(`{"v":"x"}`); return s },
			next: func(s []OperationStep) []OperationStep { s[1].ResolvedInput = json.RawMessage(`{"v":"y"}`); return s }, status: running,
			want: expect(immutable("resolved step input cannot change"))},
		{name: "resolved input removed", current: func(s []OperationStep) []OperationStep { s[1].ResolvedInput = json.RawMessage(`{"v":"x"}`); return s },
			next: func(s []OperationStep) []OperationStep { s[1].ResolvedInput = nil; return s }, status: running,
			want: expect(immutable("resolved step input cannot change"))},
		{name: "resolved input kept", current: func(s []OperationStep) []OperationStep { s[1].ResolvedInput = json.RawMessage(`{"v":"x"}`); return s },
			next: func(s []OperationStep) []OperationStep { s[1].ResolvedInput = json.RawMessage(`{"v":"x"}`); return s }, status: pending},
		{name: "resolved input committed", next: func(s []OperationStep) []OperationStep { s[1].ResolvedInput = json.RawMessage(`{"v":"x"}`); return s }, status: running},
		{name: "resolved input mismatch", next: func(s []OperationStep) []OperationStep { s[1].ResolvedInput = json.RawMessage(`{"v":"y"}`); return s }, status: running,
			want: expect(invalid(resolveFailure))},
		{name: "resolved input from unfinished source", current: func(s []OperationStep) []OperationStep {
			s[0].Status = protocolstate.StepRunning
			return s
		}, next: func(s []OperationStep) []OperationStep {
			s[0].Status = protocolstate.StepRunning
			s[1].ResolvedInput = json.RawMessage(`{"v":"x"}`)
			return s
		}, status: running, want: expect(invalid(resolveFailure))},
		{name: "resolved input without bindings", current: func(s []OperationStep) []OperationStep { s[0].Status = protocolstate.StepPending; return s },
			next: func(s []OperationStep) []OperationStep {
				s[0].Status = protocolstate.StepPending
				s[0].ResolvedInput = json.RawMessage(`{}`)
				return s
			}, status: running, want: expect(invalid(pendingOnly))},
		{name: "resolved input after dispatch", current: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt = protocolstate.StepRunning, 1
			return s
		}, next: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt, s[1].ResolvedInput = protocolstate.StepRunning, 1, json.RawMessage(`{"v":"x"}`)
			return s
		}, status: running, want: expect(invalid(pendingOnly))},
		{name: "resolved input while dispatching", next: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt, s[1].ResolvedInput = protocolstate.StepRunning, 1, json.RawMessage(`{"v":"x"}`)
			return s
		}, status: running, want: expect(invalid(pendingOnly))},
		{name: "resolved input with attempt change", next: func(s []OperationStep) []OperationStep {
			s[1].Attempt, s[1].ResolvedInput = 1, json.RawMessage(`{"v":"x"}`)
			return s
		}, status: running, want: expect(invalid(pendingOnly))},
		{name: "resolved input while operation pending", next: func(s []OperationStep) []OperationStep {
			s[1].ResolvedInput = json.RawMessage(`{"v":"x"}`)
			return s
		}, status: pending, want: expect(invalid(pendingOnly))},
		{name: "output changed", next: func(s []OperationStep) []OperationStep { s[0].Output = json.RawMessage(`{"value":"y"}`); return s }, status: running,
			want: expect(immutable("recorded step output cannot change"))},
		{name: "output recorded later", current: func(s []OperationStep) []OperationStep { s[0].Output = nil; return s }, status: running},
		{name: "external reference changed", next: func(s []OperationStep) []OperationStep { s[0].ExternalOperationRef = nil; return s }, status: running,
			want: expect(immutable("recorded external operation reference cannot change"))},
		{name: "external reference recorded later", current: func(s []OperationStep) []OperationStep { s[0].ExternalOperationRef = nil; return s }, status: running},
		{name: "illegal step transition", next: func(s []OperationStep) []OperationStep { s[1].Status = protocolstate.StepSucceeded; return s }, status: running,
			want: expect(protocolstate.ErrIllegalTransition, `step "pending" cannot transition to "succeeded": illegal protocol state transition`)},
		{name: "transition checked before attempt", next: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt = protocolstate.StepSucceeded, 4
			return s
		}, status: running, want: expect(protocolstate.ErrIllegalTransition, `step "pending" cannot transition to "succeeded": illegal protocol state transition`)},
		{name: "start step", next: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt = protocolstate.StepRunning, 1
			return s
		}, status: running},
		{name: "start step while pending", next: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt = protocolstate.StepRunning, 1
			return s
		}, status: pending, want: expect(invalid("a step may start only while the operation is running"))},
		{name: "start step without attempt", next: func(s []OperationStep) []OperationStep { s[1].Status = protocolstate.StepRunning; return s }, status: running,
			want: expect(invalid("entering running must increment step attempt by one"))},
		{name: "start step skipping attempt", next: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt = protocolstate.StepRunning, 2
			return s
		}, status: running, want: expect(invalid("entering running must increment step attempt by one"))},
		{name: "retry waiting step", current: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt, s[1].ResolvedInput = protocolstate.StepWaiting, 1, json.RawMessage(`{"v":"x"}`)
			return s
		}, next: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt, s[1].ResolvedInput = protocolstate.StepRunning, 2, json.RawMessage(`{"v":"x"}`)
			return s
		}, status: running},
		{name: "attempt changed while running", current: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt = protocolstate.StepRunning, 1
			return s
		}, next: func(s []OperationStep) []OperationStep {
			s[1].Status, s[1].Attempt = protocolstate.StepRunning, 2
			return s
		}, status: running, want: expect(immutable("step attempt can change only when entering running"))},
		{name: "attempt changed without transition", next: func(s []OperationStep) []OperationStep { s[0].Attempt = 2; return s }, status: running,
			want: expect(immutable("step attempt can change only when entering running"))},
		{name: "skip step", next: func(s []OperationStep) []OperationStep { s[1].Status = protocolstate.StepSkipped; return s }, status: pending},
		{name: "first step fails first", next: func(s []OperationStep) []OperationStep {
			s[0].Attempt = 2
			s[1].StepID = "x"
			return s
		}, status: running, want: expect(immutable("step attempt can change only when entering running"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current, next := steps(), steps()
			if test.current != nil {
				current = test.current(current)
				next = test.current(next)
			}
			if test.next != nil {
				next = test.next(next)
			}
			storeR3AssertErr(t, validateStepChanges(current, next, test.status), test.want.err, test.want.message)
		})
	}
}

// storeR3AcceptLocked accepts a test.operation that holds one lock scope.
func storeR3AcceptLocked(t *testing.T, store *BoltStore, id, scope string) StoredOperation {
	t.Helper()
	record := testOperation(id)
	record.RequestFingerprint = "sha256:" + strings.Repeat("e", 64)
	accepted, _, err := store.AcceptOperation(record, proofOf("v1", verifier("v1", 7, 8)), []string{scope})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func storeR3AssertStoredOperation(t *testing.T, store *BoltStore, stored StoredOperation) {
	t.Helper()
	want := storeR3JSON(t, operationEnvelope{Revision: stored.Revision, Record: stored.Record})
	if got := storeR3RawGet(t, store, operationsBucket, []byte(stored.Record.OperationID)); string(got) != string(want) {
		t.Fatalf("stored operation bytes:\n got %s\nwant %s", got, want)
	}
}

func TestStoreR3UpdateOperationRejectionsCharacterization(t *testing.T) {
	const scope = "r3/scope"
	running := func(r *OperationRecord) {
		r.Status = protocolstate.OperationRunning
		r.Steps[0].Status, r.Steps[0].Attempt = protocolstate.StepRunning, 1
	}
	tests := []struct {
		name    string
		prepare func(*testing.T, *BoltStore, StoredOperation) uint64
		mutate  func(*OperationRecord)
		want    error
		message string
	}{
		{name: "missing ID", mutate: func(r *OperationRecord) { r.OperationID = "" }, want: ErrInvalidRecord, message: "invalid operation record: missing operation_id"},
		{name: "unknown operation", mutate: func(r *OperationRecord) { r.OperationID = "missing" }, want: ErrNotFound, message: "operation not found"},
		{name: "corrupt stored operation", prepare: func(t *testing.T, s *BoltStore, created StoredOperation) uint64 {
			storeR3RawPut(t, s, operationsBucket, []byte(created.Record.OperationID), []byte(`{"revision":0}`))
			return 1
		}, want: ErrCorruptStore, message: "corrupt operation store: invalid stored operation"},
		{name: "stale revision", prepare: func(*testing.T, *BoltStore, StoredOperation) uint64 { return 2 }, want: ErrRevisionConflict, message: "operation revision conflict"},
		{name: "changed request", mutate: func(r *OperationRecord) { r.ActorID = "operator-2" }, want: ErrImmutableFields, message: "accepted operation fields are immutable"},
		{name: "illegal operation transition", mutate: func(r *OperationRecord) { r.Status = protocolstate.OperationWaiting }, want: protocolstate.ErrIllegalTransition,
			message: `operation "pending" cannot transition to "waiting": illegal protocol state transition`},
		{name: "operation transition before steps", mutate: func(r *OperationRecord) { r.Status = protocolstate.OperationWaiting; r.Steps = nil }, want: protocolstate.ErrIllegalTransition,
			message: `operation "pending" cannot transition to "waiting": illegal protocol state transition`},
		{name: "step change", mutate: func(r *OperationRecord) { r.Steps[0].Attempt = 3 }, want: ErrImmutableFields,
			message: "accepted operation fields are immutable: step attempt can change only when entering running"},
		{name: "invalid record", mutate: func(r *OperationRecord) { r.Progress = json.RawMessage(`[]`) }, want: ErrInvalidRecord, message: "invalid operation record: invalid progress"},
		{name: "record checked before outcome", mutate: func(r *OperationRecord) {
			r.Status, r.Progress = protocolstate.OperationFailed, json.RawMessage(`[]`)
		}, want: ErrInvalidRecord, message: "invalid operation record: invalid progress"},
		{name: "failure without error", mutate: func(r *OperationRecord) { r.Status = protocolstate.OperationFailed }, want: ErrInvalidRecord,
			message: "invalid operation record: failure requires an error"},
		{name: "unresolved step", prepare: func(t *testing.T, s *BoltStore, created StoredOperation) uint64 {
			next := created.Record
			running(&next)
			updated, err := s.UpdateOperation(created.Revision, next)
			if err != nil {
				t.Fatal(err)
			}
			return updated.Revision
		}, mutate: func(r *OperationRecord) {
			running(r)
			r.Status, r.Error = protocolstate.OperationFailed, json.RawMessage(`{"code":"x"}`)
		}, want: ErrUnresolvedEffect, message: "operation has an unresolved external effect"},
		{name: "lock owned by another operation", prepare: func(t *testing.T, s *BoltStore, created StoredOperation) uint64 {
			storeR3RawPut(t, s, locksBucket, []byte(scope), storeR3JSON(t, ResourceLock{Scope: scope, OperationID: "other", AcquiredAt: created.Record.CreatedAt}))
			return 1
		}, mutate: func(r *OperationRecord) {
			r.Status, r.Error = protocolstate.OperationFailed, json.RawMessage(`{"code":"x"}`)
		}, want: ErrCorruptStore, message: `corrupt operation store: operation does not own lock "r3/scope"`},
		{name: "lock missing", prepare: func(t *testing.T, s *BoltStore, _ StoredOperation) uint64 {
			storeR3RawPut(t, s, locksBucket, []byte(scope), nil)
			return 1
		}, mutate: func(r *OperationRecord) {
			r.Status, r.Error = protocolstate.OperationFailed, json.RawMessage(`{"code":"x"}`)
		}, want: ErrCorruptStore, message: `corrupt operation store: operation lock "r3/scope" is missing`},
		{name: "invalid reverse lock index", prepare: func(t *testing.T, s *BoltStore, created StoredOperation) uint64 {
			storeR3RawPut(t, s, operationLocksBucket, []byte(created.Record.OperationID), []byte(`[""]`))
			return 1
		}, mutate: func(r *OperationRecord) {
			r.Status = protocolstate.OperationCancelled
		}, want: ErrCorruptStore, message: "corrupt operation store: invalid operation lock scopes"},
		{name: "revision exhausted", prepare: func(t *testing.T, s *BoltStore, created StoredOperation) uint64 {
			storeR3RawPut(t, s, operationsBucket, []byte(created.Record.OperationID), storeR3JSON(t, operationEnvelope{Revision: ^uint64(0), Record: created.Record}))
			return ^uint64(0)
		}, mutate: func(r *OperationRecord) {
			r.Status = protocolstate.OperationCancelled
		}, want: ErrInvalidRecord, message: "invalid operation record: revision exhausted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			created := storeR3AcceptLocked(t, store, "r3-update", scope)
			revision := created.Revision
			if test.prepare != nil {
				revision = test.prepare(t, store, created)
			}
			current := created.Record
			if stored, err := store.GetOperation(current.OperationID); err == nil {
				current = stored.Record
			}
			next := current
			next.Steps = append([]OperationStep(nil), current.Steps...)
			if test.mutate != nil {
				test.mutate(&next)
			}
			before := storeR3Snapshot(t, store.db)
			_, err := store.UpdateOperation(revision, next)
			storeR3AssertErr(t, err, test.want, test.message)
			storeR3AssertUnchanged(t, store.db, before)
		})
	}

	t.Run("terminal operation", func(t *testing.T) {
		store := openTestStore(t)
		created := storeR3AcceptLocked(t, store, "r3-terminal", scope)
		next := created.Record
		next.Status = protocolstate.OperationCancelled
		cancelled, err := store.UpdateOperation(created.Revision, next)
		if err != nil {
			t.Fatal(err)
		}
		before := storeR3Snapshot(t, store.db)
		_, err = store.UpdateOperation(cancelled.Revision, cancelled.Record)
		storeR3AssertErr(t, err, ErrTerminalOperation, "terminal operation cannot be changed")
		storeR3AssertUnchanged(t, store.db, before)
	})
}

// storeR3Expect fails with every named check that does not hold.
func storeR3Expect(t *testing.T, subject any, checks map[string]bool) {
	t.Helper()
	for name, ok := range checks {
		if !ok {
			t.Errorf("%s does not hold for %#v", name, subject)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
}

func storeR3SameTime(left *time.Time, right time.Time) bool {
	return left != nil && left.Equal(right)
}

// storeR3Update copies the stored record, applies change, updates it, and
// requires the stored bytes to be the returned envelope.
func storeR3Update(t *testing.T, store *BoltStore, previous StoredOperation, change func(*OperationRecord)) StoredOperation {
	t.Helper()
	next := previous.Record
	next.Steps = append([]OperationStep(nil), previous.Record.Steps...)
	change(&next)
	updated, err := store.UpdateOperation(previous.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	storeR3AssertStoredOperation(t, store, updated)
	return updated
}

func TestStoreR3UpdateOperationStoredStateCharacterization(t *testing.T) {
	const scope = "r3/scope"
	store := openTestStore(t)
	created := storeR3AcceptLocked(t, store, "r3-state", scope)
	storeR3AssertStoredOperation(t, store, created)

	// Caller-supplied timestamps are ignored; the store assigns them.
	junk := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	started := storeR3Update(t, store, created, func(next *OperationRecord) {
		next.Status = protocolstate.OperationRunning
		next.Steps[0].Status, next.Steps[0].Attempt = protocolstate.StepRunning, 1
		next.CreatedAt, next.UpdatedAt, next.StartedAt, next.CompletedAt = junk, junk, &junk, &junk
		next.Steps[0].StartedAt, next.Steps[0].CompletedAt = &junk, &junk
	})
	record := started.Record
	storeR3Expect(t, started, map[string]bool{
		"revision 2":             started.Revision == 2,
		"created at kept":        record.CreatedAt.Equal(created.Record.CreatedAt),
		"updated at advanced":    record.UpdatedAt.After(created.Record.UpdatedAt),
		"started at update":      storeR3SameTime(record.StartedAt, record.UpdatedAt),
		"not completed":          record.CompletedAt == nil,
		"step started at update": storeR3SameTime(record.Steps[0].StartedAt, record.UpdatedAt),
		"step not completed":     record.Steps[0].CompletedAt == nil,
	})

	// A same-status update keeps the start times and advances UpdatedAt.
	progressed := storeR3Update(t, store, started, func(next *OperationRecord) { next.Progress = json.RawMessage(`{"percent":50}`) })
	storeR3Expect(t, progressed, map[string]bool{
		"revision 3":          progressed.Revision == 3,
		"started at kept":     storeR3SameTime(progressed.Record.StartedAt, *record.StartedAt),
		"updated at advanced": progressed.Record.UpdatedAt.After(record.UpdatedAt),
		"step started kept":   storeR3SameTime(progressed.Record.Steps[0].StartedAt, *record.Steps[0].StartedAt),
	})

	// A terminal update completes the record and the step, and releases locks.
	completed := storeR3Update(t, store, progressed, func(next *OperationRecord) {
		next.Status, next.Error = protocolstate.OperationFailed, json.RawMessage(`{"code":"boom"}`)
		next.Steps[0].Status = protocolstate.StepFailed
	})
	storeR3Expect(t, completed, map[string]bool{
		"revision 4":                 completed.Revision == 4,
		"completed at update":        storeR3SameTime(completed.Record.CompletedAt, completed.Record.UpdatedAt),
		"step completed at update":   storeR3SameTime(completed.Record.Steps[0].CompletedAt, completed.Record.UpdatedAt),
		"lock released":              len(storeR3RawGet(t, store, locksBucket, []byte(scope))) == 0,
		"reverse lock index removed": len(storeR3RawGet(t, store, operationLocksBucket, []byte("r3-state"))) == 0,
	})
}

func TestStoreR3UpdateOperationWithoutStepsCharacterization(t *testing.T) {
	store := openTestStore(t)
	record := testOperation("r3-no-steps")
	record.Steps = nil
	created, err := store.createOperation(record)
	if err != nil {
		t.Fatal(err)
	}
	next := created.Record
	next.Steps = nil
	next.Status = protocolstate.OperationRunning
	updated, err := store.UpdateOperation(created.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Record.Steps == nil || len(updated.Record.Steps) != 0 {
		t.Fatalf("steps = %#v, want empty slice", updated.Record.Steps)
	}
	storeR3AssertStoredOperation(t, store, updated)
	if raw := storeR3RawGet(t, store, operationsBucket, []byte("r3-no-steps")); !strings.Contains(string(raw), `"steps":[]`) {
		t.Fatalf("stored record lacks empty steps: %s", raw)
	}
}
