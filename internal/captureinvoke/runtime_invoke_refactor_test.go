package captureinvoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// These tests characterize Invoke and authorizeCapture branch by branch:
// the exact error text, whether a failure is reported as possibly having had
// side effects, the order of the authorization checks, and that every stage
// and stream handle Invoke opens is released on every path.

const invokeRefactorUncertainText = "capture result requires reconciliation"

// invokeRefactorStaging counts Open calls and can return a nil stage, an
// error, or run a hook (for example, to claim the external destination)
// before the stage is returned.
type invokeRefactorStaging struct {
	stage    *stage
	err      error
	nilStage bool
	onOpen   func()
	opened   int
}

func (s *invokeRefactorStaging) Open(context.Context, capturestream.Scope) (Stage, error) {
	s.opened++
	if s.onOpen != nil {
		s.onOpen()
	}
	if s.nilStage {
		return nil, s.err
	}
	return s.stage, s.err
}

// invokeRefactorHarness returns a harness with counting staging and a broker
// that holds a single pending stream, so a leaked ticket is observable.
func invokeRefactorHarness(t *testing.T) (*captureHarness, *invokeRefactorStaging) {
	t.Helper()
	h := newCaptureHarness(t)
	h.broker, _ = capturestream.New(1)
	counting := &invokeRefactorStaging{stage: h.sink}
	h.staging = counting
	return h, counting
}

// invokeRefactorExternal attaches one external stream destination.
func invokeRefactorExternal(h *captureHarness, reference capturestream.Reference) *closeBuffer {
	external := &closeBuffer{}
	h.request.Params.RequestContext.StreamTransfers = []capturestream.Reference{reference}
	if reference.TransferID != "" {
		h.request.Params.RequestContext.SetStreamDestinations(map[string]io.WriteCloser{reference.TransferID: external})
	}
	return external
}

func invokeRefactorDestination(expires time.Duration) capturestream.Reference {
	return capturestream.Reference{TransferID: "workflow-transfer", Direction: "adapter_to_controller", MaximumBytes: 8, ExpiresAt: time.Now().Add(expires)}
}

// invokeRefactorSideEffects reports whether err is classified as possibly
// side-effecting.
func invokeRefactorSideEffects(err error) bool {
	var invocation *adapter.InvocationError
	return errors.As(err, &invocation) && invocation.MayHaveSideEffects
}

// invokeRefactorReleased fails unless the broker's single pending slot is
// free again, which shows Invoke aborted the ticket it issued.
func invokeRefactorReleased(t *testing.T, h *captureHarness) {
	t.Helper()
	if _, _, err := h.broker.IssueTracked(h.scope, 8, time.Now().Add(time.Minute), time.Now().Add(time.Minute), io.Discard); err != nil {
		t.Fatalf("capture stream ticket was not released: %v", err)
	}
}

// invokeRefactorUnknownOutput is an outcome_unknown result whose
// reconciliation reference names this adapter and environment.
func invokeRefactorUnknownOutput(h *captureHarness) map[string]any {
	output := h.output()
	delete(output, "size_bytes")
	delete(output, "checksum")
	delete(output, "stream_completed")
	output["outcome"] = "outcome_unknown"
	output["reason_code"] = "capture_rejected"
	output["reconciliation_ref"] = map[string]any{"adapter_id": h.descriptor.AdapterID, "environment_id": h.scope.EnvironmentID, "resource_kind": "operation", "external_id": "capture-unknown"}
	return output
}

func invokeRefactorRejectedOutput(h *captureHarness) map[string]any {
	output := h.output()
	delete(output, "size_bytes")
	delete(output, "checksum")
	delete(output, "stream_completed")
	output["outcome"] = "rejected"
	output["reason_code"] = "capture_rejected"
	return output
}

// invokeRefactorStream claims the issued stream and writes data, completing
// it when complete is set.
func invokeRefactorStream(h *captureHarness, r adapter.InvokeRequest, data string, complete bool) error {
	writer, err := h.broker.Claim(context.Background(), r.Params.RequestContext.StreamTransfers[0].TransferID, h.scope, nil)
	if err != nil {
		return err
	}
	if _, err = writer.Write([]byte(data)); err != nil {
		return err
	}
	if complete {
		_, err = writer.Complete()
	}
	return err
}

func TestInvokeRefactorAuthorizeRejections(t *testing.T) {
	stepIDPointer := func(id string) *string { return &id }
	tests := []struct {
		name    string
		setup   func(*captureHarness) (context.Context, string)
		message string
	}{
		{"nil context", func(h *captureHarness) (context.Context, string) {
			var ctx context.Context
			return ctx, h.descriptor.AdapterID
		}, "invalid capture dispatch context"},
		{"missing initial lifecycle state", func(h *captureHarness) (context.Context, string) {
			h.records.op.Record.BackupInitialLifecycleState = ""
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"no current step", func(h *captureHarness) (context.Context, string) {
			h.records.op.Record.CurrentStepID = nil
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"another current step", func(h *captureHarness) (context.Context, string) {
			h.records.op.Record.CurrentStepID = stepIDPointer("other-step")
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"context for another server", func(h *captureHarness) (context.Context, string) {
			h.request.Params.RequestContext.ServerID = "other-server"
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"context for another environment", func(h *captureHarness) (context.Context, string) {
			h.request.Params.RequestContext.EnvironmentID = "production"
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"current step not recorded", func(h *captureHarness) (context.Context, string) {
			h.records.op.Record.CurrentStepID = stepIDPointer("missing-step")
			h.request.Params.RequestContext.StepID = "missing-step"
			return context.Background(), h.descriptor.AdapterID
		}, "capture request does not match durable step"},
		{"step never attempted", func(h *captureHarness) (context.Context, string) {
			h.step().Attempt = 0
			return context.Background(), h.descriptor.AdapterID
		}, "capture request does not match durable step"},
		{"step for another capability", func(h *captureHarness) (context.Context, string) {
			h.step().CapabilityName = "game.backup.prepare"
			return context.Background(), h.descriptor.AdapterID
		}, "capture request does not match durable step"},
		{"step input differs", func(h *captureHarness) (context.Context, string) {
			h.step().Input = json.RawMessage(`{}`)
			return context.Background(), h.descriptor.AdapterID
		}, "capture request does not match durable step"},
		{"capability version changed", func(h *captureHarness) (context.Context, string) {
			h.step().CapabilityVersion = "9.9.9"
			return context.Background(), h.descriptor.AdapterID
		}, "capture capability or deadline differs from durable plan"},
		{"lock held by another operation", func(h *captureHarness) (context.Context, string) {
			h.records.lock.OperationID = "other-operation"
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires the durable server lock"},
		{"lock for another scope", func(h *captureHarness) (context.Context, string) {
			h.records.lock.Scope = "other-scope"
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires the durable server lock"},
		{"input targets another server", func(h *captureHarness) (context.Context, string) {
			h.setInput(func(input map[string]any) { input["target"].(map[string]any)["server_id"] = "other-server" })
			return context.Background(), h.descriptor.AdapterID
		}, "capture target does not match durable operation"},
		{"input targets another environment", func(h *captureHarness) (context.Context, string) {
			h.setInput(func(input map[string]any) { input["target"].(map[string]any)["environment_id"] = "production" })
			return context.Background(), h.descriptor.AdapterID
		}, "capture target does not match durable operation"},
		{"destination without a transfer ID", func(h *captureHarness) (context.Context, string) {
			reference := invokeRefactorDestination(time.Minute)
			reference.TransferID = ""
			invokeRefactorExternal(h, reference)
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"destination without a byte limit", func(h *captureHarness) (context.Context, string) {
			reference := invokeRefactorDestination(time.Minute)
			reference.MaximumBytes = 0
			invokeRefactorExternal(h, reference)
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"destination above the runtime byte limit", func(h *captureHarness) (context.Context, string) {
			reference := invokeRefactorDestination(time.Minute)
			reference.MaximumBytes = 11
			invokeRefactorExternal(h, reference)
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"destination already expired", func(h *captureHarness) (context.Context, string) {
			invokeRefactorExternal(h, invokeRefactorDestination(-time.Second))
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"destination lives longer than five minutes", func(h *captureHarness) (context.Context, string) {
			h.request.Params.RequestContext.Deadline = time.Now().Add(10 * time.Minute)
			invokeRefactorExternal(h, invokeRefactorDestination(6*time.Minute))
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"destination already claimed", func(h *captureHarness) (context.Context, string) {
			invokeRefactorExternal(h, invokeRefactorDestination(time.Minute))
			if _, err := h.request.Params.RequestContext.CreateStream("workflow-transfer"); err != nil {
				h.t.Fatal(err)
			}
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
		{"instance reference is not an instance", func(h *captureHarness) (context.Context, string) {
			h.setInput(func(input map[string]any) {
				input["target"].(map[string]any)["instance_ref"].(map[string]any)["resource_kind"] = "volume"
			})
			var input capture
			if err := json.Unmarshal(h.request.Params.Input, &input); err != nil {
				h.t.Fatal(err)
			}
			h.records.op.Record.Target.InstanceRef = append(json.RawMessage(nil), input.Target.InstanceRef...)
			return context.Background(), h.descriptor.AdapterID
		}, "invalid capture instance"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, counting := invokeRefactorHarness(t)
			ctx, adapterID := test.setup(h)
			_, err := h.runtime().Invoke(ctx, adapterID, h.request)
			if err == nil || err.Error() != test.message {
				t.Fatalf("Invoke error = %v, want %q", err, test.message)
			}
			if invokeRefactorSideEffects(err) {
				t.Fatalf("authorization failure %v was reported as possibly side-effecting", err)
			}
			if counting.opened != 0 || h.called || h.sink.discarded || h.sink.sealed {
				t.Fatalf("rejected capture opened staging %d times, called=%v, discarded=%v, sealed=%v", counting.opened, h.called, h.sink.discarded, h.sink.sealed)
			}
			invokeRefactorReleased(t, h)
		})
	}
}

// TestInvokeRefactorAuthorizeCheckOrder breaks two adjacent checks at once
// and requires the earlier check's message.
func TestInvokeRefactorAuthorizeCheckOrder(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*captureHarness) (context.Context, string)
		message string
	}{
		{"context before operation", func(h *captureHarness) (context.Context, string) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			h.records.opErr = errors.New("not found")
			return ctx, h.descriptor.AdapterID
		}, "invalid capture dispatch context"},
		{"operation before step", func(h *captureHarness) (context.Context, string) {
			h.records.op.Record.Status = protocolstate.OperationReconciling
			h.step().Status = protocolstate.StepOutcomeUnknown
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires its running durable backup step"},
		{"step before adapter", func(h *captureHarness) (context.Context, string) {
			return context.Background(), "game.unregistered"
		}, "capture request does not match durable step"},
		{"adapter before lock", func(h *captureHarness) (context.Context, string) {
			h.step().AdapterID = "game.unregistered"
			h.records.lockErr = errors.New("not found")
			return context.Background(), "game.unregistered"
		}, "capture adapter is unavailable"},
		{"capability before lock", func(h *captureHarness) (context.Context, string) {
			h.step().CapabilityVersion = "9.9.9"
			h.records.lockErr = errors.New("not found")
			return context.Background(), h.descriptor.AdapterID
		}, "capture capability or deadline differs from durable plan"},
		{"lock before input schema", func(h *captureHarness) (context.Context, string) {
			h.setInput(func(input map[string]any) { delete(input, "capture_id") })
			h.records.lockErr = errors.New("not found")
			return context.Background(), h.descriptor.AdapterID
		}, "capture requires the durable server lock"},
		{"input schema before target", func(h *captureHarness) (context.Context, string) {
			h.setInput(func(input map[string]any) {
				delete(input, "capture_id")
				input["target"].(map[string]any)["server_id"] = "other-server"
			})
			return context.Background(), h.descriptor.AdapterID
		}, "invalid capture input"},
		{"target before destination", func(h *captureHarness) (context.Context, string) {
			h.setInput(func(input map[string]any) { input["target"].(map[string]any)["server_id"] = "other-server" })
			invokeRefactorExternal(h, invokeRefactorDestination(-time.Second))
			return context.Background(), h.descriptor.AdapterID
		}, "capture target does not match durable operation"},
		{"destination before instance", func(h *captureHarness) (context.Context, string) {
			h.setInstanceEnvironment("production")
			invokeRefactorExternal(h, invokeRefactorDestination(-time.Second))
			return context.Background(), h.descriptor.AdapterID
		}, "capture destination is not a valid controller-bound stream"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, counting := invokeRefactorHarness(t)
			ctx, adapterID := test.setup(h)
			_, err := h.runtime().Invoke(ctx, adapterID, h.request)
			if err == nil || err.Error() != test.message {
				t.Fatalf("Invoke error = %v, want %q", err, test.message)
			}
			if counting.opened != 0 || h.called {
				t.Fatalf("rejected capture opened staging %d times, called=%v", counting.opened, h.called)
			}
		})
	}
}

func TestInvokeRefactorAuthorizeAcceptsBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*captureHarness)
	}{
		{"deadline equal to the operation deadline", func(h *captureHarness) {
			deadline := h.request.Params.RequestContext.Deadline
			h.records.op.Record.Deadline = &deadline
		}},
		{"deadline before the operation deadline", func(h *captureHarness) {
			deadline := h.request.Params.RequestContext.Deadline.Add(time.Minute)
			h.records.op.Record.Deadline = &deadline
		}},
		{"destination at the runtime byte limit", func(h *captureHarness) {
			reference := invokeRefactorDestination(time.Minute)
			reference.MaximumBytes = 10
			invokeRefactorExternal(h, reference)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, counting := invokeRefactorHarness(t)
			test.setup(h)
			if _, err := h.runtime().Invoke(context.Background(), h.descriptor.AdapterID, h.request); err != nil {
				t.Fatalf("Invoke error = %v", err)
			}
			if counting.opened != 1 || !h.called || !h.sink.sealed || h.sink.discarded {
				t.Fatalf("opened=%d called=%v sealed=%v discarded=%v", counting.opened, h.called, h.sink.sealed, h.sink.discarded)
			}
			invokeRefactorReleased(t, h)
		})
	}
}

func TestInvokeRefactorStagingAndStreamSetup(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*captureHarness, *invokeRefactorStaging)
		message     string
		sideEffects bool
		discarded   bool
	}{
		{"staging needs reconciliation", func(h *captureHarness, s *invokeRefactorStaging) {
			s.err = ErrStagingNeedsReconciliation
		}, invokeRefactorUncertainText, true, false},
		{"staging wraps needs reconciliation", func(h *captureHarness, s *invokeRefactorStaging) {
			s.err = fmt.Errorf("open: %w", ErrStagingNeedsReconciliation)
			s.nilStage = true
		}, invokeRefactorUncertainText, true, false},
		{"staging fails", func(h *captureHarness, s *invokeRefactorStaging) {
			s.err = errors.New("disk full")
		}, "capture staging is unavailable", false, false},
		{"staging returns no stage", func(h *captureHarness, s *invokeRefactorStaging) {
			s.nilStage = true
		}, "capture staging is unavailable", false, false},
		{"destination claimed before dispatch", func(h *captureHarness, s *invokeRefactorStaging) {
			invokeRefactorExternal(h, invokeRefactorDestination(time.Minute))
			s.onOpen = func() { _, _ = h.request.Params.RequestContext.CreateStream("workflow-transfer") }
		}, "capture destination could not be claimed", false, true},
		{"destination claimed and cleanup fails", func(h *captureHarness, s *invokeRefactorStaging) {
			invokeRefactorExternal(h, invokeRefactorDestination(time.Minute))
			s.onOpen = func() { _, _ = h.request.Params.RequestContext.CreateStream("workflow-transfer") }
			h.sink.failDiscard = true
		}, "capture staging cleanup failed before dispatch", false, true},
		{"stream cannot be issued", func(h *captureHarness, s *invokeRefactorStaging) {
			s.onOpen = func() {
				_, _, _ = h.broker.IssueTracked(h.scope, 8, time.Now().Add(time.Minute), time.Now().Add(time.Minute), io.Discard)
			}
		}, "capture stream could not be issued", false, true},
		{"stream cannot be issued and cleanup fails", func(h *captureHarness, s *invokeRefactorStaging) {
			s.onOpen = func() {
				_, _, _ = h.broker.IssueTracked(h.scope, 8, time.Now().Add(time.Minute), time.Now().Add(time.Minute), io.Discard)
			}
			h.sink.failDiscard = true
		}, "capture staging cleanup failed before dispatch", false, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, counting := invokeRefactorHarness(t)
			test.setup(h, counting)
			response, err := h.runtime().Invoke(context.Background(), h.descriptor.AdapterID, h.request)
			if err == nil || err.Error() != test.message {
				t.Fatalf("Invoke error = %v, want %q", err, test.message)
			}
			if invokeRefactorSideEffects(err) != test.sideEffects {
				t.Fatalf("side effects = %v, want %v", invokeRefactorSideEffects(err), test.sideEffects)
			}
			if counting.opened != 1 || h.called || h.sink.sealed || h.sink.discarded != test.discarded {
				t.Fatalf("opened=%d called=%v sealed=%v discarded=%v", counting.opened, h.called, h.sink.sealed, h.sink.discarded)
			}
			if response.Result.Output != nil || response.Result.Outcome != "" {
				t.Fatalf("failed setup returned a response: %+v", response)
			}
		})
	}
}

func TestInvokeRefactorResultClassification(t *testing.T) {
	type outcome struct {
		message   string // empty means success
		sealed    bool
		discarded bool
	}
	tests := []struct {
		name    string
		setup   func(*captureHarness) context.Context
		respond func(*captureHarness, context.Context, adapter.InvokeRequest) (adapter.InvokeResponse, error)
		want    outcome
		check   func(*testing.T, *captureHarness, adapter.InvokeResponse)
	}{
		{name: "captured", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.captured(r)
		}, want: outcome{sealed: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			if response.Result.Outcome != adapter.OutcomeCompleted || string(response.Result.Output) != string(h.sink.output) || h.sink.String() != "abc" || h.sink.receipt.Scope != h.scope {
				t.Fatalf("captured response %+v, staged %q", response.Result, h.sink.String())
			}
		}},
		{name: "captured to an external destination", setup: func(h *captureHarness) context.Context {
			external := invokeRefactorExternal(h, invokeRefactorDestination(time.Minute))
			h.t.Cleanup(func() {
				if external.String() != "abc" {
					h.t.Errorf("external destination received %q", external.String())
				}
			})
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.captured(r)
		}, want: outcome{sealed: true}},
		{name: "unknown outcome without an external reference", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorUnknownOutput(h))
		}, want: outcome{discarded: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			want, _ := json.Marshal(invokeRefactorUnknownOutput(h)["reconciliation_ref"])
			if response.Result.Outcome != adapter.OutcomeUnknown || !sameJSON(response.Result.ExternalOperationRef, want) {
				t.Fatalf("unknown outcome was not normalized: %+v", response.Result)
			}
		}},
		{name: "unknown outcome with the same external reference", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			response, err := h.reply(r, adapter.OutcomeCompleted, invokeRefactorUnknownOutput(h))
			response.Result.ExternalOperationRef, _ = json.Marshal(invokeRefactorUnknownOutput(h)["reconciliation_ref"])
			return response, err
		}, want: outcome{discarded: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			if response.Result.Outcome != adapter.OutcomeUnknown || len(response.Result.ExternalOperationRef) == 0 {
				t.Fatalf("unknown outcome was not normalized: %+v", response.Result)
			}
		}},
		{name: "unknown outcome reported by the adapter as unknown", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeUnknown, invokeRefactorUnknownOutput(h))
		}, want: outcome{discarded: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			if response.Result.Outcome != adapter.OutcomeUnknown {
				t.Fatalf("unknown outcome was not normalized: %+v", response.Result)
			}
		}},
		{name: "unknown outcome referencing another adapter", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			output := invokeRefactorUnknownOutput(h)
			output["reconciliation_ref"].(map[string]any)["adapter_id"] = "game.other"
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "unknown outcome referencing another environment", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			output := invokeRefactorUnknownOutput(h)
			output["reconciliation_ref"].(map[string]any)["environment_id"] = "production"
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "unknown outcome with a different external reference", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			response, err := h.reply(r, adapter.OutcomeCompleted, invokeRefactorUnknownOutput(h))
			response.Result.ExternalOperationRef = json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"operation","external_id":"other"}`)
			return response, err
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "unknown outcome and cleanup fails", setup: func(h *captureHarness) context.Context {
			h.sink.failDiscard = true
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorUnknownOutput(h))
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "unknown outcome for another capture", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			output := invokeRefactorUnknownOutput(h)
			output["capture_id"] = "different-capture"
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "rejected without bytes", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorRejectedOutput(h))
		}, want: outcome{discarded: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			if response.Result.Outcome != adapter.OutcomeCompleted || len(response.Result.Output) == 0 {
				t.Fatalf("rejected response %+v", response.Result)
			}
		}},
		{name: "rejected after writing bytes", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abc", true); err != nil {
				return adapter.InvokeResponse{}, err
			}
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorRejectedOutput(h))
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "rejected reported by the adapter as unknown", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeUnknown, invokeRefactorRejectedOutput(h))
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "rejected and cleanup fails", setup: func(h *captureHarness) context.Context {
			h.sink.failDiscard = true
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorRejectedOutput(h))
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured for another capture", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abc", true); err != nil {
				return adapter.InvokeResponse{}, err
			}
			output := h.output()
			output["capture_id"] = "different-capture"
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured without a stream", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, h.output())
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured with an incomplete stream", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abc", false); err != nil {
				return adapter.InvokeResponse{}, err
			}
			return h.reply(r, adapter.OutcomeCompleted, h.output())
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured with a different size", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abc", true); err != nil {
				return adapter.InvokeResponse{}, err
			}
			output := h.output()
			output["size_bytes"] = 4
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured with a different digest", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abd", true); err != nil {
				return adapter.InvokeResponse{}, err
			}
			return h.reply(r, adapter.OutcomeCompleted, h.output())
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "context canceled before sealing", setup: func(h *captureHarness) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			h.t.Cleanup(cancel)
			h.respond = func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
				response, err := h.captured(r)
				cancel()
				return response, err
			}
			return ctx
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "seal fails", setup: func(h *captureHarness) context.Context {
			h.sink.failSeal = true
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.captured(r)
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "adapter output fails its schema", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, map[string]any{"outcome": "captured"})
		}, want: outcome{message: "streamed adapter invocation failed", discarded: true}},
		{name: "adapter fails", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return adapter.InvokeResponse{}, errors.New("adapter exploded")
		}, want: outcome{message: "streamed adapter invocation failed", discarded: true}},
		{name: "adapter fails and cleanup fails", setup: func(h *captureHarness) context.Context {
			h.sink.failDiscard = true
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return adapter.InvokeResponse{}, errors.New("adapter exploded")
		}, want: outcome{message: invokeRefactorUncertainText, discarded: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, counting := invokeRefactorHarness(t)
			ctx := context.Background()
			if test.setup != nil {
				ctx = test.setup(h)
			}
			if test.respond != nil {
				respond := test.respond
				h.respond = func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) { return respond(h, ctx, r) }
			}
			transfers := len(h.request.Params.RequestContext.StreamTransfers)
			response, err := h.runtime().Invoke(ctx, h.descriptor.AdapterID, h.request)
			if test.want.message == "" {
				if err != nil {
					t.Fatalf("Invoke error = %v", err)
				}
			} else {
				if err == nil || err.Error() != test.want.message {
					t.Fatalf("Invoke error = %v, want %q", err, test.want.message)
				}
				if !invokeRefactorSideEffects(err) {
					t.Fatalf("dispatched failure %v was not reported as possibly side-effecting", err)
				}
				if response.Result.Output != nil || response.Result.Outcome != "" || response.Result.ExternalOperationRef != nil {
					t.Fatalf("failed capture returned a response: %+v", response)
				}
			}
			if counting.opened != 1 || !h.called || h.sink.sealed != test.want.sealed || h.sink.discarded != test.want.discarded {
				t.Fatalf("opened=%d called=%v sealed=%v discarded=%v, want sealed=%v discarded=%v", counting.opened, h.called, h.sink.sealed, h.sink.discarded, test.want.sealed, test.want.discarded)
			}
			if len(h.request.Params.RequestContext.StreamTransfers) != transfers {
				t.Fatal("Invoke mutated the caller's stream transfers")
			}
			invokeRefactorReleased(t, h)
			if test.check != nil {
				test.check(t, h, response)
			}
		})
	}
}
