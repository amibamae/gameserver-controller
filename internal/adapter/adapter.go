// Package adapter defines the in-process Go adapter boundary and validates
// calls against the versioned protocol schemas.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

var (
	ErrAdapterNotFound       = errors.New("adapter is not registered")
	ErrUnsupportedCapability = errors.New("adapter does not advertise capability")
	ErrMutatingContext       = errors.New("mutating call requires durable operation and step IDs")
)

type Category string

const (
	CategoryHosting     Category = "hosting"
	CategoryStorage     Category = "storage"
	CategoryExecution   Category = "execution"
	CategoryGame        Category = "game"
	CategoryIntegration Category = "integration"
)

type Descriptor struct {
	AdapterID               string         `json:"adapter_id"`
	Category                Category       `json:"category"`
	DisplayName             string         `json:"display_name"`
	ImplementationVersion   string         `json:"implementation_version"`
	ProtocolVersions        []string       `json:"protocol_versions"`
	ContractVersions        []string       `json:"contract_versions"`
	ConfigurationSchemaURI  string         `json:"configuration_schema_uri,omitempty"`
	RequiredPermissions     []string       `json:"required_permissions"`
	Capabilities            []Capability   `json:"capabilities"`
	Constraints             map[string]any `json:"constraints,omitempty"`
	DocumentationURI        string         `json:"documentation_uri,omitempty"`
	StreamTransportProfiles []string       `json:"stream_transport_profiles,omitempty"`
}

type Capability struct {
	Name                string         `json:"name"`
	Version             string         `json:"version"`
	Mutating            bool           `json:"mutating"`
	InputSchemaURI      string         `json:"input_schema_uri"`
	OutputSchemaURI     string         `json:"output_schema_uri"`
	Constraints         map[string]any `json:"constraints,omitempty"`
	RequiredPermissions []string       `json:"required_permissions,omitempty"`
}

type RequestContext struct {
	StreamTransfers []capturestream.Reference `json:"stream_transfers,omitempty"`
	streamAccess    *streamAccess             `json:"-"`
	SecretRefs      []SecretReference         `json:"secret_refs,omitempty"`
	RequestID       string                    `json:"request_id"`
	CorrelationID   string                    `json:"correlation_id"`
	OperationID     string                    `json:"operation_id,omitempty"`
	StepID          string                    `json:"step_id,omitempty"`
	ServerID        string                    `json:"server_id,omitempty"`
	EnvironmentID   string                    `json:"environment_id"`
	Deadline        time.Time                 `json:"deadline"`
	IdempotencyKey  string                    `json:"idempotency_key,omitempty"`
}

// StreamTransfer pairs a protocol handle with a controller-owned bounded
// source. Verify must succeed after the adapter has consumed the complete
// stream; Close is called after invocation regardless of outcome.
type StreamTransfer struct {
	Reference capturestream.Reference
	Source    io.ReadCloser
	Sink      io.WriteCloser
	Verify    func() error
}

// SecretReference is an opaque pointer to protected secret material. Adapters
// receive references only, never resolved values, and must still enforce their
// own configured authorization policy before resolving one.
type SecretReference struct {
	SecretRef       string   `json:"secret_ref"`
	Purpose         string   `json:"purpose"`
	PermissionScope []string `json:"permission_scope,omitempty"`
	VersionRef      string   `json:"version_ref,omitempty"`
}

type InvokeParams struct {
	ProtocolVersion string          `json:"protocol_version"`
	Capability      string          `json:"capability"`
	RequestContext  RequestContext  `json:"request_context"`
	Input           json.RawMessage `json:"input"`
}

type InvokeRequest struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      string       `json:"id"`
	Method  string       `json:"method"`
	Params  InvokeParams `json:"params"`
}

type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeAccepted  Outcome = "accepted"
	OutcomeUnknown   Outcome = "outcome_unknown"
)

type InvokeResult struct {
	Outcome              Outcome         `json:"outcome"`
	ExternalOperationRef json.RawMessage `json:"external_operation_ref,omitempty"`
	Output               json.RawMessage `json:"output"`
	ObservedAt           time.Time       `json:"observed_at"`
}

type InvokeResponse struct {
	JSONRPC string       `json:"jsonrpc"`
	ID      string       `json:"id"`
	Result  InvokeResult `json:"result"`
}

type Implementation interface {
	Descriptor() Descriptor
	Invoke(context.Context, InvokeRequest) (InvokeResponse, error)
}

// InvocationError reports whether a failed call may already have changed an
// external system. Callers must reconcile mutating calls with this flag before
// retrying them.
type InvocationError struct {
	MayHaveSideEffects bool
	Cause              error
}

func (e *InvocationError) Error() string { return e.Cause.Error() }
func (e *InvocationError) Unwrap() error { return e.Cause }

type registeredAdapter struct {
	implementation Implementation
	descriptor     Descriptor
	capabilities   map[string]Capability
}

type Registry struct {
	schemas  *schemaregistry.Registry
	version  string
	mu       sync.RWMutex
	adapters map[string]registeredAdapter
}

func NewRegistry(schemas *schemaregistry.Registry) (*Registry, error) {
	if schemas == nil || schemas.ProtocolVersion() == "" {
		return nil, errors.New("a loaded protocol schema registry is required")
	}
	return &Registry{schemas: schemas, version: schemas.ProtocolVersion(), adapters: make(map[string]registeredAdapter)}, nil
}

// ProtocolVersion returns the exact protocol version selected by this
// registry. Adapters registered here must advertise this version.
func (r *Registry) ProtocolVersion() string {
	if r == nil {
		return ""
	}
	return r.version
}

func (r *Registry) Descriptor(adapterID string) (Descriptor, error) {
	if r == nil {
		return Descriptor{}, ErrAdapterNotFound
	}
	r.mu.RLock()
	registered, ok := r.adapters[adapterID]
	r.mu.RUnlock()
	if !ok {
		return Descriptor{}, ErrAdapterNotFound
	}
	return cloneDescriptor(registered.descriptor), nil
}

func (r *Registry) Invoke(ctx context.Context, adapterID string, request InvokeRequest) (InvokeResponse, error) {
	response, err := r.invoke(ctx, adapterID, request)
	if err != nil && len(request.Params.RequestContext.StreamTransfers) > 0 {
		// Stream handles are bearer credentials. Neither adapter errors nor
		// schema diagnostics may return their contents to ordinary logging.
		redacted := errors.New("streamed adapter invocation failed")
		var invocation *InvocationError
		if errors.As(err, &invocation) {
			return InvokeResponse{}, &InvocationError{MayHaveSideEffects: invocation.MayHaveSideEffects, Cause: redacted}
		}
		return InvokeResponse{}, redacted
	}
	return response, err
}

func (r *Registry) invoke(ctx context.Context, adapterID string, request InvokeRequest) (InvokeResponse, error) {
	registered, capability, err := r.prepareInvocation(ctx, adapterID, request)
	if err != nil {
		return InvokeResponse{}, err
	}
	transfers := request.Params.RequestContext.StreamTransfers
	inputCopy := append(json.RawMessage(nil), request.Params.Input...)
	request.Params.Input = inputCopy
	request.Params.RequestContext.StreamTransfers = cloneSlice(transfers)
	if err := ctx.Err(); err != nil {
		return InvokeResponse{}, err
	}
	callContext, cancel := context.WithDeadline(ctx, request.Params.RequestContext.Deadline)
	defer cancel()
	if err := callContext.Err(); err != nil {
		return InvokeResponse{}, err
	}
	response, err := registered.implementation.Invoke(callContext, request)
	if err != nil {
		return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: err}
	}
	if err := r.checkResponse(capability, request.ID, transfers, response); err != nil {
		return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: err}
	}
	return response, nil
}
