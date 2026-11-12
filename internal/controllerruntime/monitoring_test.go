package controllerruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/integrationmonitor"
)

// ntfyRecorder is a loopback ntfy server that records published messages.
type ntfyRecorder struct {
	mu       sync.Mutex
	messages []string
	auth     []string
}

func (n *ntfyRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	n.mu.Lock()
	n.messages = append(n.messages, r.Header.Get("Title")+": "+string(body))
	n.auth = append(n.auth, r.Header.Get("Authorization"))
	n.mu.Unlock()
}

func (n *ntfyRecorder) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.messages)
}

// monitoringFixture extends the integration fixture with a one-second probe
// policy and an ntfy notifier in the proxy's environment.
func monitoringFixture(t *testing.T, bridgeURL, ntfyURL string) (config.Config, string, string) {
	t.Helper()
	_, configPath, credentials := integrationFixture(t, bridgeURL, true)
	if err := os.WriteFile(filepath.Join(credentials, "ntfy-staging-token"), []byte("tk_staging"), 0o600); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(text), "  audit:\n", `  monitoring:
    proxy:
      check_interval: 1s
      failure_threshold: 1
      initial_backoff: 1s
      max_backoff: 1s
  audit:
`, 1)
	updated = strings.Replace(updated, "environments:\n", `  - id: integration.notify-ntfy
    category: integration
    implementation: ntfy
    secret_refs:
      access_token:
        secret_ref: systemd-credential://ntfy-staging-token
        purpose: staging ntfy publish token
    ntfy:
      environment_id: staging
      server_url: `+ntfyURL+`
      topic: gsc-staging
environments:
`, 1)
	if err := os.WriteFile(configPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return value, configPath, credentials
}

func TestProxyOutageAndRecoveryAreNotified(t *testing.T) {
	fake := &fakeBridge{instanceID: "proxy-staging-1"}
	bridge := httptest.NewServer(fake)
	defer bridge.Close()
	recorder := &ntfyRecorder{}
	ntfy := httptest.NewServer(recorder)
	defer ntfy.Close()
	value, configPath, credentials := monitoringFixture(t, bridge.URL, ntfy.URL)
	runtime, err := buildIntegration(t, value, configPath, credentials)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if got := runtime.MonitoredIntegrations(); len(got) != 1 || got[0] != "integration.velocity" {
		t.Fatalf("monitored = %v", got)
	}
	if got := runtime.Notifiers(); len(got) != 1 || got[0] != "integration.notify-ntfy" {
		t.Fatalf("notifiers = %v", got)
	}
	var logMu sync.Mutex
	var logged []map[string]any
	runtime.StartMonitoring(context.Background(), func(message string, fields map[string]any) {
		logMu.Lock()
		defer logMu.Unlock()
		line := map[string]any{"message": message}
		for name, value := range fields {
			line[name] = value
		}
		logged = append(logged, line)
	})

	wait := func(count int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for recorder.count() < count {
			if time.Now().After(deadline) {
				t.Fatalf("got %d notifications, want %d", recorder.count(), count)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	fake.status.Store(http.StatusServiceUnavailable)
	wait(1)
	fake.status.Store(0)
	wait(2)
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if !strings.HasPrefix(recorder.messages[0], "Proxy outage: Proxy integration.velocity (proxy-staging-1, staging) is unreachable") ||
		!strings.HasPrefix(recorder.messages[1], "Proxy recovered: ") || recorder.auth[0] != "Bearer tk_staging" {
		t.Fatalf("messages = %q, auth = %q", recorder.messages, recorder.auth)
	}
	logMu.Lock()
	defer logMu.Unlock()
	encoded, _ := json.Marshal(logged)
	for _, secret := range []string{"tk_staging", "bridge-secret", "systemd-credential://"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("controller log contains %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(string(encoded), integrationmonitor.EventUnreachable) || !strings.Contains(string(encoded), integrationmonitor.EventRecovered) {
		t.Fatalf("controller log = %s", encoded)
	}
}

func TestFailedProbeMakesProxyCallsRecheckIdentity(t *testing.T) {
	fake := &fakeBridge{instanceID: "proxy-staging-1"}
	bridge := httptest.NewServer(fake)
	defer bridge.Close()
	value, _, credentials := integrationFixture(t, bridge.URL, true)
	parts, err := buildVelocity(value, value.Adapters[4], "test", fileCredentials(credentials))
	if err != nil {
		t.Fatal(err)
	}
	if got := parts.bridge.Probe(context.Background()); got != integrationmonitor.Up || !parts.bridge.verified {
		t.Fatalf("probe = %s, verified = %v", got, parts.bridge.verified)
	}
	fake.status.Store(http.StatusServiceUnavailable)
	if got := parts.bridge.Probe(context.Background()); got != integrationmonitor.Unreachable || parts.bridge.verified {
		t.Fatalf("probe = %s, verified = %v; a failed probe must clear verification", got, parts.bridge.verified)
	}
	fake.status.Store(0)
	fake.instanceID = "replaced-proxy"
	if got := parts.bridge.Probe(context.Background()); got != integrationmonitor.IdentityMismatch {
		t.Fatalf("probe of a replaced proxy = %s", got)
	}
}

func TestMonitoringConfigIsValidated(t *testing.T) {
	text := strings.Replace(validRuntimeConfig(t), "  audit:\n", "  monitoring:\n    proxy:\n      failure_threshold: 0\n  audit:\n", 1)
	if _, err := config.Parse(strings.NewReader(text)); err == nil || !strings.Contains(err.Error(), "controller.monitoring.proxy.failure_threshold") {
		t.Fatalf("Parse = %v", err)
	}
}

func validRuntimeConfig(t *testing.T) string {
	t.Helper()
	f := newFixture(t)
	return f.configText("execution.local")
}

func TestNotifierSettingsAreValidated(t *testing.T) {
	profiles := map[string]string{
		"webhook": `  - id: integration.notify-discord
    category: integration
    implementation: webhook
    secret_refs:
      webhook_url:
        secret_ref: systemd-credential://discord-webhook-url
        purpose: Discord incoming webhook
    webhook:
      environment_id: staging
      format: discord
      events: [integration.proxy.unreachable, integration.proxy.recovered]
`,
		"smtp": `  - id: integration.notify-email
    category: integration
    implementation: smtp
    secret_refs:
      password:
        secret_ref: systemd-credential://smtp-password
        purpose: SMTP submission password
    smtp:
      environment_id: staging
      host: smtp.example.com
      port: 587
      tls: starttls
      username: alerts@example.com
      from: alerts@example.com
      to: [ops@example.com]
`,
		"ntfy": `  - id: integration.notify-ntfy
    category: integration
    implementation: ntfy
    secret_refs:
      access_token:
        secret_ref: systemd-credential://ntfy-token
        purpose: ntfy publish token
    ntfy:
      environment_id: staging
      server_url: https://ntfy.sh
      topic: gsc-alerts
`,
	}
	with := func(profile string) string {
		return strings.Replace(validRuntimeConfig(t), "environments:\n", profile+"environments:\n", 1)
	}
	for name, profile := range profiles {
		if _, err := config.Parse(strings.NewReader(with(profile))); err != nil {
			t.Fatalf("valid %s profile rejected: %v", name, err)
		}
	}
	for name, test := range map[string]struct{ profile, before, after, want string }{
		"webhook format":    {"webhook", "format: discord", "format: teams", "format must be discord, slack, or json"},
		"unknown event":     {"webhook", "integration.proxy.recovered]", "integration.proxy.exploded]", "events must list only"},
		"webhook secret":    {"webhook", "      webhook_url:", "      url:", "secret_refs must define exactly webhook_url"},
		"unknown env":       {"webhook", "environment_id: staging", "environment_id: lab", "environment_id must name a configured environment"},
		"smtp tls":          {"smtp", "tls: starttls", "tls: none", "tls must be starttls or implicit"},
		"smtp display name": {"smtp", "to: [ops@example.com]", `to: ["Ops <ops@example.com>"]`, "to must list email addresses without display names"},
		"ntfy plain http":   {"ntfy", "server_url: https://ntfy.sh", "server_url: http://ntfy.sh", "server_url must be an https URL"},
		"ntfy topic":        {"ntfy", "topic: gsc-alerts", "topic: gsc/alerts", "topic must be 1 to 64"},
		"notifier timeout":  {"ntfy", "topic: gsc-alerts\n", "topic: gsc-alerts\n      timeout: 5m\n", "timeout must be greater than zero and no more than 1m"},
		"wrong category":    {"ntfy", "    category: integration\n    implementation: ntfy", "    category: game\n    implementation: ntfy", "requires category integration"},
	} {
		t.Run(name, func(t *testing.T) {
			base := profiles[test.profile]
			changed := strings.Replace(base, test.before, test.after, 1)
			if changed == base {
				t.Fatalf("replacement %q did not apply", test.before)
			}
			if _, err := config.Parse(strings.NewReader(with(changed))); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse = %v, want %q", err, test.want)
			}
		})
	}
}
