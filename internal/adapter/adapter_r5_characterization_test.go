package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Schema validation and encoding run before the credential search in both
// callers, so its not-JSON branch is reached only directly.
func TestAdapterR5CheckNoStreamCredential(t *testing.T) {
	transfers := []capturestream.Reference{{TransferID: "secret"}}
	tests := []struct {
		name    string
		encoded string
		want    string
	}{
		{"not JSON", `{`, "invalid"},
		{"contains a transfer ID", `{"a":"secret"}`, "found"},
		{"clean", `{"a":"public"}`, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkNoStreamCredential([]byte(test.encoded), transfers, "invalid", "found")
			if test.want == "" {
				if err != nil {
					t.Fatalf("checkNoStreamCredential = %v, want nil", err)
				}
				return
			}
			adapterR5Error(t, err, test.want)
		})
	}
}
