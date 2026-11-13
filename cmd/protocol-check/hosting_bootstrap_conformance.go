package main

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// Register the hosting provisioning and bootstrap evaluators defined in this file.
func init() {
	registerEvaluator(evaluateHostingProvisionValidStandardOs, "hosting.provision.valid.standard-os")
	registerEvaluator(evaluateHostingBootstrapExecutionBindingAndReadinessGate, "hosting.bootstrap.execution-binding-and-readiness-gate")
	registerEvaluator(evaluateHostingBootstrapRejectsFalseSuccess, "hosting.bootstrap.rejects-false-success")
}

// evaluateHostingProvisionValidStandardOs evaluates the hosting.provision.valid.standard-os conformance fixture.
func evaluateHostingProvisionValidStandardOs(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	if err := pkg.validateSchema(provisionSchemaID, input); err != nil {
		return nil, err
	}
	return map[string]any{"schema_valid": true, "side_effect_permitted": false}, nil
}

// evaluateHostingBootstrapExecutionBindingAndReadinessGate evaluates the hosting.bootstrap.execution-binding-and-readiness-gate conformance fixture.
func evaluateHostingBootstrapExecutionBindingAndReadinessGate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "cases")))
	for _, rawCase := range arrayValue(fixture, "cases") {
		item := asObject(rawCase)
		deliverInput := asObject(item["deliver_input"])
		output := asObject(item["execution_output"])
		inspectInput := asObject(item["inspect_input"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-deliver-input", deliverInput); err != nil {
			return nil, fmt.Errorf("case %q delivery input does not match schema: %w", stringValue(item, "case_id"), err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-execution-output", output); err != nil {
			return nil, fmt.Errorf("case %q execution output does not match schema: %w", stringValue(item, "case_id"), err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-inspect-input", inspectInput); err != nil {
			return nil, fmt.Errorf("case %q inspection input does not match schema: %w", stringValue(item, "case_id"), err)
		}
		bootstrap := asObject(deliverInput["bootstrap"])
		instanceBound := reflect.DeepEqual(deliverInput["instance_ref"], output["instance_ref"]) &&
			reflect.DeepEqual(deliverInput["instance_ref"], inspectInput["instance_ref"])
		revisionBound := bootstrap["revision"] == output["revision"] && bootstrap["digest"] == output["digest"]
		executionBound := reflect.DeepEqual(output["bootstrap_execution_ref"], inspectInput["bootstrap_execution_ref"])
		results = append(results, map[string]any{
			"case_id":                    stringValue(item, "case_id"),
			"instance_bound":             instanceBound,
			"revision_and_digest_bound":  revisionBound,
			"execution_reference_bound":  executionBound,
			"bootstrap_completed":        output["state"] == "succeeded" && output["exit_code"] == json.Number("0"),
			"ready_transition_permitted": false,
		})
	}
	return map[string]any{"cases": results, "bootstrap_completion_alone_never_permits_ready": true}, nil
}

// evaluateHostingBootstrapRejectsFalseSuccess evaluates the hosting.bootstrap.rejects-false-success conformance fixture.
func evaluateHostingBootstrapRejectsFalseSuccess(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	output := asObject(fixture["execution_output"])
	err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:bootstrap-execution-output", output)
	return map[string]any{"accepted": err == nil}, nil
}
