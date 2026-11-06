package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

func (pkg *protocolPackage) runConformance() error {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	var failures []string
	for _, path := range paths {
		fixture, err := parseJSONFile(path)
		if err == nil {
			var actual map[string]any
			actual, err = evaluate(pkg, asObject(fixture))
			if err == nil {
				var encoded []byte
				encoded, err = json.Marshal(actual)
				if err == nil {
					var normalized any
					normalized, err = parseJSON(encoded, filepath.Base(path)+" result")
					if err == nil {
						err = compareExpected(normalized, asObject(asObject(fixture)["expected"]), "$")
					}
				}
			}
		}
		if err != nil {
			failures = append(failures, filepath.Base(path)+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("conformance failed:\n - %s", strings.Join(failures, "\n - "))
	}
	return nil
}

func evaluate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	fixtureID := stringValue(fixture, "fixture_id")
	input := asObject(fixture["input"])
	switch fixtureID {
	case "lifecycle.reject.unlisted-transition":
		transition := asObject(fixture["requested_transition"])
		allowed := legalTransitions[[2]string{stringValue(fixture, "initial_state"), stringValue(transition, "to")}]
		result := map[string]any{"accepted": allowed, "side_effect_permitted": allowed}
		if !allowed {
			result["error_code"] = "core.lifecycle.invalid_transition"
		}
		return result, nil
	case "operations.reconcile.unknown-create-outcome":
		initial := asObject(fixture["initial"])
		unknown := boolValue(initial, "dispatch_recorded") && !boolValue(initial, "provider_response_received")
		result := map[string]any{
			"operation_status":         "reconciling",
			"step_status":              "outcome_unknown",
			"retry_create_immediately": false,
			"inspect_exact_target_or_search_by_idempotency_reference": unknown,
			"if_unresolvable": "needs_operator",
		}
		if !unknown {
			result["operation_status"] = initial["operation_status"]
			result["step_status"] = initial["step_status"]
			result["retry_create_immediately"] = true
			result["inspect_exact_target_or_search_by_idempotency_reference"] = false
			result["if_unresolvable"] = nil
		}
		return result, nil
	case "safety.destroy.blocked-stale-data-generation", "safety.destroy.blocked-unverified-backup":
		state := asObject(fixture["initial_state"])
		backup := asObject(state["backup"])
		code := any(nil)
		if !boolValue(backup, "verified") {
			code = "core.safety.backup_not_verified"
		} else if state["data_generation_status"] != "known" || backup["data_generation_id"] != state["data_generation_id"] {
			code = "core.safety.backup_generation_mismatch"
		}
		accepted := code == nil
		lifecycle := state["lifecycle_state"]
		if accepted {
			lifecycle = "destroying"
		}
		calls := 0
		if accepted {
			calls = 1
		}
		return map[string]any{
			"accepted": accepted, "error_code": code,
			"provider_destroy_calls": calls, "logical_server_state": lifecycle,
		}, nil
	case "security.staging.cannot-write-production-backup":
		actor := stringValue(fixture, "actor_environment")
		target := stringValue(fixture, "target_environment")
		requiredScope := "storage.backup.write:" + target
		allowed := actor == target || contains(arrayValue(fixture, "credential_scope"), requiredScope)
		category := any(nil)
		if !allowed {
			category = "authorization"
		}
		return map[string]any{"accepted": allowed, "side_effect_permitted": allowed, "error_category": category}, nil
	case "game.readiness.unknown-required-check-blocks-ready":
		if err := pkg.validateSchema("urn:gameserver-controller:0.4.0-draft:game:readiness-check-input", input); err != nil {
			return nil, fmt.Errorf("readiness input does not match schema: %w", err)
		}
		output := asObject(fixture["output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.4.0-draft:game:readiness-check-output", output); err != nil {
			return nil, fmt.Errorf("readiness output does not match schema: %w", err)
		}
		bound := input["server_id"] == output["server_id"] &&
			input["environment_id"] == output["environment_id"] &&
			input["game_release_id"] == output["game_release_id"] &&
			reflect.DeepEqual(input["instance_ref"], output["instance_ref"])
		if !bound {
			return nil, fmt.Errorf("readiness output must remain bound to the exact requested target and release")
		}
		requiredIDs := arrayValue(input, "required_check_ids")
		if !uniqueStrings(requiredIDs) {
			return nil, fmt.Errorf("required check IDs must be unique")
		}
		requested := make(map[string]bool, len(requiredIDs))
		for _, rawID := range requiredIDs {
			requested[rawID.(string)] = true
		}
		results := arrayValue(output, "check_results")
		if len(results) != len(requested) {
			return nil, fmt.Errorf("readiness output must contain exactly one result for every required check")
		}
		observed := make(map[string]string, len(results))
		for _, rawResult := range results {
			item := asObject(rawResult)
			checkID := stringValue(item, "check_id")
			if !requested[checkID] {
				return nil, fmt.Errorf("readiness output contains an unrequested check %q", checkID)
			}
			if _, exists := observed[checkID]; exists {
				return nil, fmt.Errorf("readiness output contains duplicate check %q", checkID)
			}
			observed[checkID] = stringValue(item, "result")
		}
		allPassed, hasFailure, hasUnknown := true, false, false
		for checkID := range requested {
			result, exists := observed[checkID]
			if !exists {
				return nil, fmt.Errorf("readiness output is missing required check %q", checkID)
			}
			switch result {
			case "pass":
			case "fail":
				allPassed, hasFailure = false, true
			case "unknown":
				allPassed, hasUnknown = false, true
			default:
				return nil, fmt.Errorf("unknown readiness result %q for check %q", result, checkID)
			}
		}
		readiness := "ready"
		if hasFailure {
			readiness = "not_ready"
		} else if hasUnknown {
			readiness = "unknown"
		}
		return map[string]any{
			"schema_valid": true, "target_bound": bound,
			"required_checks_complete": true, "all_required_checks_passed": allPassed,
			"readiness": readiness, "ready_transition_permitted": allPassed,
		}, nil
	case "readiness-required-before-transfer":
		assertion := asObject(input["readiness_assertion"])
		current, err := parseTime(stringValue(input, "validation_time"))
		if err != nil {
			return nil, err
		}
		expires, err := parseTime(stringValue(assertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		expired := !current.Before(expires)
		outcome, code := "accepted", any(nil)
		if expired {
			outcome, code = "rejected", "readiness_assertion_expired"
		}
		calls := len(arrayValue(input, "player_session_ids"))
		if expired {
			calls = 0
		}
		return map[string]any{"outcome": outcome, "error_code": code, "proxy_transfer_calls": calls}, nil
	case "velocity-transfer-partial-results":
		sessions := arrayValue(input, "player_session_ids")
		results := arrayValue(input, "session_outcomes")
		if !uniqueStrings(sessions) {
			return nil, fmt.Errorf("requested session IDs must be unique")
		}
		if len(results) != len(sessions) {
			return nil, fmt.Errorf("one session outcome is required for every requested session")
		}
		requested := make(map[string]bool)
		for _, id := range sessions {
			requested[id.(string)] = true
		}
		resultIDs := make([]any, 0, len(results))
		counts := map[string]int{"transferred": 0, "already_at_destination": 0, "disconnected": 0, "rejected": 0, "failed": 0, "outcome_unknown": 0}
		for _, result := range results {
			item := asObject(result)
			id := item["proxy_session_id"]
			outcome := stringValue(item, "outcome")
			resultIDs = append(resultIDs, id)
			if _, ok := counts[outcome]; !ok {
				return nil, fmt.Errorf("unknown transfer outcome: %q", outcome)
			}
			counts[outcome]++
		}
		if !uniqueStrings(resultIDs) {
			return nil, fmt.Errorf("session outcomes must not contain duplicate IDs")
		}
		for _, id := range resultIDs {
			if str, ok := id.(string); !ok || !requested[str] {
				return nil, fmt.Errorf("session outcomes must contain exactly the requested sessions")
			}
		}
		if len(requested) != len(resultIDs) {
			return nil, fmt.Errorf("session outcomes must contain exactly the requested sessions")
		}
		normalizedCounts := make(map[string]any, len(counts))
		hasUnknown := counts["outcome_unknown"] > 0
		for outcome, count := range counts {
			normalizedCounts[outcome] = count
		}
		return map[string]any{
			"requested_count":                      len(sessions),
			"outcome_counts":                       normalizedCounts,
			"retry_unknown_without_reconciliation": !hasUnknown,
		}, nil
	case "storage.backup.partial-upload-not-verified":
		required := arrayValue(input, "required_object_ids")
		uploaded := arrayValue(input, "uploaded_object_ids")
		allUploaded := uniqueStrings(required) && uniqueStrings(uploaded) && sameStringSet(required, uploaded)
		verified := allUploaded && boolValue(input, "manifest_verified")
		return map[string]any{
			"all_required_objects_uploaded": allUploaded,
			"backup_verified":               verified,
			"destruction_allowed":           verified,
			"partial":                       !allUploaded || !boolValue(input, "manifest_verified"),
		}, nil
	case "storage.object.checksum-mismatch-blocks-restore":
		sizeMatches := input["expected_size_bytes"] == input["actual_size_bytes"]
		expected := strings.ToLower(stringValue(input, "expected_checksum"))
		actual := strings.ToLower(stringValue(input, "actual_checksum"))
		checksumMatches := expected == actual
		verified, reason := sizeMatches && checksumMatches, any(nil)
		if !verified {
			reason = "checksum_mismatch"
			if !sizeMatches {
				reason = "size_mismatch"
			}
		}
		return map[string]any{"verified": verified, "restore_allowed": verified, "verification_reason": reason}, nil
	case "storage.object.etag-not-checksum":
		expected := strings.ToLower(stringValue(input, "expected_checksum"))
		verified := false
		for _, key := range []string{"trusted_provider_checksum", "content_digest"} {
			if value, ok := input[key].(string); ok && strings.ToLower(value) == expected {
				verified = true
			}
		}
		source := "insufficient_evidence"
		if verified {
			source = "trusted_checksum"
		}
		return map[string]any{"verified": verified, "verification_source": source}, nil
	case "storage.staging.cannot-delete-production-object":
		target := stringValue(fixture, "target_environment")
		requiredScope := "storage.backup.delete:" + target
		allowed := contains(arrayValue(fixture, "credential_scope"), requiredScope)
		category := any(nil)
		calls := 0
		if allowed {
			calls = 1
		} else {
			category = "authorization"
		}
		return map[string]any{
			"accepted": allowed, "error_category": category,
			"side_effect_permitted": allowed, "adapter_delete_calls": calls,
		}, nil
	case "operations.idempotency.same-key-different-request":
		first, second := asObject(fixture["first_request"]), asObject(fixture["second_request"])
		sameBinding := first["actor_id"] == second["actor_id"] && first["idempotency_key"] == second["idempotency_key"]
		conflict := sameBinding && !reflect.DeepEqual(first, second)
		code := any(nil)
		count := 2
		if conflict {
			code, count = "core.operation.idempotency_conflict", 1
		}
		return map[string]any{"second_request_accepted": !conflict, "error_code": code, "operation_count": count}, nil
	case "operations.events.monotonic-sequence":
		events := arrayValue(fixture, "events")
		if len(events) == 0 {
			return nil, fmt.Errorf("event sequence must not be empty")
		}
		var operationID string
		oneOperation := true
		sequences := make([]int64, 0, len(events))
		for index, rawEvent := range events {
			event := asObject(rawEvent)
			if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
				return nil, fmt.Errorf("event %d does not match schema: %w", index, err)
			}
			id := stringValue(event, "operation_id")
			if index == 0 {
				operationID = id
			} else if id != operationID {
				oneOperation = false
			}
			sequence, err := integerValue(event["sequence"])
			if err != nil {
				return nil, err
			}
			sequences = append(sequences, sequence)
		}
		increasing := oneOperation
		for i := 1; i < len(sequences); i++ {
			if sequences[i] <= sequences[i-1] {
				increasing = false
			}
		}
		return map[string]any{"schema_valid": true, "sequence_strictly_increasing": increasing}, nil
	case "hosting.provision.valid.standard-os":
		if err := pkg.validateSchema(provisionSchemaID, input); err != nil {
			return nil, err
		}
		return map[string]any{"schema_valid": true, "side_effect_permitted": false}, nil
	default:
		return nil, fmt.Errorf("no evaluator registered for fixture %q", fixtureID)
	}
}

func sameStringSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]bool, len(left))
	for _, value := range left {
		str, ok := value.(string)
		if !ok {
			return false
		}
		set[str] = true
	}
	for _, value := range right {
		str, ok := value.(string)
		if !ok || !set[str] {
			return false
		}
	}
	return true
}

func integerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Int64()
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func compareExpected(actual, expected any, path string) error {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualMap := asObject(actual)
		if actualMap == nil {
			return fmt.Errorf("%s: expected an object", path)
		}
		keys := make([]string, 0, len(expectedValue))
		for key := range expectedValue {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, ok := actualMap[key]
			if !ok {
				return fmt.Errorf("%s.%s: missing expected value", path, key)
			}
			if err := compareExpected(value, expectedValue[key], path+"."+key); err != nil {
				return err
			}
		}
	default:
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("%s: expected %v, got %v", path, expected, actual)
		}
	}
	return nil
}

var legalTransitions = map[[2]string]bool{
	{"unprovisioned", "provisioning"}: true,
	{"provisioning", "preparing"}:     true,
	{"provisioning", "failed"}:        true,
	{"preparing", "restoring"}:        true,
	{"preparing", "starting"}:         true,
	{"preparing", "failed"}:           true,
	{"restoring", "starting"}:         true,
	{"restoring", "stopped"}:          true,
	{"restoring", "failed"}:           true,
	{"starting", "validating"}:        true,
	{"starting", "failed"}:            true,
	{"validating", "ready"}:           true,
	{"validating", "degraded"}:        true,
	{"validating", "failed"}:          true,
	{"ready", "degraded"}:             true,
	{"ready", "draining"}:             true,
	{"ready", "stopping"}:             true,
	{"degraded", "validating"}:        true,
	{"degraded", "draining"}:          true,
	{"degraded", "stopping"}:          true,
	{"degraded", "failed"}:            true,
	{"draining", "ready"}:             true,
	{"draining", "stopping"}:          true,
	{"draining", "failed"}:            true,
	{"stopping", "backing_up"}:        true,
	{"stopping", "stopped"}:           true,
	{"stopping", "failed"}:            true,
	{"backing_up", "starting"}:        true,
	{"backing_up", "stopped"}:         true,
	{"backing_up", "destroying"}:      true,
	{"backing_up", "failed"}:          true,
	{"stopped", "starting"}:           true,
	{"stopped", "restoring"}:          true,
	{"stopped", "backing_up"}:         true,
	{"stopped", "destroying"}:         true,
	{"destroying", "destroyed"}:       true,
	{"destroying", "failed"}:          true,
	{"destroyed", "provisioning"}:     true,
	{"failed", "provisioning"}:        true,
	{"failed", "preparing"}:           true,
	{"failed", "restoring"}:           true,
	{"failed", "stopping"}:            true,
	{"failed", "destroying"}:          true,
}
