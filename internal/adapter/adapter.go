// Package adapter defines the in-process Go adapter boundary and validates
// calls against the versioned protocol schemas.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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

// OpenStream consumes one controller-provided source stream by its transient
// transfer ID. A source can be claimed once per invocation.
func (c RequestContext) OpenStream(transferID string) (io.ReadCloser, error) {
	if c.streamAccess == nil || transferID == "" {
		return nil, errors.New("stream source is unavailable")
	}
	return c.streamAccess.open(transferID)
}

// SetStreamSources attaches invocation-only stream sources. They are excluded
// from JSON and must be set only by the trusted controller runtime.
func (c *RequestContext) SetStreamSources(sources map[string]io.ReadCloser) {
	if c == nil {
		return
	}
	c.streamAccess = &streamAccess{sources: sources, claimed: make(map[string]bool)}
}

// ClearStreamSources removes invocation-only source readers before a request
// is retained for diagnostics or test history.
func (c *RequestContext) ClearStreamSources() {
	if c != nil {
		c.streamAccess = nil
	}
}

type streamAccess struct {
	mu      sync.Mutex
	sources map[string]io.ReadCloser
	claimed map[string]bool
}

func (s *streamAccess) open(id string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	source, ok := s.sources[id]
	if !ok || s.claimed[id] {
		return nil, errors.New("stream source is unavailable")
	}
	s.claimed[id] = true
	return source, nil
}

// StreamTransfer pairs a protocol handle with a controller-owned bounded
// source. Verify must succeed after the adapter has consumed the complete
// stream; Close is called after invocation regardless of outcome.
type StreamTransfer struct {
	Reference capturestream.Reference
	Source    io.ReadCloser
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

func (r *Registry) Register(implementation Implementation) error {
	if r == nil || implementation == nil {
		return errors.New("registry and adapter implementation are required")
	}
	descriptor := cloneDescriptor(implementation.Descriptor())
	descriptorSchema := "urn:gameserver-controller:" + r.version + ":common:capability-descriptor"
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		return fmt.Errorf("encode adapter descriptor: %w", err)
	}
	if err := r.schemas.ValidateJSON(descriptorSchema, encoded); err != nil {
		return fmt.Errorf("invalid adapter descriptor: %w", err)
	}
	if !contains(descriptor.ProtocolVersions, r.version) {
		return fmt.Errorf("adapter %q does not support protocol %q", descriptor.AdapterID, r.version)
	}
	prefix, ok := capabilityPrefix(descriptor.Category)
	if !ok {
		return fmt.Errorf("adapter %q has unsupported category %q", descriptor.AdapterID, descriptor.Category)
	}
	capabilities := make(map[string]Capability, len(descriptor.Capabilities))
	for _, capability := range descriptor.Capabilities {
		if !strings.HasPrefix(capability.Name, prefix) {
			return fmt.Errorf("capability %q does not belong to category %q", capability.Name, descriptor.Category)
		}
		if _, duplicate := capabilities[capability.Name]; duplicate {
			return fmt.Errorf("adapter %q advertises duplicate capability %q", descriptor.AdapterID, capability.Name)
		}
		if !r.schemas.HasSchema(capability.InputSchemaURI) || !r.schemas.HasSchema(capability.OutputSchemaURI) {
			return fmt.Errorf("capability %q references an unregistered input or output schema", capability.Name)
		}
		capabilities[capability.Name] = capability
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.adapters[descriptor.AdapterID]; exists {
		return fmt.Errorf("adapter %q is already registered", descriptor.AdapterID)
	}
	r.adapters[descriptor.AdapterID] = registeredAdapter{implementation: implementation, descriptor: descriptor, capabilities: capabilities}
	return nil
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
	if r == nil {
		return InvokeResponse{}, ErrAdapterNotFound
	}
	r.mu.RLock()
	registered, ok := r.adapters[adapterID]
	r.mu.RUnlock()
	if !ok {
		return InvokeResponse{}, ErrAdapterNotFound
	}
	capability, ok := registered.capabilities[request.Params.Capability]
	if !ok {
		return InvokeResponse{}, ErrUnsupportedCapability
	}
	if ctx == nil {
		return InvokeResponse{}, errors.New("invocation context is required")
	}
	transfers := request.Params.RequestContext.StreamTransfers
	if capability.Name == "game.backup.capture" && len(transfers) != 1 {
		return InvokeResponse{}, errors.New("backup capture requires one stream transfer")
	}
	if len(transfers) > 0 {
		direction, _ := capability.Constraints["stream_transfer_direction"].(string)
		if direction == "" || len(registered.descriptor.StreamTransportProfiles) == 0 {
			return InvokeResponse{}, errors.New("capability does not declare stream transport")
		}
		seenTransferIDs := make(map[string]struct{}, len(transfers))
		for _, transfer := range transfers {
			if _, duplicate := seenTransferIDs[transfer.TransferID]; duplicate {
				return InvokeResponse{}, errors.New("transient stream transfer IDs must be unique within an invocation")
			}
			seenTransferIDs[transfer.TransferID] = struct{}{}
			if transfer.Direction != direction || !transfer.ExpiresAt.After(time.Now()) || transfer.ExpiresAt.After(request.Params.RequestContext.Deadline) || (capability.Name == "game.backup.capture" && time.Until(transfer.ExpiresAt) > 5*time.Minute) {
				return InvokeResponse{}, errors.New("invalid transient stream transfer")
			}
		}
	}
	if request.Params.ProtocolVersion != r.version {
		return InvokeResponse{}, fmt.Errorf("request protocol version %q does not match selected version %q", request.Params.ProtocolVersion, r.version)
	}
	if request.Params.RequestContext.Deadline.IsZero() {
		return InvokeResponse{}, errors.New("request context deadline is required")
	}
	if capability.Mutating && (request.Params.RequestContext.OperationID == "" || request.Params.RequestContext.StepID == "") {
		return InvokeResponse{}, ErrMutatingContext
	}
	if request.ID != request.Params.RequestContext.RequestID {
		return InvokeResponse{}, errors.New("JSON-RPC ID must match request_context.request_id")
	}
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		return InvokeResponse{}, fmt.Errorf("encode adapter request: %w", err)
	}
	requestSchema := "urn:gameserver-controller:" + r.version + ":rpc:adapter-invoke-request"
	if err := r.schemas.ValidateJSON(requestSchema, encodedRequest); err != nil {
		return InvokeResponse{}, fmt.Errorf("invalid adapter invocation request: %w", err)
	}
	if err := r.schemas.ValidateJSON(capability.InputSchemaURI, request.Params.Input); err != nil {
		return InvokeResponse{}, fmt.Errorf("invalid input for capability %q: %w", capability.Name, err)
	}
	if len(transfers) > 0 {
		var input any
		if json.Unmarshal(request.Params.Input, &input) != nil {
			return InvokeResponse{}, errors.New("invalid streamed input")
		}
		for _, transfer := range transfers {
			if containsStreamCredential(input, transfer.TransferID) {
				return InvokeResponse{}, errors.New("stream credential in durable input")
			}
		}
	}
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
	if response.JSONRPC != "2.0" || response.ID != request.ID {
		return InvokeResponse{}, &InvocationError{
			MayHaveSideEffects: capability.Mutating,
			Cause:              errors.New("adapter response JSON-RPC version or ID does not match the request"),
		}
	}
	if response.Result.ObservedAt.IsZero() {
		return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: errors.New("adapter response observed_at is required")}
	}
	if len(transfers) > 0 {
		encoded, encodeErr := json.Marshal(response)
		if encodeErr != nil {
			return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: errors.New("invalid streamed response")}
		}
		var decoded any
		if json.Unmarshal(encoded, &decoded) != nil {
			return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: errors.New("invalid streamed response")}
		}
		for _, transfer := range transfers {
			if containsStreamCredential(decoded, transfer.TransferID) {
				return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: errors.New("stream credential in adapter response")}
			}
		}
	}
	if err := r.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output); err != nil {
		return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: fmt.Errorf("invalid output for capability %q: %w", capability.Name, err)}
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: fmt.Errorf("encode adapter response: %w", err)}
	}
	responseSchema := "urn:gameserver-controller:" + r.version + ":rpc:adapter-invoke-result"
	if err := r.schemas.ValidateJSON(responseSchema, encodedResponse); err != nil {
		return InvokeResponse{}, &InvocationError{MayHaveSideEffects: capability.Mutating, Cause: fmt.Errorf("invalid adapter response: %w", err)}
	}
	return response, nil
}

func containsStreamCredential(value any, credential string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, credential)
	case []any:
		for _, child := range typed {
			if containsStreamCredential(child, credential) {
				return true
			}
		}
	case map[string]any:
		for key, child := range typed {
			if strings.Contains(key, credential) || containsStreamCredential(child, credential) {
				return true
			}
		}
	}
	return false
}

func capabilityPrefix(category Category) (string, bool) {
	switch category {
	case CategoryHosting:
		return "hosting.", true
	case CategoryStorage:
		return "storage.", true
	case CategoryExecution:
		return "execution.", true
	case CategoryGame:
		return "game.", true
	case CategoryIntegration:
		return "integrations.", true
	default:
		return "", false
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func cloneDescriptor(value Descriptor) Descriptor {
	value.ProtocolVersions = cloneSlice(value.ProtocolVersions)
	value.ContractVersions = cloneSlice(value.ContractVersions)
	value.RequiredPermissions = cloneSlice(value.RequiredPermissions)
	value.StreamTransportProfiles = cloneSlice(value.StreamTransportProfiles)
	value.Capabilities = cloneSlice(value.Capabilities)
	for i := range value.Capabilities {
		value.Capabilities[i].RequiredPermissions = cloneSlice(value.Capabilities[i].RequiredPermissions)
		value.Capabilities[i].Constraints = cloneMap(value.Capabilities[i].Constraints)
	}
	value.Constraints = cloneMap(value.Constraints)
	return value
}

func cloneSlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	result := make([]T, len(values))
	copy(result, values)
	return result
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	result := make(map[string]any, len(value))
	for key, entry := range value {
		result[key] = entry
	}
	return result
}
