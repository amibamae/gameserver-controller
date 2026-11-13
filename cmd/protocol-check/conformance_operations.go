package main

import (
	"fmt"
)

// Register the operation evaluators defined in this file.
func init() {
	registerEvaluator(evaluateOperationsAuthenticationClientSessionExpiry, "operations.authentication.client-session-expiry")
	registerEvaluator(withoutPackage(evaluateOperationsCancellation),
		"operations.cancellation.before-dispatch",
		"operations.cancellation.unknown-after-dispatch")
	registerEvaluator(withoutPackage(evaluateOperationsReconcileUnknownCreateOutcome), "operations.reconcile.unknown-create-outcome")
	registerEvaluator(withoutPackage(evaluateOperationsRecoveryRestartPreservesLock), "operations.recovery.restart-preserves-lock")
}

// evaluateOperationsAuthenticationClientSessionExpiry evaluates the operations.authentication.client-session-expiry conformance fixture.
func evaluateOperationsAuthenticationClientSessionExpiry(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	operation := asObject(input["operation"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
		return nil, fmt.Errorf("accepted operation does not match schema: %w", err)
	}
	if !boolValue(input, "durably_accepted") || !boolValue(input, "client_session_expired") {
		return nil, fmt.Errorf("fixture requires an accepted operation and an expired client session")
	}
	if stringValue(operation, "status") != "running" {
		return nil, fmt.Errorf("fixture requires a running accepted operation")
	}
	if !boolValue(input, "authorization_context_unchanged") || boolValue(input, "next_sensitive_step_reauthorization_required") {
		return nil, fmt.Errorf("fixture requires unchanged authorization and no separate sensitive-step reauthorization gate")
	}
	return map[string]any{
		"operation_schema_valid":                             true,
		"operation_id":                                       stringValue(operation, "operation_id"),
		"operation_status_after_expiry":                      stringValue(operation, "status"),
		"operation_remains_durable":                          true,
		"operation_continues_without_client_session":         true,
		"operation_cancelled_due_to_session_expiry":          false,
		"session_expiry_alone_requires_client_token_refresh": false,
		"authorization_context_unchanged":                    true,
		"sensitive_step_reauthorization_still_applies":       true,
		"token_material_exposed":                             false,
	}, nil
}

// evaluateOperationsCancellation evaluates the conformance fixtures
// operations.cancellation.before-dispatch, operations.cancellation.unknown-after-dispatch.
func evaluateOperationsCancellation(fixture map[string]any) (map[string]any, error) {
	initial := asObject(fixture["initial"])
	cancellationRequested := boolValue(fixture, "cancellation_requested")
	dispatchRecorded := boolValue(initial, "dispatch_recorded")
	providerResponseReceived := boolValue(initial, "provider_response_received")
	operationStatus, stepStatus := "", ""
	cancelled, cancellationApplied, reconciliationRequired := false, false, false
	resourceLockRetained := false
	completedSteps := arrayValue(initial, "completed_step_ids")
	if cancellationRequested && !dispatchRecorded && !boolValue(initial, "external_effect_possible") {
		operationStatus, stepStatus = "cancelled", "cancelled"
		cancelled, cancellationApplied = true, true
	} else if cancellationRequested && dispatchRecorded && !providerResponseReceived {
		operationStatus, stepStatus = "reconciling", "outcome_unknown"
		reconciliationRequired = true
		resourceLockRetained = boolValue(initial, "resource_lock_held")
	} else {
		return nil, fmt.Errorf("cancellation fixture must describe an undispatched step or a dispatched step with unknown outcome")
	}
	return map[string]any{
		"cancellation_request_recorded": cancellationRequested,
		"cancellation_applied":          cancellationApplied,
		"cancelled":                     cancelled,
		"operation_status":              operationStatus,
		"step_status":                   stepStatus,
		"reconciliation_required":       reconciliationRequired,
		"retry_without_reconciliation":  false,
		"additional_provider_calls":     0,
		"resource_lock_retained":        resourceLockRetained,
		"completed_step_ids_preserved":  completedSteps,
	}, nil
}

// evaluateOperationsReconcileUnknownCreateOutcome evaluates the operations.reconcile.unknown-create-outcome conformance fixture.
func evaluateOperationsReconcileUnknownCreateOutcome(fixture map[string]any) (map[string]any, error) {
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
}

// evaluateOperationsRecoveryRestartPreservesLock evaluates the operations.recovery.restart-preserves-lock conformance fixture.
func evaluateOperationsRecoveryRestartPreservesLock(fixture map[string]any) (map[string]any, error) {
	preRestart := asObject(fixture["pre_restart"])
	operation := asObject(preRestart["operation"])
	lock := asObject(preRestart["lock"])
	restart := asObject(fixture["restart"])
	request := asObject(fixture["conflicting_request"])
	if restart["controller_restarted"] != true || restart["external_outcome"] != "unknown" {
		return nil, fmt.Errorf("restart fixture must preserve an unknown external outcome")
	}
	if operation["status"] != "reconciling" || operation["unresolved_step_status"] != "outcome_unknown" {
		return nil, fmt.Errorf("restart fixture must contain a non-terminal unknown operation")
	}
	ownerID := operation["operation_id"]
	if lock["owner_operation_id"] != ownerID {
		return nil, fmt.Errorf("recovery lock must belong to the non-terminal operation")
	}
	conflicts := request["resource_kind"] == lock["resource_kind"] &&
		request["environment_id"] == lock["environment_id"] &&
		request["resource_id"] == lock["resource_id"]
	if !conflicts || request["authorized"] != true {
		return nil, fmt.Errorf("conflicting request must be authorized and target the locked resource")
	}
	return map[string]any{
		"operation_status_after_restart": operation["status"],
		"lock_held_after_restart":        true,
		"lock_owner_operation_id":        ownerID,
		"conflicting_request_accepted":   false,
		"error_code":                     "core.operation.conflict",
		"new_operation_created":          false,
		"side_effect_dispatch_count":     0,
	}, nil
}
