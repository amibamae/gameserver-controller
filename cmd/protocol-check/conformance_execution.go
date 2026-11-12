package main

import (
	"fmt"
	"reflect"
	"strings"
)

// Register the execution adapter evaluators defined in this file.
func init() {
	registerEvaluator(evaluateExecutionUnitStartStop,
		"execution.unit.start.accepted-not-ready",
		"execution.unit.start.unknown-requires-inspection",
		"execution.unit.start.target-mismatch",
		"execution.unit.graceful-stop.completed-stopped",
		"execution.unit.graceful-stop.unknown-requires-inspection")
	registerEvaluator(evaluateExecutionUnitForceStop,
		"execution.unit.force-stop.approval-required",
		"execution.unit.force-stop.approval-scope-denied",
		"execution.unit.force-stop.completed-stopped",
		"execution.unit.force-stop.accepted-not-stopped",
		"execution.unit.force-stop.target-mismatch",
		"execution.unit.force-stop.unknown-requires-inspection")
	registerEvaluator(evaluateExecutionLogsHealthDeleteSafety, "execution.logs.health.delete.safety")
}

// evaluateExecutionUnitStartStop evaluates the conformance fixtures
// execution.unit.start.accepted-not-ready, execution.unit.start.unknown-requires-inspection,
// execution.unit.start.target-mismatch, execution.unit.graceful-stop.completed-stopped,
// execution.unit.graceful-stop.unknown-requires-inspection.
func evaluateExecutionUnitStartStop(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	fixtureID := stringValue(fixture, "fixture_id")
	input := asObject(fixture["input"])
	executionInput, executionOutput := asObject(input["execution_input"]), asObject(input["execution_output"])
	inputSchemaID, outputSchemaID := "", ""
	if strings.HasPrefix(fixtureID, "execution.unit.start.") {
		inputSchemaID = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-start-input"
		outputSchemaID = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-start-output"
	} else {
		inputSchemaID = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-graceful-stop-input"
		outputSchemaID = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-graceful-stop-output"
	}
	if err := pkg.validateSchema(inputSchemaID, executionInput); err != nil {
		return nil, fmt.Errorf("execution input does not match schema: %w", err)
	}
	if err := pkg.validateSchema(outputSchemaID, executionOutput); err != nil {
		return nil, fmt.Errorf("execution output does not match schema: %w", err)
	}
	unitRef := asObject(executionInput["unit_ref"])
	resultUnitRef := asObject(executionOutput["unit_ref"])
	unitBound := reflect.DeepEqual(unitRef, resultUnitRef)
	outcome := stringValue(executionOutput, "outcome")
	state := stringValue(executionOutput, "state")
	operationRef, hasOperationRef := executionOutput["external_operation_ref"]
	operationEnvironmentMatches := !hasOperationRef ||
		stringValue(asObject(operationRef), "environment_id") == stringValue(unitRef, "environment_id")
	unknown := outcome == "outcome_unknown"
	resultRequiresInspection := !unitBound || unknown
	startSucceeded := strings.HasPrefix(fixtureID, "execution.unit.start.") && unitBound && outcome == "completed" && state == "running"
	return map[string]any{
		"schemas_valid":                     true,
		"unit_bound":                        unitBound,
		"outcome":                           outcome,
		"state":                             state,
		"external_operation_ref_present":    hasOperationRef,
		"operation_ref_environment_matches": operationEnvironmentMatches,
		"readiness_established":             false,
		"reconciliation_required":           resultRequiresInspection,
		"result_requires_inspection":        resultRequiresInspection,
		"retry_without_inspection":          false,
		"successful_start_claim_accepted":   startSucceeded,
		"termination_confirmed":             unitBound && outcome == "completed" && state == "stopped",
	}, nil
}

// evaluateExecutionUnitForceStop evaluates the conformance fixtures
// execution.unit.force-stop.approval-required, execution.unit.force-stop.approval-scope-denied,
// execution.unit.force-stop.completed-stopped, execution.unit.force-stop.accepted-not-stopped,
// execution.unit.force-stop.target-mismatch, execution.unit.force-stop.unknown-requires-inspection.
func evaluateExecutionUnitForceStop(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	forceStopInput := asObject(input["force_stop_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-force-stop-input", forceStopInput); err != nil {
		return map[string]any{"schemas_valid": false, "approval_required": forceStopInput["approval_id"] == nil, "dispatch_allowed": false, "termination_confirmed": false}, nil
	}
	preconditions := asObject(fixture["core_preconditions"])
	approvalValid := boolValue(preconditions, "approval_valid")
	dispatchAllowed := approvalValid
	output := asObject(input["force_stop_output"])
	outputPresent := len(output) > 0
	targetBound, terminationConfirmed, acceptedNotComplete, mustInspect := false, false, false, false
	if outputPresent {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-force-stop-output", output); err != nil {
			return nil, fmt.Errorf("force-stop output does not match schema: %w", err)
		}
		targetBound = reflect.DeepEqual(asObject(forceStopInput["unit_ref"]), asObject(output["unit_ref"]))
		outcome := stringValue(output, "outcome")
		state := stringValue(output, "state")
		terminationConfirmed = dispatchAllowed && targetBound && outcome == "completed" && state == "stopped"
		acceptedNotComplete = outcome == "accepted" && !terminationConfirmed
		mustInspect = !targetBound || outcome == "outcome_unknown"
	}
	return map[string]any{
		"schemas_valid":             true,
		"approval_required":         true,
		"dispatch_allowed":          dispatchAllowed,
		"target_bound":              targetBound,
		"termination_confirmed":     terminationConfirmed,
		"accepted_not_complete":     acceptedNotComplete,
		"must_inspect_before_retry": mustInspect,
	}, nil
}

// evaluateExecutionLogsHealthDeleteSafety evaluates the execution.logs.health.delete.safety conformance fixture.
func evaluateExecutionLogsHealthDeleteSafety(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	logsInput, logsOutput := asObject(input["logs_input"]), asObject(input["logs_output"])
	healthInput, healthOutput := asObject(input["health_input"]), asObject(input["health_output"])
	deleteInput, deleteOutput := asObject(input["delete_input"]), asObject(input["delete_output"])
	for schemaID, message := range map[string]struct {
		value map[string]any
		name  string
	}{
		"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-input":       {logsInput, "logs input"},
		"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-output":      {logsOutput, "logs output"},
		"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-input":  {healthInput, "health input"},
		"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-output": {healthOutput, "health output"},
		"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-delete-input":          {deleteInput, "delete input"},
		"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-delete-output":         {deleteOutput, "delete output"},
	} {
		if err := pkg.validateSchema(schemaID, message.value); err != nil {
			return nil, fmt.Errorf("%s does not match schema: %w", message.name, err)
		}
	}
	logsBound := reflect.DeepEqual(logsInput["unit_ref"], logsOutput["unit_ref"])
	healthBound := reflect.DeepEqual(healthInput["unit_ref"], healthOutput["unit_ref"])
	deleteBound := reflect.DeepEqual(deleteInput["unit_ref"], deleteOutput["unit_ref"])
	maxBytes, err := integerValue(logsInput["max_bytes"])
	if err != nil {
		return nil, err
	}
	var totalBytes int64
	for i, rawLine := range arrayValue(logsOutput, "lines") {
		line, ok := rawLine.(string)
		if !ok {
			return nil, fmt.Errorf("log lines must be strings")
		}
		totalBytes += int64(len([]byte(line)))
		if i > 0 {
			totalBytes++
		}
	}
	return map[string]any{
		"schemas_valid": true, "unit_targets_bound": logsBound && healthBound && deleteBound,
		"logs_within_bound": totalBytes <= maxBytes, "logs_redacted": boolValue(logsOutput, "redaction_applied"),
		"unknown_health_not_healthy": stringValue(healthOutput, "health") == "unknown",
		"persistent_data_preserved":  boolValue(deleteOutput, "persistent_data_preserved"),
	}, nil
}
