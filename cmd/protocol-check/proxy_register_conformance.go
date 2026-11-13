package main

import (
	"fmt"
	"reflect"
)

// Register the proxy registration evaluators defined in this file.
func init() {
	registerEvaluator(evaluateIntegrationProxyRegisterExpiredReadinessRejected, "integration.proxy.register.expired-readiness-rejected")
	registerEvaluator(evaluateIntegrationProxyRegisterAcceptedIsNotRoutable, "integration.proxy.register.accepted-is-not-routable")
	registerEvaluator(evaluateIntegrationProxyUnknownDrainAndUnregisterRequireInspection, "integration.proxy.unknown-drain-and-unregister-require-inspection")
}

// evaluateIntegrationProxyRegisterExpiredReadinessRejected evaluates the integration.proxy.register.expired-readiness-rejected conformance fixture.
func evaluateIntegrationProxyRegisterExpiredReadinessRejected(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	registerInput, registerOutput := asObject(input["register_input"]), asObject(input["register_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-input", registerInput); err != nil {
		return nil, fmt.Errorf("register input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-output", registerOutput); err != nil {
		return nil, fmt.Errorf("register output does not match schema: %w", err)
	}
	requestTarget, assertion := asObject(registerInput["target"]), asObject(registerInput["readiness_assertion"])
	bound := reflect.DeepEqual(requestTarget, registerOutput["target"]) &&
		assertionNamesTarget(requestTarget, requestTarget["instance_ref"], assertion, assertion["instance_ref"])
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
}

// evaluateIntegrationProxyRegisterAcceptedIsNotRoutable evaluates the integration.proxy.register.accepted-is-not-routable conformance fixture.
func evaluateIntegrationProxyRegisterAcceptedIsNotRoutable(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	registerInput, registerOutput := asObject(input["register_input"]), asObject(input["register_output"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-input", registerInput); err != nil {
		return nil, fmt.Errorf("register input does not match schema: %w", err)
	}
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-register-output", registerOutput); err != nil {
		return nil, fmt.Errorf("register output does not match schema: %w", err)
	}
	requestTarget, assertion := asObject(registerInput["target"]), asObject(registerInput["readiness_assertion"])
	bound := reflect.DeepEqual(requestTarget, registerOutput["target"]) &&
		assertionNamesTarget(requestTarget, requestTarget["instance_ref"], assertion, assertion["instance_ref"])
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
}

// evaluateIntegrationProxyUnknownDrainAndUnregisterRequireInspection evaluates the integration.proxy.unknown-drain-and-unregister-require-inspection conformance fixture.
func evaluateIntegrationProxyUnknownDrainAndUnregisterRequireInspection(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
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
}

// assertionNamesTarget: a readiness assertion names the owner's server and
// environment, both instances are in that environment, and the asserted
// instance is the owner's instance. Callers pass the instance values in the
// form they compare: raw references, or decoded objects.
func assertionNamesTarget(owner map[string]any, instance any, assertion map[string]any, assertedInstance any) bool {
	return stringValue(owner, "server_id") == stringValue(assertion, "server_id") &&
		stringValue(owner, "environment_id") == stringValue(assertion, "environment_id") &&
		stringValue(owner, "environment_id") == stringValue(asObject(instance), "environment_id") &&
		stringValue(assertion, "environment_id") == stringValue(asObject(assertedInstance), "environment_id") &&
		reflect.DeepEqual(instance, assertedInstance)
}
