package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/gowebpki/jcs"
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
		configurationDigestMatches := false
		if requestValid {
			expectedDigest, err := requestedConfigurationDigest(request)
			if err != nil {
				return nil, fmt.Errorf("configuration request %q digest: %w", stringValue(configurationCase, "case_id"), err)
			}
			configurationDigestMatches = reflect.DeepEqual(expectedDigest, output["configuration_digest"])
		}
		dispatchAllowed := requestValid && targetConsistent && releaseBound && pinMatches
		resultValid := outputValid && targetBound && validationBeforeApplication && configurationDigestMatches
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
			"configuration_digest_matches":  configurationDigestMatches,
			"configuration_confirmed":       confirmed,
			"must_reconcile_before_retry":   dispatchAllowed && (!resultValid || outcome == "outcome_unknown"),
		})
	}
	return map[string]any{"evaluations": results}, nil
}

func requestedConfigurationDigest(request map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(map[string]any{
		"configuration":   request["configuration"],
		"secret_bindings": request["secret_bindings"],
	})
	if err != nil {
		return nil, err
	}
	canonical, err := jcs.Transform(encoded)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(canonical)
	return map[string]any{
		"algorithm": "sha256",
		"digest":    hex.EncodeToString(digest[:]),
	}, nil
}
