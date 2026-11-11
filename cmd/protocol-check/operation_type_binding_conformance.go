package main

import "fmt"

func evaluateOperationTypeBinding(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	operations := arrayValue(asObject(fixture["input"]), "operations")
	if len(operations) == 0 {
		return nil, fmt.Errorf("operation type-binding fixture must include operations")
	}

	evaluations := make([]any, 0, len(operations))
	schemasValid := true
	snapshotTypesMatch := true
	resultTypesMatch := true
	snapshotMismatchDetected := false
	resultMismatchDetected := false
	missingResultTypeDetected := false
	matchingPartialFailureKept := false
	matchingPartialCancellationKept := false
	matchingPartialWithoutOperationDataKept := false
	for index, rawOperation := range operations {
		operation := asObject(rawOperation)
		if operation == nil {
			return nil, fmt.Errorf("operation %d must be an object", index)
		}
		schemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation) == nil
		if !schemaValid {
			schemasValid = false
		}

		operationType := stringValue(operation, "operation_type")
		requestSnapshot := asObject(operation["request_snapshot"])
		if stringValue(requestSnapshot, "operation_type") != operationType {
			snapshotTypesMatch = false
			snapshotMismatchDetected = true
		}

		result := asObject(operation["result"])
		if result == nil {
			continue
		}
		resultType := stringValue(result, "operation_type")
		bindingsMatch := stringValue(requestSnapshot, "operation_type") == operationType && resultType == operationType
		partialAccepted := schemaValid && bindingsMatch && boolValue(result, "partial")
		evaluation := map[string]any{}
		evaluation["operation_id"] = stringValue(operation, "operation_id")
		evaluation["schema_valid"] = schemaValid
		evaluation["type_bindings_match"] = bindingsMatch
		evaluation["partial_result_accepted"] = partialAccepted
		evaluations = append(evaluations, evaluation)
		if resultType == "" {
			missingResultTypeDetected = true
		}
		if resultType != operationType {
			resultTypesMatch = false
			resultMismatchDetected = true
			continue
		}
		if partialAccepted && stringValue(operation, "status") == "failed" {
			matchingPartialFailureKept = true
		}
		if partialAccepted && stringValue(operation, "status") == "cancelled" {
			matchingPartialCancellationKept = true
		}
		if partialAccepted && result["operation_data"] == nil {
			matchingPartialWithoutOperationDataKept = true
		}
	}

	result := map[string]any{
		"schemas_valid":                                schemasValid,
		"request_snapshot_types_match":                 snapshotTypesMatch,
		"result_types_match":                           resultTypesMatch,
		"snapshot_mismatch_detected":                   snapshotMismatchDetected,
		"result_mismatch_detected":                     resultMismatchDetected,
		"missing_result_type_detected":                 missingResultTypeDetected,
		"matching_partial_failure_kept":                matchingPartialFailureKept,
		"matching_partial_cancellation_kept":           matchingPartialCancellationKept,
		"matching_partial_without_operation_data_kept": matchingPartialWithoutOperationDataKept,
		"all_operation_type_bindings_match":            snapshotTypesMatch && resultTypesMatch,
	}
	result["evaluations"] = evaluations
	return result, nil
}
