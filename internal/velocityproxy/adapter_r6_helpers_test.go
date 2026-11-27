package velocityproxy

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
)

// The velocityR6 helpers drive the adapter directly, below the registry, with
// a bridge and address resolver that record every call in order. They pin the
// exact encoded output, the invocation outcome, and the call sequence.

const velocityR6TargetJSON = `{"server_id":"minecraft-test","environment_id":"test","backend_id":"paper-main",` +
	`"instance_ref":{"adapter_id":"hosting.example","environment_id":"test","resource_kind":"instance","external_id":"instance-test-001"}}`

const (
	// velocityR6Now is testNow, the adapter clock, as encoded in outputs.
	velocityR6Now = "2026-11-07T10:00:00Z"
	// velocityR6Seen is velocityR6SeenAt, a proxy observation time.
	velocityR6Seen = "2026-11-07T09:59:55Z"
)

var velocityR6SeenAt = testNow.Add(-5 * time.Second)

// velocityR6Bridge wraps fakeBridge, records calls, and can fail scripted
// InspectBackend calls in order.
type velocityR6Bridge struct {
	*fakeBridge
	calls        *[]string
	inspectFails []bool
}

func (b *velocityR6Bridge) record(call string) { *b.calls = append(*b.calls, call) }

func (b *velocityR6Bridge) InspectBackend(ctx context.Context, backendID string) (BackendObservation, error) {
	b.record("inspect " + backendID)
	if len(b.inspectFails) > 0 {
		fail := b.inspectFails[0]
		b.inspectFails = b.inspectFails[1:]
		if fail {
			return BackendObservation{}, errors.New("velocityR6 inspect unavailable")
		}
	}
	return b.fakeBridge.InspectBackend(ctx, backendID)
}

func (b *velocityR6Bridge) RegisterBackend(ctx context.Context, target BackendTarget, address string) error {
	b.record("register " + target.BackendID + " " + address)
	return b.fakeBridge.RegisterBackend(ctx, target, address)
}

func (b *velocityR6Bridge) DrainBackend(ctx context.Context, target BackendTarget) error {
	b.record("drain " + target.BackendID)
	return b.fakeBridge.DrainBackend(ctx, target)
}

func (b *velocityR6Bridge) UnregisterBackend(ctx context.Context, target BackendTarget) error {
	b.record("unregister " + target.BackendID)
	return b.fakeBridge.UnregisterBackend(ctx, target)
}

// velocityR6Resolver returns a fixed address or error and records each call.
type velocityR6Resolver struct {
	calls   *[]string
	address string
	err     error
}

func (r *velocityR6Resolver) ResolveBackendAddress(_ context.Context, target BackendTarget) (string, error) {
	*r.calls = append(*r.calls, "resolve "+target.BackendID)
	return r.address, r.err
}

type velocityR6Harness struct {
	t        *testing.T
	calls    []string
	bridge   *velocityR6Bridge
	resolver *velocityR6Resolver
	adapter  *Adapter
}

func velocityR6Config() Config {
	return Config{AdapterID: "integration.velocity", EnvironmentID: "test", ProxyInstanceRef: proxyInstance}
}

func newVelocityR6Harness(t *testing.T) *velocityR6Harness {
	t.Helper()
	h := &velocityR6Harness{t: t}
	h.bridge = &velocityR6Bridge{fakeBridge: newFakeBridge(), calls: &h.calls}
	h.resolver = &velocityR6Resolver{calls: &h.calls, address: backendAddress}
	impl, err := newAdapter(velocityR6Config(), "0.21.0-draft", h.bridge, h.resolver, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	h.adapter = impl
	return h
}

// invoke calls the adapter and returns the encoded output and outcome.
func (h *velocityR6Harness) invoke(capability string, input any) (string, adapter.Outcome) {
	h.t.Helper()
	response, err := h.adapter.Invoke(context.Background(), adapter.InvokeRequest{ID: "r6", Params: adapter.InvokeParams{
		Capability: capability, Input: mustJSON(h.t, input),
	}})
	if err != nil {
		h.t.Fatalf("%s: %v", capability, err)
	}
	return string(response.Result.Output), response.Result.Outcome
}

// expect checks the encoded output, outcome, and recorded call sequence.
func (h *velocityR6Harness) expect(gotOutput string, gotOutcome adapter.Outcome, want velocityR6Want) {
	h.t.Helper()
	if gotOutput != want.output {
		h.t.Errorf("output =\n %s\nwant\n %s", gotOutput, want.output)
	}
	if gotOutcome != want.outcome {
		h.t.Errorf("outcome = %q, want %q", gotOutcome, want.outcome)
	}
	if !reflect.DeepEqual(h.calls, want.calls) {
		h.t.Errorf("calls = %q, want %q", h.calls, want.calls)
	}
}

type velocityR6Want struct {
	output  string
	outcome adapter.Outcome
	calls   []string
}

// velocityR6StateJSON encodes a backendStateOutput; count "" is omitted.
func velocityR6StateJSON(registration, routability, count, observedAt string) string {
	countField := ""
	if count != "" {
		countField = `,"active_session_count":` + count
	}
	return `{"target":` + velocityR6TargetJSON + `,"registration_state":"` + registration + `","routability":"` + routability + `"` +
		countField + `,"observed_at":"` + observedAt + `"}`
}

// velocityR6ActionJSON encodes a backendActionOutput; reason "" is omitted.
func velocityR6ActionJSON(outcome, registration, routability, observedAt, reason string) string {
	reasonField := ""
	if reason != "" {
		reasonField = `,"reason_code":"` + reason + `"`
	}
	return `{"target":` + velocityR6TargetJSON + `,"request_outcome":"` + outcome + `","registration_state":"` + registration +
		`","routability":"` + routability + `","observed_at":"` + observedAt + `"` + reasonField + `}`
}

// velocityR6PlayersJSON encodes a playersOutput; count < 0 means unknown.
func velocityR6PlayersJSON(count int, observedAt string) string {
	if count < 0 {
		return `{"target":` + velocityR6TargetJSON + `,"observation_state":"unknown"}`
	}
	return `{"target":` + velocityR6TargetJSON + `,"observation_state":"observed","active_session_count":` + strconv.Itoa(count) +
		`,"observed_at":"` + observedAt + `"}`
}

// velocityR6Backend builds a proxy observation of paper-main.
func velocityR6Backend(state RegistrationState, ref *ResourceRef, routable bool, count *int64, at time.Time) BackendObservation {
	return BackendObservation{Registration: state, InstanceRef: ref, Routable: routable, Address: backendAddress, ActiveSessions: count, ObservedAt: at}
}

func velocityR6Count(value int64) *int64 { return &value }

func velocityR6Ref(ref ResourceRef) *ResourceRef { return &ref }
