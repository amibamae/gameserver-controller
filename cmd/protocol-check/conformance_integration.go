package main

import (
	"fmt"
	"reflect"
)

// evaluateIntegrationProxyRegisterExpiredReadinessRejected evaluates integration.proxy.register.expired-readiness-rejected.
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

// evaluateIntegrationProxyRegisterAcceptedIsNotRoutable evaluates integration.proxy.register.accepted-is-not-routable.
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

// evaluateIntegrationProxyUnknownDrainAndUnregisterRequireInspection evaluates integration.proxy.unknown-drain-and-unregister-require-inspection.
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

// evaluateReadinessRequiredBeforeTransfer evaluates readiness-required-before-transfer, readiness-target-mismatch-before-transfer and readiness-server-mismatch-before-transfer.
func evaluateReadinessRequiredBeforeTransfer(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateVelocityTransferPartialResults evaluates 5 fixtures: velocity-transfer-partial-results, velocity-transfer-count-mismatch, velocity-transfer-context-mismatch, velocity-transfer-duplicate-session-result, velocity-transfer-target-mismatch.
func evaluateVelocityTransferPartialResults(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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
