package main

import (
	"fmt"
	"reflect"
)

func evaluateUnknownResultReferenceProjection(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	baseResponse := asObject(input["base_adapter_response"])
	baseOperation := asObject(input["base_operation"])
	results := make(map[string]any)

	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		caseID := stringValue(evaluation, "case_id")
		response := mergeJSONObjects(baseResponse, asObject(evaluation["adapter_response_overrides"]))
		result := mergeJSONObjects(asObject(response["result"]), nil)
		response["result"] = result
		for _, key := range arrayValue(evaluation, "adapter_result_remove") {
			if stringKey, ok := key.(string); ok {
				delete(result, stringKey)
			}
		}
		output := mergeJSONObjects(asObject(result["output"]), nil)
		for _, key := range arrayValue(evaluation, "output_remove") {
			if stringKey, ok := key.(string); ok {
				delete(output, stringKey)
			}
		}

		responseSchemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:rpc:adapter-invoke-result", response) == nil
		outputSchemaID := stringValue(input, "capability_output_schema_id")
		if outputSchemaID == "" {
			return nil, fmt.Errorf("unknown-result fixture must name the capability output schema")
		}
		outputSchemaValid := pkg.validateSchema(outputSchemaID, output) == nil
		outcome := stringValue(result, "outcome")
		references := []any{result["external_operation_ref"], output["reconciliation_ref"], output["external_operation_ref"]}
		var normalizedReference any
		referencesConsistent := true
		for _, reference := range references {
			if reference == nil {
				continue
			}
			if normalizedReference == nil {
				normalizedReference = reference
				continue
			}
			if !reflect.DeepEqual(normalizedReference, reference) {
				referencesConsistent = false
			}
		}
		trusted := responseSchemaValid && outputSchemaValid && outcome == "outcome_unknown" && referencesConsistent

		operation := mergeJSONObjects(baseOperation, nil)
		operation["status"] = "reconciling"
		if !trusted {
			operation["status"] = "needs_operator"
		}
		steps := arrayValue(baseOperation, "steps")
		if len(steps) != 1 {
			return nil, fmt.Errorf("unknown-result fixture must have one base operation step")
		}
		step := mergeJSONObjects(asObject(steps[0]), nil)
		delete(step, "output")
		if trusted && normalizedReference != nil {
			step["external_operation_ref"] = normalizedReference
		} else {
			delete(step, "external_operation_ref")
		}
		operation["steps"] = []any{step}
		operationSchemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation) == nil
		stepReferenceMatches := normalizedReference != nil && trusted && reflect.DeepEqual(step["external_operation_ref"], normalizedReference)

		if caseID == "" {
			return nil, fmt.Errorf("unknown-result reference evaluation has no case_id")
		}
		results[caseID] = map[string]any{
			"response_schema_valid":          responseSchemaValid,
			"capability_output_schema_valid": outputSchemaValid,
			"references_consistent":          referencesConsistent,
			"reference_normalized":           stepReferenceMatches,
			"output_persisted":               step["output"] != nil,
			"operation_schema_valid":         operationSchemaValid,
			"operation_status":               stringValue(operation, "status"),
			"operator_review_required":       !trusted,
			"retry_permitted":                false,
		}
	}
	return map[string]any{"cases": results}, nil
}
