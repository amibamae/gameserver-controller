package main

import (
	"fmt"
	"reflect"
)

// Register the game migration evaluators defined in this file.
func init() {
	registerEvaluator(evaluateGameMigrate,
		"game.migrate.approved-copy-success",
		"game.migrate.approval-required",
		"game.migrate.verified-backup-required",
		"game.migrate.distinct-target-required",
		"game.migrate.target-binding",
		"game.migrate.complete-checks-required",
		"game.migrate.compatibility-required",
		"game.migrate.backup-result-binding",
		"game.migrate.target-stopped",
		"game.migrate.unknown-requires-reconciliation")
}

// evaluateGameMigrate evaluates the conformance fixtures game.migrate.approved-copy-success,
// game.migrate.approval-required, game.migrate.verified-backup-required,
// game.migrate.distinct-target-required, game.migrate.target-binding,
// game.migrate.complete-checks-required, game.migrate.compatibility-required,
// game.migrate.backup-result-binding, game.migrate.target-stopped,
// game.migrate.unknown-requires-reconciliation.
func evaluateGameMigrate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	migrationInput := asObject(input["migration_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:migrate-input", migrationInput); err != nil {
		return nil, fmt.Errorf("migration input does not match schema: %w", err)
	}
	requestContext := asObject(input["request_context"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:common:request-context", requestContext); err != nil {
		return nil, fmt.Errorf("migration request context does not match schema: %w", err)
	}
	m := newMigrationCase(migrationInput)
	d, err := m.dispatchChecks(fixture, requestContext)
	if err != nil {
		return nil, err
	}
	dispatchAllowed := d.allowed()

	output := asObject(input["migration_output"])
	outputPresent := len(output) > 0
	var result migrationResult
	if outputPresent {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:migrate-output", output); err != nil {
			return nil, fmt.Errorf("migration output does not match schema: %w", err)
		}
		result = m.checkOutput(output)
	}
	migrationConfirmed := dispatchAllowed && outputPresent && result.bound && result.outcome == "migrated" &&
		result.checksComplete && result.checksPassed && result.generationAdvanced
	return map[string]any{
		"schemas_valid":                    true,
		"backup_result_bound":              d.backupBound,
		"compatibility_requires_migration": d.compatibilityRequiresMigration,
		"dispatch_allowed":                 dispatchAllowed,
		"target_stopped":                   d.targetStopped,
		"source_preserved":                 d.distinctTarget && (!outputPresent || result.bound),
		"output_bound":                     result.bound,
		"required_checks_complete":         result.checksComplete,
		"required_checks_passed":           result.checksPassed,
		"data_generation_advanced":         result.generationAdvanced,
		"migration_confirmed":              migrationConfirmed,
		"must_reconcile_before_retry":      outputPresent && (!result.bound || result.outcome == "outcome_unknown"),
		"reconciliation_reference_present": result.reconciliationPresent,
	}, nil
}

// migrationDispatch holds the preconditions for dispatching a migration.
type migrationDispatch struct {
	approvalValid, backupVerified, backupBound, targetStopped    bool
	compatibilityRequiresMigration, distinctTarget, streamsBound bool
}

// dispatchChecks evaluates the migration preconditions: an approved,
// verified, bound backup whose compatibility result requires migration, a
// distinct stopped target, and one bounded stream per manifest object.
func (m migrationCase) dispatchChecks(fixture, requestContext map[string]any) (migrationDispatch, error) {
	manifestObjects, streams := arrayValue(m.manifest, "objects"), arrayValue(m.input, "object_streams")
	transfersBound, err := migrationTransfersBound(manifestObjects, streams, arrayValue(requestContext, "stream_transfers"))
	if err != nil {
		return migrationDispatch{}, err
	}
	preconditions := asObject(fixture["core_preconditions"])
	d := migrationDispatch{
		approvalValid:  boolValue(preconditions, "approval_valid"),
		backupVerified: boolValue(preconditions, "backup_verified"),
		backupBound:    m.backupBound(),
		targetStopped:  boolValue(preconditions, "target_stopped"),
		distinctTarget: !reflect.DeepEqual(m.sourceRef, m.targetRef),
		streamsBound:   migrationStreamsMatchManifest(manifestObjects, streams) && transfersBound,
	}
	d.compatibilityRequiresMigration = d.backupBound && m.compatibilityBound() &&
		stringValue(m.compatibility, "compatibility_status") == "migration_required"
	return d, nil
}

func (d migrationDispatch) allowed() bool {
	return d.approvalValid && d.backupVerified && d.backupBound && d.targetStopped &&
		d.compatibilityRequiresMigration && d.distinctTarget && d.streamsBound
}

// migrationCase holds the parts of a game.migrate request the checks bind
// together: the source backup and its manifest, the compatibility result,
// and the target instance and release.
type migrationCase struct {
	input, sourceBackup, backupData, manifest map[string]any
	compatibility, target, targetRelease      map[string]any
	targetRef, sourceRef                      map[string]any
}

func newMigrationCase(migrationInput map[string]any) migrationCase {
	sourceBackup := asObject(migrationInput["source_backup"])
	backupData := asObject(sourceBackup["operation_data"])
	manifest := asObject(backupData["manifest"])
	target := asObject(migrationInput["target"])
	return migrationCase{
		input: migrationInput, sourceBackup: sourceBackup, backupData: backupData, manifest: manifest,
		compatibility: asObject(migrationInput["compatibility_result"]), target: target,
		targetRelease: asObject(migrationInput["target_release"]),
		targetRef:     asObject(target["instance_ref"]), sourceRef: asObject(manifest["source_instance_ref"]),
	}
}

// backupBound: the source is a server.backup result whose operation data
// matches its own manifest.
func (m migrationCase) backupBound() bool {
	return stringValue(m.sourceBackup, "operation_type") == "server.backup" &&
		stringValue(m.backupData, "backup_id") == stringValue(m.manifest, "backup_id") &&
		stringValue(m.backupData, "environment_id") == stringValue(m.manifest, "environment_id") &&
		stringValue(m.backupData, "data_generation_id") == stringValue(m.manifest, "data_generation_id")
}

// compatibilityBound: the compatibility result was computed for this
// backup's game and data and for the target release.
func (m migrationCase) compatibilityBound() bool {
	return compatibilityNames(m.compatibility, m.backupData, asObject(m.manifest["game"]), m.targetRelease)
}

// migrationStreamsMatchManifest: the object streams are exactly the
// manifest's objects, one stream per object.
func migrationStreamsMatchManifest(manifestObjects, streams []any) bool {
	bound := len(manifestObjects) == len(streams)
	matched := make([]bool, len(streams))
	for _, rawObject := range manifestObjects {
		objectRef := asObject(asObject(rawObject)["object_ref"])
		match := -1
		for i, rawStream := range streams {
			if !matched[i] && reflect.DeepEqual(objectRef, asObject(asObject(rawStream)["object_ref"])) {
				match = i
				break
			}
		}
		if match < 0 {
			bound = false
		} else {
			matched[match] = true
		}
	}
	for _, ok := range matched {
		if !ok {
			bound = false
		}
	}
	return bound
}

// migrationTransfersBound: there is one controller-to-adapter transfer per
// stream, in order, each large enough for its manifest object.
func migrationTransfersBound(manifestObjects, streams, transfers []any) (bool, error) {
	bound := len(transfers) == len(streams)
	for i, rawStream := range streams {
		if i >= len(transfers) {
			break
		}
		transfer := asObject(transfers[i])
		if stringValue(transfer, "direction") != "controller_to_adapter" {
			bound = false
		}
		for _, rawObject := range manifestObjects {
			object := asObject(rawObject)
			if !reflect.DeepEqual(object["object_ref"], asObject(asObject(rawStream)["object_ref"])) {
				continue
			}
			expectedSize, err := integerValue(object["size_bytes"])
			if err != nil {
				return false, err
			}
			maximumBytes, err := integerValue(transfer["maximum_bytes"])
			if err != nil {
				return false, err
			}
			if maximumBytes < expectedSize {
				bound = false
			}
			break
		}
	}
	return bound, nil
}

// migrationResult is what a migration output proves.
type migrationResult struct {
	bound, checksComplete, checksPassed, generationAdvanced, reconciliationPresent bool
	outcome                                                                        string
}

// checkOutput binds a schema-valid migration output to the request: the
// source backup, target and release, exactly one passing result per
// required check, and a new data generation.
func (m migrationCase) checkOutput(output map[string]any) migrationResult {
	outputTarget := asObject(output["target"])
	outputDataID := stringValue(output, "migrated_data_generation_id")
	result := migrationResult{
		bound: stringValue(output, "source_backup_id") == stringValue(m.backupData, "backup_id") &&
			stringValue(output, "source_data_generation_id") == stringValue(m.backupData, "data_generation_id") &&
			reflect.DeepEqual(asObject(outputTarget["instance_ref"]), m.targetRef) &&
			stringValue(outputTarget, "server_id") == stringValue(m.target, "server_id") &&
			stringValue(outputTarget, "environment_id") == stringValue(m.target, "environment_id") &&
			stringValue(output, "target_release_id") == stringValue(m.targetRelease, "release_id"),
		outcome:               stringValue(output, "outcome"),
		generationAdvanced:    outputDataID != "" && outputDataID != stringValue(m.backupData, "data_generation_id"),
		reconciliationPresent: len(asObject(output["reconciliation_ref"])) > 0,
	}
	result.checksComplete, result.checksPassed = requiredChecksResult(arrayValue(m.input, "required_check_ids"), arrayValue(output, "check_results"))
	return result
}

// requiredChecksResult: complete means exactly one result for every
// required check and nothing else; passed means complete and every
// required check passed.
func requiredChecksResult(requiredIDs, results []any) (complete, passed bool) {
	requested := make(map[string]bool)
	for _, rawID := range requiredIDs {
		requested[rawID.(string)] = true
	}
	observed := make(map[string]string)
	complete = len(results) == len(requested)
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
	passed = complete
	for checkID := range requested {
		if observed[checkID] != "pass" {
			passed = false
		}
	}
	return complete, passed
}
