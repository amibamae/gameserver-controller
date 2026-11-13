// Package captureinvoke adds independently verified capture dispatch to the
// adapter runtime used by the durable workflow engine.
package captureinvoke

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"bytes"
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/gowebpki/jcs"
)

type Records interface {
	GetOperation(string) (store.StoredOperation, error)
	GetResourceLock(string) (store.ResourceLock, error)
}

// ErrStagingNeedsReconciliation signals durable evidence from an earlier
// attempt that must be inspected before another capture can be dispatched.
var ErrStagingNeedsReconciliation = errors.New("capture staging requires reconciliation")

// Stage owns private staging bytes. Seal must durably associate verified
// capture evidence with the operation/step before returning. Discard removes
// failed staging. Implementations must provide bounded, cancelable I/O.
type Stage interface {
	io.Writer
	Seal(context.Context, capturestream.Receipt, []byte) error
	Discard() error
}
type Staging interface {
	Open(context.Context, capturestream.Scope) (Stage, error)
}

// CaptureRecoveryStore reopens one published capture without permitting
// mutation. Implementations must verify the staged stream against receipt.
type CaptureRecoveryStore interface {
	OpenVerifiedCapture(context.Context, capturestream.Scope) (io.ReadCloser, capturestream.Receipt, json.RawMessage, error)
}

// RecoveredCapture contains the exact previously validated adapter output and
// independently verified stream for a durable backup step interrupted after
// capture sealing. The caller owns Data and must close it.
type RecoveredCapture struct {
	Data    io.ReadCloser
	Receipt capturestream.Receipt
	Output  json.RawMessage
}
type Runtime struct {
	adapters *adapter.Registry
	schemas  *schemaregistry.Registry
	records  Records
	broker   *capturestream.Broker
	staging  Staging
	maximum  int64
}

func New(adapters *adapter.Registry, schemas *schemaregistry.Registry, records Records, broker *capturestream.Broker, staging Staging, maximumBytes int64) (*Runtime, error) {
	if adapters == nil || schemas == nil || records == nil || broker == nil || staging == nil || maximumBytes < 1 || maximumBytes > 9007199254740991 || adapters.ProtocolVersion() != schemas.ProtocolVersion() {
		return nil, errors.New("capture runtime requires matching registries, records, broker, staging, and byte limit")
	}
	return &Runtime{adapters: adapters, schemas: schemas, records: records, broker: broker, staging: staging, maximum: maximumBytes}, nil
}
func (r *Runtime) ProtocolVersion() string                          { return r.adapters.ProtocolVersion() }
func (r *Runtime) Descriptor(id string) (adapter.Descriptor, error) { return r.adapters.Descriptor(id) }

// RecoverSealedCapture returns a previously sealed stream only when the
// durable operation still identifies the exact unresolved backup capture.
// It makes no adapter or provider calls and never redispatches the capture.
func (r *Runtime) RecoverSealedCapture(ctx context.Context, operationID, stepID string) (RecoveredCapture, error) {
	failed := RecoveredCapture{}
	if r == nil || ctx == nil || ctx.Err() != nil || operationID == "" || stepID == "" {
		return failed, errors.New("invalid capture recovery request")
	}
	staging, ok := r.staging.(CaptureRecoveryStore)
	if !ok {
		return failed, errors.New("capture staging does not support recovery")
	}
	op, step, input, err := r.recoverableCapture(operationID, stepID)
	if err != nil {
		return failed, err
	}
	instance, ok := captureInstance(input.Target.InstanceRef, op.Target.EnvironmentID)
	if !ok {
		return failed, errors.New("recorded capture instance is invalid")
	}
	if !r.holdsServerLock(operationID, op.Target.EnvironmentID, op.Target.ServerID) {
		return failed, errors.New("capture recovery requires the exact durable server lock")
	}
	scope := captureScope(operationID, stepID, step.AdapterID, op.Target.ServerID, op.Target.EnvironmentID, instance, input)
	data, receipt, encoded, err := staging.OpenVerifiedCapture(ctx, scope)
	if err != nil || data == nil {
		return failed, errors.New("sealed capture evidence is unavailable or invalid")
	}
	if err := r.checkSealedResponse(ctx, scope, receipt, encoded, input); err != nil {
		_ = data.Close()
		return failed, err
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
	descriptor, err := r.adapters.Descriptor(step.AdapterID)
	if err != nil {
		return store.OperationRecord{}, nil, capture{}, errors.New("capture adapter is unavailable")
	}
	if !r.advertisesCapture(descriptor, *step) || r.schemas.ValidateJSON(r.captureSchema("input"), step.Input) != nil {
		return store.OperationRecord{}, nil, capture{}, errors.New("recorded capture capability or input is incompatible")
	}
	var input capture
	if json.Unmarshal(step.Input, &input) != nil || !captureTargetsOperation(input, op) {
		return store.OperationRecord{}, nil, capture{}, errors.New("recorded capture target differs from its operation")
	}
	return op, step, input, nil
}

// atRecoverableCaptureStep reports whether op is a reconciling backup of
// this protocol version whose current step is stepID.
func (r *Runtime) atRecoverableCaptureStep(op store.OperationRecord, operationID, stepID string) bool {
	return op.OperationID == operationID && op.OperationType == "server.backup" &&
		op.Status == protocolstate.OperationReconciling && op.BackupInitialLifecycleState != "" &&
		op.CurrentStepID != nil && *op.CurrentStepID == stepID && op.ProtocolVersion == r.schemas.ProtocolVersion()
}

// unresolvedCaptureStep reports whether step is a dispatched capture whose
// outcome is unknown and whose output was never recorded.
func unresolvedCaptureStep(step *store.OperationStep) bool {
	return step != nil && step.Status == protocolstate.StepOutcomeUnknown && step.Attempt >= 1 &&
		step.CapabilityName == "game.backup.capture" && step.Output == nil
}

func captureTargetsOperation(input capture, op store.OperationRecord) bool {
	return input.Target.ServerID == op.Target.ServerID && input.Target.EnvironmentID == op.Target.EnvironmentID &&
		sameJSON(input.Target.InstanceRef, op.Target.InstanceRef)
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

func resultMatchesReceipt(output result, receipt capturestream.Receipt) bool {
	return output.Outcome == "captured" && output.StreamCompleted && output.Size == receipt.SizeBytes &&
		output.Checksum.Algorithm == "sha256" && output.Checksum.Digest == receipt.SHA256
}

type target struct {
	ServerID      string          `json:"server_id"`
	EnvironmentID string          `json:"environment_id"`
	InstanceRef   json.RawMessage `json:"instance_ref"`
}
type capture struct {
	Target        target `json:"target"`
	ReleaseID     string `json:"release_id"`
	BackupID      string `json:"backup_id"`
	GenerationID  string `json:"data_generation_id"`
	PreparationID string `json:"preparation_id"`
	DataSetID     string `json:"data_set_id"`
	CaptureID     string `json:"capture_id"`
	Mode          string `json:"consistency_mode"`
}
type result struct {
	capture
	Outcome           string          `json:"outcome"`
	StreamCompleted   bool            `json:"stream_completed"`
	Size              int64           `json:"size_bytes"`
	ReconciliationRef json.RawMessage `json:"reconciliation_ref"`
	Checksum          struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"checksum"`
}

func sameJSON(a, b []byte) bool {
	left, e1 := jcs.Transform(a)
	right, e2 := jcs.Transform(b)
	return e1 == nil && e2 == nil && bytes.Equal(left, right)
}
func uncertain() error {
	return &adapter.InvocationError{MayHaveSideEffects: true, Cause: errors.New("capture result requires reconciliation")}
}

func (r *Runtime) Invoke(ctx context.Context, adapterID string, request adapter.InvokeRequest) (returned adapter.InvokeResponse, returnedErr error) {
	if request.Params.Capability != "game.backup.capture" {
		return r.adapters.Invoke(ctx, adapterID, request)
	}
	dispatch, err := r.authorizeCapture(ctx, adapterID, request)
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	c, input, scope := request.Params.RequestContext, dispatch.input, dispatch.scope
	var mirror io.Writer
	stage, err := r.staging.Open(ctx, scope)
	if errors.Is(err, ErrStagingNeedsReconciliation) {
		return adapter.InvokeResponse{}, uncertain()
	}
	if err != nil || stage == nil {
		return adapter.InvokeResponse{}, errors.New("capture staging is unavailable")
	}
	if len(c.StreamTransfers) == 1 {
		mirror, err = c.CreateStream(c.StreamTransfers[0].TransferID)
		if err != nil {
			if stage.Discard() != nil {
				return adapter.InvokeResponse{}, errors.New("capture staging cleanup failed before dispatch")
			}
			return adapter.InvokeResponse{}, errors.New("capture destination could not be claimed")
		}
	}
	counted := &countingSink{sink: stage, mirror: mirror}
	expiry := time.Now().Add(5 * time.Minute)
	if c.Deadline.Before(expiry) {
		expiry = c.Deadline
	}
	ref, ticket, err := r.broker.IssueTracked(scope, r.maximum, expiry, c.Deadline, counted)
	if err != nil {
		if stage.Discard() != nil {
			return adapter.InvokeResponse{}, errors.New("capture staging cleanup failed before dispatch")
		}
		return adapter.InvokeResponse{}, errors.New("capture stream could not be issued")
	}
	sealed := false
	defer func() {
		ticket.Abort()
		if !sealed {
			if stage.Discard() != nil {
				returned = adapter.InvokeResponse{}
				returnedErr = uncertain()
			}
		}
	}()
	request.Params.RequestContext.StreamTransfers = []capturestream.Reference{ref}
	response, err := r.adapters.Invoke(ctx, adapterID, request)
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	var output result
	if r.schemas.ValidateJSON(r.captureSchema("output"), response.Result.Output) != nil || json.Unmarshal(response.Result.Output, &output) != nil {
		return adapter.InvokeResponse{}, uncertain()
	}
	if !sameCapture(input, output.capture) {
		return adapter.InvokeResponse{}, uncertain()
	}
	if output.Outcome == "outcome_unknown" {
		var reference struct {
			AdapterID     string `json:"adapter_id"`
			EnvironmentID string `json:"environment_id"`
		}
		if json.Unmarshal(output.ReconciliationRef, &reference) != nil || reference.AdapterID != adapterID || reference.EnvironmentID != c.EnvironmentID {
			return adapter.InvokeResponse{}, uncertain()
		}
		if len(response.Result.ExternalOperationRef) > 0 && !sameJSON(response.Result.ExternalOperationRef, output.ReconciliationRef) {
			return adapter.InvokeResponse{}, uncertain()
		}
		response.Result.Outcome = adapter.OutcomeUnknown
		response.Result.ExternalOperationRef = append(json.RawMessage(nil), output.ReconciliationRef...)
		return response, nil
	}
	if response.Result.Outcome != adapter.OutcomeCompleted {
		return adapter.InvokeResponse{}, uncertain()
	}
	if output.Outcome == "rejected" {
		ticket.Abort()
		if counted.size.Load() != 0 {
			return adapter.InvokeResponse{}, uncertain()
		}
		return response, nil
	}
	receipt, err := ticket.Evidence()
	if err != nil || receipt.Scope != scope || !output.StreamCompleted || receipt.SizeBytes != output.Size || output.Checksum.Algorithm != "sha256" || receipt.SHA256 != output.Checksum.Digest {
		return adapter.InvokeResponse{}, uncertain()
	}
	if ctx.Err() != nil || stage.Seal(ctx, receipt, response.Result.Output) != nil {
		return adapter.InvokeResponse{}, uncertain()
	}
	sealed = true
	return response, nil
}

type countingSink struct {
	sink   io.Writer
	mirror io.Writer
	size   atomic.Int64
}

func (w *countingSink) Write(p []byte) (int, error) {
	n, err := w.sink.Write(p)
	if err == nil && n == len(p) && w.mirror != nil {
		var mirrored int
		mirrored, err = w.mirror.Write(p)
		if err == nil && mirrored != len(p) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return mirrored, err
		}
	}
	if n > 0 && n <= len(p) {
		w.size.Add(int64(n))
	}
	return n, err
}

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
	if ctx == nil || ctx.Err() != nil || len(request.Params.RequestContext.StreamTransfers) > 1 {
		return captureDispatch{}, errors.New("invalid capture dispatch context")
	}
	c := request.Params.RequestContext
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
	if len(c.StreamTransfers) == 1 && !r.validCaptureDestination(c) {
		return captureDispatch{}, errors.New("capture destination is not a valid controller-bound stream")
	}
	instance, ok := captureInstance(input.Target.InstanceRef, c.EnvironmentID)
	if !ok {
		return captureDispatch{}, errors.New("invalid capture instance")
	}
	return captureDispatch{input: input, scope: captureScope(c.OperationID, c.StepID, adapterID, c.ServerID, c.EnvironmentID, instance, input)}, nil
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
	return op.OperationType == "server.backup" && op.Status == protocolstate.OperationRunning && op.BackupInitialLifecycleState != "" &&
		op.CurrentStepID != nil && *op.CurrentStepID == c.StepID && op.Target.ServerID == c.ServerID &&
		op.Target.EnvironmentID == c.EnvironmentID && op.ProtocolVersion == protocolVersion
}

// runningCaptureStepMatches reports whether step is a dispatched, running
// capture by adapterID whose recorded input is exactly input.
func runningCaptureStepMatches(step *store.OperationStep, adapterID string, input json.RawMessage) bool {
	return step != nil && step.Status == protocolstate.StepRunning && step.Attempt >= 1 && step.AdapterID == adapterID &&
		step.CapabilityName == "game.backup.capture" && sameJSON(step.Input, input)
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
	return input.Target.ServerID == c.ServerID && input.Target.EnvironmentID == c.EnvironmentID &&
		sameJSON(input.Target.InstanceRef, op.Target.InstanceRef)
}

// validCaptureDestination reports whether the one external stream transfer is
// an attached, adapter-to-controller destination within the stream size limit
// that expires within five minutes and before the invocation deadline.
func (r *Runtime) validCaptureDestination(c adapter.RequestContext) bool {
	external := c.StreamTransfers[0]
	now := time.Now()
	return external.TransferID != "" && external.Direction == "adapter_to_controller" && external.MaximumBytes >= 1 && external.MaximumBytes <= r.maximum &&
		external.ExpiresAt.After(now) && !external.ExpiresAt.After(c.Deadline) && !external.ExpiresAt.After(now.Add(5*time.Minute)) &&
		c.HasStreamDestination(external.TransferID)
}

// captureSchema returns the loaded protocol's backup-capture schema ID for
// "input" or "output".
func (r *Runtime) captureSchema(kind string) string {
	return "urn:gameserver-controller:" + r.schemas.ProtocolVersion() + ":game:backup-capture-" + kind
}

// advertisesCapture reports whether the adapter still advertises the step's
// capability name and version with the loaded protocol's capture schemas.
func (r *Runtime) advertisesCapture(descriptor adapter.Descriptor, step store.OperationStep) bool {
	for _, capability := range descriptor.Capabilities {
		if capability.Name == step.CapabilityName && capability.Version == step.CapabilityVersion &&
			capability.InputSchemaURI == r.captureSchema("input") && capability.OutputSchemaURI == r.captureSchema("output") {
			return true
		}
	}
	return false
}

// holdsServerLock reports whether operationID holds the logical-server lock.
func (r *Runtime) holdsServerLock(operationID, environmentID, serverID string) bool {
	scope := store.LogicalServerLockScope(environmentID, serverID)
	lock, err := r.records.GetResourceLock(scope)
	return err == nil && lock.OperationID == operationID && lock.Scope == scope
}

func findStep(op store.OperationRecord, stepID string) *store.OperationStep {
	for i := range op.Steps {
		if op.Steps[i].StepID == stepID {
			return &op.Steps[i]
		}
	}
	return nil
}

// captureInstanceRef is the hosting instance a capture reads from.
type captureInstanceRef struct {
	AdapterID     string `json:"adapter_id"`
	EnvironmentID string `json:"environment_id"`
	Kind          string `json:"resource_kind"`
	ID            string `json:"external_id"`
}

// captureInstance decodes the capture target's instance reference and requires
// an instance in the given environment.
func captureInstance(ref json.RawMessage, environmentID string) (captureInstanceRef, bool) {
	var instance captureInstanceRef
	if json.Unmarshal(ref, &instance) != nil || instance.EnvironmentID != environmentID || instance.Kind != "instance" {
		return captureInstanceRef{}, false
	}
	return instance, true
}

// captureScope is the stream scope that binds one capture to its operation,
// step, adapter, target instance, data generation, data set, and capture ID.
func captureScope(operationID, stepID, adapterID, serverID, environmentID string, instance captureInstanceRef, input capture) capturestream.Scope {
	return capturestream.Scope{OperationID: operationID, StepID: stepID, AdapterID: adapterID, ServerID: serverID, EnvironmentID: environmentID,
		InstanceAdapterID: instance.AdapterID, InstanceID: instance.ID, DataGenerationID: input.GenerationID, DataSetID: input.DataSetID, CaptureID: input.CaptureID}
}

// sameCapture reports whether a result names exactly the requested capture.
func sameCapture(requested, reported capture) bool {
	a, _ := json.Marshal(requested)
	b, _ := json.Marshal(reported)
	return sameJSON(a, b)
}
