// Package captureinvoke adds independently verified capture dispatch to the
// adapter runtime used by the durable workflow engine.
package captureinvoke

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
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

// maximumExactJSONInteger is the largest byte limit a protocol document can
// carry exactly (2^53 - 1).
const maximumExactJSONInteger = 9007199254740991

func New(adapters *adapter.Registry, schemas *schemaregistry.Registry, records Records, broker *capturestream.Broker, staging Staging, maximumBytes int64) (*Runtime, error) {
	if !registriesMatch(adapters, schemas) || !captureStoresPresent(records, broker, staging) || !byteLimitValid(maximumBytes) {
		return nil, errors.New("capture runtime requires matching registries, records, broker, staging, and byte limit")
	}
	return &Runtime{adapters: adapters, schemas: schemas, records: records, broker: broker, staging: staging, maximum: maximumBytes}, nil
}

// registriesMatch reports whether both registries are present and select the
// same protocol version.
func registriesMatch(adapters *adapter.Registry, schemas *schemaregistry.Registry) bool {
	return adapters != nil && schemas != nil && adapters.ProtocolVersion() == schemas.ProtocolVersion()
}

// captureStoresPresent reports whether the durable records, stream broker
// and staging are all present.
func captureStoresPresent(records Records, broker *capturestream.Broker, staging Staging) bool {
	return records != nil && broker != nil && staging != nil
}

// byteLimitValid reports whether maximumBytes is between 1 and the largest
// exact JSON integer.
func byteLimitValid(maximumBytes int64) bool {
	return maximumBytes >= 1 && maximumBytes <= maximumExactJSONInteger
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
	c := request.Params.RequestContext
	stream, err := r.openCaptureStream(ctx, c, dispatch.scope)
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	defer func() {
		if !stream.release() {
			returned = adapter.InvokeResponse{}
			returnedErr = uncertain()
		}
	}()
	request.Params.RequestContext.StreamTransfers = []capturestream.Reference{stream.ref}
	response, err := r.adapters.Invoke(ctx, adapterID, request)
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	return r.checkCaptureResult(ctx, adapterID, c.EnvironmentID, dispatch, stream, response)
}
