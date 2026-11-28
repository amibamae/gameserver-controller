package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// startBoundReadiness is the part of a readiness observation that must
// follow, and name, the unit start it is evidence for.
type startBoundReadiness struct {
	RuntimeID    string                 `json:"runtime_id"`
	ObservedAt   string                 `json:"observed_at"`
	CheckResults []readinessCheckResult `json:"check_results"`
}

// startedRuntime is the start attempt a server.start readiness observation
// must prove: when the store recorded the unit start step complete, and the
// runtime incarnation the post-start unit inspection reported.
type startedRuntime struct {
	completedAt time.Time
	runtimeID   string
}

// startReadinessAssertion builds the server.start readiness assertion and
// requires its evidence to prove this start attempt: it must be observed no
// earlier than the unit start completed, and name the runtime the unit
// inspection after that start reported. A game observation from an earlier
// start or another process cannot satisfy it.
func (w *ServerStartWorkflow) startReadinessAssertion(operation store.OperationRecord, server store.LogicalServer) (json.RawMessage, error) {
	assertion, err := BuildReadinessAssertion(w.schemas, operation, server, startReadinessStep, w.ttl)
	if err != nil {
		return nil, err
	}
	start, err := startedRuntimeOf(operation)
	if err != nil {
		return nil, err
	}
	if err := checkReadinessFollowsStart(operationStep(operation, startReadinessStep).Output, start); err != nil {
		return nil, err
	}
	return assertion, nil
}

// startedRuntimeOf returns the start attempt recorded in operation.
func startedRuntimeOf(operation store.OperationRecord) (startedRuntime, error) {
	completedAt := operationStep(operation, startRuntimeStep).CompletedAt
	if completedAt == nil || completedAt.IsZero() {
		return startedRuntime{}, errors.New("unit start step has no recorded completion time")
	}
	inspection, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	if err != nil || inspection.RuntimeID == "" {
		return startedRuntime{}, errors.New("unit inspection after start did not report a runtime_id")
	}
	return startedRuntime{completedAt: *completedAt, runtimeID: inspection.RuntimeID}, nil
}

// checkReadinessFollowsStart requires the readiness observation and every
// check result in it to be no earlier than the start completed, and the
// observation to name the started runtime.
func checkReadinessFollowsStart(output json.RawMessage, start startedRuntime) error {
	var observation startBoundReadiness
	if err := json.Unmarshal(output, &observation); err != nil {
		return errors.New("decode readiness observation")
	}
	if observedBefore(observation.ObservedAt, start.completedAt) {
		return errors.New("readiness observation predates this operation's unit start")
	}
	for _, check := range observation.CheckResults {
		if observedBefore(check.ObservedAt, start.completedAt) {
			return fmt.Errorf("readiness check %q observation predates this operation's unit start", check.CheckID)
		}
	}
	return checkReadinessRuntime(observation.RuntimeID, start.runtimeID)
}

// checkReadinessRuntime requires a readiness observation to report exactly
// the started runtime.
func checkReadinessRuntime(observed, started string) error {
	if observed == "" {
		return errors.New("readiness observation did not report a runtime_id")
	}
	if observed != started {
		return errors.New("readiness observation runtime_id does not match the started runtime")
	}
	return nil
}

// observedBefore reports whether the RFC 3339 time raw is earlier than
// bound. An unparseable time counts as earlier, so it fails closed.
func observedBefore(raw string, bound time.Time) bool {
	observedAt, err := time.Parse(time.RFC3339Nano, raw)
	return err != nil || observedAt.Before(bound)
}
