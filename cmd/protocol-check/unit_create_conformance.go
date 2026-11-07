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
		definition := asObject(request["unit_definition"])
		checksum := asObject(definition["checksum"])
		artifactDigest := sha256.Sum256([]byte(stringValue(creationCase, "artifact_bytes")))
		definitionVerified := stringValue(checksum, "algorithm") == "sha256" &&
			stringValue(checksum, "digest") == hex.EncodeToString(artifactDigest[:])
		targetConsistent := unit["environment_id"] == target["environment_id"] &&
			target["environment_id"] == instance["environment_id"] && stringValue(unit, "resource_kind") == "systemd-unit"
		existing := asObject(creationCase["existing_unit"])
		existingCompatible := len(existing) == 0 || (reflect.DeepEqual(existing["unit_ref"], request["unit_ref"]) &&
			reflect.DeepEqual(existing["target"], request["target"]) && existing["release_id"] == request["release_id"] &&
			reflect.DeepEqual(existing["definition_checksum"], checksum) &&
			(stringValue(existing, "state") == "stopped" || stringValue(existing, "state") == "running"))
		dispatchAllowed := requestValid && targetConsistent && definitionVerified && existingCompatible &&
			boolValue(preconditions, "host_exists") && boolValue(preconditions, "definition_approved")
		operationRef, hasOperationRef := output["external_operation_ref"]
		operationBound := !hasOperationRef || (asObject(operationRef)["adapter_id"] == unit["adapter_id"] &&
			asObject(operationRef)["environment_id"] == unit["environment_id"])
		bound := reflect.DeepEqual(output["unit_ref"], request["unit_ref"]) && reflect.DeepEqual(output["target"], target) &&
			output["release_id"] == request["release_id"] && reflect.DeepEqual(output["definition_checksum"], checksum) && operationBound
		outcome := stringValue(output, "outcome")
		confirmed := dispatchAllowed && outputValid && bound && (outcome == "created" || outcome == "unchanged")
		mustInspect := stringValue(existing, "state") == "unknown" ||
			(dispatchAllowed && (!outputValid || !bound || outcome == "accepted" || outcome == "outcome_unknown"))
		results = append(results, map[string]any{
			"case_id":                   stringValue(creationCase, "case_id"),
			"request_schema_valid":      requestValid,
			"result_schema_valid":       outputValid,
			"definition_verified":       definitionVerified,
			"target_consistent":         targetConsistent,
			"existing_unit_compatible":  existingCompatible,
			"dispatch_allowed":          dispatchAllowed,
			"result_bound":              bound,
			"definition_confirmed":      confirmed,
			"must_inspect_before_retry": mustInspect,
			"readiness_established":     false,
		})
	}
	return map[string]any{"evaluations": results}, nil
}
