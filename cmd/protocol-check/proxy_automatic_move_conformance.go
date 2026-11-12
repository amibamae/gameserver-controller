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
		backendID, fallback := stringValue(evaluation, "backend_id"), stringValue(evaluation, "fallback_id")
		now, err := time.Parse(time.RFC3339, stringValue(evaluation, "now"))
		if err != nil {
			return nil, fmt.Errorf("now: %w", err)
		}
		backend := asObject(evaluation["backend"])
		type candidate struct {
			id    string
			since time.Time
		}
		var candidates []candidate
		if stringValue(backend, "registration") == "registered" && boolValue(backend, "routable") {
			for _, rawSession := range arrayValue(evaluation, "sessions") {
				session := asObject(rawSession)
				if !boolValue(session, "connected") || stringValue(session, "cohort_id") != "waiting-"+backendID ||
					stringValue(session, "current_backend_id") != fallback {
					continue
				}
				if failed := stringValue(session, "last_failed_at"); failed != "" {
					at, err := time.Parse(time.RFC3339, failed)
					if err != nil {
						return nil, fmt.Errorf("last_failed_at: %w", err)
					}
					if now.Sub(at) < time.Duration(retryAfter)*time.Second {
						continue
					}
				}
				since, err := time.Parse(time.RFC3339, stringValue(session, "waiting_since"))
				if err != nil {
					return nil, fmt.Errorf("waiting_since: %w", err)
				}
				candidates = append(candidates, candidate{stringValue(session, "session_id"), since})
			}
		}
		sort.Slice(candidates, func(i, j int) bool {
			if !candidates[i].since.Equal(candidates[j].since) {
				return candidates[i].since.Before(candidates[j].since)
			}
			return candidates[i].id < candidates[j].id
		})
		moves := []any{}
		for i := 0; i < len(candidates) && int64(i) < batch; i++ {
			moves = append(moves, candidates[i].id)
		}
		evaluations = append(evaluations, map[string]any{"case_id": stringValue(evaluation, "case_id"), "moves": moves})
	}
	return map[string]any{"evaluations": evaluations}, nil
}
