package velocityproxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
)

var testNow = time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

var (
	instance      = ResourceRef{AdapterID: "hosting.example", EnvironmentID: "test", ResourceKind: "instance", ExternalID: "instance-test-001"}
	otherInstance = ResourceRef{AdapterID: "hosting.example", EnvironmentID: "test", ResourceKind: "instance", ExternalID: "instance-test-002"}
	proxyInstance = ResourceRef{AdapterID: "integration.velocity", EnvironmentID: "test", ResourceKind: "proxy-instance", ExternalID: "velocity-edge-001"}
	target        = BackendTarget{ServerID: "minecraft-test", EnvironmentID: "test", BackendID: "paper-main", InstanceRef: instance}
)

// fakeBridge is an in-memory Velocity proxy.
type fakeBridge struct {
	backends      map[string]BackendObservation
	sessions      map[string]SessionObservation
	inspectErr    error
	actionErr     error
	transferErrs  map[string]error
	actionApplies bool
	// registeredAddress is the address passed to the last register call.
	registeredAddress string
	actions           []string
	transfers         []string
}

func newFakeBridge() *fakeBridge {
	return &fakeBridge{backends: map[string]BackendObservation{}, sessions: map[string]SessionObservation{}, transferErrs: map[string]error{}, actionApplies: true}
}

func (b *fakeBridge) InspectBackend(_ context.Context, backendID string) (BackendObservation, error) {
	if b.inspectErr != nil {
		return BackendObservation{}, b.inspectErr
	}
	if observed, ok := b.backends[backendID]; ok {
		return observed, nil
	}
	return BackendObservation{Registration: Unregistered, ObservedAt: testNow}, nil
}

func (b *fakeBridge) act(name string, target BackendTarget, address string, state RegistrationState, routable bool) error {
	b.actions = append(b.actions, name)
	if b.actionErr != nil {
		return b.actionErr
	}
	if b.actionApplies {
		ref := target.InstanceRef
		if address == "" {
			address = b.backends[target.BackendID].Address
		}
		b.backends[target.BackendID] = BackendObservation{Registration: state, InstanceRef: &ref, Address: address, Routable: routable, ObservedAt: testNow}
	}
	return nil
}

func (b *fakeBridge) RegisterBackend(_ context.Context, t BackendTarget, address string) error {
	b.registeredAddress = address
	return b.act("register", t, address, Registered, true)
}
func (b *fakeBridge) DrainBackend(_ context.Context, t BackendTarget) error {
	return b.act("drain", t, "", Draining, false)
}
func (b *fakeBridge) UnregisterBackend(_ context.Context, t BackendTarget) error {
	return b.act("unregister", t, "", Unregistered, false)
}

func (b *fakeBridge) InspectSession(_ context.Context, id string) (SessionObservation, error) {
	if observed, ok := b.sessions[id]; ok {
		return observed, nil
	}
	return SessionObservation{State: Disconnected, ObservedAt: testNow}, nil
}

func (b *fakeBridge) TransferSession(_ context.Context, id, destination string) error {
	b.transfers = append(b.transfers, id)
	if err := b.transferErrs[id]; err != nil {
		return err
	}
	observed := b.sessions[id]
	observed.BackendID = destination
	b.sessions[id] = observed
	return nil
}

// backendIP is the current IP of instance-test-001 in the fake provider.
var backendIP = netip.MustParseAddr("203.0.113.20")

const backendAddress = "203.0.113.20:25565"

type harness struct {
	t        *testing.T
	ips      map[string]netip.Addr
	bridge   *fakeBridge
	adapter  *Adapter
	registry *adapter.Registry
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	bridge := newFakeBridge()
	ips := map[string]netip.Addr{instance.ExternalID: backendIP, otherInstance.ExternalID: netip.MustParseAddr("203.0.113.21")}
	addresses := ConfiguredAddresses{
		Backends: map[string]BackendAddressConfig{"paper-main": {Mode: AddressInstanceIP, Port: 25565}},
		LookupIP: func(_ context.Context, ref ResourceRef) (netip.Addr, error) {
			ip, ok := ips[ref.ExternalID]
			if !ok {
				return netip.Addr{}, errors.New("no such instance")
			}
			return ip, nil
		},
	}
	impl, err := newAdapter(Config{AdapterID: "integration.velocity", EnvironmentID: "test", ProxyInstanceRef: proxyInstance}, schemas.ProtocolVersion(), bridge, addresses, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(impl); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, ips: ips, bridge: bridge, adapter: impl, registry: registry}
}

// invoke calls through the registry, which validates input and output
// against the protocol schemas.
func (h *harness) invoke(capability string, input any) (adapter.InvokeResponse, map[string]any, error) {
	h.t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		h.t.Fatal(err)
	}
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: "request-1", Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: "0.21.0-draft", Capability: capability, Input: encoded,
		RequestContext: adapter.RequestContext{RequestID: "request-1", CorrelationID: "correlation-1", EnvironmentID: "test",
			OperationID: "operation-1", StepID: "step-1", Deadline: time.Now().Add(time.Minute).UTC()},
	}}
	response, err := h.registry.Invoke(context.Background(), "integration.velocity", request)
	if err != nil {
		return response, nil, err
	}
	var output map[string]any
	if err := json.Unmarshal(response.Result.Output, &output); err != nil {
		h.t.Fatal(err)
	}
	return response, output, nil
}

func (h *harness) mustInvoke(capability string, input any) (adapter.InvokeResponse, map[string]any) {
	h.t.Helper()
	response, output, err := h.invoke(capability, input)
	if err != nil {
		h.t.Fatalf("%s: %v", capability, err)
	}
	return response, output
}

func readiness(expires time.Time) ReadinessAssertion {
	return ReadinessAssertion{AssertionID: "readiness-001", ServerID: "minecraft-test", EnvironmentID: "test", InstanceRef: instance,
		ReadinessCheckSetID: "paper-ready-v1", ObservedAt: testNow.Add(-time.Minute), ExpiresAt: expires}
}

func registered(ref ResourceRef, routable bool) BackendObservation {
	return BackendObservation{Registration: Registered, InstanceRef: &ref, Address: backendAddress, Routable: routable, ObservedAt: testNow}
}

func expectState(t *testing.T, output map[string]any, outcome, registration, routability string) {
	t.Helper()
	if outcome != "" && output["request_outcome"] != outcome {
		t.Fatalf("request_outcome = %v, want %s (output %v)", output["request_outcome"], outcome, output)
	}
	if output["registration_state"] != registration || output["routability"] != routability {
		t.Fatalf("state = %v/%v, want %s/%s", output["registration_state"], output["routability"], registration, routability)
	}
}

func TestDescriptorMatchesProtocolExample(t *testing.T) {
	h := newHarness(t)
	data, err := os.ReadFile("../../protocol/examples/adapter-descriptor.velocity.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(h.adapter.Descriptor())
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor differs from protocol example:\n got %s\nwant %s", encoded, data)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	bridge := newFakeBridge()
	cases := map[string]Config{
		"non-integration ID":     {AdapterID: "hosting.velocity", EnvironmentID: "test", ProxyInstanceRef: proxyInstance},
		"missing environment":    {AdapterID: "integration.velocity", ProxyInstanceRef: proxyInstance},
		"proxy ref other env":    {AdapterID: "integration.velocity", EnvironmentID: "prod", ProxyInstanceRef: proxyInstance},
		"proxy ref wrong kind":   {AdapterID: "integration.velocity", EnvironmentID: "test", ProxyInstanceRef: ResourceRef{AdapterID: "integration.velocity", EnvironmentID: "test", ResourceKind: "instance", ExternalID: "x"}},
		"proxy ref wrong owner":  {AdapterID: "integration.velocity", EnvironmentID: "test", ProxyInstanceRef: ResourceRef{AdapterID: "integration.other", EnvironmentID: "test", ResourceKind: "proxy-instance", ExternalID: "x"}},
		"proxy ref missing name": {AdapterID: "integration.velocity", EnvironmentID: "test", ProxyInstanceRef: ResourceRef{AdapterID: "integration.velocity", EnvironmentID: "test", ResourceKind: "proxy-instance"}},
	}
	for name, config := range cases {
		if _, err := New(config, "0.21.0-draft", bridge, ConfiguredAddresses{}); err == nil {
			t.Errorf("%s: config was accepted", name)
		}
	}
	if _, err := New(Config{AdapterID: "integration.velocity", EnvironmentID: "test", ProxyInstanceRef: proxyInstance}, "0.21.0-draft", nil, ConfiguredAddresses{}); err == nil {
		t.Error("nil bridge was accepted")
	}
	if _, err := New(Config{AdapterID: "integration.velocity", EnvironmentID: "test", ProxyInstanceRef: proxyInstance}, "0.21.0-draft", bridge, nil); err == nil {
		t.Error("nil address resolver was accepted")
	}
}

func TestRegisterConfirmsOnlyObservedExactInstance(t *testing.T) {
	h := newHarness(t)
	response, output := h.mustInvoke("integration.proxy.backend.register", registerInput{Target: target, ReadinessAssertion: readiness(testNow.Add(time.Minute))})
	expectState(t, output, "confirmed", "registered", "routable")
	if response.Result.Outcome != adapter.OutcomeCompleted {
		t.Fatalf("outcome = %s", response.Result.Outcome)
	}

	// The proxy accepts the call but has not applied it yet.
	h = newHarness(t)
	h.bridge.actionApplies = false
	response, output = h.mustInvoke("integration.proxy.backend.register", registerInput{Target: target, ReadinessAssertion: readiness(testNow.Add(time.Minute))})
	expectState(t, output, "accepted", "unregistered", "not_routable")
	if response.Result.Outcome != adapter.OutcomeAccepted {
		t.Fatalf("outcome = %s", response.Result.Outcome)
	}

	// The backend name routes to a different instance.
	h = newHarness(t)
	h.bridge.actionApplies = false
	h.bridge.backends["paper-main"] = registered(otherInstance, true)
	_, output = h.mustInvoke("integration.proxy.backend.register", registerInput{Target: target, ReadinessAssertion: readiness(testNow.Add(time.Minute))})
	expectState(t, output, "accepted", "unknown", "not_routable")
}

func TestRegisterRejectsStaleOrMismatchedReadinessWithoutSideEffects(t *testing.T) {
	cases := map[string]struct {
		assertion ReadinessAssertion
		reason    string
	}{
		"expired":     {readiness(testNow.Add(-time.Second)), "readiness_assertion_expired"},
		"expires now": {readiness(testNow), "readiness_assertion_expired"},
		"future": {func() ReadinessAssertion {
			a := readiness(testNow.Add(2 * time.Minute))
			a.ObservedAt = testNow.Add(time.Minute)
			return a
		}(), "readiness_assertion_not_yet_valid"},
		"wrong instance": {func() ReadinessAssertion {
			a := readiness(testNow.Add(time.Minute))
			a.InstanceRef = otherInstance
			return a
		}(), "readiness_assertion_mismatch"},
		"wrong server": {func() ReadinessAssertion { a := readiness(testNow.Add(time.Minute)); a.ServerID = "other"; return a }(), "readiness_assertion_mismatch"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			_, output := h.mustInvoke("integration.proxy.backend.register", registerInput{Target: target, ReadinessAssertion: test.assertion})
			expectState(t, output, "rejected", "unregistered", "not_routable")
			if output["reason_code"] != test.reason {
				t.Fatalf("reason_code = %v, want %s", output["reason_code"], test.reason)
			}
			if len(h.bridge.actions) != 0 {
				t.Fatalf("proxy was called: %v", h.bridge.actions)
			}
		})
	}
}

func TestMutatingBackendCallUncertainty(t *testing.T) {
	h := newHarness(t)
	h.bridge.actionErr = errors.New("connection reset")
	response, output := h.mustInvoke("integration.proxy.backend.drain", backendInput{Target: target})
	expectState(t, output, "outcome_unknown", "unknown", "unknown")
	if response.Result.Outcome != adapter.OutcomeUnknown {
		t.Fatalf("outcome = %s", response.Result.Outcome)
	}

	h = newHarness(t)
	h.bridge.backends["paper-main"] = registered(instance, true)
	h.bridge.actionErr = ErrNotAttempted
	_, output = h.mustInvoke("integration.proxy.backend.unregister", backendInput{Target: target})
	expectState(t, output, "rejected", "registered", "routable")
}

func TestDrainAndUnregisterConfirmPostcondition(t *testing.T) {
	h := newHarness(t)
	h.bridge.backends["paper-main"] = registered(instance, true)
	_, output := h.mustInvoke("integration.proxy.backend.drain", backendInput{Target: target})
	expectState(t, output, "confirmed", "draining", "not_routable")
	_, output = h.mustInvoke("integration.proxy.backend.unregister", backendInput{Target: target})
	expectState(t, output, "confirmed", "unregistered", "not_routable")

	// A drain the proxy has not applied yet never reports routable.
	h = newHarness(t)
	h.bridge.backends["paper-main"] = registered(instance, true)
	h.bridge.actionApplies = false
	_, output = h.mustInvoke("integration.proxy.backend.drain", backendInput{Target: target})
	expectState(t, output, "accepted", "registered", "unknown")
}

func TestInspectBindsStateToExactInstance(t *testing.T) {
	count := int64(4)
	cases := map[string]struct {
		observed                  BackendObservation
		inspectErr                error
		registration, routability string
		wantCount                 bool
	}{
		"routable":           {observed: BackendObservation{Registration: Registered, InstanceRef: &instance, Address: backendAddress, Routable: true, ActiveSessions: &count, ObservedAt: testNow}, registration: "registered", routability: "routable", wantCount: true},
		"registered not yet": {observed: registered(instance, false), registration: "registered", routability: "not_routable"},
		"other instance":     {observed: BackendObservation{Registration: Registered, InstanceRef: &otherInstance, Routable: true, ActiveSessions: &count, ObservedAt: testNow}, registration: "unknown", routability: "not_routable"},
		"draining":           {observed: BackendObservation{Registration: Draining, InstanceRef: &instance, ObservedAt: testNow}, registration: "draining", routability: "not_routable"},
		"proxy unknown":      {observed: BackendObservation{Registration: Unknown, ObservedAt: testNow}, registration: "unknown", routability: "unknown"},
		"proxy unreachable":  {inspectErr: errors.New("timeout"), registration: "unknown", routability: "unknown"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.bridge.backends["paper-main"] = test.observed
			h.bridge.inspectErr = test.inspectErr
			_, output := h.mustInvoke("integration.proxy.backend.inspect", backendInput{Target: target})
			expectState(t, output, "", test.registration, test.routability)
			if _, has := output["active_session_count"]; has != test.wantCount {
				t.Fatalf("active_session_count present = %v, want %v", has, test.wantCount)
			}
		})
	}
}

func TestBackendCallsRejectOtherEnvironment(t *testing.T) {
	h := newHarness(t)
	other := target
	other.EnvironmentID = "production"
	if _, _, err := h.invoke("integration.proxy.backend.inspect", backendInput{Target: other}); err == nil {
		t.Fatal("target in another environment was accepted")
	}
	other = target
	other.InstanceRef.EnvironmentID = "production"
	if _, _, err := h.invoke("integration.proxy.backend.register", registerInput{Target: other, ReadinessAssertion: readiness(testNow.Add(time.Minute))}); err == nil {
		t.Fatal("instance in another environment was accepted")
	}
	if len(h.bridge.actions) != 0 {
		t.Fatal("proxy was called for an out-of-environment target")
	}
}

func TestPlayersInspectRequiresFreshBoundCount(t *testing.T) {
	count := int64(3)
	at := func(offset time.Duration) BackendObservation {
		return BackendObservation{Registration: Registered, InstanceRef: &instance, Address: backendAddress, Routable: true, ActiveSessions: &count, ObservedAt: testNow.Add(offset)}
	}
	cases := map[string]struct {
		observed BackendObservation
		want     string
	}{
		"fresh":          {at(-10 * time.Second), "observed"},
		"stale":          {at(-31 * time.Second), "unknown"},
		"future":         {at(time.Second), "unknown"},
		"no count":       {registered(instance, true), "unknown"},
		"other instance": {BackendObservation{Registration: Registered, InstanceRef: &otherInstance, ActiveSessions: &count, ObservedAt: testNow}, "unknown"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.bridge.backends["paper-main"] = test.observed
			_, output := h.mustInvoke("integration.proxy.backend.players.inspect", playersInput{Target: target, MaximumAgeSeconds: 30})
			if output["observation_state"] != test.want {
				t.Fatalf("observation_state = %v, want %s", output["observation_state"], test.want)
			}
			if test.want == "observed" && output["active_session_count"] != float64(3) {
				t.Fatalf("active_session_count = %v", output["active_session_count"])
			}
		})
	}
}

func TestSessionInspect(t *testing.T) {
	h := newHarness(t)
	h.bridge.sessions["proxy-session-003"] = SessionObservation{State: Connected, BackendID: "velocity-lobby", WaitingCohortID: "cohort-001", ObservedAt: testNow.Add(-2 * time.Second)}
	h.bridge.sessions["proxy-session-stale"] = SessionObservation{State: Connected, BackendID: "velocity-lobby", ObservedAt: testNow.Add(-time.Minute)}
	input := sessionInput{ProxyInstanceRef: proxyInstance, ProxySessionID: "proxy-session-003", MaximumObservationAgeSeconds: 30}
	_, output := h.mustInvoke("integration.proxy.session.inspect", input)
	if output["connection_state"] != "connected" || output["current_backend_id"] != "velocity-lobby" {
		t.Fatalf("unexpected session output: %v", output)
	}
	if len(output) != 5 {
		t.Fatalf("session output carries extra fields: %v", output)
	}

	input.ProxySessionID = "proxy-session-stale"
	_, output = h.mustInvoke("integration.proxy.session.inspect", input)
	if output["connection_state"] != "unknown" {
		t.Fatalf("stale observation reported %v", output["connection_state"])
	}

	input.ProxyInstanceRef.ExternalID = "velocity-edge-002"
	if _, _, err := h.invoke("integration.proxy.session.inspect", input); err == nil {
		t.Fatal("session inspect for another proxy instance was accepted")
	}
}

func transferInput(ids ...string) transferRequest {
	return transferRequest{ServerID: "minecraft-test", EnvironmentID: "test", SourceBackendID: "velocity-lobby", DestinationBackendID: "paper-main",
		WaitingCohortID: "cohort-001", ExpectedInstanceRef: instance, ReadinessAssertion: readiness(testNow.Add(time.Minute)), PlayerSessionIDs: ids}
}

func (h *harness) connect(id, backend, cohort string) {
	h.bridge.sessions[id] = SessionObservation{State: Connected, BackendID: backend, WaitingCohortID: cohort, ObservedAt: testNow}
}

func resultsByID(t *testing.T, output map[string]any) map[string]map[string]any {
	t.Helper()
	byID := map[string]map[string]any{}
	for _, entry := range output["results"].([]any) {
		result := entry.(map[string]any)
		byID[result["proxy_session_id"].(string)] = result
	}
	return byID
}

func TestTransferReportsOneOutcomePerSession(t *testing.T) {
	h := newHarness(t)
	h.bridge.backends["paper-main"] = registered(instance, true)
	h.connect("s-move", "velocity-lobby", "cohort-001")
	h.connect("s-there", "paper-main", "cohort-001")
	h.connect("s-other-cohort", "velocity-lobby", "cohort-002")
	h.connect("s-other-backend", "paper-other", "cohort-001")
	h.connect("s-fails", "velocity-lobby", "cohort-001")
	h.connect("s-unknown", "velocity-lobby", "cohort-001")
	h.bridge.sessions["s-stale"] = SessionObservation{State: Connected, BackendID: "velocity-lobby", WaitingCohortID: "cohort-001", ObservedAt: testNow.Add(-time.Minute)}
	h.bridge.transferErrs["s-fails"] = ErrNotAttempted
	h.bridge.transferErrs["s-unknown"] = errors.New("proxy-session s-unknown timed out")

	ids := []string{"s-move", "s-there", "s-gone", "s-other-cohort", "s-other-backend", "s-fails", "s-unknown", "s-stale"}
	response, output := h.mustInvoke("integration.velocity.players.transfer", transferInput(ids...))
	want := map[string]string{
		"s-move": "transferred", "s-there": "already_at_destination", "s-gone": "disconnected",
		"s-other-cohort": "rejected", "s-other-backend": "rejected", "s-fails": "failed",
		"s-unknown": "outcome_unknown", "s-stale": "failed",
	}
	got := resultsByID(t, output)
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for id, outcome := range want {
		if got[id]["outcome"] != outcome {
			t.Errorf("%s: outcome = %v, want %s", id, got[id]["outcome"], outcome)
		}
	}
	counts := output["outcome_counts"].(map[string]any)
	if counts["transferred"] != float64(1) || counts["rejected"] != float64(2) || counts["failed"] != float64(2) || counts["outcome_unknown"] != float64(1) {
		t.Fatalf("unexpected counts: %v", counts)
	}
	if output["requested_count"] != float64(len(ids)) {
		t.Fatalf("requested_count = %v", output["requested_count"])
	}
	if response.Result.Outcome != adapter.OutcomeUnknown {
		t.Fatalf("a batch with an unknown session must report outcome_unknown, got %s", response.Result.Outcome)
	}
	if !reflect.DeepEqual(h.bridge.transfers, []string{"s-move", "s-fails", "s-unknown"}) {
		t.Fatalf("transfers attempted for %v", h.bridge.transfers)
	}
	// Bridge diagnostics and session IDs never reach error messages.
	for id, result := range got {
		if problem, ok := result["error"].(map[string]any); ok {
			if message := problem["message"].(string); strings.Contains(message, "s-") || strings.Contains(message, id) {
				t.Errorf("%s: error message leaks a session ID: %q", id, message)
			}
		}
	}
}

func TestTransferRefusesUnsafeBatches(t *testing.T) {
	cases := map[string]struct {
		mutate      func(*harness, *transferRequest)
		wantOutcome string
		wantCode    string
	}{
		"expired readiness": {func(_ *harness, r *transferRequest) { r.ReadinessAssertion = readiness(testNow.Add(-time.Second)) }, "rejected", "readiness_assertion_expired"},
		"readiness for other instance": {func(_ *harness, r *transferRequest) {
			r.ReadinessAssertion.InstanceRef = otherInstance
		}, "rejected", "readiness_assertion_mismatch"},
		"destination bound elsewhere": {func(h *harness, _ *transferRequest) {
			h.bridge.backends["paper-main"] = registered(otherInstance, true)
		}, "rejected", "destination_not_routable"},
		"destination draining": {func(h *harness, _ *transferRequest) {
			h.bridge.backends["paper-main"] = BackendObservation{Registration: Draining, InstanceRef: &instance, ObservedAt: testNow}
		}, "rejected", "destination_not_routable"},
		"destination unobservable": {func(h *harness, _ *transferRequest) { h.bridge.inspectErr = errors.New("down") }, "failed", "destination_state_unavailable"},
		"source is destination":    {func(_ *harness, r *transferRequest) { r.SourceBackendID = r.DestinationBackendID }, "rejected", "source_is_destination"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.bridge.backends["paper-main"] = registered(instance, true)
			h.connect("s-1", "velocity-lobby", "cohort-001")
			h.connect("s-2", "velocity-lobby", "cohort-001")
			request := transferInput("s-1", "s-2")
			test.mutate(h, &request)
			_, output := h.mustInvoke("integration.velocity.players.transfer", request)
			for id, result := range resultsByID(t, output) {
				if result["outcome"] != test.wantOutcome || result["error"].(map[string]any)["code"] != test.wantCode {
					t.Errorf("%s: got %v", id, result)
				}
			}
			if len(h.bridge.transfers) != 0 {
				t.Fatalf("transfers attempted: %v", h.bridge.transfers)
			}
		})
	}
}

func TestTransferRejectsOtherEnvironment(t *testing.T) {
	h := newHarness(t)
	request := transferInput("s-1")
	request.EnvironmentID = "production"
	if _, _, err := h.invoke("integration.velocity.players.transfer", request); err == nil {
		t.Fatal("transfer in another environment was accepted")
	}
}

func TestValidateTransferResultInvariants(t *testing.T) {
	request := transferInput("s-1", "s-2")
	good := transferResult{WaitingCohortID: "cohort-001", SourceBackendID: "velocity-lobby", DestinationBackendID: "paper-main", TargetInstanceRef: instance,
		RequestedCount: 2, OutcomeCounts: outcomeCounts{Transferred: 1, Disconnected: 1},
		Results: []sessionResult{{ProxySessionID: "s-2", Outcome: "disconnected"}, {ProxySessionID: "s-1", Outcome: "transferred"}}}
	if err := validateTransferResult(request, good); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	cases := map[string]func(*transferResult){
		"wrong instance":    func(r *transferResult) { r.TargetInstanceRef = otherInstance },
		"wrong count":       func(r *transferResult) { r.RequestedCount = 3 },
		"duplicate session": func(r *transferResult) { r.Results[0].ProxySessionID = "s-1" },
		"extra session":     func(r *transferResult) { r.Results[0].ProxySessionID = "s-3" },
		"counts mismatch":   func(r *transferResult) { r.OutcomeCounts = outcomeCounts{Transferred: 2} },
	}
	for name, mutate := range cases {
		result := good
		result.Results = append([]sessionResult(nil), good.Results...)
		mutate(&result)
		if err := validateTransferResult(request, result); err == nil {
			t.Errorf("%s: invalid result accepted", name)
		}
	}
}

func TestRegisterSendsResolvedInstanceAddress(t *testing.T) {
	h := newHarness(t)
	h.mustInvoke("integration.proxy.backend.register", registerInput{Target: target, ReadinessAssertion: readiness(testNow.Add(time.Minute))})
	if h.bridge.registeredAddress != backendAddress {
		t.Fatalf("registered address = %q, want %q", h.bridge.registeredAddress, backendAddress)
	}

	// Vultr reports 0.0.0.0 until an IP is assigned; registration must wait.
	h = newHarness(t)
	h.ips[instance.ExternalID] = netip.IPv4Unspecified()
	_, output := h.mustInvoke("integration.proxy.backend.register", registerInput{Target: target, ReadinessAssertion: readiness(testNow.Add(time.Minute))})
	expectState(t, output, "rejected", "unregistered", "not_routable")
	if output["reason_code"] != "backend_address_unavailable" || len(h.bridge.actions) != 0 {
		t.Fatalf("unassigned IP: output %v, actions %v", output, h.bridge.actions)
	}
}

func TestInstanceIPChangeMakesBackendNotRoutable(t *testing.T) {
	h := newHarness(t)
	h.bridge.backends["paper-main"] = registered(instance, true)
	_, output := h.mustInvoke("integration.proxy.backend.inspect", backendInput{Target: target})
	expectState(t, output, "", "registered", "routable")

	h.ips[instance.ExternalID] = netip.MustParseAddr("203.0.113.99")
	_, output = h.mustInvoke("integration.proxy.backend.inspect", backendInput{Target: target})
	expectState(t, output, "", "unknown", "not_routable")

	// Transfers refuse a destination whose address is stale.
	h.connect("s-1", "velocity-lobby", "cohort-001")
	_, output = h.mustInvoke("integration.velocity.players.transfer", transferInput("s-1"))
	if got := resultsByID(t, output)["s-1"]; got["outcome"] != "rejected" || len(h.bridge.transfers) != 0 {
		t.Fatalf("stale destination: %v, transfers %v", got, h.bridge.transfers)
	}

	// Re-registering with a fresh readiness assertion picks up the new IP.
	_, output = h.mustInvoke("integration.proxy.backend.register", registerInput{Target: target, ReadinessAssertion: readiness(testNow.Add(time.Minute))})
	expectState(t, output, "confirmed", "registered", "routable")
	if h.bridge.registeredAddress != "203.0.113.99:25565" {
		t.Fatalf("re-registered address = %q", h.bridge.registeredAddress)
	}
}

func TestConfiguredAddresses(t *testing.T) {
	lookup := func(_ context.Context, _ ResourceRef) (netip.Addr, error) {
		return netip.MustParseAddr("2001:db8::5"), nil
	}
	addresses := ConfiguredAddresses{
		Backends: map[string]BackendAddressConfig{
			"by-ip":   {Mode: AddressInstanceIP, Port: 25565},
			"by-name": {Mode: AddressHostname, Hostname: "Paper-Main.Backends.Example.com.", Port: 25566},
		},
		LookupIP: lookup,
	}
	if err := addresses.Validate(); err != nil {
		t.Fatal(err)
	}
	resolve := func(backend string) string {
		t.Helper()
		value, err := addresses.ResolveBackendAddress(context.Background(), BackendTarget{BackendID: backend, InstanceRef: instance})
		if err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
		return value
	}
	if got := resolve("by-ip"); got != "[2001:db8::5]:25565" {
		t.Errorf("by-ip = %q", got)
	}
	if got := resolve("by-name"); got != "paper-main.backends.example.com:25566" {
		t.Errorf("by-name = %q", got)
	}
	if _, err := addresses.ResolveBackendAddress(context.Background(), BackendTarget{BackendID: "missing"}); err == nil {
		t.Error("backend without a setting resolved")
	}
	invalid := []ConfiguredAddresses{
		{},
		{Backends: map[string]BackendAddressConfig{"a": {Mode: AddressInstanceIP, Port: 25565}}},
		{Backends: map[string]BackendAddressConfig{"a": {Mode: AddressHostname, Hostname: "203.0.113.5", Port: 25565}}},
		{Backends: map[string]BackendAddressConfig{"a": {Mode: AddressHostname, Hostname: "bad_name.example.com", Port: 25565}}},
		{Backends: map[string]BackendAddressConfig{"a": {Mode: AddressHostname, Hostname: "ok.example.com"}}},
		{Backends: map[string]BackendAddressConfig{"a": {Mode: "dns", Port: 25565}}, LookupIP: lookup},
	}
	for i, config := range invalid {
		if err := config.Validate(); err == nil {
			t.Errorf("invalid config %d accepted", i)
		}
	}
	if !sameAddress("PAPER.example.com.:25565", "paper.example.com:25565") || sameAddress("203.0.113.1:25565", "203.0.113.1:25566") || !sameAddress("[::ffff:203.0.113.1]:25565", "203.0.113.1:25565") {
		t.Error("sameAddress comparison is wrong")
	}
}
