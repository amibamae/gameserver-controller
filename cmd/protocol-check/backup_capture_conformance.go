package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"time"
)

const backupCaptureFiveMinuteLifetime = 5 * time.Minute

// backupCaptureCase is one evaluation after its overrides and removals are
// applied to the fixture's base objects.
type backupCaptureCase struct {
	request, requestContext, output, preparation, scope map[string]any
	streamTransfers                                     []any
	transfer                                            map[string]any
}

func evaluateBackupCapture(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	results := make(map[string]any)
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		result := evaluateBackupCaptureCase(pkg, fixture, input, evaluation)
		caseID := stringValue(evaluation, "case_id")
		if caseID == "" {
			return nil, fmt.Errorf("backup capture evaluation has no case_id")
		}
		results[caseID] = result
	}
	return map[string]any{"cases": results}, nil
}

func evaluateBackupCaptureCase(pkg *protocolPackage, fixture, input, evaluation map[string]any) map[string]any {
	bc := newBackupCaptureCase(input, evaluation)
	request := bc.request
	schemas := bc.schemaChecks(pkg)
	target := asObject(request["target"])
	requestTargetBound := stringValue(target, "server_id") == stringValue(bc.requestContext, "server_id") &&
		stringValue(target, "environment_id") == stringValue(bc.requestContext, "environment_id")
	preparationBound := schemas.preparation && backupCapturePreparationBound(request, bc.preparation)
	transferAuthorized := schemas.transfer && backupCaptureTransferAuthorized(fixture, input, bc.requestContext, request, bc.scope, bc.transfer)
	bindingExact := backupCaptureBindingExact(request, bc.output)
	transferID := stringValue(bc.transfer, "transfer_id")
	streamBytes := []byte(stringValue(evaluation, "stream_bytes"))
	stream := checkBackupCaptureStream(bc, evaluation, streamBytes)
	outcome := stringValue(bc.output, "outcome")
	dispatchPermitted := schemas.input && schemas.requestContext && schemas.transfer &&
		requestTargetBound && preparationBound && transferAuthorized
	captureAccepted := schemas.all() && requestTargetBound && preparationBound && transferAuthorized && bindingExact &&
		outcome == "captured" && stream.accepted()
	requiresReconciliation := backupCaptureNeedsReconciliation(schemas.output, outcome, captureAccepted, bindingExact, dispatchPermitted, len(streamBytes))
	watermark := asObject(bc.output["write_watermark"])
	watermarkBound := len(watermark) > 0 && stringValue(watermark, "data_generation_id") == stringValue(request, "data_generation_id")

	return map[string]any{
		"schemas_valid":                         schemas.all(),
		"preparation_result_schema_valid":       schemas.preparation,
		"preparation_bound":                     preparationBound,
		"dispatch_permitted":                    dispatchPermitted,
		"request_target_bound":                  requestTargetBound,
		"binding_exact":                         bindingExact,
		"transfer_authorized":                   transferAuthorized,
		"transfer_handle_in_durable_input":      transferID != "" && backupCaptureContainsValue(request, transferID),
		"transfer_handle_in_invocation_context": transferID != "" && backupCaptureContainsValue(bc.requestContext, transferID),
		"transfer_handle_in_result":             transferID != "" && backupCaptureContainsValue(bc.output, transferID),
		"stream_completed":                      stream.completed,
		"stream_within_bounds":                  stream.withinBounds,
		"checksum_valid":                        stream.checksumValid,
		"write_watermark_bound":                 watermarkBound,
		"capture_accepted":                      captureAccepted,
		"requires_reconciliation":               requiresReconciliation,
		"retry_permitted":                       false,
		"destruction_current_generation_proven": backupCaptureCurrentGenerationProven(captureAccepted, evaluation, request),
	}
}

// backupCaptureSchemas records which capture messages match their schemas.
// The transfer is valid only when there is exactly one.
type backupCaptureSchemas struct {
	input, output, preparation, requestContext, transfer bool
}

func (bc backupCaptureCase) schemaChecks(pkg *protocolPackage) backupCaptureSchemas {
	return backupCaptureSchemas{
		input:          pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:backup-capture-input", bc.request) == nil,
		output:         pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:backup-capture-output", bc.output) == nil,
		preparation:    pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:game:backup-prepare-output", bc.preparation) == nil,
		requestContext: pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:common:request-context", bc.requestContext) == nil,
		transfer: len(bc.streamTransfers) == 1 &&
			pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:storage:storage-transfer-reference", asObject(bc.streamTransfers[0])) == nil,
	}
}

// all excludes the preparation result, which is reported separately.
func (s backupCaptureSchemas) all() bool {
	return s.input && s.output && s.requestContext && s.transfer
}

// backupCaptureNeedsReconciliation: an invalid or unknown result, a
// "captured" result that was not accepted, or a "rejected" result that is
// unbound, was never permitted, or still streamed bytes, must be reconciled
// before anything else happens.
func backupCaptureNeedsReconciliation(outputValid bool, outcome string, captureAccepted, bindingExact, dispatchPermitted bool, streamedBytes int) bool {
	switch {
	case !outputValid || outcome == "outcome_unknown":
		return true
	case outcome == "captured":
		return !captureAccepted
	case outcome == "rejected":
		return !bindingExact || !dispatchPermitted || streamedBytes != 0
	default:
		return false
	}
}

// newBackupCaptureCase merges an evaluation's overrides into the fixture's base
// objects and applies its key removals.
func newBackupCaptureCase(input, evaluation map[string]any) backupCaptureCase {
	bc := backupCaptureCase{
		request:        mergeJSONObjects(asObject(input["base_request"]), asObject(evaluation["request_overrides"])),
		requestContext: mergeJSONObjects(asObject(input["request_context"]), asObject(evaluation["request_context_overrides"])),
		output:         mergeJSONObjects(asObject(input["base_output"]), asObject(evaluation["output_overrides"])),
		preparation:    mergeJSONObjects(asObject(input["base_preparation_result"]), asObject(evaluation["preparation_result_overrides"])),
		scope:          mergeJSONObjects(asObject(input["base_transfer_scope"]), asObject(evaluation["transfer_scope_overrides"])),
	}
	deleteStringKeys(bc.requestContext, arrayValue(evaluation, "request_context_remove"))
	deleteStringKeys(bc.output, arrayValue(evaluation, "output_remove"))
	bc.streamTransfers = arrayValue(bc.requestContext, "stream_transfers")
	bc.transfer = map[string]any{}
	if len(bc.streamTransfers) == 1 {
		bc.transfer = asObject(bc.streamTransfers[0])
	}
	return bc
}

func deleteStringKeys(object map[string]any, keys []any) {
	for _, key := range keys {
		if stringKey, ok := key.(string); ok {
			delete(object, stringKey)
		}
	}
}

// backupCapturePreparationBound reports whether a prepared backup matches the
// capture request's target, release, backup, generation, preparation, mode,
// and data set.
func backupCapturePreparationBound(request, preparation map[string]any) bool {
	if stringValue(preparation, "outcome") != "prepared" || !reflect.DeepEqual(request["target"], preparation["target"]) {
		return false
	}
	for _, field := range []string{"release_id", "backup_id", "data_generation_id", "preparation_id", "consistency_mode"} {
		if stringValue(request, field) != stringValue(preparation, field) {
			return false
		}
	}
	return containsString(arrayValue(preparation, "data_set_ids"), stringValue(request, "data_set_id"))
}

// backupCaptureStream reports how the captured stream compares with what the
// adapter reported and what the transfer allowed.
type backupCaptureStream struct {
	completed, withinBounds, checksumValid bool
}

// accepted: the stream finished, stayed within its bounds, and matches the
// reported checksum.
func (s backupCaptureStream) accepted() bool {
	return s.completed && s.withinBounds && s.checksumValid
}

func checkBackupCaptureStream(bc backupCaptureCase, evaluation map[string]any, streamBytes []byte) backupCaptureStream {
	maximumBytes, maximumErr := integerValue(bc.transfer["maximum_bytes"])
	reportedBytes, sizeErr := integerValue(bc.output["size_bytes"])
	streamCompleted, hasStreamCompleted := bc.output["stream_completed"].(bool)
	transportCompleted := true
	if configured, ok := evaluation["stream_transport_complete"].(bool); ok {
		transportCompleted = configured
	}
	checksum := asObject(bc.output["checksum"])
	streamDigest := sha256.Sum256(streamBytes)
	return backupCaptureStream{
		completed: hasStreamCompleted && streamCompleted && transportCompleted,
		withinBounds: maximumErr == nil && sizeErr == nil && maximumBytes >= 0 &&
			reportedBytes == int64(len(streamBytes)) && int64(len(streamBytes)) <= maximumBytes,
		checksumValid: stringValue(checksum, "algorithm") == "sha256" &&
			stringValue(checksum, "digest") == hex.EncodeToString(streamDigest[:]),
	}
}

// backupCaptureCurrentGenerationProven reports whether an accepted capture
// proves the current data generation for destruction. Only an offline capture
// with no runtime resume can; this draft has no interoperable verifier for
// game-native write watermarks.
func backupCaptureCurrentGenerationProven(captureAccepted bool, evaluation, request map[string]any) bool {
	runtimeResumed, _ := evaluation["runtime_resumed"].(bool)
	currentGeneration := stringValue(evaluation, "current_generation_id")
	if currentGeneration == "" {
		currentGeneration = stringValue(request, "data_generation_id")
	}
	return captureAccepted && !runtimeResumed && currentGeneration == stringValue(request, "data_generation_id") &&
		stringValue(request, "consistency_mode") == "offline"
}

func backupCaptureTransferAuthorized(fixture, fixtureInput, requestContext, request, scope, transfer map[string]any) bool {
	return backupCaptureTransferTimely(fixtureInput, requestContext, transfer) &&
		backupCaptureScopeMatches(fixture, fixtureInput, requestContext, request, scope, transfer)
}

// backupCaptureTransferTimely: the transfer is unexpired, lives at most
// five minutes, and expires no later than the request deadline.
func backupCaptureTransferTimely(fixtureInput, requestContext, transfer map[string]any) bool {
	expiry, err := time.Parse(time.RFC3339, stringValue(transfer, "expires_at"))
	if err != nil {
		return false
	}
	now, err := time.Parse(time.RFC3339, stringValue(fixtureInput, "now"))
	if err != nil || !expiry.After(now) || expiry.Sub(now) > backupCaptureFiveMinuteLifetime {
		return false
	}
	deadline, err := time.Parse(time.RFC3339, stringValue(requestContext, "deadline"))
	return err == nil && !expiry.After(deadline)
}

// backupCaptureScopeMatches: the unconsumed transfer scope grants exactly
// this adapter-to-controller transfer, size and expiry, for this operation
// step, capability, adapter, target, data generation, data set and
// capture.
func backupCaptureScopeMatches(fixture, fixtureInput, requestContext, request, scope, transfer map[string]any) bool {
	maximumBytes, maxErr := integerValue(transfer["maximum_bytes"])
	scopeMaximumBytes, scopeMaxErr := integerValue(scope["maximum_bytes"])
	target := asObject(request["target"])
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
		stringValue(requestContext, "server_id") == stringValue(target, "server_id") &&
		stringValue(requestContext, "environment_id") == stringValue(target, "environment_id") &&
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

func init() {
	registerEvaluator(evaluateBackupCapture, "game.backup.capture.binding-and-stream-integrity")
}
