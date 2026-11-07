package main

import (
	"fmt"
	"reflect"
)

func evaluateConfiguration(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	release := asObject(input["release"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:release-describe-output", release); err != nil {
		return nil, fmt.Errorf("configuration release does not match schema: %w", err)
	}
	results := make([]any, 0, len(arrayValue(fixture, "evaluations")))
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		configurationCase := asObject(rawCase)
		request := asObject(configurationCase["request"])
		output := asObject(configurationCase["output"])
		requestValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:configure-input", request) == nil
		outputValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:configure-output", output) == nil
		releaseBound := request["release_id"] == release["release_id"]
		pinMatches := request["configuration_schema_uri"] == release["configuration_schema_uri"] &&
			reflect.DeepEqual(request["configuration_schema_checksum"], release["configuration_schema_checksum"])
		requestTarget := asObject(request["target"])
		requestInstance := asObject(requestTarget["instance_ref"])
		targetConsistent := requestTarget["environment_id"] == requestInstance["environment_id"]
		targetBound := targetConsistent &&
			reflect.DeepEqual(request["target"], output["target"]) && request["release_id"] == output["release_id"]
		validated := stringValue(output, "validation_result") == "valid"
		validationBeforeApplication := !boolValue(output, "application_started") || validated
		dispatchAllowed := requestValid && targetConsistent && releaseBound && pinMatches
		resultValid := outputValid && targetBound && validationBeforeApplication
		outcome := stringValue(output, "outcome")
		confirmed := dispatchAllowed && resultValid && validated && (outcome == "applied" || outcome == "unchanged")
		results = append(results, map[string]any{
			"case_id":                       stringValue(configurationCase, "case_id"),
			"request_schema_valid":          requestValid,
			"request_target_consistent":     targetConsistent,
			"schema_pin_matches":            pinMatches,
			"release_bound":                 releaseBound,
			"dispatch_allowed":              dispatchAllowed,
			"result_schema_valid":           outputValid,
			"result_target_bound":           targetBound,
			"validation_before_application": validationBeforeApplication,
			"configuration_confirmed":       confirmed,
			"must_reconcile_before_retry":   dispatchAllowed && (!resultValid || outcome == "outcome_unknown"),
		})
	}
	return map[string]any{"evaluations": results}, nil
}
