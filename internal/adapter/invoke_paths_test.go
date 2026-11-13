package adapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

// These tests pin the request and response checks in Registry.Invoke. Each
// starts from an invocation that succeeds and changes one thing.

type scriptedAdapter struct {
	descriptor adapter.Descriptor
	called     bool
	respond    func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
}

func (a *scriptedAdapter) Descriptor() adapter.Descriptor { return a.descriptor }
func (a *scriptedAdapter) Invoke(_ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	a.called = true
	return a.respond(r)
}

type invokeHarness struct {
	registry *adapter.Registry
	adapter  *scriptedAdapter
	request  adapter.InvokeRequest
	output   json.RawMessage
}

// newInspectHarness registers the example hosting adapter and returns a valid
// hosting.instance.inspect invocation whose adapter answers with the example
// output.
func newInspectHarness(t *testing.T) *invokeHarness {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	h := &invokeHarness{registry: registry, adapter: &scriptedAdapter{}}
	readExample(t, "adapter-descriptor.hosting.json", &h.adapter.descriptor)
	readExample(t, "adapter-invoke-request.json", &h.request)
	h.request.Params.RequestContext.Deadline = time.Now().Add(time.Minute)
	var output any
	readExample(t, "hosting-instance-inspect-output.json", &output)
	h.output, _ = json.Marshal(output)
	h.adapter.respond = func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		return adapter.InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: h.output, ObservedAt: time.Now()}}, nil
	}
	if err := registry.Register(h.adapter); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *invokeHarness) invoke(ctx context.Context) (adapter.InvokeResponse, error) {
	return h.registry.Invoke(ctx, h.adapter.descriptor.AdapterID, h.request)
}

func TestInvokeInspectBaseline(t *testing.T) {
	h := newInspectHarness(t)
	if _, err := h.invoke(context.Background()); err != nil || !h.adapter.called {
		t.Fatalf("baseline invoke: err=%v called=%v", err, h.adapter.called)
	}
}

func TestInvokeRejectsRequestsBeforeCallingTheAdapter(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name   string
		ctx    context.Context
		mutate func(*invokeHarness)
		want   string
	}{
		{"unknown capability", context.Background(), func(h *invokeHarness) { h.request.Params.Capability = "hosting.unknown" }, adapter.ErrUnsupportedCapability.Error()},
		{"no context", nil, func(*invokeHarness) {}, "invocation context is required"},
		{"stream transfer for a capability without stream transport", context.Background(), func(h *invokeHarness) {
			h.request.Params.RequestContext.StreamTransfers = []capturestream.Reference{{TransferID: "t", Direction: "adapter_to_controller", ExpiresAt: time.Now().Add(time.Minute)}}
		}, "streamed adapter invocation failed"},
		{"another protocol version", context.Background(), func(h *invokeHarness) { h.request.Params.ProtocolVersion = "0.1.0" }, `request protocol version "0.1.0" does not match`},
		{"no deadline", context.Background(), func(h *invokeHarness) { h.request.Params.RequestContext.Deadline = time.Time{} }, "request context deadline is required"},
		{"mutating call without an operation step", context.Background(), func(h *invokeHarness) {
			h.request.Params.Capability = "hosting.instance.create"
			h.request.Params.RequestContext.StepID = ""
		}, adapter.ErrMutatingContext.Error()},
		{"JSON-RPC ID differs from the request ID", context.Background(), func(h *invokeHarness) { h.request.ID = "other" }, "JSON-RPC ID must match request_context.request_id"},
		{"input fails the capability schema", context.Background(), func(h *invokeHarness) { h.request.Params.Input = json.RawMessage(`{}`) }, `invalid input for capability "hosting.instance.inspect"`},
		{"canceled context", canceled, func(*invokeHarness) {}, context.Canceled.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newInspectHarness(t)
			test.mutate(h)
			_, err := h.invoke(test.ctx)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Invoke error = %v, want one containing %q", err, test.want)
			}
			if h.adapter.called {
				t.Fatal("rejected request reached the adapter")
			}
		})
	}
	h := newInspectHarness(t)
	if _, err := h.registry.Invoke(context.Background(), "hosting.missing", h.request); !errors.Is(err, adapter.ErrAdapterNotFound) {
		t.Fatalf("unknown adapter: Invoke error = %v, want ErrAdapterNotFound", err)
	}
}

func TestInvokeRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name   string
		change func(*adapter.InvokeResponse)
		want   string
	}{
		{"another JSON-RPC version", func(r *adapter.InvokeResponse) { r.JSONRPC = "1.0" }, "JSON-RPC version or ID does not match"},
		{"another response ID", func(r *adapter.InvokeResponse) { r.ID = "other" }, "JSON-RPC version or ID does not match"},
		{"no observation time", func(r *adapter.InvokeResponse) { r.Result.ObservedAt = time.Time{} }, "observed_at is required"},
		{"output fails the capability schema", func(r *adapter.InvokeResponse) { r.Result.Output = json.RawMessage(`{}`) }, `invalid output for capability "hosting.instance.inspect"`},
		{"result fails the response schema", func(r *adapter.InvokeResponse) { r.Result.Outcome = "finished" }, "invalid adapter response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newInspectHarness(t)
			respond := h.adapter.respond
			h.adapter.respond = func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
				response, err := respond(r)
				test.change(&response)
				return response, err
			}
			_, err := h.invoke(context.Background())
			var invocation *adapter.InvocationError
			if !errors.As(err, &invocation) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Invoke error = %v, want an InvocationError containing %q", err, test.want)
			}
			if invocation.MayHaveSideEffects {
				t.Fatal("a read-only capability reported possible side effects")
			}
		})
	}
}

func TestInvokeWrapsAdapterErrorsWithTheCapabilitySideEffects(t *testing.T) {
	h := newInspectHarness(t)
	failure := errors.New("provider timeout")
	h.adapter.respond = func(adapter.InvokeRequest) (adapter.InvokeResponse, error) { return adapter.InvokeResponse{}, failure }
	_, err := h.invoke(context.Background())
	var invocation *adapter.InvocationError
	if !errors.As(err, &invocation) || !errors.Is(err, failure) || invocation.MayHaveSideEffects {
		t.Fatalf("Invoke error = %v, want the adapter error wrapped without side effects", err)
	}
}

// newCaptureHarness returns a valid streamed game.backup.capture invocation
// for the example Paper adapter.
func newCaptureInvokeHarness(t *testing.T) *invokeHarness {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	h := &invokeHarness{registry: registry, adapter: &scriptedAdapter{}}
	readExample(t, "adapter-descriptor.paper.json", &h.adapter.descriptor)
	readExample(t, "game-backup-capture-invocation.json", &h.request)
	h.request.Params.RequestContext.Deadline = time.Now().Add(2 * time.Minute)
	broker, err := capturestream.New(4)
	if err != nil {
		t.Fatal(err)
	}
	c := h.request.Params.RequestContext
	scope := capturestream.Scope{OperationID: c.OperationID, StepID: c.StepID, AdapterID: h.adapter.descriptor.AdapterID, ServerID: "minecraft-demo",
		EnvironmentID: "staging", InstanceAdapterID: "hosting.vultr", InstanceID: "instance-demo-01", DataGenerationID: "generation-demo-004",
		DataSetID: "world", CaptureID: "operation-backup-004-capture-world"}
	reference, _, err := broker.IssueTracked(scope, 8, time.Now().Add(time.Minute), c.Deadline, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	h.request.Params.RequestContext.StreamTransfers = []capturestream.Reference{reference}
	var output any
	readExample(t, "game-backup-capture-output.json", &output)
	h.output, _ = json.Marshal(output)
	h.adapter.respond = func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		return adapter.InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: h.output, ObservedAt: time.Now()}}, nil
	}
	if err := registry.Register(h.adapter); err != nil {
		t.Fatal(err)
	}
	return h
}

// Streamed invocations redact every failure to one message, so these tests
// call the checks through a request the redaction does not hide: the
// streamed-input checks run only when transfers are present, and Invoke
// redacts their errors. The tests therefore pin the redacted result and that
// the adapter was or was not reached.
func TestInvokeCaptureBaseline(t *testing.T) {
	h := newCaptureInvokeHarness(t)
	if _, err := h.invoke(context.Background()); err != nil || !h.adapter.called {
		t.Fatalf("baseline streamed invoke: err=%v called=%v", err, h.adapter.called)
	}
}

func TestInvokeStreamTransferChecks(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*invokeHarness)
		wantCalled  bool
		sideEffects bool
	}{
		{"capture without a transfer", func(h *invokeHarness) { h.request.Params.RequestContext.StreamTransfers = nil }, false, false},
		{"repeated transfer ID", func(h *invokeHarness) {
			transfer := h.request.Params.RequestContext.StreamTransfers[0]
			h.request.Params.RequestContext.StreamTransfers = []capturestream.Reference{transfer, transfer}
		}, false, false},
		{"transfer in the wrong direction", func(h *invokeHarness) {
			h.request.Params.RequestContext.StreamTransfers[0].Direction = "controller_to_adapter"
		}, false, false},
		{"transfer expires after the deadline", func(h *invokeHarness) {
			h.request.Params.RequestContext.Deadline = time.Now().Add(30 * time.Second)
		}, false, false},
		{"capture transfer that lives too long", func(h *invokeHarness) {
			h.request.Params.RequestContext.Deadline = time.Now().Add(time.Hour)
			h.request.Params.RequestContext.StreamTransfers[0].ExpiresAt = time.Now().Add(10 * time.Minute)
		}, false, false},
		{"transfer ID in the durable input", func(h *invokeHarness) {
			var input map[string]any
			_ = json.Unmarshal(h.request.Params.Input, &input)
			h.request.Params.RequestContext.StreamTransfers[0].TransferID = input["capture_id"].(string)
		}, false, false},
		{"transfer ID in the adapter response", func(h *invokeHarness) {
			var output map[string]any
			_ = json.Unmarshal(h.output, &output)
			output["reason_code"] = h.request.Params.RequestContext.StreamTransfers[0].TransferID
			h.output, _ = json.Marshal(output)
		}, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newCaptureInvokeHarness(t)
			test.mutate(h)
			_, err := h.invoke(context.Background())
			if err == nil {
				t.Fatal("Invoke accepted an invalid streamed invocation")
			}
			if len(h.request.Params.RequestContext.StreamTransfers) > 0 && err.Error() != "streamed adapter invocation failed" {
				t.Fatalf("streamed Invoke error = %q, want it redacted", err)
			}
			if h.adapter.called != test.wantCalled {
				t.Fatalf("adapter called = %v, want %v", h.adapter.called, test.wantCalled)
			}
			var invocation *adapter.InvocationError
			if errors.As(err, &invocation) != test.sideEffects || (test.sideEffects && !invocation.MayHaveSideEffects) {
				t.Fatalf("Invoke error = %#v; want possible side effects = %v", err, test.sideEffects)
			}
		})
	}
}
