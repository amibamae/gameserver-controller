package main

import (
	"fmt"
)

// Register the protocol compatibility evaluators defined in this file.
func init() {
	registerEvaluator(withoutPackage(evaluateCompatibilityProtocolVersionNegotiation), "compatibility.protocol-version-negotiation")
	registerEvaluator(evaluateCompatibilityResponseEvolution, "compatibility.response-evolution")
	registerEvaluator(evaluateCompatibilityOperationUpgradeRecovery, "compatibility.operation-upgrade-recovery")
}

// evaluateCompatibilityProtocolVersionNegotiation evaluates the compatibility.protocol-version-negotiation conformance fixture.
func evaluateCompatibilityProtocolVersionNegotiation(fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "cases")))
	for _, rawCase := range arrayValue(fixture, "cases") {
		item := asObject(rawCase)
		local, remote := arrayValue(item, "local_protocol_versions"), arrayValue(item, "peer_protocol_versions")
		selected := stringValue(item, "selected_protocol_version")
		mutual := versionOffered(local, selected) && versionOffered(remote, selected)
		priorFailureVersion := stringValue(item, "version_that_failed_validation_or_authorization")
		downgradeAfterError := priorFailureVersion != "" && selected != priorFailureVersion
		accepted := mutual && !downgradeAfterError
		var errorCode any
		if !accepted {
			errorCode = "core.compatibility.unsupported_version"
		}
		results = append(results, map[string]any{
			"case_id": stringValue(item, "case_id"), "accepted": accepted,
			"error_code": errorCode, "session_created": accepted, "adapter_calls": 0,
		})
	}
	return map[string]any{"cases": results}, nil
}

// evaluateCompatibilityResponseEvolution evaluates the compatibility.response-evolution conformance fixture.
func evaluateCompatibilityResponseEvolution(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	server := asObject(input["server"])
	operation := asObject(input["operation"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:lifecycle:server", server); err != nil {
		return nil, fmt.Errorf("extensible server resource does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
		return nil, fmt.Errorf("extensible operation resource does not match schema: %w", err)
	}
	request := asObject(input["request"])
	requestWithUnknown := asObject(input["request_with_unknown_field"])
	requestValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation-request", request) == nil
	unknownRequestRejected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation-request", requestWithUnknown) != nil
	nestedServer := asObject(input["server_with_unknown_nested_observation_field"])
	nestedServerRejected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:lifecycle:server", nestedServer) != nil
	unknownLifecycle := asObject(input["server_with_unknown_lifecycle_state"])
	unknownLifecycleRejected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:lifecycle:server", unknownLifecycle) != nil
	unknownOperationStatus := asObject(input["operation_with_unknown_status"])
	unknownOperationStatusRejected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", unknownOperationStatus) != nil
	return map[string]any{
		"server_resource_valid": true, "operation_resource_valid": true,
		"unknown_optional_top_level_fields_ignored": true, "valid_request_accepted": requestValid,
		"unknown_request_field_rejected":         unknownRequestRejected,
		"unknown_nested_response_field_rejected": nestedServerRejected,
		"unknown_lifecycle_state_rejected":       unknownLifecycleRejected,
		"unknown_operation_status_rejected":      unknownOperationStatusRejected,
		"unknown_state_error_code":               "core.compatibility.unknown_state", "adapter_calls": 0,
	}, nil
}

// evaluateCompatibilityOperationUpgradeRecovery evaluates the compatibility.operation-upgrade-recovery conformance fixture.
func evaluateCompatibilityOperationUpgradeRecovery(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	operation := asObject(input["operation"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
		return nil, fmt.Errorf("in-flight operation does not match schema: %w", err)
	}
	results := make([]any, 0, len(arrayValue(input, "recovery_cases")))
	for _, rawCase := range arrayValue(input, "recovery_cases") {
		item := asObject(rawCase)
		available := recordedCapabilityAvailable(operation, arrayValue(item, "available_capabilities"))
		completedSteps, unknownSteps := countStepStatuses(operation)
		// Recovery reconciles when the exact recorded capability is still
		// available, with or without a tested migration; otherwise an
		// operator decides.
		recoveryStatus := "needs_operator"
		if available {
			recoveryStatus = "reconciling"
		}
		results = append(results, map[string]any{
			"case_id": stringValue(item, "case_id"), "recorded_protocol_version_preserved": true,
			"recorded_capability_exactly_supported": available, "recovery_status": recoveryStatus,
			"completed_steps_replayed": 0, "unknown_steps_retried_before_reconciliation": false,
			"completed_step_count": completedSteps, "unknown_step_count": unknownSteps,
			"adapter_dispatches": 0,
		})
	}
	return map[string]any{"operation_schema_valid": true, "operation_id_preserved": operation["operation_id"], "cases": results}, nil
}

// versionOffered reports whether a version list contains version.
func versionOffered(versions []any, version string) bool {
	for _, offered := range versions {
		if offered == version {
			return true
		}
	}
	return false
}

// recordedCapabilityAvailable: some unfinished step's exact adapter,
// capability name and version is among the available capabilities.
func recordedCapabilityAvailable(operation map[string]any, capabilities []any) bool {
	for _, rawCapability := range capabilities {
		capability := asObject(rawCapability)
		for _, rawStep := range arrayValue(operation, "steps") {
			step := asObject(rawStep)
			if stringValue(step, "status") != "succeeded" &&
				stringValue(step, "adapter_id") == stringValue(capability, "adapter_id") &&
				stringValue(step, "capability_name") == stringValue(capability, "capability_name") &&
				stringValue(step, "capability_version") == stringValue(capability, "capability_version") {
				return true
			}
		}
	}
	return false
}

// countStepStatuses counts the succeeded and outcome_unknown steps.
func countStepStatuses(operation map[string]any) (completed, unknown int) {
	for _, rawStep := range arrayValue(operation, "steps") {
		switch stringValue(asObject(rawStep), "status") {
		case "succeeded":
			completed++
		case "outcome_unknown":
			unknown++
		}
	}
	return completed, unknown
}
