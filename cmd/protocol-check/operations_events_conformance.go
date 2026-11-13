package main

import (
	"fmt"
	"reflect"
)

// Register the operation event evaluators defined in this file.
func init() {
	registerEvaluator(evaluateOperationsEventsDuplicateDelivery, "operations.events.duplicate-delivery")
	registerEvaluator(evaluateOperationsEventsGapRecovery, "operations.events.gap-recovery")
	registerEvaluator(evaluateOperationsEventsMonotonicSequence, "operations.events.monotonic-sequence")
}

// evaluateOperationsEventsDuplicateDelivery evaluates the operations.events.duplicate-delivery conformance fixture.
func evaluateOperationsEventsDuplicateDelivery(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	lastSequence, err := integerValue(fixture["last_consumed_sequence"])
	if err != nil {
		return nil, err
	}
	deliveries := arrayValue(fixture, "deliveries")
	if len(deliveries) == 0 {
		return nil, fmt.Errorf("event deliveries must not be empty")
	}
	var operationID string
	seen := make(map[string]map[string]any)
	appliedEventIDs := make([]any, 0, len(deliveries))
	duplicateCount := 0
	for index, rawEvent := range deliveries {
		event := asObject(rawEvent)
		if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
			return nil, fmt.Errorf("delivery %d does not match schema: %w", index, err)
		}
		id := stringValue(event, "operation_id")
		if index == 0 {
			operationID = id
		} else if id != operationID {
			return nil, fmt.Errorf("delivery %d belongs to a different operation", index)
		}
		eventID := stringValue(event, "event_id")
		if previous, exists := seen[eventID]; exists {
			if !reflect.DeepEqual(previous, event) {
				return nil, fmt.Errorf("duplicate event ID %q has different content", eventID)
			}
			duplicateCount++
			continue
		}
		sequence, err := integerValue(event["sequence"])
		if err != nil {
			return nil, err
		}
		if sequence != lastSequence+1 {
			return nil, fmt.Errorf("new event sequence %d does not immediately follow cursor %d", sequence, lastSequence)
		}
		seen[eventID] = event
		appliedEventIDs = append(appliedEventIDs, eventID)
		lastSequence = sequence
	}
	return map[string]any{
		"schema_valid":                 true,
		"applied_event_ids":            appliedEventIDs,
		"applied_event_count":          len(appliedEventIDs),
		"duplicate_deliveries_ignored": duplicateCount,
		"duplicate_event_applications": 0,
		"last_consumed_sequence":       lastSequence,
		"cursor_regressed":             false,
	}, nil
}

// evaluateOperationsEventsGapRecovery evaluates the operations.events.gap-recovery conformance fixture.
func evaluateOperationsEventsGapRecovery(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	lastSequence, err := integerValue(fixture["last_consumed_sequence"])
	if err != nil {
		return nil, err
	}
	gap, err := detectEventGap(pkg, arrayValue(fixture, "events"), lastSequence)
	if err != nil {
		return nil, err
	}
	operation, err := authoritativeOperation(pkg, asObject(fixture["recovery"]), gap.operationID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"event_schema_valid":                  true,
		"operation_schema_valid":              true,
		"gap_detected":                        true,
		"expected_sequence":                   gap.expected,
		"received_sequence":                   gap.received,
		"authoritative_operation_fetched":     true,
		"operation_id":                        gap.operationID,
		"recovered_status":                    operation["status"],
		"recovered_steps":                     operation["steps"],
		"missing_event_details_reconstructed": false,
		"side_effect_dispatches":              0,
		"replayed_events":                     0,
		"resume_after_sequence":               gap.received,
	}, nil
}

// eventGap is the first missing sequence in an event stream.
type eventGap struct {
	operationID        string
	expected, received int64
}

// detectEventGap reads schema-valid events for one operation, each
// advancing past the last consumed sequence, until one skips a sequence.
// A stream with no gap is an error for this fixture.
func detectEventGap(pkg *protocolPackage, events []any, lastSequence int64) (eventGap, error) {
	if len(events) == 0 {
		return eventGap{}, fmt.Errorf("event sequence must not be empty")
	}
	var operationID string
	currentSequence := lastSequence
	for index, rawEvent := range events {
		event := asObject(rawEvent)
		if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
			return eventGap{}, fmt.Errorf("event %d does not match schema: %w", index, err)
		}
		id := stringValue(event, "operation_id")
		if index == 0 {
			operationID = id
		} else if id != operationID {
			return eventGap{}, fmt.Errorf("event %d belongs to a different operation", index)
		}
		sequence, err := integerValue(event["sequence"])
		if err != nil {
			return eventGap{}, err
		}
		if sequence <= currentSequence {
			return eventGap{}, fmt.Errorf("event sequence %d does not advance past %d", sequence, currentSequence)
		}
		if sequence > currentSequence+1 {
			return eventGap{operationID: operationID, expected: currentSequence + 1, received: sequence}, nil
		}
		currentSequence = sequence
	}
	return eventGap{}, fmt.Errorf("gap-recovery fixture must contain a missing event sequence")
}

// authoritativeOperation: after a gap, the client re-reads the operation
// resource. The read must succeed and return a schema-valid snapshot of the
// same operation.
func authoritativeOperation(pkg *protocolPackage, recovery map[string]any, operationID string) (map[string]any, error) {
	operationResponse := asObject(recovery["operation_resource_get"])
	responseStatus, err := integerValue(operationResponse["status"])
	if err != nil {
		return nil, err
	}
	if responseStatus != 200 {
		return nil, fmt.Errorf("gap recovery requires a successful authoritative operation read")
	}
	operation := asObject(operationResponse["operation"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
		return nil, fmt.Errorf("authoritative operation snapshot does not match schema: %w", err)
	}
	if stringValue(operation, "operation_id") != operationID {
		return nil, fmt.Errorf("authoritative operation snapshot belongs to a different operation")
	}
	return operation, nil
}

// evaluateOperationsEventsMonotonicSequence evaluates the operations.events.monotonic-sequence conformance fixture.
func evaluateOperationsEventsMonotonicSequence(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	events := arrayValue(fixture, "events")
	if len(events) == 0 {
		return nil, fmt.Errorf("event sequence must not be empty")
	}
	var operationID string
	oneOperation := true
	sequences := make([]int64, 0, len(events))
	for index, rawEvent := range events {
		event := asObject(rawEvent)
		if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
			return nil, fmt.Errorf("event %d does not match schema: %w", index, err)
		}
		id := stringValue(event, "operation_id")
		if index == 0 {
			operationID = id
		} else if id != operationID {
			oneOperation = false
		}
		sequence, err := integerValue(event["sequence"])
		if err != nil {
			return nil, err
		}
		sequences = append(sequences, sequence)
	}
	increasing := oneOperation
	for i := 1; i < len(sequences); i++ {
		if sequences[i] <= sequences[i-1] {
			increasing = false
		}
	}
	return map[string]any{"schema_valid": true, "sequence_strictly_increasing": increasing}, nil
}
