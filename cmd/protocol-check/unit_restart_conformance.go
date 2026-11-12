package main

import (
	"fmt"
	"path/filepath"
	"reflect"
	"time"
)

func evaluateUnitRestart(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "evaluations")))
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		c := asObject(rawCase)
		request, output, core := asObject(c["request"]), asObject(c["output"]), asObject(c["core_preconditions"])
		requestValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-input", request) == nil
		outputValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-output", output) == nil
		unit, target := asObject(request["unit_ref"]), asObject(request["target"])
		instance, save := asObject(target["instance_ref"]), asObject(request["save_confirmation"])
		saveStepBound, saveStep := restartSaveStepEvidence(pkg, fixture, c, request, core, target, save)
		adapterBound, targetConsistent, unitBound, unitCurrent, before := restartUnitBindings(pkg, request, core, unit, target, instance)
		unitCurrent, unitInspectionPrecedesSave, saveBound, timesValid, dispatched, evaluated := restartSaveTimeline(core, save, saveStep, before, request, target, unitCurrent, saveStepBound)
		dispatchAllowed := requestValid && adapterBound && targetConsistent && unitBound && unitCurrent && saveBound
		resolved := asObject(c["resolved_operation"])
		operationBound := restartOperationReferenceBound(output, resolved, unit, target, request)
		bound := restartOutputBound(output, unit, target, request, operationBound, timesValid, dispatched, evaluated)
		newRuntime := stringValue(output, "runtime_id") != "" && output["runtime_id"] != request["expected_runtime_id"]
		outcome := stringValue(output, "outcome")
		cycleConsistent := (!newRuntime || boolValue(output, "clean_stop_confirmed")) && (outcome != "completed" || newRuntime)
		resultValid := outputValid && bound && cycleConsistent
		confirmed := dispatchAllowed && resultValid && outcome == "completed"
		uncertain := outcome == "accepted" || outcome == "outcome_unknown"
		reconciled, operatorRequired := restartReconciliation(pkg, c, request, unit, target, resolved, dispatchAllowed, resultValid, uncertain, operationBound, dispatched, evaluated)
		results = append(results, map[string]any{
			"case_id": stringValue(c, "case_id"), "request_schema_valid": requestValid, "result_schema_valid": outputValid,
			"adapter_bound": adapterBound, "target_consistent": targetConsistent, "unit_bound": unitBound, "unit_current": unitCurrent,
			"save_step_bound": saveStepBound, "unit_inspection_precedes_save": unitInspectionPrecedesSave, "save_confirmation_bound": saveBound,
			"dispatch_allowed": dispatchAllowed, "result_bound": bound,
			"restart_confirmed": confirmed || reconciled, "reconciliation_confirmed": reconciled,
			"must_inspect_before_retry": dispatchAllowed && (!resultValid || (uncertain && !reconciled)),
			"retry_permitted":           false, "operator_required": operatorRequired, "readiness_established": false,
		})
	}
	return map[string]any{"evaluations": results}, nil
}

func restartSaveStepEvidence(pkg *protocolPackage, fixture, c, request, core, target, save map[string]any) (bool, map[string]any) {
	saveStep := asObject(core["completed_save_step"])
	selectedAdapterID := stringValue(core, "selected_game_adapter_id")
	selectedCapability := asObject(c["selected_game_capability"])
	if selectedCapability == nil {
		selectedCapability = asObject(fixture["selected_game_capability"])
	}
	advertisedCapability, capabilityErr := findAdapterCapability(pkg, selectedAdapterID, "game.save.request")
	capabilityIdentityBound := capabilityErr == nil &&
		selectedCapability["adapter_id"] == selectedAdapterID &&
		selectedCapability["name"] == "game.save.request" &&
		selectedCapability["name"] == advertisedCapability["name"] &&
		selectedCapability["version"] == advertisedCapability["version"] &&
		selectedCapability["input_schema_uri"] == advertisedCapability["input_schema_uri"] &&
		selectedCapability["output_schema_uri"] == advertisedCapability["output_schema_uri"]
	stepInput := asObject(saveStep["input"])
	stepInputSchema := pkg.validateSchema(stringValue(selectedCapability, "input_schema_uri"), stepInput) == nil
	stepOutputSchema := pkg.validateSchema(stringValue(selectedCapability, "output_schema_uri"), saveStep["output"]) == nil
	stepInputBound := stepInputSchema && reflect.DeepEqual(target, stepInput["target"]) &&
		request["release_id"] == stepInput["release_id"] && stringValue(stepInput, "reason") == "operator_request"
	saveStepBound := capabilityIdentityBound && stepInputBound && stepOutputSchema &&
		stringValue(core, "operation_id") != "" && saveStep["operation_id"] == core["operation_id"] &&
		saveStep["step_id"] == request["save_step_id"] && stringValue(saveStep, "status") == "succeeded" &&
		saveStep["adapter_id"] == selectedAdapterID && saveStep["capability_name"] == "game.save.request" &&
		saveStep["capability_version"] == selectedCapability["version"] && reflect.DeepEqual(save, saveStep["output"])
	return saveStepBound, saveStep
}

func restartUnitBindings(pkg *protocolPackage, request, core, unit, target, instance map[string]any) (bool, bool, bool, bool, map[string]any) {
	adapterBound := stringValue(core, "execution_adapter_id") != "" && unit["adapter_id"] == core["execution_adapter_id"]
	targetConsistent := unit["environment_id"] == target["environment_id"] && target["environment_id"] == instance["environment_id"] &&
		stringValue(unit, "resource_kind") == "systemd-unit"
	binding, before := asObject(core["unit_binding"]), asObject(core["unit_inspection"])
	unitBound := reflect.DeepEqual(unit, binding["unit_ref"]) && reflect.DeepEqual(target, binding["target"]) && request["release_id"] == binding["release_id"]
	unitCurrent := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output", before) == nil &&
		reflect.DeepEqual(unit, before["unit_ref"]) && reflect.DeepEqual(target, before["target"]) && before["release_ref"] == request["release_id"] &&
		before["runtime_id"] == request["expected_runtime_id"] && stringValue(before, "state") == "running"
	return adapterBound, targetConsistent, unitBound, unitCurrent, before
}

func restartSaveTimeline(core, save, saveStep, before, request, target map[string]any, current, saveStepBound bool) (bool, bool, bool, bool, time.Time, time.Time) {
	unitCurrent := current
	accepted, acceptedErr := parseTime(stringValue(core, "operation_accepted_at"))
	dispatched, dispatchErr := parseTime(stringValue(core, "dispatch_at"))
	evaluated, evaluateErr := parseTime(stringValue(core, "evaluated_at"))
	saved, savedErr := parseTime(stringValue(save, "saved_at"))
	saveObserved, saveObservedErr := parseTime(stringValue(save, "observed_at"))
	unitObserved, unitObservedErr := parseTime(stringValue(before, "observed_at"))
	stepCompleted, stepCompletedErr := parseTime(stringValue(saveStep, "completed_at"))
	timesValid := acceptedErr == nil && dispatchErr == nil && evaluateErr == nil && !dispatched.Before(accepted) && !evaluated.Before(dispatched)
	unitCurrent = unitCurrent && timesValid && unitObservedErr == nil && !unitObserved.Before(accepted) && !dispatched.Before(unitObserved)
	unitInspectionPrecedesSave := unitCurrent && savedErr == nil && !saved.Before(unitObserved)
	saveBound := saveStepBound && timesValid && savedErr == nil && saveObservedErr == nil && !saved.Before(accepted) && !saveObserved.Before(saved) &&
		!dispatched.Before(saveObserved) && unitInspectionPrecedesSave && stepCompletedErr == nil && !stepCompleted.Before(saveObserved) &&
		!dispatched.Before(stepCompleted) && stringValue(save, "outcome") == "confirmed" && reflect.DeepEqual(target, save["target"]) &&
		request["release_id"] == save["release_id"]
	return unitCurrent, unitInspectionPrecedesSave, saveBound, timesValid, dispatched, evaluated
}

func restartOperationReferenceBound(output, resolved, unit, target, request map[string]any) bool {
	operationRef, hasOperationRef := output["external_operation_ref"]
	operationBound := !hasOperationRef || (asObject(operationRef)["adapter_id"] == unit["adapter_id"] &&
		asObject(operationRef)["environment_id"] == unit["environment_id"] && stringValue(asObject(operationRef), "resource_kind") == "unit-operation" &&
		reflect.DeepEqual(operationRef, resolved["external_operation_ref"]) && reflect.DeepEqual(unit, resolved["unit_ref"]) &&
		reflect.DeepEqual(target, resolved["target"]) && request["release_id"] == resolved["release_id"] && request["expected_runtime_id"] == resolved["previous_runtime_id"])
	return operationBound
}

func restartOutputBound(output, unit, target, request map[string]any, operationBound, timesValid bool, dispatched, evaluated time.Time) bool {
	observed, observedErr := parseTime(stringValue(output, "observed_at"))
	bound := reflect.DeepEqual(unit, output["unit_ref"]) && reflect.DeepEqual(target, output["target"]) && request["release_id"] == output["release_id"] &&
		request["expected_runtime_id"] == output["previous_runtime_id"] && operationBound && timesValid && observedErr == nil &&
		!observed.Before(dispatched) && !evaluated.Before(observed)
	return bound
}

func restartReconciliation(pkg *protocolPackage, c, request, unit, target, resolved map[string]any, dispatchAllowed, resultValid, uncertain, operationBound bool, dispatched, evaluated time.Time) (bool, bool) {
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
	return reconciled, operatorRequired
}

func findAdapterCapability(pkg *protocolPackage, adapterID, capabilityName string) (map[string]any, error) {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "examples", "adapter-descriptor.*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		value, err := parseJSONFile(path)
		if err != nil {
			return nil, err
		}
		descriptor := asObject(value)
		if stringValue(descriptor, "adapter_id") != adapterID {
			continue
		}
		if stringValue(descriptor, "category") != "game" {
			return nil, fmt.Errorf("selected adapter %q is not a game adapter", adapterID)
		}
		for _, rawCapability := range arrayValue(descriptor, "capabilities") {
			capability := asObject(rawCapability)
			if stringValue(capability, "name") == capabilityName {
				return capability, nil
			}
		}
		return nil, fmt.Errorf("adapter %q does not advertise %q", adapterID, capabilityName)
	}
	return nil, fmt.Errorf("selected adapter descriptor %q is missing", adapterID)
}
