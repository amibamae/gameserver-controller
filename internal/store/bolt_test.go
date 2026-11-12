package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

func TestCreateOperationPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "controller.db")
	store, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.createOperation(testOperation("persist-me"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.Record.Status != protocolstate.OperationPending {
		t.Fatalf("unexpected created operation: %#v", created)
	}
	if created.Record.CreatedAt.IsZero() || !created.Record.CreatedAt.Equal(created.Record.UpdatedAt) {
		t.Fatalf("store did not assign consistent acceptance timestamps: %#v", created.Record)
	}
	if _, err := store.createOperation(testOperation("persist-me")); !errors.Is(err, ErrAlreadyExists) {
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
	created, err := store.createOperation(testOperation("update-me"))
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

func TestOperationAndLogicalServerTransitionCommitAtomically(t *testing.T) {
	store := openTestStore(t)
	server := testLogicalServerFixture(t, store)
	operation := testOperation("lifecycle-atomic")
	operation.OperationType = "server.provision"
	operation.RequestSnapshot.OperationType = operation.OperationType
	operation.RequestFingerprint = "sha256:" + strings.Repeat("a", 64)
	proof := IdempotencyProof{
		ActiveKeyVersion: "v1",
		Candidates: []IdempotencyVerifier{{
			KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32),
		}},
	}
	created, replayed, err := store.AcceptOperation(operation, proof, []string{LogicalServerLockScope("staging", "server-1")})
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Fatal("new lifecycle operation unexpectedly replayed")
	}

	nextOperation := created.Record
	nextOperation.Status = protocolstate.OperationRunning
	nextOperation.Steps[0].Status = protocolstate.StepRunning
	nextOperation.Steps[0].Attempt = 1
	stepID := nextOperation.Steps[0].StepID
	nextOperation.CurrentStepID = &stepID
	nextServer := server.Record
	nextServer.LifecycleState = protocolstate.ServerProvisioning
	updatedOperation, updatedServer, err := store.UpdateOperationAndLogicalServer(created.Revision, nextOperation, server.Revision, nextServer)
	if err != nil {
		t.Fatal(err)
	}
	if updatedOperation.Revision != created.Revision+1 || updatedServer.Revision != server.Revision+1 ||
		updatedOperation.Record.Status != protocolstate.OperationRunning || updatedServer.Record.LifecycleState != protocolstate.ServerProvisioning {
		t.Fatalf("operation and server did not advance together: operation=%#v server=%#v", updatedOperation, updatedServer)
	}
	if len(updatedServer.Record.LifecycleEvents) != 1 {
		t.Fatalf("server state transition event was not persisted: %#v", updatedServer.Record.LifecycleEvents)
	}
	event := updatedServer.Record.LifecycleEvents[0]
	if event.OperationID != operation.OperationID || event.ActorID != operation.ActorID || event.FromState != protocolstate.ServerUnprovisioned ||
		event.ToState != protocolstate.ServerProvisioning || event.RequestFingerprint != operation.RequestFingerprint || len(event.EvidenceStepIDs) != 0 {
		t.Fatalf("lifecycle event is not bound to the operation: %#v", event)
	}

	invalidServer := updatedServer.Record
	invalidServer.LifecycleState = protocolstate.ServerDestroyed
	if _, _, err := store.UpdateOperationAndLogicalServer(updatedOperation.Revision, updatedOperation.Record, updatedServer.Revision, invalidServer); !errors.Is(err, protocolstate.ErrIllegalTransition) {
		t.Fatalf("illegal server transition was accepted: %v", err)
	}
	storedOperation, err := store.GetOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	storedServer, err := store.GetLogicalServer(server.Record.EnvironmentID, server.Record.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	if storedOperation.Revision != updatedOperation.Revision || storedServer.Revision != updatedServer.Revision ||
		storedServer.Record.LifecycleState != protocolstate.ServerProvisioning || len(storedServer.Record.LifecycleEvents) != 1 {
		t.Fatalf("rejected transition partially committed: operation=%#v server=%#v", storedOperation, storedServer)
	}
}

func TestAcceptLifecycleOperationRequiresDefinedLogicalServer(t *testing.T) {
	store := openTestStore(t)
	operation := testOperation("lifecycle-without-server")
	operation.OperationType = "server.provision"
	operation.RequestSnapshot.OperationType = operation.OperationType
	operation.RequestFingerprint = "sha256:" + strings.Repeat("c", 64)
	proof := IdempotencyProof{
		ActiveKeyVersion: "v1",
		Candidates: []IdempotencyVerifier{{
			KeyVersion: "v1", Digest: bytes.Repeat([]byte{3}, 32), BindingDigest: bytes.Repeat([]byte{4}, 32),
		}},
	}
	lockScope := LogicalServerLockScope("staging", "server-1")
	if _, _, err := store.AcceptOperation(operation, proof, []string{lockScope}); !errors.Is(err, ErrLogicalServerNotFound) {
		t.Fatalf("lifecycle operation without definition: got %v, want ErrLogicalServerNotFound", err)
	}
	if _, err := store.GetOperation(operation.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected operation was persisted: %v", err)
	}
	if _, err := store.GetResourceLock(lockScope); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected operation retained a resource lock: %v", err)
	}
}

func TestLifecycleCommitRequiresDurableServerLock(t *testing.T) {
	store := openTestStore(t)
	server := testLogicalServerFixture(t, store)
	operation := testOperation("lifecycle-without-lock")
	operation.OperationType = "server.provision"
	operation.RequestSnapshot.OperationType = operation.OperationType
	operation.RequestFingerprint = "sha256:" + strings.Repeat("b", 64)
	created, err := store.createOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	nextOperation := created.Record
	nextOperation.Status = protocolstate.OperationRunning
	nextOperation.Steps[0].Status = protocolstate.StepRunning
	nextOperation.Steps[0].Attempt = 1
	nextServer := server.Record
	nextServer.LifecycleState = protocolstate.ServerProvisioning
	if _, _, err := store.UpdateOperationAndLogicalServer(created.Revision, nextOperation, server.Revision, nextServer); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("lifecycle commit without an acceptance lock: got %v, want ErrCorruptStore", err)
	}
	storedOperation, err := store.GetOperation(operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	storedServer, err := store.GetLogicalServer("staging", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	if storedOperation.Revision != created.Revision || storedServer.Revision != server.Revision || storedServer.Record.LifecycleState != protocolstate.ServerUnprovisioned {
		t.Fatalf("lockless lifecycle commit partially changed state: operation=%#v server=%#v", storedOperation, storedServer)
	}
}

func testLogicalServerFixture(t *testing.T, operationStore *BoltStore) StoredLogicalServer {
	t.Helper()
	now := time.Now().UTC()
	server := LogicalServer{
		ServerID: "server-1", EnvironmentID: "staging", DisplayName: "Test Server",
		HostingProfile: "vultr-small", StorageProfile: "wasabi-worlds", GameAdapterID: "game.paper",
		ReleaseChannel: "stable", ExecutionAdapterID: "execution.systemd", ExecutionProfile: "paper-default",
		Definition: json.RawMessage(`{"server_id":"server-1"}`), LifecycleState: protocolstate.ServerUnprovisioned,
		DataGenerationStatus: "unknown", LastOperationID: "definition-operation", CreatedAt: now, UpdatedAt: now,
	}
	encoded, err := encodeLogicalServer(server)
	if err != nil {
		t.Fatal(err)
	}
	if err := operationStore.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(logicalServersBucket).Put(logicalServerKey(server.EnvironmentID, server.ServerID), encoded)
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := operationStore.GetLogicalServer(server.EnvironmentID, server.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestTerminalOperationRequiresResolvedStepsAndOutcome(t *testing.T) {
	store := openTestStore(t)
	created, err := store.createOperation(testOperation("terminal-me"))
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
	first, err := store.createOperation(testOperation("first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.createOperation(testOperation("second"))
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.createOperation(testOperation("terminal"))
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
	if _, err := store.createOperation(badSnapshot); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("array parameters: got %v, want ErrInvalidRecord", err)
	}

	badKey := testOperation("bad-key")
	badKey.IdempotencyKey = "line\nbreak"
	if _, err := store.createOperation(badKey); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("invalid idempotency key: got %v, want ErrInvalidRecord", err)
	}
}

func TestCreateRejectsMissingCurrentStep(t *testing.T) {
	store := openTestStore(t)
	operation := testOperation("missing-current-step")
	missing := "not-in-plan"
	operation.CurrentStepID = &missing
	if _, err := store.createOperation(operation); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("missing current step: got %v, want ErrInvalidRecord", err)
	}
}

func TestStepCannotStartBeforeOperationRuns(t *testing.T) {
	store := openTestStore(t)
	created, err := store.createOperation(testOperation("untracked-dispatch"))
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
	operation.Steps[0].Input = json.RawMessage(`{}`)
	created, err := store.createOperation(operation)
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

func TestAdapterStepInputsAndOutputsAreDurableAndImmutable(t *testing.T) {
	store := openTestStore(t)
	operation := testOperation("adapter-step-data")
	operation.Steps[0].AdapterID = "hosting.example"
	operation.Steps[0].CapabilityName = "hosting.instance.create"
	operation.Steps[0].CapabilityVersion = "1"
	operation.Steps[0].Input = json.RawMessage(`{"server_id":"server-1","environment_id":"staging"}`)
	created, err := store.createOperation(operation)
	if err != nil {
		t.Fatal(err)
	}

	running := created.Record
	running.Status = protocolstate.OperationRunning
	running.Steps[0].Status = protocolstate.StepRunning
	running.Steps[0].Attempt = 1
	currentStep := running.Steps[0].StepID
	running.CurrentStepID = &currentStep
	runningStored, err := store.UpdateOperation(created.Revision, running)
	if err != nil {
		t.Fatal(err)
	}

	completed := runningStored.Record
	completed.Steps[0].Status = protocolstate.StepSucceeded
	completed.Steps[0].Output = json.RawMessage(`{"provider_state":"running"}`)
	completedStored, err := store.UpdateOperation(runningStored.Revision, completed)
	if err != nil {
		t.Fatal(err)
	}
	if string(completedStored.Record.Steps[0].Input) != string(operation.Steps[0].Input) || string(completedStored.Record.Steps[0].Output) != string(completed.Steps[0].Output) {
		t.Fatalf("adapter input/output did not round-trip: %#v", completedStored.Record.Steps[0])
	}

	changedInput := completedStored.Record
	changedInput.Steps[0].Input = json.RawMessage(`{"server_id":"other"}`)
	if _, err := store.UpdateOperation(completedStored.Revision, changedInput); !errors.Is(err, ErrImmutableFields) {
		t.Fatalf("changed accepted step input: got %v, want ErrImmutableFields", err)
	}
	changedOutput := completedStored.Record
	changedOutput.Steps[0].Output = json.RawMessage(`{"provider_state":"absent"}`)
	if _, err := store.UpdateOperation(completedStored.Revision, changedOutput); !errors.Is(err, ErrImmutableFields) {
		t.Fatalf("changed recorded step output: got %v, want ErrImmutableFields", err)
	}
}

func TestAdapterStepRequiresObjectInputAndSucceededOutput(t *testing.T) {
	store := openTestStore(t)
	missingInput := testOperation("missing-step-input")
	missingInput.Steps[0].AdapterID = "hosting.example"
	missingInput.Steps[0].CapabilityName = "hosting.instance.create"
	missingInput.Steps[0].CapabilityVersion = "1"
	if _, err := store.createOperation(missingInput); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("adapter step without input: got %v, want ErrInvalidRecord", err)
	}
	invalidInput := testOperation("invalid-step-input")
	invalidInput.Steps[0].AdapterID = "hosting.example"
	invalidInput.Steps[0].CapabilityName = "hosting.instance.create"
	invalidInput.Steps[0].CapabilityVersion = "1"
	invalidInput.Steps[0].Input = json.RawMessage(`[1,2]`)
	if _, err := store.createOperation(invalidInput); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("adapter step with non-object input: got %v, want ErrInvalidRecord", err)
	}

	missingOutput := testOperation("missing-step-output")
	missingOutput.Steps[0].AdapterID = "hosting.example"
	missingOutput.Steps[0].CapabilityName = "hosting.instance.create"
	missingOutput.Steps[0].CapabilityVersion = "1"
	missingOutput.Steps[0].Input = json.RawMessage(`{}`)
	created, err := store.createOperation(missingOutput)
	if err != nil {
		t.Fatal(err)
	}
	running := created.Record
	running.Status = protocolstate.OperationRunning
	running.Steps[0].Status = protocolstate.StepRunning
	running.Steps[0].Attempt = 1
	currentStep := running.Steps[0].StepID
	running.CurrentStepID = &currentStep
	runningStored, err := store.UpdateOperation(created.Revision, running)
	if err != nil {
		t.Fatal(err)
	}
	completed := runningStored.Record
	completed.Steps[0].Status = protocolstate.StepSucceeded
	if _, err := store.UpdateOperation(runningStored.Revision, completed); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("succeeded adapter step without output: got %v, want ErrInvalidRecord", err)
	}
}

func TestIdempotencyAssociationWithMissingOperationFailsClosed(t *testing.T) {
	store := openTestStore(t)
	record := testOperation("will-expire")
	record.RequestFingerprint = "sha256:" + strings.Repeat("a", 64)
	proof := IdempotencyProof{
		ActiveKeyVersion: "v1",
		Candidates:       []IdempotencyVerifier{{KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32)}},
	}
	scopes := []string{LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID)}
	accepted, replayed, err := store.AcceptOperation(record, proof, scopes)
	if err != nil || replayed {
		t.Fatalf("initial accept: replayed=%t err=%v", replayed, err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(operationsBucket).Delete([]byte(accepted.Record.OperationID))
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcceptOperation(record, proof, scopes); !errors.Is(err, ErrIdempotencyExpired) {
		t.Fatalf("missing operation record must not create a new operation: got %v", err)
	}
}

func TestLegacyNonemptyStoreRequiresExplicitMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucket(metadataBucket)
		if err != nil {
			return err
		}
		operations, err := tx.CreateBucket(operationsBucket)
		if err != nil {
			return err
		}
		if err := meta.Put(schemaKey, []byte("1")); err != nil {
			return err
		}
		return operations.Put([]byte("legacy-operation"), []byte("preserved"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBolt(path); !errors.Is(err, ErrStoreNeedsMigration) {
		t.Fatalf("nonempty schema v1 database: got %v, want ErrStoreNeedsMigration", err)
	}
	db, err = bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.View(func(tx *bolt.Tx) error {
		if got := string(tx.Bucket(metadataBucket).Get(schemaKey)); got != "1" {
			t.Fatalf("failed migration changed schema version: got %q", got)
		}
		if got := tx.Bucket(operationsBucket).Get([]byte("legacy-operation")); string(got) != "preserved" {
			t.Fatalf("failed migration changed legacy operation: got %q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMigratesSchemaV2ToLogicalServerCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v2.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucket(metadataBucket)
		if err != nil {
			return err
		}
		return meta.Put(schemaKey, []byte("2"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenBolt(path)
	if err != nil {
		t.Fatalf("open schema v2 database: %v", err)
	}
	defer store.Close()
	if err := store.db.View(func(tx *bolt.Tx) error {
		if got := string(tx.Bucket(metadataBucket).Get(schemaKey)); got != "5" {
			t.Fatalf("schema version was not advanced: got %q, want 5", got)
		}
		if tx.Bucket(logicalServersBucket) == nil {
			t.Fatal("logical server bucket was not created")
		}
		if tx.Bucket(capacityReservationsBucket) == nil {
			t.Fatal("capacity reservation bucket was not created")
		}
		if tx.Bucket(managementAPIKeysBucket) == nil {
			t.Fatal("management API key bucket was not created")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMigratesSchemaV3ToCapacityReservations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v3.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucket(metadataBucket)
		if err != nil {
			return err
		}
		if err := meta.Put(schemaKey, []byte("3")); err != nil {
			return err
		}
		_, err = tx.CreateBucket(logicalServersBucket)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := OpenBolt(path)
	if err != nil {
		t.Fatalf("open schema v3 database: %v", err)
	}
	defer store.Close()
	if err := store.db.View(func(tx *bolt.Tx) error {
		if got := string(tx.Bucket(metadataBucket).Get(schemaKey)); got != "5" {
			t.Fatalf("schema version was not advanced: got %q, want 5", got)
		}
		if tx.Bucket(capacityReservationsBucket) == nil {
			t.Fatal("capacity reservation bucket was not created")
		}
		if tx.Bucket(managementAPIKeysBucket) == nil {
			t.Fatal("management API key bucket was not created")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenMigratesSchemaV4ToManagementAPIKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema-v4.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucket(metadataBucket)
		if err != nil {
			return err
		}
		if err := meta.Put(schemaKey, []byte("4")); err != nil {
			return err
		}
		for _, name := range [][]byte{operationsBucket, idempotencyBucket, locksBucket, operationLocksBucket, keyVersionsBucket, logicalServersBucket, capacityReservationsBucket} {
			if _, err := tx.CreateBucket(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	opened, err := OpenBolt(path)
	if err != nil {
		t.Fatalf("open schema v4 database: %v", err)
	}
	defer opened.Close()
	if err := opened.db.View(func(tx *bolt.Tx) error {
		if got := string(tx.Bucket(metadataBucket).Get(schemaKey)); got != "5" {
			t.Fatalf("schema version was not advanced: got %q, want 5", got)
		}
		if tx.Bucket(managementAPIKeysBucket) == nil {
			t.Fatal("management API key bucket was not created")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsMissingResourceLockIndexes(t *testing.T) {
	for _, remove := range []string{"forward", "reverse"} {
		t.Run(remove, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "controller.db")
			store, err := OpenBolt(path)
			if err != nil {
				t.Fatal(err)
			}
			testLogicalServerFixture(t, store)
			record := testOperation("recovery-index-check")
			record.OperationType = "server.backup"
			record.RequestSnapshot.OperationType = record.OperationType
			record.RequestFingerprint = "sha256:" + strings.Repeat("b", 64)
			scope := LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID)
			proof := IdempotencyProof{
				ActiveKeyVersion: "v1",
				Candidates: []IdempotencyVerifier{{
					KeyVersion: "v1", Digest: bytes.Repeat([]byte{1}, 32), BindingDigest: bytes.Repeat([]byte{2}, 32),
				}},
			}
			if _, _, err := store.AcceptOperation(record, proof, []string{scope}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			db, err := bolt.Open(path, 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				if remove == "forward" {
					return tx.Bucket(locksBucket).Delete([]byte(scope))
				}
				return tx.Bucket(operationLocksBucket).Delete([]byte(record.OperationID))
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			if _, err := OpenBolt(path); !errors.Is(err, ErrCorruptStore) {
				t.Fatalf("reopen with missing %s index: got %v, want ErrCorruptStore", remove, err)
			}
		})
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
		OperationType:         "test.operation",
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
			OperationType: "test.operation",
			Parameters:    json.RawMessage(`{"plan":"small"}`),
		},
		Steps: []OperationStep{{
			StepID:   "validate",
			StepType: "request.validate",
			Status:   protocolstate.StepPending,
		}},
	}
}

func TestDefineRefusesAServerIDUsedInAnotherEnvironment(t *testing.T) {
	store := openTestStore(t)
	testLogicalServerFixture(t, store) // server-1 in staging
	operation := testOperation("define-in-production")
	operation.OperationType, operation.RequestSnapshot.OperationType = "server.define", "server.define"
	operation.Steps = nil
	operation.Target = OperationTarget{ServerID: "server-1", EnvironmentID: "production"}
	operation.RequestSnapshot.Parameters = json.RawMessage(`{"server_id":"server-1","environment_id":"production","display_name":"Prod","hosting_profile":"vultr-small","storage_profile":"wasabi-worlds","game":{"adapter_id":"game.paper","release_channel":"stable"},"execution":{"adapter_id":"execution.systemd","profile":"paper-default"}}`)
	operation.RequestFingerprint = "sha256:" + strings.Repeat("d", 64)
	proof := IdempotencyProof{ActiveKeyVersion: "v1", Candidates: []IdempotencyVerifier{{
		KeyVersion: "v1", Digest: bytes.Repeat([]byte{5}, 32), BindingDigest: bytes.Repeat([]byte{6}, 32)}}}
	if _, _, err := store.AcceptOperation(operation, proof, []string{LogicalServerLockScope("production", "server-1")}); !errors.Is(err, ErrLogicalServerAlreadyExists) {
		t.Fatalf("duplicate server ID in another environment: %v", err)
	}
}
