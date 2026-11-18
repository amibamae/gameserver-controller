package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

type startStopR3Reconciler func(context.Context, store.OperationRecord, store.OperationStep) (Resolution, error)

type startStopR3ReconcileCase struct {
	name       string
	step       string
	change     func(*startStopR3Fixture, *store.OperationRecord)
	stepChange func(*store.OperationStep)
	reply      func(adapter.InvokeRequest) (adapter.InvokeResponse, error)
	nilCtx     bool
	want       ResolutionState
	wantRef    bool
	wantOut    string
	wantError  string
	wantCalls  int
}

func startStopR3Reply(output string) func(adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return func(request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{
			Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(output),
		}}, nil
	}
}

func startStopR3EditReply(output string, edit func(*adapter.InvokeResponse)) func(adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return func(request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
		response, err := startStopR3Reply(output)(request)
		edit(&response)
		return response, err
	}
}

func startStopR3FailInvoke(adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	return adapter.InvokeResponse{}, errors.New("down")
}

// startStopR3ReconcileCommon lists the inspection failures both workflows
// resolve as needs-operator while keeping the step's external reference.
func startStopR3ReconcileCommon(unit func(string) string) []startStopR3ReconcileCase {
	running := unit("running")
	needs := func(name string, reply func(adapter.InvokeRequest) (adapter.InvokeResponse, error)) startStopR3ReconcileCase {
		return startStopR3ReconcileCase{name: name, reply: reply, want: ResolutionNeedsOperator, wantRef: true, wantCalls: 1}
	}
	return []startStopR3ReconcileCase{
		{name: "capability no longer advertised", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3DropCapability(f, "execution.fake", "execution.unit.inspect")
		}, want: ResolutionNeedsOperator, wantRef: true},
		{name: "capability version changed", change: func(f *startStopR3Fixture, _ *store.OperationRecord) {
			startStopR3EditCapability(f, "execution.fake", "execution.unit.inspect", func(c *adapter.Capability) { c.Version = "2" })
		}, want: ResolutionNeedsOperator, wantRef: true},
		needs("invoke fails", startStopR3FailInvoke),
		needs("wrong JSON-RPC version", startStopR3EditReply(running, func(r *adapter.InvokeResponse) { r.JSONRPC = "1.0" })),
		needs("wrong response ID", startStopR3EditReply(running, func(r *adapter.InvokeResponse) { r.ID = "other" })),
		needs("accepted outcome", startStopR3EditReply(running, func(r *adapter.InvokeResponse) { r.Result.Outcome = adapter.OutcomeAccepted })),
		needs("output rejected by schema", startStopR3Reply(`{"state":"running"}`)),
		needs("output for another unit", startStopR3Reply(startStopR3UnitOutput(startStopR3OtherUnit, "running"))),
		needs("unparseable observation time", startStopR3Reply(strings.Replace(running, startStopR3ObservedAt, "2026-11-13", 1))),
		needs("unknown state", startStopR3Reply(unit("unknown"))),
	}
}

func startStopR3RunReconcile(t *testing.T, f *startStopR3Fixture, operation store.OperationRecord, test startStopR3ReconcileCase, reconcile startStopR3Reconciler) {
	t.Helper()
	if test.change != nil {
		test.change(f, &operation)
	}
	step := operationStep(operation, test.step)
	if test.stepChange != nil {
		test.stepChange(&step)
	}
	f.adapters.reply = test.reply
	ctx := context.Background()
	if test.nilCtx {
		ctx = nil
	}
	resolution, err := reconcile(ctx, operation, step)
	if err != nil {
		t.Fatal(err)
	}
	startStopR3CheckResolution(t, resolution, test)
	if len(f.adapters.calls) != test.wantCalls {
		t.Fatalf("adapter calls = %d; want %d", len(f.adapters.calls), test.wantCalls)
	}
}

func startStopR3CheckResolution(t *testing.T, resolution Resolution, test startStopR3ReconcileCase) {
	t.Helper()
	if resolution.State != test.want {
		t.Fatalf("resolution = %#v; want %s", resolution, test.want)
	}
	wantRef := ""
	if test.wantRef {
		wantRef = startStopR3ExternalRef
	}
	if string(resolution.ExternalOperationRef) != wantRef {
		t.Fatalf("external ref = %q; want %q", resolution.ExternalOperationRef, wantRef)
	}
	if string(resolution.Output) != test.wantOut {
		t.Fatalf("output = %s; want %s", resolution.Output, test.wantOut)
	}
	if string(resolution.Error) != test.wantError {
		t.Fatalf("error = %s; want %s", resolution.Error, test.wantError)
	}
}

func startStopR3Ambiguous(step *store.OperationStep) {
	step.Status = protocolstate.StepRunning
	step.Attempt = 2
	step.Output = nil
	step.ExternalOperationRef = json.RawMessage(startStopR3ExternalRef)
}

// TestStartStopR3StartReconcile pins every server.start reconciliation outcome.
func TestStartStopR3StartReconcile(t *testing.T) {
	unit := func(state string) string { return startStopR3UnitOutput(startTestUnit, state) }
	verify := func(change func(*store.OperationStep)) func(*startStopR3Fixture, *store.OperationRecord) {
		return startStopR3Step(startVerifyRuntimeStep, change)
	}
	cases := []startStopR3ReconcileCase{
		{name: "nil context", nilCtx: true, want: ResolutionNeedsOperator},
		{name: "other step", step: startVerifyRuntimeStep, want: ResolutionNeedsOperator},
		{name: "other capability", stepChange: func(s *store.OperationStep) { s.CapabilityName = "execution.unit.stop" }, want: ResolutionNeedsOperator},
		{name: "verification step missing", change: startStopR3RemoveStep(startVerifyRuntimeStep), want: ResolutionNeedsOperator, wantRef: true},
		{name: "verification capability differs", change: verify(func(s *store.OperationStep) { s.CapabilityName = "execution.unit.start" }), want: ResolutionNeedsOperator, wantRef: true},
		{name: "verification adapter differs", change: verify(func(s *store.OperationStep) { s.AdapterID = "execution.other" }), want: ResolutionNeedsOperator, wantRef: true},
		{name: "verification version missing", change: verify(func(s *store.OperationStep) { s.CapabilityVersion = "" }), want: ResolutionNeedsOperator, wantRef: true},
		{name: "running", reply: startStopR3Reply(unit("running")), want: ResolutionCompleted, wantRef: true, wantCalls: 1,
			wantOut: fmt.Sprintf(`{"unit_ref":%s,"outcome":"completed","state":"running","observed_at":%q}`, startTestUnit, startStopR3ObservedAt)},
		{name: "absent", reply: startStopR3Reply(unit("absent")), want: ResolutionSafeToRetry, wantRef: true, wantCalls: 1},
		{name: "stopped", reply: startStopR3Reply(unit("stopped")), want: ResolutionSafeToRetry, wantRef: true, wantCalls: 1},
		{name: "starting", reply: startStopR3Reply(unit("starting")), want: ResolutionPending, wantRef: true, wantCalls: 1},
		{name: "stopping", reply: startStopR3Reply(unit("stopping")), want: ResolutionNeedsOperator, wantRef: true, wantCalls: 1},
		{name: "failed", reply: startStopR3Reply(unit("failed")), want: ResolutionDefinitiveError, wantRef: true, wantCalls: 1,
			wantError: `{"category":"provider_failure","code":"core.execution.unit_failed","message":"The exact execution unit is confirmed failed after a start attempt.","operation_id":"server.start-r3","recovery":{"action":"operator_review","operator_required":true},"retryable":false}`},
	}
	cases = append(cases, startStopR3ReconcileCommon(unit)...)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3PlannedStart(t, f)
			startStopR3SetStep(&operation, startRuntimeStep, startStopR3Ambiguous)
			if test.step == "" {
				test.step = startRuntimeStep
			}
			startStopR3RunReconcile(t, f, operation, test, f.start.Reconcile)
		})
	}
	for _, correlation := range []string{"", "correlation-1"} {
		t.Run("request with correlation "+correlation, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3PlannedStart(t, f)
			operation.CorrelationID = correlation
			startStopR3SetStep(&operation, startRuntimeStep, startStopR3Ambiguous)
			startStopR3CheckRequest(t, f, operation, f.start.Reconcile, startStopR3Request{
				stepID: startRuntimeStep, inspectID: startVerifyRuntimeStep, output: unit("running"), salt: "reconcile",
			})
		})
	}
}

// TestStartStopR3StopReconcile pins every server.stop reconciliation outcome.
func TestStartStopR3StopReconcile(t *testing.T) {
	unit := func(state string) string { return startStopR3UnitOutput(startTestUnit, state) }
	inspect := func(change func(*store.OperationStep)) func(*startStopR3Fixture, *store.OperationRecord) {
		return startStopR3Step(stopInspectUnitStep, change)
	}
	external := func(s *store.OperationStep) { s.ExternalOperationRef = json.RawMessage(startStopR3ExternalRef) }
	cases := []startStopR3ReconcileCase{
		{name: "nil context", nilCtx: true, want: ResolutionNeedsOperator, stepChange: external},
		{name: "save step", step: stopSaveGameStep, stepChange: external, want: ResolutionNeedsOperator, wantRef: true},
		{name: "inspect step", step: stopInspectUnitStep, stepChange: external, want: ResolutionNeedsOperator, wantRef: true},
		{name: "unit inspect missing", change: startStopR3RemoveStep(stopInspectUnitStep), want: ResolutionNeedsOperator, wantRef: true},
		{name: "unit inspect capability differs", change: inspect(func(s *store.OperationStep) { s.CapabilityName = "execution.unit.start" }), want: ResolutionNeedsOperator, wantRef: true},
		{name: "unit inspect adapter differs", change: inspect(func(s *store.OperationStep) { s.AdapterID = "execution.other" }), want: ResolutionNeedsOperator, wantRef: true},
		{name: "unit inspect version missing", change: inspect(func(s *store.OperationStep) { s.CapabilityVersion = "" }), want: ResolutionNeedsOperator, wantRef: true},
		{name: "unit inspect of another unit", change: inspect(startStopR3Input(`{"unit_ref":` + startStopR3OtherUnit + `}`)), want: ResolutionNeedsOperator, wantRef: true},
		{name: "stopped", reply: startStopR3Reply(unit("stopped")), want: ResolutionCompleted, wantRef: true, wantCalls: 1,
			wantOut: fmt.Sprintf(`{"observed_at":%q,"outcome":"completed","state":"stopped","unit_ref":%s}`, startStopR3ObservedAt, startTestUnit)},
		{name: "absent", reply: startStopR3Reply(unit("absent")), want: ResolutionNeedsOperator, wantRef: true, wantCalls: 1},
		{name: "running", reply: startStopR3Reply(unit("running")), want: ResolutionSafeToRetry, wantRef: true, wantCalls: 1},
		{name: "stopping", reply: startStopR3Reply(unit("stopping")), want: ResolutionPending, wantRef: true, wantCalls: 1},
		{name: "starting", reply: startStopR3Reply(unit("starting")), want: ResolutionNeedsOperator, wantRef: true, wantCalls: 1},
		{name: "failed", reply: startStopR3Reply(unit("failed")), want: ResolutionNeedsOperator, wantRef: true, wantCalls: 1},
	}
	cases = append(cases, startStopR3ReconcileCommon(unit)...)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := newStartStopR3Fixture(t)
			operation := startStopR3PlannedStop(t, f)
			startStopR3SetStep(&operation, stopRuntimeStep, startStopR3Ambiguous)
			if test.step == "" {
				test.step = stopRuntimeStep
			}
			startStopR3RunReconcile(t, f, operation, test, f.stop.Reconcile)
		})
	}
	t.Run("request", func(t *testing.T) {
		f := newStartStopR3Fixture(t)
		operation := startStopR3PlannedStop(t, f)
		startStopR3SetStep(&operation, stopRuntimeStep, startStopR3Ambiguous)
		startStopR3CheckRequest(t, f, operation, f.stop.Reconcile, startStopR3Request{
			stepID: stopRuntimeStep, inspectID: stopInspectUnitStep, output: unit("stopped"), salt: "reconcile-stop",
		})
	})
}

// startStopR3Request describes the read-only inspection a reconciliation must send.
type startStopR3Request struct {
	stepID    string
	inspectID string
	output    string
	salt      string
}

func startStopR3RequestID(operationID string, want startStopR3Request) string {
	sum := sha256.Sum256([]byte(operationID + "\x00" + want.stepID + "\x00" + want.salt + "\x002"))
	return "reconcile-" + hex.EncodeToString(sum[:16])
}

func startStopR3ExpectedRequest(f *startStopR3Fixture, operation store.OperationRecord, want startStopR3Request) adapter.InvokeRequest {
	correlation := operation.CorrelationID
	if correlation == "" {
		correlation = operation.OperationID
	}
	requestID := startStopR3RequestID(operation.OperationID, want)
	return adapter.InvokeRequest{JSONRPC: "2.0", ID: requestID, Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: f.version(), Capability: "execution.unit.inspect",
		RequestContext: adapter.RequestContext{
			RequestID: requestID, CorrelationID: correlation, OperationID: operation.OperationID, StepID: want.stepID,
			ServerID: startTestServerID, EnvironmentID: startTestEnvID,
		},
		Input: operationStep(operation, want.inspectID).Input,
	}}
}

func startStopR3MustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func startStopR3CheckRequest(t *testing.T, f *startStopR3Fixture, operation store.OperationRecord, reconcile startStopR3Reconciler, want startStopR3Request) {
	t.Helper()
	f.adapters.reply = startStopR3Reply(want.output)
	before := time.Now().UTC()
	if _, err := reconcile(context.Background(), operation, operationStep(operation, want.stepID)); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	if len(f.adapters.calls) != 1 {
		t.Fatalf("calls = %d", len(f.adapters.calls))
	}
	call := f.adapters.calls[0]
	request := call.request
	deadline := request.Params.RequestContext.Deadline
	request.Params.RequestContext.Deadline = time.Time{}
	gotJSON := startStopR3MustJSON(t, request)
	wantJSON := startStopR3MustJSON(t, startStopR3ExpectedRequest(f, operation, want))
	if call.adapterID != "execution.fake" || !bytes.Equal(gotJSON, wantJSON) || !call.deadline {
		t.Fatalf("request to %s = %s (deadline %v); want %s", call.adapterID, gotJSON, call.deadline, wantJSON)
	}
	if deadline.Location() != time.UTC || deadline.Before(before.Add(15*time.Second)) || deadline.After(after.Add(15*time.Second)) {
		t.Fatalf("deadline = %v; want 15s after %v", deadline, before)
	}
}
