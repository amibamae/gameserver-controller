package main

import (
	"fmt"
	"reflect"
	"time"
)

// Register the hosting destroy evaluators defined in this file.
func init() {
	registerEvaluator(evaluateHostingInstanceDestroyApproval,
		"hosting.instance.destroy.approval-exact-scope",
		"hosting.instance.destroy.approval-wrong-action",
		"hosting.instance.destroy.approval-target-mismatch",
		"hosting.instance.destroy.approval-environment-mismatch",
		"hosting.instance.destroy.approval-plan-mismatch",
		"hosting.instance.destroy.approval-expired",
		"hosting.instance.destroy.approval-missing-approver")
	registerEvaluator(evaluateHostingInstanceDestroy,
		"hosting.instance.destroy.authorization-required",
		"hosting.instance.destroy.verified-backup-required",
		"hosting.instance.destroy.current-generation-backup-required",
		"hosting.instance.destroy.stopped-required",
		"hosting.instance.destroy.completed-destroyed",
		"hosting.instance.destroy.accepted-not-destroyed",
		"hosting.instance.destroy.unknown-requires-inspection",
		"hosting.instance.destroy.target-mismatch")
}

// evaluateHostingInstanceDestroyApproval evaluates the conformance fixtures
// hosting.instance.destroy.approval-exact-scope, hosting.instance.destroy.approval-wrong-action,
// hosting.instance.destroy.approval-target-mismatch,
// hosting.instance.destroy.approval-environment-mismatch,
// hosting.instance.destroy.approval-plan-mismatch, hosting.instance.destroy.approval-expired,
// hosting.instance.destroy.approval-missing-approver.
func evaluateHostingInstanceDestroyApproval(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	destroyInput := asObject(input["destroy_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-input", destroyInput); err != nil {
		return nil, fmt.Errorf("hosting destroy input does not match schema: %w", err)
	}
	preconditions := asObject(fixture["core_preconditions"])
	request := asObject(fixture["request_binding"])
	approval := asObject(preconditions["approval"])
	target := asObject(destroyInput["instance_ref"])
	requestBindingMatches := destroyRequestBindingMatches(request, target)
	approvalScopeMatches := destroyApprovalScopeMatches(approval, request, target)
	approvalCurrent := approvalCurrentAt(approval, stringValue(fixture, "evaluation_time"))
	approvalApproverPresent := stringValue(approval, "approval_id") != "" && stringValue(approval, "approver_id") != ""
	approvalBound := requestBindingMatches && approvalScopeMatches && approvalCurrent && approvalApproverPresent
	allowed := boolValue(preconditions, "authorization_valid") && approvalBound &&
		boolValue(preconditions, "backup_verified") && boolValue(preconditions, "backup_generation_matches") &&
		boolValue(preconditions, "instance_stopped")
	output := asObject(input["destroy_output"])
	outputPresent := len(output) > 0
	targetBound, confirmed := false, false
	if outputPresent {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-output", output); err != nil {
			return nil, fmt.Errorf("hosting destroy output does not match schema: %w", err)
		}
		targetBound = reflect.DeepEqual(target, asObject(output["instance_ref"]))
		confirmed = allowed && targetBound && stringValue(output, "outcome") == "completed" && stringValue(output, "state") == "destroyed"
	}
	providerCalls := 0
	if allowed {
		providerCalls = 1
	}
	return map[string]any{
		"schemas_valid":             true,
		"request_binding_matches":   requestBindingMatches,
		"approval_scope_matches":    approvalScopeMatches,
		"approval_current":          approvalCurrent,
		"approval_approver_present": approvalApproverPresent,
		"approval_bound":            approvalBound,
		"destroy_dispatch_allowed":  allowed,
		"provider_destroy_calls":    providerCalls,
		"target_bound":              targetBound,
		"destruction_confirmed":     confirmed,
	}, nil
}

// destroyRequestBindingMatches: the request is a hosting.instance.destroy
// of this instance by its adapter in its environment, with a logical
// server and a plan fingerprint.
func destroyRequestBindingMatches(request, target map[string]any) bool {
	return stringValue(request, "action") == "hosting.instance.destroy" &&
		stringValue(request, "logical_server_id") != "" &&
		stringValue(request, "environment_id") == stringValue(target, "environment_id") &&
		stringValue(request, "adapter_id") == stringValue(target, "adapter_id") &&
		stringValue(request, "plan_fingerprint") != "" &&
		reflect.DeepEqual(asObject(request["instance_ref"]), target)
}

// destroyApprovalScopeMatches: the approval covers exactly the request's
// action, server, environment, adapter, instance and plan.
func destroyApprovalScopeMatches(approval, request, target map[string]any) bool {
	return stringValue(approval, "action") == stringValue(request, "action") &&
		stringValue(approval, "logical_server_id") == stringValue(request, "logical_server_id") &&
		stringValue(approval, "environment_id") == stringValue(request, "environment_id") &&
		stringValue(approval, "adapter_id") == stringValue(request, "adapter_id") &&
		reflect.DeepEqual(asObject(approval["instance_ref"]), target) &&
		reflect.DeepEqual(asObject(approval["instance_ref"]), asObject(request["instance_ref"])) &&
		stringValue(approval, "plan_fingerprint") != "" &&
		stringValue(approval, "plan_fingerprint") == stringValue(request, "plan_fingerprint")
}

// approvalCurrentAt: the approval is marked valid and expires after the
// evaluation time; both times must parse.
func approvalCurrentAt(approval map[string]any, evaluationTime string) bool {
	evaluatedAt, evaluationErr := time.Parse(time.RFC3339, evaluationTime)
	expiresAt, expiryErr := time.Parse(time.RFC3339, stringValue(approval, "expires_at"))
	return boolValue(approval, "valid") && evaluationErr == nil && expiryErr == nil && expiresAt.After(evaluatedAt)
}

// evaluateHostingInstanceDestroy evaluates the conformance fixtures
// hosting.instance.destroy.authorization-required,
// hosting.instance.destroy.verified-backup-required,
// hosting.instance.destroy.current-generation-backup-required,
// hosting.instance.destroy.stopped-required, hosting.instance.destroy.completed-destroyed,
// hosting.instance.destroy.accepted-not-destroyed,
// hosting.instance.destroy.unknown-requires-inspection, hosting.instance.destroy.target-mismatch.
func evaluateHostingInstanceDestroy(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	destroyInput := asObject(input["destroy_input"])
	if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-input", destroyInput); err != nil {
		return nil, fmt.Errorf("hosting destroy input does not match schema: %w", err)
	}
	preconditions := asObject(fixture["core_preconditions"])
	allowed := boolValue(preconditions, "authorization_valid") && boolValue(preconditions, "backup_verified") &&
		boolValue(preconditions, "backup_generation_matches") && boolValue(preconditions, "instance_stopped")
	output := asObject(input["destroy_output"])
	outputPresent := len(output) > 0
	targetBound, confirmed, unknownRequiresInspection, resultRequiresInspection := false, false, false, false
	outcome, state := "", ""
	if outputPresent {
		if err := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:hosting:instance-destroy-output", output); err != nil {
			return nil, fmt.Errorf("hosting destroy output does not match schema: %w", err)
		}
		targetBound = reflect.DeepEqual(asObject(destroyInput["instance_ref"]), asObject(output["instance_ref"]))
		outcome, state = stringValue(output, "outcome"), stringValue(output, "state")
		confirmed = allowed && targetBound && outcome == "completed" && state == "destroyed"
		unknownRequiresInspection = outcome == "outcome_unknown"
		resultRequiresInspection = !targetBound || unknownRequiresInspection
	}
	destroyCalls := 0
	if allowed {
		destroyCalls = 1
	}
	return map[string]any{
		"schemas_valid":               true,
		"destroy_dispatch_allowed":    allowed,
		"provider_destroy_calls":      destroyCalls,
		"target_bound":                targetBound,
		"destruction_confirmed":       confirmed,
		"unknown_requires_inspection": unknownRequiresInspection,
		"result_requires_inspection":  resultRequiresInspection,
	}, nil
}
