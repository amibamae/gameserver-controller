package captureinvoke

import (
	"context"
	"encoding/json"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

// checkCaptureResult accepts the adapter response only when it names exactly
// the requested capture and is either a reconcilable unknown outcome, a
// rejection that wrote no bytes, or a capture whose measured stream matches
// it and is sealed. Anything else is uncertain.
func (r *Runtime) checkCaptureResult(ctx context.Context, adapterID, environmentID string, dispatch captureDispatch, stream *captureStream, response adapter.InvokeResponse) (adapter.InvokeResponse, error) {
	var output result
	if r.schemas.ValidateJSON(r.captureSchema("output"), response.Result.Output) != nil || json.Unmarshal(response.Result.Output, &output) != nil {
		return adapter.InvokeResponse{}, uncertain()
	}
	if !sameCapture(dispatch.input, output.capture) {
		return adapter.InvokeResponse{}, uncertain()
	}
	if output.Outcome == "outcome_unknown" {
		return unknownCaptureOutcome(adapterID, environmentID, response, output)
	}
	if response.Result.Outcome != adapter.OutcomeCompleted {
		return adapter.InvokeResponse{}, uncertain()
	}
	if output.Outcome == "rejected" {
		return stream.rejected(response)
	}
	return stream.seal(ctx, dispatch.scope, output, response)
}

// unknownCaptureOutcome reports the response as an unknown outcome to
// reconcile through output's reconciliation reference, which must name this
// adapter and environment and match any external reference the adapter gave.
func unknownCaptureOutcome(adapterID, environmentID string, response adapter.InvokeResponse, output result) (adapter.InvokeResponse, error) {
	if !reconciliationRefNames(output.ReconciliationRef, adapterID, environmentID) {
		return adapter.InvokeResponse{}, uncertain()
	}
	if len(response.Result.ExternalOperationRef) > 0 && !sameJSON(response.Result.ExternalOperationRef, output.ReconciliationRef) {
		return adapter.InvokeResponse{}, uncertain()
	}
	response.Result.Outcome = adapter.OutcomeUnknown
	response.Result.ExternalOperationRef = append(json.RawMessage(nil), output.ReconciliationRef...)
	return response, nil
}

// reconciliationRefNames reports whether ref decodes and names adapterID in
// environmentID.
func reconciliationRefNames(ref json.RawMessage, adapterID, environmentID string) bool {
	var reference struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
	}
	return json.Unmarshal(ref, &reference) == nil && reference.AdapterID == adapterID && reference.EnvironmentID == environmentID
}

// rejected accepts a definitive rejection only when no byte reached the stage.
func (s *captureStream) rejected(response adapter.InvokeResponse) (adapter.InvokeResponse, error) {
	s.ticket.Abort()
	if s.counted.size.Load() != 0 {
		return adapter.InvokeResponse{}, uncertain()
	}
	return response, nil
}

// seal durably seals the stage with the validated response when the
// completed stream's measured evidence matches the reported capture.
func (s *captureStream) seal(ctx context.Context, scope capturestream.Scope, output result, response adapter.InvokeResponse) (adapter.InvokeResponse, error) {
	receipt, err := s.ticket.Evidence()
	if err != nil || !streamEvidenceMatches(receipt, scope, output) {
		return adapter.InvokeResponse{}, uncertain()
	}
	if ctx.Err() != nil || s.stage.Seal(ctx, receipt, response.Result.Output) != nil {
		return adapter.InvokeResponse{}, uncertain()
	}
	s.sealed = true
	return response, nil
}

// streamEvidenceMatches reports whether the measured stream receipt is for
// scope and matches the completed stream's reported size and sha256 digest.
func streamEvidenceMatches(receipt capturestream.Receipt, scope capturestream.Scope, output result) bool {
	return receipt.Scope == scope && output.StreamCompleted && measuredAs(output, receipt)
}
