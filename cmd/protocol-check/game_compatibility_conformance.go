package main

import (
	"fmt"
)

// Register the game compatibility evaluators defined in this file.
func init() {
	registerEvaluator(evaluateGameCompatibilityCheck,
		"game.compatibility.check.compatible",
		"game.compatibility.check.migration-required",
		"game.compatibility.check.incompatible",
		"game.compatibility.check.unknown",
		"game.compatibility.check.incomplete",
		"game.compatibility.check.unknown-reported-compatible",
		"game.compatibility.check.target-mismatch")
}

// evaluateGameCompatibilityCheck evaluates the conformance fixtures
// game.compatibility.check.compatible, game.compatibility.check.migration-required,
// game.compatibility.check.incompatible, game.compatibility.check.unknown,
// game.compatibility.check.incomplete, game.compatibility.check.unknown-reported-compatible,
// game.compatibility.check.target-mismatch.
func evaluateGameCompatibilityCheck(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	compatibilityInput, compatibilityOutput := asObject(input["compatibility_input"]), asObject(input["compatibility_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:compatibility-check-input", compatibilityInput); err != nil {
		return nil, fmt.Errorf("compatibility input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:compatibility-check-output", compatibilityOutput); err != nil {
		return nil, fmt.Errorf("compatibility output does not match schema: %w", err)
	}
	manifest := asObject(compatibilityInput["source_manifest"])
	targetBound := compatibilityNames(compatibilityOutput, manifest, asObject(manifest["game"]), asObject(compatibilityInput["target_release"]))
	complete, derived := deriveCompatibility(arrayValue(compatibilityInput, "required_check_ids"), arrayValue(compatibilityOutput, "check_results"))
	reportedMatches := stringValue(compatibilityOutput, "compatibility_status") == derived
	return map[string]any{
		"schemas_valid":                  true,
		"target_bound":                   targetBound,
		"check_set_complete":             complete,
		"derived_compatibility_status":   derived,
		"reported_compatibility_matches": reportedMatches,
		"compatibility_result_accepted":  targetBound && complete && reportedMatches && derived == "compatible",
	}, nil
}

// compatibilityNames: a compatibility result names the backup (its ID,
// environment and data generation, read from backup), the source game
// adapter and version, and the target release.
func compatibilityNames(compatibility, backup, sourceGame, targetRelease map[string]any) bool {
	return stringValue(compatibility, "backup_id") == stringValue(backup, "backup_id") &&
		stringValue(compatibility, "environment_id") == stringValue(backup, "environment_id") &&
		stringValue(compatibility, "data_generation_id") == stringValue(backup, "data_generation_id") &&
		stringValue(compatibility, "source_game_adapter_id") == stringValue(sourceGame, "adapter_id") &&
		stringValue(compatibility, "source_game_version") == stringValue(sourceGame, "version") &&
		stringValue(compatibility, "target_game_adapter_id") == stringValue(targetRelease, "game_adapter_id") &&
		stringValue(compatibility, "target_release_id") == stringValue(targetRelease, "release_id") &&
		stringValue(compatibility, "target_game_version") == stringValue(targetRelease, "game_version")
}

// deriveCompatibility: the check set is complete when there is exactly one
// known result for every required check and nothing else. Any incompatible
// check makes the result incompatible; otherwise an unknown or missing
// result makes it unknown; otherwise any migration_required check makes it
// migration_required.
func deriveCompatibility(required, results []any) (bool, string) {
	requested := make(map[string]bool)
	for _, rawID := range required {
		requested[rawID.(string)] = true
	}
	observed := make(map[string]string)
	complete := len(results) == len(requested)
	for _, rawResult := range results {
		result := asObject(rawResult)
		checkID := stringValue(result, "check_id")
		if !requested[checkID] {
			complete = false
		}
		if _, exists := observed[checkID]; exists {
			complete = false
		}
		observed[checkID] = stringValue(result, "result")
	}
	hasIncompatible, hasUnknown, hasMigration := false, false, false
	for checkID := range requested {
		result, exists := observed[checkID]
		if !exists {
			complete = false
		}
		switch result {
		case "compatible":
		case "migration_required":
			hasMigration = true
		case "incompatible":
			hasIncompatible = true
		case "unknown":
			hasUnknown = true
		default:
			complete = false
		}
	}
	switch {
	case hasIncompatible:
		return complete, "incompatible"
	case hasUnknown || !complete:
		return complete, "unknown"
	case hasMigration:
		return complete, "migration_required"
	default:
		return complete, "compatible"
	}
}
