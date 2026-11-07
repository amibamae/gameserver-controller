package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

func TestCreateOperationPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "controller.db")
	store, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateOperation(testOperation("persist-me"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.Record.Status != protocolstate.OperationPending {
		t.Fatalf("unexpected created operation: %#v", created)
	}
	if created.Record.CreatedAt.IsZero() || !created.Record.CreatedAt.Equal(created.Record.UpdatedAt) {
		t.Fatalf("store did not assign consistent acceptance timestamps: %#v", created.Record)
	}
	if _, err := store.CreateOperation(testOperation("persist-me")); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate create: got %v, want ErrAlreadyExists", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("database permissions are too broad: %o", got)
	}

	reopened, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	read, err := reopened.GetOperation("persist-me")
	if err != nil {
		t.Fatal(err)
	}
	if read.Revision != created.Revision || read.Record.OperationID != created.Record.OperationID || read.Record.Status != created.Record.Status {
		t.Fatalf("reopened operation differs: got %#v, want %#v", read, created)
	}
	if !read.Record.CreatedAt.Equal(created.Record.CreatedAt) || string(read.Record.RequestSnapshot.Parameters) != string(created.Record.RequestSnapshot.Parameters) {
		t.Fatalf("reopened operation lost request or timestamp data: got %#v, want %#v", read.Record, created.Record)
	}
}

func TestUpdateUsesRevisionAndProtocolTransitions(t *testing.T) {
	store := openTestStore(t)
	created, err := store.CreateOperation(testOperation("update-me"))
	if err != nil {
		t.Fatal(err)
	}

	next := created.Record
	next.Status = protocolstate.OperationRunning
	next.Steps[0].Status = protocolstate.StepRunning
	next.Steps[0].Attempt = 1
	currentStepID := next.Steps[0].StepID
	next.CurrentStepID = &currentStepID
	updated, err := store.UpdateOperation(created.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Record.StartedAt == nil || updated.Record.Steps[0].StartedAt == nil {
		t.Fatalf("running state and timestamps were not recorded: %#v", updated)
	}

	if _, err := store.UpdateOperation(created.Revision, updated.Record); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update: got %v, want ErrRevisionConflict", err)
	}
	invalidTransition := updated.Record
	invalidTransition.Status = protocolstate.OperationPending
	if _, err := store.UpdateOperation(updated.Revision, invalidTransition); !errors.Is(err, protocolstate.ErrIllegalTransition) {
		t.Fatalf("illegal transition: got %v, want ErrIllegalTransition", err)
	}
	changedRequest := updated.Record
	changedRequest.RequestSnapshot.Parameters = json.RawMessage(`{"world":"other"}`)
	if _, err := store.UpdateOperation(updated.Revision, changedRequest); !errors.Is(err, ErrImmutableFields) {
		t.Fatalf("changed accepted request: got %v, want ErrImmutableFields", err)
	}
	changedSchema := updated.Record
	changedSchema.SchemaPackageRevision = "other-revision"
	if _, err := store.UpdateOperation(updated.Revision, changedSchema); !errors.Is(err, ErrImmutableFields) {
		t.Fatalf("changed accepted schema revision: got %v, want ErrImmutableFields", err)
	}
}

func TestTerminalOperationRequiresResolvedStepsAndOutcome(t *testing.T) {
	store := openTestStore(t)
	created, err := store.CreateOperation(testOperation("terminal-me"))
	if err != nil {
		t.Fatal(err)
	}

	running := created.Record
	running.Status = protocolstate.OperationRunning
	running.Steps[0].Status = protocolstate.StepRunning
	running.Steps[0].Attempt = 1
	runningStored, err := store.UpdateOperation(created.Revision, running)
	if err != nil {
		t.Fatal(err)
	}

	unknown := runningStored.Record
	unknown.Status = protocolstate.OperationReconciling
	unknown.Steps[0].Status = protocolstate.StepOutcomeUnknown
	unknownStored, err := store.UpdateOperation(runningStored.Revision, unknown)
	if err != nil {
		t.Fatal(err)
	}

	failed := unknownStored.Record
	failed.Status = protocolstate.OperationFailed
	failed.Error = json.RawMessage(`{"code":"core.provider.unknown_outcome"}`)
	if _, err := store.UpdateOperation(unknownStored.Revision, failed); !errors.Is(err, ErrUnresolvedEffect) {
		t.Fatalf("failed operation with unknown effect: got %v, want ErrUnresolvedEffect", err)
	}
}

func TestListRecoverableKeepsSuspendedOperations(t *testing.T) {
	store := openTestStore(t)
	first, err := store.CreateOperation(testOperation("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateOperation(testOperation("second"))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.CreateOperation(testOperation("terminal"))
	if err != nil {
		t.Fatal(err)
	}

	secondRunning := second.Record
	secondRunning.Status = protocolstate.OperationRunning
	secondRunningStored, err := store.UpdateOperation(second.Revision, secondRunning)
	if err != nil {
		t.Fatal(err)
	}
	secondSuspended := secondRunningStored.Record
	secondSuspended.Status = protocolstate.OperationNeedsOperator
	if _, err := store.UpdateOperation(secondRunningStored.Revision, secondSuspended); err != nil {
		t.Fatal(err)
	}

	terminalRunning := terminal.Record
	terminalRunning.Status = protocolstate.OperationRunning
	terminalRunning.Steps[0].Status = protocolstate.StepRunning
	terminalRunning.Steps[0].Attempt = 1
	terminalRunningStored, err := store.UpdateOperation(terminal.Revision, terminalRunning)
	if err != nil {
		t.Fatal(err)
	}
	terminalDone := terminalRunningStored.Record
	terminalDone.Status = protocolstate.OperationSucceeded
	terminalDone.Steps[0].Status = protocolstate.StepSucceeded
	terminalDone.Result = json.RawMessage(`{"partial":false}`)
	if _, err := store.UpdateOperation(terminalRunningStored.Revision, terminalDone); err != nil {
		t.Fatal(err)
	}

	recoverable, err := store.ListRecoverableOperations()
	if err != nil {
		t.Fatal(err)
	}
	if len(recoverable) != 2 {
		t.Fatalf("got %d recoverable operations, want 2: %#v", len(recoverable), recoverable)
	}
	if recoverable[0].Record.OperationID != first.Record.OperationID || recoverable[1].Record.Status != protocolstate.OperationNeedsOperator {
		t.Fatalf("recoverable operations lost stable order or suspended state: %#v", recoverable)
	}
}

func TestCreateRejectsInvalidSnapshotAndKey(t *testing.T) {
	store := openTestStore(t)
	badSnapshot := testOperation("bad-secret-shape")
	badSnapshot.RequestSnapshot.Parameters = json.RawMessage(`[]`)
	if _, err := store.CreateOperation(badSnapshot); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("array parameters: got %v, want ErrInvalidRecord", err)
	}

	badKey := testOperation("bad-key")
	badKey.IdempotencyKey = "line\nbreak"
	if _, err := store.CreateOperation(badKey); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid idempotency key: got %v, want ErrInvalidRecord", err)
	}
}

func TestCreateRejectsMissingCurrentStep(t *testing.T) {
	store := openTestStore(t)
	operation := testOperation("missing-current-step")
	missing := "not-in-plan"
	operation.CurrentStepID = &missing
	if _, err := store.CreateOperation(operation); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("missing current step: got %v, want ErrInvalidRecord", err)
	}
}

func TestStepCannotStartBeforeOperationRuns(t *testing.T) {
	store := openTestStore(t)
	created, err := store.CreateOperation(testOperation("untracked-dispatch"))
	if err != nil {
		t.Fatal(err)
	}
	next := created.Record
	next.Steps[0].Status = protocolstate.StepRunning
	next.Steps[0].Attempt = 1
	if _, err := store.UpdateOperation(created.Revision, next); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("step started under pending operation: got %v, want ErrInvalidRecord", err)
	}
}

func TestRecordedCapabilityIdentityCannotChange(t *testing.T) {
	store := openTestStore(t)
	operation := testOperation("pinned-capability")
	operation.Steps[0].AdapterID = "hosting.example"
	operation.Steps[0].CapabilityName = "hosting.instance.create"
	operation.Steps[0].CapabilityVersion = "1"
	created, err := store.CreateOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	running := created.Record
	running.Status = protocolstate.OperationRunning
	running.Steps[0].Status = protocolstate.StepRunning
	running.Steps[0].Attempt = 1
	runningStored, err := store.UpdateOperation(created.Revision, running)
	if err != nil {
		t.Fatal(err)
	}
	changed := runningStored.Record
	changed.Steps[0].CapabilityVersion = "2"
	if _, err := store.UpdateOperation(runningStored.Revision, changed); !errors.Is(err, ErrImmutableFields) {
		t.Fatalf("changed dispatched capability version: got %v, want ErrImmutableFields", err)
	}
}

func openTestStore(t *testing.T) *BoltStore {
	t.Helper()
	store, err := OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return store
}

func testOperation(id string) OperationRecord {
	return OperationRecord{
		OperationID:           id,
		OperationType:         "server.provision",
		ProtocolVersion:       "0.21.0-draft",
		SchemaPackageRevision: "0.21.0-draft",
		Target: OperationTarget{
			ServerID:      "server-1",
			EnvironmentID: "staging",
		},
		Status:         protocolstate.OperationPending,
		ActorID:        "operator-1",
		IdempotencyKey: "create-server-one",
		RequestSnapshot: RequestSnapshot{
			OperationType: "server.provision",
			Parameters:    json.RawMessage(`{"plan":"small"}`),
		},
		Steps: []OperationStep{{
			StepID:   "validate",
			StepType: "request.validate",
			Status:   protocolstate.StepPending,
		}},
	}
}
