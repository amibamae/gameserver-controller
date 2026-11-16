package captureinvoke

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

type classificationOutcome struct {
	message   string // empty means success
	sealed    bool
	discarded bool
}

type classificationCase struct {
	name    string
	setup   func(*captureHarness) context.Context
	respond func(*captureHarness, context.Context, adapter.InvokeRequest) (adapter.InvokeResponse, error)
	want    classificationOutcome
	check   func(*testing.T, *captureHarness, adapter.InvokeResponse)
}

func TestInvokeRefactorResultClassification(t *testing.T) {
	for _, cases := range [][]classificationCase{
		successfulCaptureCases(), unknownCaptureCases(), rejectedCaptureCases(), failedCaptureCases(),
	} {
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				runClassificationCase(t, test)
			})
		}
	}
}

func runClassificationCase(t *testing.T, test classificationCase) {
	t.Helper()
	h, counting := invokeRefactorHarness(t)
	ctx := context.Background()
	if test.setup != nil {
		ctx = test.setup(h)
	}
	if test.respond != nil {
		h.respond = func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) { return test.respond(h, ctx, r) }
	}
	transfers := len(h.request.Params.RequestContext.StreamTransfers)
	response, err := h.runtime().Invoke(ctx, h.descriptor.AdapterID, h.request)
	assertClassificationResult(t, test.want.message, response, err)
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
}

func assertClassificationResult(t *testing.T, message string, response adapter.InvokeResponse, err error) {
	t.Helper()
	if message == "" {
		if err != nil {
			t.Fatalf("Invoke error = %v", err)
		}
		return
	}
	if err == nil || err.Error() != message {
		t.Fatalf("Invoke error = %v, want %q", err, message)
	}
	if !invokeRefactorSideEffects(err) {
		t.Fatalf("dispatched failure %v was not reported as possibly side-effecting", err)
	}
	if response.Result.Output != nil || response.Result.Outcome != "" || response.Result.ExternalOperationRef != nil {
		t.Fatalf("failed capture returned a response: %+v", response)
	}
}

func successfulCaptureCases() []classificationCase {
	return []classificationCase{
		{name: "captured", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.captured(r)
		}, want: classificationOutcome{sealed: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
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
		}, want: classificationOutcome{sealed: true}},
	}
}

func unknownCaptureCases() []classificationCase {
	return []classificationCase{
		{name: "unknown outcome without an external reference", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorUnknownOutput(h))
		}, want: classificationOutcome{discarded: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			want, _ := json.Marshal(invokeRefactorUnknownOutput(h)["reconciliation_ref"])
			if response.Result.Outcome != adapter.OutcomeUnknown || !sameJSON(response.Result.ExternalOperationRef, want) {
				t.Fatalf("unknown outcome was not normalized: %+v", response.Result)
			}
		}},
		{name: "unknown outcome with the same external reference", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			response, err := h.reply(r, adapter.OutcomeCompleted, invokeRefactorUnknownOutput(h))
			response.Result.ExternalOperationRef, _ = json.Marshal(invokeRefactorUnknownOutput(h)["reconciliation_ref"])
			return response, err
		}, want: classificationOutcome{discarded: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			if response.Result.Outcome != adapter.OutcomeUnknown || len(response.Result.ExternalOperationRef) == 0 {
				t.Fatalf("unknown outcome was not normalized: %+v", response.Result)
			}
		}},
		{name: "unknown outcome reported by the adapter as unknown", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeUnknown, invokeRefactorUnknownOutput(h))
		}, want: classificationOutcome{discarded: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			if response.Result.Outcome != adapter.OutcomeUnknown {
				t.Fatalf("unknown outcome was not normalized: %+v", response.Result)
			}
		}},
		{name: "unknown outcome referencing another adapter", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			output := invokeRefactorUnknownOutput(h)
			output["reconciliation_ref"].(map[string]any)["adapter_id"] = "game.other"
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "unknown outcome referencing another environment", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			output := invokeRefactorUnknownOutput(h)
			output["reconciliation_ref"].(map[string]any)["environment_id"] = "production"
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "unknown outcome with a different external reference", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			response, err := h.reply(r, adapter.OutcomeCompleted, invokeRefactorUnknownOutput(h))
			response.Result.ExternalOperationRef = json.RawMessage(`{"adapter_id":"game.paper","environment_id":"staging","resource_kind":"operation","external_id":"other"}`)
			return response, err
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "unknown outcome and cleanup fails", setup: func(h *captureHarness) context.Context {
			h.sink.failDiscard = true
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorUnknownOutput(h))
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "unknown outcome for another capture", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			output := invokeRefactorUnknownOutput(h)
			output["capture_id"] = "different-capture"
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
	}
}

func rejectedCaptureCases() []classificationCase {
	return []classificationCase{
		{name: "rejected without bytes", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorRejectedOutput(h))
		}, want: classificationOutcome{discarded: true}, check: func(t *testing.T, h *captureHarness, response adapter.InvokeResponse) {
			if response.Result.Outcome != adapter.OutcomeCompleted || len(response.Result.Output) == 0 {
				t.Fatalf("rejected response %+v", response.Result)
			}
		}},
		{name: "rejected after writing bytes", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abc", true); err != nil {
				return adapter.InvokeResponse{}, err
			}
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorRejectedOutput(h))
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "rejected reported by the adapter as unknown", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeUnknown, invokeRefactorRejectedOutput(h))
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "rejected and cleanup fails", setup: func(h *captureHarness) context.Context {
			h.sink.failDiscard = true
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, invokeRefactorRejectedOutput(h))
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
	}
}

func failedCaptureCases() []classificationCase {
	return []classificationCase{
		{name: "captured for another capture", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abc", true); err != nil {
				return adapter.InvokeResponse{}, err
			}
			output := h.output()
			output["capture_id"] = "different-capture"
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured without a stream", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, h.output())
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured with an incomplete stream", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abc", false); err != nil {
				return adapter.InvokeResponse{}, err
			}
			return h.reply(r, adapter.OutcomeCompleted, h.output())
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured with a different size", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abc", true); err != nil {
				return adapter.InvokeResponse{}, err
			}
			output := h.output()
			output["size_bytes"] = 4
			return h.reply(r, adapter.OutcomeCompleted, output)
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "captured with a different digest", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			if err := invokeRefactorStream(h, r, "abd", true); err != nil {
				return adapter.InvokeResponse{}, err
			}
			return h.reply(r, adapter.OutcomeCompleted, h.output())
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "context canceled before sealing", setup: func(h *captureHarness) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			h.t.Cleanup(cancel)
			h.respond = func(r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
				response, err := h.captured(r)
				cancel()
				return response, err
			}
			return ctx
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "seal fails", setup: func(h *captureHarness) context.Context {
			h.sink.failSeal = true
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.captured(r)
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
		{name: "adapter output fails its schema", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return h.reply(r, adapter.OutcomeCompleted, map[string]any{"outcome": "captured"})
		}, want: classificationOutcome{message: "streamed adapter invocation failed", discarded: true}},
		{name: "adapter fails", respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return adapter.InvokeResponse{}, errors.New("adapter exploded")
		}, want: classificationOutcome{message: "streamed adapter invocation failed", discarded: true}},
		{name: "adapter fails and cleanup fails", setup: func(h *captureHarness) context.Context {
			h.sink.failDiscard = true
			return context.Background()
		}, respond: func(h *captureHarness, _ context.Context, r adapter.InvokeRequest) (adapter.InvokeResponse, error) {
			return adapter.InvokeResponse{}, errors.New("adapter exploded")
		}, want: classificationOutcome{message: invokeRefactorUncertainText, discarded: true}},
	}
}
