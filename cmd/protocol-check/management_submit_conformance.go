package main

import (
	"encoding/json"
	"strconv"
)

// evaluateManagementSubmitDecision is the reference evaluator for the define
// and submit decision order in management-api/authorization.md section 2.1.
func evaluateManagementSubmitDecision(_ *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	context := newManagementDecisionContext(input)
	evaluations := []any{}
	for _, raw := range arrayValue(input, "evaluations") {
		evaluations = append(evaluations, context.evaluate(asObject(raw)))
	}
	return map[string]any{"evaluations": evaluations}, nil
}

type managementDecisionContext struct {
	serverEnvironment map[string]string
	registered        map[string]bool
	network           map[string]any
}

func newManagementDecisionContext(input map[string]any) managementDecisionContext {
	context := managementDecisionContext{
		serverEnvironment: map[string]string{},
		registered:        map[string]bool{},
		network:           asObject(input["network_profile_satisfied"]),
	}
	for _, raw := range arrayValue(input, "servers") {
		server := asObject(raw)
		context.serverEnvironment[stringValue(server, "server_id")] = stringValue(server, "environment_id")
	}
	for _, raw := range arrayValue(input, "registered_operation_types") {
		if name, ok := raw.(string); ok {
			context.registered[name] = true
		}
	}
	return context
}

type managementDecisionRequest struct {
	key, operationType, serverID, prior string
	scopes                              map[[3]string]bool
}

func newManagementDecisionRequest(c map[string]any) managementDecisionRequest {
	request := managementDecisionRequest{
		key: stringValue(c, "idempotency_key"), operationType: stringValue(c, "operation_type"),
		serverID: stringValue(c, "server_id"), prior: stringValue(c, "prior_use"),
		scopes: map[[3]string]bool{},
	}
	for _, rawScope := range arrayValue(c, "actor_scopes") {
		scope := asObject(rawScope)
		request.scopes[[3]string{stringValue(scope, "permission"), stringValue(scope, "environment_id"), stringValue(scope, "resource_id")}] = true
	}
	return request
}

func (r managementDecisionRequest) has(permission, environment string) bool {
	return r.scopes[[3]string{permission, environment, r.serverID}]
}

func (c managementDecisionContext) evaluate(raw map[string]any) map[string]any {
	request := newManagementDecisionRequest(raw)
	var status int
	var code string
	if stringValue(raw, "route") == "define" {
		status, code = c.define(request, stringValue(raw, "environment_id"))
	} else {
		status, code = c.submit(request)
	}
	var errorCode any
	if code != "" {
		errorCode = code
	}
	return map[string]any{
		"case_id": stringValue(raw, "case_id"), "status": jsonInt(status), "error_code": errorCode,
		"accepted": status == 202, "replayed": status == 202 && request.prior == "same_request",
	}
}

func (c managementDecisionContext) define(r managementDecisionRequest, environment string) (int, string) {
	_, exists := c.serverEnvironment[r.serverID]
	switch {
	case r.key == "" || r.operationType != "server.define":
		return 400, "core.validation.invalid_request"
	case !r.has("server.define", environment):
		return 403, "core.authorization.denied"
	case r.prior != "":
		return managementKeyDecision(r.prior)
	case exists:
		return 409, "core.operation.conflict"
	default:
		return 202, ""
	}
}

func (c managementDecisionContext) submit(r managementDecisionRequest) (int, string) {
	environment, exists := c.serverEnvironment[r.serverID]
	switch {
	case r.key == "":
		return 400, "core.validation.invalid_request"
	case !exists || !r.has("server.inspect", environment):
		return 404, ""
	case !r.has(r.operationType, environment):
		return 403, "core.authorization.denied"
	case !c.registered[r.operationType]:
		return 422, "core.operation.unsupported_type"
	case !boolValue(c.network, r.serverID):
		return 422, "core.network.capability_missing"
	default:
		return managementKeyDecision(r.prior)
	}
}

func managementKeyDecision(prior string) (int, string) {
	switch prior {
	case "different_request":
		return 409, "core.operation.idempotency_conflict"
	case "expired_key":
		return 409, "core.operation.idempotency_key_expired"
	default:
		return 202, ""
	}
}

func jsonInt(value int) json.Number { return json.Number(strconv.Itoa(value)) }

func init() {
	registerEvaluator(evaluateManagementSubmitDecision, "management.operations.submit-decision")
}
