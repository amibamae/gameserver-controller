package main

import (
	"reflect"
	"time"
)

const dnsRecordUpsertInputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:dns-record-upsert-input"
const dnsRecordUpsertOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:dns-record-upsert-output"

func evaluateDNSRecordUpsert(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	evaluations := make([]any, 0, len(arrayValue(input, "evaluations")))
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		requestValid := pkg.validateSchema(dnsRecordUpsertInputSchemaID, request) == nil
		responseValid := pkg.validateSchema(dnsRecordUpsertOutputSchemaID, response) == nil
		recordBound := reflect.DeepEqual(request["record"], response["record"])
		environment := stringValue(request, "environment_id")
		zone := asObject(asObject(request["record"])["zone_ref"])
		environmentConsistent := environment != "" && stringValue(zone, "environment_id") == environment
		if instance, ok := request["instance_ref"].(map[string]any); ok {
			environmentConsistent = environmentConsistent && stringValue(instance, "environment_id") == environment
		}
		purpose := stringValue(request, "purpose")
		readinessRequired := purpose == "direct_join"
		readinessSatisfied := !readinessRequired
		if readinessRequired {
			readinessSatisfied = dnsReadinessCurrent(evaluation, request)
		}
		changePermitted := requestValid && environmentConsistent && readinessSatisfied
		valuesConfirmed := responseValid && stringValue(response, "request_outcome") == "confirmed" &&
			stringValue(response, "observation_state") == "present" &&
			sameStringSet(arrayValue(request, "values"), arrayValue(response, "values")) &&
			reflect.DeepEqual(request["ttl_seconds"], response["ttl_seconds"])
		evaluations = append(evaluations, map[string]any{
			"case_id":                stringValue(evaluation, "case_id"),
			"request_schema_valid":   requestValid,
			"response_schema_valid":  responseValid,
			"record_bound":           recordBound,
			"environment_consistent": environmentConsistent,
			"readiness_required":     readinessRequired,
			"readiness_satisfied":    readinessSatisfied,
			"change_permitted":       changePermitted,
			"values_confirmed":       valuesConfirmed,
			"record_usable":          changePermitted && recordBound && valuesConfirmed,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

// dnsReadinessCurrent checks that a direct-join assertion is bound to the
// request's server, environment, and instance and valid at evaluation time.
func dnsReadinessCurrent(evaluation, request map[string]any) bool {
	assertion, ok := request["readiness_assertion"].(map[string]any)
	if !ok {
		return false
	}
	if stringValue(assertion, "server_id") != stringValue(request, "server_id") ||
		stringValue(assertion, "environment_id") != stringValue(request, "environment_id") ||
		!reflect.DeepEqual(assertion["instance_ref"], request["instance_ref"]) {
		return false
	}
	now, nowErr := time.Parse(time.RFC3339Nano, stringValue(evaluation, "evaluation_time"))
	observed, observedErr := time.Parse(time.RFC3339Nano, stringValue(assertion, "observed_at"))
	expires, expiresErr := time.Parse(time.RFC3339Nano, stringValue(assertion, "expires_at"))
	if nowErr != nil || observedErr != nil || expiresErr != nil {
		return false
	}
	return !observed.After(now) && now.Before(expires) && expires.After(observed)
}

func init() {
	registerEvaluator(evaluateDNSRecordUpsert, "integration.dns.record.upsert.purpose-readiness-and-confirmation")
}
