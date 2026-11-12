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
		results = append(results, evaluateUnitRestartCase(pkg, fixture, asObject(rawCase)))
	}
	return map[string]any{"evaluations": results}, nil
}

// instant is a parsed fixture timestamp; ok is false when it is missing or
// malformed.
type instant struct {
	at time.Time
	ok bool
}

func instantOf(object map[string]any, key string) instant {
	at, err := parseTime(stringValue(object, key))
	return instant{at: at, ok: err == nil}
}

// restartCase is one evaluation of the systemd unit restart vector with its
// timestamps parsed once.
type restartCase struct {
	request, output, core                  map[string]any
	unit, target, instance, save, saveStep map[string]any
	binding, before, resolved, after       map[string]any
	accepted, dispatched, evaluated        instant
	saved, saveObserved, unitObserved      instant
	stepCompleted, observed, afterObserved instant
	completed                              instant
}

func newRestartCase(c map[string]any) restartCase {
	request, output, core := asObject(c["request"]), asObject(c["output"]), asObject(c["core_preconditions"])
	target := asObject(request["target"])
	r := restartCase{
		request: request, output: output, core: core,
		unit: asObject(request["unit_ref"]), target: target, instance: asObject(target["instance_ref"]),
		save: asObject(request["save_confirmation"]), saveStep: asObject(core["completed_save_step"]),
		binding: asObject(core["unit_binding"]), before: asObject(core["unit_inspection"]),
		resolved: asObject(c["resolved_operation"]), after: asObject(c["unit_inspection"]),
	}
	r.accepted, r.dispatched, r.evaluated = instantOf(core, "operation_accepted_at"), instantOf(core, "dispatch_at"), instantOf(core, "evaluated_at")
	r.saved, r.saveObserved = instantOf(r.save, "saved_at"), instantOf(r.save, "observed_at")
	r.unitObserved, r.stepCompleted = instantOf(r.before, "observed_at"), instantOf(r.saveStep, "completed_at")
	r.observed, r.afterObserved, r.completed = instantOf(output, "observed_at"), instantOf(r.after, "observed_at"), instantOf(r.resolved, "completed_at")
	return r
}

// timesValid: acceptance, dispatch, and evaluation are present and ordered.
func (r restartCase) timesValid() bool {
	return r.accepted.ok && r.dispatched.ok && r.evaluated.ok && !r.dispatched.at.Before(r.accepted.at) && !r.evaluated.at.Before(r.dispatched.at)
}

// saveStepBound: the completed save step is the selected, advertised
// game.save.request capability bound to this operation, target, and release.
func restartSaveStepBound(pkg *protocolPackage, fixture, c map[string]any, r restartCase) bool {
	selectedAdapterID := stringValue(r.core, "selected_game_adapter_id")
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
	stepInput := asObject(r.saveStep["input"])
	stepInputSchema := pkg.validateSchema(stringValue(selectedCapability, "input_schema_uri"), stepInput) == nil
	stepOutputSchema := pkg.validateSchema(stringValue(selectedCapability, "output_schema_uri"), r.saveStep["output"]) == nil
	stepInputBound := stepInputSchema && reflect.DeepEqual(r.target, stepInput["target"]) &&
		r.request["release_id"] == stepInput["release_id"] && stringValue(stepInput, "reason") == "operator_request"
	return capabilityIdentityBound && stepInputBound && stepOutputSchema &&
		stringValue(r.core, "operation_id") != "" && r.saveStep["operation_id"] == r.core["operation_id"] &&
		r.saveStep["step_id"] == r.request["save_step_id"] && stringValue(r.saveStep, "status") == "succeeded" &&
		r.saveStep["adapter_id"] == selectedAdapterID && r.saveStep["capability_name"] == "game.save.request" &&
		r.saveStep["capability_version"] == selectedCapability["version"] && reflect.DeepEqual(r.save, r.saveStep["output"])
}

// unitCurrent: the pre-dispatch inspection shows this unit running the
// expected runtime, observed after acceptance and before dispatch.
func (r restartCase) unitCurrent(pkg *protocolPackage) bool {
	inspected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output", r.before) == nil &&
		reflect.DeepEqual(r.unit, r.before["unit_ref"]) && reflect.DeepEqual(r.target, r.before["target"]) && r.before["release_ref"] == r.request["release_id"] &&
		r.before["runtime_id"] == r.request["expected_runtime_id"] && stringValue(r.before, "state") == "running"
	return inspected && r.timesValid() && r.unitObserved.ok && !r.unitObserved.at.Before(r.accepted.at) && !r.dispatched.at.Before(r.unitObserved.at)
}

// saveConfirmationBound: the save confirmation is the save step's output,
// confirmed, for this target and release, and ordered between the unit
// inspection and dispatch.
func (r restartCase) saveConfirmationBound(saveStepBound, unitInspectionPrecedesSave bool) bool {
	return saveStepBound && r.timesValid() && r.saved.ok && r.saveObserved.ok && !r.saved.at.Before(r.accepted.at) && !r.saveObserved.at.Before(r.saved.at) &&
		!r.dispatched.at.Before(r.saveObserved.at) && unitInspectionPrecedesSave && r.stepCompleted.ok && !r.stepCompleted.at.Before(r.saveObserved.at) &&
		!r.dispatched.at.Before(r.stepCompleted.at) && stringValue(r.save, "outcome") == "confirmed" && reflect.DeepEqual(r.target, r.save["target"]) &&
		r.request["release_id"] == r.save["release_id"]
}

// operationBound: an external operation reference, if any, names this
// adapter's unit operation and matches the resolved operation.
func (r restartCase) operationBound() bool {
	operationRef, hasOperationRef := r.output["external_operation_ref"]
	return !hasOperationRef || (asObject(operationRef)["adapter_id"] == r.unit["adapter_id"] &&
		asObject(operationRef)["environment_id"] == r.unit["environment_id"] && stringValue(asObject(operationRef), "resource_kind") == "unit-operation" &&
		reflect.DeepEqual(operationRef, r.resolved["external_operation_ref"]) && reflect.DeepEqual(r.unit, r.resolved["unit_ref"]) &&
		reflect.DeepEqual(r.target, r.resolved["target"]) && r.request["release_id"] == r.resolved["release_id"] && r.request["expected_runtime_id"] == r.resolved["previous_runtime_id"])
}

// resultBound: the restart result names this unit, target, release, and
// previous runtime, and was observed between dispatch and evaluation.
func (r restartCase) resultBound(operationBound bool) bool {
	return reflect.DeepEqual(r.unit, r.output["unit_ref"]) && reflect.DeepEqual(r.target, r.output["target"]) && r.request["release_id"] == r.output["release_id"] &&
		r.request["expected_runtime_id"] == r.output["previous_runtime_id"] && operationBound && r.timesValid() && r.observed.ok &&
		!r.observed.at.Before(r.dispatched.at) && !r.evaluated.at.Before(r.observed.at)
}

// afterBound: the post-dispatch inspection is valid, names this unit, and was
// observed between dispatch and evaluation.
func (r restartCase) afterBound(pkg *protocolPackage) bool {
	afterValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output", r.after) == nil
	return afterValid && reflect.DeepEqual(r.unit, r.after["unit_ref"]) && reflect.DeepEqual(r.target, r.after["target"]) && r.after["release_ref"] == r.request["release_id"] &&
		r.afterObserved.ok && !r.afterObserved.at.Before(r.dispatched.at) && !r.evaluated.at.Before(r.afterObserved.at)
}

// reconciled: an uncertain restart is proven complete by the resolved
// operation and a running inspection with a new runtime.
func (r restartCase) reconciled(afterBound bool) bool {
	return stringValue(r.resolved, "state") == "completed" &&
		r.completed.ok && !r.completed.at.Before(r.dispatched.at) && !r.afterObserved.at.Before(r.completed.at) &&
		boolValue(r.resolved, "clean_stop_confirmed") && afterBound && stringValue(r.after, "state") == "running" &&
		stringValue(r.after, "runtime_id") != "" && r.after["runtime_id"] != r.request["expected_runtime_id"] && r.after["runtime_id"] == r.resolved["runtime_id"]
}

func evaluateUnitRestartCase(pkg *protocolPackage, fixture, c map[string]any) map[string]any {
	r := newRestartCase(c)
	requestValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-input", r.request) == nil
	outputValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-output", r.output) == nil
	saveStepBound := restartSaveStepBound(pkg, fixture, c, r)
	adapterBound := stringValue(r.core, "execution_adapter_id") != "" && r.unit["adapter_id"] == r.core["execution_adapter_id"]
	targetConsistent := r.unit["environment_id"] == r.target["environment_id"] && r.target["environment_id"] == r.instance["environment_id"] &&
		stringValue(r.unit, "resource_kind") == "systemd-unit"
	unitBound := reflect.DeepEqual(r.unit, r.binding["unit_ref"]) && reflect.DeepEqual(r.target, r.binding["target"]) && r.request["release_id"] == r.binding["release_id"]
	unitCurrent := r.unitCurrent(pkg)
	unitInspectionPrecedesSave := unitCurrent && r.saved.ok && !r.saved.at.Before(r.unitObserved.at)
	saveBound := r.saveConfirmationBound(saveStepBound, unitInspectionPrecedesSave)
	dispatchAllowed := requestValid && adapterBound && targetConsistent && unitBound && unitCurrent && saveBound

	operationBound := r.operationBound()
	bound := r.resultBound(operationBound)
	newRuntime := stringValue(r.output, "runtime_id") != "" && r.output["runtime_id"] != r.request["expected_runtime_id"]
	outcome := stringValue(r.output, "outcome")
	cycleConsistent := (!newRuntime || boolValue(r.output, "clean_stop_confirmed")) && (outcome != "completed" || newRuntime)
	resultValid := outputValid && bound && cycleConsistent
	confirmed := dispatchAllowed && resultValid && outcome == "completed"
	uncertain := outcome == "accepted" || outcome == "outcome_unknown"
	afterBound := r.afterBound(pkg)
	reconciled := dispatchAllowed && resultValid && uncertain && r.reconciled(afterBound)
	operatorRequired := dispatchAllowed && uncertain && (!operationBound || (len(r.after) > 0 &&
		(!afterBound || stringValue(r.after, "state") == "unknown" || (stringValue(r.resolved, "state") == "completed" && !reconciled))))
	return map[string]any{
		"case_id": stringValue(c, "case_id"), "request_schema_valid": requestValid, "result_schema_valid": outputValid,
		"adapter_bound": adapterBound, "target_consistent": targetConsistent, "unit_bound": unitBound, "unit_current": unitCurrent,
		"save_step_bound": saveStepBound, "unit_inspection_precedes_save": unitInspectionPrecedesSave, "save_confirmation_bound": saveBound,
		"dispatch_allowed": dispatchAllowed, "result_bound": bound,
		"restart_confirmed": confirmed || reconciled, "reconciliation_confirmed": reconciled,
		"must_inspect_before_retry": dispatchAllowed && (!resultValid || (uncertain && !reconciled)),
		"retry_permitted":           false, "operator_required": operatorRequired, "readiness_established": false,
	}
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

func init() {
	registerEvaluator(evaluateUnitRestart, "execution.unit.restart.clean-stop-distinct-runtime")
}
