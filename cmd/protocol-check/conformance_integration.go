package main

import (
	"fmt"
	"reflect"
	"time"
)

// Register the proxy and Velocity integration evaluators defined in this file.
func init() {
	registerEvaluator(evaluateIntegrationProxyRegisterExpiredReadinessRejected, "integration.proxy.register.expired-readiness-rejected")
	registerEvaluator(evaluateIntegrationProxyRegisterAcceptedIsNotRoutable, "integration.proxy.register.accepted-is-not-routable")
	registerEvaluator(evaluateIntegrationProxyUnknownDrainAndUnregisterRequireInspection, "integration.proxy.unknown-drain-and-unregister-require-inspection")
	registerEvaluator(evaluateReadinessBeforeTransfer,
		"readiness-required-before-transfer",
		"readiness-target-mismatch-before-transfer",
		"readiness-server-mismatch-before-transfer")
	registerEvaluator(evaluateVelocityTransferUnknownReconciliation, "velocity-transfer-outcome-unknown-reconciliation")
	registerEvaluator(evaluateVelocityTransferResults,
		"velocity-transfer-partial-results",
		"velocity-transfer-count-mismatch",
		"velocity-transfer-context-mismatch",
		"velocity-transfer-duplicate-session-result",
		"velocity-transfer-target-mismatch")
}

// evaluateIntegrationProxyRegisterExpiredReadinessRejected evaluates the integration.proxy.register.expired-readiness-rejected conformance fixture.
func evaluateIntegrationProxyRegisterExpiredReadinessRejected(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	registerInput, registerOutput := asObject(input["register_input"]), asObject(input["register_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-input", registerInput); err != nil {
		return nil, fmt.Errorf("register input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-output", registerOutput); err != nil {
		return nil, fmt.Errorf("register output does not match schema: %w", err)
	}
	requestTarget, assertion := asObject(registerInput["target"]), asObject(registerInput["readiness_assertion"])
	targetInstance := asObject(requestTarget["instance_ref"])
	assertedInstance := asObject(assertion["instance_ref"])
	bound := reflect.DeepEqual(requestTarget, registerOutput["target"]) &&
		stringValue(requestTarget, "server_id") == stringValue(assertion, "server_id") &&
		stringValue(requestTarget, "environment_id") == stringValue(assertion, "environment_id") &&
		stringValue(requestTarget, "environment_id") == stringValue(targetInstance, "environment_id") &&
		stringValue(assertion, "environment_id") == stringValue(assertedInstance, "environment_id") &&
		reflect.DeepEqual(requestTarget["instance_ref"], assertion["instance_ref"])
	current, err := parseTime(stringValue(input, "validation_time"))
	if err != nil {
		return nil, err
	}
	expires, err := parseTime(stringValue(assertion, "expires_at"))
	if err != nil {
		return nil, err
	}
	readinessCurrent := bound && current.Before(expires)
	rejected := stringValue(registerOutput, "request_outcome") == "rejected" &&
		stringValue(registerOutput, "registration_state") == "unregistered" &&
		stringValue(registerOutput, "routability") == "not_routable"
	return map[string]any{
		"schemas_valid": true, "target_bound": bound, "readiness_current": readinessCurrent,
		"registration_rejected": rejected, "routing_permitted": readinessCurrent && stringValue(registerOutput, "routability") == "routable",
	}, nil
}

// evaluateIntegrationProxyRegisterAcceptedIsNotRoutable evaluates the integration.proxy.register.accepted-is-not-routable conformance fixture.
func evaluateIntegrationProxyRegisterAcceptedIsNotRoutable(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	registerInput, registerOutput := asObject(input["register_input"]), asObject(input["register_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-input", registerInput); err != nil {
		return nil, fmt.Errorf("register input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-output", registerOutput); err != nil {
		return nil, fmt.Errorf("register output does not match schema: %w", err)
	}
	requestTarget, assertion := asObject(registerInput["target"]), asObject(registerInput["readiness_assertion"])
	targetInstance := asObject(requestTarget["instance_ref"])
	assertedInstance := asObject(assertion["instance_ref"])
	bound := reflect.DeepEqual(requestTarget, registerOutput["target"]) &&
		stringValue(requestTarget, "server_id") == stringValue(assertion, "server_id") &&
		stringValue(requestTarget, "environment_id") == stringValue(assertion, "environment_id") &&
		stringValue(requestTarget, "environment_id") == stringValue(targetInstance, "environment_id") &&
		stringValue(assertion, "environment_id") == stringValue(assertedInstance, "environment_id") &&
		reflect.DeepEqual(requestTarget["instance_ref"], assertion["instance_ref"])
	current, err := parseTime(stringValue(input, "validation_time"))
	if err != nil {
		return nil, err
	}
	expires, err := parseTime(stringValue(assertion, "expires_at"))
	if err != nil {
		return nil, err
	}
	readinessCurrent := bound && current.Before(expires)
	confirmed := stringValue(registerOutput, "request_outcome") == "confirmed"
	routingPermitted := readinessCurrent && confirmed &&
		stringValue(registerOutput, "registration_state") == "registered" &&
		stringValue(registerOutput, "routability") == "routable"
	return map[string]any{
		"schemas_valid": true, "target_bound": bound, "readiness_current": readinessCurrent,
		"registration_confirmed": confirmed, "accepted_not_routable": stringValue(registerOutput, "request_outcome") != "accepted" || stringValue(registerOutput, "routability") != "routable", "routing_permitted": routingPermitted,
	}, nil
}

// evaluateIntegrationProxyUnknownDrainAndUnregisterRequireInspection evaluates the integration.proxy.unknown-drain-and-unregister-require-inspection conformance fixture.
func evaluateIntegrationProxyUnknownDrainAndUnregisterRequireInspection(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	drainInput, drainOutput := asObject(input["drain_input"]), asObject(input["drain_output"])
	unregisterInput, unregisterOutput := asObject(input["unregister_input"]), asObject(input["unregister_output"])
	for schemaID, message := range map[string]struct {
		value map[string]any
		name  string
	}{
		"urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-drain-input":       {drainInput, "drain input"},
		"urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-drain-output":      {drainOutput, "drain output"},
		"urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-unregister-input":  {unregisterInput, "unregister input"},
		"urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-unregister-output": {unregisterOutput, "unregister output"},
	} {
		if err := pkg.validateSchema(schemaID, message.value); err != nil {
			return nil, fmt.Errorf("%s does not match schema: %w", message.name, err)
		}
	}
	drainUnknown := stringValue(drainOutput, "request_outcome") == "outcome_unknown" &&
		stringValue(drainOutput, "registration_state") == "unknown" && stringValue(drainOutput, "routability") == "unknown"
	unregisterUnknown := stringValue(unregisterOutput, "request_outcome") == "outcome_unknown" &&
		stringValue(unregisterOutput, "registration_state") == "unknown" && stringValue(unregisterOutput, "routability") == "unknown"
	bound := reflect.DeepEqual(drainInput["target"], drainOutput["target"]) &&
		reflect.DeepEqual(unregisterInput["target"], unregisterOutput["target"])
	return map[string]any{
		"schemas_valid": true, "drain_requires_inspection": drainUnknown,
		"unregister_requires_inspection": unregisterUnknown, "retry_without_inspection": !drainUnknown || !unregisterUnknown || !bound,
	}, nil
}

// evaluateReadinessBeforeTransfer evaluates the conformance fixtures
// readiness-required-before-transfer, readiness-target-mismatch-before-transfer,
// readiness-server-mismatch-before-transfer.
func evaluateReadinessBeforeTransfer(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	request := asObject(input["transfer_request"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:velocity-player-transfer-request", request); err != nil {
		return nil, fmt.Errorf("transfer request does not match schema: %w", err)
	}
	assertion := asObject(request["readiness_assertion"])
	requestTarget := asObject(request["expected_instance_ref"])
	assertedTarget := asObject(assertion["instance_ref"])
	bound := stringValue(request, "server_id") == stringValue(assertion, "server_id") &&
		stringValue(request, "environment_id") == stringValue(assertion, "environment_id") &&
		stringValue(request, "environment_id") == stringValue(requestTarget, "environment_id") &&
		stringValue(assertion, "environment_id") == stringValue(assertedTarget, "environment_id") &&
		reflect.DeepEqual(requestTarget, assertedTarget)
	current, err := parseTime(stringValue(input, "validation_time"))
	if err != nil {
		return nil, err
	}
	observedAt, err := parseTime(stringValue(assertion, "observed_at"))
	if err != nil {
		return nil, err
	}
	expiresAt, err := parseTime(stringValue(assertion, "expires_at"))
	if err != nil {
		return nil, err
	}
	readinessCurrent := bound && !current.Before(observedAt) && current.Before(expiresAt)
	outcome, code := "accepted", any(nil)
	if !bound {
		outcome, code = "rejected", "readiness_assertion_target_mismatch"
	} else if current.Before(observedAt) {
		outcome, code = "rejected", "readiness_assertion_not_current"
	} else if !current.Before(expiresAt) {
		outcome, code = "rejected", "readiness_assertion_expired"
	}
	calls := len(arrayValue(request, "player_session_ids"))
	if !readinessCurrent {
		calls = 0
	}
	return map[string]any{
		"request_schema_valid": true,
		"target_bound":         bound,
		"readiness_current":    readinessCurrent,
		"outcome":              outcome,
		"error_code":           code,
		"proxy_transfer_calls": calls,
	}, nil
}

// evaluateVelocityTransferResults evaluates the conformance fixtures
// velocity-transfer-partial-results, velocity-transfer-count-mismatch,
// velocity-transfer-context-mismatch, velocity-transfer-duplicate-session-result,
// velocity-transfer-target-mismatch.
func evaluateVelocityTransferResults(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	request := asObject(input["transfer_request"])
	result := asObject(input["transfer_result"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:velocity-player-transfer-request", request); err != nil {
		return nil, fmt.Errorf("transfer request does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:velocity-player-transfer-result", result); err != nil {
		return nil, fmt.Errorf("transfer result does not match schema: %w", err)
	}
	sessions := arrayValue(request, "player_session_ids")
	results := arrayValue(result, "results")
	requestTarget := asObject(request["expected_instance_ref"])
	assertion := asObject(request["readiness_assertion"])
	assertedTarget := asObject(assertion["instance_ref"])
	readinessBound := stringValue(request, "server_id") == stringValue(assertion, "server_id") &&
		stringValue(request, "environment_id") == stringValue(assertion, "environment_id") &&
		stringValue(request, "environment_id") == stringValue(requestTarget, "environment_id") &&
		stringValue(assertion, "environment_id") == stringValue(assertedTarget, "environment_id") &&
		reflect.DeepEqual(requestTarget, assertedTarget)
	targetBound := reflect.DeepEqual(requestTarget, asObject(result["target_instance_ref"]))
	requestResultBound := targetBound &&
		stringValue(request, "waiting_cohort_id") == stringValue(result, "waiting_cohort_id") &&
		stringValue(request, "source_backend_id") == stringValue(result, "source_backend_id") &&
		stringValue(request, "destination_backend_id") == stringValue(result, "destination_backend_id")
	current, err := parseTime(stringValue(input, "validation_time"))
	if err != nil {
		return nil, err
	}
	observedAt, err := parseTime(stringValue(assertion, "observed_at"))
	if err != nil {
		return nil, err
	}
	expiresAt, err := parseTime(stringValue(assertion, "expires_at"))
	if err != nil {
		return nil, err
	}
	readinessCurrent := readinessBound && !current.Before(observedAt) && current.Before(expiresAt)
	resultIDs := make([]any, 0, len(results))
	counts := map[string]int{
		"transferred":            0,
		"already_at_destination": 0,
		"disconnected":           0,
		"rejected":               0,
		"failed":                 0,
		"outcome_unknown":        0,
	}
	for _, raw := range results {
		item := asObject(raw)
		id, outcome := item["proxy_session_id"], stringValue(item, "outcome")
		if _, ok := counts[outcome]; !ok {
			return nil, fmt.Errorf("unknown transfer outcome: %q", outcome)
		}
		resultIDs = append(resultIDs, id)
		counts[outcome]++
	}
	sessionSetComplete := uniqueStrings(sessions) && uniqueStrings(resultIDs) &&
		len(results) == len(sessions) && sameStringSet(sessions, resultIDs)
	countsMatch := len(results) == len(sessions)
	requestedCount, err := integerValue(result["requested_count"])
	if err != nil {
		return nil, err
	}
	countsMatch = countsMatch && requestedCount == int64(len(sessions))
	reportedCounts := asObject(result["outcome_counts"])
	for outcome, count := range counts {
		reported, err := integerValue(reportedCounts[outcome])
		if err != nil || reported != int64(count) {
			countsMatch = false
		}
	}
	normalizedCounts := make(map[string]any, len(counts))
	hasUnknown := counts["outcome_unknown"] > 0
	for outcome, count := range counts {
		normalizedCounts[outcome] = count
	}
	accepted := readinessCurrent && requestResultBound && sessionSetComplete && countsMatch
	return map[string]any{
		"request_schema_valid":                 true,
		"result_schema_valid":                  true,
		"requested_count":                      len(sessions),
		"outcome_counts":                       normalizedCounts,
		"target_bound":                         targetBound,
		"request_result_bound":                 requestResultBound,
		"readiness_current":                    readinessCurrent,
		"session_set_complete":                 sessionSetComplete,
		"aggregate_counts_match":               countsMatch,
		"result_valid_and_bound":               accepted,
		"retry_unknown_without_reconciliation": !hasUnknown,
	}, nil
}

func evaluateVelocityTransferUnknownReconciliation(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	request := asObject(input["transfer_request"])
	result := asObject(input["transfer_result"])
	transferState, err := evaluate(pkg, map[string]any{
		"fixture_id": "velocity-transfer-partial-results",
		"input": map[string]any{
			"transfer_request": request,
			"transfer_result":  result,
			"validation_time":  input["transfer_validation_time"],
		},
	})
	if err != nil {
		return nil, err
	}
	transferValid := boolValue(transferState, "result_valid_and_bound")
	unknownSessions := make(map[string]bool)
	for _, raw := range arrayValue(result, "results") {
		item := asObject(raw)
		if stringValue(item, "outcome") == "outcome_unknown" {
			unknownSessions[stringValue(item, "proxy_session_id")] = true
		}
	}

	caseResults := make([]any, 0, len(arrayValue(input, "reconciliation_cases")))
	for _, rawCase := range arrayValue(input, "reconciliation_cases") {
		reconciliation := asObject(rawCase)
		inspectInput := asObject(reconciliation["inspection_request"])
		inspectOutput := asObject(reconciliation["inspection_result"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-session-inspect-input", inspectInput); err != nil {
			return nil, fmt.Errorf("session inspect request does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-session-inspect-output", inspectOutput); err != nil {
			return nil, fmt.Errorf("session inspect result does not match schema: %w", err)
		}
		decisionTimeText := stringValue(reconciliation, "decision_time")
		decisionTime, err := parseTime(decisionTimeText)
		if err != nil {
			return nil, err
		}
		observedAt, err := parseTime(stringValue(inspectOutput, "observed_at"))
		if err != nil {
			return nil, err
		}
		maximumAge, err := integerValue(inspectInput["maximum_observation_age_seconds"])
		if err != nil {
			return nil, err
		}
		age := decisionTime.Sub(observedAt)
		fresh := age >= 0 && age <= time.Duration(maximumAge)*time.Second
		proxyRef := asObject(inspectInput["proxy_instance_ref"])
		sessionID := stringValue(inspectInput, "proxy_session_id")
		bound := reflect.DeepEqual(inspectInput["proxy_instance_ref"], inspectOutput["proxy_instance_ref"]) &&
			sessionID == stringValue(inspectOutput, "proxy_session_id") &&
			unknownSessions[sessionID] &&
			stringValue(proxyRef, "adapter_id") == "integration.velocity" &&
			stringValue(proxyRef, "environment_id") == stringValue(request, "environment_id") &&
			stringValue(proxyRef, "resource_kind") == "proxy-instance"

		retryAssertion := asObject(reconciliation["retry_readiness_assertion"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:lifecycle:readiness-assertion", retryAssertion); err != nil {
			return nil, fmt.Errorf("retry readiness assertion does not match schema: %w", err)
		}
		retryReadinessCurrent, err := readinessAssertionBindsAtCompletion(
			retryAssertion,
			decisionTimeText,
			stringValue(request, "server_id"),
			stringValue(request, "environment_id"),
			request["expected_instance_ref"],
		)
		if err != nil {
			return nil, err
		}

		state := "unresolved"
		if transferValid && bound && fresh {
			switch stringValue(inspectOutput, "connection_state") {
			case "connected":
				switch stringValue(inspectOutput, "current_backend_id") {
				case stringValue(request, "source_backend_id"):
					state = "still_on_source"
				case stringValue(request, "destination_backend_id"):
					state = "already_at_destination"
				default:
					state = "other_backend"
				}
			case "disconnected":
				state = "disconnected"
			case "unknown":
				state = "unknown"
			}
		}
		retryPermitted := state == "still_on_source" && retryReadinessCurrent
		caseResults = append(caseResults, map[string]any{
			"case_id":                         stringValue(reconciliation, "case_id"),
			"session_inspection_schema_valid": true,
			"session_inspection_bound":        bound,
			"session_inspection_fresh":        fresh,
			"retry_readiness_current":         retryReadinessCurrent,
			"reconciliation_state":            state,
			"retry_permitted":                 retryPermitted,
		})
	}
	return map[string]any{
		"transfer_result_valid_and_bound": transferValid,
		"reconciliation_cases":            caseResults,
	}, nil
}
