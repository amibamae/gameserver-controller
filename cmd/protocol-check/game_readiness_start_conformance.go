package main

import (
	"fmt"
	"time"
)

func init() {
	registerEvaluator(evaluateGameReadinessStartBound, "game.readiness.start-bound-evidence")
}

const (
	readinessOutputSchema   = "urn:gameserver-controller:0.21.0-draft:game:readiness-check-output"
	unitInspectOutputSchema = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output"
	// runtimeIdentityVersion is the execution.unit.inspect and
	// game.readiness.check capability version that reports runtime_id.
	runtimeIdentityVersion = "2"
	// readinessClockSkew is how far an adapter clock may lag the controller.
	readinessClockSkew = 30 * time.Second
)

// evaluateGameReadinessStartBound evaluates each case of the
// game.readiness.start-bound-evidence fixture: a start is dispatched only
// when both runtime-identity capabilities are at version 2, and its
// readiness must be evaluated, in aggregate and per check, no earlier than
// the readiness dispatch less the clock-skew allowance, and name the runtime
// the post-start unit inspection reported.
func evaluateGameReadinessStartBound(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "evaluations")))
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		result, err := evaluateReadinessStartCase(pkg, asObject(rawCase))
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return map[string]any{"evaluations": results}, nil
}

func evaluateReadinessStartCase(pkg *protocolPackage, c map[string]any) (map[string]any, error) {
	caseID := stringValue(c, "case_id")
	inspection, readiness := asObject(c["unit_inspection"]), asObject(c["readiness_output"])
	if err := pkg.validateSchema(unitInspectOutputSchema, inspection); err != nil {
		return nil, fmt.Errorf("case %q unit inspection does not match schema: %w", caseID, err)
	}
	if err := pkg.validateSchema(readinessOutputSchema, readiness); err != nil {
		return nil, fmt.Errorf("case %q readiness output does not match schema: %w", caseID, err)
	}
	dispatched := instantOf(c, "readiness_dispatched_at")
	if !dispatched.ok {
		return nil, fmt.Errorf("case %q has no valid readiness_dispatched_at", caseID)
	}
	startPermitted := declaresRuntimeIdentity(asObject(c["capability_versions"]))
	notBefore := instant{at: dispatched.at.Add(-readinessClockSkew), ok: true}
	afterDispatch := readinessFollowsDispatch(readiness, notBefore)
	runtimeBound := readinessNamesStartedRuntime(inspection, readiness)
	return map[string]any{
		"case_id": caseID, "schemas_valid": true, "start_dispatch_permitted": startPermitted,
		"observed_after_dispatch": afterDispatch, "runtime_bound": runtimeBound,
		"ready_transition_permitted": startPermitted && afterDispatch && runtimeBound,
	}, nil
}

// declaresRuntimeIdentity reports whether the selected unit inspection and
// readiness capabilities are both at the runtime-identity version.
func declaresRuntimeIdentity(versions map[string]any) bool {
	return stringValue(versions, "execution.unit.inspect") == runtimeIdentityVersion &&
		stringValue(versions, "game.readiness.check") == runtimeIdentityVersion
}

// readinessFollowsDispatch reports whether the aggregate and every check
// observation are present and no earlier than notBefore.
func readinessFollowsDispatch(readiness map[string]any, notBefore instant) bool {
	if !instantOf(readiness, "observed_at").notBefore(notBefore) {
		return false
	}
	for _, rawCheck := range arrayValue(readiness, "check_results") {
		if !instantOf(asObject(rawCheck), "observed_at").notBefore(notBefore) {
			return false
		}
	}
	return true
}

// readinessNamesStartedRuntime reports whether the inspection reported a
// runtime and the readiness observation names exactly that runtime.
func readinessNamesStartedRuntime(inspection, readiness map[string]any) bool {
	started := stringValue(inspection, "runtime_id")
	return started != "" && stringValue(readiness, "runtime_id") == started
}
