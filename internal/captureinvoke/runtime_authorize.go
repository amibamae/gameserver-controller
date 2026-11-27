package captureinvoke

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// captureDispatch is a capture request that matched its durable backup step:
// the exact capture input and the stream scope it may write to.
type captureDispatch struct {
	input capture
	scope capturestream.Scope
}

// authorizeCapture checks, before any staging or dispatch, that the request is
// the running step of a running server.backup: same adapter, capability,
// version, input, deadline, server lock, target instance, and (when given) a
// short-lived controller-bound stream destination.
func (r *Runtime) authorizeCapture(ctx context.Context, adapterID string, request adapter.InvokeRequest) (captureDispatch, error) {
	c := request.Params.RequestContext
	if !contextLive(ctx) || len(c.StreamTransfers) > 1 {
		return captureDispatch{}, errors.New("invalid capture dispatch context")
	}
	op, step, err := r.runningCaptureStep(adapterID, request)
	if err != nil {
		return captureDispatch{}, err
	}
	if err := r.checkCapturePlan(adapterID, op, *step, c); err != nil {
		return captureDispatch{}, err
	}
	input, err := r.captureRequestInput(request, op)
	if err != nil {
		return captureDispatch{}, err
	}
	scope, err := r.dispatchScope(adapterID, c, input)
	if err != nil {
		return captureDispatch{}, err
	}
	return captureDispatch{input: input, scope: scope}, nil
}

// dispatchScope checks the request's external destination, when it names
// one, and the capture's target instance, and returns the stream scope the
// capture may write to.
func (r *Runtime) dispatchScope(adapterID string, c adapter.RequestContext, input capture) (capturestream.Scope, error) {
	if len(c.StreamTransfers) == 1 && !r.validCaptureDestination(c) {
		return capturestream.Scope{}, errors.New("capture destination is not a valid controller-bound stream")
	}
	instance, ok := captureInstance(input.Target.InstanceRef, c.EnvironmentID)
	if !ok {
		return capturestream.Scope{}, errors.New("invalid capture instance")
	}
	ref := captureStepRef{operationID: c.OperationID, stepID: c.StepID, adapterID: adapterID, serverID: c.ServerID, environmentID: c.EnvironmentID}
	return captureScope(ref, instance, input), nil
}

// runningCaptureStep loads the request's operation and checks that it is a
// running backup at the request's step, and that the step is the running
// capture recorded for this adapter with exactly the request's input.
func (r *Runtime) runningCaptureStep(adapterID string, request adapter.InvokeRequest) (store.OperationRecord, *store.OperationStep, error) {
	c := request.Params.RequestContext
	saved, err := r.records.GetOperation(c.OperationID)
	if err != nil {
		return store.OperationRecord{}, nil, errors.New("capture operation is unavailable")
	}
	op := saved.Record
	if !atRunningBackupStep(op, c, request.Params.ProtocolVersion) {
		return store.OperationRecord{}, nil, errors.New("capture requires its running durable backup step")
	}
	step := findStep(op, c.StepID)
	if !runningCaptureStepMatches(step, adapterID, request.Params.Input) {
		return store.OperationRecord{}, nil, errors.New("capture request does not match durable step")
	}
	return op, step, nil
}

// atRunningBackupStep reports whether op is a running backup of
// protocolVersion whose current step and target are the request context's.
func atRunningBackupStep(op store.OperationRecord, c adapter.RequestContext, protocolVersion string) bool {
	return backupInState(op, protocolstate.OperationRunning, protocolVersion) && atStep(op, c.StepID) &&
		operationTargetsServer(op, c.ServerID, c.EnvironmentID)
}

// operationTargetsServer reports whether op targets serverID in
// environmentID.
func operationTargetsServer(op store.OperationRecord, serverID, environmentID string) bool {
	return op.Target.ServerID == serverID && op.Target.EnvironmentID == environmentID
}

// runningCaptureStepMatches reports whether step is a dispatched, running
// capture by adapterID whose recorded input is exactly input.
func runningCaptureStepMatches(step *store.OperationStep, adapterID string, input json.RawMessage) bool {
	return dispatchedCaptureStep(step, protocolstate.StepRunning) && step.AdapterID == adapterID && sameJSON(step.Input, input)
}

// checkCapturePlan checks that the adapter still advertises the step's
// capability, that the invocation deadline is within the operation deadline,
// and that the operation holds the server lock.
func (r *Runtime) checkCapturePlan(adapterID string, op store.OperationRecord, step store.OperationStep, c adapter.RequestContext) error {
	descriptor, err := r.adapters.Descriptor(adapterID)
	if err != nil {
		return errors.New("capture adapter is unavailable")
	}
	if !r.advertisesCapture(descriptor, step) || !withinOperationDeadline(op, c) {
		return errors.New("capture capability or deadline differs from durable plan")
	}
	if !r.holdsServerLock(c.OperationID, c.EnvironmentID, c.ServerID) {
		return errors.New("capture requires the durable server lock")
	}
	return nil
}

// withinOperationDeadline reports whether the invocation deadline is no later
// than the operation deadline, when the operation has one.
func withinOperationDeadline(op store.OperationRecord, c adapter.RequestContext) bool {
	return op.Deadline == nil || !c.Deadline.After(*op.Deadline)
}

// captureRequestInput validates and decodes the request input and checks that
// it targets the request's server and environment and the operation's
// instance.
func (r *Runtime) captureRequestInput(request adapter.InvokeRequest, op store.OperationRecord) (capture, error) {
	if err := r.schemas.ValidateJSON(r.captureSchema("input"), request.Params.Input); err != nil {
		return capture{}, errors.New("invalid capture input")
	}
	var input capture
	if json.Unmarshal(request.Params.Input, &input) != nil || !captureTargetsRequest(input, request.Params.RequestContext, op) {
		return capture{}, errors.New("capture target does not match durable operation")
	}
	return input, nil
}

// captureTargetsRequest reports whether input targets the request context's
// server and environment and the operation's instance.
func captureTargetsRequest(input capture, c adapter.RequestContext, op store.OperationRecord) bool {
	return captureTargets(input, c.ServerID, c.EnvironmentID, op.Target.InstanceRef)
}

// validCaptureDestination reports whether the one external stream transfer is
// an attached, adapter-to-controller destination within the stream size limit
// that expires within five minutes and before the invocation deadline.
func (r *Runtime) validCaptureDestination(c adapter.RequestContext) bool {
	external := c.StreamTransfers[0]
	return r.destinationShapeValid(external) && destinationExpiryValid(external.ExpiresAt, time.Now(), c.Deadline) &&
		c.HasStreamDestination(external.TransferID)
}

// destinationShapeValid reports whether external is a named
// adapter-to-controller transfer of 1 to r.maximum bytes.
func (r *Runtime) destinationShapeValid(external capturestream.Reference) bool {
	return external.TransferID != "" && external.Direction == "adapter_to_controller" && external.MaximumBytes >= 1 &&
		external.MaximumBytes <= r.maximum
}

// destinationExpiryValid reports whether expiresAt is after now, no later
// than the deadline, and no more than five minutes after now.
func destinationExpiryValid(expiresAt, now, deadline time.Time) bool {
	return expiresAt.After(now) && !expiresAt.After(deadline) && !expiresAt.After(now.Add(5*time.Minute))
}
