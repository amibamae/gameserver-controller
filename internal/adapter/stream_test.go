package adapter_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

type captureAdapter struct {
	descriptor adapter.Descriptor
	invoke     func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
}

func (a *captureAdapter) Descriptor() adapter.Descriptor { return a.descriptor }
func (a *captureAdapter) Invoke(_ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return a.invoke(r)
}

func readExample(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile("../../protocol/examples/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureAdapterConsumesTransientBrokerStream(t *testing.T) {
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor adapter.Descriptor
	readExample(t, "adapter-descriptor.paper.json", &descriptor)
	var request adapter.InvokeRequest
	readExample(t, "game-backup-capture-invocation.json", &request)
	var output map[string]any
	readExample(t, "game-backup-capture-output.json", &output)
	broker, _ := capturestream.New(1)
	scope := capturestream.Scope{OperationID: request.Params.RequestContext.OperationID, StepID: request.Params.RequestContext.StepID, AdapterID: descriptor.AdapterID, ServerID: "minecraft-demo", EnvironmentID: "staging", InstanceAdapterID: "hosting.vultr", InstanceID: "instance-demo-01", DataGenerationID: "generation-demo-004", DataSetID: "world", CaptureID: "operation-backup-004-capture-world"}
	request.Params.RequestContext.Deadline = time.Now().Add(2 * time.Minute)
	var sink bytes.Buffer
	ref, ticket, err := broker.IssueTracked(scope, 3, time.Now().Add(time.Minute), request.Params.RequestContext.Deadline, &sink)
	if err != nil {
		t.Fatal(err)
	}
	request.Params.RequestContext.StreamTransfers = []capturestream.Reference{ref}
	called := false
	implementation := &captureAdapter{descriptor: descriptor, invoke: func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		called = true
		if len(r.Params.RequestContext.StreamTransfers) != 1 {
			t.Fatal("stream context was lost")
		}
		if bytes.Contains(r.Params.Input, []byte(ref.TransferID)) {
			t.Fatal("stream credential entered durable input")
		}
		writer, err := broker.Claim(context.Background(), r.Params.RequestContext.StreamTransfers[0].TransferID, scope, nil)
		if err != nil {
			return adapter.InvokeResponse{}, err
		}
		if _, err := writer.Write([]byte("abc")); err != nil {
			return adapter.InvokeResponse{}, err
		}
		receipt, err := writer.Complete()
		if err != nil {
			return adapter.InvokeResponse{}, err
		}
		output["size_bytes"] = receipt.SizeBytes
		output["checksum"] = map[string]any{"algorithm": "sha256", "digest": receipt.SHA256}
		output["observed_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		encoded, err := json.Marshal(output)
		if err != nil {
			return adapter.InvokeResponse{}, err
		}
		return adapter.InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: encoded, ObservedAt: time.Now().UTC()}}, nil
	}}
	if err := registry.Register(implementation); err != nil {
		t.Fatal(err)
	}
	response, err := registry.Invoke(context.Background(), descriptor.AdapterID, request)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := ticket.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	var reported struct {
		Size     int64 `json:"size_bytes"`
		Checksum struct {
			Digest string `json:"digest"`
		} `json:"checksum"`
	}
	if json.Unmarshal(response.Result.Output, &reported) != nil || evidence.Scope != scope || evidence.SizeBytes != reported.Size || evidence.SHA256 != reported.Checksum.Digest {
		t.Fatal("adapter output does not match independent controller evidence")
	}
	if !called || sink.String() != "abc" {
		t.Fatal("capture did not reach private staging sink")
	}
}

func TestStreamValidationAndErrorsDoNotExposeHandles(t *testing.T) {
	for _, scenario := range []string{"missing", "undeclared", "direction", "expired", "input-leak", "adapter-error", "response-leak", "escaped-response-leak"} {
		t.Run(scenario, func(t *testing.T) {
			schemas, err := schemaregistry.Load()
			if err != nil {
				t.Fatal(err)
			}
			registry, _ := adapter.NewRegistry(schemas)
			var descriptor adapter.Descriptor
			readExample(t, "adapter-descriptor.paper.json", &descriptor)
			var request adapter.InvokeRequest
			readExample(t, "game-backup-capture-invocation.json", &request)
			token := strings.Repeat("d", 64)
			request.Params.RequestContext.Deadline = time.Now().Add(2 * time.Minute)
			request.Params.RequestContext.StreamTransfers = []capturestream.Reference{{TransferID: token, Direction: "adapter_to_controller", MaximumBytes: 3, ExpiresAt: time.Now().Add(time.Minute)}}
			switch scenario {
			case "missing":
				request.Params.RequestContext.StreamTransfers = nil
			case "undeclared":
				descriptor.StreamTransportProfiles = nil
			case "direction":
				request.Params.RequestContext.StreamTransfers[0].Direction = "controller_to_adapter"
			case "expired":
				request.Params.RequestContext.StreamTransfers[0].ExpiresAt = time.Now().Add(-time.Second)
			case "input-leak":
				var input map[string]json.RawMessage
				if err := json.Unmarshal(request.Params.Input, &input); err != nil {
					t.Fatal(err)
				}
				input["capture_id"] = json.RawMessage(`"` + strings.Repeat(`\u0064`, 64) + `"`)
				request.Params.Input, err = json.Marshal(input)
				if err != nil {
					t.Fatal(err)
				}
			}
			called := false
			implementation := &captureAdapter{descriptor: descriptor, invoke: func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
				called = true
				if scenario == "adapter-error" {
					return adapter.InvokeResponse{}, errors.New(token)
				}
				if scenario == "escaped-response-leak" {
					return adapter.InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: adapter.InvokeResult{ObservedAt: time.Now(), Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(`{"reason_code":"` + strings.Repeat(`\u0064`, 64) + `"}`)}}, nil
				}
				return adapter.InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: adapter.InvokeResult{ObservedAt: time.Now(), Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(`{"reason_code":"` + token + `"}`)}}, nil
			}}
			if err := registry.Register(implementation); err != nil {
				t.Fatal(err)
			}
			_, err = registry.Invoke(context.Background(), descriptor.AdapterID, request)
			if err == nil || strings.Contains(err.Error(), token) {
				t.Fatalf("unsafe error: %v", err)
			}
			shouldCall := scenario == "adapter-error" || scenario == "response-leak" || scenario == "escaped-response-leak"
			if called != shouldCall {
				t.Fatalf("adapter called=%v", called)
			}
			if shouldCall {
				var invocation *adapter.InvocationError
				if !errors.As(err, &invocation) || !invocation.MayHaveSideEffects {
					t.Fatal("uncertain effect classification was lost")
				}
			}
		})
	}
}
