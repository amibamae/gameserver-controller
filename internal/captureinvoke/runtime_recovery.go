package captureinvoke

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// RecoverSealedCapture returns a previously sealed stream only when the
// durable operation still identifies the exact unresolved backup capture.
// It makes no adapter or provider calls and never redispatches the capture.
func (r *Runtime) RecoverSealedCapture(ctx context.Context, operationID, stepID string) (RecoveredCapture, error) {
	if !recoveryRequestValid(r, ctx, operationID, stepID) {
		return RecoveredCapture{}, errors.New("invalid capture recovery request")
	}
	staging, ok := r.staging.(CaptureRecoveryStore)
	if !ok {
		return RecoveredCapture{}, errors.New("capture staging does not support recovery")
	}
	scope, input, err := r.recoveryScope(operationID, stepID)
	if err != nil {
		return RecoveredCapture{}, err
	}
	return r.openSealedCapture(ctx, staging, scope, input)
}

// recoveryRequestValid reports whether a runtime, a live context, and both
// durable IDs were given.
func recoveryRequestValid(r *Runtime, ctx context.Context, operationID, stepID string) bool {
	return r != nil && contextLive(ctx) && operationID != "" && stepID != ""
}

// contextLive reports whether ctx is present and not yet done.
func contextLive(ctx context.Context) bool {
	return ctx != nil && ctx.Err() == nil
}

// recoveryScope returns the stream scope of the recoverable capture step and
// its recorded input, once the operation still holds the server lock.
func (r *Runtime) recoveryScope(operationID, stepID string) (capturestream.Scope, capture, error) {
	op, step, input, err := r.recoverableCapture(operationID, stepID)
	if err != nil {
		return capturestream.Scope{}, capture{}, err
	}
	instance, ok := captureInstance(input.Target.InstanceRef, op.Target.EnvironmentID)
	if !ok {
		return capturestream.Scope{}, capture{}, errors.New("recorded capture instance is invalid")
	}
	if !r.holdsServerLock(operationID, op.Target.EnvironmentID, op.Target.ServerID) {
		return capturestream.Scope{}, capture{}, errors.New("capture recovery requires the exact durable server lock")
	}
	ref := captureStepRef{operationID: operationID, stepID: stepID, adapterID: step.AdapterID, serverID: op.Target.ServerID, environmentID: op.Target.EnvironmentID}
	return captureScope(ref, instance, input), input, nil
}

// openSealedCapture opens the sealed evidence for scope and returns it once
// the sealed response matches the receipt and the durable request. The
// stream is closed when the response does not match.
func (r *Runtime) openSealedCapture(ctx context.Context, staging CaptureRecoveryStore, scope capturestream.Scope, input capture) (RecoveredCapture, error) {
	data, receipt, encoded, err := staging.OpenVerifiedCapture(ctx, scope)
	if err != nil || data == nil {
		return RecoveredCapture{}, errors.New("sealed capture evidence is unavailable or invalid")
	}
	if err := r.checkSealedResponse(ctx, scope, receipt, encoded, input); err != nil {
		// The mismatch is the error to report. The stream is read-only
		// evidence that is being abandoned, so a close failure changes
		// nothing the caller could act on.
		_ = data.Close()
		return RecoveredCapture{}, err
	}
	return RecoveredCapture{Data: data, Receipt: receipt, Output: append(json.RawMessage(nil), encoded...)}, nil
}

// recoverableCapture loads the operation and checks that stepID is its
// unresolved capture step, with a compatible adapter and an input that
// targets the operation's server and instance.
func (r *Runtime) recoverableCapture(operationID, stepID string) (store.OperationRecord, *store.OperationStep, capture, error) {
	saved, err := r.records.GetOperation(operationID)
	if err != nil {
		return store.OperationRecord{}, nil, capture{}, errors.New("capture operation is unavailable")
	}
	op := saved.Record
	if !r.atRecoverableCaptureStep(op, operationID, stepID) {
		return store.OperationRecord{}, nil, capture{}, errors.New("capture operation is not at the exact recoverable step")
	}
	step := findStep(op, stepID)
	if !unresolvedCaptureStep(step) {
		return store.OperationRecord{}, nil, capture{}, errors.New("capture step is not unresolved")
	}
	input, err := r.recordedCaptureInput(op, *step)
	if err != nil {
		return store.OperationRecord{}, nil, capture{}, err
	}
	return op, step, input, nil
}

// recordedCaptureInput checks that the step's adapter still advertises its
// capture and decodes the step's schema-valid input, which must target the
// operation's server and instance.
func (r *Runtime) recordedCaptureInput(op store.OperationRecord, step store.OperationStep) (capture, error) {
	descriptor, err := r.adapters.Descriptor(step.AdapterID)
	if err != nil {
		return capture{}, errors.New("capture adapter is unavailable")
	}
	if !r.advertisesCapture(descriptor, step) || r.schemas.ValidateJSON(r.captureSchema("input"), step.Input) != nil {
		return capture{}, errors.New("recorded capture capability or input is incompatible")
	}
	var input capture
	if json.Unmarshal(step.Input, &input) != nil || !captureTargetsOperation(input, op) {
		return capture{}, errors.New("recorded capture target differs from its operation")
	}
	return input, nil
}

// atRecoverableCaptureStep reports whether op is a reconciling backup of
// this protocol version whose current step is stepID.
func (r *Runtime) atRecoverableCaptureStep(op store.OperationRecord, operationID, stepID string) bool {
	return op.OperationID == operationID && backupInState(op, protocolstate.OperationReconciling, r.schemas.ProtocolVersion()) &&
		atStep(op, stepID)
}

// unresolvedCaptureStep reports whether step is a dispatched capture whose
// outcome is unknown and whose output was never recorded.
func unresolvedCaptureStep(step *store.OperationStep) bool {
	return dispatchedCaptureStep(step, protocolstate.StepOutcomeUnknown) && step.Output == nil
}

func captureTargetsOperation(input capture, op store.OperationRecord) bool {
	return captureTargets(input, op.Target.ServerID, op.Target.EnvironmentID, op.Target.InstanceRef)
}

// checkSealedResponse checks the sealed adapter response against the
// staging receipt's measured stream and the exact durable request.
func (r *Runtime) checkSealedResponse(ctx context.Context, scope capturestream.Scope, receipt capturestream.Receipt, encoded json.RawMessage, input capture) error {
	if ctx.Err() != nil || receipt.Scope != scope || len(encoded) == 0 || r.schemas.ValidateJSON(r.captureSchema("output"), encoded) != nil {
		return errors.New("sealed capture response is missing or invalid")
	}
	var output result
	if json.Unmarshal(encoded, &output) != nil || !resultMatchesReceipt(output, receipt) {
		return errors.New("sealed capture response does not match measured stream evidence")
	}
	if !sameCapture(input, output.capture) {
		return errors.New("sealed capture response does not match the exact durable request")
	}
	return nil
}

// resultMatchesReceipt reports whether output is a completed capture whose
// reported size and digest are the receipt's.
func resultMatchesReceipt(output result, receipt capturestream.Receipt) bool {
	return output.Outcome == "captured" && output.StreamCompleted && measuredAs(output, receipt)
}
