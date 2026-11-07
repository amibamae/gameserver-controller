package protocolstate

import (
	"errors"
	"testing"
)

func TestServerTransitionsMatchDraftTable(t *testing.T) {
	valid := [][2]ServerState{
		{ServerUnprovisioned, ServerProvisioning},
		{ServerProvisioning, ServerPreparing}, {ServerProvisioning, ServerFailed},
		{ServerPreparing, ServerRestoring}, {ServerPreparing, ServerStarting}, {ServerPreparing, ServerFailed},
		{ServerRestoring, ServerStarting}, {ServerRestoring, ServerStopped}, {ServerRestoring, ServerFailed},
		{ServerStarting, ServerValidating}, {ServerStarting, ServerFailed},
		{ServerValidating, ServerReady}, {ServerValidating, ServerDegraded}, {ServerValidating, ServerFailed},
		{ServerReady, ServerDegraded}, {ServerReady, ServerDraining}, {ServerReady, ServerStopping},
		{ServerDegraded, ServerValidating}, {ServerDegraded, ServerDraining}, {ServerDegraded, ServerStopping}, {ServerDegraded, ServerFailed},
		{ServerDraining, ServerReady}, {ServerDraining, ServerStopping}, {ServerDraining, ServerFailed},
		{ServerStopping, ServerBackingUp}, {ServerStopping, ServerStopped}, {ServerStopping, ServerFailed},
		{ServerBackingUp, ServerStarting}, {ServerBackingUp, ServerStopped}, {ServerBackingUp, ServerDestroying}, {ServerBackingUp, ServerFailed},
		{ServerStopped, ServerStarting}, {ServerStopped, ServerRestoring}, {ServerStopped, ServerBackingUp}, {ServerStopped, ServerDestroying},
		{ServerDestroying, ServerDestroyed}, {ServerDestroying, ServerFailed},
		{ServerDestroyed, ServerProvisioning},
		{ServerFailed, ServerProvisioning}, {ServerFailed, ServerPreparing}, {ServerFailed, ServerRestoring}, {ServerFailed, ServerStopping}, {ServerFailed, ServerDestroying},
	}
	assertExactTransitions(t, serverStates, valid, CanTransitionServer)
}

func TestOperationTransitionsMatchDraftTable(t *testing.T) {
	valid := [][2]OperationStatus{
		{OperationPending, OperationRunning}, {OperationPending, OperationCancellationRequested}, {OperationPending, OperationCancelled}, {OperationPending, OperationFailed},
		{OperationRunning, OperationWaiting}, {OperationRunning, OperationCancellationRequested}, {OperationRunning, OperationReconciling}, {OperationRunning, OperationNeedsOperator}, {OperationRunning, OperationSucceeded}, {OperationRunning, OperationFailed},
		{OperationWaiting, OperationRunning}, {OperationWaiting, OperationCancellationRequested}, {OperationWaiting, OperationReconciling}, {OperationWaiting, OperationNeedsOperator}, {OperationWaiting, OperationFailed},
		{OperationCancellationRequested, OperationCancelled}, {OperationCancellationRequested, OperationReconciling}, {OperationCancellationRequested, OperationNeedsOperator}, {OperationCancellationRequested, OperationSucceeded}, {OperationCancellationRequested, OperationFailed},
		{OperationReconciling, OperationRunning}, {OperationReconciling, OperationWaiting}, {OperationReconciling, OperationNeedsOperator}, {OperationReconciling, OperationSucceeded}, {OperationReconciling, OperationFailed}, {OperationReconciling, OperationCancelled},
		{OperationNeedsOperator, OperationReconciling}, {OperationNeedsOperator, OperationRunning}, {OperationNeedsOperator, OperationFailed}, {OperationNeedsOperator, OperationCancelled},
	}
	assertExactTransitions(t, operationStatuses, valid, CanTransitionOperation)

	for _, status := range []OperationStatus{OperationSucceeded, OperationFailed, OperationCancelled} {
		if !IsTerminalOperation(status) {
			t.Errorf("%q must be terminal", status)
		}
		for _, next := range operationStatuses {
			if CanTransitionOperation(status, next) {
				t.Errorf("terminal status %q must not transition to %q", status, next)
			}
		}
	}
	if IsTerminalOperation(OperationNeedsOperator) {
		t.Error("needs_operator is suspended, not terminal")
	}
}

func TestStepTransitionsMatchDraftTable(t *testing.T) {
	valid := [][2]StepStatus{
		{StepPending, StepRunning}, {StepPending, StepSkipped}, {StepPending, StepCancelled},
		{StepRunning, StepWaiting}, {StepRunning, StepOutcomeUnknown}, {StepRunning, StepSucceeded}, {StepRunning, StepFailed}, {StepRunning, StepCancelled},
		{StepWaiting, StepRunning}, {StepWaiting, StepOutcomeUnknown}, {StepWaiting, StepSucceeded}, {StepWaiting, StepFailed}, {StepWaiting, StepCancelled},
		{StepOutcomeUnknown, StepRunning}, {StepOutcomeUnknown, StepWaiting}, {StepOutcomeUnknown, StepSucceeded}, {StepOutcomeUnknown, StepFailed},
	}
	assertExactTransitions(t, stepStatuses, valid, CanTransitionStep)
}

func TestTransitionValidationReturnsTypedError(t *testing.T) {
	err := ValidateServerTransition(ServerReady, ServerDestroyed)
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("expected ErrIllegalTransition, got %v", err)
	}
	var transitionErr *TransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatalf("expected *TransitionError, got %T", err)
	}
	if transitionErr.Kind != "server" || transitionErr.From != string(ServerReady) || transitionErr.To != string(ServerDestroyed) {
		t.Fatalf("unexpected transition error: %#v", transitionErr)
	}

	for _, test := range []struct {
		name string
		err  error
	}{
		{"operation", ValidateOperationTransition(OperationSucceeded, OperationRunning)},
		{"step", ValidateStepTransition(StepSucceeded, StepRunning)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !errors.Is(test.err, ErrIllegalTransition) {
				t.Fatalf("expected ErrIllegalTransition, got %v", test.err)
			}
		})
	}
}

var serverStates = []ServerState{
	ServerUnprovisioned, ServerProvisioning, ServerPreparing, ServerRestoring, ServerStarting,
	ServerValidating, ServerReady, ServerDegraded, ServerDraining, ServerStopping,
	ServerBackingUp, ServerStopped, ServerDestroying, ServerDestroyed, ServerFailed,
}

var operationStatuses = []OperationStatus{
	OperationPending, OperationRunning, OperationWaiting, OperationCancellationRequested,
	OperationReconciling, OperationNeedsOperator, OperationSucceeded, OperationFailed, OperationCancelled,
}

var stepStatuses = []StepStatus{
	StepPending, StepRunning, StepWaiting, StepOutcomeUnknown, StepSucceeded, StepFailed, StepSkipped, StepCancelled,
}

func assertExactTransitions[T comparable](
	t *testing.T,
	states []T,
	valid [][2]T,
	canTransition func(T, T) bool,
) {
	t.Helper()
	expected := make(map[[2]T]struct{}, len(valid))
	for _, pair := range valid {
		expected[pair] = struct{}{}
		if !canTransition(pair[0], pair[1]) {
			t.Errorf("expected transition %v -> %v to be allowed", pair[0], pair[1])
		}
	}
	for _, from := range states {
		for _, to := range states {
			_, want := expected[[2]T{from, to}]
			if got := canTransition(from, to); got != want {
				t.Errorf("transition %v -> %v: got allowed=%t, want %t", from, to, got, want)
			}
		}
	}
	var unknown T
	if canTransition(unknown, states[0]) || canTransition(states[0], unknown) {
		t.Errorf("unknown state must not be transitionable")
	}
}
