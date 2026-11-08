package captureinvoke

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
}

func (s *stage) Seal(_ context.Context, r capturestream.Receipt) error {
	if s.failSeal {
		return errors.New("seal failed")
	}
	s.sealed = true
	s.receipt = r
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

type implementation struct {
	d      adapter.Descriptor
	invoke func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
}

func (a implementation) Descriptor() adapter.Descriptor { return a.d }
func (a implementation) Invoke(_ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return a.invoke(r)
}
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
	for _, scenario := range []string{"success", "digest", "size", "binding", "incomplete", "no-stream", "rejected-empty", "rejected-bytes", "unknown", "seal-error", "changed-input", "missing-lock", "wrong-version", "cleanup-error", "unknown-reference", "existing-stage"} {
		t.Run(scenario, func(t *testing.T) {
			schemas, err := schemaregistry.Load()
			if err != nil {
				t.Fatal(err)
			}
			registry, _ := adapter.NewRegistry(schemas)
			broker, _ := capturestream.New(2)
			var descriptor adapter.Descriptor
			example(t, "adapter-descriptor.paper.json", &descriptor)
			var request adapter.InvokeRequest
			example(t, "game-backup-capture-invocation.json", &request)
			request.Params.RequestContext.StreamTransfers = nil
			request.Params.RequestContext.Deadline = time.Now().Add(2 * time.Minute)
			var in capture
			if err = json.Unmarshal(request.Params.Input, &in); err != nil {
				t.Fatal(err)
			}
			scope := capturestream.Scope{OperationID: request.Params.RequestContext.OperationID, StepID: request.Params.RequestContext.StepID, AdapterID: descriptor.AdapterID, ServerID: in.Target.ServerID, EnvironmentID: in.Target.EnvironmentID, InstanceAdapterID: "hosting.vultr", InstanceID: "instance-demo-01", DataGenerationID: in.GenerationID, DataSetID: in.DataSetID, CaptureID: in.CaptureID}
			version := ""
			for _, c := range descriptor.Capabilities {
				if c.Name == "game.backup.capture" {
					version = c.Version
				}
			}
			stepID := scope.StepID
			source := &records{op: store.StoredOperation{Revision: 3, Record: store.OperationRecord{OperationID: scope.OperationID, OperationType: "server.backup", ProtocolVersion: schemas.ProtocolVersion(), Status: protocolstate.OperationRunning, BackupInitialLifecycleState: protocolstate.ServerStopped, CurrentStepID: &stepID, Target: store.OperationTarget{ServerID: scope.ServerID, EnvironmentID: scope.EnvironmentID, InstanceRef: in.Target.InstanceRef}, Steps: []store.OperationStep{{StepID: stepID, Status: protocolstate.StepRunning, Attempt: 1, AdapterID: descriptor.AdapterID, CapabilityName: "game.backup.capture", CapabilityVersion: version, Input: append(json.RawMessage(nil), request.Params.Input...)}}}}, lock: store.ResourceLock{Scope: store.LogicalServerLockScope(scope.EnvironmentID, scope.ServerID), OperationID: scope.OperationID}}
			if scenario == "changed-input" {
				source.op.Record.Steps[0].Input = json.RawMessage(`{}`)
			}
			if scenario == "missing-lock" {
				source.lock.OperationID = "other"
			}
			if scenario == "wrong-version" {
				source.op.Record.Steps[0].CapabilityVersion = "wrong"
			}
			sink := &stage{failSeal: scenario == "seal-error", failDiscard: scenario == "cleanup-error"}
			called := false
			adapterImpl := implementation{d: descriptor, invoke: func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
				called = true
				var output map[string]any
				example(t, "game-backup-capture-output.json", &output)
				output["capture_id"] = in.CaptureID
				if scenario != "no-stream" && scenario != "rejected-empty" && scenario != "unknown" && scenario != "cleanup-error" && scenario != "unknown-reference" {
					writer, err := broker.Claim(context.Background(), r.Params.RequestContext.StreamTransfers[0].TransferID, scope, nil)
					if err != nil {
						return adapter.InvokeResponse{}, err
					}
					if _, err = writer.Write([]byte("abc")); err != nil {
						return adapter.InvokeResponse{}, err
					}
					if scenario != "incomplete" {
						if _, err = writer.Complete(); err != nil {
							return adapter.InvokeResponse{}, err
						}
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
				if scenario == "rejected-empty" || scenario == "rejected-bytes" || scenario == "unknown" || scenario == "cleanup-error" || scenario == "unknown-reference" {
					delete(output, "size_bytes")
					delete(output, "checksum")
					delete(output, "stream_completed")
					output["outcome"] = "rejected"
					output["reason_code"] = "capture_rejected"
					if scenario == "unknown" || scenario == "unknown-reference" {
						output["outcome"] = "outcome_unknown"
						output["reconciliation_ref"] = map[string]any{"adapter_id": descriptor.AdapterID, "environment_id": scope.EnvironmentID, "resource_kind": "operation", "external_id": "capture-unknown"}
						if scenario == "unknown-reference" {
							output["reconciliation_ref"].(map[string]any)["environment_id"] = "production"
						}
					}
				}
				encoded, _ := json.Marshal(output)
				return adapter.InvokeResponse{JSONRPC: "2.0", ID: r.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: encoded, ObservedAt: time.Now()}}, nil
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
			response, err := runtime.Invoke(context.Background(), descriptor.AdapterID, request)
			success := scenario == "success"
			definitive := scenario == "rejected-empty"
			unknown := scenario == "unknown"
			predispatch := scenario == "changed-input" || scenario == "missing-lock" || scenario == "wrong-version" || scenario == "existing-stage"
			if (success || definitive || unknown) != (err == nil) {
				t.Fatalf("unexpected error=%v", err)
			}
			if sink.sealed != success {
				t.Fatalf("sealed=%v", sink.sealed)
			}
			if !predispatch && sink.discarded == success {
				t.Fatalf("discarded=%v", sink.discarded)
			}
			if called == predispatch {
				t.Fatalf("adapter called=%v", called)
			}
			if success && (sink.String() != "abc" || sink.receipt.Scope != scope) {
				t.Fatal("incorrect staged evidence")
			}
			if unknown && (response.Result.Outcome != adapter.OutcomeUnknown || len(response.Result.ExternalOperationRef) == 0) {
				t.Fatal("capability uncertainty was not normalized")
			}
			if err != nil && (!predispatch || scenario == "existing-stage") {
				var uncertain *adapter.InvocationError
				if !errors.As(err, &uncertain) || !uncertain.MayHaveSideEffects {
					t.Fatal("failure could bypass reconciliation")
				}
			}
			if len(request.Params.RequestContext.StreamTransfers) != 0 {
				t.Fatal("transient handle mutated caller request")
			}
		})
	}
}
