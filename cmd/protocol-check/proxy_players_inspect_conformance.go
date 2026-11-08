package main

import (
	"reflect"
	"time"
)

const proxyBackendPlayersInspectInputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-players-inspect-input"
const proxyBackendPlayersInspectOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-players-inspect-output"

func evaluateProxyBackendPlayersInspect(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	evaluations := make([]any, 0, len(arrayValue(input, "evaluations")))
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		requestValid := pkg.validateSchema(proxyBackendPlayersInspectInputSchemaID, request) == nil
		responseValid := pkg.validateSchema(proxyBackendPlayersInspectOutputSchemaID, response) == nil
		requestTarget := asObject(request["target"])
		responseTarget := asObject(response["target"])
		requestInstance := asObject(requestTarget["instance_ref"])
		responseInstance := asObject(responseTarget["instance_ref"])
		targetBound := reflect.DeepEqual(request["target"], response["target"])
		targetEnvironmentConsistent := stringValue(requestTarget, "environment_id") != "" &&
			stringValue(requestTarget, "environment_id") == stringValue(requestInstance, "environment_id") &&
			stringValue(responseTarget, "environment_id") == stringValue(responseInstance, "environment_id")
		observationState := stringValue(response, "observation_state")
		observationTimeValid := false
		countFresh := false
		if observationState == "observed" {
			evaluatedAt, evaluationErr := time.Parse(time.RFC3339Nano, stringValue(evaluation, "evaluation_time"))
			observedAt, observationErr := time.Parse(time.RFC3339Nano, stringValue(response, "observed_at"))
			maximumAge, ageErr := integerValue(request["maximum_age_seconds"])
			if evaluationErr == nil && observationErr == nil {
				observationTimeValid = !observedAt.After(evaluatedAt)
				if ageErr == nil && maximumAge >= 0 && observationTimeValid {
					countFresh = evaluatedAt.Sub(observedAt) <= time.Duration(maximumAge)*time.Second
				}
			}
		}
		countUsable := requestValid && responseValid && targetBound && targetEnvironmentConsistent &&
			observationState == "observed" && observationTimeValid && countFresh
		caseResult := map[string]any{}
		caseResult["case_id"] = stringValue(evaluation, "case_id")
		caseResult["request_schema_valid"] = requestValid
		caseResult["response_schema_valid"] = responseValid
		caseResult["target_bound"] = targetBound
		caseResult["target_environment_consistent"] = targetEnvironmentConsistent
		caseResult["observation_state"] = observationState
		caseResult["observation_time_valid"] = observationTimeValid
		caseResult["count_fresh"] = countFresh
		caseResult["count_usable"] = countUsable
		evaluations = append(evaluations, caseResult)
	}
	return map[string]any{"evaluations": evaluations}, nil
}
