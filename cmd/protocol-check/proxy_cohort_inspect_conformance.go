package main

import (
	"reflect"
	"time"
)

const proxyCohortInspectInputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-cohort-inspect-input"
const proxyCohortInspectOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-cohort-inspect-output"

func evaluateProxyCohortInspect(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	evaluations := make([]any, 0, len(arrayValue(input, "evaluations")))
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		requestValid := pkg.validateSchema(proxyCohortInspectInputSchemaID, request) == nil
		responseValid := pkg.validateSchema(proxyCohortInspectOutputSchemaID, response) == nil
		targetBound := reflect.DeepEqual(request["proxy_instance_ref"], response["proxy_instance_ref"]) &&
			stringValue(request, "waiting_cohort_id") != "" &&
			stringValue(request, "waiting_cohort_id") == stringValue(response, "waiting_cohort_id")
		observationState := stringValue(response, "observation_state")
		observationTimeValid := false
		observationFresh := false
		membershipConsistent := false
		if observationState == "observed" {
			evaluatedAt, evaluationErr := time.Parse(time.RFC3339Nano, stringValue(evaluation, "evaluation_time"))
			observedAt, observationErr := time.Parse(time.RFC3339Nano, stringValue(response, "observed_at"))
			maximumAge, ageErr := integerValue(request["maximum_observation_age_seconds"])
			if evaluationErr == nil && observationErr == nil {
				observationTimeValid = !observedAt.After(evaluatedAt)
				if ageErr == nil && maximumAge >= 0 && observationTimeValid {
					observationFresh = evaluatedAt.Sub(observedAt) <= time.Duration(maximumAge)*time.Second
				}
			}
			listed := len(arrayValue(response, "player_session_ids"))
			count, countErr := integerValue(response["session_count"])
			maximum, maximumErr := integerValue(request["maximum_sessions"])
			truncated, truncatedOK := response["truncated"].(bool)
			membershipConsistent = countErr == nil && maximumErr == nil && truncatedOK &&
				int64(listed) <= maximum && int64(listed) <= count && truncated == (int64(listed) < count)
		}
		membershipUsable := requestValid && responseValid && targetBound && observationState == "observed" &&
			observationTimeValid && observationFresh && membershipConsistent
		evaluations = append(evaluations, map[string]any{
			"case_id":                stringValue(evaluation, "case_id"),
			"request_schema_valid":   requestValid,
			"response_schema_valid":  responseValid,
			"target_bound":           targetBound,
			"observation_state":      observationState,
			"observation_time_valid": observationTimeValid,
			"observation_fresh":      observationFresh,
			"membership_consistent":  membershipConsistent,
			"membership_usable":      membershipUsable,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func init() {
	registerEvaluator(evaluateProxyCohortInspect, "integration.proxy.cohort.inspect.binding-and-freshness")
}
