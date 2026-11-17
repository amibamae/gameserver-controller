package main

import "reflect"

const (
	proxyBackendInspectInputSchemaID  = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-inspect-input"
	proxyBackendInspectOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:proxy-backend-inspect-output"
)

func evaluateProxyBackendInspect(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	evaluations := make([]any, 0, len(arrayValue(input, "evaluations")))
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		requestValid := pkg.validateSchema(proxyBackendInspectInputSchemaID, request) == nil
		responseValid := pkg.validateSchema(proxyBackendInspectOutputSchemaID, response) == nil
		requestTarget := asObject(request["target"])
		responseTarget := asObject(response["target"])
		targetBound := reflect.DeepEqual(request["target"], response["target"])
		environmentConsistent := proxyBackendTargetsMatchInstanceEnvironments(requestTarget, responseTarget)
		schemasValid := requestValid && responseValid
		bindingValid := targetBound && environmentConsistent
		routingPermitted := schemasValid && bindingValid &&
			stringValue(response, "registration_state") == "registered" &&
			stringValue(response, "routability") == "routable"
		evaluations = append(evaluations, map[string]any{
			"case_id":                       stringValue(evaluation, "case_id"),
			"request_schema_valid":          requestValid,
			"response_schema_valid":         responseValid,
			"target_bound":                  targetBound,
			"target_environment_consistent": environmentConsistent,
			"routing_permitted":             routingPermitted,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func proxyBackendTargetsMatchInstanceEnvironments(requestTarget, responseTarget map[string]any) bool {
	requestEnvironment := stringValue(requestTarget, "environment_id")
	requestInstance := asObject(requestTarget["instance_ref"])
	responseInstance := asObject(responseTarget["instance_ref"])
	return requestEnvironment != "" &&
		requestEnvironment == stringValue(requestInstance, "environment_id") &&
		stringValue(responseTarget, "environment_id") == stringValue(responseInstance, "environment_id")
}

func init() {
	registerEvaluator(evaluateProxyBackendInspect, "integration.proxy.inspect.target-state-and-routability")
}
