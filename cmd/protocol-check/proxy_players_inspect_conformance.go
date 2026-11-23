package main

import (
	"reflect"
	"time"
)

const proxyBackendPlayersInspectInputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-players-inspect-input"
const proxyBackendPlayersInspectOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-players-inspect-output"

func evaluateProxyBackendPlayersInspect(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	cases := arrayValue(input, "evaluations")
	evaluations := make([]any, 0, len(cases))
	for _, rawEvaluation := range cases {
		evaluations = append(evaluations, evaluateProxyPlayerCount(pkg, asObject(rawEvaluation)))
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func evaluateProxyPlayerCount(pkg *protocolPackage, evaluation map[string]any) map[string]any {
	request := asObject(evaluation["request"])
	response := asObject(evaluation["response"])
	requestValid := pkg.validateSchema(proxyBackendPlayersInspectInputSchemaID, request) == nil
	responseValid := pkg.validateSchema(proxyBackendPlayersInspectOutputSchemaID, response) == nil
	requestTarget := asObject(request["target"])
	responseTarget := asObject(response["target"])
	requestInstance := asObject(requestTarget["instance_ref"])
	responseInstance := asObject(responseTarget["instance_ref"])
	targetBound := reflect.DeepEqual(request["target"], response["target"])
	requestEnvironment := stringValue(requestTarget, "environment_id")
	targetEnvironmentConsistent := requestEnvironment != "" &&
		requestEnvironment == stringValue(requestInstance, "environment_id") &&
		stringValue(responseTarget, "environment_id") == stringValue(responseInstance, "environment_id")

	observationState := stringValue(response, "observation_state")
	observationTimeValid, countFresh := proxyPlayerCountFreshness(evaluation, request, response, observationState)
	targetValid := targetBound && targetEnvironmentConsistent
	countUsable := requestValid && responseValid && targetValid && countFresh

	return map[string]any{
		"case_id":                       stringValue(evaluation, "case_id"),
		"request_schema_valid":          requestValid,
		"response_schema_valid":         responseValid,
		"target_bound":                  targetBound,
		"target_environment_consistent": targetEnvironmentConsistent,
		"observation_state":             observationState,
		"observation_time_valid":        observationTimeValid,
		"count_fresh":                   countFresh,
		"count_usable":                  countUsable,
	}
}

func proxyPlayerCountFreshness(evaluation, request, response map[string]any, observationState string) (timeValid, countFresh bool) {
	if observationState != "observed" {
		return false, false
	}
	evaluatedAt, evaluationErr := time.Parse(time.RFC3339Nano, stringValue(evaluation, "evaluation_time"))
	observedAt, observationErr := time.Parse(time.RFC3339Nano, stringValue(response, "observed_at"))
	if evaluationErr != nil || observationErr != nil || observedAt.After(evaluatedAt) {
		return false, false
	}
	maximumAge, ageErr := integerValue(request["maximum_age_seconds"])
	if ageErr != nil || maximumAge < 0 {
		return true, false
	}
	return true, evaluatedAt.Sub(observedAt) <= time.Duration(maximumAge)*time.Second
}

func init() {
	registerEvaluator(evaluateProxyBackendPlayersInspect, "integration.proxy.players.inspect.fresh-count-and-target-binding")
}
