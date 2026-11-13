package main

import (
	"fmt"
	"reflect"
)

// Register the game health and readiness evaluators defined in this file.
func init() {
	registerEvaluator(evaluateGameHealthCheckUnknownNotHealthy, "game.health.check.unknown-not-healthy")
	registerEvaluator(evaluateGameReadinessUnknownRequiredCheckBlocksReady, "game.readiness.unknown-required-check-blocks-ready")
}

// evaluateGameHealthCheckUnknownNotHealthy evaluates the game.health.check.unknown-not-healthy conformance fixture.
func evaluateGameHealthCheckUnknownNotHealthy(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	healthInput, healthOutput := asObject(input["health_input"]), asObject(input["health_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:health-check-input", healthInput); err != nil {
		return nil, fmt.Errorf("health input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:health-check-output", healthOutput); err != nil {
		return nil, fmt.Errorf("health output does not match schema: %w", err)
	}
	healthInstance := asObject(healthInput["instance_ref"])
	bound := healthInput["server_id"] == healthOutput["server_id"] &&
		healthInput["environment_id"] == healthOutput["environment_id"] &&
		stringValue(healthInstance, "environment_id") == stringValue(healthInput, "environment_id") &&
		healthInput["game_release_id"] == healthOutput["game_release_id"] &&
		reflect.DeepEqual(healthInput["instance_ref"], healthOutput["instance_ref"])
	required := arrayValue(healthInput, "required_check_ids")
	results := arrayValue(healthOutput, "check_results")
	requested := make(map[string]bool, len(required))
	for _, rawID := range required {
		requested[rawID.(string)] = true
	}
	observed := make(map[string]string, len(results))
	complete := len(results) == len(requested)
	for _, rawResult := range results {
		item := asObject(rawResult)
		id := stringValue(item, "check_id")
		if !requested[id] {
			complete = false
		}
		if _, exists := observed[id]; exists {
			complete = false
		}
		observed[id] = stringValue(item, "result")
	}
	health := "healthy"
	hasFailure, hasUnknown, hasWarning := false, false, false
	for id := range requested {
		result, ok := observed[id]
		if !ok {
			complete = false
		}
		switch result {
		case "pass":
		case "warn":
			hasWarning = true
		case "fail":
			hasFailure = true
		case "unknown":
			hasUnknown = true
		default:
			complete = false
		}
	}
	incompleteEvidence := hasUnknown || !complete
	if hasFailure {
		health = "unhealthy"
	} else if incompleteEvidence {
		health = "unknown"
	} else if hasWarning {
		health = "degraded"
	}
	return map[string]any{
		"schemas_valid":           true,
		"target_bound":            bound,
		"check_set_complete":      complete,
		"derived_health":          health,
		"reported_health_matches": stringValue(healthOutput, "health") == health,
		"unknown_not_healthy":     !incompleteEvidence || stringValue(healthOutput, "health") != "healthy",
	}, nil
}

// evaluateGameReadinessUnknownRequiredCheckBlocksReady evaluates the game.readiness.unknown-required-check-blocks-ready conformance fixture.
func evaluateGameReadinessUnknownRequiredCheckBlocksReady(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-input", input); err != nil {
		return nil, fmt.Errorf("readiness input does not match schema: %w", err)
	}
	output := asObject(fixture["output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-output", output); err != nil {
		return nil, fmt.Errorf("readiness output does not match schema: %w", err)
	}
	bound := input["server_id"] == output["server_id"] &&
		input["environment_id"] == output["environment_id"] &&
		input["game_release_id"] == output["game_release_id"] &&
		reflect.DeepEqual(input["instance_ref"], output["instance_ref"])
	if !bound {
		return nil, fmt.Errorf("readiness output must remain bound to the exact requested target and release")
	}
	requiredChecks := arrayValue(input, "required_checks")
	if len(requiredChecks) == 0 {
		return nil, fmt.Errorf("readiness request must contain required checks")
	}
	requested := make(map[string]string, len(requiredChecks))
	for _, rawRequirement := range requiredChecks {
		requirement := asObject(rawRequirement)
		checkID := stringValue(requirement, "check_id")
		checkVersion := stringValue(requirement, "check_version")
		if checkID == "" || checkVersion == "" {
			return nil, fmt.Errorf("required check IDs and versions must be non-empty")
		}
		if _, duplicate := requested[checkID]; duplicate {
			return nil, fmt.Errorf("required check IDs must be unique")
		}
		requested[checkID] = checkVersion
	}
	results := arrayValue(output, "check_results")
	if len(results) != len(requested) {
		return nil, fmt.Errorf("readiness output must contain exactly one result for every required check")
	}
	type observedReadiness struct {
		result string
	}
	observed := make(map[string]observedReadiness, len(results))
	for _, rawResult := range results {
		item := asObject(rawResult)
		checkID := stringValue(item, "check_id")
		expectedVersion, exists := requested[checkID]
		if !exists {
			return nil, fmt.Errorf("readiness output contains an unrequested check %q", checkID)
		}
		if _, exists := observed[checkID]; exists {
			return nil, fmt.Errorf("readiness output contains duplicate check %q", checkID)
		}
		version := stringValue(item, "check_version")
		if version == "" || version != expectedVersion {
			return nil, fmt.Errorf("readiness output check %q has a missing or mismatched version", checkID)
		}
		observed[checkID] = observedReadiness{result: stringValue(item, "result")}
	}
	allPassed, hasFailure, hasUnknown := true, false, false
	for checkID := range requested {
		result, exists := observed[checkID]
		if !exists {
			return nil, fmt.Errorf("readiness output is missing required check %q", checkID)
		}
		switch result.result {
		case "pass":
		case "fail":
			allPassed, hasFailure = false, true
		case "unknown":
			allPassed, hasUnknown = false, true
		default:
			return nil, fmt.Errorf("unknown readiness result %q for check %q", result.result, checkID)
		}
	}
	readiness := "ready"
	if hasFailure {
		readiness = "not_ready"
	} else if hasUnknown {
		readiness = "unknown"
	}
	return map[string]any{
		"schema_valid": true, "target_bound": bound,
		"required_checks_complete": true, "all_required_checks_passed": allPassed,
		"readiness": readiness, "ready_transition_permitted": allPassed,
	}, nil
}
