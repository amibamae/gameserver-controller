package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/capturestream"
)

// prepareInvocation checks a request before the adapter is called: a
// registered adapter and advertised capability, a context, valid stream
// transfers, the selected protocol version, a deadline, an operation step
// for mutating calls, matching JSON-RPC and request IDs, schema-valid request
// and input, and no stream credential in the durable input.
func (r *Registry) prepareInvocation(ctx context.Context, adapterID string, request InvokeRequest) (registeredAdapter, Capability, error) {
	registered, capability, err := r.lookupCapability(adapterID, request.Params.Capability)
	if err != nil {
		return registeredAdapter{}, Capability{}, err
	}
	if ctx == nil {
		return registeredAdapter{}, Capability{}, errors.New("invocation context is required")
	}
	if err := checkInvocationTransfers(capability, registered.descriptor.StreamTransportProfiles, request.Params.RequestContext); err != nil {
		return registeredAdapter{}, Capability{}, err
	}
	if err := r.checkRequestEnvelope(capability, request); err != nil {
		return registeredAdapter{}, Capability{}, err
	}
	if err := r.checkRequestSchemas(capability, request); err != nil {
		return registeredAdapter{}, Capability{}, err
	}
	if err := checkStreamedInput(request.Params); err != nil {
		return registeredAdapter{}, Capability{}, err
	}
	return registered, capability, nil
}

// lookupCapability returns the registered adapter and the named capability
// it advertises.
func (r *Registry) lookupCapability(adapterID, name string) (registeredAdapter, Capability, error) {
	if r == nil {
		return registeredAdapter{}, Capability{}, ErrAdapterNotFound
	}
	r.mu.RLock()
	registered, ok := r.adapters[adapterID]
	r.mu.RUnlock()
	if !ok {
		return registeredAdapter{}, Capability{}, ErrAdapterNotFound
	}
	capability, ok := registered.capabilities[name]
	if !ok {
		return registeredAdapter{}, Capability{}, ErrUnsupportedCapability
	}
	return registered, capability, nil
}

// checkInvocationTransfers requires exactly one transfer for a backup
// capture, then checks every transfer against the capability and the
// adapter's stream profiles.
func checkInvocationTransfers(capability Capability, profiles []string, c RequestContext) error {
	if capability.Name == "game.backup.capture" && len(c.StreamTransfers) != 1 {
		return errors.New("backup capture requires one stream transfer")
	}
	return checkStreamTransfers(capability, profiles, c, time.Now())
}

// checkRequestEnvelope requires the selected protocol version, a deadline,
// an operation step for mutating calls, and a JSON-RPC ID equal to the
// request ID.
func (r *Registry) checkRequestEnvelope(capability Capability, request InvokeRequest) error {
	c := request.Params.RequestContext
	if request.Params.ProtocolVersion != r.version {
		return fmt.Errorf("request protocol version %q does not match selected version %q", request.Params.ProtocolVersion, r.version)
	}
	if c.Deadline.IsZero() {
		return errors.New("request context deadline is required")
	}
	if missingOperationStep(capability, c) {
		return ErrMutatingContext
	}
	if request.ID != c.RequestID {
		return errors.New("JSON-RPC ID must match request_context.request_id")
	}
	return nil
}

// missingOperationStep reports whether a mutating capability is called
// without both a durable operation ID and step ID.
func missingOperationStep(capability Capability, c RequestContext) bool {
	return capability.Mutating && (c.OperationID == "" || c.StepID == "")
}

// checkRequestSchemas validates the encoded request against the invoke
// envelope schema, then the input against the capability's input schema.
func (r *Registry) checkRequestSchemas(capability Capability, request InvokeRequest) error {
	encodedRequest, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode adapter request: %w", err)
	}
	requestSchema := "urn:gameserver-controller:" + r.version + ":rpc:adapter-invoke-request"
	if err := r.schemas.ValidateJSON(requestSchema, encodedRequest); err != nil {
		return fmt.Errorf("invalid adapter invocation request: %w", err)
	}
	if err := r.schemas.ValidateJSON(capability.InputSchemaURI, request.Params.Input); err != nil {
		return fmt.Errorf("invalid input for capability %q: %w", capability.Name, err)
	}
	return nil
}

// checkStreamedInput rejects a streamed invocation whose durable input
// contains one of its transfer IDs.
func checkStreamedInput(params InvokeParams) error {
	transfers := params.RequestContext.StreamTransfers
	if len(transfers) == 0 {
		return nil
	}
	return checkNoStreamCredential(params.Input, transfers, "invalid streamed input", "stream credential in durable input")
}

// checkNoStreamCredential decodes encoded and returns an error with message
// invalid when it is not JSON, or with message found when it contains one of
// the transfer IDs.
func checkNoStreamCredential(encoded []byte, transfers []capturestream.Reference, invalid, found string) error {
	var decoded any
	if json.Unmarshal(encoded, &decoded) != nil {
		return errors.New(invalid)
	}
	if containsAnyStreamCredential(decoded, transfers) {
		return errors.New(found)
	}
	return nil
}

// streamTransferRules are the expiry and direction limits that every
// transfer of one invocation must meet.
type streamTransferRules struct {
	capability string
	direction  string
	now        time.Time
	deadline   time.Time
}

// checkStreamTransfers requires any stream transfers to use a capability that
// declares stream transport, to have unique IDs, to flow in the declared
// direction, and to expire after now but no later than the deadline (and,
// for backup capture, within five minutes).
func checkStreamTransfers(capability Capability, profiles []string, c RequestContext, now time.Time) error {
	if len(c.StreamTransfers) == 0 {
		return nil
	}
	// A missing or non-string direction reads as "" and is rejected below.
	direction, _ := capability.Constraints["stream_transfer_direction"].(string)
	if direction == "" || len(profiles) == 0 {
		return errors.New("capability does not declare stream transport")
	}
	rules := streamTransferRules{capability: capability.Name, direction: direction, now: now, deadline: c.Deadline}
	seenTransferIDs := make(map[string]struct{}, len(c.StreamTransfers))
	for _, transfer := range c.StreamTransfers {
		if _, duplicate := seenTransferIDs[transfer.TransferID]; duplicate {
			return errors.New("transient stream transfer IDs must be unique within an invocation")
		}
		seenTransferIDs[transfer.TransferID] = struct{}{}
		if !rules.allow(transfer) {
			return errors.New("invalid transient stream transfer")
		}
	}
	return nil
}

// allow reports whether transfer flows in the declared direction and expires
// within the allowed window.
func (rules streamTransferRules) allow(transfer capturestream.Reference) bool {
	return transfer.Direction == rules.direction && rules.expiresInWindow(transfer.ExpiresAt)
}

// expiresInWindow reports whether expiresAt is after now, no later than the
// deadline, and, for backup capture, no more than five minutes away.
func (rules streamTransferRules) expiresInWindow(expiresAt time.Time) bool {
	return expiresAt.After(rules.now) && !expiresAt.After(rules.deadline) && !rules.captureOutlivesLimit(expiresAt)
}

// captureOutlivesLimit reports whether a backup capture transfer expires more
// than five minutes after now.
func (rules streamTransferRules) captureOutlivesLimit(expiresAt time.Time) bool {
	return rules.capability == "game.backup.capture" && expiresAt.Sub(rules.now) > 5*time.Minute
}

// checkResponse validates an adapter response: JSON-RPC version and ID, an
// observation time, no stream credential when streams were used, and
// schema-valid output and result. The caller wraps any error with the
// capability's side-effect risk.
func (r *Registry) checkResponse(capability Capability, requestID string, transfers []capturestream.Reference, response InvokeResponse) error {
	if err := checkResponseEnvelope(requestID, response); err != nil {
		return err
	}
	if err := checkStreamedResponse(response, transfers); err != nil {
		return err
	}
	return r.checkResponseSchemas(capability, response)
}

// checkResponseEnvelope requires JSON-RPC 2.0, the request's ID, and an
// observation time.
func checkResponseEnvelope(requestID string, response InvokeResponse) error {
	if response.JSONRPC != "2.0" || response.ID != requestID {
		return errors.New("adapter response JSON-RPC version or ID does not match the request")
	}
	if response.Result.ObservedAt.IsZero() {
		return errors.New("adapter response observed_at is required")
	}
	return nil
}

// checkStreamedResponse rejects a response to a streamed invocation that
// cannot be encoded or that contains one of its transfer IDs.
func checkStreamedResponse(response InvokeResponse, transfers []capturestream.Reference) error {
	if len(transfers) == 0 {
		return nil
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return errors.New("invalid streamed response")
	}
	return checkNoStreamCredential(encoded, transfers, "invalid streamed response", "stream credential in adapter response")
}

// checkResponseSchemas validates the output against the capability's output
// schema, then the encoded response against the invoke result schema.
func (r *Registry) checkResponseSchemas(capability Capability, response InvokeResponse) error {
	if err := r.schemas.ValidateJSON(capability.OutputSchemaURI, response.Result.Output); err != nil {
		return fmt.Errorf("invalid output for capability %q: %w", capability.Name, err)
	}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode adapter response: %w", err)
	}
	responseSchema := "urn:gameserver-controller:" + r.version + ":rpc:adapter-invoke-result"
	if err := r.schemas.ValidateJSON(responseSchema, encodedResponse); err != nil {
		return fmt.Errorf("invalid adapter response: %w", err)
	}
	return nil
}

func containsAnyStreamCredential(value any, transfers []capturestream.Reference) bool {
	for _, transfer := range transfers {
		if containsStreamCredential(value, transfer.TransferID) {
			return true
		}
	}
	return false
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
