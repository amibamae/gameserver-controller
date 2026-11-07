// Package protocolstate implements the state-transition rules in the draft
// protocol. Workflow preconditions remain the caller's responsibility.
package protocolstate

import (
	"errors"
	"fmt"
)

type ServerState string

const (
	ServerUnprovisioned ServerState = "unprovisioned"
	ServerProvisioning  ServerState = "provisioning"
	ServerPreparing     ServerState = "preparing"
	ServerRestoring     ServerState = "restoring"
	ServerStarting      ServerState = "starting"
	ServerValidating    ServerState = "validating"
	ServerReady         ServerState = "ready"
	ServerDegraded      ServerState = "degraded"
	ServerDraining      ServerState = "draining"
	ServerStopping      ServerState = "stopping"
	ServerBackingUp     ServerState = "backing_up"
	ServerStopped       ServerState = "stopped"
	ServerDestroying    ServerState = "destroying"
	ServerDestroyed     ServerState = "destroyed"
	ServerFailed        ServerState = "failed"
)

type OperationStatus string

const (
	OperationPending               OperationStatus = "pending"
	OperationRunning               OperationStatus = "running"
	OperationWaiting               OperationStatus = "waiting"
	OperationCancellationRequested OperationStatus = "cancellation_requested"
	OperationReconciling           OperationStatus = "reconciling"
	OperationNeedsOperator         OperationStatus = "needs_operator"
	OperationSucceeded             OperationStatus = "succeeded"
	OperationFailed                OperationStatus = "failed"
	OperationCancelled             OperationStatus = "cancelled"
)

type StepStatus string

const (
	StepPending        StepStatus = "pending"
	StepRunning        StepStatus = "running"
	StepWaiting        StepStatus = "waiting"
	StepOutcomeUnknown StepStatus = "outcome_unknown"
	StepSucceeded      StepStatus = "succeeded"
	StepFailed         StepStatus = "failed"
	StepSkipped        StepStatus = "skipped"
	StepCancelled      StepStatus = "cancelled"
)

var ErrIllegalTransition = errors.New("illegal protocol state transition")

// TransitionError identifies a requested transition that the draft protocol
// does not permit.
type TransitionError struct {
	Kind string
	From string
	To   string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%s %q cannot transition to %q: %v", e.Kind, e.From, e.To, ErrIllegalTransition)
}

func (e *TransitionError) Unwrap() error { return ErrIllegalTransition }

var serverTransitions = map[ServerState]map[ServerState]struct{}{
	ServerUnprovisioned: transitions(ServerProvisioning),
	ServerProvisioning:  transitions(ServerPreparing, ServerFailed),
	ServerPreparing:     transitions(ServerRestoring, ServerStarting, ServerFailed),
	ServerRestoring:     transitions(ServerStarting, ServerStopped, ServerFailed),
	ServerStarting:      transitions(ServerValidating, ServerFailed),
	ServerValidating:    transitions(ServerReady, ServerDegraded, ServerFailed),
	ServerReady:         transitions(ServerDegraded, ServerDraining, ServerStopping),
	ServerDegraded:      transitions(ServerValidating, ServerDraining, ServerStopping, ServerFailed),
	ServerDraining:      transitions(ServerReady, ServerStopping, ServerFailed),
	ServerStopping:      transitions(ServerBackingUp, ServerStopped, ServerFailed),
	ServerBackingUp:     transitions(ServerStarting, ServerStopped, ServerDestroying, ServerFailed),
	ServerStopped:       transitions(ServerStarting, ServerRestoring, ServerBackingUp, ServerDestroying),
	ServerDestroying:    transitions(ServerDestroyed, ServerFailed),
	ServerDestroyed:     transitions(ServerProvisioning),
	ServerFailed:        transitions(ServerProvisioning, ServerPreparing, ServerRestoring, ServerStopping, ServerDestroying),
}

var operationTransitions = map[OperationStatus]map[OperationStatus]struct{}{
	OperationPending:               transitions(OperationRunning, OperationCancellationRequested, OperationCancelled, OperationFailed),
	OperationRunning:               transitions(OperationWaiting, OperationCancellationRequested, OperationReconciling, OperationNeedsOperator, OperationSucceeded, OperationFailed),
	OperationWaiting:               transitions(OperationRunning, OperationCancellationRequested, OperationReconciling, OperationNeedsOperator, OperationFailed),
	OperationCancellationRequested: transitions(OperationCancelled, OperationReconciling, OperationNeedsOperator, OperationSucceeded, OperationFailed),
	OperationReconciling:           transitions(OperationRunning, OperationWaiting, OperationNeedsOperator, OperationSucceeded, OperationFailed, OperationCancelled),
	OperationNeedsOperator:         transitions(OperationReconciling, OperationRunning, OperationFailed, OperationCancelled),
}

var stepTransitions = map[StepStatus]map[StepStatus]struct{}{
	StepPending:        transitions(StepRunning, StepSkipped, StepCancelled),
	StepRunning:        transitions(StepWaiting, StepOutcomeUnknown, StepSucceeded, StepFailed, StepCancelled),
	StepWaiting:        transitions(StepRunning, StepOutcomeUnknown, StepSucceeded, StepFailed, StepCancelled),
	StepOutcomeUnknown: transitions(StepRunning, StepWaiting, StepSucceeded, StepFailed),
}

func transitions[T comparable](to ...T) map[T]struct{} {
	allowed := make(map[T]struct{}, len(to))
	for _, status := range to {
		allowed[status] = struct{}{}
	}
	return allowed
}

// CanTransitionServer reports whether the draft protocol allows the state
// transition. It does not evaluate the transition's required evidence or
// authorization.
func CanTransitionServer(from, to ServerState) bool {
	return canTransition(serverTransitions, from, to)
}

// ValidateServerTransition returns an error for any transition not listed in
// protocol/lifecycle.md.
func ValidateServerTransition(from, to ServerState) error {
	if CanTransitionServer(from, to) {
		return nil
	}
	return &TransitionError{Kind: "server", From: string(from), To: string(to)}
}

// CanTransitionOperation reports whether the draft protocol allows the
// operation-status transition.
func CanTransitionOperation(from, to OperationStatus) bool {
	return canTransition(operationTransitions, from, to)
}

// ValidateOperationTransition returns an error for any transition not listed
// in protocol/operations.md. Terminal statuses have no outgoing transitions.
func ValidateOperationTransition(from, to OperationStatus) error {
	if CanTransitionOperation(from, to) {
		return nil
	}
	return &TransitionError{Kind: "operation", From: string(from), To: string(to)}
}

// IsTerminalOperation reports whether the status is terminal in the draft
// protocol.
func IsTerminalOperation(status OperationStatus) bool {
	return status == OperationSucceeded || status == OperationFailed || status == OperationCancelled
}

// CanTransitionStep reports whether the draft protocol allows the step-status
// transition.
func CanTransitionStep(from, to StepStatus) bool {
	return canTransition(stepTransitions, from, to)
}

// ValidateStepTransition returns an error for any transition not listed in
// protocol/operations.md. Terminal step statuses have no outgoing transitions.
func ValidateStepTransition(from, to StepStatus) error {
	if CanTransitionStep(from, to) {
		return nil
	}
	return &TransitionError{Kind: "step", From: string(from), To: string(to)}
}

func canTransition[T comparable](table map[T]map[T]struct{}, from, to T) bool {
	_, allowed := table[from][to]
	return allowed
}
