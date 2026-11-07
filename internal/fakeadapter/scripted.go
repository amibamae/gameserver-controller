// Package fakeadapter provides scripted in-process adapters for controller
// tests. It performs no network, process, or provider calls.
package fakeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

type Reply struct {
	Outcome              adapter.Outcome
	Output               json.RawMessage
	ExternalOperationRef json.RawMessage
	ObservedAt           time.Time
	ResponseID           string
	Delay                time.Duration
	Err                  error
}

type Scripted struct {
	mu         sync.Mutex
	descriptor adapter.Descriptor
	scripts    map[string][]Reply
	nextReply  map[string]int
	calls      []adapter.InvokeRequest
}

func New(descriptor adapter.Descriptor) *Scripted {
	return &Scripted{
		descriptor: cloneDescriptor(descriptor),
		scripts:    make(map[string][]Reply),
		nextReply:  make(map[string]int),
	}
}

func (f *Scripted) Descriptor() adapter.Descriptor {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneDescriptor(f.descriptor)
}

func (f *Scripted) SetScript(capability string, replies ...Reply) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	advertised := false
	for _, value := range f.descriptor.Capabilities {
		if value.Name == capability {
			advertised = true
			break
		}
	}
	if !advertised {
		return fmt.Errorf("fake adapter does not advertise capability %q", capability)
	}
	if len(replies) == 0 {
		return errors.New("script must contain at least one reply")
	}
	f.scripts[capability] = cloneReplies(replies)
	f.nextReply[capability] = 0
	return nil
}

func (f *Scripted) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if err := ctx.Err(); err != nil {
		return adapter.InvokeResponse{}, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, cloneRequest(request))
	index := f.nextReply[request.Params.Capability]
	script := f.scripts[request.Params.Capability]
	if index >= len(script) {
		f.mu.Unlock()
		return adapter.InvokeResponse{}, fmt.Errorf("fake adapter script exhausted for %q", request.Params.Capability)
	}
	reply := cloneReply(script[index])
	f.nextReply[request.Params.Capability] = index + 1
	f.mu.Unlock()
	if reply.Delay > 0 {
		timer := time.NewTimer(reply.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return adapter.InvokeResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
	if reply.Err != nil {
		return adapter.InvokeResponse{}, reply.Err
	}
	responseID := request.ID
	if reply.ResponseID != "" {
		responseID = reply.ResponseID
	}
	return adapter.InvokeResponse{
		JSONRPC: "2.0",
		ID:      responseID,
		Result: adapter.InvokeResult{
			Outcome:              reply.Outcome,
			Output:               append(json.RawMessage(nil), reply.Output...),
			ExternalOperationRef: append(json.RawMessage(nil), reply.ExternalOperationRef...),
			ObservedAt:           reply.ObservedAt,
		},
	}, nil
}

func (f *Scripted) Calls() []adapter.InvokeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]adapter.InvokeRequest, len(f.calls))
	for i := range f.calls {
		calls[i] = cloneRequest(f.calls[i])
	}
	return calls
}

func cloneDescriptor(value adapter.Descriptor) adapter.Descriptor {
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
	copyOfValue := make(map[string]any, len(value))
	for key, entry := range value {
		copyOfValue[key] = entry
	}
	return copyOfValue
}

func cloneRequest(value adapter.InvokeRequest) adapter.InvokeRequest {
	value.Params.Input = append(json.RawMessage(nil), value.Params.Input...)
	return value
}

func cloneReplies(values []Reply) []Reply {
	cloned := make([]Reply, len(values))
	for i := range values {
		cloned[i] = cloneReply(values[i])
	}
	return cloned
}

func cloneReply(value Reply) Reply {
	value.Output = append(json.RawMessage(nil), value.Output...)
	value.ExternalOperationRef = append(json.RawMessage(nil), value.ExternalOperationRef...)
	return value
}
