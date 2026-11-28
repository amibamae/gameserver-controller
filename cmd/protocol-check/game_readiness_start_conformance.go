package main

import "fmt"

func init() {
	registerEvaluator(evaluateGameReadinessStartBound, "game.readiness.start-bound-evidence")
}

const (
	readinessOutputSchema   = "urn:gameserver-controller:0.21.0-draft:game:readiness-check-output"
	unitInspectOutputSchema = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output"
)

// evaluateGameReadinessStartBound evaluates each case of the
// game.readiness.start-bound-evidence fixture: readiness for a start must be
// observed, in aggregate and per check, no earlier than the unit start
// completed, and must name the runtime the post-start unit inspection
// reported.
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
	startCompleted := instantOf(c, "start_completed_at")
	if !startCompleted.ok {
		return nil, fmt.Errorf("case %q has no valid start_completed_at", caseID)
	}
	afterStart := readinessFollowsStart(readiness, startCompleted)
	runtimeBound := readinessNamesStartedRuntime(inspection, readiness)
	return map[string]any{
		"case_id": caseID, "schemas_valid": true,
		"observed_after_start": afterStart, "runtime_bound": runtimeBound,
		"ready_transition_permitted": afterStart && runtimeBound,
	}, nil
}

// readinessFollowsStart reports whether the aggregate and every check
// observation are present and no earlier than the start completion.
func readinessFollowsStart(readiness map[string]any, startCompleted instant) bool {
	if !instantOf(readiness, "observed_at").notBefore(startCompleted) {
		return false
	}
	for _, rawCheck := range arrayValue(readiness, "check_results") {
		if !instantOf(asObject(rawCheck), "observed_at").notBefore(startCompleted) {
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
