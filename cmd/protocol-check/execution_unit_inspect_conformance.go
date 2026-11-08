package main

import (
	"fmt"
	"reflect"
)

func evaluateSystemdUnitInspection(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	evaluations := arrayValue(input, "evaluations")
	if len(evaluations) == 0 {
		return nil, fmt.Errorf("inspection fixture must contain evaluation cases")
	}

	results := make([]any, 0, len(evaluations))
	for _, rawEvaluation := range evaluations {
		evaluation := asObject(rawEvaluation)
		caseID := stringValue(evaluation, "case_id")
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		if caseID == "" || request == nil || response == nil {
			return nil, fmt.Errorf("inspection evaluation must include case_id, request, and response")
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-input", request); err != nil {
			return nil, fmt.Errorf("%s: inspect input does not match schema: %w", caseID, err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-inspect-output", response); err != nil {
			return nil, fmt.Errorf("%s: inspect output does not match schema: %w", caseID, err)
		}

		unitBound := reflect.DeepEqual(request["unit_ref"], response["unit_ref"])
		state := stringValue(response, "state")
		results = append(results, map[string]any{
			"case_id":                  caseID,
			"request_schema_valid":     true,
			"response_schema_valid":    true,
			"unit_bound":               unitBound,
			"observation_conclusive":   unitBound && state != "unknown",
			"observation_unknown":      state == "unknown",
			"safe_to_treat_as_running": unitBound && state == "running",
			"safe_to_treat_as_stopped": unitBound && state == "stopped",
		})
	}
	return map[string]any{"evaluations": results}, nil
}
