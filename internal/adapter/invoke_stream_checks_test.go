package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

// Registry.Invoke redacts every streamed failure to one message, so this test
// calls the unexported invoke to pin which stream rule rejected the request.

type streamCheckAdapter struct {
	descriptor Descriptor
	output     json.RawMessage
	called     bool
}

func (a *streamCheckAdapter) Descriptor() Descriptor { return a.descriptor }
func (a *streamCheckAdapter) Invoke(_ context.Context, r InvokeRequest) (InvokeResponse, error) {
	a.called = true
	return InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: InvokeResult{Outcome: OutcomeCompleted, Output: a.output, ObservedAt: time.Now()}}, nil
}

func readStreamExample(t *testing.T, name string, target any) {
	t.Helper()
	data, err := os.ReadFile("../../protocol/examples/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatal(err)
	}
}

func TestInvokeStreamRulesReportTheirReason(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(request *InvokeRequest, output map[string]any)
		want   string
	}{
		{"capture without a transfer", func(r *InvokeRequest, _ map[string]any) { r.Params.RequestContext.StreamTransfers = nil }, "backup capture requires one stream transfer"},
		{"two transfers for a capture", func(r *InvokeRequest, _ map[string]any) {
			r.Params.RequestContext.StreamTransfers = append(r.Params.RequestContext.StreamTransfers, r.Params.RequestContext.StreamTransfers[0])
		}, "backup capture requires one stream transfer"},
		{"transfer in the wrong direction", func(r *InvokeRequest, _ map[string]any) {
			r.Params.RequestContext.StreamTransfers[0].Direction = "controller_to_adapter"
		}, "invalid transient stream transfer"},
		{"transfer expires after the deadline", func(r *InvokeRequest, _ map[string]any) {
			r.Params.RequestContext.Deadline = time.Now().Add(30 * time.Second)
		}, "invalid transient stream transfer"},
		{"capture transfer that lives too long", func(r *InvokeRequest, _ map[string]any) {
			r.Params.RequestContext.Deadline = time.Now().Add(time.Hour)
			r.Params.RequestContext.StreamTransfers[0].ExpiresAt = time.Now().Add(10 * time.Minute)
		}, "invalid transient stream transfer"},
		{"transfer ID in the durable input", func(r *InvokeRequest, _ map[string]any) {
			var input map[string]any
			_ = json.Unmarshal(r.Params.Input, &input)
			input["capture_id"] = r.Params.RequestContext.StreamTransfers[0].TransferID
			r.Params.Input, _ = json.Marshal(input)
		}, "stream credential in durable input"},
		{"transfer ID in the adapter response", func(r *InvokeRequest, output map[string]any) {
			output["reason_code"] = r.Params.RequestContext.StreamTransfers[0].TransferID
		}, "stream credential in adapter response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schemas, err := schemaregistry.Load()
			if err != nil {
				t.Fatal(err)
			}
			registry, err := NewRegistry(schemas)
			if err != nil {
				t.Fatal(err)
			}
			implementation := &streamCheckAdapter{}
			readStreamExample(t, "adapter-descriptor.paper.json", &implementation.descriptor)
			if err := registry.Register(implementation); err != nil {
				t.Fatal(err)
			}
			var request InvokeRequest
			readStreamExample(t, "game-backup-capture-invocation.json", &request)
			request.Params.RequestContext.Deadline = time.Now().Add(2 * time.Minute)
			c := request.Params.RequestContext
			broker, _ := capturestream.New(4)
			reference, _, err := broker.IssueTracked(capturestream.Scope{OperationID: c.OperationID, StepID: c.StepID, AdapterID: implementation.descriptor.AdapterID,
				ServerID: "minecraft-demo", EnvironmentID: "staging", InstanceAdapterID: "hosting.vultr", InstanceID: "instance-demo-01",
				DataGenerationID: "generation-demo-004", DataSetID: "world", CaptureID: "operation-backup-004-capture-world"},
				8, time.Now().Add(time.Minute), c.Deadline, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			request.Params.RequestContext.StreamTransfers = []capturestream.Reference{reference}
			var output map[string]any
			readStreamExample(t, "game-backup-capture-output.json", &output)
			test.mutate(&request, output)
			implementation.output, _ = json.Marshal(output)

			_, err = registry.invoke(context.Background(), implementation.descriptor.AdapterID, request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invoke error = %v, want one containing %q", err, test.want)
			}
		})
	}
}

func TestCheckStreamTransfers(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	streaming := Capability{Name: "storage.object.put", Constraints: map[string]any{"stream_transfer_direction": "controller_to_adapter"}}
	capture := Capability{Name: "game.backup.capture", Constraints: map[string]any{"stream_transfer_direction": "adapter_to_controller"}}
	transfer := func(id, direction string, expiresIn time.Duration) capturestream.Reference {
		return capturestream.Reference{TransferID: id, Direction: direction, ExpiresAt: now.Add(expiresIn)}
	}
	deadline := now.Add(time.Hour)
	tests := []struct {
		name       string
		capability Capability
		profiles   []string
		transfers  []capturestream.Reference
		want       string
	}{
		{"no transfers", Capability{}, nil, nil, ""},
		{"valid transfers", streaming, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "controller_to_adapter", time.Minute), transfer("b", "controller_to_adapter", time.Minute)}, ""},
		{"capability without a direction", Capability{Name: "game.save.request"}, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "controller_to_adapter", time.Minute)}, "does not declare stream transport"},
		{"adapter without a stream profile", streaming, nil, []capturestream.Reference{transfer("a", "controller_to_adapter", time.Minute)}, "does not declare stream transport"},
		{"repeated transfer ID", streaming, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "controller_to_adapter", time.Minute), transfer("a", "controller_to_adapter", time.Minute)}, "must be unique"},
		{"wrong direction", streaming, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "adapter_to_controller", time.Minute)}, "invalid transient stream transfer"},
		{"already expired", streaming, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "controller_to_adapter", 0)}, "invalid transient stream transfer"},
		{"expires after the deadline", streaming, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "controller_to_adapter", 2*time.Hour)}, "invalid transient stream transfer"},
		{"long-lived transfer outside capture", streaming, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "controller_to_adapter", 10*time.Minute)}, ""},
		{"capture transfer longer than five minutes", capture, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "adapter_to_controller", 6*time.Minute)}, "invalid transient stream transfer"},
		{"capture transfer of exactly five minutes", capture, []string{"local-unix-stream-v1"}, []capturestream.Reference{transfer("a", "adapter_to_controller", 5*time.Minute)}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkStreamTransfers(test.capability, test.profiles, RequestContext{StreamTransfers: test.transfers, Deadline: deadline}, now)
			if test.want == "" {
				if err != nil {
					t.Fatalf("checkStreamTransfers = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("checkStreamTransfers = %v, want one containing %q", err, test.want)
			}
		})
	}
}
