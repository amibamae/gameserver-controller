package velocityproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type staticCredential string

func (c staticCredential) WithCredential(_ string, use func([]byte) error) error {
	return use([]byte(c))
}

func newTestBridge(t *testing.T, handler http.HandlerFunc) *HTTPBridge {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bridge-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	bridge, err := NewHTTPBridge(HTTPBridgeConfig{BaseURL: server.URL, TokenRef: "systemd-credential://velocity-bridge-token", ProxyInstanceID: "velocity-edge-001"}, staticCredential("bridge-token\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	bridge.now = func() time.Time { return testNow }
	return bridge
}

const registeredBackendJSON = `{"backend_id":"paper-main","registration":"registered","instance_ref":{"adapter_id":"hosting.example","environment_id":"test","resource_kind":"instance","external_id":"instance-test-001"},"address":"203.0.113.20:25565","routable":true,"active_sessions":2,"observed_at":"2026-11-07T10:00:00Z"}`

func TestNewHTTPBridgeValidatesConfig(t *testing.T) {
	cases := map[string]HTTPBridgeConfig{
		"plain HTTP to remote host": {BaseURL: "http://203.0.113.5:25580", TokenRef: "systemd-credential://t", ProxyInstanceID: "p"},
		"path in URL":               {BaseURL: "http://127.0.0.1:25580/api", TokenRef: "systemd-credential://t", ProxyInstanceID: "p"},
		"user info":                 {BaseURL: "https://user:pass@proxy.example.com", TokenRef: "systemd-credential://t", ProxyInstanceID: "p"},
		"token not a credential":    {BaseURL: "http://127.0.0.1:25580", TokenRef: "bridge-token", ProxyInstanceID: "p"},
		"missing instance":          {BaseURL: "http://127.0.0.1:25580", TokenRef: "systemd-credential://t"},
		"other scheme":              {BaseURL: "ftp://127.0.0.1", TokenRef: "systemd-credential://t", ProxyInstanceID: "p"},
	}
	for name, config := range cases {
		if _, err := NewHTTPBridge(config, staticCredential("x"), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, base := range []string{"http://127.0.0.1:25580", "http://[::1]:25580", "http://localhost:25580", "https://proxy.example.com"} {
		if _, err := NewHTTPBridge(HTTPBridgeConfig{BaseURL: base, TokenRef: "systemd-credential://t", ProxyInstanceID: "p"}, staticCredential("x"), nil); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
}

func TestHTTPBridgeCheckInfo(t *testing.T) {
	info := `{"api_version":"1","proxy_platform":"velocity","proxy_version":"3.4.0","proxy_instance_id":"velocity-edge-001","features":["backends"]}`
	bridge := newTestBridge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/info" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, info)
	})
	if err := bridge.CheckInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	info = strings.Replace(info, "velocity-edge-001", "velocity-edge-002", 1)
	if err := bridge.CheckInfo(context.Background()); err == nil {
		t.Fatal("wrong proxy instance accepted")
	}
	info = strings.Replace(info, `"api_version":"1"`, `"api_version":"2"`, 1)
	if err := bridge.CheckInfo(context.Background()); err == nil {
		t.Fatal("unsupported API version accepted")
	}
}

func TestHTTPBridgeBackendCalls(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	bridge := newTestBridge(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody = nil
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
		}
		_, _ = io.WriteString(w, registeredBackendJSON)
	})
	ctx := context.Background()
	observed, err := bridge.InspectBackend(ctx, "paper-main")
	if err != nil || observed.Registration != Registered || !observed.Routable || observed.Address != "203.0.113.20:25565" || *observed.InstanceRef != instance || *observed.ActiveSessions != 2 {
		t.Fatalf("InspectBackend = %+v, %v", observed, err)
	}
	if err := bridge.RegisterBackend(ctx, target, "203.0.113.20:25565"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPut || gotPath != "/v1/backends/paper-main" || gotBody["address"] != "203.0.113.20:25565" {
		t.Fatalf("register sent %s %s %v", gotMethod, gotPath, gotBody)
	}
	if err := bridge.DrainBackend(ctx, target); err != nil || gotMethod != http.MethodPost || gotPath != "/v1/backends/paper-main/drain" {
		t.Fatalf("drain sent %s %s: %v", gotMethod, gotPath, err)
	}
	if err := bridge.UnregisterBackend(ctx, target); err != nil || gotMethod != http.MethodDelete || gotPath != "/v1/backends/paper-main" {
		t.Fatalf("unregister sent %s %s: %v", gotMethod, gotPath, err)
	}
	if _, err := bridge.InspectBackend(ctx, "../admin"); err == nil {
		t.Fatal("invalid backend ID was sent")
	}
}

func TestHTTPBridgeErrorClassification(t *testing.T) {
	status := http.StatusConflict
	bridge := newTestBridge(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":{"code":"backend_not_managed","message":"Backend is configured statically."}}`)
	})
	ctx := context.Background()
	if err := bridge.RegisterBackend(ctx, target, "203.0.113.20:25565"); !errors.Is(err, ErrNotAttempted) {
		t.Fatalf("409 must be not attempted, got %v", err)
	}
	status = http.StatusBadGateway
	if err := bridge.DrainBackend(ctx, target); err == nil || errors.Is(err, ErrNotAttempted) {
		t.Fatalf("5xx must be an unknown outcome, got %v", err)
	}

	// A connection error is an unknown outcome and never leaks the URL.
	closed, err := NewHTTPBridge(HTTPBridgeConfig{BaseURL: "http://127.0.0.1:1", TokenRef: "systemd-credential://t", ProxyInstanceID: "p"}, staticCredential("x"), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = closed.TransferSession(ctx, "secret-session-id", "paper-main")
	if err == nil || errors.Is(err, ErrNotAttempted) || strings.Contains(err.Error(), "secret-session-id") {
		t.Fatalf("transport error = %v", err)
	}
}

func TestHTTPBridgeRejectsMalformedBackendState(t *testing.T) {
	body := ""
	bridge := newTestBridge(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
	cases := map[string]string{
		"other backend":          strings.Replace(registeredBackendJSON, `"backend_id":"paper-main"`, `"backend_id":"paper-other"`, 1),
		"registered no instance": `{"backend_id":"paper-main","registration":"registered","instance_ref":null,"address":"203.0.113.20:25565","routable":true,"observed_at":"2026-11-07T10:00:00Z"}`,
		"unknown registration":   strings.Replace(registeredBackendJSON, `"registration":"registered"`, `"registration":"maybe"`, 1),
		"missing observed_at":    `{"backend_id":"paper-main","registration":"unregistered","routable":false}`,
		"not JSON":               "oops",
	}
	for name, value := range cases {
		body = value
		if _, err := bridge.InspectBackend(context.Background(), "paper-main"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	body = `{"backend_id":"paper-main","registration":"draining","instance_ref":{"adapter_id":"hosting.example","environment_id":"test","resource_kind":"instance","external_id":"instance-test-001"},"address":"203.0.113.20:25565","routable":true,"observed_at":"2026-11-07T10:00:00Z"}`
	if observed, err := bridge.InspectBackend(context.Background(), "paper-main"); err != nil || observed.Routable {
		t.Fatalf("a draining backend must never be routable: %+v, %v", observed, err)
	}
}

func TestHTTPBridgeSessionsAndTransfer(t *testing.T) {
	outcome := "transferred"
	bridge := newTestBridge(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/sessions/s-1":
			_, _ = io.WriteString(w, `{"session_id":"s-1","state":"connected","backend_id":"lobby","waiting_cohort_id":"waiting-paper-main","observed_at":"2026-11-07T10:00:00Z"}`)
		case r.URL.Path == "/v1/sessions/s-gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"session_not_found","message":"Session not found."}}`)
		case r.URL.Path == "/v1/sessions/s-1/transfer" && r.Method == http.MethodPost:
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["destination_backend_id"] != "paper-main" {
				t.Errorf("transfer body = %v", body)
			}
			_, _ = fmt.Fprintf(w, `{"outcome":%q}`, outcome)
		case r.URL.Path == "/v1/cohorts/waiting-paper-main":
			_, _ = io.WriteString(w, `{"cohort_id":"waiting-paper-main","session_ids":["s-1","s-2"],"observed_at":"2026-11-07T10:00:00Z"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	observed, err := bridge.InspectSession(ctx, "s-1")
	if err != nil || observed.State != Connected || observed.BackendID != "lobby" || observed.WaitingCohortID != "waiting-paper-main" {
		t.Fatalf("InspectSession = %+v, %v", observed, err)
	}
	if observed, err := bridge.InspectSession(ctx, "s-gone"); err != nil || observed.State != Disconnected || !observed.ObservedAt.Equal(testNow) {
		t.Fatalf("unknown session = %+v, %v", observed, err)
	}
	if err := bridge.TransferSession(ctx, "s-1", "paper-main"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"failed", "not_attempted"} {
		outcome = value
		if err := bridge.TransferSession(ctx, "s-1", "paper-main"); !errors.Is(err, ErrNotAttempted) {
			t.Fatalf("%s: got %v", value, err)
		}
	}
	outcome = "maybe"
	if err := bridge.TransferSession(ctx, "s-1", "paper-main"); err == nil || errors.Is(err, ErrNotAttempted) {
		t.Fatalf("unrecognized outcome must be unknown, got %v", err)
	}
	ids, at, err := bridge.WaitingCohort(ctx, "waiting-paper-main")
	if err != nil || len(ids) != 2 || !at.Equal(testNow) {
		t.Fatalf("WaitingCohort = %v, %v, %v", ids, at, err)
	}
	if _, err := bridge.InspectSession(ctx, "s-1/transfer"); err == nil {
		t.Fatal("session ID with a path separator was sent")
	}
}

func TestHTTPBridgeWorksWithAdapter(t *testing.T) {
	// End-to-end: the adapter registers through the HTTP client, then
	// confirms by inspecting.
	registeredState := `{"backend_id":"paper-main","registration":"unregistered","instance_ref":null,"address":null,"routable":false,"observed_at":"2026-11-07T10:00:00Z"}`
	bridge := newTestBridge(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			registeredState = registeredBackendJSON
		}
		_, _ = io.WriteString(w, registeredState)
	})
	h := newHarness(t)
	impl, err := newAdapter(h.adapter.config, h.adapter.protocolVersion, bridge, h.adapter.addresses, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(registerInput{Target: target, ReadinessAssertion: readiness(testNow.Add(time.Minute))})
	output, outcome, err := impl.registerBackend(context.Background(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	if result := output.(backendActionOutput); result.RequestOutcome != "confirmed" || outcome != "completed" {
		t.Fatalf("register through HTTP bridge = %+v (%s)", result, outcome)
	}
}
