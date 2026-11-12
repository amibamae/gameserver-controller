package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

// evaluateHostingInstanceWaitFixtures evaluates the 7 hosting.instance.wait.* fixtures.
func evaluateHostingInstanceWaitFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateHostingInstanceDestroyFixtures evaluates the 7 hosting.instance.destroy.* fixtures.
func evaluateHostingInstanceDestroyFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	destroyInput := asObject(input["destroy_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-input", destroyInput); err != nil {
		return nil, fmt.Errorf("hosting destroy input does not match schema: %w", err)
	}
	preconditions := asObject(fixture["core_preconditions"])
	request := asObject(fixture["request_binding"])
	approval := asObject(preconditions["approval"])
	target := asObject(destroyInput["instance_ref"])
	requestBindingMatches := stringValue(request, "action") == "hosting.instance.destroy" &&
		stringValue(request, "logical_server_id") != "" &&
		stringValue(request, "environment_id") == stringValue(target, "environment_id") &&
		stringValue(request, "adapter_id") == stringValue(target, "adapter_id") &&
		stringValue(request, "plan_fingerprint") != "" &&
		reflect.DeepEqual(asObject(request["instance_ref"]), target)
	approvalScopeMatches := stringValue(approval, "action") == stringValue(request, "action") &&
		stringValue(approval, "logical_server_id") == stringValue(request, "logical_server_id") &&
		stringValue(approval, "environment_id") == stringValue(request, "environment_id") &&
		stringValue(approval, "adapter_id") == stringValue(request, "adapter_id") &&
		reflect.DeepEqual(asObject(approval["instance_ref"]), target) &&
		reflect.DeepEqual(asObject(approval["instance_ref"]), asObject(request["instance_ref"])) &&
		stringValue(approval, "plan_fingerprint") != "" &&
		stringValue(approval, "plan_fingerprint") == stringValue(request, "plan_fingerprint")
	evaluatedAt, evaluationErr := time.Parse(time.RFC3339, stringValue(fixture, "evaluation_time"))
	expiresAt, expiryErr := time.Parse(time.RFC3339, stringValue(approval, "expires_at"))
	approvalCurrent := boolValue(approval, "valid") && evaluationErr == nil && expiryErr == nil && expiresAt.After(evaluatedAt)
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

// evaluateHostingInstanceDestroyAuthorizationRequired evaluates the 8 hosting.instance.destroy.* fixtures.
func evaluateHostingInstanceDestroyAuthorizationRequired(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateHostingInstanceRebootFixtures evaluates the 9 hosting.instance.reboot.* fixtures.
func evaluateHostingInstanceRebootFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateHostingInstanceRebootCompletedActive evaluates the 4 hosting.instance.reboot.* fixtures.
func evaluateHostingInstanceRebootCompletedActive(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateHostingInstanceFixtures evaluates the 8 hosting.instance.* fixtures.
func evaluateHostingInstanceFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateHostingRegionsListFiltered evaluates the 4 hosting.* fixtures.
func evaluateHostingRegionsListFiltered(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	kind := stringValue(input, "catalog_kind")
	catalogInput := asObject(input["catalog_input"])
	catalogOutput := asObject(input["catalog_output"])
	inputSchemaID, outputSchemaID, itemsKey, itemIDKey := "", "", "", ""
	switch kind {
	case "regions":
		inputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:regions-list-input"
		outputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:regions-list-output"
		itemsKey, itemIDKey = "regions", "region_id"
	case "plans":
		inputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:plans-list-input"
		outputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:plans-list-output"
		itemsKey, itemIDKey = "plans", "plan_id"
	case "os_offerings":
		inputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:os-offerings-list-input"
		outputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:os-offerings-list-output"
		itemsKey, itemIDKey = "os_offerings", "offering_id"
	}
	if err := pkg.validateSchema(inputSchemaID, catalogInput); err != nil {
		return nil, fmt.Errorf("hosting %s list input does not match schema: %w", kind, err)
	}
	if err := pkg.validateSchema(outputSchemaID, catalogOutput); err != nil {
		return nil, fmt.Errorf("hosting %s list output does not match schema: %w", kind, err)
	}
	numberValue := func(object map[string]any, key string) (float64, bool) {
		raw, ok := object[key].(json.Number)
		if !ok {
			return 0, false
		}
		value, err := raw.Float64()
		return value, err == nil
	}
	items := arrayValue(catalogOutput, itemsKey)
	paginationContinues := stringValue(catalogOutput, "next_page_token") != ""
	filterMatches, uniqueItems, eligible, withinPageSize := true, true, len(items) > 0, true
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
		if kind == "regions" {
			regionIDs := arrayValue(catalogInput, "region_ids")
			if len(regionIDs) > 0 && !contains(regionIDs, stringValue(item, "region_id")) {
				filterMatches = false
			}
		} else {
			regionID := stringValue(catalogInput, "region_id")
			if regionID != "" && stringValue(item, "region_id") != regionID {
				filterMatches = false
			}
		}
		if kind == "plans" {
			if minimum, ok := numberValue(catalogInput, "minimum_vcpu_count"); ok {
				if actual, ok := numberValue(item, "vcpu_count"); !ok || actual < minimum {
					filterMatches = false
				}
			}
			if minimum, ok := numberValue(catalogInput, "minimum_memory_mib"); ok {
				if actual, ok := numberValue(item, "memory_mib"); !ok || actual < minimum {
					filterMatches = false
				}
			}
		}
		if kind == "os_offerings" {
			for _, field := range []string{"os_family", "architecture"} {
				if requested := stringValue(catalogInput, field); requested != "" && stringValue(item, field) != requested {
					filterMatches = false
				}
			}
		}
		if stringValue(item, "availability") != "available" {
			eligible = false
		}
	}
	if pageSize, ok := numberValue(catalogInput, "page_size"); ok && float64(len(items)) > pageSize {
		withinPageSize = false
	}
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

// evaluateHostingInstanceInspectFixtures evaluates hosting.instance.inspect.exact-active, hosting.instance.inspect.unknown-not-active and hosting.instance.inspect.target-mismatch.
func evaluateHostingInstanceInspectFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateHostingProvisionValidStandardOs evaluates hosting.provision.valid.standard-os.
func evaluateHostingProvisionValidStandardOs(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	if err := pkg.validateSchema(provisionSchemaID, input); err != nil {
		return nil, err
	}
	return map[string]any{"schema_valid": true, "side_effect_permitted": false}, nil
}

// evaluateHostingBootstrapExecutionBindingAndReadinessGate evaluates hosting.bootstrap.execution-binding-and-readiness-gate.
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

// evaluateHostingBootstrapRejectsFalseSuccess evaluates hosting.bootstrap.rejects-false-success.
func evaluateHostingBootstrapRejectsFalseSuccess(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	output := asObject(fixture["execution_output"])
	err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-execution-output", output)
	return map[string]any{"accepted": err == nil}, nil
}
