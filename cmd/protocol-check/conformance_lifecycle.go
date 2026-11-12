package main

import (
	"fmt"
	"reflect"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// Register the lifecycle, compatibility, transport and safety evaluators defined in this file.
func init() {
	registerEvaluator(withoutPackage(evaluateLifecycleRejectUnlistedTransition), "lifecycle.reject.unlisted-transition")
	registerEvaluator(evaluateLifecycleReplacementIdentity, "lifecycle.replacement.identity")
	registerEvaluator(evaluateLifecycleRequiredReadinessSet, "lifecycle.required-readiness-set")
	registerEvaluator(withoutPackage(evaluateCompatibilityProtocolVersionNegotiation), "compatibility.protocol-version-negotiation")
	registerEvaluator(evaluateCompatibilityResponseEvolution, "compatibility.response-evolution")
	registerEvaluator(evaluateCompatibilityOperationUpgradeRecovery, "compatibility.operation-upgrade-recovery")
	registerEvaluator(evaluateTransportStorageProfile,
		"transport.storage.profile-negotiated",
		"transport.storage.profile-not-selected",
		"transport.storage.selection-not-offered")
	registerEvaluator(withoutPackage(evaluateSafetyDestroyBlocked),
		"safety.destroy.blocked-stale-data-generation",
		"safety.destroy.blocked-unverified-backup")
}

// evaluateLifecycleRejectUnlistedTransition evaluates the lifecycle.reject.unlisted-transition conformance fixture.
func evaluateLifecycleRejectUnlistedTransition(fixture map[string]any) (map[string]any, error) {
	transition := asObject(fixture["requested_transition"])
	allowed := protocolstate.CanTransitionServer(
		protocolstate.ServerState(stringValue(fixture, "initial_state")),
		protocolstate.ServerState(stringValue(transition, "to")),
	)
	result := map[string]any{"accepted": allowed, "side_effect_permitted": allowed}
	if !allowed {
		result["error_code"] = "core.lifecycle.invalid_transition"
	}
	return result, nil
}

// evaluateLifecycleReplacementIdentity evaluates the lifecycle.replacement.identity conformance fixture.
func evaluateLifecycleReplacementIdentity(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	previous := asObject(input["previous_server"])
	replacement := asObject(input["replacement_server"])
	serverSchemaID := "urn:gameserver-controller:0.21.0-draft:lifecycle:server"
	if err := pkg.validateSchema(serverSchemaID, previous); err != nil {
		return nil, fmt.Errorf("previous server does not match schema: %w", err)
	}
	if err := pkg.validateSchema(serverSchemaID, replacement); err != nil {
		return nil, fmt.Errorf("replacement server does not match schema: %w", err)
	}
	previousRef, replacementRef := previous["instance_ref"], replacement["instance_ref"]
	identityPreserved := previous["server_id"] == replacement["server_id"] && previous["environment_id"] == replacement["environment_id"]
	previousDestroyed := stringValue(previous, "lifecycle_state") == "destroyed"
	replacementInstanceDistinct := previousRef != nil && replacementRef != nil && !reflect.DeepEqual(previousRef, replacementRef)
	generationRotated := previous["data_generation_id"] != nil && replacement["data_generation_id"] != nil && previous["data_generation_id"] != replacement["data_generation_id"]
	reprovisionPermitted := protocolstate.CanTransitionServer(protocolstate.ServerDestroyed, protocolstate.ServerProvisioning)
	return map[string]any{
		"previous_server_schema_valid": true, "replacement_server_schema_valid": true,
		"logical_identity_preserved": identityPreserved, "previous_instance_destroyed": previousDestroyed,
		"replacement_instance_distinct": replacementInstanceDistinct, "data_generation_rotated": generationRotated,
		"reprovision_transition_permitted": reprovisionPermitted,
		"replacement_safe":                 identityPreserved && previousDestroyed && replacementInstanceDistinct && generationRotated && reprovisionPermitted,
	}, nil
}

// evaluateLifecycleRequiredReadinessSet evaluates the lifecycle.required-readiness-set conformance fixture.
func evaluateLifecycleRequiredReadinessSet(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-input", input); err != nil {
		return nil, fmt.Errorf("readiness request does not match schema: %w", err)
	}
	set := asObject(fixture["declared_check_set"])
	requiredEntries := arrayValue(input, "required_checks")
	declared := arrayValue(set, "check_ids")
	versions := asObject(set["check_versions"])
	required := make([]any, 0, len(requiredEntries))
	requiredVersions := make(map[string]string, len(requiredEntries))
	setValid := stringValue(set, "readiness_check_set_id") != "" &&
		stringValue(input, "readiness_check_set_id") == stringValue(set, "readiness_check_set_id") &&
		uniqueStrings(declared)
	for _, rawRequirement := range requiredEntries {
		requirement := asObject(rawRequirement)
		checkID := stringValue(requirement, "check_id")
		checkVersion := stringValue(requirement, "check_version")
		if checkID == "" || checkVersion == "" {
			setValid = false
			continue
		}
		if _, duplicate := requiredVersions[checkID]; duplicate {
			setValid = false
		}
		required = append(required, checkID)
		requiredVersions[checkID] = checkVersion
		if declaredVersion, ok := versions[checkID].(string); !ok || declaredVersion == "" || declaredVersion != checkVersion {
			setValid = false
		}
	}
	setValid = setValid && sameStringSet(required, declared)
	caseResults := make([]any, 0, len(arrayValue(fixture, "evaluations")))
	for _, rawCase := range arrayValue(fixture, "evaluations") {
		readinessCase := asObject(rawCase)
		output := asObject(readinessCase["output"])
		result := map[string]any{
			"case_id":                    stringValue(readinessCase, "case_id"),
			"ready_transition_permitted": false,
			"readiness":                  "not_ready",
			"block_reason":               any(nil),
			"error_code":                 any(nil),
		}
		unknownState := false
		for _, rawResult := range arrayValue(output, "check_results") {
			status := stringValue(asObject(rawResult), "result")
			if status != "pass" && status != "fail" && status != "unknown" {
				unknownState = true
			}
		}
		if unknownState {
			result["readiness"] = "unknown"
			result["block_reason"] = "unknown_state"
			result["error_code"] = "core.compatibility.unknown_state"
			caseResults = append(caseResults, result)
			continue
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-output", output); err != nil {
			return nil, fmt.Errorf("readiness output %q does not match schema: %w", stringValue(readinessCase, "case_id"), err)
		}
		bound := input["server_id"] == output["server_id"] &&
			input["environment_id"] == output["environment_id"] &&
			input["game_release_id"] == output["game_release_id"] &&
			reflect.DeepEqual(input["instance_ref"], output["instance_ref"])
		if !setValid {
			result["block_reason"] = "invalid_required_check_set"
		} else if !bound {
			result["block_reason"] = "target_mismatch"
		} else {
			observed := make(map[string]int)
			versionsMatch := true
			allPassed := true
			for _, rawResult := range arrayValue(output, "check_results") {
				check := asObject(rawResult)
				checkID := stringValue(check, "check_id")
				observed[checkID]++
				if check["check_version"] != versions[checkID] {
					versionsMatch = false
				}
				if stringValue(check, "result") != "pass" {
					allPassed = false
				}
			}
			complete := len(observed) == len(required) && len(arrayValue(output, "check_results")) == len(required)
			for _, rawID := range required {
				if id, ok := rawID.(string); !ok || observed[id] != 1 {
					complete = false
				}
			}
			if !complete {
				result["block_reason"] = "required_check_set_mismatch"
			} else if !versionsMatch {
				result["block_reason"] = "required_check_set_mismatch"
			} else if !allPassed {
				result["readiness"] = "unknown"
				result["block_reason"] = "required_check_not_passed"
			} else {
				result["ready_transition_permitted"] = true
				result["readiness"] = "ready"
			}
		}
		caseResults = append(caseResults, result)
	}
	return map[string]any{"readiness_check_set_valid": setValid, "evaluations": caseResults, "ready_transition_requires_complete_passing_set": true}, nil
}

// evaluateCompatibilityProtocolVersionNegotiation evaluates the compatibility.protocol-version-negotiation conformance fixture.
func evaluateCompatibilityProtocolVersionNegotiation(fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0, len(arrayValue(fixture, "cases")))
	for _, rawCase := range arrayValue(fixture, "cases") {
		item := asObject(rawCase)
		local, remote := arrayValue(item, "local_protocol_versions"), arrayValue(item, "peer_protocol_versions")
		selected := stringValue(item, "selected_protocol_version")
		mutual := false
		for _, localVersion := range local {
			for _, peerVersion := range remote {
				if localVersion == peerVersion && localVersion == selected {
					mutual = true
				}
			}
		}
		priorFailureVersion := stringValue(item, "version_that_failed_validation_or_authorization")
		downgradeAfterError := priorFailureVersion != "" && selected != priorFailureVersion
		accepted := mutual && !downgradeAfterError
		var errorCode any
		if !accepted {
			errorCode = "core.compatibility.unsupported_version"
		}
		results = append(results, map[string]any{
			"case_id": stringValue(item, "case_id"), "accepted": accepted,
			"error_code": errorCode, "session_created": accepted, "adapter_calls": 0,
		})
	}
	return map[string]any{"cases": results}, nil
}

// evaluateCompatibilityResponseEvolution evaluates the compatibility.response-evolution conformance fixture.
func evaluateCompatibilityResponseEvolution(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	server := asObject(input["server"])
	operation := asObject(input["operation"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:lifecycle:server", server); err != nil {
		return nil, fmt.Errorf("extensible server resource does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
		return nil, fmt.Errorf("extensible operation resource does not match schema: %w", err)
	}
	request := asObject(input["request"])
	requestWithUnknown := asObject(input["request_with_unknown_field"])
	requestValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation-request", request) == nil
	unknownRequestRejected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation-request", requestWithUnknown) != nil
	nestedServer := asObject(input["server_with_unknown_nested_observation_field"])
	nestedServerRejected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:lifecycle:server", nestedServer) != nil
	unknownLifecycle := asObject(input["server_with_unknown_lifecycle_state"])
	unknownLifecycleRejected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:lifecycle:server", unknownLifecycle) != nil
	unknownOperationStatus := asObject(input["operation_with_unknown_status"])
	unknownOperationStatusRejected := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", unknownOperationStatus) != nil
	return map[string]any{
		"server_resource_valid": true, "operation_resource_valid": true,
		"unknown_optional_top_level_fields_ignored": true, "valid_request_accepted": requestValid,
		"unknown_request_field_rejected":         unknownRequestRejected,
		"unknown_nested_response_field_rejected": nestedServerRejected,
		"unknown_lifecycle_state_rejected":       unknownLifecycleRejected,
		"unknown_operation_status_rejected":      unknownOperationStatusRejected,
		"unknown_state_error_code":               "core.compatibility.unknown_state", "adapter_calls": 0,
	}, nil
}

// evaluateCompatibilityOperationUpgradeRecovery evaluates the compatibility.operation-upgrade-recovery conformance fixture.
func evaluateCompatibilityOperationUpgradeRecovery(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	operation := asObject(input["operation"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
		return nil, fmt.Errorf("in-flight operation does not match schema: %w", err)
	}
	results := make([]any, 0, len(arrayValue(input, "recovery_cases")))
	for _, rawCase := range arrayValue(input, "recovery_cases") {
		item := asObject(rawCase)
		available := false
		for _, rawCapability := range arrayValue(item, "available_capabilities") {
			capability := asObject(rawCapability)
			for _, rawStep := range arrayValue(operation, "steps") {
				step := asObject(rawStep)
				if stringValue(step, "adapter_id") == stringValue(capability, "adapter_id") &&
					stringValue(step, "capability_name") == stringValue(capability, "capability_name") &&
					stringValue(step, "capability_version") == stringValue(capability, "capability_version") &&
					stringValue(step, "status") != "succeeded" {
					available = true
				}
			}
		}
		completedSteps, unknownSteps := 0, 0
		for _, rawStep := range arrayValue(operation, "steps") {
			switch stringValue(asObject(rawStep), "status") {
			case "succeeded":
				completedSteps++
			case "outcome_unknown":
				unknownSteps++
			}
		}
		recoveryStatus := "needs_operator"
		if available && boolValue(item, "tested_migration_available") {
			recoveryStatus = "reconciling"
		} else if available {
			recoveryStatus = "reconciling"
		}
		results = append(results, map[string]any{
			"case_id": stringValue(item, "case_id"), "recorded_protocol_version_preserved": true,
			"recorded_capability_exactly_supported": available, "recovery_status": recoveryStatus,
			"completed_steps_replayed": 0, "unknown_steps_retried_before_reconciliation": false,
			"completed_step_count": completedSteps, "unknown_step_count": unknownSteps,
			"adapter_dispatches": 0,
		})
	}
	return map[string]any{"operation_schema_valid": true, "operation_id_preserved": operation["operation_id"], "cases": results}, nil
}

// evaluateTransportStorageProfile evaluates the conformance fixtures
// transport.storage.profile-negotiated, transport.storage.profile-not-selected,
// transport.storage.selection-not-offered.
func evaluateTransportStorageProfile(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	initRequest := asObject(input["initialize_request"])
	initResult := asObject(input["initialize_result"])
	descriptor := asObject(input["adapter_descriptor"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:rpc:adapter-initialize-request", initRequest); err != nil {
		return nil, fmt.Errorf("stream initialize request does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:rpc:adapter-initialize-result", initResult); err != nil {
		return nil, fmt.Errorf("stream initialize result does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:common:capability-descriptor", descriptor); err != nil {
		return nil, fmt.Errorf("stream adapter descriptor does not match schema: %w", err)
	}
	offer := asObject(asObject(initRequest["params"])["stream_transport"])
	selected := stringValue(asObject(initResult["result"]), "selected_stream_transport")
	profile := "local-unix-stream-v1"
	profileNegotiated := stringValue(offer, "profile") == profile && selected == profile
	profileDeclared := contains(arrayValue(descriptor, "stream_transport_profiles"), profile)
	hasPut, hasGet := false, false
	for _, raw := range arrayValue(descriptor, "capabilities") {
		name := stringValue(asObject(raw), "name")
		hasPut = hasPut || name == "storage.object.put"
		hasGet = hasGet || name == "storage.object.get"
	}
	storageStreamCapabilitiesAccepted := (!hasPut && !hasGet) || profileNegotiated && profileDeclared
	return map[string]any{
		"initialization_valid":                 true,
		"descriptor_valid":                     true,
		"stream_profile_negotiated":            profileNegotiated,
		"stream_profile_declared":              profileDeclared,
		"storage_put_advertised":               hasPut,
		"storage_get_advertised":               hasGet,
		"storage_stream_capabilities_accepted": storageStreamCapabilitiesAccepted,
	}, nil
}

// evaluateSafetyDestroyBlocked evaluates the conformance fixtures
// safety.destroy.blocked-stale-data-generation, safety.destroy.blocked-unverified-backup.
func evaluateSafetyDestroyBlocked(fixture map[string]any) (map[string]any, error) {
	state := asObject(fixture["initial_state"])
	backup := asObject(state["backup"])
	code := any(nil)
	if !boolValue(backup, "verified") {
		code = "core.safety.backup_not_verified"
	} else if state["data_generation_status"] != "known" || backup["data_generation_id"] != state["data_generation_id"] {
		code = "core.safety.backup_generation_mismatch"
	}
	accepted := code == nil
	lifecycle := state["lifecycle_state"]
	if accepted {
		lifecycle = "destroying"
	}
	calls := 0
	if accepted {
		calls = 1
	}
	return map[string]any{
		"accepted": accepted, "error_code": code,
		"provider_destroy_calls": calls, "logical_server_state": lifecycle,
	}, nil
}
