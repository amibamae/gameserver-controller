package main

import (
	"encoding/json"
	"strconv"
)

// evaluateManagementSubmitDecision is the reference evaluator for the define
// and submit decision order in management-api/authorization.md section 2.1.
func evaluateManagementSubmitDecision(_ *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	serverEnvironment := map[string]string{}
	for _, raw := range arrayValue(input, "servers") {
		server := asObject(raw)
		serverEnvironment[stringValue(server, "server_id")] = stringValue(server, "environment_id")
	}
	registered := map[string]bool{}
	for _, raw := range arrayValue(input, "registered_operation_types") {
		if name, ok := raw.(string); ok {
			registered[name] = true
		}
	}
	network := asObject(input["network_profile_satisfied"])
	evaluations := []any{}
	for _, raw := range arrayValue(input, "evaluations") {
		c := asObject(raw)
		scopes := map[[3]string]bool{}
		for _, rawScope := range arrayValue(c, "actor_scopes") {
			scope := asObject(rawScope)
			scopes[[3]string{stringValue(scope, "permission"), stringValue(scope, "environment_id"), stringValue(scope, "resource_id")}] = true
		}
		has := func(permission, environment, resource string) bool {
			return scopes[[3]string{permission, environment, resource}]
		}
		key, operationType, serverID, prior := stringValue(c, "idempotency_key"), stringValue(c, "operation_type"), stringValue(c, "server_id"), stringValue(c, "prior_use")
		status, code := 202, ""
		idempotency := func() {
			switch prior {
			case "different_request":
				status, code = 409, "core.operation.idempotency_conflict"
			}
		}
		if stringValue(c, "route") == "define" {
			environment := stringValue(c, "environment_id")
			_, exists := serverEnvironment[serverID]
			switch {
			case key == "" || operationType != "server.define":
				status, code = 400, "core.validation.invalid_request"
			case !has("server.define", environment, serverID):
				status, code = 403, "core.authorization.denied"
			case prior != "":
				idempotency()
			case exists:
				status, code = 409, "core.operation.conflict"
			}
		} else {
			environment, exists := serverEnvironment[serverID]
			switch {
			case key == "":
				status, code = 400, "core.validation.invalid_request"
			case !exists || !has("server.inspect", environment, serverID):
				status = 404
			case !has(operationType, environment, serverID):
				status, code = 403, "core.authorization.denied"
			case !registered[operationType]:
				status, code = 422, "core.operation.unsupported_type"
			case !boolValue(network, serverID):
				status, code = 422, "core.network.capability_missing"
			default:
				idempotency()
			}
		}
		var errorCode any
		if code != "" {
			errorCode = code
		}
		evaluations = append(evaluations, map[string]any{
			"case_id": stringValue(c, "case_id"), "status": jsonInt(status), "error_code": errorCode,
			"accepted": status == 202, "replayed": status == 202 && prior == "same_request",
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func jsonInt(value int) json.Number { return json.Number(strconv.Itoa(value)) }

func init() {
	registerEvaluator(evaluateManagementSubmitDecision, "management.operations.submit-decision")
}
