package main

import (
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

// notBefore: i is present and not earlier than lower. Whether lower is
// present is the caller's check.
func (i instant) notBefore(lower instant) bool {
	return i.ok && !i.at.Before(lower.at)
}

// between: i is present and lies in [lower, upper]. Whether the bounds are
// present is the caller's check.
func (i instant) between(lower, upper instant) bool {
	return i.notBefore(lower) && !upper.at.Before(i.at)
}

// restartInspectOutputSchema is the systemd unit inspection both the pre- and
// post-dispatch inspections must match.
const restartInspectOutputSchema = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output"

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
	return r.accepted.ok && r.dispatched.notBefore(r.accepted) && r.evaluated.notBefore(r.dispatched)
}

// namesUnit: object names this unit and target, and the request's release
// under releaseKey.
func (r restartCase) namesUnit(object map[string]any, releaseKey string) bool {
	return reflect.DeepEqual(r.unit, object["unit_ref"]) && reflect.DeepEqual(r.target, object["target"]) &&
		r.request["release_id"] == object[releaseKey]
}

// namesRestart: object names this unit, target and release, and the
// request's expected runtime as its previous runtime.
func (r restartCase) namesRestart(object map[string]any) bool {
	return r.namesUnit(object, "release_id") && r.request["expected_runtime_id"] == object["previous_runtime_id"]
}

// unitCurrent: the pre-dispatch inspection shows this unit running the
// expected runtime, observed after acceptance and before dispatch.
func (r restartCase) unitCurrent(pkg *protocolPackage) bool {
	inspected := pkg.validateSchema(restartInspectOutputSchema, r.before) == nil && r.namesUnit(r.before, "release_ref") &&
		r.before["runtime_id"] == r.request["expected_runtime_id"] && stringValue(r.before, "state") == "running"
	return inspected && r.timesValid() && r.unitObserved.between(r.accepted, r.dispatched)
}

// saveConfirmationBound: the save confirmation is the save step's output,
// confirmed, for this target and release, and ordered between the unit
// inspection and dispatch.
func (r restartCase) saveConfirmationBound(saveStepBound, unitInspectionPrecedesSave bool) bool {
	return saveStepBound && unitInspectionPrecedesSave && r.saveEvidenceOrdered() && r.saveConfirmed()
}

// saveEvidenceOrdered: the operation times are valid, the save happened
// after acceptance, the save observation falls between the save and
// dispatch, and the save step completed between that observation and
// dispatch.
func (r restartCase) saveEvidenceOrdered() bool {
	return r.timesValid() && r.saved.notBefore(r.accepted) && r.saveObserved.between(r.saved, r.dispatched) &&
		r.stepCompleted.between(r.saveObserved, r.dispatched)
}

// saveConfirmed: the save confirmation is confirmed for this target and
// release.
func (r restartCase) saveConfirmed() bool {
	return stringValue(r.save, "outcome") == "confirmed" && reflect.DeepEqual(r.target, r.save["target"]) &&
		r.request["release_id"] == r.save["release_id"]
}

// operationBound: an external operation reference, if any, names this
// adapter's unit operation and matches the resolved operation.
func (r restartCase) operationBound() bool {
	operationRef, hasOperationRef := r.output["external_operation_ref"]
	if !hasOperationRef {
		return true
	}
	return r.unitOperationRef(asObject(operationRef)) &&
		reflect.DeepEqual(operationRef, r.resolved["external_operation_ref"]) && r.namesRestart(r.resolved)
}

// unitOperationRef: ref is a unit operation of this unit's adapter and
// environment.
func (r restartCase) unitOperationRef(ref map[string]any) bool {
	return ref["adapter_id"] == r.unit["adapter_id"] && ref["environment_id"] == r.unit["environment_id"] &&
		stringValue(ref, "resource_kind") == "unit-operation"
}

// resultBound: the restart result names this unit, target, release, and
// previous runtime, and was observed between dispatch and evaluation.
func (r restartCase) resultBound(operationBound bool) bool {
	return r.namesRestart(r.output) && operationBound && r.timesValid() && r.observed.between(r.dispatched, r.evaluated)
}

// afterBound: the post-dispatch inspection is valid, names this unit, and was
// observed between dispatch and evaluation.
func (r restartCase) afterBound(pkg *protocolPackage) bool {
	afterValid := pkg.validateSchema(restartInspectOutputSchema, r.after) == nil
	return afterValid && r.namesUnit(r.after, "release_ref") && r.afterObserved.between(r.dispatched, r.evaluated)
}

// reconciled: an uncertain restart is proven complete by the resolved
// operation and a running inspection with a new runtime.
func (r restartCase) reconciled(afterBound bool) bool {
	return r.resolvedCleanly() && afterBound && r.afterRunsResolvedRuntime()
}

// resolvedCleanly: the resolved operation completed after dispatch, no
// later than the follow-up inspection, with a confirmed clean stop.
func (r restartCase) resolvedCleanly() bool {
	return stringValue(r.resolved, "state") == "completed" && r.completed.notBefore(r.dispatched) &&
		!r.afterObserved.at.Before(r.completed.at) && boolValue(r.resolved, "clean_stop_confirmed")
}

// afterRunsResolvedRuntime: the follow-up inspection shows the unit running
// the resolved operation's runtime, which is new.
func (r restartCase) afterRunsResolvedRuntime() bool {
	return stringValue(r.after, "state") == "running" && stringValue(r.after, "runtime_id") != "" &&
		r.after["runtime_id"] != r.request["expected_runtime_id"] && r.after["runtime_id"] == r.resolved["runtime_id"]
}

func evaluateUnitRestartCase(pkg *protocolPackage, fixture, c map[string]any) map[string]any {
	r := newRestartCase(c)
	d := r.dispatchChecks(pkg, fixture, c)
	dispatchAllowed := d.allowed()
	o := r.outcomeChecks(pkg, dispatchAllowed)
	return map[string]any{
		"case_id": stringValue(c, "case_id"), "request_schema_valid": d.requestValid, "result_schema_valid": o.outputValid,
		"adapter_bound": d.adapterBound, "target_consistent": d.targetConsistent, "unit_bound": d.unitBound, "unit_current": d.unitCurrent,
		"save_step_bound": d.saveStepBound, "unit_inspection_precedes_save": d.inspectionPrecedesSave, "save_confirmation_bound": d.saveBound,
		"dispatch_allowed": dispatchAllowed, "result_bound": o.bound,
		"restart_confirmed": o.restartConfirmed(), "reconciliation_confirmed": o.reconciled,
		"must_inspect_before_retry": o.mustInspectBeforeRetry(dispatchAllowed),
		"retry_permitted":           false, "operator_required": o.operatorRequired, "readiness_established": false,
	}
}

// restartDispatch holds the preconditions for dispatching a unit restart.
type restartDispatch struct {
	requestValid, saveStepBound, adapterBound, targetConsistent bool
	unitBound, unitCurrent, inspectionPrecedesSave, saveBound   bool
}

func (r restartCase) dispatchChecks(pkg *protocolPackage, fixture, c map[string]any) restartDispatch {
	d := restartDispatch{
		requestValid:     pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-input", r.request) == nil,
		saveStepBound:    restartSaveStepBound(pkg, fixture, c, r),
		adapterBound:     r.adapterBound(),
		targetConsistent: r.targetConsistent(),
		unitBound:        r.namesUnit(r.binding, "release_id"),
		unitCurrent:      r.unitCurrent(pkg),
	}
	d.inspectionPrecedesSave = d.unitCurrent && r.saved.ok && !r.saved.at.Before(r.unitObserved.at)
	d.saveBound = r.saveConfirmationBound(d.saveStepBound, d.inspectionPrecedesSave)
	return d
}

// adapterBound: the unit belongs to the operation's execution adapter.
func (r restartCase) adapterBound() bool {
	return stringValue(r.core, "execution_adapter_id") != "" && r.unit["adapter_id"] == r.core["execution_adapter_id"]
}

// targetConsistent: the unit, target and instance share one environment,
// and the unit is a systemd unit.
func (r restartCase) targetConsistent() bool {
	return r.unit["environment_id"] == r.target["environment_id"] && r.target["environment_id"] == r.instance["environment_id"] &&
		stringValue(r.unit, "resource_kind") == "systemd-unit"
}

func (d restartDispatch) allowed() bool {
	return d.requestBound() && d.unitReady()
}

// requestBound: the request is valid and names a consistent unit of the
// execution adapter.
func (d restartDispatch) requestBound() bool {
	return d.requestValid && d.adapterBound && d.targetConsistent
}

// unitReady: the unit is bound, current, and saved before dispatch.
func (d restartDispatch) unitReady() bool {
	return d.unitBound && d.unitCurrent && d.saveBound
}

// restartOutcome is what the restart result, and any follow-up
// inspection, proves.
type restartOutcome struct {
	outputValid, operationBound, bound, resultValid, uncertain bool
	confirmed, reconciled, operatorRequired                    bool
}

// outcomeChecks: a valid, bound result with a consistent stop-start cycle
// confirms the restart when completed. An accepted or unknown outcome is
// reconciled only by a bound inspection showing a new running runtime;
// anything else that cannot be resolved needs an operator.
func (r restartCase) outcomeChecks(pkg *protocolPackage, dispatchAllowed bool) restartOutcome {
	o := r.resultChecks(pkg)
	o.confirmed = dispatchAllowed && o.resultValid && stringValue(r.output, "outcome") == "completed"
	afterBound := r.afterBound(pkg)
	o.reconciled = dispatchAllowed && o.resultValid && o.uncertain && r.reconciled(afterBound)
	o.operatorRequired = dispatchAllowed && o.uncertain && (!o.operationBound || r.unresolved(afterBound, o.reconciled))
	return o
}

// resultChecks: what the restart result alone shows, before dispatch
// permission and any follow-up inspection are considered.
func (r restartCase) resultChecks(pkg *protocolPackage) restartOutcome {
	o := restartOutcome{
		outputValid:    pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-restart-output", r.output) == nil,
		operationBound: r.operationBound(),
	}
	o.bound = r.resultBound(o.operationBound)
	o.resultValid = o.outputValid && o.bound && r.cycleConsistent()
	o.uncertain = restartOutcomeUncertain(stringValue(r.output, "outcome"))
	return o
}

// restartOutcomeUncertain: the adapter only accepted the restart, or does
// not know how it ended.
func restartOutcomeUncertain(outcome string) bool {
	return outcome == "accepted" || outcome == "outcome_unknown"
}

// restartConfirmed: the result confirmed the restart, or reconciliation did.
func (o restartOutcome) restartConfirmed() bool {
	return o.confirmed || o.reconciled
}

// mustInspectBeforeRetry: a dispatched restart whose result is invalid, or
// uncertain and not reconciled, must be inspected before any retry.
func (o restartOutcome) mustInspectBeforeRetry(dispatchAllowed bool) bool {
	return dispatchAllowed && (!o.resultValid || (o.uncertain && !o.reconciled))
}

// cycleConsistent: a new runtime is reported only with a confirmed clean
// stop, and a completed restart always reports a new runtime.
func (r restartCase) cycleConsistent() bool {
	newRuntime := stringValue(r.output, "runtime_id") != "" && r.output["runtime_id"] != r.request["expected_runtime_id"]
	return (!newRuntime || boolValue(r.output, "clean_stop_confirmed")) && (stringValue(r.output, "outcome") != "completed" || newRuntime)
}

// unresolved: a follow-up inspection exists but is unbound, reports an
// unknown state, or shows a completed operation that did not reconcile.
func (r restartCase) unresolved(afterBound, reconciled bool) bool {
	return len(r.after) > 0 && (!afterBound || r.inspectionInconclusive(reconciled))
}

// inspectionInconclusive: the follow-up inspection reports an unknown state,
// or the operation completed without reconciling.
func (r restartCase) inspectionInconclusive(reconciled bool) bool {
	return stringValue(r.after, "state") == "unknown" || (stringValue(r.resolved, "state") == "completed" && !reconciled)
}

func init() {
	registerEvaluator(evaluateUnitRestart, "execution.unit.restart.clean-stop-distinct-runtime")
}
