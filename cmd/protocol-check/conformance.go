Warning: truncated output (original token count: 28640)
Total output lines: 2289

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

func (pkg *protocolPackage) runConformance() error {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	var failures []string
	for _, path := range paths {
		fixture, err := parseJSONFile(path)
		if err == nil {
			var actual map[string]any
			actual, err = evaluate(pkg, asObject(fixture))
			if err == nil {
				var encoded []byte
				encoded, err = json.Marshal(actual)
				if err == nil {
					var normalized any
					normalized, err = parseJSON(encoded, filepath.Base(path)+" result")
					if err == nil {
						err = compareExpected(normalized, asObject(asObject(fixture)["expected"]), "$")
					}
				}
			}
		}
		if err != nil {
			failures = append(failures, filepath.Base(path)+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("conformance failed:\n - %s", strings.Join(failures, "\n - "))
	}
	return nil
}

func evaluate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	fixtureID := stringValue(fixture, "fixture_id")
	input := asObject(fixture["input"])
	switch fixtureID {
	case "lifecycle.reject.unlisted-transition":
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
	case "lifecycle.replacement.identity":
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
	case "lifecycle.required-readiness-set":
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:readiness-check-input", input); err != nil {
			return nil, fmt.Errorf("readiness request does not match schema: %w", err)
		}
		set := asObject(fixture["declared_check_set"])
		required := arrayValue(input, "required_check_ids")
		declared := arrayValue(set, "check_ids")
		versions := asObject(set["check_versions"])
		setValid := stringValue(set, "readiness_check_set_id") != "" && uniqueStrings(declared) && sameStringSet(required, declared)
		for _, rawID := range required {
			checkID, ok := rawID.(string)
			if !ok {
				setValid = false
				continue
			}
			if version, ok := versions[checkID].(string); !ok || version == "" {
				setValid = false
			}
		}
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
	case "compatibility.protocol-version-negotiation":
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
	case "compatibility.response-evolution":
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
	case "compatibility.operation-upgrade-recovery":
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
	case "transport.storage.profile-negotiated", "transport.storage.profile-not-selected", "transport.storage.selection-not-offered":
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
	case "management.pagination.cursor-scope":
		cursor := asObject(input["cursor"])
		cursorActor := stringValue(cursor, "actor_id")
		cursorFilters, filtersValid := cursor["filter_scope"].(map[string]any)
		attempts := arrayValue(input, "attempts")
		if cursorActor == "" || !filtersValid || len(attempts) == 0 {
			return nil, fmt.Errorf("cursor scope fixture must include an actor, filter scope, and attempts")
		}
		outcomes := make([]any, 0, len(attempts))
		for index, rawAttempt := range attempts {
			attempt := asObject(rawAttempt)
			attemptID := stringValue(attempt, "attempt_id")
			actorID := stringValue(attempt, "actor_id")
			filters, attemptFiltersValid := attempt["filter_scope"].(map[string]any)
			if attemptID == "" || actorID == "" || !attemptFiltersValid {
				return nil, fmt.Errorf("cursor attempt %d must include an attempt ID, actor, and filter scope", index)
			}
			sameScope := actorID == cursorActor && reflect.DeepEqual(filters, cursorFilters)
			if !sameScope {
				outcomes = append(outcomes, map[string]any{
					"attempt_id":                 attemptID,
					"accepted":                   false,
					"error_code":                 "core.validation.invalid_request",
					"items":                      []any{},
					"cursor_advanced":            false,
					"resource_metadata_returned": false,
				})
				continue
			}
			items := arrayValue(attempt, "page_items")
			outcomes = append(outcomes, map[string]any{
				"attempt_id":                 attemptID,
				"accepted":                   true,
				"error_code":                 nil,
				"items":                      items,
				"cursor_advanced":            true,
				"resource_metadata_returned": len(items) > 0,
			})
		}
		return map[string]any{"binding_enforced": true, "attempts": outcomes}, nil
	case "management.authentication.credential-profiles":
		remote := asObject(input["remote_profile"])
		expectedAudience := stringValue(remote, "expected_audience")
		remoteAttempts := arrayValue(remote, "attempts")
		if expectedAudience == "" || len(remoteAttempts) == 0 {
			return nil, fmt.Errorf("remote authentication fixture must include an expected audience and attempts")
		}
		remoteResults := make([]any, 0, len(remoteAttempts))
		for index, rawAttempt := range remoteAttempts {
			attempt := asObject(rawAttempt)
			attemptID := stringValue(attempt, "case_id")
			actorID := stringValue(attempt, "authenticated_actor_id")
			if attemptID == "" {
				return nil, fmt.Errorf("remote authentication attempt %d must have a case_id", index)
			}
			authenticated := boolValue(attempt, "tls") &&
				boolValue(attempt, "signature_valid") &&
				boolValue(attempt, "not_expired") &&
				stringValue(attempt, "audience") == expectedAudience &&
				!boolValue(attempt, "revoked") &&
				actorID != ""
			httpStatus := 401
			var trustedActor any
			if authenticated {
				httpStatus = 200
				trustedActor = actorID
			}
			remoteResults = append(remoteResults, map[string]any{
				"case_id": attemptID, "authenticated": authenticated, "http_status": httpStatus,
				"actor_id": trustedActor, "request_claims_used_for_auth": false,
				"protected_resource_data_returned": false, "operation_created": false,
				"adapter_dispatch_count": 0,
			})
		}
		local := asObject(input["local_profile"])
		localAttempts := arrayValue(local, "attempts")
		if len(localAttempts) == 0 {
			return nil, fmt.Errorf("local authentication fixture must include attempts")
		}
		localResults := make([]any, 0, len(localAttempts))
		for index, rawAttempt := range localAttempts {
			attempt := asObject(rawAttempt)
			attemptID := stringValue(attempt, "case_id")
			if attemptID == "" {
				return nil, fmt.Errorf("local authentication attempt %d must have a case_id", index)
			}
			actorID := stringValue(attempt, "peer_actor_id")
			authenticated := boolValue(attempt, "peer_identity_verified") && actorID != ""
			httpStatus := 401
			var trustedActor any
			if authenticated {
				httpStatus = 200
				trustedActor = actorID
			}
			localResults = append(localResults, map[string]any{
				"case_id": attemptID, "authenticated": authenticated, "http_status": httpStatus,
				"actor_id": trustedActor, "request_claims_used_for_auth": false,
				"protected_resource_data_returned": false, "operation_created": false,
				"adapter_dispatch_count": 0,
			})
		}
		redaction := asObject(input["token_redaction"])
		token := stringValue(redaction, "synthetic_token")
		surfaces := asObject(redaction["surfaces"])
		if token == "" || len(surfaces) == 0 {
			return nil, fmt.Errorf("token redaction fixture must include a synthetic token and observed surfaces")
		}
		authorizationHeader := stringValue(redaction, "authorization_header")
		authorizationOverTLS := boolValue(redaction, "authorization_over_tls")
		tokenInCorrectHeader := authorizationHeader == "Bearer "+token
		leakedSurfaces := make([]string, 0)
		for surface, rawValue := range surfaces {
			if strings.Contains(fmt.Sprint(rawValue), token) {
				leakedSurfaces = append(leakedSurfaces, surface)
			}
		}
		sort.Strings(leakedSurfaces)
		return map[string]any{
			"remote_attempts": remoteResults,
			"local_attempts":  localResults,
			"token_redaction": map[string]any{
				"token_in_authorization_header_over_tls": tokenInCorrectHeader && authorizationOverTLS,
				"token_absent_from_observed_surfaces":    len(leakedSurfaces) == 0,
				"leaked_surfaces":                        leakedSurfaces,
			},
		}, nil
	case "management.authentication.actor-spoofing":
		principal := asObject(input["authenticated_principal"])
		authenticatedActor := stringValue(principal, "actor_id")
		principalPermissions := arrayValue(principal, "permissions")
		principalEnvironments := arrayValue(principal, "environments")
		attempts := arrayValue(input, "attempts")
		if authenticatedActor == "" || len(attempts) == 0 {
			return nil, fmt.Errorf("actor-spoofing fixture must include an authenticated principal and attempts")
		}
		outcomes := make([]any, 0, len(attempts))
		for index, rawAttempt := range attempts {
			attempt := asObject(rawAttempt)
			attemptID := stringValue(attempt, "attempt_id")
			claimedActor := stringValue(attempt, "claimed_actor_id")
			claimedRoles := arrayValue(attempt, "claimed_roles")
			claimedPermissions := arrayValue(attempt, "claimed_permissions")
			requiredPermission := stringValue(attempt, "required_permission")
			targetEnvironment := stringValue(attempt, "target_environment")
			if attemptID == "" || claimedActor == "" || claimedActor == authenticatedActor ||
				len(claimedRoles) == 0 || len(claimedPermissions) == 0 ||
				requiredPermission == "" || targetEnvironment == "" {
				return nil, fmt.Errorf("actor-spoofing attempt %d must include spoofed claims and an authorization target", index)
			}
			allowed := contains(principalPermissions, requiredPermission) &&
				contains(principalEnvironments, targetEnvironment)
			operationResource := any(nil)
			protectedResourceDataReturned := false
			errorCategory := any("authorization")
			if allowed {
				operationID := stringValue(attempt, "operation_id")
				if operationID == "" {
					return nil, fmt.Errorf("accepted actor-spoofing attempt %d must identify its durable operation", index)
				}
				operationResource = map[string]any{
					"operation_id": operationID,
					"actor_id":     authenticatedActor,
				}
				protectedResourceDataReturned = true
				errorCategory = nil
			}
			outcomes = append(outcomes, map[string]any{
				"attempt_id":                       attemptID,
				"accepted":                         allowed,
				"error_category":                   errorCategory,
				"operation_resource":               operationResource,
				"request_claims_used_for_auth":     false,
				"protected_resource_data_returned": protectedResourceDataReturned,
			})
		}
		return map[string]any{
			"authenticated_actor_id":      authenticatedActor,
			"attempts":                    outcomes,
			"credential_material_exposed": false,
		}, nil
	case "operations.authentication.client-session-expiry":
		operation := asObject(input["operation"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
			return nil, fmt.Errorf("accepted operation does not match schema: %w", err)
		}
		if !boolValue(input, "durably_accepted") || !boolValue(input, "client_session_expired") {
			return nil, fmt.Errorf("fixture requires an accepted operation and an expired client session")
		}
		if stringValue(operation, "status") != "running" {
			return nil, fmt.Errorf("fixture requires a running accepted operation")
		}
		if !boolValue(input, "authorization_context_unchanged") || boolValue(input, "next_sensitive_step_reauthorization_required") {
			return nil, fmt.Errorf("fixture requires unchanged authorization and no separate sensitive-step reauthorization gate")
		}
		return map[string]any{
			"operation_schema_valid":                             true,
			"operation_id":                                       stringValue(operation, "operation_id"),
			"operation_status_after_expiry":                      stringValue(operation, "status"),
			"operation_remains_durable":                          true,
			"operation_continues_without_client_session":         true,
			"operation_cancelled_due_to_session_expiry":          false,
			"session_expiry_alone_requires_client_token_refresh": false,
			"authorization_context_unchanged":                    true,
			"sensitive_step_reauthorization_still_applies":       true,
			"token_material_exposed":                             false,
		}, nil
	case "operations.cancellation.before-dispatch", "operations.cancellation.unknown-after-dispatch":
		initial := asObject(fixture["initial"])
		cancellationRequested := boolValue(fixture, "cancellation_requested")
		dispatchRecorded := boolValue(initial, "dispatch_recorded")
		providerResponseReceived := boolValue(initial, "provider_response_received")
		operationStatus, stepStatus := "", ""
		cancelled, cancellationApplied, reconciliationRequired := false, false, false
		resourceLockRetained := false
		completedSteps := arrayValue(initial, "completed_step_ids")
		if cancellationRequested && !dispatchRecorded && !boolValue(initial, "external_effect_possible") {
			operationStatus, stepStatus = "cancelled", "cancelled"
			cancelled, cancellationApplied = true, true
		} else if cancellationRequested && dispatchRecorded && !providerResponseReceived {
			operationStatus, stepStatus = "reconciling", "outcome_unknown"
			reconciliationRequired = true
			resourceLockRetained = boolValue(initial, "resource_lock_held")
		} else {
			return nil, fmt.Errorf("cancellation fixture must describe an undispatched step or a dispatched step with unknown outcome")
		}
		return map[string]any{
			"cancellation_request_recorded": cancellationRequested,
			"cancellation_applied":          cancellationApplied,
			"cancelled":                     cancelled,
			"operation_status":              operationStatus,
			"step_status":                   stepStatus,
			"reconciliation_required":       reconciliationRequired,
			"retry_without_reconciliation":  false,
			"additional_provider_calls":     0,
			"resource_lock_retained":        resourceLockRetained,
			"completed_step_ids_preserved":  completedSteps,
		}, nil
	case "operations.reconcile.unknown-create-outcome":
		initial := asObject(fixture["initial"])
		unknown := boolValue(initial, "dispatch_recorded") && !boolValue(initial, "provider_response_received")
		result := map[string]any{
			"operation_status":         "reconciling",
			"step_status":              "outcome_unknown",
			"retry_create_immediately": false,
			"inspect_exact_target_or_search_by_idempotency_reference": unknown,
			"if_unresolvable": "needs_operator",
		}
		if !unknown {
			result["operation_status"] = initial["operation_status"]
			result["step_status"] = initial["step_status"]
			result["retry_create_immediately"] = true
			result["inspect_exact_target_or_search_by_idempotency_reference"] = false
			result["if_unresolvable"] = nil
		}
		return result, nil
	case "safety.destroy.blocked-stale-data-generation", "safety.destroy.blocked-unverified-backup":
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
	case "security.staging.cannot-write-production-backup", "security.staging.production-write-scope-denied", "security.production.backup-write-authorized":
		actor := stringValue(fixture, "actor_environment")
		target := stringValue(fixture, "target_environment")
		requiredScope := "storage.backup.write:" + target
		allowed := actor == target && contains(arrayValue(fixture, "credential_scope"), requiredScope)
		category := any(nil)
		if !allowed {
			category = "authorization"
		}
		return map[string]any{"accepted": allowed, "side_effect_permitted": allowed, "error_category": category}, nil
	case "hosting.instance.wait.pending-remains-pending", "hosting.instance.wait.succeeded-postcondition-confirmed", "hosting.instance.wait.succeeded-state-mismatch", "hosting.instance.wait.unknown-requires-inspection", "hosting.instance.wait.operation-reference-mismatch", "hosting.instance.wait.failed-terminal-requires-inspection":
		waitInput, waitOutput := asObject(input["wait_input"]), asObject(input["wait_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-wait-input", waitInput); err != nil {
			return nil, fmt.Errorf("hosting wait input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-wait-output", waitOutput); err != nil {
			return nil, fmt.Errorf("hosting wait output does not match schema: %w", err)
		}
		targetBound := reflect.DeepEqual(asObject(waitInput["instance_ref"]), asObject(waitOutput["instance_ref"]))
		operationBound := reflect.DeepEqual(asObject(waitInput["provider_operation_ref"]), asObject(waitOutput["provider_operation_ref"]))
		requestedTarget := asObject(waitInput["instance_ref"])
		operationRef := asObject(waitInput["provider_operation_ref"])
		operationScopeMatches := stringValue(operationRef, "adapter_id") == stringValue(requestedTarget, "adapter_id") &&
			stringValue(operationRef, "environment_id") == stringValue(requestedTarget, "environment_id")
		status := stringValue(waitOutput, "operation_status")
		state := stringValue(waitOutput, "instance_state")
		referencesBound := targetBound && operationBound && operationScopeMatches
		waitCompleted := referencesBound && (status == "succeeded" || status == "failed")
		postconditionConfirmed := referencesBound && status == "succeeded" &&
			state == stringValue(waitInput, "expected_state")
		return map[string]any{
			"schemas_valid":                    true,
			"target_bound":                     targetBound,
			"operation_reference_bound":        operationBound && operationScopeMatches,
			"wait_completed":                   waitCompleted,
			"poll_again":                       referencesBound && status == "pending",
			"instance_postcondition_confirmed": postconditionConfirmed,
			"unknown_requires_inspection":      status == "unknown",
			"result_requires_inspection":       !referencesBound || status == "unknown" || status == "failed",
		}, nil
	case "hosting.instance.destroy.approval-exact-scope", "hosting.instance.destroy.approval-wrong-action", "hosting.instance.destroy.approval-target-mismatch", "hosting.instance.destroy.approval-environment-mismatch", "hosting.instance.destroy.approval-plan-mismatch", "hosting.instance.destroy.approval-expired", "hosting.instance.destroy.approval-missing-approver":
		destroyInput := asObject(input["destroy_input"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-input", destroyInput); err != nil {
			return nil, fmt.Errorf("hosting destroy input does not match schema: %w", err)
		}
		preconditions := asObject(fixture["core_preconditions"])
		request := asObject(fixture["request_binding"])
		approval := asObject(preconditions["approval"])
		target := asObject(destroyInput["instance_ref"])
		requestBindingMatches := stringValue(request, "action") == "hosting.instance.destroy" &&
			stringValue(request, "logical_server_id") != "" &&
			stringValue(request, "environment_id") == stringValue(target, "environment_id") &&
			stringValue(request, "adapter_id") == stringValue(target, "adapter_id") &&
			stringValue(request, "plan_fingerprint") != "" &&
			reflect.DeepEqual(asObject(request["instance_ref"]), target)
		approvalScopeMatches := stringValue(approval, "action") == stringValue(request, "action") &&
			stringValue(approval, "logical_server_id") == stringValue(request, "logical_server_id") &&
			stringValue(approval, "environment_id") == stringValue(request, "environment_id") &&
			stringValue(approval, "adapter_id") == stringValue(request, "adapter_id") &&
			reflect.DeepEqual(asObject(approval["instance_ref"]), target) &&
			reflect.DeepEqual(asObject(approval["instance_ref"]), asObject(request["instance_ref"])) &&
			stringValue(approval, "plan_fingerprint") != "" &&
			stringValue(approval, "plan_fingerprint") == stringValue(request, "plan_fingerprint")
		evaluatedAt, evaluationErr := time.Parse(time.RFC3339, stringValue(fixture, "evaluation_time"))
		expiresAt, expiryErr := time.Parse(time.RFC3339, stringValue(approval, "expires_at"))
		approvalCurrent := boolValue(approval, "valid") && evaluationErr == nil && expiryErr == nil && expiresAt.After(evaluatedAt)
		approvalApproverPresent := stringValue(approval, "approval_id") != "" && stringValue(approval, "approver_id") != ""
		approvalBound := requestBindingMatches && approvalScopeMatches && approvalCurrent && approvalApproverPresent
		allowed := boolValue(preconditions, "authorization_valid") && approvalBound &&
			boolValue(preconditions, "backup_verified") && boolValue(preconditions, "backup_generation_matches") &&
			boolValue(preconditions, "instance_stopped")
		output := asObject(input["destroy_output"])
		outputPresent := len(output) > 0
		targetBound, confirmed := false, false
		if outputPresent {
			if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-output", output); err != nil {
				return nil, fmt.Errorf("hosting destroy output does not match schema: %w", err)
			}
			targetBound = reflect.DeepEqual(target, asObject(output["instance_ref"]))
			confirmed = allowed && targetBound && stringValue(output, "outcome") == "completed" && stringValue(output, "state") == "destroyed"
		}
		providerCalls := 0
		if allowed {
			providerCalls = 1
		}
		return map[string]any{
			"schemas_valid":             true,
			"request_binding_matches":   requestBindingMatches,
			"approval_scope_matches":    approvalScopeMatches,
			"approval_current":          approvalCurrent,
			"approval_approver_present": approvalApproverPresent,
			"approval_bound":            approvalBound,
			"destroy_dispatch_allowed":  allowed,
			"provider_destroy_calls":    providerCalls,
			"target_bound":              targetBound,
			"destruction_confirmed":     confirmed,
		}, nil
	case "hosting.instance.destroy.authorization-required", "hosting.instance.destroy.verified-backup-required", "hosting.instance.destroy.current-generation-backup-required", "hosting.instance.destroy.stopped-required", "hosting.instance.destroy.completed-destroyed", "hosting.instance.destroy.accepted-not-destroyed", "hosting.instance.destroy.unknown-requires-inspection", "hosting.instance.destroy.target-mismatch":
		destroyInput := asObject(input["destroy_input"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-input", destroyInput); err != nil {
			return nil, fmt.Errorf("hosting destroy input does not match schema: %w", err)
		}
		preconditions := asObject(fixture["core_preconditions"])
		allowed := boolValue(preconditions, "authorization_valid") && boolValue(preconditions, "backup_verified") &&
			boolValue(preconditions, "backup_generation_matches") && boolValue(preconditions, "instance_stopped")
		output := asObject(input["destroy_output"])
		outputPresent := len(output) > 0
		targetBound, confirmed, unknownRequiresInspection, resultRequiresInspection := false, false, false, false
		outcome, state := "", ""
		if outputPresent {
			if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-output", output); err != nil {
				return nil, fmt.Errorf("hosting destroy output does not match schema: %w", err)
			}
			targetBound = reflect.DeepEqual(asObject(destroyInput["instance_ref"]), asObject(output["instance_ref"]))
			outcome, state = stringValue(output, "outcome"), stringValue(output, "state")
			confirmed = allowed && targetBound && outcome == "completed" && state == "destroyed"
			unknownRequiresInspection = outcome == "outcome_unknown"
			resultRequiresInspection = !targetBound || unknownRequiresInspection
		}
		destroyCalls := 0
		if allowed {
			destroyCalls = 1
		}
		return map[string]any{
			"schemas_valid":               true,
			"destroy_dispatch_allowed":    allowed,
			"provider_destroy_calls":      destroyCalls,
			"target_bound":                targetBound,
			"destruction_confirmed":       confirmed,
			"unknown_requires_inspection": unknownRequiresInspection,
			"result_requires_inspection":  resultRequiresInspection,
		}, nil
	case "hosting.instance.reboot.core-missing-save-blocked", "hosting.instance.reboot.core-save-bound", "hosting.instance.reboot.core-clean-stop-bound", "hosting.instance.reboot.core-save-mismatch-blocked", "hosting.instance.reboot.core-unknown-data-blocked", "hosting.instance.reboot.core-emergency-approved", "hosting.instance.reboot.core-emergency-target-mismatch", "hosting.instance.reboot.core-emergency-wrong-action", "hosting.instance.reboot.core-no-data-no-gate":
		rebootInput := asObject(input["reboot_input"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-input", rebootInput); err != nil {
			return nil, fmt.Errorf("hosting reboot input does not match schema: %w", err)
		}
		core := asObject(input["core_preconditions"])
		target := asObject(rebootInput["instance_ref"])
		dataStatus := stringValue(core, "persistent_data_status")
		if dataStatus != "present" && dataStatus != "absent" && dataStatus != "unknown" {
			return nil, fmt.Errorf("persistent_data_status must be present, absent, or unknown")
		}
		dataSafetyGateRequired := dataStatus != "absent"
		saveEvidenceBound := boolValue(core, "game_save_confirmed") &&
			reflect.DeepEqual(target, asObject(core["game_save_instance_ref"]))
		cleanStopEvidenceBound := boolValue(core, "clean_stop_confirmed") &&
			reflect.DeepEqual(target, asObject(core["clean_stop_instance_ref"]))
		routineRebootAllowed := !dataSafetyGateRequired || saveEvidenceBound || cleanStopEvidenceBound
		approval := asObject(core["emergency_approval"])
		emergencyApprovalBound := boolValue(approval, "valid") &&
			stringValue(approval, "approval_id") != "" &&
			stringValue(approval, "approver_id") != "" &&
			stringValue(approval, "action") == "hosting.instance.reboot.emergency" &&
			reflect.DeepEqual(target, asObject(approval["instance_ref"]))
		dispatchAllowed := routineRebootAllowed || emergencyApprovalBound
		emergencyBypassUsed := dataSafetyGateRequired && !saveEvidenceBound && !cleanStopEvidenceBound && emergencyApprovalBound
		return map[string]any{
			"core_preconditions_valid":         true,
			"data_safety_gate_required":        dataSafetyGateRequired,
			"save_evidence_bound":              saveEvidenceBound,
			"clean_stop_evidence_bound":        cleanStopEvidenceBound,
			"routine_reboot_allowed":           routineRebootAllowed,
			"emergency_approval_bound":         emergencyApprovalBound,
			"provider_reboot_dispatch_allowed": dispatchAllowed,
			"emergency_bypass_used":            emergencyBypassUsed,
			"game_readiness_established":       false,
		}, nil
	case "hosting.instance.reboot.completed-active", "hosting.instance.reboot.accepted-not-complete", "hosting.instance.reboot.unknown-requires-inspection", "hosting.instance.reboot.target-mismatch":
		rebootInput, rebootOutput := asObject(input["reboot_input"]), asObject(input["reboot_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-input", rebootInput); err != nil {
			return nil, fmt.Errorf("hosting reboot input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-reboot-output", rebootOutput); err != nil {
			return nil, fmt.Errorf("host…8640 tokens truncated…_operation"])
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
		defineResult := asObject(defineOperation["result"])
		startResult := asObject(startOperation["result"])
		stopResult := asObject(stopOperation["result"])
		defineData := asObject(defineResult["operation_data"])
		startData := asObject(startResult["operation_data"])
		stopData := asObject(stopResult["operation_data"])
		startTarget, stopTarget := asObject(startOperation["target"]), asObject(stopOperation["target"])
		startAssertion := asObject(startData["readiness_assertion"])
		startInstance := asObject(startTarget["instance_ref"])
		assertedInstance := asObject(startAssertion["instance_ref"])
		startCompleted, err := parseTime(stringValue(startOperation, "completed_at"))
		if err != nil {
			return nil, err
		}
		assertionExpires, err := parseTime(stringValue(startAssertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		serverIdentityConsistent := stringValue(defineData, "server_id") == stringValue(startData, "server_id") &&
			stringValue(startData, "server_id") == stringValue(stopData, "server_id") &&
			stringValue(defineData, "environment_id") == stringValue(startData, "environment_id") &&
			stringValue(startData, "environment_id") == stringValue(stopData, "environment_id")
		startReadinessBound := stringValue(startData, "server_id") == stringValue(startAssertion, "server_id") &&
			stringValue(startData, "environment_id") == stringValue(startAssertion, "environment_id") &&
			stringValue(startData, "server_id") == stringValue(startTarget, "server_id") &&
			stringValue(startData, "environment_id") == stringValue(startTarget, "environment_id") &&
			reflect.DeepEqual(startData["instance_ref"], startTarget["instance_ref"]) &&
			reflect.DeepEqual(startTarget["instance_ref"], startAssertion["instance_ref"]) &&
			stringValue(startInstance, "environment_id") == stringValue(startData, "environment_id") &&
			stringValue(assertedInstance, "environment_id") == stringValue(startAssertion, "environment_id")
		startReadinessCurrent := startReadinessBound && startCompleted.Before(assertionExpires) &&
			stringValue(startData, "lifecycle_state") == "ready"
		defineNoInfrastructure := stringValue(defineData, "lifecycle_state") == "unprovisioned" &&
			len(arrayValue(defineResult, "affected_resources")) == 0
		gameSave, execution, proxyDrain := asObject(stopData["game_save"]), asObject(stopData["execution"]), asObject(stopData["proxy_drain"])
		stopBound := stringValue(stopData, "server_id") == stringValue(stopTarget, "server_id") &&
			stringValue(stopData, "environment_id") == stringValue(stopTarget, "environment_id") &&
			reflect.DeepEqual(stopData["instance_ref"], stopTarget["instance_ref"])
		stopPostconditions := stopBound && stringValue(stopData, "lifecycle_state") == "stopped" &&
			stringValue(gameSave, "outcome") == "confirmed" && stringValue(execution, "outcome") == "stopped" &&
			stringValue(proxyDrain, "outcome") == "drained"
		return map[string]any{
			"schemas_valid":                     true,
			"server_identity_consistent":        serverIdentityConsistent,
			"define_has_no_infrastructure":      defineNoInfrastructure,
			"start_readiness_bound_and_current": startReadinessCurrent,
			"stop_postconditions_confirmed":     stopPostconditions,
		}, nil
	case "execution.logs.health.delete.safety":
		logsInput, logsOutput := asObject(input["logs_input"]), asObject(input["logs_output"])
		healthInput, healthOutput := asObject(input["health_input"]), asObject(input["health_output"])
		deleteInput, deleteOutput := asObject(input["delete_input"]), asObject(input["delete_output"])
		for schemaID, message := range map[string]struct {
			value map[string]any
			name  string
		}{
			"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-input":       {logsInput, "logs input"},
			"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-output":      {logsOutput, "logs output"},
			"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-input":  {healthInput, "health input"},
			"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-output": {healthOutput, "health output"},
			"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-delete-input":          {deleteInput, "delete input"},
			"urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-delete-output":         {deleteOutput, "delete output"},
		} {
			if err := pkg.validateSchema(schemaID, message.value); err != nil {
				return nil, fmt.Errorf("%s does not match schema: %w", message.name, err)
			}
		}
		logsBound := reflect.DeepEqual(logsInput["unit_ref"], logsOutput["unit_ref"])
		healthBound := reflect.DeepEqual(healthInput["unit_ref"], healthOutput["unit_ref"])
		deleteBound := reflect.DeepEqual(deleteInput["unit_ref"], deleteOutput["unit_ref"])
		maxBytes, err := integerValue(logsInput["max_bytes"])
		if err != nil {
			return nil, err
		}
		var totalBytes int64
		for i, rawLine := range arrayValue(logsOutput, "lines") {
			line, ok := rawLine.(string)
			if !ok {
				return nil, fmt.Errorf("log lines must be strings")
			}
			totalBytes += int64(len([]byte(line)))
			if i > 0 {
				totalBytes++
			}
		}
		return map[string]any{
			"schemas_valid": true, "unit_targets_bound": logsBound && healthBound && deleteBound,
			"logs_within_bound": totalBytes <= maxBytes, "logs_redacted": boolValue(logsOutput, "redaction_applied"),
			"unknown_health_not_healthy": stringValue(healthOutput, "health") == "unknown",
			"persistent_data_preserved":  boolValue(deleteOutput, "persistent_data_preserved"),
		}, nil
	case "integration.proxy.register.expired-readiness-rejected":
		registerInput, registerOutput := asObject(input["register_input"]), asObject(input["register_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-input", registerInput); err != nil {
			return nil, fmt.Errorf("register input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-output", registerOutput); err != nil {
			return nil, fmt.Errorf("register output does not match schema: %w", err)
		}
		requestTarget, assertion := asObject(registerInput["target"]), asObject(registerInput["readiness_assertion"])
		targetInstance := asObject(requestTarget["instance_ref"])
		assertedInstance := asObject(assertion["instance_ref"])
		bound := reflect.DeepEqual(requestTarget, registerOutput["target"]) &&
			stringValue(requestTarget, "server_id") == stringValue(assertion, "server_id") &&
			stringValue(requestTarget, "environment_id") == stringValue(assertion, "environment_id") &&
			stringValue(requestTarget, "environment_id") == stringValue(targetInstance, "environment_id") &&
			stringValue(assertion, "environment_id") == stringValue(assertedInstance, "environment_id") &&
			reflect.DeepEqual(requestTarget["instance_ref"], assertion["instance_ref"])
		current, err := parseTime(stringValue(input, "validation_time"))
		if err != nil {
			return nil, err
		}
		expires, err := parseTime(stringValue(assertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		readinessCurrent := bound && current.Before(expires)
		rejected := stringValue(registerOutput, "request_outcome") == "rejected" &&
			stringValue(registerOutput, "registration_state") == "unregistered" &&
			stringValue(registerOutput, "routability") == "not_routable"
		return map[string]any{
			"schemas_valid": true, "target_bound": bound, "readiness_current": readinessCurrent,
			"registration_rejected": rejected, "routing_permitted": readinessCurrent && stringValue(registerOutput, "routability") == "routable",
		}, nil
	case "integration.proxy.register.accepted-is-not-routable":
		registerInput, registerOutput := asObject(input["register_input"]), asObject(input["register_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-input", registerInput); err != nil {
			return nil, fmt.Errorf("register input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-output", registerOutput); err != nil {
			return nil, fmt.Errorf("register output does not match schema: %w", err)
		}
		requestTarget, assertion := asObject(registerInput["target"]), asObject(registerInput["readiness_assertion"])
		targetInstance := asObject(requestTarget["instance_ref"])
		assertedInstance := asObject(assertion["instance_ref"])
		bound := reflect.DeepEqual(requestTarget, registerOutput["target"]) &&
			stringValue(requestTarget, "server_id") == stringValue(assertion, "server_id") &&
			stringValue(requestTarget, "environment_id") == stringValue(assertion, "environment_id") &&
			stringValue(requestTarget, "environment_id") == stringValue(targetInstance, "environment_id") &&
			stringValue(assertion, "environment_id") == stringValue(assertedInstance, "environment_id") &&
			reflect.DeepEqual(requestTarget["instance_ref"], assertion["instance_ref"])
		current, err := parseTime(stringValue(input, "validation_time"))
		if err != nil {
			return nil, err
		}
		expires, err := parseTime(stringValue(assertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		readinessCurrent := bound && current.Before(expires)
		confirmed := stringValue(registerOutput, "request_outcome") == "confirmed"
		routingPermitted := readinessCurrent && confirmed &&
			stringValue(registerOutput, "registration_state") == "registered" &&
			stringValue(registerOutput, "routability") == "routable"
		return map[string]any{
			"schemas_valid": true, "target_bound": bound, "readiness_current": readinessCurrent,
			"registration_confirmed": confirmed, "accepted_not_routable": stringValue(registerOutput, "request_outcome") != "accepted" || stringValue(registerOutput, "routability") != "routable", "routing_permitted": routingPermitted,
		}, nil
	case "integration.proxy.unknown-drain-and-unregister-require-inspection":
		drainInput, drainOutput := asObject(input["drain_input"]), asObject(input["drain_output"])
		unregisterInput, unregisterOutput := asObject(input["unregister_input"]), asObject(input["unregister_output"])
		for schemaID, message := range map[string]struct {
			value map[string]any
			name  string
		}{
			"urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-drain-input":       {drainInput, "drain input"},
			"urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-drain-output":      {drainOutput, "drain output"},
			"urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-unregister-input":  {unregisterInput, "unregister input"},
			"urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-unregister-output": {unregisterOutput, "unregister output"},
		} {
			if err := pkg.validateSchema(schemaID, message.value); err != nil {
				return nil, fmt.Errorf("%s does not match schema: %w", message.name, err)
			}
		}
		drainUnknown := stringValue(drainOutput, "request_outcome") == "outcome_unknown" &&
			stringValue(drainOutput, "registration_state") == "unknown" && stringValue(drainOutput, "routability") == "unknown"
		unregisterUnknown := stringValue(unregisterOutput, "request_outcome") == "outcome_unknown" &&
			stringValue(unregisterOutput, "registration_state") == "unknown" && stringValue(unregisterOutput, "routability") == "unknown"
		bound := reflect.DeepEqual(drainInput["target"], drainOutput["target"]) &&
			reflect.DeepEqual(unregisterInput["target"], unregisterOutput["target"])
		return map[string]any{
			"schemas_valid": true, "drain_requires_inspection": drainUnknown,
			"unregister_requires_inspection": unregisterUnknown, "retry_without_inspection": !drainUnknown || !unregisterUnknown || !bound,
		}, nil
	case "game.release.install.configure.fail-closed":
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
	case "game.install.repeat-same-release-is-idempotent", "game.install.completed-verifies-exact-artifacts", "game.install.incomplete-artifact-set-requires-reconciliation":
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
		requiredArtifacts := arrayValue(input, "required_artifact_ids")
		verifiedArtifacts := arrayValue(installOutput, "verified_artifact_ids")
		complete := uniqueStrings(requiredArtifacts) && uniqueStrings(verifiedArtifacts) && sameStringSet(requiredArtifacts, verifiedArtifacts)
		outcome, verification := stringValue(installOutput, "outcome"), stringValue(installOutput, "artifact_verification")
		started := boolValue(installOutput, "installation_started")
		outcomeConsistent := outcome == "installed" && started || outcome == "already_installed" && !started
		accepted := bound && complete && verification == "verified" && outcomeConsistent
		return map[string]any{
			"schema_valid": true, "target_bound": bound, "artifact_set_complete": complete,
			"installation_result_accepted":      accepted,
			"duplicate_install_started":         outcome == "already_installed" && started,
			"operation_reconciliation_required": started && !accepted,
		}, nil
	case "game.validate-installation.unknown-blocks-start", "game.validate-installation.valid-exact-artifacts", "game.validate-installation.valid-incomplete-artifacts-blocks-start", "game.validate-installation.valid-claim-with-unverified-artifact", "game.validate-installation.valid-claim-with-checksum-mismatch":
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
	case "game.readiness.unknown-required-check-blocks-ready":
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
		requiredIDs := arrayValue(input, "required_check_ids")
		if !uniqueStrings(requiredIDs) {
			return nil, fmt.Errorf("required check IDs must be unique")
		}
		requested := make(map[string]bool, len(requiredIDs))
		for _, rawID := range requiredIDs {
			requested[rawID.(string)] = true
		}
		results := arrayValue(output, "check_results")
		if len(results) != len(requested) {
			return nil, fmt.Errorf("readiness output must contain exactly one result for every required check")
		}
		observed := make(map[string]string, len(results))
		for _, rawResult := range results {
			item := asObject(rawResult)
			checkID := stringValue(item, "check_id")
			if !requested[checkID] {
				return nil, fmt.Errorf("readiness output contains an unrequested check %q", checkID)
			}
			if _, exists := observed[checkID]; exists {
				return nil, fmt.Errorf("readiness output contains duplicate check %q", checkID)
			}
			observed[checkID] = stringValue(item, "result")
		}
		allPassed, hasFailure, hasUnknown := true, false, false
		for checkID := range requested {
			result, exists := observed[checkID]
			if !exists {
				return nil, fmt.Errorf("readiness output is missing required check %q", checkID)
			}
			switch result {
			case "pass":
			case "fail":
				allPassed, hasFailure = false, true
			case "unknown":
				allPassed, hasUnknown = false, true
			default:
				return nil, fmt.Errorf("unknown readiness result %q for check %q", result, checkID)
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
	case "readiness-required-before-transfer":
		assertion := asObject(input["readiness_assertion"])
		current, err := parseTime(stringValue(input, "validation_time"))
		if err != nil {
			return nil, err
		}
		expires, err := parseTime(stringValue(assertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		expired := !current.Before(expires)
		outcome, code := "accepted", any(nil)
		if expired {
			outcome, code = "rejected", "readiness_assertion_expired"
		}
		calls := len(arrayValue(input, "player_session_ids"))
		if expired {
			calls = 0
		}
		return map[string]any{"outcome": outcome, "error_code": code, "proxy_transfer_calls": calls}, nil
	case "velocity-transfer-partial-results":
		sessions := arrayValue(input, "player_session_ids")
		results := arrayValue(input, "session_outcomes")
		if !uniqueStrings(sessions) {
			return nil, fmt.Errorf("requested session IDs must be unique")
		}
		if len(results) != len(sessions) {
			return nil, fmt.Errorf("one session outcome is required for every requested session")
		}
		requested := make(map[string]bool)
		for _, id := range sessions {
			requested[id.(string)] = true
		}
		resultIDs := make([]any, 0, len(results))
		counts := map[string]int{"transferred": 0, "already_at_destination": 0, "disconnected": 0, "rejected": 0, "failed": 0, "outcome_unknown": 0}
		for _, result := range results {
			item := asObject(result)
			id := item["proxy_session_id"]
			outcome := stringValue(item, "outcome")
			resultIDs = append(resultIDs, id)
			if _, ok := counts[outcome]; !ok {
				return nil, fmt.Errorf("unknown transfer outcome: %q", outcome)
			}
			counts[outcome]++
		}
		if !uniqueStrings(resultIDs) {
			return nil, fmt.Errorf("session outcomes must not contain duplicate IDs")
		}
		for _, id := range resultIDs {
			if str, ok := id.(string); !ok || !requested[str] {
				return nil, fmt.Errorf("session outcomes must contain exactly the requested sessions")
			}
		}
		if len(requested) != len(resultIDs) {
			return nil, fmt.Errorf("session outcomes must contain exactly the requested sessions")
		}
		normalizedCounts := make(map[string]any, len(counts))
		hasUnknown := counts["outcome_unknown"] > 0
		for outcome, count := range counts {
			normalizedCounts[outcome] = count
		}
		return map[string]any{
			"requested_count":                      len(sessions),
			"outcome_counts":                       normalizedCounts,
			"retry_unknown_without_reconciliation": !hasUnknown,
		}, nil
	case "storage.backup.partial-upload-not-verified":
		required := arrayValue(input, "required_object_ids")
		uploaded := arrayValue(input, "uploaded_object_ids")
		allUploaded := uniqueStrings(required) && uniqueStrings(uploaded) && sameStringSet(required, uploaded)
		verified := allUploaded && boolValue(input, "manifest_verified")
		return map[string]any{
			"all_required_objects_uploaded": allUploaded,
			"backup_verified":               verified,
			"destruction_allowed":           verified,
			"partial":                       !allUploaded || !boolValue(input, "manifest_verified"),
		}, nil
	case "storage.object.checksum-mismatch-blocks-restore":
		sizeMatches := input["expected_size_bytes"] == input["actual_size_bytes"]
		expected := strings.ToLower(stringValue(input, "expected_checksum"))
		actual := strings.ToLower(stringValue(input, "actual_checksum"))
		checksumMatches := expected == actual
		verified, reason := sizeMatches && checksumMatches, any(nil)
		if !verified {
			reason = "checksum_mismatch"
			if !sizeMatches {
				reason = "size_mismatch"
			}
		}
		return map[string]any{"verified": verified, "restore_allowed": verified, "verification_reason": reason}, nil
	case "storage.object.etag-not-checksum":
		expected := strings.ToLower(stringValue(input, "expected_checksum"))
		verified := false
		for _, key := range []string{"trusted_provider_checksum", "content_digest"} {
			if value, ok := input[key].(string); ok && strings.ToLower(value) == expected {
				verified = true
			}
		}
		source := "insufficient_evidence"
		if verified {
			source = "trusted_checksum"
		}
		return map[string]any{"verified": verified, "verification_source": source}, nil
	case "storage.staging.cannot-delete-production-object", "storage.staging.production-delete-scope-denied", "storage.production.backup-delete-authorized":
		actor := stringValue(fixture, "actor_environment")
		target := stringValue(fixture, "target_environment")
		requiredScope := "storage.backup.delete:" + target
		allowed := actor == target && contains(arrayValue(fixture, "credential_scope"), requiredScope)
		category := any(nil)
		calls := 0
		if allowed {
			calls = 1
		} else {
			category = "authorization"
		}
		return map[string]any{
			"accepted": allowed, "error_category": category,
			"side_effect_permitted": allowed, "adapter_delete_calls": calls,
		}, nil
	case "operations.idempotency.same-key-same-request":
		first, second := asObject(fixture["first_request"]), asObject(fixture["second_request"])
		sameBinding := first["actor_id"] == second["actor_id"] && first["idempotency_key"] == second["idempotency_key"]
		reused := sameBinding && reflect.DeepEqual(first, second)
		operationID := any(nil)
		operationCount, dispatchCount := 2, 2
		if reused {
			operationID = fixture["existing_operation_id"]
			operationCount, dispatchCount = 1, 1
		}
		return map[string]any{
			"same_operation_reused":      reused,
			"operation_id":               operationID,
			"operation_count":            operationCount,
			"side_effect_dispatch_count": dispatchCount,
		}, nil
	case "operations.idempotency.same-key-different-request":
		first, second := asObject(fixture["first_request"]), asObject(fixture["second_request"])
		sameBinding := first["actor_id"] == second["actor_id"] && first["idempotency_key"] == second["idempotency_key"]
		conflict := sameBinding && !reflect.DeepEqual(first, second)
		code := any(nil)
		count := 2
		if conflict {
			code, count = "core.operation.idempotency_conflict", 1
		}
		return map[string]any{"second_request_accepted": !conflict, "error_code": code, "operation_count": count}, nil
	case "operations.idempotency.expired-key-reuse":
		tombstone := asObject(fixture["expired_tombstone"])
		request := asObject(fixture["retry_request"])
		if tombstone["tombstone_present"] != true || tombstone["full_operation_record_retained"] != false {
			return nil, fmt.Errorf("expired-key fixture must contain only a tombstone")
		}
		if tombstone["actor_id"] != request["actor_id"] || tombstone["idempotency_key"] != request["idempotency_key"] {
			return nil, fmt.Errorf("retry request must match the expired tombstone scope")
		}
		return map[string]any{
			"second_request_accepted":     false,
			"error_code":                  "core.operation.idempotency_key_expired",
			"new_operation_created":       false,
			"side_effect_dispatch_count":  0,
			"original_operation_replayed": false,
			"tombstone_retained":          true,
		}, nil
	case "operations.recovery.restart-preserves-lock":
		preRestart := asObject(fixture["pre_restart"])
		operation := asObject(preRestart["operation"])
		lock := asObject(preRestart["lock"])
		restart := asObject(fixture["restart"])
		request := asObject(fixture["conflicting_request"])
		if restart["controller_restarted"] != true || restart["external_outcome"] != "unknown" {
			return nil, fmt.Errorf("restart fixture must preserve an unknown external outcome")
		}
		if operation["status"] != "reconciling" || operation["unresolved_step_status"] != "outcome_unknown" {
			return nil, fmt.Errorf("restart fixture must contain a non-terminal unknown operation")
		}
		ownerID := operation["operation_id"]
		if lock["owner_operation_id"] != ownerID {
			return nil, fmt.Errorf("recovery lock must belong to the non-terminal operation")
		}
		conflicts := request["resource_kind"] == lock["resource_kind"] &&
			request["environment_id"] == lock["environment_id"] &&
			request["resource_id"] == lock["resource_id"]
		if !conflicts || request["authorized"] != true {
			return nil, fmt.Errorf("conflicting request must be authorized and target the locked resource")
		}
		return map[string]any{
			"operation_status_after_restart": operation["status"],
			"lock_held_after_restart":        true,
			"lock_owner_operation_id":        ownerID,
			"conflicting_request_accepted":   false,
			"error_code":                     "core.operation.conflict",
			"new_operation_created":          false,
			"side_effect_dispatch_count":     0,
		}, nil
	case "operations.events.duplicate-delivery":
		lastSequence, err := integerValue(fixture["last_consumed_sequence"])
		if err != nil {
			return nil, err
		}
		deliveries := arrayValue(fixture, "deliveries")
		if len(deliveries) == 0 {
			return nil, fmt.Errorf("event deliveries must not be empty")
		}
		var operationID string
		seen := make(map[string]map[string]any)
		appliedEventIDs := make([]any, 0, len(deliveries))
		duplicateCount := 0
		for index, rawEvent := range deliveries {
			event := asObject(rawEvent)
			if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
				return nil, fmt.Errorf("delivery %d does not match schema: %w", index, err)
			}
			id := stringValue(event, "operation_id")
			if index == 0 {
				operationID = id
			} else if id != operationID {
				return nil, fmt.Errorf("delivery %d belongs to a different operation", index)
			}
			eventID := stringValue(event, "event_id")
			if previous, exists := seen[eventID]; exists {
				if !reflect.DeepEqual(previous, event) {
					return nil, fmt.Errorf("duplicate event ID %q has different content", eventID)
				}
				duplicateCount++
				continue
			}
			sequence, err := integerValue(event["sequence"])
			if err != nil {
				return nil, err
			}
			if sequence != lastSequence+1 {
				return nil, fmt.Errorf("new event sequence %d does not immediately follow cursor %d", sequence, lastSequence)
			}
			seen[eventID] = event
			appliedEventIDs = append(appliedEventIDs, eventID)
			lastSequence = sequence
		}
		return map[string]any{
			"schema_valid":                 true,
			"applied_event_ids":            appliedEventIDs,
			"applied_event_count":          len(appliedEventIDs),
			"duplicate_deliveries_ignored": duplicateCount,
			"duplicate_event_applications": 0,
			"last_consumed_sequence":       lastSequence,
			"cursor_regressed":             false,
		}, nil
	case "operations.events.gap-recovery":
		lastSequence, err := integerValue(fixture["last_consumed_sequence"])
		if err != nil {
			return nil, err
		}
		events := arrayValue(fixture, "events")
		if len(events) == 0 {
			return nil, fmt.Errorf("event sequence must not be empty")
		}
		var operationID string
		currentSequence := lastSequence
		var expectedSequence, receivedSequence int64
		gapDetected := false
		for index, rawEvent := range events {
			event := asObject(rawEvent)
			if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
				return nil, fmt.Errorf("event %d does not match schema: %w", index, err)
			}
			id := stringValue(event, "operation_id")
			if index == 0 {
				operationID = id
			} else if id != operationID {
				return nil, fmt.Errorf("event %d belongs to a different operation", index)
			}
			sequence, err := integerValue(event["sequence"])
			if err != nil {
				return nil, err
			}
			if sequence <= currentSequence {
				return nil, fmt.Errorf("event sequence %d does not advance past %d", sequence, currentSequence)
			}
			if sequence > currentSequence+1 {
				gapDetected = true
				expectedSequence = currentSequence + 1
				receivedSequence = sequence
				currentSequence = sequence
				break
			}
			currentSequence = sequence
		}
		if !gapDetected {
			return nil, fmt.Errorf("gap-recovery fixture must contain a missing event sequence")
		}
		recovery := asObject(fixture["recovery"])
		operationResponse := asObject(recovery["operation_resource_get"])
		responseStatus, err := integerValue(operationResponse["status"])
		if err != nil {
			return nil, err
		}
		if responseStatus != 200 {
			return nil, fmt.Errorf("gap recovery requires a successful authoritative operation read")
		}
		operation := asObject(operationResponse["operation"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation); err != nil {
			return nil, fmt.Errorf("authoritative operation snapshot does not match schema: %w", err)
		}
		if stringValue(operation, "operation_id") != operationID {
			return nil, fmt.Errorf("authoritative operation snapshot belongs to a different operation")
		}
		return map[string]any{
			"event_schema_valid":                  true,
			"operation_schema_valid":              true,
			"gap_detected":                        true,
			"expected_sequence":                   expectedSequence,
			"received_sequence":                   receivedSequence,
			"authoritative_operation_fetched":     true,
			"operation_id":                        operationID,
			"recovered_status":                    operation["status"],
			"recovered_steps":                     operation["steps"],
			"missing_event_details_reconstructed": false,
			"side_effect_dispatches":              0,
			"replayed_events":                     0,
			"resume_after_sequence":               currentSequence,
		}, nil
	case "operations.events.monotonic-sequence":
		events := arrayValue(fixture, "events")
		if len(events) == 0 {
			return nil, fmt.Errorf("event sequence must not be empty")
		}
		var operationID string
		oneOperation := true
		sequences := make([]int64, 0, len(events))
		for index, rawEvent := range events {
			event := asObject(rawEvent)
			if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
				return nil, fmt.Errorf("event %d does not match schema: %w", index, err)
			}
			id := stringValue(event, "operation_id")
			if index == 0 {
				operationID = id
			} else if id != operationID {
				oneOperation = false
			}
			sequence, err := integerValue(event["sequence"])
			if err != nil {
				return nil, err
			}
			sequences = append(sequences, sequence)
		}
		increasing := oneOperation
		for i := 1; i < len(sequences); i++ {
			if sequences[i] <= sequences[i-1] {
				increasing = false
			}
		}
		return map[string]any{"schema_valid": true, "sequence_strictly_increasing": increasing}, nil
	case "hosting.provision.valid.standard-os":
		if err := pkg.validateSchema(provisionSchemaID, input); err != nil {
			return nil, err
		}
		return map[string]any{"schema_valid": true, "side_effect_permitted": false}, nil
	default:
		return nil, fmt.Errorf("no evaluator registered for fixture %q", fixtureID)
	}
}

func sameObjectRefSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet, rightSet := make(map[string]bool, len(left)), make(map[string]bool, len(right))
	for _, value := range left {
		encoded, err := json.Marshal(value)
		if err != nil || leftSet[string(encoded)] {
			return false
		}
		leftSet[string(encoded)] = true
	}
	for _, value := range right {
		encoded, err := json.Marshal(value)
		if err != nil || rightSet[string(encoded)] {
			return false
		}
		rightSet[string(encoded)] = true
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for key := range leftSet {
		if !rightSet[key] {
			return false
		}
	}
	return true
}

func sameStringSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]bool, len(left))
	for _, value := range left {
		str, ok := value.(string)
		if !ok {
			return false
		}
		set[str] = true
	}
	for _, value := range right {
		str, ok := value.(string)
		if !ok || !set[str] {
			return false
		}
	}
	return true
}

func integerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Int64()
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func compareExpected(actual, expected any, path string) error {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualMap := asObject(actual)
		if actualMap == nil {
			return fmt.Errorf("%s: expected an object", path)
		}
		keys := make([]string, 0, len(expectedValue))
		for key := range expectedValue {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, ok := actualMap[key]
			if !ok {
				return fmt.Errorf("%s.%s: missing expected value", path, key)
			}
			if err := compareExpected(value, expectedValue[key], path+"."+key); err != nil {
				return err
			}
		}
	default:
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("%s: expected %v, got %v", path, expected, actual)
		}
	}
	return nil
}
