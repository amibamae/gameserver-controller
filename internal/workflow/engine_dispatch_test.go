package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/fakeadapter"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// These tests pin what dispatch records for each way an adapter step can
// end before or after the call, so the function can be reorganized safely.

// dispatchOutcome is the durable result a test compares.
type dispatchOutcome struct {
	Operation, Step, StepError, OperationError string
	Calls                                      int
}

func observeDispatch(t *testing.T, fixture engineFixture, engine *Engine) dispatchOutcome {
	t.Helper()
	stored, err := engine.Run(context.Background(), fixture.operation.Record.OperationID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return dispatchOutcome{
		Operation: string(stored.Record.Status), Step: string(stored.Record.Steps[0].Status),
		StepError: errorCode(stored.Record.Steps[0].Error), OperationError: errorCode(stored.Record.Error),
		Calls: len(fixture.adapter.Calls()),
	}
}

func errorCode(value json.RawMessage) string {
	if len(value) == 0 {
		return ""
	}
	var decoded struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(value, &decoded); err != nil {
		return "undecodable"
	}
	return decoded.Code
}

// countingCloser counts Close calls and can fail them.
type countingCloser struct {
	io.Reader
	io.Writer
	closed *atomic.Int32
	err    error
}

func (c countingCloser) Close() error {
	c.closed.Add(1)
	return c.err
}

func streamReference(id, direction string) capturestream.Reference {
	return capturestream.Reference{TransferID: id, Direction: direction, MaximumBytes: 7, ExpiresAt: time.Now().UTC().Add(20 * time.Second)}
}

var (
	failedBeforeDispatch = dispatchOutcome{Operation: "failed", Step: "failed", StepError: "core.workflow.stream_transfer_failed", OperationError: "core.workflow.stream_transfer_failed"}
	verifyOK             = func() error { return nil }
)

func TestDispatchRejectsUnsafeStreamBindingsBeforeTheCall(t *testing.T) {
	transferID := strings.Repeat("a", 64)
	cases := map[string]func(closed *atomic.Int32) []adapter.StreamTransfer{
		"source on an adapter-to-controller stream": func(closed *atomic.Int32) []adapter.StreamTransfer {
			return []adapter.StreamTransfer{{Reference: streamReference(transferID, "adapter_to_controller"), Source: countingCloser{Reader: strings.NewReader("x"), closed: closed}, Verify: verifyOK}}
		},
		"both ends set": func(closed *atomic.Int32) []adapter.StreamTransfer {
			both := countingCloser{Reader: strings.NewReader("x"), Writer: io.Discard, closed: closed}
			return []adapter.StreamTransfer{{Reference: streamReference(transferID, "controller_to_adapter"), Source: both, Sink: both, Verify: verifyOK}}
		},
		"unknown direction": func(closed *atomic.Int32) []adapter.StreamTransfer {
			return []adapter.StreamTransfer{{Reference: streamReference(transferID, "sideways"), Source: countingCloser{Reader: strings.NewReader("x"), closed: closed}, Verify: verifyOK}}
		},
		"no verifier": func(closed *atomic.Int32) []adapter.StreamTransfer {
			return []adapter.StreamTransfer{{Reference: streamReference(transferID, "controller_to_adapter"), Source: countingCloser{Reader: strings.NewReader("x"), closed: closed}}}
		},
		"empty transfer ID": func(closed *atomic.Int32) []adapter.StreamTransfer {
			return []adapter.StreamTransfer{{Reference: streamReference("", "controller_to_adapter"), Source: countingCloser{Reader: strings.NewReader("x"), closed: closed}, Verify: verifyOK}}
		},
		"zero byte bound": func(closed *atomic.Int32) []adapter.StreamTransfer {
			reference := streamReference(transferID, "controller_to_adapter")
			reference.MaximumBytes = 0
			return []adapter.StreamTransfer{{Reference: reference, Source: countingCloser{Reader: strings.NewReader("x"), closed: closed}, Verify: verifyOK}}
		},
		"duplicate transfer ID": func(closed *atomic.Int32) []adapter.StreamTransfer {
			return []adapter.StreamTransfer{
				{Reference: streamReference(transferID, "controller_to_adapter"), Source: countingCloser{Reader: strings.NewReader("x"), closed: closed}, Verify: verifyOK},
				{Reference: streamReference(transferID, "adapter_to_controller"), Sink: countingCloser{Writer: io.Discard, closed: closed}, Verify: verifyOK},
			}
		},
	}
	for name, transfers := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newEngineFixture(t, "unsafe-stream", "1", nil)
			var closed atomic.Int32
			fixture.definition.streamTransfers = transfers(&closed)
			if got := observeDispatch(t, fixture, fixture.engine); got != failedBeforeDispatch {
				t.Fatalf("outcome = %+v, want %+v", got, failedBeforeDispatch)
			}
			want := int32(len(fixture.definition.streamTransfers))
			if name == "both ends set" {
				want = 2
			}
			if closed.Load() != want {
				t.Fatalf("closed %d stream ends, want %d", closed.Load(), want)
			}
		})
	}
}

func TestDispatchTreatsAStreamCloseFailureAsAnUnknownMutation(t *testing.T) {
	fixture := newEngineFixture(t, "stream-close", "1", nil)
	var closed atomic.Int32
	fixture.definition.streamTransfers = []adapter.StreamTransfer{
		{Reference: streamReference(strings.Repeat("a", 64), "controller_to_adapter"), Source: countingCloser{Reader: strings.NewReader("x"), closed: &closed, err: errors.New("close failed")}, Verify: verifyOK},
		{Reference: streamReference(strings.Repeat("b", 64), "controller_to_adapter"), Source: countingCloser{Reader: strings.NewReader("y"), closed: &closed}, Verify: verifyOK},
	}
	if got := observeDispatch(t, fixture, fixture.engine); got != unknownNeedsOperator("core.adapter.outcome_unknown") {
		t.Fatalf("outcome = %+v", got)
	}
	if closed.Load() != 2 {
		t.Fatalf("closed %d streams, want both", closed.Load())
	}
}

// unknownNeedsOperator is the result of one uncertain call that the test
// definition's reconciliation hands to an operator.
func unknownNeedsOperator(stepError string) dispatchOutcome {
	return dispatchOutcome{Operation: "needs_operator", Step: "outcome_unknown", StepError: stepError, OperationError: "core.reconciliation.operator_required", Calls: 1}
}

func TestDispatchResponseOutcomes(t *testing.T) {
	now := time.Now().UTC()
	// The registry turns a malformed response into an uncertain call before
	// dispatch sees it, so those cases share the outcome-unknown code.
	unknown := unknownNeedsOperator("core.adapter.outcome_unknown")
	want := map[string]dispatchOutcome{
		"output fails its schema":   unknown,
		"unsupported outcome":       unknown,
		"wrong response ID":         unknown,
		"no observation time":       unknown,
		"adapter reports unknown":   unknown,
		"external reference kept":   {Operation: "succeeded", Step: "succeeded", Calls: 1},
		"external reference change": unknownNeedsOperator("core.adapter.external_reference_changed"),
	}
	for name, reply := range map[string]fakeadapter.Reply{
		"output fails its schema":   {Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(`{"unexpected":true}`), ObservedAt: now},
		"unsupported outcome":       {Outcome: adapter.Outcome("finished"), Output: json.RawMessage(testExternalRef), ObservedAt: now},
		"wrong response ID":         {Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ObservedAt: now, ResponseID: "other"},
		"no observation time":       {Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef)},
		"adapter reports unknown":   {Outcome: adapter.OutcomeUnknown, Output: json.RawMessage(testExternalRef), ObservedAt: now},
		"external reference kept":   {Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ExternalOperationRef: json.RawMessage(testExternalRef), ObservedAt: now},
		"external reference change": {Outcome: adapter.OutcomeCompleted, Output: json.RawMessage(testExternalRef), ExternalOperationRef: json.RawMessage(`{"adapter_id":"hosting.test","environment_id":"staging","resource_kind":"instance","external_id":"other"}`), ObservedAt: now},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newEngineFixture(t, "response-outcome", "1", []fakeadapter.Reply{reply})
			if strings.HasPrefix(name, "external reference") {
				current, err := fixture.store.GetOperation(fixture.operation.Record.OperationID)
				if err != nil {
					t.Fatal(err)
				}
				changed := current.Record
				changed.Steps[0].ExternalOperationRef = json.RawMessage(testExternalRef)
				if _, err := fixture.store.UpdateOperation(current.Revision, changed); err != nil {
					t.Fatalf("seed a recorded external reference: %v", err)
				}
			}
			if got := observeDispatch(t, fixture, fixture.engine); got != want[name] {
				t.Fatalf("outcome = %+v, want %+v", got, want[name])
			}
		})
	}
}

// secretFailingDefinition is testDefinition whose secret lookup fails.
type secretFailingDefinition struct{ *testDefinition }

func (secretFailingDefinition) SecretReferencesForStep(context.Context, store.OperationRecord, store.OperationStep) ([]adapter.SecretReference, error) {
	return nil, errors.New("vault detail")
}

func TestDispatchStopsWhenSecretReferencesCannotBeAuthorized(t *testing.T) {
	fixture := newEngineFixture(t, "secret-refs", "1", nil)
	definitions := NewDefinitions()
	if err := definitions.Register(testOperationType, secretFailingDefinition{fixture.definition}); err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(fixture.store, fixture.engine.adapters, fixture.engine.schemas, definitions, fixture.authorizer, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := dispatchOutcome{Operation: "failed", Step: "failed", StepError: "core.workflow.secret_reference_failed", OperationError: "core.workflow.secret_reference_failed"}
	if got := observeDispatch(t, fixture, engine); got != want {
		t.Fatalf("outcome = %+v, want %+v", got, want)
	}
}
