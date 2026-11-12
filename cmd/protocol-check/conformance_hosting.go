package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// Register the hosting adapter evaluators defined in this file.
func init() {
	registerEvaluator(evaluateHostingInstanceWait,
		"hosting.instance.wait.pending-remains-pending",
		"hosting.instance.wait.succeeded-postcondition-confirmed",
		"hosting.instance.wait.succeeded-state-mismatch",
		"hosting.instance.wait.unknown-requires-inspection",
		"hosting.instance.wait.operation-reference-mismatch",
		"hosting.instance.wait.failed-terminal-requires-inspection",
		"hosting.instance.wait.without-operation-reference")
	registerEvaluator(evaluateHostingInstanceDestroyApproval,
		"hosting.instance.destroy.approval-exact-scope",
		"hosting.instance.destroy.approval-wrong-action",
		"hosting.instance.destroy.approval-target-mismatch",
		"hosting.instance.destroy.approval-environment-mismatch",
		"hosting.instance.destroy.approval-plan-mismatch",
		"hosting.instance.destroy.approval-expired",
		"hosting.instance.destroy.approval-missing-approver")
	registerEvaluator(evaluateHostingInstanceDestroy,
		"hosting.instance.destroy.authorization-required",
		"hosting.instance.destroy.verified-backup-required",
		"hosting.instance.destroy.current-generation-backup-required",
		"hosting.instance.destroy.stopped-required",
		"hosting.instance.destroy.completed-destroyed",
		"hosting.instance.destroy.accepted-not-destroyed",
		"hosting.instance.destroy.unknown-requires-inspection",
		"hosting.instance.destroy.target-mismatch")
	registerEvaluator(evaluateHostingInstanceRebootCoreGate,
		"hosting.instance.reboot.core-missing-save-blocked",
		"hosting.instance.reboot.core-save-bound",
		"hosting.instance.reboot.core-clean-stop-bound",
		"hosting.instance.reboot.core-save-mismatch-blocked",
		"hosting.instance.reboot.core-unknown-data-blocked",
		"hosting.instance.reboot.core-emergency-approved",
		"hosting.instance.reboot.core-emergency-target-mismatch",
		"hosting.instance.reboot.core-emergency-wrong-action",
		"hosting.instance.reboot.core-no-data-no-gate")
	registerEvaluator(evaluateHostingInstanceReboot,
		"hosting.instance.reboot.completed-active",
		"hosting.instance.reboot.accepted-not-complete",
		"hosting.instance.reboot.unknown-requires-inspection",
		"hosting.instance.reboot.target-mismatch")
	registerEvaluator(evaluateHostingInstanceStartStop,
		"hosting.instance.start.completed-active",
		"hosting.instance.start.accepted-not-active",
		"hosting.instance.start.unknown-requires-inspection",
		"hosting.instance.start.target-mismatch",
		"hosting.instance.stop.completed-stopped",
		"hosting.instance.stop.accepted-not-stopped",
		"hosting.instance.stop.unknown-requires-inspection",
		"hosting.instance.stop.target-mismatch")
	registerEvaluator(evaluateHostingCatalogList,
		"hosting.regions.list.filtered",
		"hosting.plans.list.resource-filter",
		"hosting.plans.list.minimum-filter-mismatch",
		"hosting.os_offerings.list.unknown-not-available")
	registerEvaluator(evaluateHostingInstanceInspect,
		"hosting.instance.inspect.exact-active",
		"hosting.instance.inspect.unknown-not-active",
		"hosting.instance.inspect.target-mismatch")
	registerEvaluator(evaluateHostingProvisionValidStandardOs, "hosting.provision.valid.standard-os")
	registerEvaluator(evaluateHostingBootstrapExecutionBindingAndReadinessGate, "hosting.bootstrap.execution-binding-and-readiness-gate")
	registerEvaluator(evaluateHostingBootstrapRejectsFalseSuccess, "hosting.bootstrap.rejects-false-success")
}

// evaluateHostingInstanceWait evaluates the conformance fixtures
// hosting.instance.wait.pending-remains-pending,
// hosting.instance.wait.succeeded-postcondition-confirmed,
// hosting.instance.wait.succeeded-state-mismatch,
// hosting.instance.wait.unknown-requires-inspection,
// hosting.instance.wait.operation-reference-mismatch,
// hosting.instance.wait.failed-terminal-requires-inspection,
// hosting.instance.wait.without-operation-reference.
func evaluateHostingInstanceWait(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	waitInput, waitOutput := asObject(input["wait_input"]), asObject(input["wait_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-wait-input", waitInput); err != nil {
		return nil, fmt.Errorf("hosting wait input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-wait-output", waitOutput); err != nil {
		return nil, fmt.Errorf("hosting wait output does not match schema: %w", err)
	}
	targetBound := reflect.DeepEqual(asObject(waitInput["instance_ref"]), asObject(waitOutput["instance_ref"]))
	requestedTarget := asObject(waitInput["instance_ref"])
	operationRef := asObject(waitInput["provider_operation_ref"])
	observedOperationRef := asObject(waitOutput["provider_operation_ref"])
	operationBound, operationScopeMatches := true, true
	if len(operationRef) > 0 {
		operationBound = reflect.DeepEqual(operationRef, observedOperationRef)
		operationScopeMatches = stringValue(operationRef, "adapter_id") == stringValue(requestedTarget, "adapter_id") &&
			stringValue(operationRef, "environment_id") == stringValue(requestedTarget, "environment_id")
	} else if len(observedOperationRef) > 0 {
		operationScopeMatches = stringValue(observedOperationRef, "adapter_id") == stringValue(requestedTarget, "adapter_id") &&
			stringValue(observedOperationRef, "environment_id") == stringValue(requestedTarget, "environment_id") &&
			stringValue(observedOperationRef, "resource_kind") == "operation"
	}
	status := stringValue(waitOutput, "operation_status")
	state := stringValue(waitOutput, "instance_state")
	referencesBound := targetBound && operationBound && operationScopeMatches
	waitCompleted := referencesBound && (status == "succeeded" || status == "failed")
	postconditionConfirmed := referencesBound && status == "succeeded" &&
		state == stringValue(waitInput, "expected_state")
	return map[string]any{
		"schemas_valid":                    true,
		"target_bound":                     targetBound,
		"operation_reference_bound":        operationBound && operationScopeMatches,
		"wait_completed":                   waitCompleted,
		"poll_again":                       referencesBound && status == "pending",
		"instance_postcondition_confirmed": postconditionConfirmed,
		"unknown_requires_inspection":      status == "unknown",
		"result_requires_inspection":       !referencesBound || status == "unknown" || status == "failed",
	}, nil
}

// evaluateHostingInstanceDestroyApproval evaluates the conformance fixtures
// hosting.instance.destroy.approval-exact-scope, hosting.instance.destroy.approval-wrong-action,
// hosting.instance.destroy.approval-target-mismatch,
// hosting.instance.destroy.approval-environment-mismatch,
// hosting.instance.destroy.approval-plan-mismatch, hosting.instance.destroy.approval-expired,
// hosting.instance.destroy.approval-missing-approver.
func evaluateHostingInstanceDestroyApproval(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	destroyInput := asObject(input["destroy_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-input", destroyInput); err != nil {
		return nil, fmt.Errorf("hosting destroy input does not match schema: %w", err)
	}
	preconditions := asObject(fixture["core_preconditions"])
	request := asObject(fixture["request_binding"])
	approval := asObject(preconditions["approval"])
	target := asObject(destroyInput["instance_ref"])
	requestBindingMatches := destroyRequestBindingMatches(request, target)
	approvalScopeMatches := destroyApprovalScopeMatches(approval, request, target)
	approvalCurrent := approvalCurrentAt(approval, stringValue(fixture, "evaluation_time"))
	approvalApproverPresent := stringValue(approval, "approval_id") != "" && stringValue(approval, "approver_id") != ""
	approvalBound := requestBindingMatches && approvalScopeMatches && approvalCurrent && approvalApproverPresent
	allowed := boolValue(preconditions, "authorization_valid") && approvalBound &&
		boolValue(preconditions, "backup_verified") && boolValue(preconditions, "backup_generation_matches") &&
		boolValue(preconditions, "instance_stopped")
	output := asObject(input["destroy_output"])
	outputPresent := len(output) > 0
	targetBound, confirmed := false, false
	if outputPresent {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-output", output); err != nil {
			return nil, fmt.Errorf("hosting destroy output does not match schema: %w", err)
		}
		targetBound = reflect.DeepEqual(target, asObject(output["instance_ref"]))
		confirmed = allowed && targetBound && stringValue(output, "outcome") == "completed" && stringValue(output, "state") == "destroyed"
	}
	providerCalls := 0
	if allowed {
		providerCalls = 1
	}
	return map[string]any{
		"schemas_valid":             true,
		"request_binding_matches":   requestBindingMatches,
		"approval_scope_matches":    approvalScopeMatches,
		"approval_current":          approvalCurrent,
		"approval_approver_present": approvalApproverPresent,
		"approval_bound":            approvalBound,
		"destroy_dispatch_allowed":  allowed,
		"provider_destroy_calls":    providerCalls,
		"target_bound":              targetBound,
		"destruction_confirmed":     confirmed,
	}, nil
}

// destroyRequestBindingMatches: the request is a hosting.instance.destroy
// of this instance by its adapter in its environment, with a logical
// server and a plan fingerprint.
func destroyRequestBindingMatches(request, target map[string]any) bool {
	return stringValue(request, "action") == "hosting.instance.destroy" &&
		stringValue(request, "logical_server_id") != "" &&
		stringValue(request, "environment_id") == stringValue(target, "environment_id") &&
		stringValue(request, "adapter_id") == stringValue(target, "adapter_id") &&
		stringValue(request, "plan_fingerprint") != "" &&
		reflect.DeepEqual(asObject(request["instance_ref"]), target)
}

// destroyApprovalScopeMatches: the approval covers exactly the request's
// action, server, environment, adapter, instance and plan.
func destroyApprovalScopeMatches(approval, request, target map[string]any) bool {
	return stringValue(approval, "action") == stringValue(request, "action") &&
		stringValue(approval, "logical_server_id") == stringValue(request, "logical_server_id") &&
		stringValue(approval, "environment_id") == stringValue(request, "environment_id") &&
		stringValue(approval, "adapter_id") == stringValue(request, "adapter_id") &&
		reflect.DeepEqual(asObject(approval["instance_ref"]), target) &&
		reflect.DeepEqual(asObject(approval["instance_ref"]), asObject(request["instance_ref"])) &&
		stringValue(approval, "plan_fingerprint") != "" &&
		stringValue(approval, "plan_fingerprint") == stringValue(request, "plan_fingerprint")
}

// approvalCurrentAt: the approval is marked valid and expires after the
// evaluation time; both times must parse.
func approvalCurrentAt(approval map[string]any, evaluationTime string) bool {
	evaluatedAt, evaluationErr := time.Parse(time.RFC3339, evaluationTime)
	expiresAt, expiryErr := time.Parse(time.RFC3339, stringValue(approval, "expires_at"))
	return boolValue(approval, "valid") && evaluationErr == nil && expiryErr == nil && expiresAt.After(evaluatedAt)
}

// evaluateHostingInstanceDestroy evaluates the conformance fixtures
// hosting.instance.destroy.authorization-required,
// hosting.instance.destroy.verified-backup-required,
// hosting.instance.destroy.current-generation-backup-required,
// hosting.instance.destroy.stopped-required, hosting.instance.destroy.completed-destroyed,
// hosting.instance.destroy.accepted-not-destroyed,
// hosting.instance.destroy.unknown-requires-inspection, hosting.instance.destroy.target-mismatch.
func evaluateHostingInstanceDestroy(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	destroyInput := asObject(input["destroy_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-input", destroyInput); err != nil {
		return nil, fmt.Errorf("hosting destroy input does not match schema: %w", err)
	}
	preconditions := asObject(fixture["core_preconditions"])
	allowed := boolValue(preconditions, "authorization_valid") && boolValue(preconditions, "backup_verified") &&
		boolValue(preconditions, "backup_generation_matches") && boolValue(preconditions, "instance_stopped")
	output := asObject(input["destroy_output"])
	outputPresent := len(output) > 0
	targetBound, confirmed, unknownRequiresInspection, resultRequiresInspection := false, false, false, false
	outcome, state := "", ""
	if outputPresent {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-output", output); err != nil {
			return nil, fmt.Errorf("hosting destroy output does not match schema: %w", err)
		}
		targetBound = reflect.DeepEqual(asObject(destroyInput["instance_ref"]), asObject(output["instance_ref"]))
		outcome, state = stringValue(output, "outcome"), stringValue(output, "state")
		confirmed = allowed && targetBound && outcome == "completed" && state == "destroyed"
		unknownRequiresInspection = outcome == "outcome_unknown"
		resultRequiresInspection = !targetBound || unknownRequiresInspection
	}
	destroyCalls := 0
	if allowed {
		destroyCalls = 1
	}
	return map[string]any{
		"schemas_valid":               true,
		"destroy_dispatch_allowed":    allowed,
		"provider_destroy_calls":      destroyCalls,
		"target_bound":                targetBound,
		"destruction_confirmed":       confirmed,
		"unknown_requires_inspection": unknownRequiresInspection,
		"result_requires_inspection":  resultRequiresInspection,
	}, nil
}

// evaluateHostingInstanceRebootCoreGate evaluates the conformance fixtures
// hosting.instance.reboot.core-missing-save-blocked, hosting.instance.reboot.core-save-bound,
// hosting.instance.reboot.core-clean-stop-bound,
// hosting.instance.reboot.core-save-mismatch-blocked,
// hosting.instance.reboot.core-unknown-data-blocked,
// hosting.instance.reboot.core-emergency-approved,
// hosting.instance.reboot.core-emergency-target-mismatch,
// hosting.instance.reboot.core-emergency-wrong-action,
// hosting.instance.reboot.core-no-data-no-gate.
func evaluateHostingInstanceRebootCoreGate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	rebootInput := asObject(input["reboot_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-input", rebootInput); err != nil {
		return nil, fmt.Errorf("hosting reboot input does not match schema: %w", err)
	}
	core := asObject(input["core_preconditions"])
	target := asObject(rebootInput["instance_ref"])
	dataStatus := stringValue(core, "persistent_data_status")
	if dataStatus != "present" && dataStatus != "absent" && dataStatus != "unknown" {
		return nil, fmt.Errorf("persistent_data_status must be present, absent, or unknown")
	}
	dataSafetyGateRequired := dataStatus != "absent"
	saveEvidenceBound := boolValue(core, "game_save_confirmed") &&
		reflect.DeepEqual(target, asObject(core["game_save_instance_ref"]))
	cleanStopEvidenceBound := boolValue(core, "clean_stop_confirmed") &&
		reflect.DeepEqual(target, asObject(core["clean_stop_instance_ref"]))
	routineRebootAllowed := !dataSafetyGateRequired || saveEvidenceBound || cleanStopEvidenceBound
	approval := asObject(core["emergency_approval"])
	emergencyApprovalBound := boolValue(approval, "valid") &&
		stringValue(approval, "approval_id") != "" &&
		stringValue(approval, "approver_id") != "" &&
		stringValue(approval, "action") == "hosting.instance.reboot.emergency" &&
		reflect.DeepEqual(target, asObject(approval["instance_ref"]))
	dispatchAllowed := routineRebootAllowed || emergencyApprovalBound
	emergencyBypassUsed := dataSafetyGateRequired && !saveEvidenceBound && !cleanStopEvidenceBound && emergencyApprovalBound
	return map[string]any{
		"core_preconditions_valid":         true,
		"data_safety_gate_required":        dataSafetyGateRequired,
		"save_evidence_bound":              saveEvidenceBound,
		"clean_stop_evidence_bound":        cleanStopEvidenceBound,
		"routine_reboot_allowed":           routineRebootAllowed,
		"emergency_approval_bound":         emergencyApprovalBound,
		"provider_reboot_dispatch_allowed": dispatchAllowed,
		"emergency_bypass_used":            emergencyBypassUsed,
		"game_readiness_established":       false,
	}, nil
}

// evaluateHostingInstanceReboot evaluates the conformance fixtures
// hosting.instance.reboot.completed-active, hosting.instance.reboot.accepted-not-complete,
// hosting.instance.reboot.unknown-requires-inspection, hosting.instance.reboot.target-mismatch.
func evaluateHostingInstanceReboot(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	rebootInput, rebootOutput := asObject(input["reboot_input"]), asObject(input["reboot_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-input", rebootInput); err != nil {
		return nil, fmt.Errorf("hosting reboot input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-output", rebootOutput); err != nil {
		return nil, fmt.Errorf("hosting reboot output does not match schema: %w", err)
	}
	targetBound := reflect.DeepEqual(asObject(rebootInput["instance_ref"]), asObject(rebootOutput["instance_ref"]))
	outcome := stringValue(rebootOutput, "outcome")
	state := stringValue(rebootOutput, "state")
	return map[string]any{
		"schemas_valid":               true,
		"target_bound":                targetBound,
		"completion_confirmed":        targetBound && outcome == "completed" && state == "active",
		"unknown_requires_inspection": outcome == "outcome_unknown",
		"result_requires_inspection":  !targetBound || outcome == "outcome_unknown",
	}, nil
}

// evaluateHostingInstanceStartStop evaluates the conformance fixtures
// hosting.instance.start.completed-active, hosting.instance.start.accepted-not-active,
// hosting.instance.start.unknown-requires-inspection, hosting.instance.start.target-mismatch,
// hosting.instance.stop.completed-stopped, hosting.instance.stop.accepted-not-stopped,
// hosting.instance.stop.unknown-requires-inspection, hosting.instance.stop.target-mismatch.
func evaluateHostingInstanceStartStop(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	action := stringValue(input, "action")
	actionInput, actionOutput := asObject(input["hosting_input"]), asObject(input["hosting_output"])
	inputSchemaID, outputSchemaID := "", ""
	if action == "start" {
		inputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:instance-start-input"
		outputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:instance-start-output"
	} else {
		inputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:instance-stop-input"
		outputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:instance-stop-output"
	}
	if err := pkg.validateSchema(inputSchemaID, actionInput); err != nil {
		return nil, fmt.Errorf("hosting instance %s input does not match schema: %w", action, err)
	}
	if err := pkg.validateSchema(outputSchemaID, actionOutput); err != nil {
		return nil, fmt.Errorf("hosting instance %s output does not match schema: %w", action, err)
	}
	targetBound := reflect.DeepEqual(asObject(actionInput["instance_ref"]), asObject(actionOutput["instance_ref"]))
	outcome, state := stringValue(actionOutput, "outcome"), stringValue(actionOutput, "state")
	postcondition := action == "start" && state == "active" || action == "stop" && state == "stopped"
	return map[string]any{
		"schemas_valid":               true,
		"target_bound":                targetBound,
		"completion_confirmed":        targetBound && outcome == "completed" && postcondition,
		"unknown_requires_inspection": outcome == "outcome_unknown",
		"result_requires_inspection":  !targetBound || outcome == "outcome_unknown",
	}, nil
}

// evaluateHostingCatalogList evaluates the conformance fixtures hosting.regions.list.filtered,
// hosting.plans.list.resource-filter, hosting.plans.list.minimum-filter-mismatch,
// hosting.os_offerings.list.unknown-not-available.
func evaluateHostingCatalogList(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	kind := stringValue(input, "catalog_kind")
	catalogInput := asObject(input["catalog_input"])
	catalogOutput := asObject(input["catalog_output"])
	inputSchemaID, outputSchemaID, itemsKey, itemIDKey := catalogShape(kind)
	if err := pkg.validateSchema(inputSchemaID, catalogInput); err != nil {
		return nil, fmt.Errorf("hosting %s list input does not match schema: %w", kind, err)
	}
	if err := pkg.validateSchema(outputSchemaID, catalogOutput); err != nil {
		return nil, fmt.Errorf("hosting %s list output does not match schema: %w", kind, err)
	}
	items := arrayValue(catalogOutput, itemsKey)
	paginationContinues := stringValue(catalogOutput, "next_page_token") != ""
	filterMatches, uniqueItems, eligible := true, true, len(items) > 0
	seen := make(map[string]bool)
	for _, raw := range items {
		item := asObject(raw)
		key := stringValue(item, itemIDKey)
		if kind == "plans" || kind == "os_offerings" {
			key = stringValue(item, "region_id") + "\x00" + key
		}
		if seen[key] {
			uniqueItems = false
		}
		seen[key] = true
		if !catalogItemMatchesFilter(kind, catalogInput, item) {
			filterMatches = false
		}
		if stringValue(item, "availability") != "available" {
			eligible = false
		}
	}
	pageSize, ok := catalogNumber(catalogInput, "page_size")
	withinPageSize := !ok || float64(len(items)) <= pageSize
	return map[string]any{
		"schemas_valid":              true,
		"request_filter_matches":     filterMatches,
		"items_unique":               uniqueItems,
		"within_page_size":           withinPageSize,
		"read_only":                  true,
		"all_items_report_available": eligible,
		"pagination_continues":       paginationContinues,
	}, nil
}

// catalogShape returns the schemas and item keys of a hosting catalog
// listing. An unknown kind returns empty values, so schema validation
// fails.
func catalogShape(kind string) (inputSchemaID, outputSchemaID, itemsKey, itemIDKey string) {
	switch kind {
	case "regions":
		return "urn:gameserver-controller:0.21.0-draft:hosting:regions-list-input",
			"urn:gameserver-controller:0.21.0-draft:hosting:regions-list-output", "regions", "region_id"
	case "plans":
		return "urn:gameserver-controller:0.21.0-draft:hosting:plans-list-input",
			"urn:gameserver-controller:0.21.0-draft:hosting:plans-list-output", "plans", "plan_id"
	case "os_offerings":
		return "urn:gameserver-controller:0.21.0-draft:hosting:os-offerings-list-input",
			"urn:gameserver-controller:0.21.0-draft:hosting:os-offerings-list-output", "os_offerings", "offering_id"
	}
	return "", "", "", ""
}

// catalogNumber reads a JSON number field; ok is false when it is absent
// or not a valid number.
func catalogNumber(object map[string]any, key string) (float64, bool) {
	raw, ok := object[key].(json.Number)
	if !ok {
		return 0, false
	}
	value, err := raw.Float64()
	return value, err == nil
}

// catalogItemMatchesFilter: the item satisfies the request's region filter
// and, for plans, the minimum CPU and memory, and for OS offerings, the
// requested family and architecture.
func catalogItemMatchesFilter(kind string, request, item map[string]any) bool {
	matches := true
	if kind == "regions" {
		regionIDs := arrayValue(request, "region_ids")
		if len(regionIDs) > 0 && !contains(regionIDs, stringValue(item, "region_id")) {
			matches = false
		}
	} else if regionID := stringValue(request, "region_id"); regionID != "" && stringValue(item, "region_id") != regionID {
		matches = false
	}
	switch kind {
	case "plans":
		for _, minimum := range [][2]string{{"minimum_vcpu_count", "vcpu_count"}, {"minimum_memory_mib", "memory_mib"}} {
			if want, ok := catalogNumber(request, minimum[0]); ok {
				if actual, ok := catalogNumber(item, minimum[1]); !ok || actual < want {
					matches = false
				}
			}
		}
	case "os_offerings":
		for _, field := range []string{"os_family", "architecture"} {
			if requested := stringValue(request, field); requested != "" && stringValue(item, field) != requested {
				matches = false
			}
		}
	}
	return matches
}

// evaluateHostingInstanceInspect evaluates the conformance fixtures
// hosting.instance.inspect.exact-active, hosting.instance.inspect.unknown-not-active,
// hosting.instance.inspect.target-mismatch.
func evaluateHostingInstanceInspect(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	hostingInput, hostingOutput := asObject(input["hosting_input"]), asObject(input["hosting_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-inspect-input", hostingInput); err != nil {
		return nil, fmt.Errorf("hosting inspect input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-inspect-output", hostingOutput); err != nil {
		return nil, fmt.Errorf("hosting inspect output does not match schema: %w", err)
	}
	requestedRef := asObject(hostingInput["instance_ref"])
	observedRef := asObject(hostingOutput["instance_ref"])
	targetBound := reflect.DeepEqual(requestedRef, observedRef)
	state := stringValue(hostingOutput, "state")
	return map[string]any{
		"schemas_valid":           true,
		"target_bound":            targetBound,
		"observed_state":          state,
		"unknown_not_active":      state == "unknown",
		"safe_to_treat_as_active": targetBound && state == "active",
		"inspection_read_only":    true,
	}, nil
}

// evaluateHostingProvisionValidStandardOs evaluates the hosting.provision.valid.standard-os conformance fixture.
func evaluateHostingProvisionValidStandardOs(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	if err := pkg.validateSchema(provisionSchemaID, input); err != nil {
		return nil, err
	}
	return map[string]any{"schema_valid": true, "side_effect_permitted": false}, nil
}

// evaluateHostingBootstrapExecutionBindingAndReadinessGate evaluates the hosting.bootstrap.execution-binding-and-readiness-gate conformance fixture.
func evaluateHostingBootstrapExecutionBindingAndReadinessGate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "cases")))
	for _, rawCase := range arrayValue(fixture, "cases") {
		item := asObject(rawCase)
		deliverInput := asObject(item["deliver_input"])
		output := asObject(item["execution_output"])
		inspectInput := asObject(item["inspect_input"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-deliver-input", deliverInput); err != nil {
			return nil, fmt.Errorf("case %q delivery input does not match schema: %w", stringValue(item, "case_id"), err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-execution-output", output); err != nil {
			return nil, fmt.Errorf("case %q execution output does not match schema: %w", stringValue(item, "case_id"), err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-inspect-input", inspectInput); err != nil {
			return nil, fmt.Errorf("case %q inspection input does not match schema: %w", stringValue(item, "case_id"), err)
		}
		bootstrap := asObject(deliverInput["bootstrap"])
		instanceBound := reflect.DeepEqual(deliverInput["instance_ref"], output["instance_ref"]) &&
			reflect.DeepEqual(deliverInput["instance_ref"], inspectInput["instance_ref"])
		revisionBound := bootstrap["revision"] == output["revision"] && bootstrap["digest"] == output["digest"]
		executionBound := reflect.DeepEqual(output["bootstrap_execution_ref"], inspectInput["bootstrap_execution_ref"])
		results = append(results, map[string]any{
			"case_id":                    stringValue(item, "case_id"),
			"instance_bound":             instanceBound,
			"revision_and_digest_bound":  revisionBound,
			"execution_reference_bound":  executionBound,
			"bootstrap_completed":        output["state"] == "succeeded" && output["exit_code"] == json.Number("0"),
			"ready_transition_permitted": false,
		})
	}
	return map[string]any{"cases": results, "bootstrap_completion_alone_never_permits_ready": true}, nil
}

// evaluateHostingBootstrapRejectsFalseSuccess evaluates the hosting.bootstrap.rejects-false-success conformance fixture.
func evaluateHostingBootstrapRejectsFalseSuccess(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	output := asObject(fixture["execution_output"])
	err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-execution-output", output)
	return map[string]any{"accepted": err == nil}, nil
}
