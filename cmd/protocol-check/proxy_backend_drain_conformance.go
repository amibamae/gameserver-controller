package main

import (
	"fmt"
	"reflect"
)

func evaluateProxyBackendDrainUnregisterState(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	target := asObject(input["target"])
	if target == nil {
		return nil, fmt.Errorf("proxy drain/unregister fixture must include a target")
	}
	cases := arrayValue(input, "cases")
	if len(cases) == 0 {
		return nil, fmt.Errorf("proxy drain/unregister fixture must include cases")
	}

	seen := make(map[string]bool, len(cases))
	caseResults := make(map[string]any, len(cases))
	for index, rawCase := range cases {
		caseInput := asObject(rawCase)
		caseID := stringValue(caseInput, "case_id")
		if caseID == "" || seen[caseID] {
			return nil, fmt.Errorf("proxy drain/unregister case %d has an empty or duplicate case_id", index)
		}
		seen[caseID] = true

		action := stringValue(caseInput, "action")
		var inputSchemaID, outputSchemaID string
		switch action {
		case "drain":
			inputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-drain-input"
			outputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-drain-output"
		case "unregister":
			inputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-unregister-input"
			outputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-unregister-output"
		default:
			return nil, fmt.Errorf("proxy drain/unregister case %q has unsupported action %q", caseID, action)
		}

		outputTarget := target
		if override := asObject(caseInput["output_target_override"]); override != nil {
			outputTarget = override
		}
		request := map[string]any{"target": target}
		result := map[string]any{
			"target":             outputTarget,
			"request_outcome":    stringValue(caseInput, "request_outcome"),
			"registration_state": stringValue(caseInput, "registration_state"),
			"routability":        stringValue(caseInput, "routability"),
			"observed_at":        stringValue(input, "observed_at"),
		}
		if err := pkg.validateSchema(inputSchemaID, request); err != nil {
			return nil, fmt.Errorf("case %q input does not match schema: %w", caseID, err)
		}
		expectedSchemaValid := true
		if expected, ok := caseInput["expected_schema_valid"].(bool); ok {
			expectedSchemaValid = expected
		}
		schemaErr := pkg.validateSchema(outputSchemaID, result)
		schemaValid := schemaErr == nil
		if schemaValid != expectedSchemaValid {
			if schemaErr != nil {
				return nil, fmt.Errorf("case %q expected a schema-valid output: %w", caseID, schemaErr)
			}
			return nil, fmt.Errorf("case %q expected its output to be rejected by schema validation", caseID)
		}

		targetBound := reflect.DeepEqual(request["target"], result["target"])
		expectedRegistrationState := "draining"
		if action == "unregister" {
			expectedRegistrationState = "unregistered"
		}
		completionConfirmed := schemaValid && targetBound &&
			stringValue(result, "request_outcome") == "confirmed" &&
			stringValue(result, "registration_state") == expectedRegistrationState &&
			stringValue(result, "routability") == "not_routable"

		caseResults[caseID] = map[string]any{
			"schema_valid":                schemaValid,
			"target_bound":                targetBound,
			"completion_confirmed":        completionConfirmed,
			"retry_requires_inspection":   !completionConfirmed,
			"observed_registration_state": stringValue(result, "registration_state"),
			"observed_routability":        stringValue(result, "routability"),
		}
	}

	return map[string]any{
		"schema_expectations_met": true,
		"case_results":            caseResults,
	}, nil
}

func init() {
	registerEvaluator(evaluateProxyBackendDrainUnregisterState, "integration.proxy.backend.drain-unregister-state-confirmation")
}
