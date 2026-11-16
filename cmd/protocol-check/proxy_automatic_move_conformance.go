package main

import (
	"fmt"
	"sort"
	"time"
)

// evaluateProxyAutomaticMove is the reference evaluator for integrations
// section 3.3: which waiting players a proxy may move by itself this tick.
func evaluateProxyAutomaticMove(_ *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	policy := asObject(input["policy"])
	batch, err := integerValue(policy["batch_size"])
	if err != nil {
		return nil, fmt.Errorf("batch_size: %w", err)
	}
	retryAfter, err := integerValue(policy["retry_after_seconds"])
	if err != nil {
		return nil, fmt.Errorf("retry_after_seconds: %w", err)
	}
	evaluations := []any{}
	for _, raw := range arrayValue(input, "evaluations") {
		evaluation := asObject(raw)
		moves, err := automaticMovesForEvaluation(evaluation, batch, time.Duration(retryAfter)*time.Second)
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, map[string]any{"case_id": stringValue(evaluation, "case_id"), "moves": moves})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

type automaticMoveCandidate struct {
	id    string
	since time.Time
}

func automaticMovesForEvaluation(evaluation map[string]any, batch int64, retryAfter time.Duration) ([]any, error) {
	now, err := time.Parse(time.RFC3339, stringValue(evaluation, "now"))
	if err != nil {
		return nil, fmt.Errorf("now: %w", err)
	}
	moves := []any{}
	backend := asObject(evaluation["backend"])
	if stringValue(backend, "registration") != "registered" || !boolValue(backend, "routable") {
		return moves, nil
	}
	candidates, err := automaticMoveCandidates(evaluation, now, retryAfter)
	if err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].since.Equal(candidates[j].since) {
			return candidates[i].since.Before(candidates[j].since)
		}
		return candidates[i].id < candidates[j].id
	})
	for i := 0; i < len(candidates) && int64(i) < batch; i++ {
		moves = append(moves, candidates[i].id)
	}
	return moves, nil
}

func automaticMoveCandidates(evaluation map[string]any, now time.Time, retryAfter time.Duration) ([]automaticMoveCandidate, error) {
	waitingCohort := "waiting-" + stringValue(evaluation, "backend_id")
	fallback := stringValue(evaluation, "fallback_id")
	var candidates []automaticMoveCandidate
	for _, rawSession := range arrayValue(evaluation, "sessions") {
		session := asObject(rawSession)
		if !boolValue(session, "connected") || stringValue(session, "cohort_id") != waitingCohort ||
			stringValue(session, "current_backend_id") != fallback {
			continue
		}
		coolingDown, err := automaticMoveCoolingDown(session, now, retryAfter)
		if err != nil {
			return nil, err
		}
		if coolingDown {
			continue
		}
		since, err := time.Parse(time.RFC3339, stringValue(session, "waiting_since"))
		if err != nil {
			return nil, fmt.Errorf("waiting_since: %w", err)
		}
		candidates = append(candidates, automaticMoveCandidate{stringValue(session, "session_id"), since})
	}
	return candidates, nil
}

func automaticMoveCoolingDown(session map[string]any, now time.Time, retryAfter time.Duration) (bool, error) {
	failed := stringValue(session, "last_failed_at")
	if failed == "" {
		return false, nil
	}
	at, err := time.Parse(time.RFC3339, failed)
	if err != nil {
		return false, fmt.Errorf("last_failed_at: %w", err)
	}
	return now.Sub(at) < retryAfter, nil
}

func init() {
	registerEvaluator(evaluateProxyAutomaticMove, "integration.proxy.cohort.automatic-move")
}
