package main

import (
	"fmt"
	"reflect"
)

// Register the required readiness set evaluators defined in this file.
func init() {
	registerEvaluator(evaluateLifecycleRequiredReadinessSet, "lifecycle.required-readiness-set")
}

// evaluateLifecycleRequiredReadinessSet evaluates the lifecycle.required-readiness-set conformance fixture.
func evaluateLifecycleRequiredReadinessSet(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-input", input); err != nil {
		return nil, fmt.Errorf("readiness request does not match schema: %w", err)
	}
	set := asObject(fixture["declared_check_set"])
	versions := asObject(set["check_versions"])
	setValid, required := requiredCheckSetValid(input, set, versions)
	caseResults := make([]any, 0, len(arrayValue(fixture, "evaluations")))
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		result, err := readinessCaseResult(pkg, input, asObject(rawCase), setValid, required, versions)
		if err != nil {
			return nil, err
		}
		caseResults = append(caseResults, result)
	}
	return map[string]any{"readiness_check_set_valid": setValid, "evaluations": caseResults, "ready_transition_requires_complete_passing_set": true}, nil
}

// requiredCheckSetValid: the request names the declared check set, and its
// required checks are exactly the declared checks, each once, at the
// declared version. It returns the required check IDs in request order.
func requiredCheckSetValid(input, set, versions map[string]any) (bool, []any) {
	requiredEntries := arrayValue(input, "required_checks")
	declared := arrayValue(set, "check_ids")
	required := make([]any, 0, len(requiredEntries))
	requiredVersions := make(map[string]string, len(requiredEntries))
	valid := stringValue(set, "readiness_check_set_id") != "" &&
		stringValue(input, "readiness_check_set_id") == stringValue(set, "readiness_check_set_id") &&
		uniqueStrings(declared)
	for _, rawRequirement := range requiredEntries {
		requirement := asObject(rawRequirement)
		checkID := stringValue(requirement, "check_id")
		checkVersion := stringValue(requirement, "check_version")
		if checkID == "" || checkVersion == "" {
			valid = false
			continue
		}
		if _, duplicate := requiredVersions[checkID]; duplicate {
			valid = false
		}
		required = append(required, checkID)
		requiredVersions[checkID] = checkVersion
		if declaredVersion, ok := versions[checkID].(string); !ok || declaredVersion == "" || declaredVersion != checkVersion {
			valid = false
		}
	}
	return valid && sameStringSet(required, declared), required
}

// readinessCaseResult decides one readiness observation. An unknown check
// state is reported before schema validation; otherwise the observation
// must be for the requested target and report exactly the required checks,
// at their declared versions, all passing.
func readinessCaseResult(pkg *protocolPackage, input, readinessCase map[string]any, setValid bool, required []any, versions map[string]any) (map[string]any, error) {
	output := asObject(readinessCase["output"])
	result := map[string]any{
		"case_id":                    stringValue(readinessCase, "case_id"),
		"ready_transition_permitted": false,
		"readiness":                  "not_ready",
		"block_reason":               any(nil),
		"error_code":                 any(nil),
	}
	if hasUnknownCheckState(output) {
		result["readiness"] = "unknown"
		result["block_reason"] = "unknown_state"
		result["error_code"] = "core.compatibility.unknown_state"
		return result, nil
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-output", output); err != nil {
		return nil, fmt.Errorf("readiness output %q does not match schema: %w", stringValue(readinessCase, "case_id"), err)
	}
	bound := input["server_id"] == output["server_id"] &&
		input["environment_id"] == output["environment_id"] &&
		input["game_release_id"] == output["game_release_id"] &&
		reflect.DeepEqual(input["instance_ref"], output["instance_ref"])
	switch {
	case !setValid:
		result["block_reason"] = "invalid_required_check_set"
		return result, nil
	case !bound:
		result["block_reason"] = "target_mismatch"
		return result, nil
	}
	complete, versionsMatch, allPassed := readinessResultsMatch(arrayValue(output, "check_results"), required, versions)
	switch {
	case !complete || !versionsMatch:
		result["block_reason"] = "required_check_set_mismatch"
	case !allPassed:
		result["readiness"] = "unknown"
		result["block_reason"] = "required_check_not_passed"
	default:
		result["ready_transition_permitted"] = true
		result["readiness"] = "ready"
	}
	return result, nil
}

// hasUnknownCheckState: some check result is not pass, fail or unknown.
func hasUnknownCheckState(output map[string]any) bool {
	for _, rawResult := range arrayValue(output, "check_results") {
		status := stringValue(asObject(rawResult), "result")
		if status != "pass" && status != "fail" && status != "unknown" {
			return true
		}
	}
	return false
}

// readinessResultsMatch: complete means exactly one result per required
// check and nothing else; versionsMatch that each result has its declared
// version; allPassed that every result passed.
func readinessResultsMatch(results, required []any, versions map[string]any) (complete, versionsMatch, allPassed bool) {
	observed := make(map[string]int)
	versionsMatch, allPassed = true, true
	for _, rawResult := range results {
		check := asObject(rawResult)
		checkID := stringValue(check, "check_id")
		observed[checkID]++
		if check["check_version"] != versions[checkID] {
			versionsMatch = false
		}
		if stringValue(check, "result") != "pass" {
			allPassed = false
		}
	}
	complete = len(observed) == len(required) && len(results) == len(required)
	for _, rawID := range required {
		if id, ok := rawID.(string); !ok || observed[id] != 1 {
			complete = false
		}
	}
	return complete, versionsMatch, allPassed
}
