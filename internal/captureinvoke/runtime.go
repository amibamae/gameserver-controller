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

// Stage owns private staging bytes. Seal must durably associate verified
// capture evidence with the operation/step before returning. Discard removes
// failed staging. Implementations must provide bounded, cancelable I/O.
type Stage interface {
	io.Writer
	Seal(context.Context, capturestream.Receipt) error
	Discard() error
}
type Staging interface {
	Open(context.Context, capturestream.Scope) (Stage, error)
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
	if ctx == nil || ctx.Err() != nil || len(request.Params.RequestContext.StreamTransfers) != 0 {
		return adapter.InvokeResponse{}, errors.New("invalid capture dispatch context")
	}
	c := request.Params.RequestContext
	saved, err := r.records.GetOperation(c.OperationID)
	if err != nil {
		return adapter.InvokeResponse{}, errors.New("capture operation is unavailable")
	}
	op := saved.Record
	if op.OperationType != "server.backup" || op.Status != protocolstate.OperationRunning || op.BackupInitialLifecycleState == "" || op.CurrentStepID == nil || *op.CurrentStepID != c.StepID || op.Target.ServerID != c.ServerID || op.Target.EnvironmentID != c.EnvironmentID || op.ProtocolVersion != request.Params.ProtocolVersion {
		return adapter.InvokeResponse{}, errors.New("capture requires its running durable backup step")
	}
	var step *store.OperationStep
	for i := range op.Steps {
		if op.Steps[i].StepID == c.StepID {
			step = &op.Steps[i]
			break
		}
	}
	if step == nil || step.Status != protocolstate.StepRunning || step.Attempt < 1 || step.AdapterID != adapterID || step.CapabilityName != "game.backup.capture" || !sameJSON(step.Input, request.Params.Input) {
		return adapter.InvokeResponse{}, errors.New("capture request does not match durable step")
	}
	descriptor, err := r.adapters.Descriptor(adapterID)
	if err != nil {
		return adapter.InvokeResponse{}, errors.New("capture adapter is unavailable")
	}
	matched := false
	for _, capability := range descriptor.Capabilities {
		if capability.Name == step.CapabilityName && capability.Version == step.CapabilityVersion && capability.InputSchemaURI == "urn:gameserver-controller:"+r.schemas.ProtocolVersion()+":game:backup-capture-input" && capability.OutputSchemaURI == "urn:gameserver-controller:"+r.schemas.ProtocolVersion()+":game:backup-capture-output" {
			matched = true
		}
	}
	if !matched || (op.Deadline != nil && c.Deadline.After(*op.Deadline)) {
		return adapter.InvokeResponse{}, errors.New("capture capability or deadline differs from durable plan")
	}
	lock, err := r.records.GetResourceLock(store.LogicalServerLockScope(c.EnvironmentID, c.ServerID))
	if err != nil || lock.OperationID != c.OperationID || lock.Scope != store.LogicalServerLockScope(c.EnvironmentID, c.ServerID) {
		return adapter.InvokeResponse{}, errors.New("capture requires the durable server lock")
	}
	version := r.schemas.ProtocolVersion()
	if err := r.schemas.ValidateJSON("urn:gameserver-controller:"+version+":game:backup-capture-input", request.Params.Input); err != nil {
		return adapter.InvokeResponse{}, errors.New("invalid capture input")
	}
	var input capture
	if json.Unmarshal(request.Params.Input, &input) != nil || input.Target.ServerID != c.ServerID || input.Target.EnvironmentID != c.EnvironmentID || !sameJSON(input.Target.InstanceRef, op.Target.InstanceRef) {
		return adapter.InvokeResponse{}, errors.New("capture target does not match durable operation")
	}
	var instance struct {
		AdapterID     string `json:"adapter_id"`
		EnvironmentID string `json:"environment_id"`
		Kind          string `json:"resource_kind"`
		ID            string `json:"external_id"`
	}
	if json.Unmarshal(input.Target.InstanceRef, &instance) != nil || instance.EnvironmentID != c.EnvironmentID || instance.Kind != "instance" {
		return adapter.InvokeResponse{}, errors.New("invalid capture instance")
	}
	scope := capturestream.Scope{OperationID: c.OperationID, StepID: c.StepID, AdapterID: adapterID, ServerID: c.ServerID, EnvironmentID: c.EnvironmentID, InstanceAdapterID: instance.AdapterID, InstanceID: instance.ID, DataGenerationID: input.GenerationID, DataSetID: input.DataSetID, CaptureID: input.CaptureID}
	stage, err := r.staging.Open(ctx, scope)
	if err != nil || stage == nil {
		return adapter.InvokeResponse{}, errors.New("capture staging is unavailable")
	}
	counted := &countingSink{sink: stage}
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
	if r.schemas.ValidateJSON("urn:gameserver-controller:"+version+":game:backup-capture-output", response.Result.Output) != nil || json.Unmarshal(response.Result.Output, &output) != nil {
		return adapter.InvokeResponse{}, uncertain()
	}
	a, _ := json.Marshal(input)
	b, _ := json.Marshal(output.capture)
	if !sameJSON(a, b) {
		return adapter.InvokeResponse{}, uncertain()
	}
	if output.Outcome == "outcome_unknown" {
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
	if ctx.Err() != nil || stage.Seal(ctx, receipt) != nil {
		return adapter.InvokeResponse{}, uncertain()
	}
	sealed = true
	return response, nil
}

type countingSink struct {
	sink io.Writer
	size atomic.Int64
}

func (w *countingSink) Write(p []byte) (int, error) {
	n, err := w.sink.Write(p)
	if n > 0 && n <= len(p) {
		w.size.Add(int64(n))
	}
	return n, err
}
