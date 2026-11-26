package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

// These characterization tests pin the exact errors, error identities and
// check order of Register, prepareInvocation, checkResponse,
// checkStreamTransfers and the request-context stream handles. Each case
// starts from a call that succeeds and changes one or two things.

type adapterR5Adapter struct {
	descriptor Descriptor
	called     bool
	respond    func(InvokeRequest) (InvokeResponse, error)
}

func (a *adapterR5Adapter) Descriptor() Descriptor { return a.descriptor }
func (a *adapterR5Adapter) Invoke(_ context.Context, r InvokeRequest) (InvokeResponse, error) {
	a.called = true
	return a.respond(r)
}

// adapterR5Fixture holds a registry with the example hosting and Paper
// adapters and one valid request for each.
type adapterR5Fixture struct {
	schemas *schemaregistry.Registry
	reg     *Registry
	hosting *adapterR5Adapter
	paper   *adapterR5Adapter
	inspect InvokeRequest
	capture InvokeRequest
	output  json.RawMessage
}

func adapterR5NewFixture(t *testing.T) *adapterR5Fixture {
	t.Helper()
	f := &adapterR5Fixture{schemas: adapterR5Schemas(t)}
	f.reg = adapterR5Registry(t, f.schemas)
	f.hosting = adapterR5Example(t, "adapter-descriptor.hosting.json")
	f.paper = adapterR5Example(t, "adapter-descriptor.paper.json")
	readStreamExample(t, "adapter-invoke-request.json", &f.inspect)
	f.inspect.Params.RequestContext.Deadline = time.Now().Add(time.Minute)
	readStreamExample(t, "game-backup-capture-invocation.json", &f.capture)
	f.capture.Params.RequestContext.Deadline = time.Now().Add(2 * time.Minute)
	f.capture.Params.RequestContext.StreamTransfers = []capturestream.Reference{adapterR5Transfer(adapterR5TransferID, "adapter_to_controller", time.Minute)}
	var output any
	readStreamExample(t, "hosting-instance-inspect-output.json", &output)
	f.output = adapterR5Marshal(t, output)
	f.hosting.respond = f.reply
	for _, implementation := range []*adapterR5Adapter{f.hosting, f.paper} {
		if err := f.reg.Register(implementation); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *adapterR5Fixture) reply(r InvokeRequest) (InvokeResponse, error) {
	return InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: InvokeResult{Outcome: OutcomeCompleted, Output: f.output, ObservedAt: time.Now()}}, nil
}

func adapterR5Schemas(t *testing.T) *schemaregistry.Registry {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	return schemas
}

func adapterR5Registry(t *testing.T, schemas *schemaregistry.Registry) *Registry {
	t.Helper()
	registry, err := NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func adapterR5Example(t *testing.T, name string) *adapterR5Adapter {
	t.Helper()
	implementation := &adapterR5Adapter{}
	readStreamExample(t, name, &implementation.descriptor)
	return implementation
}

func adapterR5Marshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// adapterR5TransferID is a protocol-valid transient transfer handle.
const adapterR5TransferID = "abababababababababababababababababababababababababababababababab"

func adapterR5Transfer(id, direction string, expiresIn time.Duration) capturestream.Reference {
	return capturestream.Reference{TransferID: id, Direction: direction, MaximumBytes: 8, ExpiresAt: time.Now().Add(expiresIn)}
}

// adapterR5Error requires err to have exactly the message want.
func adapterR5Error(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

// adapterR5Wrapped requires err to read prefix + ": " + cause and to wrap an
// error with exactly the cause's message.
func adapterR5Wrapped(t *testing.T, err error, prefix string, cause error) {
	t.Helper()
	if cause == nil {
		t.Fatal("expected cause is nil")
	}
	adapterR5Error(t, err, prefix+": "+cause.Error())
	if unwrapped := errors.Unwrap(err); unwrapped == nil || unwrapped.Error() != cause.Error() {
		t.Fatalf("error %v does not wrap %v", err, cause)
	}
}

func (f *adapterR5Fixture) requestSchema() string {
	return "urn:gameserver-controller:" + f.reg.version + ":rpc:adapter-invoke-request"
}

func (f *adapterR5Fixture) capability(adapterID, name string) Capability {
	return f.reg.adapters[adapterID].capabilities[name]
}

type adapterR5PrepareCase struct {
	name    string
	ctx     context.Context
	capture bool
	mutate  func(*adapterR5Fixture, *InvokeRequest)
	want    func(*adapterR5Fixture, InvokeRequest) string
	is      error
}

// adapterR5Fixed returns an expectation that is always message.
func adapterR5Fixed[T any](message string) func(*adapterR5Fixture, T) string {
	return func(*adapterR5Fixture, T) string { return message }
}

func adapterR5Message(message string) func(*adapterR5Fixture, InvokeRequest) string {
	return adapterR5Fixed[InvokeRequest](message)
}

func adapterR5PrepareCases() []adapterR5PrepareCase {
	background := context.Background()
	return []adapterR5PrepareCase{
		{"unknown capability", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.Params.Capability = "hosting.unknown" },
			adapterR5Message("adapter does not advertise capability"), ErrUnsupportedCapability},
		{"no context", nil, false, func(*adapterR5Fixture, *InvokeRequest) {}, adapterR5Message("invocation context is required"), nil},
		{"capture without a transfer", background, true, func(_ *adapterR5Fixture, r *InvokeRequest) { r.Params.RequestContext.StreamTransfers = nil },
			adapterR5Message("backup capture requires one stream transfer"), nil},
		{"capture with two transfers", background, true, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.RequestContext.StreamTransfers = append(r.Params.RequestContext.StreamTransfers, adapterR5Transfer("cd"+adapterR5TransferID[2:], "adapter_to_controller", time.Minute))
		}, adapterR5Message("backup capture requires one stream transfer"), nil},
		{"transfer for a capability without stream transport", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.RequestContext.StreamTransfers = []capturestream.Reference{adapterR5Transfer("t", "adapter_to_controller", time.Minute)}
		}, adapterR5Message("capability does not declare stream transport"), nil},
		{"capture transfer in the wrong direction", background, true, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.RequestContext.StreamTransfers[0].Direction = "controller_to_adapter"
		}, adapterR5Message("invalid transient stream transfer"), nil},
		{"another protocol version", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.Params.ProtocolVersion = "0.1.0" },
			func(f *adapterR5Fixture, _ InvokeRequest) string {
				return fmt.Sprintf("request protocol version %q does not match selected version %q", "0.1.0", f.reg.version)
			}, nil},
		{"no deadline", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.Params.RequestContext.Deadline = time.Time{} },
			adapterR5Message("request context deadline is required"), nil},
		{"mutating call without a step", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.Capability = "hosting.instance.create"
			r.Params.RequestContext.OperationID = "operation-1"
		}, adapterR5Message("mutating call requires durable operation and step IDs"), ErrMutatingContext},
		{"mutating call without an operation", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.Capability = "hosting.instance.create"
			r.Params.RequestContext.StepID = "step-1"
		}, adapterR5Message("mutating call requires durable operation and step IDs"), ErrMutatingContext},
		{"mutating call with an operation step reaches the input schema", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.Capability = "hosting.instance.create"
			r.Params.RequestContext.OperationID = "operation-1"
			r.Params.RequestContext.StepID = "step-1"
		}, func(f *adapterR5Fixture, r InvokeRequest) string {
			uri := f.capability(f.hosting.descriptor.AdapterID, "hosting.instance.create").InputSchemaURI
			return fmt.Sprintf("invalid input for capability %q: %v", "hosting.instance.create", f.schemas.ValidateJSON(uri, r.Params.Input))
		}, nil},
		{"JSON-RPC ID differs from the request ID", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.ID = "other" },
			adapterR5Message("JSON-RPC ID must match request_context.request_id"), nil},
		{"input is not JSON", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.Params.Input = json.RawMessage(`{`) },
			func(_ *adapterR5Fixture, r InvokeRequest) string {
				_, err := json.Marshal(r)
				return "encode adapter request: " + err.Error()
			}, nil},
		{"request fails the invoke envelope schema", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.JSONRPC = "1.0" },
			func(f *adapterR5Fixture, r InvokeRequest) string {
				encoded, _ := json.Marshal(r)
				return "invalid adapter invocation request: " + f.schemas.ValidateJSON(f.requestSchema(), encoded).Error()
			}, nil},
		{"input fails the capability schema", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.Params.Input = json.RawMessage(`{}`) },
			func(f *adapterR5Fixture, r InvokeRequest) string {
				uri := f.capability(f.hosting.descriptor.AdapterID, "hosting.instance.inspect").InputSchemaURI
				return fmt.Sprintf("invalid input for capability %q: %v", "hosting.instance.inspect", f.schemas.ValidateJSON(uri, r.Params.Input))
			}, nil},
		{"transfer ID in the durable input", background, true, func(_ *adapterR5Fixture, r *InvokeRequest) {
			var input map[string]any
			_ = json.Unmarshal(r.Params.Input, &input)
			input["capture_id"] = adapterR5TransferID
			r.Params.Input, _ = json.Marshal(input)
		}, adapterR5Message("stream credential in durable input"), nil},
	}
}

// adapterR5PrepareOrderCases break two rules at once and pin which is
// reported first.
func adapterR5PrepareOrderCases() []adapterR5PrepareCase {
	background := context.Background()
	return []adapterR5PrepareCase{
		{"capability before context", nil, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.Params.Capability = "hosting.unknown" },
			adapterR5Message("adapter does not advertise capability"), ErrUnsupportedCapability},
		{"context before protocol version", nil, false, func(_ *adapterR5Fixture, r *InvokeRequest) { r.Params.ProtocolVersion = "0.1.0" },
			adapterR5Message("invocation context is required"), nil},
		{"capture transfer count before protocol version", background, true, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.RequestContext.StreamTransfers = nil
			r.Params.ProtocolVersion = "0.1.0"
		}, adapterR5Message("backup capture requires one stream transfer"), nil},
		{"stream transfers before protocol version", background, true, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.RequestContext.StreamTransfers[0].Direction = "controller_to_adapter"
			r.Params.ProtocolVersion = "0.1.0"
		}, adapterR5Message("invalid transient stream transfer"), nil},
		{"protocol version before deadline", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.ProtocolVersion = "0.1.0"
			r.Params.RequestContext.Deadline = time.Time{}
		}, func(f *adapterR5Fixture, _ InvokeRequest) string {
			return fmt.Sprintf("request protocol version %q does not match selected version %q", "0.1.0", f.reg.version)
		}, nil},
		{"deadline before mutating context", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.Capability = "hosting.instance.create"
			r.Params.RequestContext.Deadline = time.Time{}
		}, adapterR5Message("request context deadline is required"), nil},
		{"mutating context before request ID", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.Params.Capability = "hosting.instance.create"
			r.ID = "other"
		}, adapterR5Message("mutating call requires durable operation and step IDs"), ErrMutatingContext},
		{"request ID before envelope schema", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.ID = "other"
			r.JSONRPC = "1.0"
		}, adapterR5Message("JSON-RPC ID must match request_context.request_id"), nil},
		{"envelope schema before input schema", background, false, func(_ *adapterR5Fixture, r *InvokeRequest) {
			r.JSONRPC = "1.0"
			r.Params.Input = json.RawMessage(`{}`)
		}, func(f *adapterR5Fixture, r InvokeRequest) string {
			encoded, _ := json.Marshal(r)
			return "invalid adapter invocation request: " + f.schemas.ValidateJSON(f.requestSchema(), encoded).Error()
		}, nil},
	}
}

func adapterR5RunPrepare(t *testing.T, tests []adapterR5PrepareCase) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := adapterR5NewFixture(t)
			request, adapterID := f.inspect, f.hosting.descriptor.AdapterID
			if test.capture {
				request, adapterID = f.capture, f.paper.descriptor.AdapterID
			}
			test.mutate(f, &request)
			_, _, err := f.reg.prepareInvocation(test.ctx, adapterID, request)
			adapterR5Error(t, err, test.want(f, request))
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("error %v is not %v", err, test.is)
			}
		})
	}
}

func TestAdapterR5PrepareInvocationRejections(t *testing.T) {
	adapterR5RunPrepare(t, adapterR5PrepareCases())
}

func TestAdapterR5PrepareInvocationCheckOrder(t *testing.T) {
	adapterR5RunPrepare(t, adapterR5PrepareOrderCases())
}

func TestAdapterR5PrepareInvocationUnknownAdapter(t *testing.T) {
	f := adapterR5NewFixture(t)
	var missing *Registry
	for name, registry := range map[string]*Registry{"nil registry": missing, "unregistered adapter": f.reg} {
		t.Run(name, func(t *testing.T) {
			_, _, err := registry.prepareInvocation(nil, "hosting.missing", f.inspect)
			if !errors.Is(err, ErrAdapterNotFound) || err.Error() != "adapter is not registered" {
				t.Fatalf("error = %v, want ErrAdapterNotFound", err)
			}
		})
	}
}

func TestAdapterR5PrepareInvocationAccepts(t *testing.T) {
	f := adapterR5NewFixture(t)
	tests := []struct {
		name      string
		adapterID string
		request   InvokeRequest
	}{
		{"read-only call", f.hosting.descriptor.AdapterID, f.inspect},
		{"streamed capture", f.paper.descriptor.AdapterID, f.capture},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registered, capability, err := f.reg.prepareInvocation(context.Background(), test.adapterID, test.request)
			if err != nil {
				t.Fatal(err)
			}
			if registered.descriptor.AdapterID != test.adapterID || capability.Name != test.request.Params.Capability {
				t.Fatalf("prepared %q/%q, want %q/%q", registered.descriptor.AdapterID, capability.Name, test.adapterID, test.request.Params.Capability)
			}
		})
	}
}

func TestAdapterR5InvokeContextAndCopies(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		deadline time.Duration
		want     error
	}{
		{"canceled caller context", canceled, time.Minute, context.Canceled},
		{"deadline already passed", context.Background(), -time.Second, context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := adapterR5NewFixture(t)
			f.inspect.Params.RequestContext.Deadline = time.Now().Add(test.deadline)
			_, err := f.reg.Invoke(test.ctx, f.hosting.descriptor.AdapterID, f.inspect)
			if !errors.Is(err, test.want) || err.Error() != test.want.Error() || f.hosting.called {
				t.Fatalf("Invoke error = %v (called %v), want %v before the adapter", err, f.hosting.called, test.want)
			}
		})
	}
	t.Run("adapter changes do not reach the caller", func(t *testing.T) {
		f := adapterR5NewFixture(t)
		original := append(json.RawMessage(nil), f.inspect.Params.Input...)
		f.hosting.respond = func(r InvokeRequest) (InvokeResponse, error) {
			r.Params.Input[0] = ' '
			return f.reply(r)
		}
		if _, err := f.reg.Invoke(context.Background(), f.hosting.descriptor.AdapterID, f.inspect); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(f.inspect.Params.Input, original) {
			t.Fatal("adapter changed the caller's input")
		}
	})
}

func TestAdapterR5InvokeRedactsStreamedFailures(t *testing.T) {
	tests := []struct {
		name        string
		respond     func(InvokeRequest) (InvokeResponse, error)
		invocation  bool
		sideEffects bool
	}{
		{"rejected before the adapter", nil, false, false},
		{"adapter failure", func(InvokeRequest) (InvokeResponse, error) {
			return InvokeResponse{}, errors.New(adapterR5TransferID + " leaked")
		}, true, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := adapterR5NewFixture(t)
			f.paper.respond = test.respond
			if test.respond == nil {
				f.capture.ID = "other"
			}
			_, err := f.reg.Invoke(context.Background(), f.paper.descriptor.AdapterID, f.capture)
			adapterR5Error(t, err, "streamed adapter invocation failed")
			var invocation *InvocationError
			if errors.As(err, &invocation) != test.invocation || (test.invocation && invocation.MayHaveSideEffects != test.sideEffects) {
				t.Fatalf("Invoke error = %#v, want invocation=%v side effects=%v", err, test.invocation, test.sideEffects)
			}
		})
	}
}

type adapterR5ResponseCase struct {
	name     string
	streamed bool
	change   func(*InvokeResponse)
	want     func(*adapterR5Fixture, InvokeResponse) string
}

func adapterR5ResponseCases() []adapterR5ResponseCase {
	versionOrID := adapterR5Fixed[InvokeResponse]("adapter response JSON-RPC version or ID does not match the request")
	return []adapterR5ResponseCase{
		{"another JSON-RPC version", false, func(r *InvokeResponse) { r.JSONRPC = "1.0" }, versionOrID},
		{"another response ID", false, func(r *InvokeResponse) { r.ID = "other" }, versionOrID},
		{"ID checked before observation time", false, func(r *InvokeResponse) { r.ID, r.Result.ObservedAt = "other", time.Time{} }, versionOrID},
		{"no observation time", false, func(r *InvokeResponse) { r.Result.ObservedAt = time.Time{} }, adapterR5Fixed[InvokeResponse]("adapter response observed_at is required")},
		{"observation time checked before output", false, func(r *InvokeResponse) {
			r.Result.ObservedAt = time.Time{}
			r.Result.Output = json.RawMessage(`{}`)
		}, adapterR5Fixed[InvokeResponse]("adapter response observed_at is required")},
		{"streamed response that cannot be encoded", true, func(r *InvokeResponse) { r.Result.ExternalOperationRef = json.RawMessage(`{`) },
			adapterR5Fixed[InvokeResponse]("invalid streamed response")},
		{"transfer ID in the streamed response", true, func(r *InvokeResponse) {
			r.Result.ExternalOperationRef = json.RawMessage(`{"id":"x-` + adapterR5TransferID + `-x"}`)
		},
			adapterR5Fixed[InvokeResponse]("stream credential in adapter response")},
		{"streamed credential checked before output", true, func(r *InvokeResponse) {
			r.Result.ExternalOperationRef = json.RawMessage(`{"` + adapterR5TransferID + `":1}`)
			r.Result.Output = json.RawMessage(`{}`)
		}, adapterR5Fixed[InvokeResponse]("stream credential in adapter response")},
		{"output fails the capability schema", false, func(r *InvokeResponse) { r.Result.Output = json.RawMessage(`{}`) },
			func(f *adapterR5Fixture, r InvokeResponse) string {
				uri := f.capability(f.hosting.descriptor.AdapterID, "hosting.instance.inspect").OutputSchemaURI
				return fmt.Sprintf("invalid output for capability %q: %v", "hosting.instance.inspect", f.schemas.ValidateJSON(uri, r.Result.Output))
			}},
		{"response that cannot be encoded", false, func(r *InvokeResponse) { r.Result.ExternalOperationRef = json.RawMessage(`{`) },
			func(_ *adapterR5Fixture, r InvokeResponse) string {
				_, err := json.Marshal(r)
				return "encode adapter response: " + err.Error()
			}},
		{"result fails the response schema", false, func(r *InvokeResponse) { r.Result.Outcome = "finished" },
			func(f *adapterR5Fixture, r InvokeResponse) string {
				encoded, _ := json.Marshal(r)
				return "invalid adapter response: " + f.schemas.ValidateJSON("urn:gameserver-controller:"+f.reg.version+":rpc:adapter-invoke-result", encoded).Error()
			}},
	}
}

func TestAdapterR5CheckResponse(t *testing.T) {
	for _, test := range adapterR5ResponseCases() {
		t.Run(test.name, func(t *testing.T) {
			f := adapterR5NewFixture(t)
			response, _ := f.reply(f.inspect)
			test.change(&response)
			var transfers []capturestream.Reference
			if test.streamed {
				transfers = []capturestream.Reference{{TransferID: adapterR5TransferID}}
			}
			err := f.reg.checkResponse(f.capability(f.hosting.descriptor.AdapterID, "hosting.instance.inspect"), f.inspect.ID, transfers, response)
			adapterR5Error(t, err, test.want(f, response))
		})
	}
	t.Run("valid streamed response", func(t *testing.T) {
		f := adapterR5NewFixture(t)
		response, _ := f.reply(f.inspect)
		transfers := []capturestream.Reference{{TransferID: adapterR5TransferID}}
		if err := f.reg.checkResponse(f.capability(f.hosting.descriptor.AdapterID, "hosting.instance.inspect"), f.inspect.ID, transfers, response); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAdapterR5CheckStreamTransferBoundaries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Hour)
	streaming := Capability{Name: "storage.object.put", Constraints: map[string]any{"stream_transfer_direction": "controller_to_adapter"}}
	profiles := []string{"local-unix-stream-v1"}
	at := func(expires time.Time) []capturestream.Reference {
		return []capturestream.Reference{{TransferID: "a", Direction: "controller_to_adapter", ExpiresAt: expires}}
	}
	tests := []struct {
		name       string
		capability Capability
		transfers  []capturestream.Reference
		want       string
	}{
		{"direction that is not a string", Capability{Constraints: map[string]any{"stream_transfer_direction": 1}}, at(now.Add(time.Minute)), "capability does not declare stream transport"},
		{"expires exactly now", streaming, at(now), "invalid transient stream transfer"},
		{"expires exactly at the deadline", streaming, at(deadline), ""},
		{"expires just after the deadline", streaming, at(deadline.Add(time.Nanosecond)), "invalid transient stream transfer"},
		{"duplicate reported before an invalid first transfer", streaming, []capturestream.Reference{
			{TransferID: "a", Direction: "controller_to_adapter", ExpiresAt: now.Add(time.Minute)},
			{TransferID: "a", Direction: "adapter_to_controller", ExpiresAt: now},
		}, "transient stream transfer IDs must be unique within an invocation"},
		{"invalid first transfer reported before a later duplicate", streaming, []capturestream.Reference{
			{TransferID: "a", Direction: "adapter_to_controller", ExpiresAt: now.Add(time.Minute)},
			{TransferID: "a", Direction: "controller_to_adapter", ExpiresAt: now.Add(time.Minute)},
		}, "invalid transient stream transfer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkStreamTransfers(test.capability, profiles, RequestContext{StreamTransfers: test.transfers, Deadline: deadline}, now)
			if test.want == "" {
				if err != nil {
					t.Fatalf("checkStreamTransfers = %v, want nil", err)
				}
				return
			}
			adapterR5Error(t, err, test.want)
		})
	}
}

type adapterR5RegisterCase struct {
	name   string
	change func(*Descriptor)
	want   func(*adapterR5Fixture, Descriptor) string
}

func adapterR5RegisterCases() []adapterR5RegisterCase {
	return []adapterR5RegisterCase{
		{"descriptor that cannot be encoded", func(d *Descriptor) { d.Constraints = map[string]any{"bad": func() {}} },
			func(_ *adapterR5Fixture, d Descriptor) string {
				_, err := json.Marshal(d)
				return "encode adapter descriptor: " + err.Error()
			}},
		{"descriptor fails its schema", func(d *Descriptor) { d.AdapterID = "" },
			func(f *adapterR5Fixture, d Descriptor) string {
				encoded, _ := json.Marshal(d)
				return "invalid adapter descriptor: " + f.schemas.ValidateJSON("urn:gameserver-controller:"+f.reg.version+":common:capability-descriptor", encoded).Error()
			}},
		{"another protocol version", func(d *Descriptor) { d.ProtocolVersions = []string{"0.0.1"} },
			func(f *adapterR5Fixture, d Descriptor) string {
				return fmt.Sprintf("adapter %q does not support protocol %q", d.AdapterID, f.reg.version)
			}},
		{"capability outside the category", func(d *Descriptor) { d.Capabilities[1].Name = "storage.object.put" },
			adapterR5RegisterMessage(`capability "storage.object.put" does not belong to category "hosting"`)},
		{"duplicate capability", func(d *Descriptor) { d.Capabilities[1].Name = d.Capabilities[0].Name },
			adapterR5RegisterMessage(`adapter "hosting.r5" advertises duplicate capability "hosting.instance.create"`)},
		{"capabilities checked in order: duplicate first", func(d *Descriptor) {
			d.Capabilities[1].Name = d.Capabilities[0].Name
			d.Capabilities[2].Name = "game.save.request"
		}, adapterR5RegisterMessage(`adapter "hosting.r5" advertises duplicate capability "hosting.instance.create"`)},
		{"capabilities checked in order: category first", func(d *Descriptor) {
			d.Capabilities[1].Name = "game.save.request"
			d.Capabilities[2].Name = d.Capabilities[0].Name
		}, adapterR5RegisterMessage(`capability "game.save.request" does not belong to category "hosting"`)},
		{"category checked before schemas", func(d *Descriptor) {
			d.Capabilities[0].Name = "game.save.request"
			d.Capabilities[0].InputSchemaURI = "urn:gameserver-controller:missing"
		}, adapterR5RegisterMessage(`capability "game.save.request" does not belong to category "hosting"`)},
		{"unregistered input schema", func(d *Descriptor) { d.Capabilities[0].InputSchemaURI = "urn:gameserver-controller:missing" },
			adapterR5RegisterMessage(`capability "hosting.instance.create" references an unregistered input or output schema`)},
		{"unregistered output schema", func(d *Descriptor) { d.Capabilities[0].OutputSchemaURI = "urn:gameserver-controller:missing" },
			adapterR5RegisterMessage(`capability "hosting.instance.create" references an unregistered input or output schema`)},
		{"adapter already registered", func(d *Descriptor) { d.AdapterID = "hosting.example" },
			adapterR5RegisterMessage(`adapter "hosting.example" is already registered`)},
	}
}

func adapterR5RegisterMessage(message string) func(*adapterR5Fixture, Descriptor) string {
	return adapterR5Fixed[Descriptor](message)
}

func TestAdapterR5RegisterRejections(t *testing.T) {
	for _, test := range adapterR5RegisterCases() {
		t.Run(test.name, func(t *testing.T) {
			f := adapterR5NewFixture(t)
			implementation := adapterR5Example(t, "adapter-descriptor.hosting.json")
			implementation.descriptor.AdapterID = "hosting.r5"
			test.change(&implementation.descriptor)
			err := f.reg.Register(implementation)
			adapterR5Error(t, err, test.want(f, implementation.descriptor))
			if _, lookup := f.reg.Descriptor("hosting.r5"); !errors.Is(lookup, ErrAdapterNotFound) {
				t.Fatal("rejected adapter was registered")
			}
		})
	}
}

func TestAdapterR5RegisterRequiresRegistryAndImplementation(t *testing.T) {
	f := adapterR5NewFixture(t)
	var missing *Registry
	adapterR5Error(t, missing.Register(f.hosting), "registry and adapter implementation are required")
	adapterR5Error(t, f.reg.Register(nil), "registry and adapter implementation are required")
	if _, err := missing.Descriptor("hosting.example"); !errors.Is(err, ErrAdapterNotFound) {
		t.Fatalf("nil registry Descriptor error = %v", err)
	}
	if missing.ProtocolVersion() != "" {
		t.Fatal("nil registry reported a protocol version")
	}
	if _, err := NewRegistry(nil); err == nil || err.Error() != "a loaded protocol schema registry is required" {
		t.Fatalf("NewRegistry(nil) error = %v", err)
	}
}

func TestAdapterR5RegisterStoresAnIndependentCopy(t *testing.T) {
	f := adapterR5NewFixture(t)
	implementation := adapterR5Example(t, "adapter-descriptor.paper.json")
	implementation.descriptor.AdapterID = "game.r5"
	if err := f.reg.Register(implementation); err != nil {
		t.Fatal(err)
	}
	implementation.descriptor.Capabilities[0].Name = "game.changed"
	implementation.descriptor.StreamTransportProfiles[0] = "changed"
	stored, err := f.reg.Descriptor("game.r5")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Capabilities[0].Name == "game.changed" || stored.StreamTransportProfiles[0] == "changed" {
		t.Fatal("registered descriptor shares memory with the implementation's")
	}
	if _, ok := f.reg.adapters["game.r5"].capabilities["game.backup.capture"]; !ok {
		t.Fatal("registered adapter is missing its advertised capability")
	}
}

func TestAdapterR5CapabilityPrefix(t *testing.T) {
	tests := map[Category]string{CategoryHosting: "hosting.", CategoryStorage: "storage.", CategoryExecution: "execution.",
		CategoryGame: "game.", CategoryIntegration: "integration.", "integrations": ""}
	for category, want := range tests {
		if got, ok := capabilityPrefix(category); got != want || ok != (want != "") {
			t.Fatalf("capabilityPrefix(%q) = %q, %v; want %q", category, got, ok, want)
		}
	}
}

type adapterR5Closer struct{ bytes.Buffer }

func (*adapterR5Closer) Close() error { return nil }

func TestAdapterR5StreamHandles(t *testing.T) {
	source := io.NopCloser(bytes.NewReader(nil))
	var empty RequestContext
	if _, err := empty.OpenStream("s"); err == nil || err.Error() != "stream source is unavailable" {
		t.Fatalf("OpenStream without access = %v", err)
	}
	if _, err := empty.CreateStream("d"); err == nil || err.Error() != "stream destination is unavailable" {
		t.Fatalf("CreateStream without access = %v", err)
	}
	if empty.HasStreamDestination("d") {
		t.Fatal("destination reported without access")
	}
	var c RequestContext
	c.SetStreamSources(map[string]io.ReadCloser{"s": source, "": source})
	c.SetStreamDestinations(map[string]io.WriteCloser{"d": &adapterR5Closer{}, "": &adapterR5Closer{}})
	adapterR5ClaimTwice(t, "source", func(id string) error { _, err := c.OpenStream(id); return err }, "s", "stream source is unavailable")
	if !c.HasStreamDestination("d") || c.HasStreamDestination("") || c.HasStreamDestination("missing") {
		t.Fatal("HasStreamDestination misreported an attached handle")
	}
	adapterR5ClaimTwice(t, "destination", func(id string) error { _, err := c.CreateStream(id); return err }, "d", "stream destination is unavailable")
	if c.HasStreamDestination("d") {
		t.Fatal("claimed destination still reported")
	}
}

// adapterR5ClaimTwice requires claim to reject an empty and an unknown ID,
// accept id once, and then reject it with message.
func adapterR5ClaimTwice(t *testing.T, kind string, claim func(string) error, id, message string) {
	t.Helper()
	for _, rejected := range []string{"", "missing"} {
		if err := claim(rejected); err == nil || err.Error() != message {
			t.Fatalf("%s %q claim = %v, want %q", kind, rejected, err, message)
		}
	}
	if err := claim(id); err != nil {
		t.Fatalf("first %s claim = %v", kind, err)
	}
	if err := claim(id); err == nil || err.Error() != message {
		t.Fatalf("second %s claim = %v, want %q", kind, err, message)
	}
}

func TestAdapterR5StreamHandleAttachment(t *testing.T) {
	source := io.NopCloser(bytes.NewReader(nil))
	t.Run("destinations alone leave no sources", func(t *testing.T) {
		var c RequestContext
		c.SetStreamDestinations(map[string]io.WriteCloser{"d": &adapterR5Closer{}})
		if _, err := c.OpenStream("d"); err == nil || err.Error() != "stream source is unavailable" {
			t.Fatalf("OpenStream = %v", err)
		}
	})
	t.Run("sources alone leave no destinations", func(t *testing.T) {
		var c RequestContext
		c.SetStreamSources(map[string]io.ReadCloser{"s": source})
		if _, err := c.CreateStream("s"); err == nil || err.Error() != "stream destination is unavailable" {
			t.Fatalf("CreateStream = %v", err)
		}
	})
	t.Run("setting sources replaces earlier destinations", func(t *testing.T) {
		var c RequestContext
		c.SetStreamDestinations(map[string]io.WriteCloser{"d": &adapterR5Closer{}})
		c.SetStreamSources(map[string]io.ReadCloser{"s": source})
		if c.HasStreamDestination("d") {
			t.Fatal("destination survived SetStreamSources")
		}
	})
	t.Run("clearing removes every handle", func(t *testing.T) {
		var c RequestContext
		c.SetStreamSources(map[string]io.ReadCloser{"s": source})
		c.ClearStreamSources()
		if _, err := c.OpenStream("s"); err == nil {
			t.Fatal("cleared source still open")
		}
	})
	t.Run("nil contexts are ignored", func(t *testing.T) {
		var c *RequestContext
		c.SetStreamSources(nil)
		c.SetStreamDestinations(nil)
		c.ClearStreamSources()
	})
}

func TestAdapterR5ContainsStreamCredential(t *testing.T) {
	transfers := []capturestream.Reference{{TransferID: "other"}, {TransferID: "secret"}}
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{"string containing the handle", "x-secret-x", true},
		{"string without it", "public", false},
		{"nested array", []any{1.0, []any{"public", "secret"}}, true},
		{"array without it", []any{"public", true}, false},
		{"object key", map[string]any{"secret-key": "public"}, true},
		{"object value", map[string]any{"key": map[string]any{"inner": "secret"}}, true},
		{"object without it", map[string]any{"key": "public"}, false},
		{"number", 42.0, false},
		{"null", nil, false},
	}
	for _, test := range tests {
		if got := containsAnyStreamCredential(test.value, transfers); got != test.want {
			t.Errorf("%s: containsAnyStreamCredential = %v, want %v", test.name, got, test.want)
		}
	}
	if containsAnyStreamCredential("secret", nil) {
		t.Error("no transfers reported a credential")
	}
}
