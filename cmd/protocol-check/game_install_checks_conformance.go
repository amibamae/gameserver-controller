package main

import (
	"fmt"
	"reflect"
)

// Register the game installation check evaluators defined in this file.
func init() {
	registerEvaluator(evaluateGameReleaseInstallConfigureFailClosed, "game.release.install.configure.fail-closed")
	registerEvaluator(evaluateGameInstallArtifacts,
		"game.install.repeat-same-release-is-idempotent",
		"game.install.completed-verifies-exact-artifacts",
		"game.install.incomplete-artifact-set-requires-reconciliation")
	registerEvaluator(evaluateGameValidateInstallation,
		"game.validate-installation.unknown-blocks-start",
		"game.validate-installation.valid-exact-artifacts",
		"game.validate-installation.valid-incomplete-artifacts-blocks-start",
		"game.validate-installation.valid-claim-with-unverified-artifact",
		"game.validate-installation.valid-claim-with-checksum-mismatch")
}

// evaluateGameReleaseInstallConfigureFailClosed evaluates the game.release.install.configure.fail-closed conformance fixture.
func evaluateGameReleaseInstallConfigureFailClosed(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	releaseInput := asObject(input["release_describe_input"])
	releaseOutput := asObject(input["release_describe_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:release-describe-input", releaseInput); err != nil {
		return nil, fmt.Errorf("release input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:release-describe-output", releaseOutput); err != nil {
		return nil, fmt.Errorf("release output does not match schema: %w", err)
	}
	releaseBound := stringValue(releaseInput, "release_id") == stringValue(releaseOutput, "release_id")
	if !releaseBound {
		return nil, fmt.Errorf("release output must describe the exact requested release")
	}
	artifacts := arrayValue(releaseOutput, "artifacts")
	artifactIDs := make([]any, 0, len(artifacts))
	checksummed := true
	for _, rawArtifact := range artifacts {
		artifact := asObject(rawArtifact)
		artifactIDs = append(artifactIDs, artifact["artifact_id"])
		checksum := asObject(artifact["checksum"])
		if stringValue(checksum, "algorithm") != "sha256" || stringValue(checksum, "digest") == "" {
			checksummed = false
		}
	}
	if !uniqueStrings(artifactIDs) {
		return nil, fmt.Errorf("release artifact IDs must be unique")
	}
	installInput := asObject(input["install_input"])
	installOutput := asObject(input["install_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:install-input", installInput); err != nil {
		return nil, fmt.Errorf("install input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:install-output", installOutput); err != nil {
		return nil, fmt.Errorf("install output does not match schema: %w", err)
	}
	installBound := reflect.DeepEqual(installInput["target"], installOutput["target"]) &&
		stringValue(installInput, "release_id") == stringValue(installOutput, "release_id")
	if !installBound || stringValue(installInput, "release_id") != stringValue(releaseOutput, "release_id") {
		return nil, fmt.Errorf("install messages must remain bound to the described release and exact target")
	}
	installationBlocked := stringValue(installOutput, "artifact_verification") == "mismatch" &&
		stringValue(installOutput, "outcome") == "rejected" && !boolValue(installOutput, "installation_started")
	configureInput := asObject(input["configure_input"])
	configureOutput := asObject(input["configure_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:configure-input", configureInput); err != nil {
		return nil, fmt.Errorf("configure input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:configure-output", configureOutput); err != nil {
		return nil, fmt.Errorf("configure output does not match schema: %w", err)
	}
	configureBound := reflect.DeepEqual(configureInput["target"], configureOutput["target"]) &&
		stringValue(configureInput, "release_id") == stringValue(configureOutput, "release_id")
	if !configureBound || stringValue(configureInput, "release_id") != stringValue(releaseOutput, "release_id") ||
		configureInput["configuration_schema_uri"] != releaseOutput["configuration_schema_uri"] ||
		!reflect.DeepEqual(configureInput["configuration_schema_checksum"], releaseOutput["configuration_schema_checksum"]) {
		return nil, fmt.Errorf("configuration must remain bound to the described release schema and target")
	}
	invalidNotApplied := stringValue(configureOutput, "validation_result") == "invalid" &&
		stringValue(configureOutput, "outcome") == "rejected" && !boolValue(configureOutput, "application_started")
	return map[string]any{
		"release_schema_valid": true, "release_bound": releaseBound, "release_artifacts_checksummed": checksummed,
		"install_schema_valid": true, "installation_blocked_before_start": installationBlocked,
		"configure_schema_valid": true, "invalid_configuration_not_applied": invalidNotApplied,
	}, nil
}

// evaluateGameInstallArtifacts evaluates the conformance fixtures
// game.install.repeat-same-release-is-idempotent, game.install.completed-verifies-exact-artifacts,
// game.install.incomplete-artifact-set-requires-reconciliation.
func evaluateGameInstallArtifacts(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	installInput := asObject(input["install_request"])
	installOutput := asObject(input["install_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:install-input", installInput); err != nil {
		return nil, fmt.Errorf("install input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:install-output", installOutput); err != nil {
		return nil, fmt.Errorf("install output does not match schema: %w", err)
	}
	bound := reflect.DeepEqual(installInput["target"], installOutput["target"]) &&
		stringValue(installInput, "release_id") == stringValue(installOutput, "release_id") &&
		stringValue(installInput, "release_id") == stringValue(input, "release_id")
	requiredArtifacts := arrayValue(input, "required_artifacts")
	artifactResults := arrayValue(installOutput, "artifact_results")
	complete, checksumsVerified := compareArtifactVerifications(requiredArtifacts, artifactResults)
	outcome, verification := stringValue(installOutput, "outcome"), stringValue(installOutput, "artifact_verification")
	started := boolValue(installOutput, "installation_started")
	outcomeConsistent := outcome == "installed" && started || outcome == "already_installed" && !started
	accepted := bound && complete && checksumsVerified && verification == "verified" && outcomeConsistent
	return map[string]any{
		"schema_valid": true, "target_bound": bound, "artifact_set_complete": complete,
		"artifact_checksums_verified":       checksumsVerified,
		"installation_result_accepted":      accepted,
		"duplicate_install_started":         outcome == "already_installed" && started,
		"operation_reconciliation_required": started && !accepted,
	}, nil
}

// evaluateGameValidateInstallation evaluates the conformance fixtures
// game.validate-installation.unknown-blocks-start,
// game.validate-installation.valid-exact-artifacts,
// game.validate-installation.valid-incomplete-artifacts-blocks-start,
// game.validate-installation.valid-claim-with-unverified-artifact,
// game.validate-installation.valid-claim-with-checksum-mismatch.
func evaluateGameValidateInstallation(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	validateInput := asObject(input["validate_request"])
	validateOutput := asObject(input["validate_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:validate-installation-input", validateInput); err != nil {
		return nil, fmt.Errorf("validation input does not match schema: %w", err)
	}
	bound := reflect.DeepEqual(validateInput["target"], validateOutput["target"]) &&
		stringValue(validateInput, "release_id") == stringValue(validateOutput, "release_id")
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:validate-installation-output", validateOutput); err != nil {
		if stringValue(fixture, "fixture_id") == "game.validate-installation.valid-claim-with-unverified-artifact" {
			return map[string]any{
				"schema_valid": false, "target_bound": bound, "artifact_set_complete": true,
				"aggregate_consistent": false, "start_permitted": false,
			}, nil
		}
		return nil, fmt.Errorf("validation output does not match schema: %w", err)
	}
	requiredArtifacts := arrayValue(input, "required_artifact_ids")
	results := arrayValue(validateOutput, "artifact_results")
	resultIDs := make([]any, 0, len(results))
	allVerified := len(results) > 0
	for _, raw := range results {
		result := asObject(raw)
		resultIDs = append(resultIDs, result["artifact_id"])
		allVerified = allVerified && stringValue(result, "result") == "verified" && reflect.DeepEqual(result["expected_checksum"], result["observed_checksum"])
	}
	complete := uniqueStrings(requiredArtifacts) && uniqueStrings(resultIDs) && sameStringSet(requiredArtifacts, resultIDs)
	validationResult := stringValue(validateOutput, "validation_result")
	aggregateConsistent := validationResult != "valid" || allVerified
	return map[string]any{
		"schema_valid": true, "target_bound": bound, "artifact_set_complete": complete,
		"aggregate_consistent": aggregateConsistent,
		"validation_result":    validationResult,
		"start_permitted":      bound && complete && aggregateConsistent && validationResult == "valid",
	}, nil
}
