package main

import "fmt"

type operationTypeBindingSummary struct {
	schemasValid                            bool
	snapshotMismatchDetected                bool
	resultMismatchDetected                  bool
	missingResultTypeDetected               bool
	matchingPartialFailureKept              bool
	matchingPartialCancellationKept         bool
	matchingPartialWithoutOperationDataKept bool
	evaluations                             []any
}

func evaluateOperationTypeBinding(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	operations := arrayValue(asObject(fixture["input"]), "operations")
	if len(operations) == 0 {
		return nil, fmt.Errorf("operation type-binding fixture must include operations")
	}

	summary := operationTypeBindingSummary{
		schemasValid: true,
		evaluations:  make([]any, 0, len(operations)),
	}
	for index, rawOperation := range operations {
		operation := asObject(rawOperation)
		if operation == nil {
			return nil, fmt.Errorf("operation %d must be an object", index)
		}
		summary.addOperation(pkg, operation)
	}
	return summary.output(), nil
}

func (s *operationTypeBindingSummary) addOperation(pkg *protocolPackage, operation map[string]any) {
	schemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation) == nil
	s.schemasValid = s.schemasValid && schemaValid

	operationType := stringValue(operation, "operation_type")
	requestSnapshot := asObject(operation["request_snapshot"])
	snapshotMatches := stringValue(requestSnapshot, "operation_type") == operationType
	s.snapshotMismatchDetected = s.snapshotMismatchDetected || !snapshotMatches

	result := asObject(operation["result"])
	if result == nil {
		return
	}
	resultType := stringValue(result, "operation_type")
	resultMatches := resultType == operationType
	bindingsMatch := snapshotMatches && resultMatches
	partialAccepted := schemaValid && bindingsMatch && boolValue(result, "partial")
	s.evaluations = append(s.evaluations, map[string]any{
		"operation_id":            stringValue(operation, "operation_id"),
		"schema_valid":            schemaValid,
		"type_bindings_match":     bindingsMatch,
		"partial_result_accepted": partialAccepted,
	})
	s.missingResultTypeDetected = s.missingResultTypeDetected || resultType == ""
	s.resultMismatchDetected = s.resultMismatchDetected || !resultMatches

	if partialAccepted {
		s.addAcceptedPartialResult(operation, result)
	}
}

func (s *operationTypeBindingSummary) addAcceptedPartialResult(operation, result map[string]any) {
	status := stringValue(operation, "status")
	s.matchingPartialFailureKept = s.matchingPartialFailureKept || status == "failed"
	s.matchingPartialCancellationKept = s.matchingPartialCancellationKept || status == "cancelled"
	s.matchingPartialWithoutOperationDataKept = s.matchingPartialWithoutOperationDataKept || result["operation_data"] == nil
}

func (s *operationTypeBindingSummary) output() map[string]any {
	return map[string]any{
		"schemas_valid":                                s.schemasValid,
		"request_snapshot_types_match":                 !s.snapshotMismatchDetected,
		"result_types_match":                           !s.resultMismatchDetected,
		"snapshot_mismatch_detected":                   s.snapshotMismatchDetected,
		"result_mismatch_detected":                     s.resultMismatchDetected,
		"missing_result_type_detected":                 s.missingResultTypeDetected,
		"matching_partial_failure_kept":                s.matchingPartialFailureKept,
		"matching_partial_cancellation_kept":           s.matchingPartialCancellationKept,
		"matching_partial_without_operation_data_kept": s.matchingPartialWithoutOperationDataKept,
		"all_operation_type_bindings_match":            !s.snapshotMismatchDetected && !s.resultMismatchDetected,
		"evaluations":                                  s.evaluations,
	}
}

func init() {
	registerEvaluator(evaluateOperationTypeBinding, "operations.type-binding")
}
