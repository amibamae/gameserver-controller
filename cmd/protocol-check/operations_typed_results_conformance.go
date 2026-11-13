package main

import (
	"fmt"
	"reflect"
)

// Register the typed operation result evaluators defined in this file.
func init() {
	registerEvaluator(evaluateTypedProvisionBackupRestoreDestroyResults,
		"operations.typed-provision-backup-restore-destroy-results",
		"operations.typed-provision-backup-restore-destroy-target-mismatch",
		"operations.typed-provision-backup-restore-readiness-binding",
		"operations.typed-provision-backup-restore-readiness-freshness",
		"operations.typed-restore-data-generation-not-rotated")
	registerEvaluator(evaluateTypedDefineStartStopResults,
		"operations.typed-define-start-stop-results",
		"operations.typed-define-start-stop-target-mismatch",
		"operations.typed-define-start-stop-future-readiness",
		"operations.typed-define-start-stop-no-drain",
		"operations.typed-define-start-stop-no-proxy-drain")
}

// evaluateTypedProvisionBackupRestoreDestroyResults evaluates the conformance fixtures
// operations.typed-provision-backup-restore-destroy-results,
// operations.typed-provision-backup-restore-destroy-target-mismatch,
// operations.typed-provision-backup-restore-readiness-binding,
// operations.typed-provision-backup-restore-readiness-freshness,
// operations.typed-restore-data-generation-not-rotated.
func evaluateTypedProvisionBackupRestoreDestroyResults(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	provisionOperation := asObject(input["provision_operation"])
	backupOperation := asObject(input["backup_operation"])
	restoreOperation := asObject(input["restore_operation"])
	destroyOperation := asObject(input["destroy_operation"])
	for _, operation := range []struct {
		name  string
		value map[string]any
	}{
		{"server.provision", provisionOperation},
		{"server.backup", backupOperation},
		{"server.restore", restoreOperation},
		{"server.destroy", destroyOperation},
	} {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation.value); err != nil {
			return nil, fmt.Errorf("%s operation does not match schema: %w", operation.name, err)
		}
	}
	provisionResultBound, err := typedProvisionResultBound(provisionOperation)
	if err != nil {
		return nil, err
	}
	backupResultBound, err := typedBackupResultBound(backupOperation)
	if err != nil {
		return nil, err
	}
	restore, err := typedRestoreResult(restoreOperation, backupOperation, backupResultBound)
	if err != nil {
		return nil, err
	}
	destroyResultBound := typedDestroyResultBound(destroyOperation)
	allResultsBound := provisionResultBound && backupResultBound && restore.resultBound && destroyResultBound
	return map[string]any{
		"schemas_valid":                   true,
		"provision_result_bound":          provisionResultBound,
		"backup_result_bound":             backupResultBound,
		"restore_backup_owner_bound":      restore.backupOwnerBound,
		"restore_data_generation_rotated": restore.dataGenerationRotated,
		"restore_result_bound":            restore.resultBound,
		"destroy_result_bound":            destroyResultBound,
		"all_results_bound":               allResultsBound,
	}, nil
}

// operationData returns an operation's result.operation_data.
func operationData(operation map[string]any) map[string]any {
	return asObject(asObject(operation["result"])["operation_data"])
}

// operationParameters returns an operation's request_snapshot.parameters.
func operationParameters(operation map[string]any) map[string]any {
	return asObject(asObject(operation["request_snapshot"])["parameters"])
}

// typedProvisionResultBound: a provision result names its target, reports
// it ready, and carries a readiness assertion for the same instance that
// was current when the operation completed.
func typedProvisionResultBound(operation map[string]any) (bool, error) {
	target := asObject(operation["target"])
	data := operationData(operation)
	assertion := asObject(data["readiness_assertion"])
	completedAt, err := parseTime(stringValue(operation, "completed_at"))
	if err != nil {
		return false, err
	}
	observedAt, err := parseTime(stringValue(assertion, "observed_at"))
	if err != nil {
		return false, err
	}
	expiresAt, err := parseTime(stringValue(assertion, "expires_at"))
	if err != nil {
		return false, err
	}
	return stringValue(data, "server_id") == stringValue(target, "server_id") &&
		stringValue(data, "environment_id") == stringValue(target, "environment_id") &&
		stringValue(assertion, "server_id") == stringValue(data, "server_id") &&
		stringValue(assertion, "environment_id") == stringValue(data, "environment_id") &&
		stringValue(asObject(data["instance_ref"]), "environment_id") == stringValue(data, "environment_id") &&
		stringValue(asObject(assertion["instance_ref"]), "environment_id") == stringValue(assertion, "environment_id") &&
		reflect.DeepEqual(data["instance_ref"], assertion["instance_ref"]) &&
		stringValue(data, "lifecycle_state") == "ready" &&
		!completedAt.Before(observedAt) && completedAt.Before(expiresAt), nil
}

// typedBackupResultBound: a backup result and its manifest name the target
// server, environment and source instance (the expected one, if requested),
// and readiness held when it completed.
func typedBackupResultBound(operation map[string]any) (bool, error) {
	target := asObject(operation["target"])
	parameters := operationParameters(operation)
	data := operationData(operation)
	manifest := asObject(data["manifest"])
	readinessBound, err := readinessAssertionBindsAtCompletion(asObject(data["readiness_assertion"]),
		stringValue(operation, "completed_at"), stringValue(target, "server_id"), stringValue(target, "environment_id"), target["instance_ref"])
	if err != nil {
		return false, fmt.Errorf("backup readiness assertion: %w", err)
	}
	return stringValue(target, "server_id") == stringValue(manifest, "server_id") &&
		stringValue(target, "environment_id") == stringValue(data, "environment_id") &&
		stringValue(data, "environment_id") == stringValue(manifest, "environment_id") &&
		stringValue(data, "backup_id") == stringValue(manifest, "backup_id") &&
		stringValue(data, "data_generation_id") == stringValue(manifest, "data_generation_id") &&
		stringValue(asObject(target["instance_ref"]), "environment_id") == stringValue(target, "environment_id") &&
		reflect.DeepEqual(target["instance_ref"], manifest["source_instance_ref"]) &&
		(parameters["expected_instance_ref"] == nil ||
			reflect.DeepEqual(asObject(parameters["expected_instance_ref"]), asObject(manifest["source_instance_ref"]))) &&
		readinessBound, nil
}

// typedRestore is what a restore result proves.
type typedRestore struct {
	backupOwnerBound, dataGenerationRotated, resultBound bool
}

// typedRestoreResult: a restore uses a verified, succeeded backup of the
// same server, writes a new data generation, and names its target instance
// (the requested one, if any), with readiness current at completion.
func typedRestoreResult(operation, backupOperation map[string]any, backupResultBound bool) (typedRestore, error) {
	target := asObject(operation["target"])
	parameters := operationParameters(operation)
	data := operationData(operation)
	backupData := operationData(backupOperation)
	manifest := asObject(backupData["manifest"])
	readinessBound, err := readinessAssertionBindsAtCompletion(asObject(data["readiness_assertion"]),
		stringValue(operation, "completed_at"), stringValue(target, "server_id"), stringValue(target, "environment_id"), target["instance_ref"])
	if err != nil {
		return typedRestore{}, fmt.Errorf("restore readiness assertion: %w", err)
	}
	var result typedRestore
	result.backupOwnerBound = backupResultBound &&
		stringValue(parameters, "backup_id") == stringValue(manifest, "backup_id") &&
		stringValue(target, "server_id") == stringValue(manifest, "server_id") &&
		stringValue(target, "environment_id") == stringValue(manifest, "environment_id") &&
		stringValue(backupOperation, "status") == "succeeded" &&
		stringValue(backupData, "verified_at") != ""
	result.dataGenerationRotated = stringValue(data, "data_generation_id") != "" &&
		stringValue(data, "data_generation_id") != stringValue(manifest, "data_generation_id")
	result.resultBound = result.backupOwnerBound && result.dataGenerationRotated &&
		stringValue(target, "environment_id") == stringValue(asObject(target["instance_ref"]), "environment_id") &&
		stringValue(parameters, "backup_id") == stringValue(data, "backup_id") &&
		reflect.DeepEqual(target["instance_ref"], data["target_instance_ref"]) &&
		(parameters["target_instance_ref"] == nil ||
			reflect.DeepEqual(asObject(parameters["target_instance_ref"]), asObject(data["target_instance_ref"]))) &&
		readinessBound
	return result, nil
}

// typedDestroyResultBound: a destroy result names the expected instance,
// confirms the provider no longer has it, and names the verified backup
// if one was required.
func typedDestroyResultBound(operation map[string]any) bool {
	target := asObject(operation["target"])
	parameters := operationParameters(operation)
	data := operationData(operation)
	instance := asObject(target["instance_ref"])
	backupBound := parameters["verified_backup_id"] == nil ||
		stringValue(parameters, "verified_backup_id") == stringValue(data, "verified_backup_id")
	return stringValue(target, "environment_id") == stringValue(instance, "environment_id") &&
		reflect.DeepEqual(instance, asObject(data["instance_ref"])) &&
		reflect.DeepEqual(target["instance_ref"], parameters["expected_instance_ref"]) &&
		stringValue(data, "final_lifecycle_state") == "destroyed" &&
		boolValue(data, "provider_absence_confirmed") && backupBound &&
		(parameters["expected_instance_ref"] == nil || reflect.DeepEqual(asObject(parameters["expected_instance_ref"]), instance))
}

// evaluateTypedDefineStartStopResults evaluates the conformance fixtures
// operations.typed-define-start-stop-results, operations.typed-define-start-stop-target-mismatch,
// operations.typed-define-start-stop-future-readiness, operations.typed-define-start-stop-no-drain,
// operations.typed-define-start-stop-no-proxy-drain.
func evaluateTypedDefineStartStopResults(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	defineOperation := asObject(input["define_operation"])
	startOperation := asObject(input["start_operation"])
	stopOperation := asObject(input["stop_operation"])
	for _, operation := range []struct {
		name  string
		value map[string]any
	}{
		{"server.define", defineOperation},
		{"server.start", startOperation},
		{"server.stop", stopOperation},
	} {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation.value); err != nil {
			return nil, fmt.Errorf("%s operation does not match schema: %w", operation.name, err)
		}
	}
	defineData, startData, stopData := operationData(defineOperation), operationData(startOperation), operationData(stopOperation)
	defineTarget := asObject(defineOperation["target"])
	startReadinessCurrent, err := typedStartReadinessCurrent(startOperation)
	if err != nil {
		return nil, err
	}
	defineResultBound := stringValue(defineData, "server_id") == stringValue(defineTarget, "server_id") &&
		stringValue(defineData, "environment_id") == stringValue(defineTarget, "environment_id")
	serverIdentityConsistent := stringValue(defineData, "server_id") == stringValue(startData, "server_id") &&
		stringValue(startData, "server_id") == stringValue(stopData, "server_id") &&
		stringValue(defineData, "environment_id") == stringValue(startData, "environment_id") &&
		stringValue(startData, "environment_id") == stringValue(stopData, "environment_id")
	defineNoInfrastructure := stringValue(defineData, "lifecycle_state") == "unprovisioned" &&
		len(arrayValue(asObject(defineOperation["result"]), "affected_resources")) == 0
	return map[string]any{
		"schemas_valid":                     true,
		"server_identity_consistent":        serverIdentityConsistent,
		"define_result_bound":               defineResultBound,
		"define_has_no_infrastructure":      defineNoInfrastructure,
		"start_readiness_bound_and_current": startReadinessCurrent,
		"stop_postconditions_confirmed":     typedStopPostconditions(stopOperation),
	}, nil
}

// typedStartReadinessCurrent: a start result reports the target ready, and
// its readiness assertion names the same server, environment and instance
// and was current when the operation completed.
func typedStartReadinessCurrent(operation map[string]any) (bool, error) {
	data := operationData(operation)
	target := asObject(operation["target"])
	assertion := asObject(data["readiness_assertion"])
	atCompletion, err := readinessAssertionBindsAtCompletion(assertion, stringValue(operation, "completed_at"),
		stringValue(target, "server_id"), stringValue(target, "environment_id"), target["instance_ref"])
	if err != nil {
		return false, fmt.Errorf("start readiness assertion: %w", err)
	}
	bound := stringValue(data, "server_id") == stringValue(assertion, "server_id") &&
		stringValue(data, "environment_id") == stringValue(assertion, "environment_id") &&
		stringValue(data, "server_id") == stringValue(target, "server_id") &&
		stringValue(data, "environment_id") == stringValue(target, "environment_id") &&
		reflect.DeepEqual(data["instance_ref"], target["instance_ref"]) &&
		reflect.DeepEqual(target["instance_ref"], assertion["instance_ref"]) &&
		stringValue(asObject(target["instance_ref"]), "environment_id") == stringValue(data, "environment_id") &&
		stringValue(asObject(assertion["instance_ref"]), "environment_id") == stringValue(assertion, "environment_id")
	return bound && atCompletion && stringValue(data, "lifecycle_state") == "ready", nil
}

// typedStopPostconditions: a stop result names its target, reports it
// stopped with a confirmed game save and a stopped unit, and drained the
// proxy when draining was requested and configured.
func typedStopPostconditions(operation map[string]any) bool {
	data := operationData(operation)
	target := asObject(operation["target"])
	drainRequested := boolValue(operationParameters(operation), "drain")
	drainOutcome := stringValue(asObject(data["proxy_drain"]), "outcome")
	drainSatisfied := drainOutcome == "drained" || drainOutcome == "not_configured" ||
		(drainOutcome == "not_requested" && !drainRequested)
	return stringValue(data, "server_id") == stringValue(target, "server_id") &&
		stringValue(data, "environment_id") == stringValue(target, "environment_id") &&
		reflect.DeepEqual(data["instance_ref"], target["instance_ref"]) &&
		stringValue(data, "lifecycle_state") == "stopped" &&
		stringValue(asObject(data["game_save"]), "outcome") == "confirmed" &&
		stringValue(asObject(data["execution"]), "outcome") == "stopped" &&
		drainSatisfied
}
