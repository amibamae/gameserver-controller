package main

import "fmt"

func evaluateOperationTypeBinding(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	operations := arrayValue(asObject(fixture["input"]), "operations")
	if len(operations) == 0 {
		return nil, fmt.Errorf("operation type-binding fixture must include operations")
	}

	snapshotTypesMatch := true
	resultTypesMatch := true
	snapshotMismatchDetected := false
	resultMismatchDetected := false
	matchingPartialFailureKept := false
	matchingPartialCancellationKept := false
	for index, rawOperation := range operations {
		operation := asObject(rawOperation)
		if operation == nil {
			return nil, fmt.Errorf("operation %d must be an object", index)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
			return nil, fmt.Errorf("operation %d does not match schema: %w", index, err)
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
		if stringValue(result, "operation_type") != operationType {
			resultTypesMatch = false
			resultMismatchDetected = true
			continue
		}
		if boolValue(result, "partial") && stringValue(operation, "status") == "failed" {
			matchingPartialFailureKept = true
		}
		if boolValue(result, "partial") && stringValue(operation, "status") == "cancelled" {
			matchingPartialCancellationKept = true
		}
	}

	return map[string]any{
		"schemas_valid":                      true,
		"request_snapshot_types_match":       snapshotTypesMatch,
		"result_types_match":                 resultTypesMatch,
		"snapshot_mismatch_detected":         snapshotMismatchDetected,
		"result_mismatch_detected":           resultMismatchDetected,
		"matching_partial_failure_kept":      matchingPartialFailureKept,
		"matching_partial_cancellation_kept": matchingPartialCancellationKept,
		"all_operation_type_bindings_match":  snapshotTypesMatch && resultTypesMatch,
	}, nil
}
