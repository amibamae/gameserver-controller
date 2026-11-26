package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These helpers support the round-5 characterization tests, which pin the
// engine's construction checks, lifecycle-projection routing, Run bounds,
// response checks and the exact error records it persists, so engine.go
// can be split safely.

// engineR5Store wraps the recording store from the advance tests. It keeps
// every full record written, counts reads, can list the current operation
// as recoverable, and in forgetful mode always reads back the seed and
// accepts every write, so Run can be driven to its transition limit.
type engineR5Store struct {
	*advanceRefactorStore
	records     []store.OperationRecord
	gets        int
	recoverable bool
	forget      *store.StoredOperation
}

func (s *engineR5Store) GetOperation(operationID string) (store.StoredOperation, error) {
	s.mu.Lock()
	s.gets++
	forget := s.forget
	s.mu.Unlock()
	if forget != nil {
		return advanceRefactorClone(*forget), nil
	}
	return s.advanceRefactorStore.GetOperation(operationID)
}

func (s *engineR5Store) UpdateOperation(expectedRevision uint64, next store.OperationRecord) (store.StoredOperation, error) {
	s.mu.Lock()
	s.records = append(s.records, advanceRefactorClone(store.StoredOperation{Record: next}).Record)
	forget := s.forget != nil
	s.mu.Unlock()
	if forget {
		return advanceRefactorClone(store.StoredOperation{Record: next, Revision: expectedRevision + 1}), nil
	}
	return s.advanceRefactorStore.UpdateOperation(expectedRevision, next)
}

func (s *engineR5Store) ListRecoverableOperations() ([]store.StoredOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.recoverable {
		return nil, nil
	}
	return []store.StoredOperation{advanceRefactorClone(s.current)}, nil
}

// engineR5Writes returns the summaries of every write attempt, in order.
func (s *engineR5Store) engineR5Writes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.writes...)
}

// engineR5Record returns the full record of write attempt index.
func (s *engineR5Store) engineR5Record(t *testing.T, index int) store.OperationRecord {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index >= len(s.records) {
		t.Fatalf("write %d not recorded; %d writes", index, len(s.records))
	}
	return s.records[index]
}

// engineR5LifecycleStore adds atomic logical-server commits and can fail the
// logical-server read.
type engineR5LifecycleStore struct {
	advanceRefactorLifecycleStore
	getErr error
}

func (s engineR5LifecycleStore) GetLogicalServer(environmentID, serverID string) (store.StoredLogicalServer, error) {
	if s.getErr != nil {
		return store.StoredLogicalServer{}, s.getErr
	}
	return s.advanceRefactorLifecycleStore.GetLogicalServer(environmentID, serverID)
}

// engineR5Schemas overrides the loaded protocol version, records each
// schema validation and fails the configured schema IDs.
type engineR5Schemas struct {
	schemaRuntime
	version   string
	failing   map[string]bool
	mu        *sync.Mutex
	validated *[]string
}

func engineR5NewSchemas(inner schemaRuntime, version string, failing ...string) engineR5Schemas {
	schemas := engineR5Schemas{schemaRuntime: inner, version: version, failing: map[string]bool{}, mu: &sync.Mutex{}, validated: &[]string{}}
	for _, id := range failing {
		schemas.failing[id] = true
	}
	return schemas
}

func (s engineR5Schemas) ProtocolVersion() string { return s.version }

func (s engineR5Schemas) ValidateJSON(schemaID string, value []byte) error {
	s.mu.Lock()
	*s.validated = append(*s.validated, schemaID)
	s.mu.Unlock()
	if s.failing[schemaID] {
		return errors.New("schema rejected " + schemaID)
	}
	if s.schemaRuntime == nil {
		return nil
	}
	return s.schemaRuntime.ValidateJSON(schemaID, value)
}

func (s engineR5Schemas) engineR5Validated() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(*s.validated, ",")
}

// engineR5Adapters replaces the adapter call while keeping the registered
// descriptors.
type engineR5Adapters struct {
	adapterRuntime
	invoke func(context.Context, string, adapter.InvokeRequest) (adapter.InvokeResponse, error)
}

func (a engineR5Adapters) Invoke(ctx context.Context, adapterID string, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return a.invoke(ctx, adapterID, request)
}

// engineR5Projector is a definition whose lifecycle projection is scripted.
type engineR5Projector struct {
	*testDefinition
	project func(store.OperationRecord, store.StoredLogicalServer) (store.LogicalServer, bool, error)
}

func (d engineR5Projector) ProjectLifecycle(operation store.OperationRecord, current store.StoredLogicalServer) (store.LogicalServer, bool, error) {
	return d.project(operation, current)
}

// engineR5CompletionDefinition rejects completion with err.
type engineR5CompletionDefinition struct {
	*testDefinition
	err error
}

func (d engineR5CompletionDefinition) ValidateCompletion(context.Context, store.OperationRecord) error {
	return d.err
}

// engineR5HandlerDefinition answers a failed precondition with a scripted
// resolution and error.
type engineR5HandlerDefinition struct {
	*testDefinition
	resolution Resolution
	err        error
}

func (d engineR5HandlerDefinition) HandlePreconditionFailure(context.Context, store.OperationRecord, store.OperationStep, error) (Resolution, error) {
	return d.resolution, d.err
}

// engineR5Options configures one engine wired to an engineR5Store.
type engineR5Options struct {
	capabilityVersion string
	mutating          bool
	replies           []fakeadapter.Reply
	extraSteps        []store.OperationStep
	seed              func(*store.OperationRecord)
	maxStepAttempts   int
	forget            bool
	recoverable       bool
}

// engineR5Env is one engine wired to a recording store.
type engineR5Env struct {
	fixture engineFixture
	store   *engineR5Store
	engine  *Engine
	ctx     context.Context
	cancel  context.CancelFunc
	id      string
}

func engineR5NewEnv(t *testing.T, options engineR5Options) *engineR5Env {
	t.Helper()
	version := options.capabilityVersion
	if version == "" {
		version = "1"
	}
	fixture := newEngineFixtureForOperation(t, "engine-r5", version, options.replies, options.mutating, testOperationType, false, options.extraSteps...)
	seeded := advanceRefactorClone(fixture.operation)
	seeded.Record.CreatedAt, seeded.Record.UpdatedAt, seeded.Record.StartedAt = time.Time{}, time.Time{}, nil
	if options.seed != nil {
		options.seed(&seeded.Record)
	}
	recording := &engineR5Store{advanceRefactorStore: &advanceRefactorStore{current: seeded}, recoverable: options.recoverable}
	if options.forget {
		recording.forget = &seeded
	}
	attempts := options.maxStepAttempts
	if attempts == 0 {
		attempts = 3
	}
	definitions := NewDefinitions()
	if err := definitions.Register(testOperationType, fixture.definition); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(recording, fixture.engine.adapters, fixture.engine.schemas, definitions, fixture.authorizer, time.Minute, attempts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &engineR5Env{fixture: fixture, store: recording, engine: engine, ctx: ctx, cancel: cancel, id: seeded.Record.OperationID}
}

// engineR5Replace registers definition as the only test workflow.
func engineR5Replace(t *testing.T, env *engineR5Env, definition Definition) {
	t.Helper()
	definitions := NewDefinitions()
	if err := definitions.Register(testOperationType, definition); err != nil {
		t.Fatal(err)
	}
	env.engine.definitions = definitions
}

// engineR5Completed is a scripted adapter reply that completes the step.
func engineR5Completed(count int) []fakeadapter.Reply {
	replies := make([]fakeadapter.Reply, count)
	for i := range replies {
		replies[i] = fakeadapter.Reply{Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ObservedAt: time.Now().UTC()}
	}
	return replies
}

// engineR5ExpectError compares a persisted error with the exact record.
func engineR5ExpectError(t *testing.T, label string, got, want json.RawMessage) {
	t.Helper()
	if string(got) != string(want) {
		t.Errorf("%s:\n  got  %s\n  want %s", label, got, want)
	}
}

// engineR5ExpectWrites compares the ordered write summaries.
func engineR5ExpectWrites(t *testing.T, env *engineR5Env, want ...string) {
	t.Helper()
	got := env.store.engineR5Writes()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("durable writes:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// engineR5ExpectErr compares an error's text; want "" means no error.
func engineR5ExpectErr(t *testing.T, err error, want string) {
	t.Helper()
	got := ""
	if err != nil {
		got = err.Error()
	}
	if got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

// engineR5Seed seeds statuses like advanceRefactorSeed.
func engineR5Seed(operation protocolstate.OperationStatus, current string, steps ...protocolstate.StepStatus) func(*store.OperationRecord) {
	return advanceRefactorSeed(operation, current, steps...)
}
