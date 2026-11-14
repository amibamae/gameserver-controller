package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// These tests call ServerRestoreWorkflow.Reconcile directly on a restore call
// whose outcome is unknown and pin the exact Resolution (state, output bytes,
// external reference and error bytes) for each inspection result.

// restoreR3ApplyIndex is the restore call's position in the planned steps.
const restoreR3ApplyIndex = 3

type restoreR3ReconcileCase struct {
	fixture  *restoreR3Fixture
	output   map[string]any
	response func(adapter.InvokeRequest, json.RawMessage) (adapter.InvokeResponse, error)
}

// newRestoreR3Reconcile plans a restore whose restore call was dispatched with
// an exact execution reference and an unknown outcome. The fake adapter
// answers the inspection with output unless a test replaces response.
func newRestoreR3Reconcile(t *testing.T) *restoreR3ReconcileCase {
	t.Helper()
	fixture := newPlannedRestoreR3Fixture(t, restoreR3Replace)
	restoreR3CompleteThrough(t, fixture, restoreApplyStep)
	apply := fixture.step(restoreApplyStep)
	started := time.Now().UTC()
	apply.Status = protocolstate.StepOutcomeUnknown
	apply.Attempt = 2
	apply.StartedAt = &started
	apply.ExternalOperationRef = json.RawMessage(restoreR3Execution)
	test := &restoreR3ReconcileCase{fixture: fixture, output: restoreR3GameOutput(t, fixture)}
	test.response = func(request adapter.InvokeRequest, output json.RawMessage) (adapter.InvokeResponse, error) {
		return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: output}}, nil
	}
	return test
}

func (test *restoreR3ReconcileCase) run(t *testing.T) (Resolution, json.RawMessage) {
	t.Helper()
	output := restoreR3JSON(t, test.output)
	test.fixture.adapters.reply = func(request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		return test.response(request, output)
	}
	resolution, err := test.fixture.workflow.Reconcile(context.Background(), test.fixture.operation, test.fixture.operation.Steps[restoreR3ApplyIndex])
	if err != nil {
		t.Fatalf("Reconcile returned error %v; it must always resolve", err)
	}
	return resolution, output
}

func restoreR3SameResolution(t *testing.T, got, want Resolution) {
	t.Helper()
	if got.State != want.State || !bytes.Equal(got.Output, want.Output) || !bytes.Equal(got.ExternalOperationRef, want.ExternalOperationRef) || !bytes.Equal(got.Error, want.Error) {
		t.Fatalf("Resolution = {%s output=%s ref=%s error=%s}; want {%s output=%s ref=%s error=%s}",
			got.State, got.Output, got.ExternalOperationRef, got.Error, want.State, want.Output, want.ExternalOperationRef, want.Error)
	}
}

func restoreR3EditInput(t *testing.T, test *restoreR3ReconcileCase, field string, value any) {
	t.Helper()
	apply := test.fixture.step(restoreApplyStep)
	var input map[string]any
	if err := json.Unmarshal(apply.Input, &input); err != nil {
		t.Fatal(err)
	}
	input[field] = value
	apply.Input = restoreR3JSON(t, input)
}

// restoreR3InspectionRequest is the comparable part of an inspection request.
type restoreR3InspectionRequest struct {
	JSONRPC, ID, Method, Capability, ProtocolVersion, Input string
	RequestID, CorrelationID, OperationID, StepID           string
	ServerID, EnvironmentID, IdempotencyKey                 string
	StreamTransfers                                         int
}

func restoreR3DescribeRequest(request adapter.InvokeRequest) restoreR3InspectionRequest {
	requestContext := request.Params.RequestContext
	return restoreR3InspectionRequest{JSONRPC: request.JSONRPC, ID: request.ID, Method: request.Method, Capability: request.Params.Capability,
		ProtocolVersion: request.Params.ProtocolVersion, Input: string(request.Params.Input), RequestID: requestContext.RequestID,
		CorrelationID: requestContext.CorrelationID, OperationID: requestContext.OperationID, StepID: requestContext.StepID, ServerID: requestContext.ServerID,
		EnvironmentID: requestContext.EnvironmentID, IdempotencyKey: requestContext.IdempotencyKey, StreamTransfers: len(requestContext.StreamTransfers)}
}

// restoreR3OnlyCall returns the single adapter call the fixture recorded.
func restoreR3OnlyCall(t *testing.T, test *restoreR3ReconcileCase) adapter.InvokeRequest {
	t.Helper()
	if len(test.fixture.adapters.calls) != 1 {
		t.Fatalf("adapter calls = %d; want 1", len(test.fixture.adapters.calls))
	}
	return test.fixture.adapters.calls[0]
}

func restoreR3Completed(output json.RawMessage) Resolution {
	return Resolution{State: ResolutionCompleted, Output: output, ExternalOperationRef: json.RawMessage(restoreR3Execution)}
}

// TestServerRestoreR3ReconcileConfirmed pins the completed resolution and the
// exact inspection request it is based on.
func TestServerRestoreR3ReconcileConfirmed(t *testing.T) {
	test := newRestoreR3Reconcile(t)
	before := time.Now().UTC()
	resolution, output := test.run(t)
	restoreR3SameResolution(t, resolution, restoreR3Completed(output))
	request := restoreR3OnlyCall(t, test)
	wantID := startReconcileRequestID("restore-op", restoreApplyStep, 2)
	want := restoreR3InspectionRequest{JSONRPC: "2.0", ID: wantID, Method: "adapter.invoke", Capability: "game.restore.inspect",
		ProtocolVersion: test.fixture.operation.ProtocolVersion, RequestID: wantID, CorrelationID: "correlation-1", OperationID: "restore-op",
		StepID: restoreApplyStep, ServerID: "server-1", EnvironmentID: "staging",
		Input: `{"backup_id":"backup-1","restore_execution_ref":` + restoreR3Execution + `,"target":{"environment_id":"staging","instance_ref":` + restoreR3Instance +
			`,"server_id":"server-1"},"target_data_generation_id":"restore-restore-op"}`}
	if got := restoreR3DescribeRequest(request); got != want {
		t.Fatalf("inspection request = %+v; want %+v", got, want)
	}
	deadline := request.Params.RequestContext.Deadline
	if deadline.Before(before.Add(15*time.Second)) || deadline.After(time.Now().UTC().Add(15*time.Second)) {
		t.Fatalf("inspection deadline %s is not 15s after the call", deadline)
	}
}

func TestServerRestoreR3ReconcileCorrelationDefaultsToOperation(t *testing.T) {
	test := newRestoreR3Reconcile(t)
	test.fixture.operation.CorrelationID = ""
	test.run(t)
	if got := restoreR3OnlyCall(t, test).Params.RequestContext.CorrelationID; got != "restore-op" {
		t.Fatalf("correlation ID without one recorded = %q; want the operation ID", got)
	}
}

// TestServerRestoreR3ReconcileConfirmedVariants pins completion when the
// recorded output already names the same execution, and that a resolved
// input takes precedence over the planned input.
func TestServerRestoreR3ReconcileConfirmedVariants(t *testing.T) {
	cases := map[string]func(*testing.T, *restoreR3ReconcileCase){
		"recorded output names the same execution": func(t *testing.T, test *restoreR3ReconcileCase) {
			test.fixture.step(restoreApplyStep).Output = restoreR3JSON(t, map[string]any{"restore_execution_ref": json.RawMessage(restoreR3Execution)})
		},
		"resolved input wins over planned input": func(t *testing.T, test *restoreR3ReconcileCase) {
			apply := test.fixture.step(restoreApplyStep)
			apply.ResolvedInput = append(json.RawMessage(nil), apply.Input...)
			restoreR3EditInput(t, test, "target_data_generation_id", "ignored-when-resolved")
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			test := newRestoreR3Reconcile(t)
			prepare(t, test)
			resolution, output := test.run(t)
			restoreR3SameResolution(t, resolution, restoreR3Completed(output))
		})
	}
}

func TestServerRestoreR3ReconcileRejected(t *testing.T) {
	test := newRestoreR3Reconcile(t)
	test.output["outcome"] = "rejected"
	resolution, _ := test.run(t)
	restoreR3SameResolution(t, resolution, Resolution{State: ResolutionDefinitiveError, ExternalOperationRef: json.RawMessage(restoreR3Execution),
		Error: json.RawMessage(`{"category":"provider_failure","code":"core.restore.rejected","message":"The exact restore execution was confirmed rejected.","operation_id":"restore-op","recovery":{"action":"operator_review","operator_required":false},"retryable":false}`)})
}

// TestServerRestoreR3ReconcileUntrustedStep pins the bare needs-operator
// resolution, without any adapter call, for steps that are not an exact
// restore call.
func TestServerRestoreR3ReconcileUntrustedStep(t *testing.T) {
	otherAdapterRef := json.RawMessage(`{"adapter_id":"game.other","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-1"}`)
	otherExecution := json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-2"}`)
	cases := map[string]func(*restoreR3ReconcileCase){
		"wrong operation type": func(test *restoreR3ReconcileCase) { test.fixture.operation.OperationType = "server.start" },
		"wrong step":           func(test *restoreR3ReconcileCase) { test.fixture.step(restoreApplyStep).StepID = restoreInspectStep },
		"wrong capability": func(test *restoreR3ReconcileCase) {
			test.fixture.step(restoreApplyStep).CapabilityName = "game.restore.inspect"
		},
		"no execution reference": func(test *restoreR3ReconcileCase) { test.fixture.step(restoreApplyStep).ExternalOperationRef = nil },
		"execution reference for another adapter": func(test *restoreR3ReconcileCase) {
			test.fixture.step(restoreApplyStep).ExternalOperationRef = otherAdapterRef
		},
		"execution reference in another environment": func(test *restoreR3ReconcileCase) { test.fixture.operation.Target.EnvironmentID = "production" },
		"recorded output names another execution": func(test *restoreR3ReconcileCase) {
			test.fixture.step(restoreApplyStep).Output = json.RawMessage(`{"restore_execution_ref":` + string(otherExecution) + `}`)
		},
		"recorded output reference invalid": func(test *restoreR3ReconcileCase) {
			test.fixture.step(restoreApplyStep).Output = json.RawMessage(`{"restore_execution_ref":` + string(otherAdapterRef) + `}`)
		},
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			test := newRestoreR3Reconcile(t)
			prepare(test)
			resolution, _ := test.run(t)
			restoreR3SameResolution(t, resolution, Resolution{State: ResolutionNeedsOperator})
			if len(test.fixture.adapters.calls) != 0 {
				t.Fatalf("untrusted step reached the adapter %d times", len(test.fixture.adapters.calls))
			}
		})
	}
	test := newRestoreR3Reconcile(t)
	var missing context.Context
	if resolution, err := test.fixture.workflow.Reconcile(missing, test.fixture.operation, *test.fixture.step(restoreApplyStep)); err != nil {
		t.Fatal(err)
	} else {
		restoreR3SameResolution(t, resolution, Resolution{State: ResolutionNeedsOperator})
	}
	var nilWorkflow *ServerRestoreWorkflow
	if resolution, err := nilWorkflow.Reconcile(context.Background(), test.fixture.operation, *test.fixture.step(restoreApplyStep)); err != nil {
		t.Fatal(err)
	} else {
		restoreR3SameResolution(t, resolution, Resolution{State: ResolutionNeedsOperator})
	}
}

// TestServerRestoreR3ReconcileNeedsOperator pins the needs-operator resolution
// that keeps the exact execution reference, and whether the adapter was asked.
func TestServerRestoreR3ReconcileNeedsOperator(t *testing.T) {
	stale := time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano)
	cases := []struct {
		name      string
		prepare   func(*testing.T, *restoreR3ReconcileCase)
		wantCalls int
	}{
		{"inspect capability missing", func(_ *testing.T, test *restoreR3ReconcileCase) {
			game := test.fixture.adapters.descriptors["game.paper"]
			game.Capabilities = game.Capabilities[:2]
			test.fixture.adapters.descriptors["game.paper"] = game
		}, 0},
		{"planned input not an object", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.fixture.step(restoreApplyStep).ResolvedInput = json.RawMessage(`[]`)
		}, 0},
		{"source backup not an object", func(t *testing.T, test *restoreR3ReconcileCase) {
			restoreR3EditInput(t, test, "source_backup", "backup-1")
		}, 0},
		{"server missing", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.fixture.servers.record.Record.ServerID = "other"
		}, 0},
		{"planned target is another instance", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.fixture.server().InstanceRef = json.RawMessage(restoreR3OtherInstance)
		}, 0},
		{"planned target generation differs", func(t *testing.T, test *restoreR3ReconcileCase) {
			restoreR3EditInput(t, test, "target_data_generation_id", "generation-source")
		}, 0},
		{"source backup has no ID", func(t *testing.T, test *restoreR3ReconcileCase) {
			restoreR3EditInput(t, test, "source_backup", map[string]any{"operation_data": map[string]any{"environment_id": "staging", "data_generation_id": "generation-source"}})
		}, 0},
		{"source backup in another environment", func(t *testing.T, test *restoreR3ReconcileCase) {
			restoreR3EditInput(t, test, "source_backup", map[string]any{"operation_data": map[string]any{"backup_id": "backup-1", "environment_id": "production", "data_generation_id": "generation-source"}})
		}, 0},
		{"source backup has no generation", func(t *testing.T, test *restoreR3ReconcileCase) {
			restoreR3EditInput(t, test, "source_backup", map[string]any{"operation_data": map[string]any{"backup_id": "backup-1", "environment_id": "staging"}})
		}, 0},
		{"adapter call fails", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.response = func(adapter.InvokeRequest, json.RawMessage) (adapter.InvokeResponse, error) {
				return adapter.InvokeResponse{}, errors.New("unreachable")
			}
		}, 1},
		{"response not JSON-RPC 2.0", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.response = func(request adapter.InvokeRequest, output json.RawMessage) (adapter.InvokeResponse, error) {
				return adapter.InvokeResponse{JSONRPC: "1.0", ID: request.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: output}}, nil
			}
		}, 1},
		{"response for another request", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.response = func(_ adapter.InvokeRequest, output json.RawMessage) (adapter.InvokeResponse, error) {
				return adapter.InvokeResponse{JSONRPC: "2.0", ID: "other", Result: adapter.InvokeResult{Outcome: adapter.OutcomeCompleted, Output: output}}, nil
			}
		}, 1},
		{"inspection not completed", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.response = func(request adapter.InvokeRequest, output json.RawMessage) (adapter.InvokeResponse, error) {
				return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{Outcome: adapter.OutcomeAccepted, Output: output}}, nil
			}
		}, 1},
		{"output fails schema", func(_ *testing.T, test *restoreR3ReconcileCase) { delete(test.output, "observed_at") }, 1},
		{"output for another backup", func(_ *testing.T, test *restoreR3ReconcileCase) { test.output["backup_id"] = "backup-2" }, 1},
		{"output in another environment", func(_ *testing.T, test *restoreR3ReconcileCase) { test.output["environment_id"] = "production" }, 1},
		{"output from another source generation", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.output["source_data_generation_id"] = "generation-other"
		}, 1},
		{"output to another target generation", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.output["target_data_generation_id"] = "generation-other"
		}, 1},
		{"output for another target", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.output["target"] = map[string]any{"server_id": "server-2", "environment_id": "staging", "instance_ref": json.RawMessage(restoreR3Instance)}
		}, 1},
		{"output for another execution", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.output["restore_execution_ref"] = json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"restore-execution","external_id":"restore-2"}`)
		}, 1},
		{"output observation stale", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.output["observed_at"] = stale
			test.output["validation_checks"] = []any{map[string]any{"check_id": "world.loaded", "result": "pass", "observed_at": stale}}
		}, 1},
		{"validation check stale", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.output["validation_checks"] = []any{map[string]any{"check_id": "world.loaded", "result": "pass", "observed_at": stale}}
		}, 1},
		{"restored without required check", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.output["validation_checks"] = []any{map[string]any{"check_id": "world.other", "result": "pass", "observed_at": restoreR3Now()}}
		}, 1},
		{"outcome still unknown", func(_ *testing.T, test *restoreR3ReconcileCase) {
			test.output["outcome"] = "outcome_unknown"
		}, 1},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			reconcile := newRestoreR3Reconcile(t)
			test.prepare(t, reconcile)
			resolution, _ := reconcile.run(t)
			restoreR3SameResolution(t, resolution, Resolution{State: ResolutionNeedsOperator, ExternalOperationRef: json.RawMessage(restoreR3Execution)})
			if len(reconcile.fixture.adapters.calls) != test.wantCalls {
				t.Fatalf("adapter calls = %d; want %d", len(reconcile.fixture.adapters.calls), test.wantCalls)
			}
		})
	}
}
