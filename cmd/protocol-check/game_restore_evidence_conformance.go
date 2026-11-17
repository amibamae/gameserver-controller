package main

import (
	"fmt"
	"reflect"
)

const (
	gameRestoreInputSchemaID        = "urn:gameserver-controller:0.21.0-draft:game:restore-input"
	gameRestoreOutputSchemaID       = "urn:gameserver-controller:0.21.0-draft:game:restore-output"
	gameRestoreInspectInputSchemaID = "urn:gameserver-controller:0.21.0-draft:game:restore-inspect-input"
)

func init() {
	registerEvaluator(evaluateGameRestoreEvidence, "game.restore.exact-evidence-and-reconciliation")
}

func evaluateGameRestoreEvidence(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	evaluations := arrayValue(input, "evaluations")
	if len(evaluations) == 0 {
		return nil, fmt.Errorf("restore fixture must contain evaluation cases")
	}
	results := make(map[string]any, len(evaluations))
	for _, rawCase := range evaluations {
		evaluation := asObject(rawCase)
		caseID := stringValue(evaluation, "case_id")
		if caseID == "" || results[caseID] != nil {
			return nil, fmt.Errorf("restore evaluation case_id %q is empty or duplicated", caseID)
		}
		results[caseID] = evaluateGameRestoreCase(pkg, input, evaluation)
	}
	return map[string]any{"evaluations": results}, nil
}

func evaluateGameRestoreCase(pkg *protocolPackage, input, evaluation map[string]any) map[string]any {
	request := mergeJSONObjects(asObject(input["restore_request"]), asObject(evaluation["request_overrides"]))
	output := mergeJSONObjects(asObject(input["restore_output"]), asObject(evaluation["output_overrides"]))
	requestValid := pkg.validateSchema(gameRestoreInputSchemaID, request) == nil
	outputValid := pkg.validateSchema(gameRestoreOutputSchemaID, output) == nil
	backup := asObject(asObject(request["source_backup"])["operation_data"])
	sourceBound := requestValid && gameRestoreSourceScopeMatches(request, backup)
	newGeneration := gameRestoreGenerationDiffers(request, backup)
	evidenceBound := requestValid && outputValid && gameRestoreEvidenceMatches(request, backup, output)
	eligible := sourceBound && newGeneration
	trusted := eligible && evidenceBound
	requiresInspection := eligible && stringValue(output, "outcome") == "outcome_unknown"
	canInspect := trusted && requiresInspection
	result := map[string]any{
		"request_schema_valid": requestValid, "output_schema_valid": outputValid,
		"source_scope_matches": sourceBound, "target_generation_is_new": newGeneration,
		"restore_evidence_bound": evidenceBound, "inspection_required": requiresInspection,
		"inspection_has_exact_reference": canInspect,
		"restore_completion_supported":   trusted && gameRestoreChecksPassed(request, output),
	}
	if boolValue(evaluation, "inspect") {
		result["inspection"] = evaluateGameRestoreInspection(pkg, input, evaluation, gameRestoreInspectionBasis{
			request: request, backup: backup, output: output,
			canInspect: canInspect,
		})
	}
	return result
}

// An ordinary server.restore uses a verified backup of the same server and
// environment. Cross-environment imports require a separately authorized workflow.
func gameRestoreSourceScopeMatches(request, backup map[string]any) bool {
	manifest := asObject(backup["manifest"])
	target := asObject(request["target"])
	instance := asObject(target["instance_ref"])
	sameTargetScope := manifest["server_id"] == target["server_id"] && manifest["environment_id"] == target["environment_id"] &&
		instance["environment_id"] == target["environment_id"]
	backupMatchesManifest := backup["backup_id"] == manifest["backup_id"] && backup["data_generation_id"] == manifest["data_generation_id"] &&
		backup["environment_id"] == manifest["environment_id"]
	return sameTargetScope && backupMatchesManifest
}

func gameRestoreEvidenceMatches(request, backup, output map[string]any) bool {
	target := asObject(request["target"])
	ref := asObject(output["restore_execution_ref"])
	compatibility := asObject(request["compatibility_result"])
	backupBound := stringValue(backup, "backup_id") == output["backup_id"] && stringValue(backup, "data_generation_id") == output["source_data_generation_id"]
	targetBound := reflect.DeepEqual(target, output["target"]) && stringValue(target, "environment_id") == output["environment_id"] &&
		stringValue(request, "target_data_generation_id") == output["target_data_generation_id"]
	executionBound := ref["environment_id"] == stringValue(target, "environment_id") && ref["adapter_id"] == stringValue(compatibility, "target_game_adapter_id") &&
		ref["resource_kind"] == "restore-execution"
	return backupBound && targetBound && executionBound
}

func gameRestoreGenerationDiffers(request, backup map[string]any) bool {
	target := stringValue(request, "target_data_generation_id")
	source := stringValue(backup, "data_generation_id")
	return target != "" && source != "" && target != source
}

func gameRestoreChecksPassed(request, output map[string]any) bool {
	if stringValue(output, "outcome") != "restored" {
		return false
	}
	required := arrayValue(request, "required_check_ids")
	checks := arrayValue(output, "validation_checks")
	if len(required) == 0 || len(required) != len(checks) {
		return false
	}
	passed := make([]any, 0, len(checks))
	// The output schema requires every validation check to pass on "restored".
	for _, rawCheck := range checks {
		passed = append(passed, asObject(rawCheck)["check_id"])
	}
	return uniqueStrings(passed) && sameStringSet(required, passed)
}

type gameRestoreInspectionBasis struct {
	request, backup, output map[string]any
	canInspect              bool
}

func evaluateGameRestoreInspection(pkg *protocolPackage, input, evaluation map[string]any, basis gameRestoreInspectionBasis) map[string]any {
	request := mergeJSONObjects(asObject(input["inspect_request"]), asObject(evaluation["inspect_request_overrides"]))
	output := mergeJSONObjects(asObject(input["inspect_output"]), asObject(evaluation["inspect_output_overrides"]))
	requestValid := pkg.validateSchema(gameRestoreInspectInputSchemaID, request) == nil
	outputValid := pkg.validateSchema(gameRestoreOutputSchemaID, output) == nil
	requestBound := requestValid && gameRestoreInspectRequestMatches(basis, request)
	evidenceBound := outputValid && gameRestoreInspectionEvidenceMatches(basis, request, output)
	bound := basis.canInspect && requestBound && evidenceBound
	return map[string]any{
		"request_schema_valid": requestValid, "output_schema_valid": outputValid,
		"request_bound": requestBound, "evidence_bound": evidenceBound,
		"restore_completion_supported": bound && gameRestoreChecksPassed(basis.request, output),
	}
}

func gameRestoreInspectionEvidenceMatches(basis gameRestoreInspectionBasis, request, output map[string]any) bool {
	return reflect.DeepEqual(request["restore_execution_ref"], output["restore_execution_ref"]) &&
		gameRestoreEvidenceMatches(basis.request, basis.backup, output)
}

func gameRestoreInspectRequestMatches(basis gameRestoreInspectionBasis, request map[string]any) bool {
	targetBound := reflect.DeepEqual(basis.request["target"], request["target"]) &&
		stringValue(basis.request, "target_data_generation_id") == request["target_data_generation_id"]
	return targetBound && stringValue(basis.backup, "backup_id") == request["backup_id"] &&
		reflect.DeepEqual(basis.output["restore_execution_ref"], request["restore_execution_ref"])
}
