package captureinvoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests pin the rejection paths of Invoke and RecoverSealedCapture that
// TestCaptureDispatchUsesDurableScopeAndIndependentEvidence does not reach.
// Each starts from a request that would succeed and changes one thing.

// failingRecords returns the configured errors from the durable store.
type failingRecords struct {
	records
	opErr, lockErr error
}

func (r *failingRecords) GetOperation(id string) (store.StoredOperation, error) {
	if r.opErr != nil {
		return store.StoredOperation{}, r.opErr
	}
	return r.records.GetOperation(id)
}
func (r *failingRecords) GetResourceLock(scope string) (store.ResourceLock, error) {
	if r.lockErr != nil {
		return store.ResourceLock{}, r.lockErr
	}
	return r.records.GetResourceLock(scope)
}

// openOnlyStaging can open a stage but cannot recover a sealed capture.
type openOnlyStaging struct{ s *stage }

func (s openOnlyStaging) Open(context.Context, capturestream.Scope) (Stage, error) { return s.s, nil }

// fixedRecovery returns fixed sealed evidence for any scope.
type fixedRecovery struct {
	staging
	receipt capturestream.Receipt
	output  json.RawMessage
	err     error
}

func (s fixedRecovery) OpenVerifiedCapture(context.Context, capturestream.Scope) (io.ReadCloser, capturestream.Receipt, json.RawMessage, error) {
	if s.err != nil {
		return nil, capturestream.Receipt{}, nil, s.err
	}
	return io.NopCloser(bytes.NewReader([]byte("abc"))), s.receipt, s.output, nil
}

type captureHarness struct {
	t          *testing.T
	schemas    *schemaregistry.Registry
	registry   *adapter.Registry
	broker     *capturestream.Broker
	descriptor adapter.Descriptor
	request    adapter.InvokeRequest
	input      capture
	scope      capturestream.Scope
	records    *failingRecords
	sink       *stage
	staging    Staging
	called     bool
	respond    func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
}

func newCaptureHarness(t *testing.T) *captureHarness {
	t.Helper()
	h := &captureHarness{t: t, sink: &stage{}}
	var err error
	if h.schemas, err = schemaregistry.Load(); err != nil {
		t.Fatal(err)
	}
	h.registry, _ = adapter.NewRegistry(h.schemas)
	h.broker, _ = capturestream.New(2)
	example(t, "adapter-descriptor.paper.json", &h.descriptor)
	example(t, "game-backup-capture-invocation.json", &h.request)
	h.request.Params.RequestContext.StreamTransfers = nil
	h.request.Params.RequestContext.Deadline = time.Now().Add(2 * time.Minute)
	if err = json.Unmarshal(h.request.Params.Input, &h.input); err != nil {
		t.Fatal(err)
	}
	c := h.request.Params.RequestContext
	h.scope = capturestream.Scope{OperationID: c.OperationID, StepID: c.StepID, AdapterID: h.descriptor.AdapterID, ServerID: h.input.Target.ServerID,
		EnvironmentID: h.input.Target.EnvironmentID, InstanceAdapterID: "hosting.vultr", InstanceID: "instance-demo-01",
		DataGenerationID: h.input.GenerationID, DataSetID: h.input.DataSetID, CaptureID: h.input.CaptureID}
	version := ""
	for _, capability := range h.descriptor.Capabilities {
		if capability.Name == "game.backup.capture" {
			version = capability.Version
		}
	}
	stepID := h.scope.StepID
	h.records = &failingRecords{records: records{
		op: store.StoredOperation{Revision: 3, Record: store.OperationRecord{OperationID: h.scope.OperationID, OperationType: "server.backup",
			ProtocolVersion: h.schemas.ProtocolVersion(), Status: protocolstate.OperationRunning, BackupInitialLifecycleState: protocolstate.ServerStopped,
			CurrentStepID: &stepID, Target: store.OperationTarget{ServerID: h.scope.ServerID, EnvironmentID: h.scope.EnvironmentID, InstanceRef: h.input.Target.InstanceRef},
			Steps: []store.OperationStep{{StepID: stepID, Status: protocolstate.StepRunning, Attempt: 1, AdapterID: h.descriptor.AdapterID,
				CapabilityName: "game.backup.capture", CapabilityVersion: version, Input: append(json.RawMessage(nil), h.request.Params.Input...)}}}},
		lock: store.ResourceLock{Scope: store.LogicalServerLockScope(h.scope.EnvironmentID, h.scope.ServerID), OperationID: h.scope.OperationID},
	}}
	h.staging = staging{s: h.sink}
	h.respond = h.captured
	if err = h.registry.Register(implementation{d: h.descriptor, invoke: func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		h.called = true
		return h.respond(r)
	}}); err != nil {
		t.Fatal(err)
	}
	return h
}

// output returns the example capture output bound to this request.
func (h *captureHarness) output() map[string]any {
	var output map[string]any
	example(h.t, "game-backup-capture-output.json", &output)
	output["capture_id"] = h.input.CaptureID
	output["size_bytes"] = 3
	output["checksum"] = map[string]any{"algorithm": "sha256", "digest": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}
	return output
}

func (h *captureHarness) reply(r adapter.InvokeRequest, outcome adapter.Outcome, output any) (adapter.InvokeResponse, error) {
	encoded, _ := json.Marshal(output)
	return adapter.InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: adapter.InvokeResult{Outcome: outcome, Output: encoded, ObservedAt: time.Now()}}, nil
}

// captured streams "abc" and reports a matching completed capture.
func (h *captureHarness) captured(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	writer, err := h.broker.Claim(context.Background(), r.Params.RequestContext.StreamTransfers[0].TransferID, h.scope, nil)
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	if _, err = writer.Write([]byte("abc")); err != nil {
		return adapter.InvokeResponse{}, err
	}
	if _, err = writer.Complete(); err != nil {
		return adapter.InvokeResponse{}, err
	}
	return h.reply(r, adapter.OutcomeCompleted, h.output())
}

func (h *captureHarness) runtime() *Runtime {
	h.t.Helper()
	runtime, err := New(h.registry, h.schemas, h.records, h.broker, h.staging, 10)
	if err != nil {
		h.t.Fatal(err)
	}
	return runtime
}

func (h *captureHarness) step() *store.OperationStep { return &h.records.op.Record.Steps[0] }

// setInput replaces the capture input in both the request and the durable step.
func (h *captureHarness) setInput(change func(map[string]any)) {
	var input map[string]any
	if err := json.Unmarshal(h.request.Params.Input, &input); err != nil {
		h.t.Fatal(err)
	}
	change(input)
	encoded, _ := json.Marshal(input)
	h.request.Params.Input = encoded
	h.step().Input = append(json.RawMessage(nil), encoded...)
}

// setInstanceEnvironment moves the target instance reference, in the request,
// the durable step, and the operation target, to another environment.
func (h *captureHarness) setInstanceEnvironment(environment string) {
	h.setInput(func(input map[string]any) {
		input["target"].(map[string]any)["instance_ref"].(map[string]any)["environment_id"] = environment
	})
	var input capture
	if err := json.Unmarshal(h.request.Params.Input, &input); err != nil {
		h.t.Fatal(err)
	}
	h.records.op.Record.Target.InstanceRef = append(json.RawMessage(nil), input.Target.InstanceRef...)
}

func TestInvokeSuccessBaseline(t *testing.T) {
	h := newCaptureHarness(t)
	if _, err := h.runtime().Invoke(context.Background(), h.descriptor.AdapterID, h.request); err != nil || !h.sink.sealed {
		t.Fatalf("baseline capture: err=%v sealed=%v", err, h.sink.sealed)
	}
}

func TestInvokeRejectsBeforeDispatch(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*captureHarness) (context.Context, string)
		message string
	}{
		{"canceled context", func(h *captureHarness) (context.Context, string) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, h.descriptor.AdapterID
		}, "invalid capture dispatch context"},
		{"two stream transfers", func(h *captureHarness) (context.Context, string) {
			h.request.Params.RequestContext.StreamTransfers = make([]capturestream.Reference, 2)
			return context.Background(), h.descriptor.AdapterID
		}, "invalid capture dispatch context"},
		{"operation unavailable", func(h *captureHarness) (context.Context, string) {
			h.records.opErr = errors.New("not found")
			return context.Background(), h.descriptor.AdapterID
		}, "capture operation is unavailable"},
		{"operation is not a backup", func(h *captureHarness) (context.Context, string) {
			h.records.op.Record.OperationType = "server.restore"
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"operation is not running", func(h *captureHarness) (context.Context, string) {
			h.records.op.Record.Status = protocolstate.OperationReconciling
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"request for another protocol version", func(h *captureHarness) (context.Context, string) {
			h.request.Params.ProtocolVersion = "0.1.0"
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"step is not running", func(h *captureHarness) (context.Context, string) {
			h.step().Status = protocolstate.StepOutcomeUnknown
			return context.Background(), h.descriptor.AdapterID
		}, "capture request does not match durable step"},
		{"step recorded for another adapter", func(h *captureHarness) (context.Context, string) {
			h.step().AdapterID = "game.other"
			return context.Background(), h.descriptor.AdapterID
		}, "capture request does not match durable step"},
		{"adapter not registered", func(h *captureHarness) (context.Context, string) {
			h.step().AdapterID = "game.unregistered"
			return context.Background(), "game.unregistered"
		}, "capture adapter is unavailable"},
		{"deadline after the operation deadline", func(h *captureHarness) (context.Context, string) {
			deadline := time.Now().Add(time.Minute)
			h.records.op.Record.Deadline = &deadline
			return context.Background(), h.descriptor.AdapterID
		}, "capture capability or deadline differs from durable plan"},
		{"lock unavailable", func(h *captureHarness) (context.Context, string) {
			h.records.lockErr = errors.New("not found")
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires the durable server lock"},
		{"input fails its schema", func(h *captureHarness) (context.Context, string) {
			h.setInput(func(input map[string]any) { delete(input, "capture_id") })
			return context.Background(), h.descriptor.AdapterID
		}, "invalid capture input"},
		{"operation targets another instance", func(h *captureHarness) (context.Context, string) {
			h.records.op.Record.Target.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.vultr","environment_id":"staging","resource_kind":"instance","external_id":"other"}`)
			return context.Background(), h.descriptor.AdapterID
		}, "capture target does not match durable operation"},
		{"destination flows the wrong way", func(h *captureHarness) (context.Context, string) {
			h.request.Params.RequestContext.StreamTransfers = []capturestream.Reference{{TransferID: "t", Direction: "controller_to_adapter", MaximumBytes: 8, ExpiresAt: time.Now().Add(time.Minute)}}
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"destination without an attached writer", func(h *captureHarness) (context.Context, string) {
			h.request.Params.RequestContext.StreamTransfers = []capturestream.Reference{{TransferID: "t", Direction: "adapter_to_controller", MaximumBytes: 8, ExpiresAt: time.Now().Add(time.Minute)}}
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"destination outlives the invocation deadline", func(h *captureHarness) (context.Context, string) {
			h.request.Params.RequestContext.Deadline = time.Now().Add(time.Minute)
			h.request.Params.RequestContext.StreamTransfers = []capturestream.Reference{{TransferID: "t", Direction: "adapter_to_controller", MaximumBytes: 8, ExpiresAt: time.Now().Add(2 * time.Minute)}}
			h.request.Params.RequestContext.SetStreamDestinations(map[string]io.WriteCloser{"t": &closeBuffer{}})
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"instance in another environment", func(h *captureHarness) (context.Context, string) {
			h.setInstanceEnvironment("production")
			return context.Background(), h.descriptor.AdapterID
		}, "invalid capture instance"},
		{"staging unavailable", func(h *captureHarness) (context.Context, string) {
			h.staging = staging{s: h.sink, err: errors.New("disk full")}
			return context.Background(), h.descriptor.AdapterID
		}, "capture staging is unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newCaptureHarness(t)
			ctx, adapterID := test.setup(h)
			_, err := h.runtime().Invoke(ctx, adapterID, h.request)
			if err == nil || err.Error() != test.message {
				t.Fatalf("Invoke error = %v, want %q", err, test.message)
			}
			if h.called || h.sink.sealed {
				t.Fatalf("rejected capture reached the adapter (%v) or sealed (%v)", h.called, h.sink.sealed)
			}
		})
	}
}

func TestInvokeDiscardsStageWhenStreamCannotBeIssued(t *testing.T) {
	h := newCaptureHarness(t)
	h.broker, _ = capturestream.New(1)
	// Fill the broker's only pending slot.
	if _, _, err := h.broker.IssueTracked(h.scope, 8, time.Now().Add(time.Minute), time.Now().Add(time.Minute), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	_, err := h.runtime().Invoke(context.Background(), h.descriptor.AdapterID, h.request)
	if err == nil || err.Error() != "capture stream could not be issued" || !h.sink.discarded || h.called {
		t.Fatalf("Invoke error = %v, discarded = %v, called = %v", err, h.sink.discarded, h.called)
	}
}

func TestInvokeTreatsUnverifiedResultsAsUncertain(t *testing.T) {
	tests := []struct {
		name    string
		respond func(h *captureHarness, r adapter.InvokeRequest) (adapter.InvokeResponse, error)
	}{
		{"output fails its schema", func(h *captureHarness, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, map[string]any{"outcome": "captured"})
		}},
		{"captured output reported as unknown", func(h *captureHarness, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			response, err := h.captured(r)
			response.Result.Outcome = adapter.OutcomeUnknown
			return response, err
		}},
		{"unknown outcome with a different external reference", func(h *captureHarness, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			output := h.output()
			delete(output, "size_bytes")
			delete(output, "checksum")
			delete(output, "stream_completed")
			output["outcome"] = "outcome_unknown"
			output["reason_code"] = "capture_rejected"
			output["reconciliation_ref"] = map[string]any{"adapter_id": h.descriptor.AdapterID, "environment_id": h.scope.EnvironmentID, "resource_kind": "operation", "external_id": "capture-unknown"}
			response, err := h.reply(r, adapter.OutcomeCompleted, output)
			response.Result.ExternalOperationRef = json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"operation","external_id":"other"}`)
			return response, err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newCaptureHarness(t)
			h.respond = func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) { return test.respond(h, r) }
			_, err := h.runtime().Invoke(context.Background(), h.descriptor.AdapterID, h.request)
			var invocation *adapter.InvocationError
			if !errors.As(err, &invocation) || !invocation.MayHaveSideEffects {
				t.Fatalf("Invoke error = %v, want an uncertain capture result", err)
			}
			if h.sink.sealed || !h.sink.discarded {
				t.Fatalf("sealed = %v, discarded = %v; want an unsealed, discarded stage", h.sink.sealed, h.sink.discarded)
			}
		})
	}
}

func TestInvokeReturnsAdapterErrorAndDiscardsStage(t *testing.T) {
	h := newCaptureHarness(t)
	h.respond = func(adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		return adapter.InvokeResponse{}, errors.New("adapter exploded")
	}
	// The registry redacts a streamed adapter's error; Invoke returns it as is.
	_, err := h.runtime().Invoke(context.Background(), h.descriptor.AdapterID, h.request)
	if err == nil || err.Error() != "streamed adapter invocation failed" || !h.sink.discarded || h.sink.sealed {
		t.Fatalf("Invoke error = %v, discarded = %v, sealed = %v", err, h.sink.discarded, h.sink.sealed)
	}
}

func TestInvokePassesOtherCapabilitiesThrough(t *testing.T) {
	h := newCaptureHarness(t)
	h.request.Params.Capability = "game.unadvertised"
	// No durable operation exists, so any capture check would fail first with
	// "capture operation is unavailable". The registry's own error shows the
	// request went straight to it.
	h.records.opErr = errors.New("not found")
	_, err := h.runtime().Invoke(context.Background(), h.descriptor.AdapterID, h.request)
	if err == nil || err.Error() != "adapter does not advertise capability" || strings.Contains(err.Error(), "capture") {
		t.Fatalf("Invoke error = %v, want the registry's capability error", err)
	}
}

// recoverableHarness returns a harness whose operation is paused at an
// unresolved capture step with sealed evidence for "abc".
func recoverableHarness(t *testing.T) *captureHarness {
	h := newCaptureHarness(t)
	h.records.op.Record.Status = protocolstate.OperationReconciling
	h.step().Status = protocolstate.StepOutcomeUnknown
	output, _ := json.Marshal(h.output())
	h.staging = fixedRecovery{staging: staging{s: h.sink}, output: output,
		receipt: capturestream.Receipt{Scope: h.scope, SizeBytes: 3, SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}}
	return h
}

func TestRecoverSealedCaptureBaseline(t *testing.T) {
	h := recoverableHarness(t)
	recovered, err := h.runtime().RecoverSealedCapture(context.Background(), h.scope.OperationID, h.scope.StepID)
	if err != nil {
		t.Fatal(err)
	}
	recovered.Data.Close()
}

func TestRecoverSealedCaptureRejections(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*captureHarness) (context.Context, string, string)
		message string
	}{
		{"empty step ID", func(h *captureHarness) (context.Context, string, string) {
			return context.Background(), h.scope.OperationID, ""
		}, "invalid capture recovery request"},
		{"staging cannot recover", func(h *captureHarness) (context.Context, string, string) {
			h.staging = openOnlyStaging{s: h.sink}
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "capture staging does not support recovery"},
		{"operation unavailable", func(h *captureHarness) (context.Context, string, string) {
			h.records.opErr = errors.New("not found")
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "capture operation is unavailable"},
		{"operation still running", func(h *captureHarness) (context.Context, string, string) {
			h.records.op.Record.Status = protocolstate.OperationRunning
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "capture operation is not at the exact recoverable step"},
		{"step already resolved", func(h *captureHarness) (context.Context, string, string) {
			h.step().Output = json.RawMessage(`{}`)
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "capture step is not unresolved"},
		{"adapter not registered", func(h *captureHarness) (context.Context, string, string) {
			h.step().AdapterID = "game.unregistered"
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "capture adapter is unavailable"},
		{"capability version changed", func(h *captureHarness) (context.Context, string, string) {
			h.step().CapabilityVersion = "9.9.9"
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "recorded capture capability or input is incompatible"},
		{"operation targets another instance", func(h *captureHarness) (context.Context, string, string) {
			h.records.op.Record.Target.InstanceRef = json.RawMessage(`{"adapter_id":"hosting.vultr","environment_id":"staging","resource_kind":"instance","external_id":"other"}`)
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "recorded capture target differs from its operation"},
		{"instance in another environment", func(h *captureHarness) (context.Context, string, string) {
			h.setInstanceEnvironment("production")
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "recorded capture instance is invalid"},
		{"lock held by another operation", func(h *captureHarness) (context.Context, string, string) {
			h.records.lock.OperationID = "other"
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "capture recovery requires the exact durable server lock"},
		{"no sealed evidence", func(h *captureHarness) (context.Context, string, string) {
			recovery := h.staging.(fixedRecovery)
			recovery.err = errors.New("missing")
			h.staging = recovery
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "sealed capture evidence is unavailable or invalid"},
		{"receipt for another scope", func(h *captureHarness) (context.Context, string, string) {
			recovery := h.staging.(fixedRecovery)
			recovery.receipt.Scope.CaptureID = "other"
			h.staging = recovery
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "sealed capture response is missing or invalid"},
		{"sealed size differs from the receipt", func(h *captureHarness) (context.Context, string, string) {
			recovery := h.staging.(fixedRecovery)
			recovery.receipt.SizeBytes = 4
			h.staging = recovery
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "sealed capture response does not match measured stream evidence"},
		{"sealed output for another data set", func(h *captureHarness) (context.Context, string, string) {
			recovery := h.staging.(fixedRecovery)
			output := h.output()
			output["release_id"] = "paper-other"
			recovery.output, _ = json.Marshal(output)
			h.staging = recovery
			return context.Background(), h.scope.OperationID, h.scope.StepID
		}, "sealed capture response does not match the exact durable request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := recoverableHarness(t)
			ctx, operationID, stepID := test.setup(h)
			_, err := h.runtime().RecoverSealedCapture(ctx, operationID, stepID)
			if err == nil || err.Error() != test.message {
				t.Fatalf("RecoverSealedCapture error = %v, want %q", err, test.message)
			}
		})
	}
}
