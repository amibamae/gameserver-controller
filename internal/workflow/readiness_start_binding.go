package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

// startBoundReadiness is the part of a readiness observation that must
// follow the unit start it is evidence for.
type startBoundReadiness struct {
	ObservedAt   string                 `json:"observed_at"`
	CheckResults []readinessCheckResult `json:"check_results"`
}

// startReadinessAssertion builds the server.start readiness assertion and
// requires its evidence to have been observed no earlier than this
// operation's unit start completed, so a game observation from an earlier
// start attempt cannot satisfy it.
func (w *ServerStartWorkflow) startReadinessAssertion(operation store.OperationRecord, server store.LogicalServer) (json.RawMessage, error) {
	assertion, err := BuildReadinessAssertion(w.schemas, operation, server, startReadinessStep, w.ttl)
	if err != nil {
		return nil, err
	}
	completedAt, err := unitStartCompletedAt(operation)
	if err != nil {
		return nil, err
	}
	if err := checkReadinessFollowsStart(operationStep(operation, startReadinessStep).Output, completedAt); err != nil {
		return nil, err
	}
	return assertion, nil
}

// unitStartCompletedAt returns when the store recorded this operation's unit
// start step complete.
func unitStartCompletedAt(operation store.OperationRecord) (time.Time, error) {
	completedAt := operationStep(operation, startRuntimeStep).CompletedAt
	if completedAt == nil || completedAt.IsZero() {
		return time.Time{}, errors.New("unit start step has no recorded completion time")
	}
	return *completedAt, nil
}

// checkReadinessFollowsStart requires the readiness observation and every
// check result in it to be no earlier than startCompletedAt.
func checkReadinessFollowsStart(output json.RawMessage, startCompletedAt time.Time) error {
	var observation startBoundReadiness
	if err := json.Unmarshal(output, &observation); err != nil {
		return errors.New("decode readiness observation")
	}
	if observedBefore(observation.ObservedAt, startCompletedAt) {
		return errors.New("readiness observation predates this operation's unit start")
	}
	for _, check := range observation.CheckResults {
		if observedBefore(check.ObservedAt, startCompletedAt) {
			return fmt.Errorf("readiness check %q observation predates this operation's unit start", check.CheckID)
		}
	}
	return nil
}

// observedBefore reports whether the RFC 3339 time raw is earlier than
// bound. An unparseable time counts as earlier, so it fails closed.
func observedBefore(raw string, bound time.Time) bool {
	observedAt, err := time.Parse(time.RFC3339Nano, raw)
	return err != nil || observedAt.Before(bound)
}
