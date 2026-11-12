package controllerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/velocityproxy"
)

// TestNetworkRequirementsMatchConformanceVector runs the published section 6
// vector through the controller's own planning functions.
func TestNetworkRequirementsMatchConformanceVector(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "protocol", "conformance", "test-vectors", "network-profile-requirements.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Input struct {
			Evaluations []struct {
				CaseID             string         `json:"case_id"`
				EnvironmentID      string         `json:"environment_id"`
				NetworkProfile     NetworkProfile `json:"network_profile"`
				RegisteredAdapters []struct {
					AdapterID     string   `json:"adapter_id"`
					EnvironmentID string   `json:"environment_id"`
					Capabilities  []string `json:"capabilities"`
				} `json:"registered_adapters"`
			} `json:"evaluations"`
		} `json:"input"`
		Expected struct {
			Evaluations []struct {
				CaseID               string                  `json:"case_id"`
				RequiredCapabilities []CapabilityRequirement `json:"required_capabilities"`
				MissingCapabilities  []CapabilityRequirement `json:"missing_capabilities"`
				PlanningPermitted    bool                    `json:"planning_permitted"`
			} `json:"evaluations"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Input.Evaluations) == 0 || len(fixture.Input.Evaluations) != len(fixture.Expected.Evaluations) {
		t.Fatal("vector has no cases or mismatched expectations")
	}
	for i, evaluation := range fixture.Input.Evaluations {
		expected := fixture.Expected.Evaluations[i]
		t.Run(evaluation.CaseID, func(t *testing.T) {
			adapters := map[string]RegisteredAdapter{}
			for _, registered := range evaluation.RegisteredAdapters {
				capabilities := map[string]bool{}
				for _, name := range registered.Capabilities {
					capabilities[name] = true
				}
				adapters[registered.AdapterID] = RegisteredAdapter{EnvironmentID: registered.EnvironmentID, Capabilities: capabilities}
			}
			lookup := func(id string) (RegisteredAdapter, bool) { value, ok := adapters[id]; return value, ok }
			required := NetworkRequirements(evaluation.NetworkProfile)
			missing := MissingCapabilities(evaluation.NetworkProfile, evaluation.EnvironmentID, lookup)
			if len(required) == 0 {
				required = []CapabilityRequirement{}
			}
			if !reflect.DeepEqual(required, nonNil(expected.RequiredCapabilities)) || !reflect.DeepEqual(missing, nonNil(expected.MissingCapabilities)) || (len(missing) == 0) != expected.PlanningPermitted {
				t.Fatalf("required=%v missing=%v; want required=%v missing=%v permitted=%v", required, missing, expected.RequiredCapabilities, expected.MissingCapabilities, expected.PlanningPermitted)
			}
		})
	}
}

func nonNil(values []CapabilityRequirement) []CapabilityRequirement {
	if values == nil {
		return []CapabilityRequirement{}
	}
	return values
}

// fakeBridge answers /v1/info; status lets a test make it fail.
type fakeBridge struct {
	instanceID string
	status     atomic.Int32
}

func (b *fakeBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if code := b.status.Load(); code != 0 {
		w.WriteHeader(int(code))
		return
	}
	if r.Header.Get("Authorization") != "Bearer bridge-secret" || r.URL.Path != "/v1/info" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"api_version": "1", "proxy_platform": "velocity", "proxy_version": "3.4.0",
		"proxy_instance_id": b.instanceID, "features": []string{}})
}

// integrationFixture writes credentials and a config with a Velocity proxy, a
// Cloudflare zone, and a staging server whose backends use managed hostnames.
// dnsImplemented false leaves the DNS adapter declared but not started.
func integrationFixture(t *testing.T, bridgeURL string, dnsImplemented bool) (config.Config, string, string) {
	t.Helper()
	dir := t.TempDir()
	credentials := filepath.Join(dir, "credentials")
	if err := os.Mkdir(credentials, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"velocity-staging-bridge-token": "bridge-secret", "cloudflare-staging-dns-token": "dns-secret"} {
		if err := os.WriteFile(filepath.Join(credentials, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dns := `  - id: integration.cloudflare-dns
    category: integration
`
	if dnsImplemented {
		dns += `    implementation: cloudflare
    secret_refs:
      api_token:
        secret_ref: systemd-credential://cloudflare-staging-dns-token
        purpose: staging DNS edit token
    cloudflare:
      environment_id: staging
      zone_id: 0123456789abcdef0123456789abcdef
      zone_name: example.com
`
	}
	text := fmt.Sprintf(`schema_version: 1
controller:
  mode: local
  state_store:
    type: bbolt
    path: ./state/controller.db
  defaults:
    operation_timeout: 1m
    retry_limit: 2
  audit:
    enabled: true
    destination: local
adapters:
  - id: hosting.local
    category: hosting
  - id: storage.local
    category: storage
  - id: execution.local
    category: execution
  - id: game.paper
    category: game
  - id: integration.velocity
    category: integration
    implementation: velocity
    secret_refs:
      bridge_token:
        secret_ref: systemd-credential://velocity-staging-bridge-token
        purpose: staging proxy bridge token
    velocity:
      environment_id: staging
      bridge_url: %s
      proxy_instance_id: proxy-staging-1
      timeout: 2s
%senvironments:
  - id: production
    classification: production
    production_backup_access: read_write
  - id: staging
    classification: staging
    production_backup_access: read_only
servers:
  - id: staging-server
    environment_id: staging
    adapters:
      hosting: hosting.local
      storage: storage.local
      execution: execution.local
      game: game.paper
    network:
      join_mode: proxy
      proxy: integration.velocity
      dns: integration.cloudflare-dns
      backend_hostname_template: "{backend}.{environment}.mc.example.com"
      backends:
        - id: paper-main
          port: 25565
    lifecycle:
      require_verified_backup_before_destroy: true
      require_approval_for_destroy: true
`, bridgeURL, dns)
	configPath := filepath.Join(dir, "controller.yaml")
	if err := os.WriteFile(configPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return value, configPath, credentials
}

func buildIntegration(t *testing.T, value config.Config, configPath, credentials string) (*Runtime, error) {
	t.Helper()
	return Build(context.Background(), value, configPath, Options{CredentialDirectory: credentials, ControllerVersion: "test-build"})
}

func TestBuildRegistersVelocityAndCloudflare(t *testing.T) {
	bridge := httptest.NewServer(&fakeBridge{instanceID: "proxy-staging-1"})
	defer bridge.Close()
	value, configPath, credentials := integrationFixture(t, bridge.URL, true)
	runtime, err := buildIntegration(t, value, configPath, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if got := strings.Join(runtime.AdapterIDs(), ","); got != "integration.velocity,integration.cloudflare-dns" {
		t.Fatalf("registered adapters = %s", got)
	}
	if len(runtime.UnreachableProxies()) != 0 {
		t.Fatalf("bridge reported unreachable: %v", runtime.UnreachableProxies())
	}
	for id, want := range map[string]string{"integration.velocity": "integration.velocity.players.transfer", "integration.cloudflare-dns": "integration.dns.record.upsert"} {
		descriptor, err := runtime.Registry.Descriptor(id)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, capability := range descriptor.Capabilities {
			found = found || capability.Name == want
		}
		if !found {
			t.Fatalf("%s does not advertise %s", id, want)
		}
	}
	if err := runtime.CheckNetwork("staging-server"); err != nil {
		t.Fatalf("network check: %v", err)
	}
	if err := runtime.CheckNetwork("unknown"); !errors.Is(err, ErrNoNetworkProfile) {
		t.Fatalf("unknown server = %v", err)
	}
}

func TestBuildRefusesWrongBridgeIdentity(t *testing.T) {
	bridge := httptest.NewServer(&fakeBridge{instanceID: "some-other-proxy"})
	defer bridge.Close()
	value, configPath, credentials := integrationFixture(t, bridge.URL, true)
	if _, err := buildIntegration(t, value, configPath, credentials); err == nil || !errors.Is(err, velocityproxy.ErrBridgeIdentity) {
		t.Fatalf("Build error = %v, want a bridge identity failure", err)
	}
}

func TestUnreachableBridgeStartsButRefusesProxyCallsUntilVerified(t *testing.T) {
	fake := &fakeBridge{instanceID: "proxy-staging-1"}
	fake.status.Store(http.StatusServiceUnavailable)
	bridge := httptest.NewServer(fake)
	defer bridge.Close()
	value, configPath, credentials := integrationFixture(t, bridge.URL, true)
	runtime, err := buildIntegration(t, value, configPath, credentials)
	if err != nil {
		t.Fatalf("an unreachable bridge must not stop startup: %v", err)
	}
	defer runtime.Close()
	if got := runtime.UnreachableProxies(); len(got) != 1 || got[0] != "integration.velocity" {
		t.Fatalf("unreachable proxies = %v", got)
	}

	// The same checked bridge refuses calls, without acting, until it verifies.
	parts, err := buildVelocity(value, value.Adapters[4], "test", fileCredentials(credentials))
	if err != nil {
		t.Fatal(err)
	}
	if err := parts.bridge.DrainBackend(context.Background(), velocityproxy.BackendTarget{}); !errors.Is(err, velocityproxy.ErrNotAttempted) {
		t.Fatalf("drain before verification = %v, want not attempted", err)
	}
	fake.status.Store(0)
	if err := parts.bridge.ensure(context.Background()); err != nil {
		t.Fatalf("bridge did not verify once reachable: %v", err)
	}
}

type fileCredentials string

func (dir fileCredentials) WithCredential(reference string, use func([]byte) error) error {
	value, err := os.ReadFile(filepath.Join(string(dir), strings.TrimPrefix(reference, "systemd-credential://")))
	if err != nil {
		return err
	}
	return use(value)
}

func TestCloudflareRecordsAreDerivedFromNetworkProfiles(t *testing.T) {
	value, _, _ := integrationFixture(t, "http://127.0.0.1:1", true)
	records := cloudflareRecords(value, "integration.cloudflare-dns")
	record, ok := records["paper-main.staging.mc.example.com"]
	if len(records) != 1 || !ok || len(record.Purposes) != 1 || record.Purposes[0] != "backend_address" {
		t.Fatalf("records = %+v", records)
	}
}

func TestServerIsUnplannableWithoutItsDNSAdapter(t *testing.T) {
	bridge := httptest.NewServer(&fakeBridge{instanceID: "proxy-staging-1"})
	defer bridge.Close()
	value, configPath, credentials := integrationFixture(t, bridge.URL, false)
	runtime, err := buildIntegration(t, value, configPath, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	err = runtime.CheckNetwork("staging-server")
	var networkErr *NetworkError
	if !errors.As(err, &networkErr) || len(networkErr.Missing) != 3 || networkErr.Missing[0].Role != "dns" {
		t.Fatalf("CheckNetwork = %v, want the three DNS record capabilities missing", err)
	}
	if problems := runtime.NetworkProblems(); problems["staging-server"] == nil {
		t.Fatalf("network problems = %v", problems)
	}
}
