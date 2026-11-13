package main

import (
	"fmt"
	"reflect"
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
	registerEvaluator(evaluateHostingInstanceInspect,
		"hosting.instance.inspect.exact-active",
		"hosting.instance.inspect.unknown-not-active",
		"hosting.instance.inspect.target-mismatch")
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
	operationBound, operationScopeMatches := waitOperationReference(asObject(waitInput["instance_ref"]),
		asObject(waitInput["provider_operation_ref"]), asObject(waitOutput["provider_operation_ref"]))
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

// waitOperationReference: a provider-operation reference sent with the
// wait must come back unchanged and belong to the instance's adapter and
// environment. Without one, any reference the result carries must be an
// operation of that adapter and environment.
func waitOperationReference(target, sent, observed map[string]any) (bound, scopeMatches bool) {
	switch {
	case len(sent) > 0:
		return reflect.DeepEqual(sent, observed),
			stringValue(sent, "adapter_id") == stringValue(target, "adapter_id") &&
				stringValue(sent, "environment_id") == stringValue(target, "environment_id")
	case len(observed) > 0:
		return true, stringValue(observed, "adapter_id") == stringValue(target, "adapter_id") &&
			stringValue(observed, "environment_id") == stringValue(target, "environment_id") &&
			stringValue(observed, "resource_kind") == "operation"
	default:
		return true, true
	}
}

// emergencyRebootApprovalBound: a valid emergency approval, with an
// approval and approver ID, for hosting.instance.reboot.emergency of this
// exact instance.
func emergencyRebootApprovalBound(approval, target map[string]any) bool {
	return boolValue(approval, "valid") &&
		stringValue(approval, "approval_id") != "" &&
		stringValue(approval, "approver_id") != "" &&
		stringValue(approval, "action") == "hosting.instance.reboot.emergency" &&
		reflect.DeepEqual(target, asObject(approval["instance_ref"]))
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
	emergencyApprovalBound := emergencyRebootApprovalBound(asObject(core["emergency_approval"]), target)
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
