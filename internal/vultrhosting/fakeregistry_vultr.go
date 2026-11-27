// Package vultrhosting provides the Vultr hosting adapter implementation.
package vultrhosting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

// FakeVultr is a scripted in-process adapter for controller tests. It performs no network calls.
type FakeVultr struct {
	mu         sync.Mutex
	descriptor adapter.Descriptor
	scripts    map[string][]FakeReply
	nextReply  map[string]int
}

type FakeReply struct {
	Outcome    adapter.Outcome
	Output     json.RawMessage
	Delay      time.Duration
	Err        error
	ResponseID string
}

// NewFakeVultr creates a fake Vultr adapter for testing.
func NewFakeVultr(descriptor adapter.Descriptor) *FakeVultr {
	return &FakeVultr{
		descriptor: descriptor,
		scripts:    make(map[string][]FakeReply),
		nextReply:  make(map[string]int),
	}
}

// Descriptor implements adapter.Implementation.
func (f *FakeVultr) Descriptor() adapter.Descriptor {
	return f.descriptor
}

// SetScript programs a capability with scripted replies.
func (f *FakeVultr) SetScript(capability string, replies ...FakeReply) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	advertised := false
	for _, cap := range f.descriptor.Capabilities {
		if cap.Name == capability {
			advertised = true
			break
		}
	}

	if !advertised {
		return fmt.Errorf("fake Vultr adapter does not advertise capability %q", capability)
	}

	if len(replies) == 0 {
		return errors.New("script must contain at least one reply")
	}

	f.scripts[capability] = cloneReplies(replies)
	f.nextReply[capability] = 0
	return nil
}

// Invoke handles a single request to the fake adapter.
func (f *FakeVultr) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if err := ctx.Err(); err != nil {
		return adapter.InvokeResponse{}, err
	}

	f.mu.Lock()
	index := f.nextReply[request.Params.Capability]
	script := f.scripts[request.Params.Capability]
	f.mu.Unlock()

	if index >= len(script) {
		return adapter.InvokeResponse{}, fmt.Errorf("fake Vultr script exhausted for %q", request.Params.Capability)
	}

	reply := cloneReply(script[index])
	f.mu.Lock()
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
			Outcome:    reply.Outcome,
			Output:     append(json.RawMessage(nil), reply.Output...),
			ObservedAt: time.Now(),
		},
	}, nil
}

// CloneReplies creates a deep copy of replies.
func cloneReplies(values []FakeReply) []FakeReply {
	cloned := make([]FakeReply, len(values))
	for i := range values {
		cloned[i] = cloneReply(values[i])
	}
	return cloned
}

// CloneReply creates a deep copy of a reply.
func cloneReply(value FakeReply) FakeReply {
	result := value
	if len(value.Output) > 0 {
		result.Output = append(json.RawMessage(nil), value.Output...)
	}
	return result
}
