package fakeadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

const externalRefSchema = "urn:gameserver-controller:0.21.0-draft:common:external-resource-ref"

func TestScriptedAdaptersRunThroughSchemasForEveryCategory(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	categories := []struct {
		category   adapter.Category
		adapterID  string
		capability string
	}{
		{adapter.CategoryHosting, "hosting.fake", "hosting.fake.inspect"},
		{adapter.CategoryStorage, "storage.fake", "storage.fake.inspect"},
		{adapter.CategoryExecution, "execution.fake", "execution.fake.inspect"},
		{adapter.CategoryGame, "game.fake", "game.fake.inspect"},
		{adapter.CategoryIntegration, "integration.fake", "integration.fake.inspect"},
	}
	for _, test := range categories {
		t.Run(string(test.category), func(t *testing.T) {
			descriptor := adapter.Descriptor{
				AdapterID:             test.adapterID,
				Category:              test.category,
				DisplayName:           "scripted test adapter",
				ImplementationVersion: "test-1",
				ProtocolVersions:      []string{schemas.ProtocolVersion()},
				ContractVersions:      []string{"1"},
				RequiredPermissions:   []string{},
				Capabilities: []adapter.Capability{{
					Name: test.capability, Version: "1", Mutating: true,
					InputSchemaURI: externalRefSchema, OutputSchemaURI: externalRefSchema,
					RequiredPermissions: []string{},
				}},
			}
			fake := New(descriptor)
			output := fmt.Sprintf(`{"adapter_id":%q,"environment_id":"test","resource_kind":"fake-resource","external_id":"opaque-out"}`, test.adapterID)
			if err := fake.SetScript(test.capability, Reply{
				Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(output), ObservedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(fake); err != nil {
				t.Fatal(err)
			}
			request := invokeRequest(test.capability, test.adapterID, true)
			response, err := registry.Invoke(context.Background(), test.adapterID, request)
			if err != nil {
				t.Fatal(err)
			}
			if response.ID != request.ID || response.Result.Outcome != adapter.OutcomeCompleted {
				t.Fatalf("unexpected fake response: %#v", response)
			}
			if got := len(fake.Calls()); got != 1 {
				t.Fatalf("fake recorded %d calls, want 1", got)
			}
			unsupported := request
			unsupported.Params.Capability = "unsupported.capability"
			if _, err := registry.Invoke(context.Background(), test.adapterID, unsupported); !errors.Is(err, adapter.ErrUnsupportedCapability) {
				t.Fatalf("unsupported capability: got %v", err)
			}
			if got := len(fake.Calls()); got != 1 {
				t.Fatalf("unsupported capability reached fake adapter; calls=%d", got)
			}
		})
	}
}

func TestMutatingCallRequiresOperationStepAndValidOutput(t *testing.T) {
	registry, fake := testRegistry(t, []Reply{{
		Outcome:    adapter.OutcomeCompleted,
		Output:     json.RawMessage(`{}`),
		ObservedAt: time.Now().UTC(),
	}})
	request := invokeRequest("hosting.fake.inspect", "hosting.fake", false)
	if _, err := registry.Invoke(context.Background(), "hosting.fake", request); !errors.Is(err, adapter.ErrMutatingContext) {
		t.Fatalf("mutating call without durable IDs: got %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Fatal("call missing durable operation context reached adapter")
	}

	request = invokeRequest("hosting.fake.inspect", "hosting.fake", true)
	if _, err := registry.Invoke(context.Background(), "hosting.fake", request); err == nil {
		t.Fatal("invalid output passed capability output schema")
	} else {
		var invocationErr *adapter.InvocationError
		if !errors.As(err, &invocationErr) || !invocationErr.MayHaveSideEffects {
			t.Fatalf("invalid mutating output must report possible side effects: %v", err)
		}
	}
	if len(fake.Calls()) != 1 {
		t.Fatalf("fake call count = %d, want 1", len(fake.Calls()))
	}
}

func TestExpiredDeadlineDoesNotDispatch(t *testing.T) {
	registry, fake := testRegistry(t, []Reply{{
		Outcome:    adapter.OutcomeCompleted,
		Output:     json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"test","resource_kind":"instance","external_id":"opaque-out"}`),
		ObservedAt: time.Now().UTC(),
	}})
	request := invokeRequest("hosting.fake.inspect", "hosting.fake", true)
	request.Params.RequestContext.Deadline = time.Now().Add(-time.Second)
	if _, err := registry.Invoke(context.Background(), "hosting.fake", request); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired request deadline: got %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Fatal("expired request was dispatched")
	}
}

func TestDeadlineAfterDispatchIsMarkedAmbiguous(t *testing.T) {
	registry, fake := testRegistry(t, []Reply{{
		Outcome:    adapter.OutcomeCompleted,
		Output:     json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"test","resource_kind":"instance","external_id":"opaque-out"}`),
		ObservedAt: time.Now().UTC(),
		Delay:      100 * time.Millisecond,
	}})
	request := invokeRequest("hosting.fake.inspect", "hosting.fake", true)
	request.Params.RequestContext.Deadline = time.Now().Add(10 * time.Millisecond)
	_, err := registry.Invoke(context.Background(), "hosting.fake", request)
	var invocationErr *adapter.InvocationError
	if !errors.As(err, &invocationErr) || !invocationErr.MayHaveSideEffects || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired in-flight mutating call must be ambiguous: %v", err)
	}
	if len(fake.Calls()) != 1 {
		t.Fatalf("in-flight call count = %d, want 1", len(fake.Calls()))
	}
}

func TestInvalidInputIsRejectedBeforeDispatch(t *testing.T) {
	registry, fake := testRegistry(t, []Reply{{
		Outcome:    adapter.OutcomeCompleted,
		Output:     json.RawMessage(`{"adapter_id":"hosting.fake","environment_id":"test","resource_kind":"instance","external_id":"opaque-out"}`),
		ObservedAt: time.Now().UTC(),
	}})
	request := invokeRequest("hosting.fake.inspect", "hosting.fake", true)
	request.Params.Input = json.RawMessage(`{}`)
	if _, err := registry.Invoke(context.Background(), "hosting.fake", request); err == nil {
		t.Fatal("invalid capability input was accepted")
	}
	if len(fake.Calls()) != 0 {
		t.Fatal("invalid input reached the adapter")
	}
}

func testRegistry(t *testing.T, replies []Reply) (*adapter.Registry, *Scripted) {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	fake := New(adapter.Descriptor{
		AdapterID: "hosting.fake", Category: adapter.CategoryHosting,
		DisplayName: "scripted fake", ImplementationVersion: "test-1",
		ProtocolVersions: []string{schemas.ProtocolVersion()}, ContractVersions: []string{"1"},
		RequiredPermissions: []string{},
		Capabilities:        []adapter.Capability{{Name: "hosting.fake.inspect", Version: "1", Mutating: true, InputSchemaURI: externalRefSchema, OutputSchemaURI: externalRefSchema, RequiredPermissions: []string{}}},
	})
	if err := fake.SetScript("hosting.fake.inspect", replies...); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fake); err != nil {
		t.Fatal(err)
	}
	return registry, fake
}

func invokeRequest(capability, adapterID string, includeOperationContext bool) adapter.InvokeRequest {
	requestContext := adapter.RequestContext{
		RequestID: "request-1", CorrelationID: "correlation-1", EnvironmentID: "test",
		Deadline: time.Now().Add(time.Minute).UTC(),
	}
	if includeOperationContext {
		requestContext.OperationID = "operation-1"
		requestContext.StepID = "step-1"
	}
	input := fmt.Sprintf(`{"adapter_id":%q,"environment_id":"test","resource_kind":"instance","external_id":"opaque-in"}`, adapterID)
	return adapter.InvokeRequest{
		JSONRPC: "2.0", ID: requestContext.RequestID, Method: "adapter.invoke",
		Params: adapter.InvokeParams{
			ProtocolVersion: "0.21.0-draft", Capability: capability,
			RequestContext: requestContext, Input: json.RawMessage(input),
		},
	}
}
