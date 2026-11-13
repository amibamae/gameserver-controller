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
