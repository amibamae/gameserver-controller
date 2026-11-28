package workflow

import (
	"context"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/simulation"
)

// TestFaultControllerCrashAroundDispatch kills the controller at each
// durable boundary around one mutating provider call, restarts it over the
// same store, and requires that the provider is mutated exactly once:
// nothing is dispatched without a durable claim, and a step whose outcome
// the dead process never recorded is reconciled before anything is retried.
func TestFaultControllerCrashAroundDispatch(t *testing.T) {
	tests := []struct {
		name string
		// point is where the first controller process dies.
		point crashPoint
		// afterCrash is the durable state the dead process left behind.
		afterCrash faultState
		// recovered is the state after the restarted process recovers.
		recovered faultState
		// recoverMessage is the persisted reason recovery records.
		recoverMessage string
		// final is the state after the restarted process runs the operation.
		final           faultState
		reconciliations int
	}{
		{
			name:  "crash before the claim is durable",
			point: crashPoint{before: isClaim},
			afterCrash: faultState{Operation: protocolstate.OperationRunning, Step: protocolstate.StepPending,
				Calls: 0, Effects: 0},
			recovered: faultState{Operation: protocolstate.OperationRunning, Step: protocolstate.StepPending},
			final: faultState{Operation: protocolstate.OperationSucceeded, Step: protocolstate.StepSucceeded,
				Attempt: 1, Calls: 1, Effects: 1},
		},
		{
			name:  "crash after the claim is durable and before dispatch",
			point: crashPoint{after: isClaim},
			afterCrash: faultState{Operation: protocolstate.OperationRunning, Step: protocolstate.StepRunning,
				Attempt: 1},
			recovered: faultState{Operation: protocolstate.OperationReconciling, Step: protocolstate.StepOutcomeUnknown,
				Attempt: 1, ErrorCode: "core.adapter.outcome_unknown"},
			recoverMessage: "The controller restarted while this adapter step was running.",
			final: faultState{Operation: protocolstate.OperationSucceeded, Step: protocolstate.StepSucceeded,
				Attempt: 2, Calls: 1, Effects: 1},
			reconciliations: 1,
		},
		{
			name:  "crash after the provider created the resource and before its result is durable",
			point: crashPoint{before: isStepResult},
			afterCrash: faultState{Operation: protocolstate.OperationRunning, Step: protocolstate.StepRunning,
				Attempt: 1, Calls: 1, Effects: 1},
			recovered: faultState{Operation: protocolstate.OperationReconciling, Step: protocolstate.StepOutcomeUnknown,
				Attempt: 1, ErrorCode: "core.adapter.outcome_unknown", Calls: 1, Effects: 1},
			recoverMessage: "The controller restarted while this adapter step was running.",
			final: faultState{Operation: protocolstate.OperationSucceeded, Step: protocolstate.StepSucceeded,
				Attempt: 1, Calls: 1, Effects: 1},
			reconciliations: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Two completing replies: a blind repeat would succeed and
			// create a second resource instead of failing loudly.
			env := newFaultEnv(t, completedReplies(2)...)
			var violations []string
			env.requireDurableClaimAtDispatch(&violations)
			reconciliations := 0
			env.reconcileFromProvider(&reconciliations)

			_, err := env.startController(t, test.point).Run(context.Background(), env.id)
			expectErrIs(t, "crashed run", err, errSimulatedCrash)
			expectFaultState(t, "after crash", env.state(t), test.afterCrash)

			restarted := env.startController(t, crashPoint{})
			if _, err := restarted.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			expectFaultState(t, "after recovery", env.state(t), test.recovered)
			if test.recoverMessage != "" {
				if got := errorMessage(env.stored(t).Record.Error); got != test.recoverMessage {
					t.Errorf("recovery message = %q, want %q", got, test.recoverMessage)
				}
			}

			if _, err := restarted.Run(context.Background(), env.id); err != nil {
				t.Fatal(err)
			}
			expectFaultState(t, "after restart", env.state(t), test.final)
			if reconciliations != test.reconciliations {
				t.Errorf("reconciliations = %d, want %d", reconciliations, test.reconciliations)
			}
			for _, violation := range violations {
				t.Errorf("persist-before-dispatch violated: %s", violation)
			}
		})
	}
}

// TestFaultUnclearProviderAnswerIsReconciledBeforeRetry injects provider
// faults into the first mutating call. Whether or not the provider acted, the
// step must be reconciled against the provider's real state: a lost response
// after a successful creation completes without a second creation, and a
// call the provider never received is retried once.
func TestFaultUnclearProviderAnswerIsReconciledBeforeRetry(t *testing.T) {
	tests := []struct {
		name            string
		fault           simulation.Fault
		lostAfterEffect bool
		want            faultState
	}{
		{name: "timeout after the provider created the resource", fault: simulation.Timeout(), lostAfterEffect: true,
			want: faultState{Operation: protocolstate.OperationSucceeded, Step: protocolstate.StepSucceeded, Attempt: 1, Calls: 1, Effects: 1}},
		{name: "connection lost after the provider created the resource", fault: simulation.NetworkError("connection reset"), lostAfterEffect: true,
			want: faultState{Operation: protocolstate.OperationSucceeded, Step: protocolstate.StepSucceeded, Attempt: 1, Calls: 1, Effects: 1}},
		{name: "timeout before the provider received the call", fault: simulation.Timeout(),
			want: faultState{Operation: protocolstate.OperationSucceeded, Step: protocolstate.StepSucceeded, Attempt: 2, Calls: 1, Effects: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newFaultEnv(t, completedReplies(2)...)
			env.provider.lostAfterEffect = test.lostAfterEffect
			env.provider.schedule(t, testCapability, 1, test.fault)
			reconciliations := 0
			env.reconcileFromProvider(&reconciliations)

			if _, err := env.startController(t, crashPoint{}).Run(context.Background(), env.id); err != nil {
				t.Fatal(err)
			}
			expectFaultState(t, "after run", env.state(t), test.want)
			if reconciliations != 1 {
				t.Errorf("reconciliations = %d, want exactly 1 before any retry", reconciliations)
			}
		})
	}
}
