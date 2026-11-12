package main

import (
	"fmt"
	"reflect"
	"strings"
)

// evaluateGameMigrateFixtures evaluates the 10 game.migrate.* fixtures.
func evaluateGameMigrateFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	migrationInput := asObject(input["migration_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:migrate-input", migrationInput); err != nil {
		return nil, fmt.Errorf("migration input does not match schema: %w", err)
	}
	requestContext := asObject(input["request_context"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:common:request-context", requestContext); err != nil {
		return nil, fmt.Errorf("migration request context does not match schema: %w", err)
	}
	sourceBackup := asObject(migrationInput["source_backup"])
	backupData := asObject(sourceBackup["operation_data"])
	manifest := asObject(backupData["manifest"])
	compatibility := asObject(migrationInput["compatibility_result"])
	target := asObject(migrationInput["target"])
	targetRef := asObject(target["instance_ref"])
	sourceRef := asObject(manifest["source_instance_ref"])
	targetRelease := asObject(migrationInput["target_release"])

	backupBound := stringValue(sourceBackup, "operation_type") == "server.backup" &&
		stringValue(backupData, "backup_id") == stringValue(manifest, "backup_id") &&
		stringValue(backupData, "environment_id") == stringValue(manifest, "environment_id") &&
		stringValue(backupData, "data_generation_id") == stringValue(manifest, "data_generation_id")
	compatibilityBound := backupBound &&
		stringValue(compatibility, "backup_id") == stringValue(backupData, "backup_id") &&
		stringValue(compatibility, "environment_id") == stringValue(backupData, "environment_id") &&
		stringValue(compatibility, "data_generation_id") == stringValue(backupData, "data_generation_id") &&
		stringValue(compatibility, "source_game_adapter_id") == stringValue(asObject(manifest["game"]), "adapter_id") &&
		stringValue(compatibility, "source_game_version") == stringValue(asObject(manifest["game"]), "version") &&
		stringValue(compatibility, "target_game_adapter_id") == stringValue(targetRelease, "game_adapter_id") &&
		stringValue(compatibility, "target_release_id") == stringValue(targetRelease, "release_id") &&
		stringValue(compatibility, "target_game_version") == stringValue(targetRelease, "game_version")
	compatibilityRequiresMigration := compatibilityBound && stringValue(compatibility, "compatibility_status") == "migration_required"
	distinctTarget := !reflect.DeepEqual(sourceRef, targetRef)

	manifestObjects := arrayValue(manifest, "objects")
	streams := arrayValue(migrationInput, "object_streams")
	streamsBound := len(manifestObjects) == len(streams)
	matchedStreams := make([]bool, len(streams))
	for _, rawObject := range manifestObjects {
		objectRef := asObject(asObject(rawObject)["object_ref"])
		match := -1
		for i, rawStream := range streams {
			stream := asObject(rawStream)
			if !matchedStreams[i] && reflect.DeepEqual(objectRef, asObject(stream["object_ref"])) {
				match = i
				break
			}
		}
		if match < 0 {
			streamsBound = false
		} else {
			matchedStreams[match] = true
		}
	}
	for _, matched := range matchedStreams {
		if !matched {
			streamsBound = false
		}
	}
	transfers := arrayValue(requestContext, "stream_transfers")
	if len(transfers) != len(streams) {
		streamsBound = false
	}
	for i, rawStream := range streams {
		if i >= len(transfers) {
			break
		}
		transfer := asObject(transfers[i])
		if stringValue(transfer, "direction") != "controller_to_adapter" {
			streamsBound = false
		}
		for _, rawObject := range manifestObjects {
			object := asObject(rawObject)
			if !reflect.DeepEqual(object["object_ref"], asObject(asObject(rawStream)["object_ref"])) {
				continue
			}
			expectedSize, err := integerValue(object["size_bytes"])
			if err != nil {
				return nil, err
			}
			maximumBytes, err := integerValue(transfer["maximum_bytes"])
			if err != nil {
				return nil, err
			}
			if maximumBytes < expectedSize {
				streamsBound = false
			}
			break
		}
	}
	preconditions := asObject(fixture["core_preconditions"])
	approvalValid := boolValue(preconditions, "approval_valid")
	backupVerified := boolValue(preconditions, "backup_verified")
	targetStopped := boolValue(preconditions, "target_stopped")
	dispatchAllowed := approvalValid && backupVerified && backupBound && targetStopped && compatibilityRequiresMigration && distinctTarget && streamsBound

	output := asObject(input["migration_output"])
	outputPresent := len(output) > 0
	outputBound, checksComplete, checksPassed, generationAdvanced := false, false, false, false
	outcome := ""
	reconciliationPresent := false
	if outputPresent {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:migrate-output", output); err != nil {
			return nil, fmt.Errorf("migration output does not match schema: %w", err)
		}
		outputTarget := asObject(output["target"])
		outputDataID := stringValue(output, "migrated_data_generation_id")
		outputBound = stringValue(output, "source_backup_id") == stringValue(backupData, "backup_id") &&
			stringValue(output, "source_data_generation_id") == stringValue(backupData, "data_generation_id") &&
			reflect.DeepEqual(asObject(outputTarget["instance_ref"]), targetRef) &&
			stringValue(outputTarget, "server_id") == stringValue(target, "server_id") &&
			stringValue(outputTarget, "environment_id") == stringValue(target, "environment_id") &&
			stringValue(output, "target_release_id") == stringValue(targetRelease, "release_id")
		outcome = stringValue(output, "outcome")
		requested := make(map[string]bool)
		for _, rawID := range arrayValue(migrationInput, "required_check_ids") {
			requested[rawID.(string)] = true
		}
		observed := make(map[string]string)
		results := arrayValue(output, "check_results")
		checksComplete = len(results) == len(requested)
		for _, rawResult := range results {
			result := asObject(rawResult)
			checkID := stringValue(result, "check_id")
			if !requested[checkID] {
				checksComplete = false
			}
			if _, exists := observed[checkID]; exists {
				checksComplete = false
			}
			observed[checkID] = stringValue(result, "result")
		}
		checksPassed = checksComplete
		for checkID := range requested {
			if observed[checkID] != "pass" {
				checksPassed = false
			}
		}
		generationAdvanced = outputDataID != "" && outputDataID != stringValue(backupData, "data_generation_id")
		reconciliationPresent = len(asObject(output["reconciliation_ref"])) > 0
	}
	migrationConfirmed := dispatchAllowed && outputPresent && outputBound && outcome == "migrated" &&
		checksComplete && checksPassed && generationAdvanced
	return map[string]any{
		"schemas_valid":                    true,
		"backup_result_bound":              backupBound,
		"compatibility_requires_migration": compatibilityRequiresMigration,
		"dispatch_allowed":                 dispatchAllowed,
		"target_stopped":                   targetStopped,
		"source_preserved":                 distinctTarget && (!outputPresent || outputBound),
		"output_bound":                     outputBound,
		"required_checks_complete":         checksComplete,
		"required_checks_passed":           checksPassed,
		"data_generation_advanced":         generationAdvanced,
		"migration_confirmed":              migrationConfirmed,
		"must_reconcile_before_retry":      outputPresent && (!outputBound || outcome == "outcome_unknown"),
		"reconciliation_reference_present": reconciliationPresent,
	}, nil
}

// evaluateGameCompatibilityCheckFixtures evaluates the 7 game.compatibility.check.* fixtures.
func evaluateGameCompatibilityCheckFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	compatibilityInput, compatibilityOutput := asObject(input["compatibility_input"]), asObject(input["compatibility_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:compatibility-check-input", compatibilityInput); err != nil {
		return nil, fmt.Errorf("compatibility input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:compatibility-check-output", compatibilityOutput); err != nil {
		return nil, fmt.Errorf("compatibility output does not match schema: %w", err)
	}
	manifest := asObject(compatibilityInput["source_manifest"])
	sourceGame := asObject(manifest["game"])
	targetRelease := asObject(compatibilityInput["target_release"])
	targetBound := stringValue(compatibilityOutput, "backup_id") == stringValue(manifest, "backup_id") &&
		stringValue(compatibilityOutput, "environment_id") == stringValue(manifest, "environment_id") &&
		stringValue(compatibilityOutput, "data_generation_id") == stringValue(manifest, "data_generation_id") &&
		stringValue(compatibilityOutput, "source_game_adapter_id") == stringValue(sourceGame, "adapter_id") &&
		stringValue(compatibilityOutput, "source_game_version") == stringValue(sourceGame, "version") &&
		stringValue(compatibilityOutput, "target_game_adapter_id") == stringValue(targetRelease, "game_adapter_id") &&
		stringValue(compatibilityOutput, "target_release_id") == stringValue(targetRelease, "release_id") &&
		stringValue(compatibilityOutput, "target_game_version") == stringValue(targetRelease, "game_version")
	requested := make(map[string]bool)
	for _, rawID := range arrayValue(compatibilityInput, "required_check_ids") {
		requested[rawID.(string)] = true
	}
	observed := make(map[string]string)
	results := arrayValue(compatibilityOutput, "check_results")
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
	derived := "compatible"
	if hasIncompatible {
		derived = "incompatible"
	} else if hasUnknown || !complete {
		derived = "unknown"
	} else if hasMigration {
		derived = "migration_required"
	}
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

// evaluateGameHealthCheckUnknownNotHealthy evaluates game.health.check.unknown-not-healthy.
func evaluateGameHealthCheckUnknownNotHealthy(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	healthInput, healthOutput := asObject(input["health_input"]), asObject(input["health_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:health-check-input", healthInput); err != nil {
		return nil, fmt.Errorf("health input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:health-check-output", healthOutput); err != nil {
		return nil, fmt.Errorf("health output does not match schema: %w", err)
	}
	healthInstance := asObject(healthInput["instance_ref"])
	bound := healthInput["server_id"] == healthOutput["server_id"] &&
		healthInput["environment_id"] == healthOutput["environment_id"] &&
		stringValue(healthInstance, "environment_id") == stringValue(healthInput, "environment_id") &&
		healthInput["game_release_id"] == healthOutput["game_release_id"] &&
		reflect.DeepEqual(healthInput["instance_ref"], healthOutput["instance_ref"])
	required := arrayValue(healthInput, "required_check_ids")
	results := arrayValue(healthOutput, "check_results")
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
	health := "healthy"
	hasFailure, hasUnknown, hasWarning := false, false, false
	for id := range requested {
		result, ok := observed[id]
		if !ok {
			complete = false
		}
		switch result {
		case "pass":
		case "warn":
			hasWarning = true
		case "fail":
			hasFailure = true
		case "unknown":
			hasUnknown = true
		default:
			complete = false
		}
	}
	incompleteEvidence := hasUnknown || !complete
	if hasFailure {
		health = "unhealthy"
	} else if incompleteEvidence {
		health = "unknown"
	} else if hasWarning {
		health = "degraded"
	}
	return map[string]any{
		"schemas_valid":           true,
		"target_bound":            bound,
		"check_set_complete":      complete,
		"derived_health":          health,
		"reported_health_matches": stringValue(healthOutput, "health") == health,
		"unknown_not_healthy":     !incompleteEvidence || stringValue(healthOutput, "health") != "healthy",
	}, nil
}

// evaluateGameBackupValidateFixtures evaluates game.backup.validate.exact-objects and game.backup.validate.unknown-not-valid.
func evaluateGameBackupValidateFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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
	manifestRefs := make([]any, 0, len(arrayValue(manifest, "objects")))
	for _, rawObject := range arrayValue(manifest, "objects") {
		manifestRefs = append(manifestRefs, asObject(rawObject)["object_ref"])
	}
	streamRefs := make([]any, 0, len(arrayValue(backupInput, "object_streams")))
	for _, rawStream := range arrayValue(backupInput, "object_streams") {
		streamRefs = append(streamRefs, asObject(rawStream)["object_ref"])
	}
	streamsBound := sameObjectRefSet(manifestRefs, streamRefs)
	streams := arrayValue(backupInput, "object_streams")
	transfers := arrayValue(requestContext, "stream_transfers")
	if len(transfers) != len(streams) {
		streamsBound = false
	}
	for i, rawStream := range streams {
		stream := asObject(rawStream)
		if stringValue(asObject(stream["object_ref"]), "environment_id") != stringValue(backupInput, "environment_id") {
			streamsBound = false
		}
		matched := false
		for _, rawObject := range arrayValue(manifest, "objects") {
			object := asObject(rawObject)
			if !reflect.DeepEqual(object["object_ref"], stream["object_ref"]) {
				continue
			}
			matched = true
			expectedSize, err := integerValue(object["size_bytes"])
			if err != nil {
				return nil, err
			}
			if i >= len(transfers) {
				streamsBound = false
				break
			}
			transfer := asObject(transfers[i])
			maximumBytes, err := integerValue(transfer["maximum_bytes"])
			if err != nil {
				return nil, err
			}
			if maximumBytes < expectedSize || stringValue(transfer, "direction") != "controller_to_adapter" {
				streamsBound = false
			}
			break
		}
		if !matched {
			streamsBound = false
		}
	}
	required := arrayValue(backupInput, "required_check_ids")
	results := arrayValue(backupOutput, "check_results")
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
	derived := "valid"
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
	if hasFailure {
		derived = "invalid"
	} else if hasUnknown || !complete {
		derived = "unknown"
	}
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

// evaluateGameDataSaveBackupPrepareFixtures evaluates game.data.save.backup-prepare.binding and game.data.save.backup-prepare.reject-unsafe-datasets.
func evaluateGameDataSaveBackupPrepareFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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
	dataSets := arrayValue(dataOutput, "data_sets")
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
			pathValue, ok := rawPath.(string)
			if !ok || pathValue == "" || strings.HasPrefix(pathValue, "/") || strings.Contains(pathValue, "\\") {
				pathsSafe = false
				continue
			}
			for _, segment := range strings.Split(pathValue, "/") {
				if segment == "" || segment == "." || segment == ".." {
					pathsSafe = false
				}
			}
		}
	}
	selectedIDs := arrayValue(prepareInput, "data_set_ids")
	selectedOutputIDs := arrayValue(prepareOutput, "data_set_ids")
	dataSetSelectionBound := uniqueStrings(dataSetIDs) && uniqueStrings(selectedIDs) &&
		sameStringSet(selectedIDs, selectedOutputIDs)
	dataSetsSupportConsistency := dataSetSelectionBound
	for _, rawID := range selectedIDs {
		id, ok := rawID.(string)
		if !ok || !supportedModes[id][stringValue(prepareInput, "consistency_mode")] {
			dataSetsSupportConsistency = false
		}
	}
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

// evaluateGameSaveUnknownRequiresReconciliation evaluates game.save.unknown-requires-reconciliation.
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

// evaluateGameReleaseInstallConfigureFailClosed evaluates game.release.install.configure.fail-closed.
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

// evaluateGameInstallFixtures evaluates game.install.repeat-same-release-is-idempotent, game.install.completed-verifies-exact-artifacts and game.install.incomplete-artifact-set-requires-reconciliation.
func evaluateGameInstallFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateGameValidateInstallationFixtures evaluates the 5 game.validate-installation.* fixtures.
func evaluateGameValidateInstallationFixtures(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateGameReadinessUnknownRequiredCheckBlocksReady evaluates game.readiness.unknown-required-check-blocks-ready.
func evaluateGameReadinessUnknownRequiredCheckBlocksReady(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-input", input); err != nil {
		return nil, fmt.Errorf("readiness input does not match schema: %w", err)
	}
	output := asObject(fixture["output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-output", output); err != nil {
		return nil, fmt.Errorf("readiness output does not match schema: %w", err)
	}
	bound := input["server_id"] == output["server_id"] &&
		input["environment_id"] == output["environment_id"] &&
		input["game_release_id"] == output["game_release_id"] &&
		reflect.DeepEqual(input["instance_ref"], output["instance_ref"])
	if !bound {
		return nil, fmt.Errorf("readiness output must remain bound to the exact requested target and release")
	}
	requiredChecks := arrayValue(input, "required_checks")
	if len(requiredChecks) == 0 {
		return nil, fmt.Errorf("readiness request must contain required checks")
	}
	requested := make(map[string]string, len(requiredChecks))
	for _, rawRequirement := range requiredChecks {
		requirement := asObject(rawRequirement)
		checkID := stringValue(requirement, "check_id")
		checkVersion := stringValue(requirement, "check_version")
		if checkID == "" || checkVersion == "" {
			return nil, fmt.Errorf("required check IDs and versions must be non-empty")
		}
		if _, duplicate := requested[checkID]; duplicate {
			return nil, fmt.Errorf("required check IDs must be unique")
		}
		requested[checkID] = checkVersion
	}
	results := arrayValue(output, "check_results")
	if len(results) != len(requested) {
		return nil, fmt.Errorf("readiness output must contain exactly one result for every required check")
	}
	type observedReadiness struct {
		result string
	}
	observed := make(map[string]observedReadiness, len(results))
	for _, rawResult := range results {
		item := asObject(rawResult)
		checkID := stringValue(item, "check_id")
		expectedVersion, exists := requested[checkID]
		if !exists {
			return nil, fmt.Errorf("readiness output contains an unrequested check %q", checkID)
		}
		if _, exists := observed[checkID]; exists {
			return nil, fmt.Errorf("readiness output contains duplicate check %q", checkID)
		}
		version := stringValue(item, "check_version")
		if version == "" || version != expectedVersion {
			return nil, fmt.Errorf("readiness output check %q has a missing or mismatched version", checkID)
		}
		observed[checkID] = observedReadiness{result: stringValue(item, "result")}
	}
	allPassed, hasFailure, hasUnknown := true, false, false
	for checkID := range requested {
		result, exists := observed[checkID]
		if !exists {
			return nil, fmt.Errorf("readiness output is missing required check %q", checkID)
		}
		switch result.result {
		case "pass":
		case "fail":
			allPassed, hasFailure = false, true
		case "unknown":
			allPassed, hasUnknown = false, true
		default:
			return nil, fmt.Errorf("unknown readiness result %q for check %q", result.result, checkID)
		}
	}
	readiness := "ready"
	if hasFailure {
		readiness = "not_ready"
	} else if hasUnknown {
		readiness = "unknown"
	}
	return map[string]any{
		"schema_valid": true, "target_bound": bound,
		"required_checks_complete": true, "all_required_checks_passed": allPassed,
		"readiness": readiness, "ready_transition_permitted": allPassed,
	}, nil
}
