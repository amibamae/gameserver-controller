package main

import (
	"fmt"
	"reflect"
)

// Register the operation evaluators defined in this file.
func init() {
	registerEvaluator(evaluateOperationsAuthenticationClientSessionExpiry, "operations.authentication.client-session-expiry")
	registerEvaluator(withoutPackage(evaluateOperationsCancellation),
		"operations.cancellation.before-dispatch",
		"operations.cancellation.unknown-after-dispatch")
	registerEvaluator(withoutPackage(evaluateOperationsReconcileUnknownCreateOutcome), "operations.reconcile.unknown-create-outcome")
	registerEvaluator(evaluateTypedProvisionBackupRestoreDestroyResults,
		"operations.typed-provision-backup-restore-destroy-results",
		"operations.typed-provision-backup-restore-destroy-target-mismatch",
		"operations.typed-provision-backup-restore-readiness-binding",
		"operations.typed-provision-backup-restore-readiness-freshness",
		"operations.typed-restore-data-generation-not-rotated")
	registerEvaluator(evaluateTypedDefineStartStopResults,
		"operations.typed-define-start-stop-results",
		"operations.typed-define-start-stop-target-mismatch",
		"operations.typed-define-start-stop-future-readiness",
		"operations.typed-define-start-stop-no-drain",
		"operations.typed-define-start-stop-no-proxy-drain")
	registerEvaluator(withoutPackage(evaluateOperationsIdempotencySameKeySameRequest), "operations.idempotency.same-key-same-request")
	registerEvaluator(withoutPackage(evaluateOperationsIdempotencySameKeyDifferentRequest), "operations.idempotency.same-key-different-request")
	registerEvaluator(withoutPackage(evaluateOperationsIdempotencyExpiredKeyReuse), "operations.idempotency.expired-key-reuse")
	registerEvaluator(withoutPackage(evaluateOperationsRecoveryRestartPreservesLock), "operations.recovery.restart-preserves-lock")
	registerEvaluator(evaluateOperationsEventsDuplicateDelivery, "operations.events.duplicate-delivery")
	registerEvaluator(evaluateOperationsEventsGapRecovery, "operations.events.gap-recovery")
	registerEvaluator(evaluateOperationsEventsMonotonicSequence, "operations.events.monotonic-sequence")
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

// evaluateTypedProvisionBackupRestoreDestroyResults evaluates the conformance fixtures
// operations.typed-provision-backup-restore-destroy-results,
// operations.typed-provision-backup-restore-destroy-target-mismatch,
// operations.typed-provision-backup-restore-readiness-binding,
// operations.typed-provision-backup-restore-readiness-freshness,
// operations.typed-restore-data-generation-not-rotated.
func evaluateTypedProvisionBackupRestoreDestroyResults(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	provisionOperation := asObject(input["provision_operation"])
	backupOperation := asObject(input["backup_operation"])
	restoreOperation := asObject(input["restore_operation"])
	destroyOperation := asObject(input["destroy_operation"])
	for _, operation := range []struct {
		name  string
		value map[string]any
	}{
		{"server.provision", provisionOperation},
		{"server.backup", backupOperation},
		{"server.restore", restoreOperation},
		{"server.destroy", destroyOperation},
	} {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation.value); err != nil {
			return nil, fmt.Errorf("%s operation does not match schema: %w", operation.name, err)
		}
	}
	provisionResultBound, err := typedProvisionResultBound(provisionOperation)
	if err != nil {
		return nil, err
	}
	backupResultBound, err := typedBackupResultBound(backupOperation)
	if err != nil {
		return nil, err
	}
	restore, err := typedRestoreResult(restoreOperation, backupOperation, backupResultBound)
	if err != nil {
		return nil, err
	}
	destroyResultBound := typedDestroyResultBound(destroyOperation)
	allResultsBound := provisionResultBound && backupResultBound && restore.resultBound && destroyResultBound
	return map[string]any{
		"schemas_valid":                   true,
		"provision_result_bound":          provisionResultBound,
		"backup_result_bound":             backupResultBound,
		"restore_backup_owner_bound":      restore.backupOwnerBound,
		"restore_data_generation_rotated": restore.dataGenerationRotated,
		"restore_result_bound":            restore.resultBound,
		"destroy_result_bound":            destroyResultBound,
		"all_results_bound":               allResultsBound,
	}, nil
}

// operationData returns an operation's result.operation_data.
func operationData(operation map[string]any) map[string]any {
	return asObject(asObject(operation["result"])["operation_data"])
}

// operationParameters returns an operation's request_snapshot.parameters.
func operationParameters(operation map[string]any) map[string]any {
	return asObject(asObject(operation["request_snapshot"])["parameters"])
}

// typedProvisionResultBound: a provision result names its target, reports
// it ready, and carries a readiness assertion for the same instance that
// was current when the operation completed.
func typedProvisionResultBound(operation map[string]any) (bool, error) {
	target := asObject(operation["target"])
	data := operationData(operation)
	assertion := asObject(data["readiness_assertion"])
	completedAt, err := parseTime(stringValue(operation, "completed_at"))
	if err != nil {
		return false, err
	}
	observedAt, err := parseTime(stringValue(assertion, "observed_at"))
	if err != nil {
		return false, err
	}
	expiresAt, err := parseTime(stringValue(assertion, "expires_at"))
	if err != nil {
		return false, err
	}
	return stringValue(data, "server_id") == stringValue(target, "server_id") &&
		stringValue(data, "environment_id") == stringValue(target, "environment_id") &&
		stringValue(assertion, "server_id") == stringValue(data, "server_id") &&
		stringValue(assertion, "environment_id") == stringValue(data, "environment_id") &&
		stringValue(asObject(data["instance_ref"]), "environment_id") == stringValue(data, "environment_id") &&
		stringValue(asObject(assertion["instance_ref"]), "environment_id") == stringValue(assertion, "environment_id") &&
		reflect.DeepEqual(data["instance_ref"], assertion["instance_ref"]) &&
		stringValue(data, "lifecycle_state") == "ready" &&
		!completedAt.Before(observedAt) && completedAt.Before(expiresAt), nil
}

// typedBackupResultBound: a backup result and its manifest name the target
// server, environment and source instance (the expected one, if requested),
// and readiness held when it completed.
func typedBackupResultBound(operation map[string]any) (bool, error) {
	target := asObject(operation["target"])
	parameters := operationParameters(operation)
	data := operationData(operation)
	manifest := asObject(data["manifest"])
	readinessBound, err := readinessAssertionBindsAtCompletion(asObject(data["readiness_assertion"]),
		stringValue(operation, "completed_at"), stringValue(target, "server_id"), stringValue(target, "environment_id"), target["instance_ref"])
	if err != nil {
		return false, fmt.Errorf("backup readiness assertion: %w", err)
	}
	return stringValue(target, "server_id") == stringValue(manifest, "server_id") &&
		stringValue(target, "environment_id") == stringValue(data, "environment_id") &&
		stringValue(data, "environment_id") == stringValue(manifest, "environment_id") &&
		stringValue(data, "backup_id") == stringValue(manifest, "backup_id") &&
		stringValue(data, "data_generation_id") == stringValue(manifest, "data_generation_id") &&
		stringValue(asObject(target["instance_ref"]), "environment_id") == stringValue(target, "environment_id") &&
		reflect.DeepEqual(target["instance_ref"], manifest["source_instance_ref"]) &&
		(parameters["expected_instance_ref"] == nil ||
			reflect.DeepEqual(asObject(parameters["expected_instance_ref"]), asObject(manifest["source_instance_ref"]))) &&
		readinessBound, nil
}

// typedRestore is what a restore result proves.
type typedRestore struct {
	backupOwnerBound, dataGenerationRotated, resultBound bool
}

// typedRestoreResult: a restore uses a verified, succeeded backup of the
// same server, writes a new data generation, and names its target instance
// (the requested one, if any), with readiness current at completion.
func typedRestoreResult(operation, backupOperation map[string]any, backupResultBound bool) (typedRestore, error) {
	target := asObject(operation["target"])
	parameters := operationParameters(operation)
	data := operationData(operation)
	backupData := operationData(backupOperation)
	manifest := asObject(backupData["manifest"])
	readinessBound, err := readinessAssertionBindsAtCompletion(asObject(data["readiness_assertion"]),
		stringValue(operation, "completed_at"), stringValue(target, "server_id"), stringValue(target, "environment_id"), target["instance_ref"])
	if err != nil {
		return typedRestore{}, fmt.Errorf("restore readiness assertion: %w", err)
	}
	var result typedRestore
	result.backupOwnerBound = backupResultBound &&
		stringValue(parameters, "backup_id") == stringValue(manifest, "backup_id") &&
		stringValue(target, "server_id") == stringValue(manifest, "server_id") &&
		stringValue(target, "environment_id") == stringValue(manifest, "environment_id") &&
		stringValue(backupOperation, "status") == "succeeded" &&
		stringValue(backupData, "verified_at") != ""
	result.dataGenerationRotated = stringValue(data, "data_generation_id") != "" &&
		stringValue(data, "data_generation_id") != stringValue(manifest, "data_generation_id")
	result.resultBound = result.backupOwnerBound && result.dataGenerationRotated &&
		stringValue(target, "environment_id") == stringValue(asObject(target["instance_ref"]), "environment_id") &&
		stringValue(parameters, "backup_id") == stringValue(data, "backup_id") &&
		reflect.DeepEqual(target["instance_ref"], data["target_instance_ref"]) &&
		(parameters["target_instance_ref"] == nil ||
			reflect.DeepEqual(asObject(parameters["target_instance_ref"]), asObject(data["target_instance_ref"]))) &&
		readinessBound
	return result, nil
}

// typedDestroyResultBound: a destroy result names the expected instance,
// confirms the provider no longer has it, and names the verified backup
// if one was required.
func typedDestroyResultBound(operation map[string]any) bool {
	target := asObject(operation["target"])
	parameters := operationParameters(operation)
	data := operationData(operation)
	instance := asObject(target["instance_ref"])
	backupBound := parameters["verified_backup_id"] == nil ||
		stringValue(parameters, "verified_backup_id") == stringValue(data, "verified_backup_id")
	return stringValue(target, "environment_id") == stringValue(instance, "environment_id") &&
		reflect.DeepEqual(instance, asObject(data["instance_ref"])) &&
		reflect.DeepEqual(target["instance_ref"], parameters["expected_instance_ref"]) &&
		stringValue(data, "final_lifecycle_state") == "destroyed" &&
		boolValue(data, "provider_absence_confirmed") && backupBound &&
		(parameters["expected_instance_ref"] == nil || reflect.DeepEqual(asObject(parameters["expected_instance_ref"]), instance))
}

// evaluateTypedDefineStartStopResults evaluates the conformance fixtures
// operations.typed-define-start-stop-results, operations.typed-define-start-stop-target-mismatch,
// operations.typed-define-start-stop-future-readiness, operations.typed-define-start-stop-no-drain,
// operations.typed-define-start-stop-no-proxy-drain.
func evaluateTypedDefineStartStopResults(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	defineOperation := asObject(input["define_operation"])
	startOperation := asObject(input["start_operation"])
	stopOperation := asObject(input["stop_operation"])
	for _, operation := range []struct {
		name  string
		value map[string]any
	}{
		{"server.define", defineOperation},
		{"server.start", startOperation},
		{"server.stop", stopOperation},
	} {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation.value); err != nil {
			return nil, fmt.Errorf("%s operation does not match schema: %w", operation.name, err)
		}
	}
	defineResult := asObject(defineOperation["result"])
	startResult := asObject(startOperation["result"])
	stopResult := asObject(stopOperation["result"])
	defineData := asObject(defineResult["operation_data"])
	startData := asObject(startResult["operation_data"])
	stopData := asObject(stopResult["operation_data"])
	defineTarget := asObject(defineOperation["target"])
	startTarget, stopTarget := asObject(startOperation["target"]), asObject(stopOperation["target"])
	startAssertion := asObject(startData["readiness_assertion"])
	startInstance := asObject(startTarget["instance_ref"])
	assertedInstance := asObject(startAssertion["instance_ref"])
	startReadinessAtCompletion, err := readinessAssertionBindsAtCompletion(
		startAssertion,
		stringValue(startOperation, "completed_at"),
		stringValue(startTarget, "server_id"),
		stringValue(startTarget, "environment_id"),
		startTarget["instance_ref"],
	)
	if err != nil {
		return nil, fmt.Errorf("start readiness assertion: %w", err)
	}
	defineResultBound := stringValue(defineData, "server_id") == stringValue(defineTarget, "server_id") &&
		stringValue(defineData, "environment_id") == stringValue(defineTarget, "environment_id")
	serverIdentityConsistent := stringValue(defineData, "server_id") == stringValue(startData, "server_id") &&
		stringValue(startData, "server_id") == stringValue(stopData, "server_id") &&
		stringValue(defineData, "environment_id") == stringValue(startData, "environment_id") &&
		stringValue(startData, "environment_id") == stringValue(stopData, "environment_id")
	startReadinessBound := stringValue(startData, "server_id") == stringValue(startAssertion, "server_id") &&
		stringValue(startData, "environment_id") == stringValue(startAssertion, "environment_id") &&
		stringValue(startData, "server_id") == stringValue(startTarget, "server_id") &&
		stringValue(startData, "environment_id") == stringValue(startTarget, "environment_id") &&
		reflect.DeepEqual(startData["instance_ref"], startTarget["instance_ref"]) &&
		reflect.DeepEqual(startTarget["instance_ref"], startAssertion["instance_ref"]) &&
		stringValue(startInstance, "environment_id") == stringValue(startData, "environment_id") &&
		stringValue(assertedInstance, "environment_id") == stringValue(startAssertion, "environment_id")
	startReadinessCurrent := startReadinessBound && startReadinessAtCompletion &&
		stringValue(startData, "lifecycle_state") == "ready"
	defineNoInfrastructure := stringValue(defineData, "lifecycle_state") == "unprovisioned" &&
		len(arrayValue(defineResult, "affected_resources")) == 0
	gameSave, execution, proxyDrain := asObject(stopData["game_save"]), asObject(stopData["execution"]), asObject(stopData["proxy_drain"])
	stopParameters := asObject(asObject(stopOperation["request_snapshot"])["parameters"])
	drainRequested := boolValue(stopParameters, "drain")
	drainOutcome := stringValue(proxyDrain, "outcome")
	drainSatisfied := drainOutcome == "drained" || drainOutcome == "not_configured" ||
		(drainOutcome == "not_requested" && !drainRequested)
	stopBound := stringValue(stopData, "server_id") == stringValue(stopTarget, "server_id") &&
		stringValue(stopData, "environment_id") == stringValue(stopTarget, "environment_id") &&
		reflect.DeepEqual(stopData["instance_ref"], stopTarget["instance_ref"])
	stopPostconditions := stopBound && stringValue(stopData, "lifecycle_state") == "stopped" &&
		stringValue(gameSave, "outcome") == "confirmed" && stringValue(execution, "outcome") == "stopped" &&
		drainSatisfied
	return map[string]any{
		"schemas_valid":                     true,
		"server_identity_consistent":        serverIdentityConsistent,
		"define_result_bound":               defineResultBound,
		"define_has_no_infrastructure":      defineNoInfrastructure,
		"start_readiness_bound_and_current": startReadinessCurrent,
		"stop_postconditions_confirmed":     stopPostconditions,
	}, nil
}

// evaluateOperationsIdempotencySameKeySameRequest evaluates the operations.idempotency.same-key-same-request conformance fixture.
func evaluateOperationsIdempotencySameKeySameRequest(fixture map[string]any) (map[string]any, error) {
	first, second := asObject(fixture["first_request"]), asObject(fixture["second_request"])
	sameBinding := first["actor_id"] == second["actor_id"] && first["idempotency_key"] == second["idempotency_key"]
	reused := sameBinding && reflect.DeepEqual(first, second)
	operationID := any(nil)
	operationCount, dispatchCount := 2, 2
	if reused {
		operationID = fixture["existing_operation_id"]
		operationCount, dispatchCount = 1, 1
	}
	return map[string]any{
		"same_operation_reused":      reused,
		"operation_id":               operationID,
		"operation_count":            operationCount,
		"side_effect_dispatch_count": dispatchCount,
	}, nil
}

// evaluateOperationsIdempotencySameKeyDifferentRequest evaluates the operations.idempotency.same-key-different-request conformance fixture.
func evaluateOperationsIdempotencySameKeyDifferentRequest(fixture map[string]any) (map[string]any, error) {
	first, second := asObject(fixture["first_request"]), asObject(fixture["second_request"])
	sameBinding := first["actor_id"] == second["actor_id"] && first["idempotency_key"] == second["idempotency_key"]
	conflict := sameBinding && !reflect.DeepEqual(first, second)
	code := any(nil)
	count := 2
	if conflict {
		code, count = "core.operation.idempotency_conflict", 1
	}
	return map[string]any{"second_request_accepted": !conflict, "error_code": code, "operation_count": count}, nil
}

// evaluateOperationsIdempotencyExpiredKeyReuse evaluates the operations.idempotency.expired-key-reuse conformance fixture.
func evaluateOperationsIdempotencyExpiredKeyReuse(fixture map[string]any) (map[string]any, error) {
	tombstone := asObject(fixture["expired_tombstone"])
	request := asObject(fixture["retry_request"])
	if tombstone["tombstone_present"] != true || tombstone["full_operation_record_retained"] != false {
		return nil, fmt.Errorf("expired-key fixture must contain only a tombstone")
	}
	if tombstone["actor_id"] != request["actor_id"] || tombstone["idempotency_key"] != request["idempotency_key"] {
		return nil, fmt.Errorf("retry request must match the expired tombstone scope")
	}
	return map[string]any{
		"second_request_accepted":     false,
		"error_code":                  "core.operation.idempotency_key_expired",
		"new_operation_created":       false,
		"side_effect_dispatch_count":  0,
		"original_operation_replayed": false,
		"tombstone_retained":          true,
	}, nil
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

// evaluateOperationsEventsDuplicateDelivery evaluates the operations.events.duplicate-delivery conformance fixture.
func evaluateOperationsEventsDuplicateDelivery(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	lastSequence, err := integerValue(fixture["last_consumed_sequence"])
	if err != nil {
		return nil, err
	}
	deliveries := arrayValue(fixture, "deliveries")
	if len(deliveries) == 0 {
		return nil, fmt.Errorf("event deliveries must not be empty")
	}
	var operationID string
	seen := make(map[string]map[string]any)
	appliedEventIDs := make([]any, 0, len(deliveries))
	duplicateCount := 0
	for index, rawEvent := range deliveries {
		event := asObject(rawEvent)
		if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
			return nil, fmt.Errorf("delivery %d does not match schema: %w", index, err)
		}
		id := stringValue(event, "operation_id")
		if index == 0 {
			operationID = id
		} else if id != operationID {
			return nil, fmt.Errorf("delivery %d belongs to a different operation", index)
		}
		eventID := stringValue(event, "event_id")
		if previous, exists := seen[eventID]; exists {
			if !reflect.DeepEqual(previous, event) {
				return nil, fmt.Errorf("duplicate event ID %q has different content", eventID)
			}
			duplicateCount++
			continue
		}
		sequence, err := integerValue(event["sequence"])
		if err != nil {
			return nil, err
		}
		if sequence != lastSequence+1 {
			return nil, fmt.Errorf("new event sequence %d does not immediately follow cursor %d", sequence, lastSequence)
		}
		seen[eventID] = event
		appliedEventIDs = append(appliedEventIDs, eventID)
		lastSequence = sequence
	}
	return map[string]any{
		"schema_valid":                 true,
		"applied_event_ids":            appliedEventIDs,
		"applied_event_count":          len(appliedEventIDs),
		"duplicate_deliveries_ignored": duplicateCount,
		"duplicate_event_applications": 0,
		"last_consumed_sequence":       lastSequence,
		"cursor_regressed":             false,
	}, nil
}

// evaluateOperationsEventsGapRecovery evaluates the operations.events.gap-recovery conformance fixture.
func evaluateOperationsEventsGapRecovery(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	lastSequence, err := integerValue(fixture["last_consumed_sequence"])
	if err != nil {
		return nil, err
	}
	events := arrayValue(fixture, "events")
	if len(events) == 0 {
		return nil, fmt.Errorf("event sequence must not be empty")
	}
	var operationID string
	currentSequence := lastSequence
	var expectedSequence, receivedSequence int64
	gapDetected := false
	for index, rawEvent := range events {
		event := asObject(rawEvent)
		if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
			return nil, fmt.Errorf("event %d does not match schema: %w", index, err)
		}
		id := stringValue(event, "operation_id")
		if index == 0 {
			operationID = id
		} else if id != operationID {
			return nil, fmt.Errorf("event %d belongs to a different operation", index)
		}
		sequence, err := integerValue(event["sequence"])
		if err != nil {
			return nil, err
		}
		if sequence <= currentSequence {
			return nil, fmt.Errorf("event sequence %d does not advance past %d", sequence, currentSequence)
		}
		if sequence > currentSequence+1 {
			gapDetected = true
			expectedSequence = currentSequence + 1
			receivedSequence = sequence
			currentSequence = sequence
			break
		}
		currentSequence = sequence
	}
	if !gapDetected {
		return nil, fmt.Errorf("gap-recovery fixture must contain a missing event sequence")
	}
	recovery := asObject(fixture["recovery"])
	operationResponse := asObject(recovery["operation_resource_get"])
	responseStatus, err := integerValue(operationResponse["status"])
	if err != nil {
		return nil, err
	}
	if responseStatus != 200 {
		return nil, fmt.Errorf("gap recovery requires a successful authoritative operation read")
	}
	operation := asObject(operationResponse["operation"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
		return nil, fmt.Errorf("authoritative operation snapshot does not match schema: %w", err)
	}
	if stringValue(operation, "operation_id") != operationID {
		return nil, fmt.Errorf("authoritative operation snapshot belongs to a different operation")
	}
	return map[string]any{
		"event_schema_valid":                  true,
		"operation_schema_valid":              true,
		"gap_detected":                        true,
		"expected_sequence":                   expectedSequence,
		"received_sequence":                   receivedSequence,
		"authoritative_operation_fetched":     true,
		"operation_id":                        operationID,
		"recovered_status":                    operation["status"],
		"recovered_steps":                     operation["steps"],
		"missing_event_details_reconstructed": false,
		"side_effect_dispatches":              0,
		"replayed_events":                     0,
		"resume_after_sequence":               currentSequence,
	}, nil
}

// evaluateOperationsEventsMonotonicSequence evaluates the operations.events.monotonic-sequence conformance fixture.
func evaluateOperationsEventsMonotonicSequence(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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
}
