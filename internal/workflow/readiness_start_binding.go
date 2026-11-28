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

// readinessClockSkew is how far an adapter's clock may lag the controller's,
// the same allowance observationIsFreshForStep grants provider evidence.
const readinessClockSkew = 30 * time.Second

// startedRuntime is the start attempt a server.start readiness observation
// must prove: the earliest time its evaluation may claim, and the runtime
// incarnation the post-start unit inspection reported.
type startedRuntime struct {
	notBefore time.Time
	runtimeID string
}

// startReadinessAssertion builds the server.start readiness assertion and
// requires its evidence to prove this start attempt: it must be evaluated no
// earlier than the readiness step was dispatched, less the clock-skew
// allowance, and name the runtime the unit inspection after the start
// reported. The readiness step is first dispatched only after the final unit
// start attempt succeeded, and observed_at is evaluation time, so a game
// observation from an earlier start or another process cannot satisfy it.
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
	dispatchedAt := operationStep(operation, startReadinessStep).StartedAt
	if dispatchedAt == nil || dispatchedAt.IsZero() {
		return startedRuntime{}, errors.New("readiness step has no recorded dispatch time")
	}
	inspection, err := unitInspectStepOutput(operation, startVerifyRuntimeStep)
	if err != nil || inspection.RuntimeID == "" {
		return startedRuntime{}, errors.New("unit inspection after start did not report a runtime_id")
	}
	return startedRuntime{notBefore: dispatchedAt.Add(-readinessClockSkew), runtimeID: inspection.RuntimeID}, nil
}

// checkReadinessFollowsStart requires the readiness observation and every
// check result in it to be no earlier than start.notBefore, and the
// observation to name the started runtime.
func checkReadinessFollowsStart(output json.RawMessage, start startedRuntime) error {
	var observation startBoundReadiness
	if err := json.Unmarshal(output, &observation); err != nil {
		return errors.New("decode readiness observation")
	}
	if observedBefore(observation.ObservedAt, start.notBefore) {
		return errors.New("readiness observation predates its dispatch by more than the 30s clock-skew allowance")
	}
	for _, check := range observation.CheckResults {
		if observedBefore(check.ObservedAt, start.notBefore) {
			return fmt.Errorf("readiness check %q observation predates its dispatch by more than the 30s clock-skew allowance", check.CheckID)
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
