// Package workflow advances durable protocol operations through schema-checked
// adapter calls. Operation-specific safety policy and reconciliation remain
// explicit through registered definitions.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"github.com/amibamae/gameserver-controller/internal/store"
)

var (
	ErrOperationInProgress = errors.New("operation already has an active dispatcher")
	ErrNeedsOperator       = errors.New("operation is suspended for operator intervention")
	ErrNotRecoverable      = errors.New("operation cannot be advanced from its current state")
	operationTypePattern   = regexp.MustCompile(`^[a-z][a-z0-9.]{1,159}$`)
)

type ResolutionState string

const (
	ResolutionCompleted       ResolutionState = "completed"
	ResolutionPending         ResolutionState = "pending"
	ResolutionSafeToRetry     ResolutionState = "safe_to_retry"
	ResolutionDefinitiveError ResolutionState = "definitive_error"
	ResolutionNeedsOperator   ResolutionState = "needs_operator"
)

// Resolution is returned only after operation-specific code has inspected the
// external state. SafeToRetry is an explicit assertion that the prior attempt
// did not take effect or that the capability makes repetition safe.
type Resolution struct {
	State                ResolutionState
	Output               json.RawMessage
	ExternalOperationRef json.RawMessage
	Error                json.RawMessage
}

// Definition supplies operation-specific workflow policy. The engine always
// runs CheckPreconditions before dispatch and never automatically guesses a
// reconciliation result.
type Definition interface {
	CheckPreconditions(context.Context, store.OperationRecord, store.OperationStep) error
	Reconcile(context.Context, store.OperationRecord, store.OperationStep) (Resolution, error)
	BuildSuccessResult(context.Context, store.OperationRecord) (json.RawMessage, error)
	BuildFailureResult(context.Context, store.OperationRecord, store.OperationStep, json.RawMessage) (json.RawMessage, error)
}

// CompletionValidator checks operation postconditions after every planned
// adapter step succeeded. It lets a definition fail a completed observation
// safely without rewriting an adapter step's immutable successful output.
type CompletionValidator interface {
	ValidateCompletion(context.Context, store.OperationRecord) error
}

// PreconditionFailureHandler distinguishes a definitive, observed blocker from
// an unresolved precondition. A definitive result may fail the operation and
// release its lock; uncertain failures remain suspended for operator review.
type PreconditionFailureHandler interface {
	HandlePreconditionFailure(context.Context, store.OperationRecord, store.OperationStep, error) (Resolution, error)
}

// DispatchGuard runs after a step is durably marked running and immediately
// before the adapter call. It is for controller-owned claims that must precede
// an external side effect, such as consuming a managed-capacity create token.
// An error prevents the adapter call and fails the operation.
type DispatchGuard interface {
	BeforeAdapterDispatch(context.Context, store.OperationRecord, store.OperationStep) error
}

// LifecycleProjector derives a logical-server record from durable operation
// progress. It is called for every persisted operation update; when it reports
// a state change, the engine commits the operation and state event atomically.
// Implementations must be deterministic and must not perform external effects.
type LifecycleProjector interface {
	ProjectLifecycle(store.OperationRecord, store.StoredLogicalServer) (store.LogicalServer, bool, error)
}

type Definitions struct {
	mu     sync.RWMutex
	values map[string]Definition
}

func NewDefinitions() *Definitions {
	return &Definitions{values: make(map[string]Definition)}
}

func (d *Definitions) Register(operationType string, definition Definition) error {
	if d == nil || !operationTypePattern.MatchString(operationType) || definition == nil {
		return errors.New("valid operation type and workflow definition are required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.values[operationType]; exists {
		return fmt.Errorf("workflow definition %q is already registered", operationType)
	}
	d.values[operationType] = definition
	return nil
}

func (d *Definitions) get(operationType string) (Definition, bool) {
	if d == nil {
		return nil, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	definition, ok := d.values[operationType]
	return definition, ok
}

// ResumeAuthorizer is required to leave needs_operator. It should verify the
// authenticated operator and current approval/policy for the exact operation.
type ResumeAuthorizer interface {
	AuthorizeResume(context.Context, string, store.OperationRecord) error
}
