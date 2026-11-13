package velocityproxy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

// TestInvokeRejectsBadInputBeforeCallingTheProxy pins the exact error each
// capability returns for undecodable input and for a target this adapter does
// not own. It calls the adapter directly, below the registry's schema check,
// so the adapter's own guards are what is tested.
func TestInvokeRejectsBadInputBeforeCallingTheProxy(t *testing.T) {
	h := newHarness(t)
	otherEnvironment := target
	otherEnvironment.EnvironmentID = "production"
	otherProxy := proxyInstance
	otherProxy.ExternalID = "velocity-edge-002"
	const (
		badInput   = "invalid Velocity capability input"
		outside    = "backend target is outside this Velocity adapter's environment"
		badSession = "session inspection targets a different proxy instance"
		badCohort  = "cohort inspection targets a different proxy instance"
		badMove    = "transfer request is outside this Velocity adapter's environment"
	)
	cases := []struct {
		capability string
		input      any
		want       string
	}{
		{"integration.proxy.backend.inspect", backendInput{Target: otherEnvironment}, outside},
		{"integration.proxy.backend.register", registerInput{Target: otherEnvironment}, outside},
		{"integration.proxy.backend.drain", backendInput{Target: otherEnvironment}, outside},
		{"integration.proxy.backend.unregister", backendInput{Target: otherEnvironment}, outside},
		{"integration.proxy.backend.players.inspect", playersInput{Target: otherEnvironment}, outside},
		{"integration.proxy.session.inspect", sessionInput{ProxyInstanceRef: otherProxy}, badSession},
		{"integration.proxy.cohort.inspect", cohortInput{ProxyInstanceRef: otherProxy}, badCohort},
		{"integration.velocity.players.transfer", transferRequest{EnvironmentID: "production"}, badMove},
	}
	for _, test := range cases {
		t.Run(test.capability, func(t *testing.T) {
			for name, raw := range map[string]json.RawMessage{"undecodable": json.RawMessage(`[`), "foreign": mustJSON(t, test.input)} {
				want := test.want
				if name == "undecodable" {
					want = badInput
				}
				_, err := h.adapter.Invoke(context.Background(), adapter.InvokeRequest{ID: "r", Params: adapter.InvokeParams{Capability: test.capability, Input: raw}})
				if err == nil || err.Error() != want {
					t.Fatalf("%s error = %v, want %q", name, err, want)
				}
			}
		})
	}
	var missing context.Context
	if _, err := h.adapter.Invoke(missing, adapter.InvokeRequest{}); err == nil || err.Error() != "Velocity adapter and invocation context are required" {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := h.adapter.Invoke(context.Background(), adapter.InvokeRequest{Params: adapter.InvokeParams{Capability: "integration.proxy.other"}}); err == nil || err.Error() != "unsupported Velocity capability" {
		t.Fatalf("unknown capability error = %v", err)
	}
	if len(h.bridge.actions) != 0 || len(h.bridge.transfers) != 0 {
		t.Fatalf("proxy was called: actions %v transfers %v", h.bridge.actions, h.bridge.transfers)
	}
}

// TestInvokeStampsTheAdapterClockAndRequestID pins the response envelope.
func TestInvokeStampsTheAdapterClockAndRequestID(t *testing.T) {
	h := newHarness(t)
	response, err := h.adapter.Invoke(context.Background(), adapter.InvokeRequest{ID: "request-7", Params: adapter.InvokeParams{
		Capability: "integration.proxy.backend.inspect", Input: mustJSON(t, backendInput{Target: target}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response.JSONRPC != "2.0" || response.ID != "request-7" || !response.Result.ObservedAt.Equal(testNow) || response.Result.Outcome != adapter.OutcomeCompleted {
		t.Fatalf("response = %+v", response)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
