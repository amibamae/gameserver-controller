package main

import "reflect"

func evaluateUnitRestart(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "evaluations")))
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		c := asObject(rawCase)
		request, output, core := asObject(c["request"]), asObject(c["output"]), asObject(c["core_preconditions"])
		requestValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-input", request) == nil
		outputValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-output", output) == nil
		unit, target := asObject(request["unit_ref"]), asObject(request["target"])
		instance, save := asObject(target["instance_ref"]), asObject(request["save_confirmation"])
		saveStep := asObject(core["completed_save_step"])
		saveStepBound := stringValue(core, "operation_id") != "" && saveStep["operation_id"] == core["operation_id"] &&
			saveStep["step_id"] == request["save_step_id"] && stringValue(saveStep, "status") == "completed" && reflect.DeepEqual(save, saveStep["output"])
		adapterBound := stringValue(core, "execution_adapter_id") != "" && unit["adapter_id"] == core["execution_adapter_id"]
		targetConsistent := unit["environment_id"] == target["environment_id"] && target["environment_id"] == instance["environment_id"] &&
			stringValue(unit, "resource_kind") == "systemd-unit"
		binding, before := asObject(core["unit_binding"]), asObject(core["unit_inspection"])
		unitBound := reflect.DeepEqual(unit, binding["unit_ref"]) && reflect.DeepEqual(target, binding["target"]) && request["release_id"] == binding["release_id"]
		unitCurrent := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output", before) == nil &&
			reflect.DeepEqual(unit, before["unit_ref"]) && reflect.DeepEqual(target, before["target"]) && before["release_ref"] == request["release_id"] &&
			before["runtime_id"] == request["expected_runtime_id"] && stringValue(before, "state") == "running"
		accepted, acceptedErr := parseTime(stringValue(core, "operation_accepted_at"))
		dispatched, dispatchErr := parseTime(stringValue(core, "dispatch_at"))
		evaluated, evaluateErr := parseTime(stringValue(core, "evaluated_at"))
		saved, savedErr := parseTime(stringValue(save, "saved_at"))
		saveObserved, saveObservedErr := parseTime(stringValue(save, "observed_at"))
		unitObserved, unitObservedErr := parseTime(stringValue(before, "observed_at"))
		timesValid := acceptedErr == nil && dispatchErr == nil && evaluateErr == nil && !dispatched.Before(accepted) && !evaluated.Before(dispatched)
		saveBound := saveStepBound && timesValid && savedErr == nil && saveObservedErr == nil && !saved.Before(accepted) && !saveObserved.Before(saved) &&
			!dispatched.Before(saveObserved) && stringValue(save, "outcome") == "confirmed" && reflect.DeepEqual(target, save["target"]) && request["release_id"] == save["release_id"]
		unitCurrent = unitCurrent && timesValid && unitObservedErr == nil && !unitObserved.Before(accepted) && !dispatched.Before(unitObserved)
		dispatchAllowed := requestValid && adapterBound && targetConsistent && unitBound && unitCurrent && saveBound
		operationRef, hasOperationRef := output["external_operation_ref"]
		resolved := asObject(c["resolved_operation"])
		operationBound := !hasOperationRef || (asObject(operationRef)["adapter_id"] == unit["adapter_id"] &&
			asObject(operationRef)["environment_id"] == unit["environment_id"] && stringValue(asObject(operationRef), "resource_kind") == "unit-operation" &&
			reflect.DeepEqual(operationRef, resolved["external_operation_ref"]) && reflect.DeepEqual(unit, resolved["unit_ref"]) &&
			reflect.DeepEqual(target, resolved["target"]) && request["release_id"] == resolved["release_id"] && request["expected_runtime_id"] == resolved["previous_runtime_id"])
		observed, observedErr := parseTime(stringValue(output, "observed_at"))
		bound := reflect.DeepEqual(unit, output["unit_ref"]) && reflect.DeepEqual(target, output["target"]) && request["release_id"] == output["release_id"] &&
			request["expected_runtime_id"] == output["previous_runtime_id"] && operationBound && timesValid && observedErr == nil &&
			!observed.Before(dispatched) && !evaluated.Before(observed)
		newRuntime := stringValue(output, "runtime_id") != "" && output["runtime_id"] != request["expected_runtime_id"]
		outcome := stringValue(output, "outcome")
		cycleConsistent := (!newRuntime || boolValue(output, "clean_stop_confirmed")) && (outcome != "completed" || newRuntime)
		resultValid := outputValid && bound && cycleConsistent
		confirmed := dispatchAllowed && resultValid && outcome == "completed"
		uncertain := outcome == "accepted" || outcome == "outcome_unknown"
		after := asObject(c["unit_inspection"])
		afterValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output", after) == nil
		afterObserved, afterObservedErr := parseTime(stringValue(after, "observed_at"))
		completed, completedErr := parseTime(stringValue(resolved, "completed_at"))
		afterBound := afterValid && reflect.DeepEqual(unit, after["unit_ref"]) && reflect.DeepEqual(target, after["target"]) && after["release_ref"] == request["release_id"] &&
			afterObservedErr == nil && !afterObserved.Before(dispatched) && !evaluated.Before(afterObserved)
		reconciled := dispatchAllowed && resultValid && uncertain && stringValue(resolved, "state") == "completed" &&
			completedErr == nil && !completed.Before(dispatched) && !afterObserved.Before(completed) &&
			boolValue(resolved, "clean_stop_confirmed") && afterBound && stringValue(after, "state") == "running" &&
			stringValue(after, "runtime_id") != "" && after["runtime_id"] != request["expected_runtime_id"] && after["runtime_id"] == resolved["runtime_id"]
		operatorRequired := dispatchAllowed && uncertain && (!operationBound || (len(after) > 0 &&
			(!afterBound || stringValue(after, "state") == "unknown" || (stringValue(resolved, "state") == "completed" && !reconciled))))
		results = append(results, map[string]any{
			"case_id": stringValue(c, "case_id"), "request_schema_valid": requestValid, "result_schema_valid": outputValid,
			"adapter_bound": adapterBound, "target_consistent": targetConsistent, "unit_bound": unitBound, "unit_current": unitCurrent,
			"save_confirmation_bound": saveBound, "dispatch_allowed": dispatchAllowed, "result_bound": bound,
			"restart_confirmed": confirmed || reconciled, "reconciliation_confirmed": reconciled,
			"must_inspect_before_retry": dispatchAllowed && (!resultValid || (uncertain && !reconciled)),
			"retry_permitted":           false, "operator_required": operatorRequired, "readiness_established": false,
		})
	}
	return map[string]any{"evaluations": results}, nil
}
