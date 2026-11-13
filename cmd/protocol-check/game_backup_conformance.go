package main

import (
	"fmt"
	"reflect"
	"strings"
)

// Register the game backup evaluators defined in this file.
func init() {
	registerEvaluator(evaluateGameBackupValidate,
		"game.backup.validate.exact-objects",
		"game.backup.validate.unknown-not-valid")
	registerEvaluator(evaluateGameDataSaveBackupPrepare,
		"game.data.save.backup-prepare.binding",
		"game.data.save.backup-prepare.reject-unsafe-datasets")
	registerEvaluator(evaluateGameSaveUnknownRequiresReconciliation, "game.save.unknown-requires-reconciliation")
}

// evaluateGameBackupValidate evaluates the conformance fixtures game.backup.validate.exact-objects,
// game.backup.validate.unknown-not-valid.
func evaluateGameBackupValidate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	backupInput, backupOutput := asObject(input["backup_input"]), asObject(input["backup_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:backup-validate-input", backupInput); err != nil {
		return nil, fmt.Errorf("backup validation input does not match schema: %w", err)
	}
	requestContext := asObject(input["request_context"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:common:request-context", requestContext); err != nil {
		return nil, fmt.Errorf("backup validation request context does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:backup-validate-output", backupOutput); err != nil {
		return nil, fmt.Errorf("backup validation output does not match schema: %w", err)
	}
	manifest := asObject(backupInput["manifest"])
	manifestBinding := stringValue(backupInput, "backup_id") == stringValue(manifest, "backup_id") &&
		stringValue(backupInput, "environment_id") == stringValue(manifest, "environment_id") &&
		stringValue(backupOutput, "backup_id") == stringValue(manifest, "backup_id") &&
		stringValue(backupOutput, "environment_id") == stringValue(manifest, "environment_id") &&
		stringValue(backupOutput, "data_generation_id") == stringValue(manifest, "data_generation_id")
	streamsBound, err := backupValidateStreamsBound(backupInput, manifest, arrayValue(requestContext, "stream_transfers"))
	if err != nil {
		return nil, err
	}
	complete, derived := deriveBackupValidation(arrayValue(backupInput, "required_check_ids"), arrayValue(backupOutput, "check_results"))
	return map[string]any{
		"schemas_valid":                 true,
		"manifest_binding_valid":        manifestBinding,
		"object_streams_match_manifest": streamsBound,
		"check_set_complete":            complete,
		"derived_validation_result":     derived,
		"backup_valid": manifestBinding && streamsBound && complete && derived == "valid" &&
			stringValue(backupOutput, "validation_result") == "valid",
	}, nil
}

// backupValidateStreamsBound: the object streams are exactly the
// manifest's objects in the backup's environment, each with a
// controller-to-adapter transfer at its position large enough for it.
func backupValidateStreamsBound(backupInput, manifest map[string]any, transfers []any) (bool, error) {
	objects := arrayValue(manifest, "objects")
	manifestRefs := make([]any, 0, len(objects))
	for _, rawObject := range objects {
		manifestRefs = append(manifestRefs, asObject(rawObject)["object_ref"])
	}
	streams := arrayValue(backupInput, "object_streams")
	streamRefs := make([]any, 0, len(streams))
	for _, rawStream := range streams {
		streamRefs = append(streamRefs, asObject(rawStream)["object_ref"])
	}
	bound := sameObjectRefSet(manifestRefs, streamRefs) && len(transfers) == len(streams)
	for i, rawStream := range streams {
		stream := asObject(rawStream)
		if stringValue(asObject(stream["object_ref"]), "environment_id") != stringValue(backupInput, "environment_id") {
			bound = false
		}
		streamBound, err := backupValidateStreamTransfer(objects, stream, transfers, i)
		if err != nil {
			return false, err
		}
		bound = bound && streamBound
	}
	return bound, nil
}

// backupValidateStreamTransfer: the stream names a manifest object, and
// transfer i is controller-to-adapter and at least that object's size.
func backupValidateStreamTransfer(objects []any, stream map[string]any, transfers []any, i int) (bool, error) {
	for _, rawObject := range objects {
		object := asObject(rawObject)
		if !reflect.DeepEqual(object["object_ref"], stream["object_ref"]) {
			continue
		}
		expectedSize, err := integerValue(object["size_bytes"])
		if err != nil {
			return false, err
		}
		if i >= len(transfers) {
			return false, nil
		}
		transfer := asObject(transfers[i])
		maximumBytes, err := integerValue(transfer["maximum_bytes"])
		if err != nil {
			return false, err
		}
		return maximumBytes >= expectedSize && stringValue(transfer, "direction") == "controller_to_adapter", nil
	}
	return false, nil
}

// deriveBackupValidation: the check set is complete when there is exactly
// one known result for every required check and nothing else. Any failure
// makes the backup invalid; otherwise an unknown or missing result makes it
// unknown.
func deriveBackupValidation(required, results []any) (bool, string) {
	requested := make(map[string]bool, len(required))
	for _, rawID := range required {
		requested[rawID.(string)] = true
	}
	observed := make(map[string]string, len(results))
	complete := len(results) == len(requested)
	for _, rawResult := range results {
		item := asObject(rawResult)
		id := stringValue(item, "check_id")
		if !requested[id] {
			complete = false
		}
		if _, exists := observed[id]; exists {
			complete = false
		}
		observed[id] = stringValue(item, "result")
	}
	hasFailure, hasUnknown := false, false
	for id := range requested {
		result, ok := observed[id]
		if !ok {
			complete = false
		}
		switch result {
		case "pass":
		case "fail":
			hasFailure = true
		case "unknown":
			hasUnknown = true
		default:
			complete = false
		}
	}
	switch {
	case hasFailure:
		return complete, "invalid"
	case hasUnknown || !complete:
		return complete, "unknown"
	default:
		return complete, "valid"
	}
}

// evaluateGameDataSaveBackupPrepare evaluates the conformance fixtures
// game.data.save.backup-prepare.binding, game.data.save.backup-prepare.reject-unsafe-datasets.
func evaluateGameDataSaveBackupPrepare(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	dataInput, dataOutput := asObject(input["data_input"]), asObject(input["data_output"])
	saveInput, saveOutput := asObject(input["save_input"]), asObject(input["save_output"])
	prepareInput, prepareOutput := asObject(input["prepare_input"]), asObject(input["prepare_output"])
	for schemaID, message := range map[string]struct {
		value map[string]any
		name  string
	}{
		"urn:gameserver-controller:0.21.0-draft:game:data-describe-input":   {dataInput, "data describe input"},
		"urn:gameserver-controller:0.21.0-draft:game:data-describe-output":  {dataOutput, "data describe output"},
		"urn:gameserver-controller:0.21.0-draft:game:save-request-input":    {saveInput, "save request input"},
		"urn:gameserver-controller:0.21.0-draft:game:save-request-output":   {saveOutput, "save request output"},
		"urn:gameserver-controller:0.21.0-draft:game:backup-prepare-input":  {prepareInput, "backup prepare input"},
		"urn:gameserver-controller:0.21.0-draft:game:backup-prepare-output": {prepareOutput, "backup prepare output"},
	} {
		if err := pkg.validateSchema(schemaID, message.value); err != nil {
			return nil, fmt.Errorf("%s does not match schema: %w", message.name, err)
		}
	}
	dataTargetBound := reflect.DeepEqual(dataInput["target"], dataOutput["target"]) &&
		dataInput["release_id"] == dataOutput["release_id"]
	saveTargetBound := reflect.DeepEqual(saveInput["target"], saveOutput["target"]) &&
		saveInput["release_id"] == saveOutput["release_id"]
	prepareTargetBound := reflect.DeepEqual(prepareInput["target"], prepareOutput["target"]) &&
		prepareInput["release_id"] == prepareOutput["release_id"] &&
		prepareInput["backup_id"] == prepareOutput["backup_id"] &&
		prepareInput["data_generation_id"] == prepareOutput["data_generation_id"] &&
		prepareInput["consistency_mode"] == prepareOutput["consistency_mode"]
	dataSetIDs, supportedModes, pathsSafe := describeDataSets(arrayValue(dataOutput, "data_sets"))
	selectedIDs := arrayValue(prepareInput, "data_set_ids")
	dataSetSelectionBound := uniqueStrings(dataSetIDs) && uniqueStrings(selectedIDs) &&
		sameStringSet(selectedIDs, arrayValue(prepareOutput, "data_set_ids"))
	dataSetsSupportConsistency := dataSetSelectionBound &&
		selectedSetsSupport(selectedIDs, supportedModes, stringValue(prepareInput, "consistency_mode"))
	saveConfirmed := stringValue(saveOutput, "outcome") == "confirmed" && stringValue(saveOutput, "saved_at") != ""
	preparationConfirmed := stringValue(prepareOutput, "outcome") == "prepared" &&
		stringValue(prepareOutput, "preparation_id") != "" && stringValue(prepareOutput, "consistency_evidence") != ""
	return map[string]any{
		"schemas_valid":                  true,
		"data_targets_bound":             dataTargetBound,
		"data_sets_unique_and_path_safe": uniqueStrings(dataSetIDs) && pathsSafe,
		"save_confirmed":                 saveTargetBound && saveConfirmed,
		"backup_preparation_bound":       prepareTargetBound && dataSetSelectionBound,
		"data_sets_support_consistency":  dataSetsSupportConsistency,
		"preparation_confirmed":          preparationConfirmed,
	}, nil
}

// describeDataSets returns the data set IDs in order, each set's supported
// consistency modes, and whether every relative path is safe.
func describeDataSets(dataSets []any) ([]any, map[string]map[string]bool, bool) {
	dataSetIDs := make([]any, 0, len(dataSets))
	supportedModes := make(map[string]map[string]bool, len(dataSets))
	pathsSafe := true
	for _, rawSet := range dataSets {
		dataSet := asObject(rawSet)
		setID := stringValue(dataSet, "data_set_id")
		dataSetIDs = append(dataSetIDs, setID)
		supportedModes[setID] = make(map[string]bool)
		for _, rawMode := range arrayValue(dataSet, "supported_consistency_modes") {
			if mode, ok := rawMode.(string); ok {
				supportedModes[setID][mode] = true
			}
		}
		for _, rawPath := range arrayValue(dataSet, "relative_paths") {
			if !safeRelativePath(rawPath) {
				pathsSafe = false
			}
		}
	}
	return dataSetIDs, supportedModes, pathsSafe
}

// safeRelativePath: a non-empty, slash-separated relative path with no
// backslash and no empty, "." or ".." segment.
func safeRelativePath(rawPath any) bool {
	pathValue, ok := rawPath.(string)
	if !ok || pathValue == "" || strings.HasPrefix(pathValue, "/") || strings.Contains(pathValue, "\\") {
		return false
	}
	for _, segment := range strings.Split(pathValue, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// selectedSetsSupport: every selected data set supports the requested
// consistency mode.
func selectedSetsSupport(selectedIDs []any, supportedModes map[string]map[string]bool, mode string) bool {
	supported := true
	for _, rawID := range selectedIDs {
		id, ok := rawID.(string)
		if !ok || !supportedModes[id][mode] {
			supported = false
		}
	}
	return supported
}

// evaluateGameSaveUnknownRequiresReconciliation evaluates the game.save.unknown-requires-reconciliation conformance fixture.
func evaluateGameSaveUnknownRequiresReconciliation(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	saveInput, saveOutput := asObject(input["save_input"]), asObject(input["save_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:save-request-input", saveInput); err != nil {
		return nil, fmt.Errorf("save input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:save-request-output", saveOutput); err != nil {
		return nil, fmt.Errorf("save output does not match schema: %w", err)
	}
	unknown := stringValue(saveOutput, "outcome") == "outcome_unknown"
	hasReference := asObject(saveOutput["reconciliation_ref"]) != nil
	targetBound := reflect.DeepEqual(saveInput["target"], saveOutput["target"]) &&
		saveInput["release_id"] == saveOutput["release_id"]
	return map[string]any{
		"schemas_valid":                          true,
		"reconciliation_reference_present":       unknown && hasReference && targetBound,
		"retry_permitted_without_reconciliation": !unknown || !hasReference,
	}, nil
}
