package main

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// evaluateManagementPaginationCursorScope evaluates management.pagination.cursor-scope.
func evaluateManagementPaginationCursorScope(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
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
}

// evaluateManagementAuthenticationCredentialProfiles evaluates management.authentication.credential-profiles.
func evaluateManagementAuthenticationCredentialProfiles(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
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
}

// evaluateManagementAuthenticationActorSpoofing evaluates management.authentication.actor-spoofing.
func evaluateManagementAuthenticationActorSpoofing(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
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
}
