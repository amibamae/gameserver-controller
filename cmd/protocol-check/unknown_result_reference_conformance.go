package main

import (
	"fmt"
	"reflect"
)

func evaluateUnknownResultReferenceProjection(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	results := make(map[string]any)
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		result, err := evaluateUnknownResultReferenceCase(pkg, input, evaluation)
		if err != nil {
			return nil, err
		}
		caseID := stringValue(evaluation, "case_id")
		if caseID == "" {
			return nil, fmt.Errorf("unknown-result reference evaluation has no case_id")
		}
		results[caseID] = result
	}
	return map[string]any{"cases": results}, nil
}

func evaluateUnknownResultReferenceCase(pkg *protocolPackage, input, evaluation map[string]any) (map[string]any, error) {
	baseResponse := asObject(input["base_adapter_response"])
	baseOperation := asObject(input["base_operation"])
	response := mergeJSONObjects(baseResponse, asObject(evaluation["adapter_response_overrides"]))
	result := mergeJSONObjects(asObject(response["result"]), nil)
	response["result"] = result
	deleteStringKeys(result, arrayValue(evaluation, "adapter_result_remove"))
	// Capability output removals apply to the output check, after the response is copied.
	output := mergeJSONObjects(asObject(result["output"]), nil)
	deleteStringKeys(output, arrayValue(evaluation, "output_remove"))

	responseSchemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:rpc:adapter-invoke-result", response) == nil
	outputSchemaID := stringValue(input, "capability_output_schema_id")
	if outputSchemaID == "" {
		return nil, fmt.Errorf("unknown-result fixture must name the capability output schema")
	}
	outputSchemaValid := pkg.validateSchema(outputSchemaID, output) == nil
	outcome := stringValue(result, "outcome")
	normalizedReference, referencesConsistent := normalizeUnknownResultReferences(
		result["external_operation_ref"], output["reconciliation_ref"], output["external_operation_ref"],
	)
	trusted := responseSchemaValid && outputSchemaValid && outcome == "outcome_unknown" && referencesConsistent

	operation, step, err := projectUnknownResultOperation(baseOperation, trusted, normalizedReference)
	if err != nil {
		return nil, err
	}
	operationSchemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation) == nil
	stepReferenceMatches := normalizedReference != nil && trusted && reflect.DeepEqual(step["external_operation_ref"], normalizedReference)

	return map[string]any{
		"response_schema_valid":          responseSchemaValid,
		"capability_output_schema_valid": outputSchemaValid,
		"references_consistent":          referencesConsistent,
		"reference_normalized":           stepReferenceMatches,
		"output_persisted":               step["output"] != nil,
		"operation_schema_valid":         operationSchemaValid,
		"operation_status":               stringValue(operation, "status"),
		"operator_review_required":       !trusted,
		"retry_permitted":                false,
	}, nil
}

func normalizeUnknownResultReferences(references ...any) (any, bool) {
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
	return normalizedReference, referencesConsistent
}

func projectUnknownResultOperation(baseOperation map[string]any, trusted bool, normalizedReference any) (map[string]any, map[string]any, error) {
	operation := mergeJSONObjects(baseOperation, nil)
	operation["status"] = "reconciling"
	if !trusted {
		operation["status"] = "needs_operator"
	}
	steps := arrayValue(baseOperation, "steps")
	if len(steps) != 1 {
		return nil, nil, fmt.Errorf("unknown-result fixture must have one base operation step")
	}
	step := mergeJSONObjects(asObject(steps[0]), nil)
	delete(step, "output")
	if trusted && normalizedReference != nil {
		step["external_operation_ref"] = normalizedReference
	} else {
		delete(step, "external_operation_ref")
	}
	operation["steps"] = []any{step}
	return operation, step, nil
}

func init() {
	registerEvaluator(evaluateUnknownResultReferenceProjection, "operations.unknown-result-reference-projection")
}
