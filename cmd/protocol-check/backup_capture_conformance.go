package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"
)

const backupCaptureFiveMinuteLifetime = 5 * time.Minute

func evaluateBackupCapture(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	baseRequest := asObject(input["base_request"])
	baseOutput := asObject(input["base_output"])
	basePreparation := asObject(input["base_preparation_result"])
	baseScope := asObject(input["base_transfer_scope"])
	baseRequestContext := asObject(input["request_context"])
	results := make(map[string]any)
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		caseID := stringValue(evaluation, "case_id")
		request := mergeJSONObjects(baseRequest, asObject(evaluation["request_overrides"]))
		requestContext := mergeJSONObjects(baseRequestContext, asObject(evaluation["request_context_overrides"]))
		for _, key := range arrayValue(evaluation, "request_context_remove") {
			if stringKey, ok := key.(string); ok {
				delete(requestContext, stringKey)
			}
		}
		output := mergeJSONObjects(baseOutput, asObject(evaluation["output_overrides"]))
		preparation := mergeJSONObjects(basePreparation, asObject(evaluation["preparation_result_overrides"]))
		for _, key := range arrayValue(evaluation, "output_remove") {
			if stringKey, ok := key.(string); ok {
				delete(output, stringKey)
			}
		}
		scope := mergeJSONObjects(baseScope, asObject(evaluation["transfer_scope_overrides"]))

		inputSchemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:backup-capture-input", request) == nil
		outputSchemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:backup-capture-output", output) == nil
		preparationSchemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:backup-prepare-output", preparation) == nil
		requestContextSchemaValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:common:request-context", requestContext) == nil
		streamTransfers := arrayValue(requestContext, "stream_transfers")
		transferSchemaValid := len(streamTransfers) == 1 && pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:storage:storage-transfer-reference", asObject(streamTransfers[0])) == nil
		schemasValid := inputSchemaValid && outputSchemaValid && requestContextSchemaValid && transferSchemaValid
		target := asObject(request["target"])
		requestTargetBound := stringValue(target, "server_id") == stringValue(requestContext, "server_id") &&
			stringValue(target, "environment_id") == stringValue(requestContext, "environment_id")
		preparationBound := preparationSchemaValid &&
			stringValue(preparation, "outcome") == "prepared" &&
			reflect.DeepEqual(request["target"], preparation["target"]) &&
			stringValue(request, "release_id") == stringValue(preparation, "release_id") &&
			stringValue(request, "backup_id") == stringValue(preparation, "backup_id") &&
			stringValue(request, "data_generation_id") == stringValue(preparation, "data_generation_id") &&
			stringValue(request, "preparation_id") == stringValue(preparation, "preparation_id") &&
			stringValue(request, "consistency_mode") == stringValue(preparation, "consistency_mode") &&
			containsString(arrayValue(preparation, "data_set_ids"), stringValue(request, "data_set_id"))
		transfer := map[string]any{}
		if len(streamTransfers) == 1 {
			transfer = asObject(streamTransfers[0])
		}
		transferAuthorized := transferSchemaValid && backupCaptureTransferAuthorized(fixture, input, requestContext, request, scope, transfer)
		bindingExact := backupCaptureBindingExact(request, output)
		transferID := stringValue(transfer, "transfer_id")
		transferHandleInDurableInput := transferID != "" && backupCaptureContainsValue(request, transferID)
		transferHandleInInvocationContext := transferID != "" && backupCaptureContainsValue(requestContext, transferID)
		transferHandleInResult := transferID != "" && backupCaptureContainsValue(output, transferID)

		streamBytes := []byte(stringValue(evaluation, "stream_bytes"))
		maximumBytes, maximumErr := integerValue(transfer["maximum_bytes"])
		reportedBytes, sizeErr := integerValue(output["size_bytes"])
		streamWithinBounds := maximumErr == nil && sizeErr == nil && maximumBytes >= 0 &&
			reportedBytes == int64(len(streamBytes)) && int64(len(streamBytes)) <= maximumBytes
		streamCompleted, hasStreamCompleted := output["stream_completed"].(bool)
		transportCompleted := true
		if configured, ok := evaluation["stream_transport_complete"].(bool); ok {
			transportCompleted = configured
		}
		streamCompleted = hasStreamCompleted && streamCompleted && transportCompleted
		checksum := asObject(output["checksum"])
		streamDigest := sha256.Sum256(streamBytes)
		checksumValid := stringValue(checksum, "algorithm") == "sha256" &&
			stringValue(checksum, "digest") == hex.EncodeToString(streamDigest[:])
		outcome := stringValue(output, "outcome")
		dispatchPermitted := inputSchemaValid && requestContextSchemaValid && transferSchemaValid &&
			requestTargetBound && preparationBound && transferAuthorized
		captureAccepted := schemasValid && requestTargetBound && preparationBound && transferAuthorized && bindingExact &&
			outcome == "captured" && streamCompleted && streamWithinBounds && checksumValid
		requiresReconciliation := !outputSchemaValid || outcome == "outcome_unknown" ||
			outcome == "captured" && !captureAccepted ||
			outcome == "rejected" && (!bindingExact || !dispatchPermitted)

		watermark := asObject(output["write_watermark"])
		watermarkBound := len(watermark) > 0 && stringValue(watermark, "data_generation_id") == stringValue(request, "data_generation_id")
		runtimeResumed, _ := evaluation["runtime_resumed"].(bool)
		currentGeneration := stringValue(evaluation, "current_generation_id")
		if currentGeneration == "" {
			currentGeneration = stringValue(request, "data_generation_id")
		}
		destructionCurrentGenerationProven := false
		if captureAccepted && !runtimeResumed && currentGeneration == stringValue(request, "data_generation_id") {
			switch stringValue(request, "consistency_mode") {
			case "offline":
				destructionCurrentGenerationProven = true
			case "game_native":
				// This draft has no interoperable watermark-verifier contract.
				destructionCurrentGenerationProven = false
			}
		}

		if caseID == "" {
			return nil, fmt.Errorf("backup capture evaluation has no case_id")
		}
		results[caseID] = map[string]any{
			"schemas_valid":                         schemasValid,
			"preparation_result_schema_valid":       preparationSchemaValid,
			"preparation_bound":                     preparationBound,
			"dispatch_permitted":                    dispatchPermitted,
			"request_target_bound":                  requestTargetBound,
			"binding_exact":                         bindingExact,
			"transfer_authorized":                   transferAuthorized,
			"transfer_handle_in_durable_input":      transferHandleInDurableInput,
			"transfer_handle_in_invocation_context": transferHandleInInvocationContext,
			"transfer_handle_in_result":             transferHandleInResult,
			"stream_completed":                      streamCompleted,
			"stream_within_bounds":                  streamWithinBounds,
			"checksum_valid":                        checksumValid,
			"write_watermark_bound":                 watermarkBound,
			"capture_accepted":                      captureAccepted,
			"requires_reconciliation":               requiresReconciliation,
			"retry_permitted":                       false,
			"destruction_current_generation_proven": destructionCurrentGenerationProven,
		}
	}
	return map[string]any{"cases": results}, nil
}

func backupCaptureTransferAuthorized(fixture, fixtureInput, requestContext, request, scope, transfer map[string]any) bool {
	expiry, err := time.Parse(time.RFC3339, stringValue(transfer, "expires_at"))
	if err != nil {
		return false
	}
	now, err := time.Parse(time.RFC3339, stringValue(fixtureInput, "now"))
	if err != nil || !expiry.After(now) || expiry.Sub(now) > backupCaptureFiveMinuteLifetime {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, stringValue(requestContext, "deadline"))
	if err != nil || expiry.After(deadline) {
		return false
	}
	maximumBytes, maxErr := integerValue(transfer["maximum_bytes"])
	scopeMaximumBytes, scopeMaxErr := integerValue(scope["maximum_bytes"])
	return maxErr == nil && scopeMaxErr == nil && maximumBytes == scopeMaximumBytes &&
		stringValue(transfer, "direction") == "adapter_to_controller" &&
		stringValue(scope, "direction") == stringValue(transfer, "direction") &&
		stringValue(scope, "transfer_id") == stringValue(transfer, "transfer_id") &&
		stringValue(scope, "expires_at") == stringValue(transfer, "expires_at") &&
		stringValue(scope, "operation_id") == stringValue(requestContext, "operation_id") &&
		stringValue(scope, "step_id") == stringValue(requestContext, "step_id") &&
		stringValue(scope, "capability") == stringValue(fixture, "capability") &&
		stringValue(scope, "adapter_id") == stringValue(fixtureInput, "adapter_id") &&
		reflect.DeepEqual(scope["target"], request["target"]) &&
		stringValue(scope, "data_generation_id") == stringValue(request, "data_generation_id") &&
		stringValue(scope, "data_set_id") == stringValue(request, "data_set_id") &&
		stringValue(scope, "capture_id") == stringValue(request, "capture_id") &&
		stringValue(requestContext, "server_id") == stringValue(asObject(request["target"]), "server_id") &&
		stringValue(requestContext, "environment_id") == stringValue(asObject(request["target"]), "environment_id") &&
		!backupCaptureBoolValue(scope["consumed"])
}

func backupCaptureBindingExact(request, output map[string]any) bool {
	if !reflect.DeepEqual(request["target"], output["target"]) {
		return false
	}
	for _, field := range []string{"release_id", "backup_id", "data_generation_id", "preparation_id", "data_set_id", "capture_id", "consistency_mode"} {
		if stringValue(request, field) != stringValue(output, field) {
			return false
		}
	}
	return true
}

func containsString(values []any, target string) bool {
	for _, value := range values {
		if item, ok := value.(string); ok && item == target {
			return true
		}
	}
	return false
}

func mergeJSONObjects(base, override map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range override {
		if baseObject, ok := result[key].(map[string]any); ok {
			if overrideObject, nested := value.(map[string]any); nested {
				result[key] = mergeJSONObjects(baseObject, overrideObject)
				continue
			}
		}
		result[key] = value
	}
	return result
}

func backupCaptureContainsValue(value any, target string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			if backupCaptureContainsValue(child, target) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if backupCaptureContainsValue(child, target) {
				return true
			}
		}
	case string:
		return typed == target
	}
	return false
}

func backupCaptureBoolValue(value any) bool {
	result, _ := value.(bool)
	return result
}
