package captureinvoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type records struct {
	op   store.StoredOperation
	lock store.ResourceLock
}

func (r *records) GetOperation(string) (store.StoredOperation, error) { return r.op, nil }
func (r *records) GetResourceLock(string) (store.ResourceLock, error) { return r.lock, nil }

type stage struct {
	bytes.Buffer
	sealed, discarded, failSeal, failDiscard bool
	receipt                                  capturestream.Receipt
	output                                   []byte
}

func (s *stage) Seal(_ context.Context, r capturestream.Receipt, output []byte) error {
	if s.failSeal {
		return errors.New("seal failed")
	}
	s.sealed = true
	s.receipt = r
	s.output = append([]byte(nil), output...)
	return nil
}
func (s *stage) Discard() error {
	s.discarded = true
	if s.failDiscard {
		return errors.New("discard failed")
	}
	return nil
}

type staging struct {
	s   *stage
	err error
}

func (s staging) Open(context.Context, capturestream.Scope) (Stage, error) { return s.s, s.err }
func (s staging) OpenVerifiedCapture(_ context.Context, scope capturestream.Scope) (io.ReadCloser, capturestream.Receipt, json.RawMessage, error) {
	if s.s == nil || s.s.receipt.Scope != scope || !s.s.sealed {
		return nil, capturestream.Receipt{}, nil, errors.New("missing fake recovery evidence")
	}
	return io.NopCloser(bytes.NewReader(s.s.Bytes())), s.s.receipt, append(json.RawMessage(nil), s.s.output...), nil
}

type implementation struct {
	d      adapter.Descriptor
	invoke func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
}

func (a implementation) Descriptor() adapter.Descriptor { return a.d }
func (a implementation) Invoke(_ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return a.invoke(r)
}

type closeBuffer struct{ bytes.Buffer }

func (*closeBuffer) Close() error { return nil }
func example(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile("../../protocol/examples/" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureDispatchUsesDurableScopeAndIndependentEvidence(t *testing.T) {
	for _, scenario := range []string{"success", "success-external-destination", "digest", "size", "binding", "incomplete", "no-stream", "rejected-empty", "rejected-bytes", "unknown", "seal-error", "changed-input", "missing-lock", "wrong-version", "cleanup-error", "unknown-reference", "existing-stage"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newCaptureDispatchFixture(t, scenario)
			response, err := fixture.runtime.Invoke(context.Background(), fixture.scope.AdapterID, fixture.request)
			fixture.assertDispatch(t, scenario, err)
			fixture.assertEvidence(t, scenario)
			if scenario == "success" || scenario == "success-external-destination" {
				fixture.assertRestartRecovery(t)
			}
			assertCaptureDispatchOutcome(t, scenario, response, err)
			wantTransfers := 0
			if scenario == "success-external-destination" {
				wantTransfers = 1
			}
			if len(fixture.request.Params.RequestContext.StreamTransfers) != wantTransfers {
				t.Fatal("transient handle mutated caller request")
			}
		})
	}
}

type captureDispatchFixture struct {
	request  adapter.InvokeRequest
	scope    capturestream.Scope
	source   *records
	sink     *stage
	external *closeBuffer
	runtime  *Runtime
	called   bool
}

func newCaptureDispatchFixture(t *testing.T, scenario string) *captureDispatchFixture {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := capturestream.New(2)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor adapter.Descriptor
	example(t, "adapter-descriptor.paper.json", &descriptor)
	var request adapter.InvokeRequest
	example(t, "game-backup-capture-invocation.json", &request)
	request.Params.RequestContext.StreamTransfers = nil
	request.Params.RequestContext.Deadline = time.Now().Add(2 * time.Minute)
	external := &closeBuffer{}
	if scenario == "success-external-destination" {
		request.Params.RequestContext.StreamTransfers = []capturestream.Reference{{
			TransferID: "workflow-transfer", Direction: "adapter_to_controller",
			MaximumBytes: 8, ExpiresAt: time.Now().Add(time.Minute),
		}}
		request.Params.RequestContext.SetStreamDestinations(map[string]io.WriteCloser{"workflow-transfer": external})
	}
	var in capture
	if err = json.Unmarshal(request.Params.Input, &in); err != nil {
		t.Fatal(err)
	}
	scope := capturestream.Scope{
		OperationID: request.Params.RequestContext.OperationID, StepID: request.Params.RequestContext.StepID,
		AdapterID: descriptor.AdapterID, ServerID: in.Target.ServerID, EnvironmentID: in.Target.EnvironmentID,
		InstanceAdapterID: "hosting.vultr", InstanceID: "instance-demo-01",
		DataGenerationID: in.GenerationID, DataSetID: in.DataSetID, CaptureID: in.CaptureID,
	}
	source := captureDispatchRecords(scope, descriptor, schemas.ProtocolVersion(), in.Target.InstanceRef, request.Params.Input)
	alterCaptureDispatchRecords(source, scenario)
	sink := &stage{failSeal: scenario == "seal-error", failDiscard: scenario == "cleanup-error"}
	fixture := &captureDispatchFixture{request: request, scope: scope, source: source, sink: sink, external: external}
	adapterImpl := implementation{d: descriptor, invoke: func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		fixture.called = true
		return captureDispatchResponse(t, scenario, scope, broker, r)
	}}
	if err = registry.Register(adapterImpl); err != nil {
		t.Fatal(err)
	}
	var stageErr error
	if scenario == "existing-stage" {
		stageErr = ErrStagingNeedsReconciliation
	}
	runtime, err := New(registry, schemas, source, broker, staging{s: sink, err: stageErr}, 10)
	if err != nil {
		t.Fatal(err)
	}
	fixture.runtime = runtime
	return fixture
}

func captureDispatchRecords(scope capturestream.Scope, descriptor adapter.Descriptor, protocolVersion string, instance, input json.RawMessage) *records {
	version := ""
	for _, capability := range descriptor.Capabilities {
		if capability.Name == "game.backup.capture" {
			version = capability.Version
		}
	}
	stepID := scope.StepID
	record := store.OperationRecord{
		OperationID: scope.OperationID, OperationType: "server.backup", ProtocolVersion: protocolVersion,
		Status: protocolstate.OperationRunning, BackupInitialLifecycleState: protocolstate.ServerStopped,
		CurrentStepID: &stepID,
		Target:        store.OperationTarget{ServerID: scope.ServerID, EnvironmentID: scope.EnvironmentID, InstanceRef: instance},
		Steps: []store.OperationStep{{
			StepID: stepID, Status: protocolstate.StepRunning, Attempt: 1, AdapterID: scope.AdapterID,
			CapabilityName: "game.backup.capture", CapabilityVersion: version, Input: append(json.RawMessage(nil), input...),
		}},
	}
	return &records{
		op:   store.StoredOperation{Revision: 3, Record: record},
		lock: store.ResourceLock{Scope: store.LogicalServerLockScope(scope.EnvironmentID, scope.ServerID), OperationID: scope.OperationID},
	}
}

func alterCaptureDispatchRecords(source *records, scenario string) {
	switch scenario {
	case "changed-input":
		source.op.Record.Steps[0].Input = json.RawMessage(`{}`)
	case "missing-lock":
		source.lock.OperationID = "other"
	case "wrong-version":
		source.op.Record.Steps[0].CapabilityVersion = "wrong"
	}
}

func captureDispatchResponse(t *testing.T, scenario string, scope capturestream.Scope, broker *capturestream.Broker, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	t.Helper()
	var output map[string]any
	example(t, "game-backup-capture-output.json", &output)
	output["capture_id"] = scope.CaptureID
	if !slices.Contains([]string{"no-stream", "rejected-empty", "unknown", "cleanup-error", "unknown-reference"}, scenario) {
		if err := writeCaptureDispatchStream(broker, r.Params.RequestContext.StreamTransfers[0].TransferID, scope, scenario != "incomplete"); err != nil {
			return adapter.InvokeResponse{}, err
		}
	}
	output["size_bytes"] = 3
	output["checksum"] = map[string]any{"algorithm": "sha256", "digest": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}
	if scenario == "digest" {
		output["checksum"] = map[string]any{"algorithm": "sha256", "digest": strings.Repeat("a", 64)}
	}
	if scenario == "size" {
		output["size_bytes"] = 4
	}
	if scenario == "binding" {
		output["capture_id"] = "different-capture"
	}
	setCaptureDispatchOutcome(output, scenario, scope)
	encoded, err := json.Marshal(output)
	if err != nil {
		return adapter.InvokeResponse{}, err
	}
	return adapter.InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: encoded, ObservedAt: time.Now()}}, nil
}

func setCaptureDispatchOutcome(output map[string]any, scenario string, scope capturestream.Scope) {
	if !slices.Contains([]string{"rejected-empty", "rejected-bytes", "unknown", "cleanup-error", "unknown-reference"}, scenario) {
		return
	}
	delete(output, "size_bytes")
	delete(output, "checksum")
	delete(output, "stream_completed")
	output["outcome"] = "rejected"
	output["reason_code"] = "capture_rejected"
	if scenario == "unknown" || scenario == "unknown-reference" {
		output["outcome"] = "outcome_unknown"
		output["reconciliation_ref"] = map[string]any{"adapter_id": scope.AdapterID, "environment_id": scope.EnvironmentID, "resource_kind": "operation", "external_id": "capture-unknown"}
		if scenario == "unknown-reference" {
			output["reconciliation_ref"].(map[string]any)["environment_id"] = "production"
		}
	}
}

func writeCaptureDispatchStream(broker *capturestream.Broker, transferID string, scope capturestream.Scope, complete bool) error {
	writer, err := broker.Claim(context.Background(), transferID, scope, nil)
	if err != nil {
		return err
	}
	if _, err = writer.Write([]byte("abc")); err != nil {
		return err
	}
	if complete {
		if _, err = writer.Complete(); err != nil {
			return err
		}
	}
	return nil
}

func captureDispatchPredispatch(scenario string) bool {
	return slices.Contains([]string{"changed-input", "missing-lock", "wrong-version", "existing-stage"}, scenario)
}

func (f *captureDispatchFixture) assertDispatch(t *testing.T, scenario string, err error) {
	t.Helper()
	success := scenario == "success" || scenario == "success-external-destination"
	wantNoError := success || scenario == "rejected-empty" || scenario == "unknown"
	predispatch := captureDispatchPredispatch(scenario)
	if wantNoError != (err == nil) {
		t.Fatalf("unexpected error=%v", err)
	}
	if f.sink.sealed != success {
		t.Fatalf("sealed=%v", f.sink.sealed)
	}
	if !predispatch && f.sink.discarded == success {
		t.Fatalf("discarded=%v", f.sink.discarded)
	}
	if f.called == predispatch {
		t.Fatalf("adapter called=%v", f.called)
	}
}

func (f *captureDispatchFixture) assertEvidence(t *testing.T, scenario string) {
	t.Helper()
	external := scenario == "success-external-destination"
	success := scenario == "success" || external
	if success && (f.sink.String() != "abc" || f.sink.receipt.Scope != f.scope) {
		t.Fatal("incorrect staged evidence")
	}
	if external && f.external.String() != "abc" {
		t.Fatalf("workflow destination received %q, want captured bytes", f.external.String())
	}
	if success && !json.Valid(f.sink.output) {
		t.Fatal("original validated adapter response was not staged")
	}
}

func (f *captureDispatchFixture) assertRestartRecovery(t *testing.T) {
	t.Helper()
	source, scope, sink, runtime := f.source, f.scope, f.sink, f.runtime
	stepID := scope.StepID
	// Simulate a process restart after the stage was durably sealed but
	// before the engine persisted the completed adapter response.
	source.op.Record.Status = protocolstate.OperationReconciling
	source.op.Record.Steps[0].Status = protocolstate.StepOutcomeUnknown
	source.op.Record.CurrentStepID = &stepID
	recovered, recoverErr := runtime.RecoverSealedCapture(context.Background(), scope.OperationID, scope.StepID)
	if recoverErr != nil {
		t.Fatal(recoverErr)
	}
	recoveredBytes, readErr := io.ReadAll(recovered.Data)
	closeErr := recovered.Data.Close()
	sameData := readErr == nil && closeErr == nil && string(recoveredBytes) == "abc"
	sameEvidence := string(recovered.Output) == string(sink.output) && recovered.Receipt == sink.receipt
	if !sameData || !sameEvidence {
		t.Fatal("restart recovery did not return the exact staged capture evidence")
	}
	if _, recoverErr = runtime.RecoverSealedCapture(context.Background(), scope.OperationID, "other-step"); recoverErr == nil {
		t.Fatal("recovery accepted a nonmatching step")
	}
}

func assertCaptureDispatchOutcome(t *testing.T, scenario string, response adapter.InvokeResponse, err error) {
	t.Helper()
	if scenario == "unknown" && (response.Result.Outcome != adapter.OutcomeUnknown || len(response.Result.ExternalOperationRef) == 0) {
		t.Fatal("capability uncertainty was not normalized")
	}
	if err != nil && (!captureDispatchPredispatch(scenario) || scenario == "existing-stage") {
		var uncertain *adapter.InvocationError
		if !errors.As(err, &uncertain) || !uncertain.MayHaveSideEffects {
			t.Fatal("failure could bypass reconciliation")
		}
	}
}
