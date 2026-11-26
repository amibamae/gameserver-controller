package workflow

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type adapterRuntime interface {
	ProtocolVersion() string
	Descriptor(string) (adapter.Descriptor, error)
	Invoke(context.Context, string, adapter.InvokeRequest) (adapter.InvokeResponse, error)
}

type schemaRuntime interface {
	ProtocolVersion() string
	HasSchema(string) bool
	ValidateJSON(string, []byte) error
}

// Engine advances one persisted step at a time. A step is marked running in
// the store before Invoke is called. Interrupted calls become outcome_unknown
// and require operation-specific reconciliation before retry.
type Engine struct {
	store            store.OperationStore
	adapters         adapterRuntime
	schemas          schemaRuntime
	definitions      *Definitions
	resumeAuthorizer ResumeAuthorizer
	stepTimeout      time.Duration
	maxStepAttempts  int
	activeMu         sync.Mutex
	activeOperations map[string]bool
}

// NewEngine checks that every dependency is present before it checks the
// protocol version and limits, so a missing dependency is reported first.
// Its seven positional parameters are kept because callers outside this
// package depend on the exported signature.
func NewEngine(operationStore store.OperationStore, adapters adapterRuntime, schemas schemaRuntime, definitions *Definitions, resumeAuthorizer ResumeAuthorizer, stepTimeout time.Duration, maxStepAttempts int) (*Engine, error) {
	engine := &Engine{
		store: operationStore, adapters: adapters, schemas: schemas, definitions: definitions,
		resumeAuthorizer: resumeAuthorizer, stepTimeout: stepTimeout, maxStepAttempts: maxStepAttempts,
		activeOperations: make(map[string]bool),
	}
	if engine.missingDependency() {
		return nil, errors.New("operation store, adapter registry, schemas, workflow definitions, and resume authorizer are required")
	}
	if !engine.limitsValid() {
		return nil, errors.New("protocol version, step timeout (1ns..24h), and maximum step attempts (1..10) are required")
	}
	return engine, nil
}

// missingDependency reports whether any injected dependency is nil.
func (e *Engine) missingDependency() bool {
	if e.store == nil || e.adapters == nil || e.schemas == nil {
		return true
	}
	return e.definitions == nil || e.resumeAuthorizer == nil
}

// limitsValid reports whether a protocol version is loaded, the step timeout
// is within 1ns..24h and the attempt limit within 1..10.
func (e *Engine) limitsValid() bool {
	if e.schemas.ProtocolVersion() == "" {
		return false
	}
	return stepTimeoutInRange(e.stepTimeout) && stepAttemptsInRange(e.maxStepAttempts)
}

func stepTimeoutInRange(timeout time.Duration) bool {
	return timeout > 0 && timeout <= 24*time.Hour
}

func stepAttemptsInRange(attempts int) bool {
	return attempts >= 1 && attempts <= 10
}

// Advance advances one durable workflow boundary. An uncertain step is
// reconciled before it can be retried; dispatch after reconciliation is allowed
// only when the definition explicitly reports that retry is safe.
func (e *Engine) Advance(ctx context.Context, operationID string) (store.StoredOperation, error) {
	if ctx == nil || operationID == "" {
		return store.StoredOperation{}, errors.New("context and operation ID are required")
	}
	if !e.begin(operationID) {
		return store.StoredOperation{}, ErrOperationInProgress
	}
	defer e.end(operationID)
	return e.advanceLocked(ctx, operationID)
}

// Run repeatedly advances synchronous steps until the operation completes,
// waits for an external effect, or requires operator intervention.
func (e *Engine) Run(ctx context.Context, operationID string) (store.StoredOperation, error) {
	if ctx == nil || operationID == "" {
		return store.StoredOperation{}, errors.New("context and operation ID are required")
	}
	stored, err := e.store.GetOperation(operationID)
	if err != nil {
		return store.StoredOperation{}, err
	}
	limit := e.transitionLimit(len(stored.Record.Steps))
	for i := 0; i < limit; i++ {
		stored, err = e.Advance(ctx, operationID)
		if err != nil {
			return stored, err
		}
		if runSettled(stored.Record.Status) {
			return stored, nil
		}
		if err := ctx.Err(); err != nil {
			return stored, err
		}
	}
	return e.suspend(stored, "core.workflow.transition_limit", "Workflow advancement exceeded its bounded transition count.")
}

// transitionLimit bounds Run: every attempt of every step, one more
// advancement per step, four for start and completion, and never below 8.
func (e *Engine) transitionLimit(stepCount int) int {
	limit := e.maxStepAttempts*stepCount + stepCount + 4
	if limit < 8 {
		return 8
	}
	return limit
}

// runSettled reports whether Run stops at status: the operation is
// terminal, waits for an external effect, or needs an operator.
func runSettled(status protocolstate.OperationStatus) bool {
	if protocolstate.IsTerminalOperation(status) {
		return true
	}
	return status == protocolstate.OperationWaiting || status == protocolstate.OperationNeedsOperator
}

func (e *Engine) begin(operationID string) bool {
	e.activeMu.Lock()
	defer e.activeMu.Unlock()
	if e.activeOperations[operationID] {
		return false
	}
	e.activeOperations[operationID] = true
	return true
}

func (e *Engine) end(operationID string) {
	e.activeMu.Lock()
	delete(e.activeOperations, operationID)
	e.activeMu.Unlock()
}
