package main

import (
	"testing"
	"time"
)

// pcR6RestartCase loads the named restart case, lets mutate change its raw
// objects, and builds the parsed case from the result.
func pcR6RestartCase(t *testing.T, caseID string, mutate func(t *testing.T, c map[string]any)) restartCase {
	t.Helper()
	_, _, c := restartFixtureCase(t, caseID)
	mutate(t, c)
	return newRestartCase(c)
}

// pcR6CheckBool fails the test when got differs from want.
func pcR6CheckBool(t *testing.T, name string, got, want bool) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %t, want %t", name, got, want)
	}
}

// pcR6Object walks keys from c through nested objects.
func pcR6Object(c map[string]any, keys ...string) map[string]any {
	object := c
	for _, key := range keys {
		object = asObject(object[key])
	}
	return object
}

// pcR6SetField returns a mutation that sets key in the object at path.
func pcR6SetField(key string, value any, path ...string) func(*testing.T, map[string]any) {
	return func(_ *testing.T, c map[string]any) {
		pcR6Object(c, path...)[key] = value
	}
}

// pcR6ShiftField returns a mutation that sets key, in the object at path, to the
// timestamp found at fromKey in the object at fromPath, shifted by offset.
func pcR6ShiftField(key string, path []string, fromKey string, fromPath []string, offset time.Duration) func(*testing.T, map[string]any) {
	return func(t *testing.T, c map[string]any) {
		from := stringValue(pcR6Object(c, fromPath...), fromKey)
		pcR6Object(c, path...)[key] = shiftedTimestamp(t, from, offset)
	}
}

func TestRestartInstantOrdering(t *testing.T) {
	base := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	at := func(seconds int) instant {
		return instant{at: base.Add(time.Duration(seconds) * time.Second), ok: true}
	}
	missing := instant{at: base.Add(5 * time.Second)}
	tests := []struct {
		name              string
		value             instant
		notBefore, within bool
	}{
		{"missing", missing, false, false},
		{"before lower", at(0), false, false},
		{"at lower", at(1), true, true},
		{"inside", at(2), true, true},
		{"at upper", at(3), true, true},
		{"after upper", at(4), true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcR6CheckBool(t, "notBefore", test.value.notBefore(at(1)), test.notBefore)
			pcR6CheckBool(t, "between", test.value.between(at(1), at(3)), test.within)
		})
	}
}

func TestRestartFieldsEqual(t *testing.T) {
	left := map[string]any{"name": "game.save.request", "version": "1"}
	tests := []struct {
		name   string
		right  map[string]any
		fields []string
		want   bool
	}{
		{"no fields", nil, nil, true},
		{"all equal", map[string]any{"name": "game.save.request", "version": "1"}, []string{"name", "version"}, true},
		{"one differs", map[string]any{"name": "game.save.request", "version": "2"}, []string{"name", "version"}, false},
		{"missing on one side", map[string]any{"name": "game.save.request"}, []string{"name", "version"}, false},
		{"missing on both sides", map[string]any{}, []string{"input_schema_uri"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcR6CheckBool(t, "equal", restartFieldsEqual(left, test.right, test.fields...), test.want)
		})
	}
}

func TestSelectedSaveCapabilityBound(t *testing.T) {
	tests := []struct {
		name    string
		adapter string
		mutate  func(selected map[string]any)
		want    bool
	}{
		{"advertised capability", "game.minecraft-paper", func(map[string]any) {}, true},
		{"another adapter selected", "game.minecraft-paper", func(s map[string]any) { s["adapter_id"] = "game.other" }, false},
		{"another capability", "game.minecraft-paper", func(s map[string]any) { s["name"] = "game.data.describe" }, false},
		{"another version", "game.minecraft-paper", func(s map[string]any) { s["version"] = "2" }, false},
		{"another output schema", "game.minecraft-paper", func(s map[string]any) { s["output_schema_uri"] = "urn:other" }, false},
		{"no descriptor for the adapter", "game.missing", func(s map[string]any) { s["adapter_id"] = "game.missing" }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pkg, fixture, _ := restartFixtureCase(t, "completed-distinct-runtime")
			selected := asObject(fixture["selected_game_capability"])
			test.mutate(selected)
			pcR6CheckBool(t, "bound", selectedSaveCapabilityBound(pkg, test.adapter, selected), test.want)
		})
	}
}

// pcR6RestartPredicate is one named restart predicate, a change to the
// reconciliation case, and what the predicate must then return.
type pcR6RestartPredicate struct {
	name   string
	check  func(r restartCase) bool
	mutate func(*testing.T, map[string]any)
	want   bool
}

var (
	pcR6Core       = []string{"core_preconditions"}
	pcR6SaveStep   = []string{"core_preconditions", "completed_save_step"}
	pcR6StepInput  = []string{"core_preconditions", "completed_save_step", "input"}
	pcR6Save       = []string{"request", "save_confirmation"}
	pcR6Before     = []string{"core_preconditions", "unit_inspection"}
	pcR6Output     = []string{"output"}
	pcR6OutputRef  = []string{"output", "external_operation_ref"}
	pcR6Resolved   = []string{"resolved_operation"}
	pcR6After      = []string{"unit_inspection"}
	pcR6Unit       = []string{"request", "unit_ref"}
	pcR6Instance   = []string{"request", "target", "instance_ref"}
	pcR6OtherUnit  = map[string]any{"adapter_id": "execution.systemd", "environment_id": "staging", "resource_kind": "systemd-unit", "external_id": "other"}
	pcR6NoMutation = func(*testing.T, map[string]any) {}
)

func pcR6RestartPredicates() []pcR6RestartPredicate {
	timesValid := func(r restartCase) bool { return r.timesValid() }
	namesBinding := func(r restartCase) bool { return r.namesUnit(r.binding, "release_id") }
	namesBefore := func(r restartCase) bool { return r.namesUnit(r.before, "release_ref") }
	namesOutput := func(r restartCase) bool { return r.namesRestart(r.output) }
	inputRequested := func(r restartCase) bool { return r.saveStepInputRequested(asObject(r.saveStep["input"])) }
	stepSucceeded := func(r restartCase) bool { return r.saveStepSucceededInOperation() }
	ranCapability := func(r restartCase) bool {
		return r.saveStepRanCapability("game.minecraft-paper", map[string]any{"version": "1"})
	}
	evidenceOrdered := func(r restartCase) bool { return r.saveEvidenceOrdered() }
	saveConfirmed := func(r restartCase) bool { return r.saveConfirmed() }
	operationRef := func(r restartCase) bool { return r.unitOperationRef(asObject(r.output["external_operation_ref"])) }
	operationBound := func(r restartCase) bool { return r.operationBound() }
	resolvedCleanly := func(r restartCase) bool { return r.resolvedCleanly() }
	newRuntime := func(r restartCase) bool { return r.afterRunsResolvedRuntime() }
	adapterBound := func(r restartCase) bool { return r.adapterBound() }
	targetConsistent := func(r restartCase) bool { return r.targetConsistent() }
	return []pcR6RestartPredicate{
		{"times: acceptance missing", timesValid, pcR6SetField("operation_accepted_at", "", pcR6Core...), false},
		{"times: dispatch before acceptance", timesValid, pcR6ShiftField("dispatch_at", pcR6Core, "operation_accepted_at", pcR6Core, -time.Second), false},
		{"times: evaluation before dispatch", timesValid, pcR6ShiftField("evaluated_at", pcR6Core, "dispatch_at", pcR6Core, -time.Second), false},
		{"binding: another unit", namesBinding, pcR6SetField("unit_ref", pcR6OtherUnit, "core_preconditions", "unit_binding"), false},
		{"binding: another target", namesBinding, pcR6SetField("target", map[string]any{}, "core_preconditions", "unit_binding"), false},
		{"binding: another release", namesBinding, pcR6SetField("release_id", "other", "core_preconditions", "unit_binding"), false},
		{"inspection: release under release_ref", namesBefore, pcR6SetField("release_ref", "other", pcR6Before...), false},
		{"result: another previous runtime", namesOutput, pcR6SetField("previous_runtime_id", "other", pcR6Output...), false},
		{"result: another release", namesOutput, pcR6SetField("release_id", "other", pcR6Output...), false},
		{"save input: another target", inputRequested, pcR6SetField("target", map[string]any{}, pcR6StepInput...), false},
		{"save input: another release", inputRequested, pcR6SetField("release_id", "other", pcR6StepInput...), false},
		{"save input: another reason", inputRequested, pcR6SetField("reason", "backup", pcR6StepInput...), false},
		{"save step: no operation", stepSucceeded, func(t *testing.T, c map[string]any) {
			pcR6SetField("operation_id", "", pcR6Core...)(t, c)
			pcR6SetField("operation_id", "", pcR6SaveStep...)(t, c)
		}, false},
		{"save step: another operation", stepSucceeded, pcR6SetField("operation_id", "other", pcR6SaveStep...), false},
		{"save step: another step", stepSucceeded, pcR6SetField("step_id", "other", pcR6SaveStep...), false},
		{"save step: not succeeded", stepSucceeded, pcR6SetField("status", "pending", pcR6SaveStep...), false},
		{"save step: another adapter", ranCapability, pcR6SetField("adapter_id", "game.other", pcR6SaveStep...), false},
		{"save step: another capability", ranCapability, pcR6SetField("capability_name", "game.data.describe", pcR6SaveStep...), false},
		{"save step: another version", ranCapability, pcR6SetField("capability_version", "2", pcR6SaveStep...), false},
		{"save order: operation times invalid", evidenceOrdered, pcR6SetField("dispatch_at", "", pcR6Core...), false},
		{"save order: save time missing", evidenceOrdered, pcR6SetField("saved_at", "", pcR6Save...), false},
		{"save order: saved before acceptance", evidenceOrdered, pcR6ShiftField("saved_at", pcR6Save, "operation_accepted_at", pcR6Core, -time.Second), false},
		{"save order: observed before saved", evidenceOrdered, pcR6ShiftField("observed_at", pcR6Save, "saved_at", pcR6Save, -time.Second), false},
		{"save order: observed after dispatch", evidenceOrdered, pcR6ShiftField("observed_at", pcR6Save, "dispatch_at", pcR6Core, time.Second), false},
		{"save order: step completed before observation", evidenceOrdered, pcR6ShiftField("completed_at", pcR6SaveStep, "observed_at", pcR6Save, -time.Second), false},
		{"save order: step completed after dispatch", evidenceOrdered, pcR6ShiftField("completed_at", pcR6SaveStep, "dispatch_at", pcR6Core, time.Second), false},
		{"save order: step completion missing", evidenceOrdered, pcR6SetField("completed_at", "", pcR6SaveStep...), false},
		{"save: not confirmed", saveConfirmed, pcR6SetField("outcome", "unknown", pcR6Save...), false},
		{"save: another target", saveConfirmed, pcR6SetField("target", map[string]any{}, pcR6Save...), false},
		{"save: another release", saveConfirmed, pcR6SetField("release_id", "other", pcR6Save...), false},
		{"operation ref: another adapter", operationRef, pcR6SetField("adapter_id", "execution.other", pcR6OutputRef...), false},
		{"operation ref: another environment", operationRef, pcR6SetField("environment_id", "production", pcR6OutputRef...), false},
		{"operation ref: another kind", operationRef, pcR6SetField("resource_kind", "systemd-unit", pcR6OutputRef...), false},
		{"operation: no reference", operationBound, func(_ *testing.T, c map[string]any) {
			delete(asObject(c["output"]), "external_operation_ref")
			c["resolved_operation"] = map[string]any{}
		}, true},
		{"operation: reference not resolved", operationBound, pcR6SetField("external_id", "other", "resolved_operation", "external_operation_ref"), false},
		{"operation: resolved for another runtime", operationBound, pcR6SetField("previous_runtime_id", "other", pcR6Resolved...), false},
		{"operation: unit-operation of another adapter", operationBound, pcR6SetField("adapter_id", "execution.other", pcR6OutputRef...), false},
		{"resolved: not completed", resolvedCleanly, pcR6SetField("state", "pending", pcR6Resolved...), false},
		{"resolved: completion missing", resolvedCleanly, pcR6SetField("completed_at", "", pcR6Resolved...), false},
		{"resolved: completed before dispatch", resolvedCleanly, pcR6ShiftField("completed_at", pcR6Resolved, "dispatch_at", pcR6Core, -time.Second), false},
		{"resolved: completed after inspection", resolvedCleanly, pcR6ShiftField("completed_at", pcR6Resolved, "observed_at", pcR6After, time.Second), false},
		{"resolved: no clean stop", resolvedCleanly, pcR6SetField("clean_stop_confirmed", false, pcR6Resolved...), false},
		{"after: not running", newRuntime, pcR6SetField("state", "stopped", pcR6After...), false},
		{"after: no runtime", newRuntime, pcR6SetField("runtime_id", "", pcR6After...), false},
		{"after: old runtime", newRuntime, func(t *testing.T, c map[string]any) {
			pcR6SetField("runtime_id", "runtime-before-001", pcR6After...)(t, c)
			pcR6SetField("runtime_id", "runtime-before-001", pcR6Resolved...)(t, c)
		}, false},
		{"after: another runtime than resolved", newRuntime, pcR6SetField("runtime_id", "runtime-after-003", pcR6Resolved...), false},
		{"adapter: none selected", adapterBound, pcR6SetField("execution_adapter_id", "", pcR6Core...), false},
		{"adapter: another adapter", adapterBound, pcR6SetField("execution_adapter_id", "execution.other", pcR6Core...), false},
		{"target: unit in another environment", targetConsistent, pcR6SetField("environment_id", "production", pcR6Unit...), false},
		{"target: instance in another environment", targetConsistent, pcR6SetField("environment_id", "production", pcR6Instance...), false},
		{"target: not a systemd unit", targetConsistent, pcR6SetField("resource_kind", "container", pcR6Unit...), false},
	}
}

func TestRestartCasePredicates(t *testing.T) {
	for _, test := range pcR6RestartPredicates() {
		t.Run(test.name, func(t *testing.T) {
			baseline := pcR6RestartCase(t, "reconcile-completed-new-runtime", pcR6NoMutation)
			pcR6CheckBool(t, "baseline", test.check(baseline), true)
			mutated := pcR6RestartCase(t, "reconcile-completed-new-runtime", test.mutate)
			pcR6CheckBool(t, "mutated", test.check(mutated), test.want)
		})
	}
}

func TestRestartInspectionInconclusive(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*testing.T, map[string]any)
		reconciled bool
		afterBound bool
		inconclusive,
		unresolved bool
	}{
		{"reconciled", pcR6NoMutation, true, true, false, false},
		{"unknown state", pcR6SetField("state", "unknown", pcR6After...), true, true, true, true},
		{"completed but not reconciled", pcR6NoMutation, false, true, true, true},
		{"pending and not reconciled", pcR6SetField("state", "pending", pcR6Resolved...), false, true, false, false},
		{"unbound inspection", pcR6NoMutation, true, false, false, true},
		{"no inspection", func(_ *testing.T, c map[string]any) { c["unit_inspection"] = map[string]any{} }, false, false, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := pcR6RestartCase(t, "reconcile-completed-new-runtime", test.mutate)
			pcR6CheckBool(t, "inspectionInconclusive", r.inspectionInconclusive(test.reconciled), test.inconclusive)
			pcR6CheckBool(t, "unresolved", r.unresolved(test.afterBound, test.reconciled), test.unresolved)
		})
	}
}

func TestRestartDispatchAllowed(t *testing.T) {
	ready := restartDispatch{requestValid: true, adapterBound: true, targetConsistent: true, unitBound: true, unitCurrent: true, saveBound: true}
	tests := []struct {
		name                         string
		mutate                       func(*restartDispatch)
		requestBound, unitReady, all bool
	}{
		{"all preconditions", func(*restartDispatch) {}, true, true, true},
		{"invalid request", func(d *restartDispatch) { d.requestValid = false }, false, true, false},
		{"another adapter", func(d *restartDispatch) { d.adapterBound = false }, false, true, false},
		{"inconsistent target", func(d *restartDispatch) { d.targetConsistent = false }, false, true, false},
		{"unbound unit", func(d *restartDispatch) { d.unitBound = false }, true, false, false},
		{"stale unit", func(d *restartDispatch) { d.unitCurrent = false }, true, false, false},
		{"unbound save", func(d *restartDispatch) { d.saveBound = false }, true, false, false},
		{"save step alone does not gate dispatch", func(d *restartDispatch) { d.saveStepBound = false }, true, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := ready
			test.mutate(&d)
			pcR6CheckBool(t, "requestBound", d.requestBound(), test.requestBound)
			pcR6CheckBool(t, "unitReady", d.unitReady(), test.unitReady)
			pcR6CheckBool(t, "allowed", d.allowed(), test.all)
		})
	}
}

func TestRestartOutcomeHelpers(t *testing.T) {
	for outcome, want := range map[string]bool{"accepted": true, "outcome_unknown": true, "completed": false, "": false} {
		pcR6CheckBool(t, "uncertain "+outcome, restartOutcomeUncertain(outcome), want)
	}
	tests := []struct {
		name                     string
		outcome                  restartOutcome
		dispatchAllowed          bool
		confirmed, mustInspectIt bool
	}{
		{"confirmed by the result", restartOutcome{confirmed: true, resultValid: true}, true, true, false},
		{"confirmed by reconciliation", restartOutcome{reconciled: true, resultValid: true, uncertain: true}, true, true, false},
		{"invalid result", restartOutcome{}, true, false, true},
		{"uncertain and unreconciled", restartOutcome{resultValid: true, uncertain: true}, true, false, true},
		{"valid and certain", restartOutcome{resultValid: true}, true, false, false},
		{"never dispatched", restartOutcome{uncertain: true}, false, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pcR6CheckBool(t, "restartConfirmed", test.outcome.restartConfirmed(), test.confirmed)
			pcR6CheckBool(t, "mustInspectBeforeRetry", test.outcome.mustInspectBeforeRetry(test.dispatchAllowed), test.mustInspectIt)
		})
	}
}

func TestRestartOutcomeChecks(t *testing.T) {
	pkg, _, _ := restartFixtureCase(t, "reconcile-completed-new-runtime")
	r := pcR6RestartCase(t, "reconcile-completed-new-runtime", pcR6NoMutation)
	result := restartOutcome{outputValid: true, operationBound: true, bound: true, resultValid: true, uncertain: true}
	if got := r.resultChecks(pkg); got != result {
		t.Fatalf("resultChecks = %+v, want %+v", got, result)
	}
	reconciled := result
	reconciled.reconciled = true
	if got := r.outcomeChecks(pkg, true); got != reconciled {
		t.Fatalf("outcomeChecks(dispatched) = %+v, want %+v", got, reconciled)
	}
	if got := r.outcomeChecks(pkg, false); got != result {
		t.Fatalf("outcomeChecks(not dispatched) = %+v, want %+v", got, result)
	}
}
