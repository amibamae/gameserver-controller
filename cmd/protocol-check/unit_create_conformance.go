package main

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
)

func evaluateUnitCreate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "evaluations")))
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		creationCase := asObject(rawCase)
		request, output := asObject(creationCase["request"]), asObject(creationCase["output"])
		preconditions := asObject(creationCase["core_preconditions"])
		requestValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-create-input", request) == nil
		outputValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-create-output", output) == nil
		unit, target := asObject(request["unit_ref"]), asObject(request["target"])
		instance := asObject(target["instance_ref"])
		hostAvailable, definitionVerified, adapterBound, definitionApproved, targetConsistent, existingCompatible, dispatchAllowed, checksum, existing := unitCreateDispatchPreconditions(creationCase, preconditions, request, unit, target, instance, requestValid)
		resolvedOperation := asObject(creationCase["resolved_operation"])
		operationBound, bound := unitCreateResultBinding(output, request, unit, target, checksum, resolvedOperation)
		outcome := stringValue(output, "outcome")
		confirmed := dispatchAllowed && outputValid && bound && (outcome == "created" || outcome == "unchanged")
		uncertain := outcome == "accepted" || outcome == "outcome_unknown"
		reconciled, retryPermitted, operatorRequired, mustInspect := unitCreateReconciliation(creationCase, request, target, checksum, resolvedOperation, existing, dispatchAllowed, outputValid, bound, uncertain, operationBound)
		results = append(results, map[string]any{
			"case_id":                   stringValue(creationCase, "case_id"),
			"request_schema_valid":      requestValid,
			"result_schema_valid":       outputValid,
			"definition_verified":       definitionVerified,
			"adapter_bound":             adapterBound,
			"host_available":            hostAvailable,
			"definition_approved":       definitionApproved,
			"target_consistent":         targetConsistent,
			"existing_unit_compatible":  existingCompatible,
			"dispatch_allowed":          dispatchAllowed,
			"result_bound":              bound,
			"definition_confirmed":      confirmed || reconciled,
			"must_inspect_before_retry": mustInspect,
			"reconciliation_confirmed":  reconciled,
			"retry_permitted":           retryPermitted,
			"operator_required":         operatorRequired,
			"readiness_established":     false,
		})
	}
	return map[string]any{"evaluations": results}, nil
}
func unitCreateDispatchPreconditions(creationCase, preconditions, request, unit, target, instance map[string]any, requestValid bool) (bool, bool, bool, bool, bool, bool, bool, map[string]any, map[string]any) {
	host := asObject(preconditions["host_inspection"])
	hostAvailable := reflect.DeepEqual(instance, host["instance_ref"]) && stringValue(host, "state") == "active"
	definition := asObject(request["unit_definition"])
	checksum := asObject(definition["checksum"])
	artifactDigest := sha256.Sum256([]byte(stringValue(creationCase, "artifact_bytes")))
	definitionVerified := stringValue(checksum, "algorithm") == "sha256" &&
		stringValue(checksum, "digest") == hex.EncodeToString(artifactDigest[:])
	adapterBound := stringValue(preconditions, "execution_adapter_id") != "" &&
		unit["adapter_id"] == preconditions["execution_adapter_id"]
	approvedDefinition := asObject(preconditions["approved_release_definition"])
	definitionApproved := request["release_id"] == approvedDefinition["release_id"] &&
		reflect.DeepEqual(definition, approvedDefinition["unit_definition"])
	targetConsistent := unit["environment_id"] == target["environment_id"] &&
		target["environment_id"] == instance["environment_id"] && stringValue(unit, "resource_kind") == "systemd-unit"
	existing := asObject(creationCase["existing_unit"])
	existingCompatible := len(existing) == 0 || (reflect.DeepEqual(existing["unit_ref"], request["unit_ref"]) &&
		reflect.DeepEqual(existing["target"], request["target"]) && existing["release_id"] == request["release_id"] &&
		reflect.DeepEqual(existing["definition_checksum"], checksum) &&
		(stringValue(existing, "state") == "stopped" || stringValue(existing, "state") == "running"))
	dispatchAllowed := requestValid && adapterBound && targetConsistent && definitionApproved && definitionVerified && existingCompatible &&
		hostAvailable
	return hostAvailable, definitionVerified, adapterBound, definitionApproved, targetConsistent, existingCompatible, dispatchAllowed, checksum, existing
}

func unitCreateResultBinding(output, request, unit, target, checksum, resolvedOperation map[string]any) (bool, bool) {
	operationRef, hasOperationRef := output["external_operation_ref"]
	operationBound := !hasOperationRef || (asObject(operationRef)["adapter_id"] == unit["adapter_id"] &&
		asObject(operationRef)["environment_id"] == unit["environment_id"] &&
		stringValue(asObject(operationRef), "resource_kind") == "unit-operation" &&
		reflect.DeepEqual(operationRef, resolvedOperation["external_operation_ref"]) &&
		reflect.DeepEqual(request["unit_ref"], resolvedOperation["unit_ref"]) &&
		reflect.DeepEqual(target, resolvedOperation["target"]) &&
		request["release_id"] == resolvedOperation["release_id"] &&
		reflect.DeepEqual(checksum, resolvedOperation["definition_checksum"]))
	bound := reflect.DeepEqual(output["unit_ref"], request["unit_ref"]) && reflect.DeepEqual(output["target"], target) &&
		output["release_id"] == request["release_id"] && reflect.DeepEqual(output["definition_checksum"], checksum) && operationBound
	return operationBound, bound
}

func unitCreateReconciliation(creationCase, request, target, checksum, resolvedOperation, existing map[string]any, dispatchAllowed, outputValid, bound, uncertain, operationBound bool) (bool, bool, bool, bool) {
	inspection := asObject(creationCase["unit_inspection"])
	inspectionBound := reflect.DeepEqual(request["unit_ref"], inspection["unit_ref"]) && reflect.DeepEqual(target, inspection["target"])
	inspectionState := stringValue(inspection, "state")
	installedDefinitionMatches := inspection["release_id"] == request["release_id"] && reflect.DeepEqual(inspection["definition_checksum"], checksum)
	reconciled := dispatchAllowed && outputValid && bound && uncertain && inspectionBound && installedDefinitionMatches &&
		stringValue(resolvedOperation, "state") == "completed" &&
		(inspectionState == "stopped" || inspectionState == "running")
	retryPermitted := dispatchAllowed && outputValid && bound && uncertain && inspectionBound && inspectionState == "absent" &&
		stringValue(resolvedOperation, "state") == "no_effect"
	operatorRequired := dispatchAllowed && uncertain && (!operationBound || (len(inspection) > 0 &&
		(!inspectionBound || inspectionState == "unknown" || ((inspectionState == "stopped" || inspectionState == "running") && !installedDefinitionMatches))))
	mustInspect := stringValue(existing, "state") == "unknown" ||
		(dispatchAllowed && (!outputValid || !bound || (uncertain && !reconciled && !retryPermitted)))
	return reconciled, retryPermitted, operatorRequired, mustInspect
}

