package main

import (
	"fmt"
	"reflect"
)

// Register the idempotency evaluators defined in this file.
func init() {
	registerEvaluator(withoutPackage(evaluateOperationsIdempotencySameKeySameRequest), "operations.idempotency.same-key-same-request")
	registerEvaluator(withoutPackage(evaluateOperationsIdempotencySameKeyDifferentRequest), "operations.idempotency.same-key-different-request")
	registerEvaluator(withoutPackage(evaluateOperationsIdempotencyExpiredKeyReuse), "operations.idempotency.expired-key-reuse")
}

// evaluateOperationsIdempotencySameKeySameRequest evaluates the operations.idempotency.same-key-same-request conformance fixture.
func evaluateOperationsIdempotencySameKeySameRequest(fixture map[string]any) (map[string]any, error) {
	first, second := asObject(fixture["first_request"]), asObject(fixture["second_request"])
	sameBinding := first["actor_id"] == second["actor_id"] && first["idempotency_key"] == second["idempotency_key"]
	reused := sameBinding && reflect.DeepEqual(first, second)
	operationID := any(nil)
	operationCount, dispatchCount := 2, 2
	if reused {
		operationID = fixture["existing_operation_id"]
		operationCount, dispatchCount = 1, 1
	}
	return map[string]any{
		"same_operation_reused":      reused,
		"operation_id":               operationID,
		"operation_count":            operationCount,
		"side_effect_dispatch_count": dispatchCount,
	}, nil
}

// evaluateOperationsIdempotencySameKeyDifferentRequest evaluates the operations.idempotency.same-key-different-request conformance fixture.
func evaluateOperationsIdempotencySameKeyDifferentRequest(fixture map[string]any) (map[string]any, error) {
	first, second := asObject(fixture["first_request"]), asObject(fixture["second_request"])
	sameBinding := first["actor_id"] == second["actor_id"] && first["idempotency_key"] == second["idempotency_key"]
	conflict := sameBinding && !reflect.DeepEqual(first, second)
	code := any(nil)
	count := 2
	if conflict {
		code, count = "core.operation.idempotency_conflict", 1
	}
	return map[string]any{"second_request_accepted": !conflict, "error_code": code, "operation_count": count}, nil
}

// evaluateOperationsIdempotencyExpiredKeyReuse evaluates the operations.idempotency.expired-key-reuse conformance fixture.
func evaluateOperationsIdempotencyExpiredKeyReuse(fixture map[string]any) (map[string]any, error) {
	tombstone := asObject(fixture["expired_tombstone"])
	request := asObject(fixture["retry_request"])
	if tombstone["tombstone_present"] != true || tombstone["full_operation_record_retained"] != false {
		return nil, fmt.Errorf("expired-key fixture must contain only a tombstone")
	}
	if tombstone["actor_id"] != request["actor_id"] || tombstone["idempotency_key"] != request["idempotency_key"] {
		return nil, fmt.Errorf("retry request must match the expired tombstone scope")
	}
	return map[string]any{
		"second_request_accepted":     false,
		"error_code":                  "core.operation.idempotency_key_expired",
		"new_operation_created":       false,
		"side_effect_dispatch_count":  0,
		"original_operation_replayed": false,
		"tombstone_retained":          true,
	}, nil
}
