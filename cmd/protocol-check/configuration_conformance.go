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
		result, err := evaluateConfigurationCase(pkg, asObject(rawCase), release)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return map[string]any{"evaluations": results}, nil
}

type configurationRequestBinding struct {
	releaseBound     bool
	pinMatches       bool
	targetConsistent bool
}

func bindConfigurationRequest(request, release map[string]any) configurationRequestBinding {
	releaseBound := request["release_id"] == release["release_id"]
	pinMatches := request["configuration_schema_uri"] == release["configuration_schema_uri"] &&
		reflect.DeepEqual(request["configuration_schema_checksum"], release["configuration_schema_checksum"])
	target := asObject(request["target"])
	instance := asObject(target["instance_ref"])
	return configurationRequestBinding{
		releaseBound:     releaseBound,
		pinMatches:       pinMatches,
		targetConsistent: target["environment_id"] == instance["environment_id"],
	}
}

func (b configurationRequestBinding) allowsDispatch(requestValid bool) bool {
	return requestValid && b.targetConsistent && b.releaseBound && b.pinMatches
}

func evaluateConfigurationCase(pkg *protocolPackage, configurationCase, release map[string]any) (map[string]any, error) {
	request := asObject(configurationCase["request"])
	output := asObject(configurationCase["output"])
	requestValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:configure-input", request) == nil
	outputValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:configure-output", output) == nil
	binding := bindConfigurationRequest(request, release)
	targetBound := binding.targetConsistent &&
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
	dispatchAllowed := binding.allowsDispatch(requestValid)
	resultValid := outputValid && targetBound && validationBeforeApplication && configurationDigestMatches
	outcome := stringValue(output, "outcome")
	confirmed, mustReconcile := configurationOutcome(outcome, dispatchAllowed, resultValid, validated)
	return map[string]any{
		"case_id":                       stringValue(configurationCase, "case_id"),
		"request_schema_valid":          requestValid,
		"request_target_consistent":     binding.targetConsistent,
		"schema_pin_matches":            binding.pinMatches,
		"release_bound":                 binding.releaseBound,
		"dispatch_allowed":              dispatchAllowed,
		"result_schema_valid":           outputValid,
		"result_target_bound":           targetBound,
		"validation_before_application": validationBeforeApplication,
		"configuration_digest_matches":  configurationDigestMatches,
		"configuration_confirmed":       confirmed,
		"must_reconcile_before_retry":   mustReconcile,
	}, nil
}

func configurationOutcome(outcome string, dispatchAllowed, resultValid, validated bool) (confirmed, mustReconcile bool) {
	if !dispatchAllowed {
		return false, false
	}
	switch outcome {
	case "applied", "unchanged":
		confirmed = resultValid && validated
	}
	return confirmed, !resultValid || outcome == "outcome_unknown"
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

func init() {
	registerEvaluator(evaluateConfiguration, "game.configure.pinned-schema-and-result-binding")
}
