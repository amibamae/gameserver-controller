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
	requestTargetBound := backupCaptureRequestTargetBound(request, bc.requestContext)
	preparationBound := schemas.preparation && backupCapturePreparationBound(request, bc.preparation)
	transferAuthorized := schemas.transfer && backupCaptureTransferAuthorized(fixture, input, bc)
	bindingExact := backupCaptureBindingExact(request, bc.output)
	streamBytes := []byte(stringValue(evaluation, "stream_bytes"))
	stream := checkBackupCaptureStream(bc, evaluation, streamBytes)
	outcome := stringValue(bc.output, "outcome")
	dispatchPermitted := schemas.allowsDispatch(requestTargetBound, preparationBound, transferAuthorized)
	resultBound := schemas.output && bindingExact
	captureAccepted := dispatchPermitted && resultBound && outcome == "captured" && stream.accepted()
	requiresReconciliation := backupCaptureSettlement{
		outputValid: schemas.output, outcome: outcome, captureAccepted: captureAccepted,
		bindingExact: bindingExact, dispatchPermitted: dispatchPermitted, streamedBytes: len(streamBytes),
	}.needsReconciliation()
	watermarkBound := backupCaptureWatermarkBound(request, bc.output)

	return map[string]any{
		"schemas_valid":                         schemas.all(),
		"preparation_result_schema_valid":       schemas.preparation,
		"preparation_bound":                     preparationBound,
		"dispatch_permitted":                    dispatchPermitted,
		"request_target_bound":                  requestTargetBound,
		"binding_exact":                         bindingExact,
		"transfer_authorized":                   transferAuthorized,
		"transfer_handle_in_durable_input":      bc.transferHandleIn(request),
		"transfer_handle_in_invocation_context": bc.transferHandleIn(bc.requestContext),
		"transfer_handle_in_result":             bc.transferHandleIn(bc.output),
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

func (bc backupCaptureCase) transferHandleIn(value any) bool {
	transferID := stringValue(bc.transfer, "transfer_id")
	return transferID != "" && backupCaptureContainsValue(value, transferID)
}

func backupCaptureRequestTargetBound(request, requestContext map[string]any) bool {
	target := asObject(request["target"])
	return stringValue(target, "server_id") == stringValue(requestContext, "server_id") &&
		stringValue(target, "environment_id") == stringValue(requestContext, "environment_id")
}

func backupCaptureWatermarkBound(request, output map[string]any) bool {
	watermark := asObject(output["write_watermark"])
	return len(watermark) > 0 && stringValue(watermark, "data_generation_id") == stringValue(request, "data_generation_id")
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

func (s backupCaptureSchemas) allowsDispatch(targetBound, preparationBound, transferAuthorized bool) bool {
	messagesValid := s.input && s.requestContext && s.transfer
	return messagesValid && targetBound && preparationBound && transferAuthorized
}

// all excludes the preparation result, which is reported separately.
func (s backupCaptureSchemas) all() bool {
	return s.input && s.output && s.requestContext && s.transfer
}

// backupCaptureSettlement is what a capture result, and the checks around
// it, show about whether the capture settled.
type backupCaptureSettlement struct {
	outputValid                                      bool
	outcome                                          string
	captureAccepted, bindingExact, dispatchPermitted bool
	streamedBytes                                    int
}

// needsReconciliation: an invalid or unknown result, a "captured" result
// that was not accepted, or a "rejected" result that is unbound, was never
// permitted, or still streamed bytes, must be reconciled before anything
// else happens.
func (s backupCaptureSettlement) needsReconciliation() bool {
	switch {
	case !s.outputValid || s.outcome == "outcome_unknown":
		return true
	case s.outcome == "captured":
		return !s.captureAccepted
	case s.outcome == "rejected":
		return !s.bindingExact || !s.dispatchPermitted || s.streamedBytes != 0
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
	return backupCaptureStringFieldsMatch(request, preparation, "release_id", "backup_id", "data_generation_id", "preparation_id", "consistency_mode") &&
		containsString(arrayValue(preparation, "data_set_ids"), stringValue(request, "data_set_id"))
}

// backupCaptureStringFieldsMatch: left and right hold the same string, or
// both no string, under every field.
func backupCaptureStringFieldsMatch(left, right map[string]any, fields ...string) bool {
	for _, field := range fields {
		if stringValue(left, field) != stringValue(right, field) {
			return false
		}
	}
	return true
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
	return backupCaptureStream{
		completed:     backupCaptureStreamCompleted(bc.output, evaluation),
		withinBounds:  backupCaptureStreamWithinBounds(bc.transfer, bc.output, int64(len(streamBytes))),
		checksumValid: backupCaptureChecksumValid(bc.output, streamBytes),
	}
}

// backupCaptureStreamCompleted: the adapter reported a completed stream and
// the transport, unless the evaluation says otherwise, completed too.
func backupCaptureStreamCompleted(output, evaluation map[string]any) bool {
	streamCompleted, hasStreamCompleted := output["stream_completed"].(bool)
	transportCompleted := true
	if configured, ok := evaluation["stream_transport_complete"].(bool); ok {
		transportCompleted = configured
	}
	return hasStreamCompleted && streamCompleted && transportCompleted
}

// backupCaptureStreamWithinBounds: the transfer's maximum and the reported
// size are integers, the maximum is not negative, the reported size is the
// streamed size, and the streamed size fits the maximum.
func backupCaptureStreamWithinBounds(transfer, output map[string]any, streamedBytes int64) bool {
	maximumBytes, maximumErr := integerValue(transfer["maximum_bytes"])
	reportedBytes, sizeErr := integerValue(output["size_bytes"])
	if maximumErr != nil || sizeErr != nil {
		return false
	}
	return maximumBytes >= 0 && reportedBytes == streamedBytes && streamedBytes <= maximumBytes
}

// backupCaptureChecksumValid: the reported checksum is the SHA-256 digest of
// the streamed bytes.
func backupCaptureChecksumValid(output map[string]any, streamBytes []byte) bool {
	checksum := asObject(output["checksum"])
	streamDigest := sha256.Sum256(streamBytes)
	return stringValue(checksum, "algorithm") == "sha256" &&
		stringValue(checksum, "digest") == hex.EncodeToString(streamDigest[:])
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

func backupCaptureTransferAuthorized(fixture, fixtureInput map[string]any, bc backupCaptureCase) bool {
	return backupCaptureTransferTimely(fixtureInput, bc.requestContext, bc.transfer) &&
		backupCaptureScopeMatches(fixture, fixtureInput, bc)
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
func backupCaptureScopeMatches(fixture, fixtureInput map[string]any, bc backupCaptureCase) bool {
	return backupCaptureScopeGrantsTransfer(bc.scope, bc.transfer) &&
		backupCaptureScopeNamesStep(fixture, fixtureInput, bc.scope, bc.requestContext) &&
		backupCaptureScopeNamesCapture(bc.scope, bc.request, bc.requestContext) &&
		!backupCaptureBoolValue(bc.scope["consumed"])
}

// backupCaptureScopeGrantsTransfer: the scope grants exactly this
// adapter-to-controller transfer, with its size and expiry.
func backupCaptureScopeGrantsTransfer(scope, transfer map[string]any) bool {
	return backupCaptureScopeSizeMatches(scope, transfer) && stringValue(transfer, "direction") == "adapter_to_controller" &&
		backupCaptureStringFieldsMatch(scope, transfer, "direction", "transfer_id", "expires_at")
}

// backupCaptureScopeSizeMatches: the scope and the transfer allow the same
// integer maximum size.
func backupCaptureScopeSizeMatches(scope, transfer map[string]any) bool {
	maximumBytes, maxErr := integerValue(transfer["maximum_bytes"])
	scopeMaximumBytes, scopeMaxErr := integerValue(scope["maximum_bytes"])
	return maxErr == nil && scopeMaxErr == nil && maximumBytes == scopeMaximumBytes
}

// backupCaptureScopeNamesStep: the scope is for this operation step, the
// fixture's capability and the fixture's adapter.
func backupCaptureScopeNamesStep(fixture, fixtureInput, scope, requestContext map[string]any) bool {
	return backupCaptureStringFieldsMatch(scope, requestContext, "operation_id", "step_id") &&
		stringValue(scope, "capability") == stringValue(fixture, "capability") &&
		stringValue(scope, "adapter_id") == stringValue(fixtureInput, "adapter_id")
}

// backupCaptureScopeNamesCapture: the scope is for the request's target,
// data generation, data set and capture, and the invocation context names
// the same target.
func backupCaptureScopeNamesCapture(scope, request, requestContext map[string]any) bool {
	return reflect.DeepEqual(scope["target"], request["target"]) &&
		backupCaptureStringFieldsMatch(scope, request, "data_generation_id", "data_set_id", "capture_id") &&
		backupCaptureRequestTargetBound(request, requestContext)
}

func backupCaptureBindingExact(request, output map[string]any) bool {
	return reflect.DeepEqual(request["target"], output["target"]) &&
		backupCaptureStringFieldsMatch(request, output, "release_id", "backup_id", "data_generation_id", "preparation_id", "data_set_id", "capture_id", "consistency_mode")
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
