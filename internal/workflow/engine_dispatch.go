package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func (e *Engine) dispatch(ctx context.Context, running store.StoredOperation, stepIndex int, definition Definition, capability adapter.Capability) (store.StoredOperation, error) {
	step := running.Record.Steps[stepIndex]
	deadline := e.stepDeadline(ctx, running.Record)
	if !deadline.After(time.Now()) {
		errValue := protocolError("core.operation.deadline_exceeded", "timeout", "The operation deadline expired before adapter dispatch.", running.Record.OperationID, false, "inspect_operation", true)
		return e.failStep(ctx, running, stepIndex, definition, errValue)
	}
	secretRefs, streams, refusal := prepareDispatch(ctx, running.Record, step, definition, deadline)
	if refusal != nil {
		return e.failBeforeDispatch(ctx, running, stepIndex, definition, refusal)
	}
	defer func() {
		// invokeStep closes the streams and turns a close failure into an
		// uncertain outcome. This deferred close only releases them if
		// invokeStep is never reached, when no outcome is left to report.
		_ = streams.close()
	}()

	request := newInvokeRequest(running.Record, step, deadline, secretRefs, streams)
	response, invokeErr := e.invokeStep(ctx, step.AdapterID, request, deadline, streams, capability.Mutating)
	if invokeErr != nil {
		return e.recordInvokeFailure(ctx, running, stepIndex, definition, invokeErr)
	}
	if code, message := e.responseProblem(response, request.ID, capability); code != "" {
		return e.markOutcomeUnknown(running, stepIndex, reconcileRequired(code, message, running.Record.OperationID))
	}
	return e.applyAdapterResult(ctx, running, stepIndex, definition, response)
}

// dispatchRefusal names why a step was stopped before its adapter call.
type dispatchRefusal struct{ code, message, category string }

var (
	refusedByDeadline      = &dispatchRefusal{"core.operation.deadline_exceeded", "The operation deadline expired before adapter dispatch.", "timeout"}
	refusedByDispatchGuard = &dispatchRefusal{"core.workflow.dispatch_guard_failed", "A controller safety check failed immediately before adapter dispatch.", "safety_blocked"}
	refusedSecretRefs      = &dispatchRefusal{"core.workflow.secret_reference_failed", "The controller could not authorize the secret references required by this adapter step.", "safety_blocked"}
	refusedStreams         = &dispatchRefusal{"core.workflow.stream_transfer_failed", "The controller could not prepare the bounded stream inputs required by this adapter step.", "safety_blocked"}
)

// prepareDispatch runs the definition's last safety check and collects the
// step's secret references and validated streams, in that order.
func prepareDispatch(ctx context.Context, record store.OperationRecord, step store.OperationStep, definition Definition, deadline time.Time) ([]adapter.SecretReference, *preparedStreams, *dispatchRefusal) {
	if guard, ok := definition.(DispatchGuard); ok {
		if err := guard.BeforeAdapterDispatch(ctx, record, step); err != nil {
			return nil, nil, refusedByDispatchGuard
		}
	}
	var secretRefs []adapter.SecretReference
	if provider, ok := definition.(StepSecretReferences); ok {
		refs, err := provider.SecretReferencesForStep(ctx, record, step)
		if err != nil {
			return nil, nil, refusedSecretRefs
		}
		secretRefs = refs
	}
	var transfers []adapter.StreamTransfer
	if provider, ok := definition.(StepStreamTransfers); ok {
		prepared, err := provider.StreamTransfersForStep(ctx, record, step, deadline)
		if err != nil {
			return nil, nil, refusedStreams
		}
		transfers = prepared
	}
	streams, ok := newPreparedStreams(transfers)
	if !ok {
		return nil, nil, refusedStreams
	}
	return secretRefs, streams, nil
}

func newInvokeRequest(record store.OperationRecord, step store.OperationStep, deadline time.Time, secretRefs []adapter.SecretReference, streams *preparedStreams) adapter.InvokeRequest {
	requestID := adapterRequestID(record.OperationID, step.StepID, step.Attempt)
	correlationID := record.CorrelationID
	if correlationID == "" {
		correlationID = record.OperationID
	}
	request := adapter.InvokeRequest{
		JSONRPC: "2.0",
		ID:      requestID,
		Method:  "adapter.invoke",
		Params: adapter.InvokeParams{
			ProtocolVersion: record.ProtocolVersion,
			Capability:      step.CapabilityName,
			RequestContext: adapter.RequestContext{
				RequestID: requestID, CorrelationID: correlationID,
				OperationID: record.OperationID, StepID: step.StepID,
				ServerID: record.Target.ServerID, EnvironmentID: record.Target.EnvironmentID,
				Deadline: deadline, SecretRefs: secretRefs, StreamTransfers: streams.references,
			},
			Input: append(json.RawMessage(nil), effectiveStepInput(step)...),
		},
	}
	request.Params.RequestContext.SetStreamSources(streams.sources)
	request.Params.RequestContext.SetStreamDestinations(streams.destinations)
	return request
}

// invokeStep calls the adapter under the step deadline, then closes and
// verifies the streams. A stream that cannot be closed or verified after a
// successful call makes a mutating call's outcome uncertain.
func (e *Engine) invokeStep(ctx context.Context, adapterID string, request adapter.InvokeRequest, deadline time.Time, streams *preparedStreams, mutating bool) (adapter.InvokeResponse, error) {
	callContext, cancel := context.WithDeadline(ctx, deadline)
	response, invokeErr := e.adapters.Invoke(callContext, adapterID, request)
	cancel()
	if closeErr := streams.close(); invokeErr == nil && closeErr != nil {
		return response, &adapter.InvocationError{MayHaveSideEffects: mutating, Cause: errors.New("controller-provided stream could not be closed cleanly")}
	}
	if invokeErr == nil && streams.verify() != nil {
		return response, &adapter.InvocationError{MayHaveSideEffects: mutating, Cause: errors.New("controller-provided stream was not fully and correctly consumed")}
	}
	return response, invokeErr
}

// recordInvokeFailure leaves a call that may have taken effect for
// reconciliation and fails the step for any other error.
func (e *Engine) recordInvokeFailure(ctx context.Context, running store.StoredOperation, stepIndex int, definition Definition, invokeErr error) (store.StoredOperation, error) {
	var ambiguous *adapter.InvocationError
	if errors.As(invokeErr, &ambiguous) && ambiguous.MayHaveSideEffects {
		return e.markOutcomeUnknown(running, stepIndex, reconcileRequired("core.adapter.outcome_unknown", "The adapter call may have taken effect but returned no definitive result.", running.Record.OperationID))
	}
	failure := protocolError("core.adapter.invocation_failed", "provider_failure", "The adapter call failed before a side effect was confirmed.", running.Record.OperationID, false, "inspect_operation", true)
	return e.failStep(ctx, running, stepIndex, definition, failure)
}

// responseProblem returns the error code and message for a response that is
// malformed, mismatched or carries an unsupported outcome, or "" when the
// response can be applied.
func (e *Engine) responseProblem(response adapter.InvokeResponse, requestID string, capability adapter.Capability) (string, string) {
	if e.responseMalformed(response, requestID, capability) {
		return "core.adapter.invalid_response", "The adapter returned a malformed response after dispatch."
	}
	switch response.Result.Outcome {
	case adapter.OutcomeCompleted, adapter.OutcomeAccepted, adapter.OutcomeUnknown:
		return "", ""
	}
	return "core.adapter.invalid_outcome", "The adapter returned an unsupported outcome after dispatch."
}

// responseMalformed reports whether a response cannot be encoded, does not
// answer requestID as JSON-RPC 2.0 with an observation time, or fails the
// response schema or the capability's output schema, checked in that order.
func (e *Engine) responseMalformed(response adapter.InvokeResponse, requestID string, capability adapter.Capability) bool {
	encodedResponse, encodeErr := json.Marshal(response)
	responseSchemaID := "urn:gameserver-controller:" + e.schemas.ProtocolVersion() + ":rpc:adapter-invoke-result"
	if encodeErr != nil || !responseAnswers(response, requestID) {
		return true
	}
	if e.schemas.ValidateJSON(responseSchemaID, encodedResponse) != nil {
		return true
	}
	return e.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output) != nil
}

// responseAnswers reports whether a response is JSON-RPC 2.0, answers
// requestID and records when the outcome was observed.
func responseAnswers(response adapter.InvokeResponse, requestID string) bool {
	return response.JSONRPC == "2.0" && response.ID == requestID && !response.Result.ObservedAt.IsZero()
}

// applyAdapterResult records a valid response: the step's external
// reference, then its completion, wait or unknown outcome.
func (e *Engine) applyAdapterResult(ctx context.Context, running store.StoredOperation, stepIndex int, definition Definition, response adapter.InvokeResponse) (store.StoredOperation, error) {
	updated := running.Record
	updated.Error = nil
	updated.CurrentStepID = nil
	updated.Steps[stepIndex].Error = nil
	if len(response.Result.ExternalOperationRef) > 0 {
		recorded := updated.Steps[stepIndex].ExternalOperationRef
		if len(recorded) > 0 && !equalJSON(recorded, response.Result.ExternalOperationRef) {
			return e.markOutcomeUnknown(running, stepIndex, reconcileRequired("core.adapter.external_reference_changed", "The adapter returned a different external operation reference for a recorded step.", running.Record.OperationID))
		}
		updated.Steps[stepIndex].ExternalOperationRef = append(json.RawMessage(nil), response.Result.ExternalOperationRef...)
	}
	switch response.Result.Outcome {
	case adapter.OutcomeCompleted:
		updated.Steps[stepIndex].Status = protocolstate.StepSucceeded
		updated.Steps[stepIndex].Output = append(json.RawMessage(nil), response.Result.Output...)
		return e.updateOperation(running.Revision, updated)
	case adapter.OutcomeAccepted:
		updated.Status = protocolstate.OperationWaiting
		updated.Steps[stepIndex].Status = protocolstate.StepWaiting
		return e.updateOperation(running.Revision, updated)
	case adapter.OutcomeUnknown:
		updated.CurrentStepID = stringPointer(updated.Steps[stepIndex].StepID)
		markStepOutcomeUnknown(&updated, stepIndex, reconcileRequired("core.adapter.outcome_unknown", "The adapter reported that the external outcome is unknown.", running.Record.OperationID))
		return e.updateOperation(running.Revision, updated)
	default:
		failure := protocolError("core.adapter.invalid_outcome", "internal", "The adapter returned an unsupported outcome value.", running.Record.OperationID, false, "inspect_operation", true)
		return e.failStep(ctx, running, stepIndex, definition, failure)
	}
}
