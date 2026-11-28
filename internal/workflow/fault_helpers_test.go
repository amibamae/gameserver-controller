package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/simulation"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// Shared fixtures for the requirement-derived fault-injection tests in
// fault_*_test.go. Expected behaviour comes from the lifecycle safety
// invariants (persist before a mutating call, reconcile before a retry, one
// provider mutation per operation), not from the engine's implementation.
// Every provider here is an in-process scripted fake; nothing contacts a
// real provider, and nothing here is live verification.

// errSimulatedCrash is what a crashed controller's store returns: the
// process is gone, so no later write of that process can land.
var errSimulatedCrash = errors.New("simulated controller crash")

// faultProvider is a provider simulator around a scripted adapter. Before
// each call it asks the fault plan for a fault on the capability. A fault
// normally fires before the provider acts; with lostAfterEffect it fires
// after the provider acted, which models a response lost on the way back.
// effects counts provider mutations that really happened.
type faultProvider struct {
	inner           *fakeadapter.Scripted
	lostAfterEffect bool
	observe         func(adapter.InvokeRequest)
	mu              sync.Mutex
	plan            *simulation.FaultPlan
	effects         map[string]int
}

func newFaultProvider(inner *fakeadapter.Scripted) *faultProvider {
	return &faultProvider{inner: inner, plan: simulation.NewFaultPlan(), effects: map[string]int{}}
}

func (p *faultProvider) Descriptor() adapter.Descriptor { return p.inner.Descriptor() }

func (p *faultProvider) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if p.observe != nil {
		p.observe(request)
	}
	capability := request.Params.Capability
	p.mu.Lock()
	fault := p.plan.Next(capability)
	p.mu.Unlock()
	if fault != nil && !p.lostAfterEffect {
		return adapter.InvokeResponse{}, fault
	}
	response, err := p.inner.Invoke(ctx, request)
	if err == nil {
		p.mu.Lock()
		p.effects[capability]++
		p.mu.Unlock()
	}
	if fault != nil {
		return adapter.InvokeResponse{}, fault
	}
	return response, err
}

// schedule makes the given attempt of capability fail with fault.
func (p *faultProvider) schedule(t *testing.T, capability string, attempt int, fault simulation.Fault) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.plan.Schedule(capability, attempt, fault); err != nil {
		t.Fatal(err)
	}
}

// effectCount reports how many provider mutations capability really made.
func (p *faultProvider) effectCount(capability string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effects[capability]
}

// crashPoint selects the durable write at which a controller process dies.
// before: the write never lands. after: the write lands, then the process
// dies before it can act on the result.
type crashPoint struct {
	before func(store.OperationRecord) bool
	after  func(store.OperationRecord) bool
}

// crashingStore is one controller process's view of the shared Bolt store.
// Once the process has crashed every call fails, as a dead process makes no
// further reads or writes.
type crashingStore struct {
	*store.BoltStore
	point   crashPoint
	mu      sync.Mutex
	crashed bool
}

func (s *crashingStore) GetOperation(operationID string) (store.StoredOperation, error) {
	if s.hasCrashed() {
		return store.StoredOperation{}, errSimulatedCrash
	}
	return s.BoltStore.GetOperation(operationID)
}

func (s *crashingStore) UpdateOperation(expectedRevision uint64, next store.OperationRecord) (store.StoredOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.crashed {
		return store.StoredOperation{}, errSimulatedCrash
	}
	if s.point.before != nil && s.point.before(next) {
		s.crashed = true
		return store.StoredOperation{}, errSimulatedCrash
	}
	updated, err := s.BoltStore.UpdateOperation(expectedRevision, next)
	if err == nil && s.point.after != nil && s.point.after(next) {
		s.crashed = true
		return store.StoredOperation{}, errSimulatedCrash
	}
	return updated, err
}

func (s *crashingStore) hasCrashed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.crashed
}

// faultEnv is a generic one-step mutating workflow whose adapter is a
// faultProvider and whose controller processes share one durable store.
type faultEnv struct {
	fixture  engineFixture
	provider *faultProvider
	registry *adapter.Registry
	id       string
}

// newFaultEnv accepts the engine fixture's one-step operation and routes
// its adapter through a faultProvider scripted with replies.
func newFaultEnv(t *testing.T, replies ...fakeadapter.Reply) *faultEnv {
	t.Helper()
	fixture := newEngineFixture(t, "fault-operation", "1", replies)
	provider := newFaultProvider(fixture.adapter)
	registry, err := adapter.NewRegistry(workflowTestSchemas(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	return &faultEnv{fixture: fixture, provider: provider, registry: registry, id: fixture.operation.Record.OperationID}
}

// startController starts a fresh controller process over the shared store:
// a new Engine with no in-memory state that dies at point.
func (e *faultEnv) startController(t *testing.T, point crashPoint) *Engine {
	t.Helper()
	process := &crashingStore{BoltStore: e.fixture.store, point: point}
	engine, err := NewEngine(process, e.registry, e.fixture.engine.schemas, e.fixture.engine.definitions, e.fixture.authorizer, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

// stored reads the operation's durable record.
func (e *faultEnv) stored(t *testing.T) store.StoredOperation {
	t.Helper()
	stored, err := e.fixture.store.GetOperation(e.id)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// providerCalls counts the calls that reached the scripted provider.
func (e *faultEnv) providerCalls() int { return len(e.fixture.adapter.Calls()) }

// reconcileFromProvider makes reconciliation ask the provider simulator what
// really happened: an effect means the step completed, none means a retry is
// safe. It counts reconciliations.
func (e *faultEnv) reconcileFromProvider(count *int) {
	e.fixture.definition.reconcileFn = func(context.Context, store.OperationRecord, store.OperationStep) (Resolution, error) {
		*count++
		if e.provider.effectCount(testCapability) > 0 {
			return Resolution{State: ResolutionCompleted, Output: json.RawMessage(testExternalRef)}, nil
		}
		return Resolution{State: ResolutionSafeToRetry}, nil
	}
}

// requireDurableClaimAtDispatch records a violation whenever the provider is
// called before the store holds the step as running for that exact attempt.
func (e *faultEnv) requireDurableClaimAtDispatch(violations *[]string) {
	e.provider.observe = func(request adapter.InvokeRequest) {
		stored, err := e.fixture.store.GetOperation(request.Params.RequestContext.OperationID)
		if err != nil {
			*violations = append(*violations, err.Error())
			return
		}
		step := stored.Record.Steps[0]
		if step.Status != protocolstate.StepRunning || adapterRequestID(e.id, step.StepID, step.Attempt) != request.ID {
			*violations = append(*violations, fmt.Sprintf("dispatch of %s while the durable step is %s at attempt %d", request.ID, step.Status, step.Attempt))
		}
	}
}

// isClaim reports whether next claims the first step for its first attempt.
func isClaim(next store.OperationRecord) bool {
	return next.Steps[0].Status == protocolstate.StepRunning && next.Steps[0].Attempt == 1
}

// isStepResult reports whether next records the first step's success.
func isStepResult(next store.OperationRecord) bool {
	return next.Steps[0].Status == protocolstate.StepSucceeded
}

// completedReplies returns count scripted replies that complete the step.
func completedReplies(count int) []fakeadapter.Reply { return engineR5Completed(count) }

// errorMessage returns the message of a persisted protocol error, or ""
// when none is recorded.
func errorMessage(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var decoded struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(value, &decoded) != nil {
		return "undecodable"
	}
	return decoded.Message
}

// faultState is the durable outcome a fault test pins.
type faultState struct {
	Operation protocolstate.OperationStatus
	Step      protocolstate.StepStatus
	Attempt   int
	ErrorCode string
	Calls     int
	Effects   int
}

func (e *faultEnv) state(t *testing.T) faultState {
	t.Helper()
	stored := e.stored(t)
	return faultState{
		Operation: stored.Record.Status, Step: stored.Record.Steps[0].Status, Attempt: stored.Record.Steps[0].Attempt,
		ErrorCode: errorCode(stored.Record.Error), Calls: e.providerCalls(), Effects: e.provider.effectCount(testCapability),
	}
}

func expectFaultState(t *testing.T, label string, got, want faultState) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n  got  %+v\n  want %+v", label, got, want)
	}
}

func expectErrIs(t *testing.T, label string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Errorf("%s: error = %v, want %v", label, err, want)
	}
}
