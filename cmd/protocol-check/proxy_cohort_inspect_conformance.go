package main

import (
	"reflect"
	"time"
)

const proxyCohortInspectInputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-cohort-inspect-input"
const proxyCohortInspectOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-cohort-inspect-output"

func evaluateProxyCohortInspect(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	cases := arrayValue(input, "evaluations")
	evaluations := make([]any, 0, len(cases))
	for _, rawEvaluation := range cases {
		evaluations = append(evaluations, inspectProxyCohortEvaluation(pkg, asObject(rawEvaluation)))
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func inspectProxyCohortEvaluation(pkg *protocolPackage, evaluation map[string]any) map[string]any {
	request := asObject(evaluation["request"])
	response := asObject(evaluation["response"])
	requestValid := pkg.validateSchema(proxyCohortInspectInputSchemaID, request) == nil
	responseValid := pkg.validateSchema(proxyCohortInspectOutputSchemaID, response) == nil
	targetBound := proxyCohortTargetBound(request, response)
	observation := inspectProxyCohortObservation(evaluation, request, response)
	membershipUsable := requestValid && responseValid && targetBound && observation.usable()

	return map[string]any{
		"case_id":                stringValue(evaluation, "case_id"),
		"request_schema_valid":   requestValid,
		"response_schema_valid":  responseValid,
		"target_bound":           targetBound,
		"observation_state":      observation.state,
		"observation_time_valid": observation.timeValid,
		"observation_fresh":      observation.fresh,
		"membership_consistent":  observation.membershipConsistent,
		"membership_usable":      membershipUsable,
	}
}

func proxyCohortTargetBound(request, response map[string]any) bool {
	cohortID := stringValue(request, "waiting_cohort_id")
	return reflect.DeepEqual(request["proxy_instance_ref"], response["proxy_instance_ref"]) &&
		cohortID != "" && cohortID == stringValue(response, "waiting_cohort_id")
}

type proxyCohortObservation struct {
	state                string
	timeValid            bool
	fresh                bool
	membershipConsistent bool
}

func (observation proxyCohortObservation) usable() bool {
	return observation.state == "observed" && observation.timeValid && observation.fresh && observation.membershipConsistent
}

func inspectProxyCohortObservation(evaluation, request, response map[string]any) proxyCohortObservation {
	state := stringValue(response, "observation_state")
	if state != "observed" {
		return proxyCohortObservation{state: state}
	}

	timeValid, fresh := proxyCohortObservationTime(
		stringValue(evaluation, "evaluation_time"),
		stringValue(response, "observed_at"),
		request["maximum_observation_age_seconds"],
	)
	return proxyCohortObservation{
		state:                state,
		timeValid:            timeValid,
		fresh:                fresh,
		membershipConsistent: proxyCohortMembershipConsistent(request, response),
	}
}

func proxyCohortObservationTime(evaluationTime, observationTime string, maximumAgeValue any) (valid, fresh bool) {
	evaluatedAt, evaluationErr := time.Parse(time.RFC3339Nano, evaluationTime)
	observedAt, observationErr := time.Parse(time.RFC3339Nano, observationTime)
	maximumAge, ageErr := integerValue(maximumAgeValue)
	if evaluationErr != nil || observationErr != nil {
		return false, false
	}
	valid = !observedAt.After(evaluatedAt)
	if ageErr != nil || maximumAge < 0 || !valid {
		return valid, false
	}
	return valid, evaluatedAt.Sub(observedAt) <= time.Duration(maximumAge)*time.Second
}

func proxyCohortMembershipConsistent(request, response map[string]any) bool {
	listed := int64(len(arrayValue(response, "player_session_ids")))
	count, countErr := integerValue(response["session_count"])
	maximum, maximumErr := integerValue(request["maximum_sessions"])
	truncated, truncatedOK := response["truncated"].(bool)
	if countErr != nil || maximumErr != nil || !truncatedOK {
		return false
	}
	return listed <= maximum && listed <= count && truncated == (listed < count)
}

func init() {
	registerEvaluator(evaluateProxyCohortInspect, "integration.proxy.cohort.inspect.binding-and-freshness")
}
