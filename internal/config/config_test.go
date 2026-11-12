package config

import (
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/backuppolicy"
)

const validConfig = `schema_version: 1
controller:
  mode: local
  state_store:
    type: bbolt
    path: ./var/controller.db
  defaults:
    operation_timeout: 15m
    retry_limit: 2
  audit:
    enabled: true
    destination: local
adapters:
  - id: hosting.local
    category: hosting
  - id: storage.production
    category: storage
    secret_refs:
      backup_writer:
        secret_ref: systemd-credential://wasabi-production-writer
        purpose: production backup writer
  - id: storage.staging
    category: storage
    secret_refs:
      production_reader:
        secret_ref: systemd-credential://wasabi-staging-production-reader
        purpose: read selected production backup
      backup_writer:
        secret_ref: systemd-credential://wasabi-staging-writer
        purpose: staging backup writer
  - id: execution.local
    category: execution
  - id: game.paper
    category: game
    secret_refs:
      server_password:
        secret_ref: systemd-credential://example-game-server-password
        purpose: test-only example
        permission_scope: [game.server.read]
environments:
  - id: production
    classification: production
    production_backup_access: read_write
  - id: staging
    classification: staging
    production_backup_access: read_only
servers:
  - id: production-server
    environment_id: production
    adapters:
      hosting: hosting.local
      storage: storage.production
      execution: execution.local
      game: game.paper
    lifecycle:
      require_verified_backup_before_destroy: true
      require_approval_for_destroy: true
  - id: staging-server
    environment_id: staging
    adapters:
      hosting: hosting.local
      storage: storage.staging
      execution: execution.local
      game: game.paper
    lifecycle:
      idle_shutdown: 30m
      require_verified_backup_before_destroy: true
      require_approval_for_destroy: true
`

func TestParseValidConfig(t *testing.T) {
	value, err := Parse(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if value.SchemaVersion != SchemaVersion || len(value.Servers) != 2 || len(value.Adapters) != 5 {
		t.Fatalf("unexpected decoded config summary: version=%d adapters=%d servers=%d", value.SchemaVersion, len(value.Adapters), len(value.Servers))
	}
}

func TestManagementAPIIsDisabledByDefaultAndRequiresExplicitTLSConfiguration(t *testing.T) {
	value, err := Parse(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if value.Controller.ManagementAPI.Enabled {
		t.Fatal("remote management API must default to disabled")
	}
	configured := strings.Replace(validConfig, "  audit:\n", "  management_api:\n    enabled: true\n    listen_address: 127.0.0.1:8443\n    audience: controller-instance-1\n    tls_certificate_file: /etc/gameserver-controller/tls.crt\n    tls_private_key_file: /etc/gameserver-controller/tls.key\n  audit:\n", 1)
	value, err = Parse(strings.NewReader(configured))
	if err != nil {
		t.Fatalf("valid HTTPS listener configuration rejected: %v", err)
	}
	if !value.Controller.ManagementAPI.Enabled || value.Controller.ManagementAPI.ListenAddress != "127.0.0.1:8443" {
		t.Fatalf("management API configuration not decoded: %#v", value.Controller.ManagementAPI)
	}
}

func TestManagementAPIRejectsInvalidListenerConfiguration(t *testing.T) {
	base := "  management_api:\n    enabled: true\n    listen_address: 127.0.0.1:8443\n    audience: controller-instance-1\n    tls_certificate_file: /etc/gameserver-controller/tls.crt\n    tls_private_key_file: /etc/gameserver-controller/tls.key\n"
	cases := []struct{ name, before, after, want string }{
		{"wildcard bind requires an explicit IP", "127.0.0.1:8443", ":8443", "listen_address"},
		{"hostname bind rejected", "127.0.0.1:8443", "controller.local:8443", "listen_address"},
		{"invalid port", "127.0.0.1:8443", "127.0.0.1:0", "listen_address"},
		{"invalid audience", "audience: controller-instance-1", "audience: '*'", "audience"},
		{"relative certificate path", "/etc/gameserver-controller/tls.crt", "tls.crt", "tls_certificate_file"},
		{"same cert and key", "/etc/gameserver-controller/tls.crt", "/etc/gameserver-controller/tls.key", "paths must differ"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			listener := strings.Replace(base, test.before, test.after, 1)
			input := strings.Replace(validConfig, "  audit:\n", listener+"  audit:\n", 1)
			_, err := Parse(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestBackupPolicyDefaultsAndOverrides(t *testing.T) {
	value, err := Parse(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	defaults, err := backuppolicy.Resolve(value.Servers[0].Lifecycle.Backups)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Schedule != (backuppolicy.Interval{Every: 9, Unit: backuppolicy.Hour}) || defaults.KeepLast != 5 {
		t.Fatalf("backup policy defaults = %#v", defaults)
	}

	customConfig := strings.Replace(validConfig,
		"      require_approval_for_destroy: true\n",
		"      require_approval_for_destroy: true\n      backups:\n        schedule: {every: 5, unit: minutes}\n        retention: {keep_last: 0}\n", 1)
	custom, err := Parse(strings.NewReader(customConfig))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := backuppolicy.Resolve(custom.Servers[0].Lifecycle.Backups)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Schedule != (backuppolicy.Interval{Every: 5, Unit: backuppolicy.Minute}) || resolved.KeepLast != 0 {
		t.Fatalf("custom backup policy = %#v, want five minutes and unlimited retention", resolved)
	}
}

func TestParseRejectsInvalidBackupPolicy(t *testing.T) {
	input := strings.Replace(validConfig,
		"      require_approval_for_destroy: true\n",
		"      require_approval_for_destroy: true\n      backups:\n        schedule: {every: 0, unit: months}\n", 1)
	if _, err := Parse(strings.NewReader(input)); err == nil {
		t.Fatal("Parse accepted a zero backup schedule interval")
	}
	negativeRetention := strings.Replace(validConfig,
		"      require_approval_for_destroy: true\n",
		"      require_approval_for_destroy: true\n      backups:\n        retention: {keep_last: -1}\n", 1)
	if _, err := Parse(strings.NewReader(negativeRetention)); err == nil {
		t.Fatal("Parse accepted negative backup retention")
	}
}

func TestParseRejectsMalformedOrAmbiguousYAML(t *testing.T) {
	cases := map[string]string{
		"unknown field":      strings.Replace(validConfig, "  retry_limit: 2", "  retry_limit: 2\n  retries: 3", 1),
		"duplicate field":    strings.Replace(validConfig, "  retry_limit: 2", "  retry_limit: 2\n  retry_limit: 4", 1),
		"multiple documents": validConfig + "---\nschema_version: 1\n",
		"invalid yaml":       "schema_version: [\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(input)); err == nil {
				t.Fatal("Parse accepted invalid YAML")
			}
		})
	}
}

func TestParseErrorDoesNotEchoInvalidScalar(t *testing.T) {
	const sentinel = "SUPER_SECRET_VALUE_DO_NOT_ECHO"
	input := strings.Replace(validConfig, "  enabled: true", "  enabled: "+sentinel, 1)
	_, err := Parse(strings.NewReader(input))
	if err == nil {
		t.Fatal("Parse accepted a string in the audit enabled field")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("parse error echoed the supplied scalar: %q", err)
	}
}

func TestValidateRejectsUnsafeSecretAndEnvironmentPolicy(t *testing.T) {
	tests := []struct {
		name, before, after, want string
	}{
		{"literal secret", "systemd-credential://example-game-server-password", "hunter2", "secret_ref"},
		{"environment reference rejected", "systemd-credential://example-game-server-password", "env://GAME_SERVER_PASSWORD", "secret_ref"},
		{"malformed credential name", "systemd-credential://example-game-server-password", "systemd-credential://BAD/../PATH", "secret_ref"},
		{"staging production write", "classification: staging\n    production_backup_access: read_only", "classification: staging\n    production_backup_access: read_write", "must not have production backup write access"},
		{"disabled backup gate", "require_verified_backup_before_destroy: true", "require_verified_backup_before_destroy: false", "require_verified_backup_before_destroy"},
		{"disabled destroy approval", "require_approval_for_destroy: true", "require_approval_for_destroy: false", "require_approval_for_destroy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Replace(validConfig, test.before, test.after, 1)
			_, err := Parse(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsSharedStorageCredentialAcrossEnvironments(t *testing.T) {
	input := strings.Replace(validConfig, "systemd-credential://wasabi-staging-writer", "systemd-credential://wasabi-production-writer", 1)
	_, err := Parse(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "must not be shared between production and staging/testing") {
		t.Fatalf("shared production/staging storage credential: got %v", err)
	}
}

func TestValidateRejectsCrossReferenceAndBoundsErrors(t *testing.T) {
	tests := []struct {
		name, before, after, want string
	}{
		{"missing retry limit", "    retry_limit: 2\n", "", "retry_limit must be explicitly set"},
		{"unknown environment", "environment_id: production", "environment_id: missing", "references unknown environment"},
		{"wrong adapter category", "hosting: hosting.local", "hosting: game.paper", "must reference a hosting adapter"},
		{"zero timeout", "operation_timeout: 15m", "operation_timeout: 0s", "operation_timeout"},
		{"excessive timeout", "operation_timeout: 15m", "operation_timeout: 25h", "operation_timeout"},
		{"retry limit", "retry_limit: 2", "retry_limit: 11", "retry_limit"},
		{"excessive idle shutdown", "idle_shutdown: 30m", "idle_shutdown: 169h", "idle_shutdown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Replace(validConfig, test.before, test.after, 1)
			_, err := Parse(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsNonLocalStoreURL(t *testing.T) {
	input := strings.Replace(validConfig, "path: ./var/controller.db", "path: s3://bucket/controller.db", 1)
	_, err := Parse(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "must be a non-empty local path") {
		t.Fatalf("Parse error = %v, want local path validation", err)
	}
}

func TestResolveStateStorePath(t *testing.T) {
	if got := ResolveStateStorePath("/etc/gsc/controller.yaml", "./var/state.db"); got != "/etc/gsc/var/state.db" {
		t.Fatalf("relative path resolved to %q", got)
	}
	if got := ResolveStateStorePath("/etc/gsc/controller.yaml", "/var/lib/gsc/state.db"); got != "/var/lib/gsc/state.db" {
		t.Fatalf("absolute path resolved to %q", got)
	}
}

// withNetwork adds integration adapters and a network block to the staging
// server of validConfig.
func withNetwork(network string) string {
	input := strings.Replace(validConfig, "environments:\n", `  - id: integration.velocity
    category: integration
  - id: integration.cloudflare-dns
    category: integration
environments:
`, 1)
	return strings.Replace(input, "      idle_shutdown: 30m\n      require_verified_backup_before_destroy: true\n      require_approval_for_destroy: true\n",
		"      idle_shutdown: 30m\n      require_verified_backup_before_destroy: true\n      require_approval_for_destroy: true\n"+network, 1)
}

func TestNetworkProfiles(t *testing.T) {
	valid := map[string]string{
		"proxy with instance IP": `    network:
      join_mode: proxy
      proxy: integration.velocity
      backends:
        - id: paper-main
          address_mode: instance_ip
          port: 25565
`,
		"proxy with managed hostname": `    network:
      join_mode: proxy
      proxy: integration.velocity
      dns: integration.cloudflare-dns
      backends:
        - id: paper-main
          address_mode: hostname
          hostname: paper-main.backends.example.com
          port: 25565
`,
		"direct dns": `    network:
      join_mode: direct_dns
      dns: integration.cloudflare-dns
      join_hostname: play.example.com
      fallback_records:
        A: [203.0.113.10]
`,
		"none": `    network:
      join_mode: none
`,
	}
	for name, network := range valid {
		t.Run(name, func(t *testing.T) {
			config, err := Parse(strings.NewReader(withNetwork(network)))
			if err != nil {
				t.Fatal(err)
			}
			if config.Servers[1].Network == nil {
				t.Fatal("network block was not parsed")
			}
		})
	}
	config, err := Parse(strings.NewReader(validConfig))
	if err != nil || config.Servers[0].Network != nil {
		t.Fatalf("a server without a network block must stay unset: %v", err)
	}
}

func TestNetworkProfileRejectsMissingOrInconsistentSettings(t *testing.T) {
	proxyBackend := "      backends:\n        - id: paper-main\n          address_mode: instance_ip\n          port: 25565\n"
	tests := map[string]struct{ network, want string }{
		"unknown join mode":               {"    network:\n      join_mode: dns\n", "join_mode must be proxy, direct_dns, or none"},
		"proxy without adapter":           {"    network:\n      join_mode: proxy\n" + proxyBackend, "network.proxy is required"},
		"proxy adapter of wrong category": {"    network:\n      join_mode: proxy\n      proxy: game.paper\n" + proxyBackend, "network.proxy must reference a integration adapter"},
		"proxy without backends":          {"    network:\n      join_mode: proxy\n      proxy: integration.velocity\n", "must list at least one backend"},
		"hostname backend without dns":    {"    network:\n      join_mode: proxy\n      proxy: integration.velocity\n      backends:\n        - id: paper-main\n          address_mode: hostname\n          hostname: paper-main.backends.example.com\n          port: 25565\n", "network.dns is required"},
		"bad hostname":                    {"    network:\n      join_mode: proxy\n      proxy: integration.velocity\n      dns: integration.cloudflare-dns\n      backends:\n        - id: paper-main\n          address_mode: hostname\n          hostname: Paper_Main.example.com\n          port: 25565\n", "hostname must be a lower-case DNS hostname"},
		"numeric hostname":                {"    network:\n      join_mode: direct_dns\n      dns: integration.cloudflare-dns\n      join_hostname: 203.0.113.5\n", "join_hostname must be a lower-case DNS hostname"},
		"bad port":                        {"    network:\n      join_mode: proxy\n      proxy: integration.velocity\n      backends:\n        - id: paper-main\n          address_mode: instance_ip\n          port: 70000\n", "port must be between 1 and 65535"},
		"duplicate backend":               {"    network:\n      join_mode: proxy\n      proxy: integration.velocity\n" + proxyBackend + "        - id: paper-main\n          address_mode: instance_ip\n          port: 25566\n", "id is a duplicate"},
		"bad address mode":                {"    network:\n      join_mode: proxy\n      proxy: integration.velocity\n      backends:\n        - id: paper-main\n          address_mode: dns\n          port: 25565\n", "address_mode must be instance_ip or hostname"},
		"direct dns without adapter":      {"    network:\n      join_mode: direct_dns\n      join_hostname: play.example.com\n", "network.dns is required"},
		"direct dns with backends":        {"    network:\n      join_mode: direct_dns\n      dns: integration.cloudflare-dns\n      join_hostname: play.example.com\n" + proxyBackend, "backends are only used with join_mode proxy"},
		"none with settings":              {"    network:\n      join_mode: none\n      proxy: integration.velocity\n", "must not set proxy"},
		"unused dns adapter":              {"    network:\n      join_mode: proxy\n      proxy: integration.velocity\n      dns: integration.cloudflare-dns\n" + proxyBackend, "dns is only used with"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(withNetwork(test.network)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}

	// The legacy single integration field must agree with network.proxy.
	input := withNetwork("    network:\n      join_mode: proxy\n      proxy: integration.velocity\n" + proxyBackend)
	input = strings.Replace(input, "      game: game.paper\n    lifecycle:\n      idle_shutdown", "      game: game.paper\n      integration: integration.cloudflare-dns\n    lifecycle:\n      idle_shutdown", 1)
	if _, err := Parse(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "must match adapters.integration") {
		t.Fatalf("legacy mismatch: %v", err)
	}
}

func TestBackendHostnameTemplate(t *testing.T) {
	network := `    network:
      join_mode: proxy
      proxy: integration.velocity
      dns: integration.cloudflare-dns
      backend_hostname_template: "{backend}.{environment}.mc.example.com"
      backends:
        - id: paper-main
          port: 25565
        - id: paper-creative
          port: 25566
        - id: paper-lobby-ip
          address_mode: instance_ip
          port: 25567
`
	config, err := Parse(strings.NewReader(withNetwork(network)))
	if err != nil {
		t.Fatal(err)
	}
	server := config.Servers[1]
	want := map[string]string{"paper-main": "paper-main.staging.mc.example.com", "paper-creative": "paper-creative.staging.mc.example.com"}
	for _, backend := range server.Network.Backends {
		name, ok := server.Network.BackendHostname(server.ID, server.EnvironmentID, backend)
		if expected, hasName := want[backend.ID]; hasName != ok || name != expected {
			t.Errorf("%s: hostname %q (%v), want %q", backend.ID, name, ok, expected)
		}
	}

	tests := map[string]struct{ from, to, want string }{
		"explicit hostname with template":  {"        - id: paper-main\n          port: 25565\n", "        - id: paper-main\n          hostname: custom.example.com\n          port: 25565\n", "must not be set when backend_hostname_template is set"},
		"template without backend":         {`"{backend}.{environment}.mc.example.com"`, `"{environment}.mc.example.com"`, "must contain {backend}"},
		"unknown placeholder":              {`"{backend}.{environment}.mc.example.com"`, `"{backend}.{region}.mc.example.com"`, "may only use {backend}, {server}, and {environment}"},
		"invalid generated hostname":       {"        - id: paper-main\n", "        - id: paper_main\n", "hostname from backend_hostname_template is not a valid"},
		"scheme without environment is ok": {`"{backend}.{environment}.mc.example.com"`, `"{backend}.mc.example.com"`, ""},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			input := strings.Replace(withNetwork(network), test.from, test.to, 1)
			_, err := Parse(strings.NewReader(input))
			if test.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestBackendHostnamesAreUniqueAcrossServers(t *testing.T) {
	network := "    network:\n      join_mode: proxy\n      proxy: integration.velocity\n      dns: integration.cloudflare-dns\n" +
		"      backends:\n        - id: paper-main\n          address_mode: hostname\n          hostname: paper-main.mc.example.com\n          port: 25565\n"
	input := withNetwork(network)
	// Give the production server a backend with the same hostname.
	input = strings.Replace(input, "    lifecycle:\n      require_verified_backup_before_destroy: true\n      require_approval_for_destroy: true\n  - id: staging-server",
		"    lifecycle:\n      require_verified_backup_before_destroy: true\n      require_approval_for_destroy: true\n"+network+"  - id: staging-server", 1)
	if _, err := Parse(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "hostname is already used by servers[0].network.backends[0]") {
		t.Fatalf("duplicate hostname: %v", err)
	}
}

func TestScalingPolicy(t *testing.T) {
	input := strings.Replace(validConfig, "      idle_shutdown: 30m\n", "      idle_shutdown: 30m\n      scaling:\n        decision_window_start: 58m\n        decision_window_end: 59m\n        hourly_cost: \"0.036\"\n        ignore_billing_below_hourly_cost: \"0.005\"\n", 1)
	config, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if config.Servers[1].Lifecycle.Scaling.HourlyCost != "0.036" {
		t.Fatal("scaling settings were not parsed")
	}
	bad := strings.Replace(validConfig, "      idle_shutdown: 30m\n", "      idle_shutdown: 30m\n      scaling:\n        decision_window_end: 61m\n", 1)
	if _, err := Parse(strings.NewReader(bad)); err == nil || !strings.Contains(err.Error(), "lifecycle.scaling") {
		t.Fatalf("invalid scaling accepted: %v", err)
	}
}

// implementedConfig turns storage.staging into a Wasabi profile and
// execution.local into a process adapter.
var implementedConfig = strings.NewReplacer(
	`  - id: storage.staging
    category: storage
    secret_refs:
      production_reader:
        secret_ref: systemd-credential://wasabi-staging-production-reader
        purpose: read selected production backup
      backup_writer:
        secret_ref: systemd-credential://wasabi-staging-writer
        purpose: staging backup writer
`, `  - id: storage.staging
    category: storage
    implementation: wasabi
    secret_refs:
      access_key_id:
        secret_ref: systemd-credential://wasabi-staging-access-key-id
        purpose: staging Wasabi access key ID
      secret_access_key:
        secret_ref: systemd-credential://wasabi-staging-secret-access-key
        purpose: staging Wasabi secret access key
    wasabi:
      storage_profile_id: wasabi-staging
      environment_id: staging
      container: example-staging-backups
      key_prefix: server/
      region: us-west-1
      endpoint_url: https://s3.us-west-1.wasabisys.com
      max_object_bytes: 1073741824
      require_version_ids: true
`,
	`  - id: execution.local
    category: execution
`, `  - id: execution.local
    category: execution
    implementation: process
    process:
      path: /usr/lib/gameserver-controller/execution-adapter
      args: [--profile, local]
      env: [LANG=C.UTF-8]
      start_timeout: 10s
`).Replace(validConfig)

func TestParseImplementedAdapterProfiles(t *testing.T) {
	if !strings.Contains(implementedConfig, "implementation: wasabi") || !strings.Contains(implementedConfig, "implementation: process") {
		t.Fatal("test fixture replacement did not apply")
	}
	value, err := Parse(strings.NewReader(implementedConfig))
	if err != nil {
		t.Fatal(err)
	}
	var wasabi, process *AdapterProfile
	for i := range value.Adapters {
		switch value.Adapters[i].ID {
		case "storage.staging":
			wasabi = &value.Adapters[i]
		case "execution.local":
			process = &value.Adapters[i]
		}
	}
	if wasabi == nil || wasabi.Wasabi == nil || wasabi.Wasabi.Container != "example-staging-backups" || !wasabi.Wasabi.RequireVersionIDs {
		t.Fatalf("wasabi profile not decoded: %+v", wasabi)
	}
	if process == nil || process.Process == nil || process.Process.Path != "/usr/lib/gameserver-controller/execution-adapter" || len(process.Process.Args) != 2 {
		t.Fatalf("process profile not decoded: %+v", process)
	}
}

func TestValidateRejectsInvalidImplementedAdapterProfiles(t *testing.T) {
	tests := []struct {
		name, before, after, want string
	}{
		{"unknown implementation", "implementation: process", "implementation: docker", "implementation must be wasabi, process, velocity, or cloudflare"},
		{"wasabi on non-storage", "implementation: process", "implementation: wasabi", "requires category storage"},
		{"settings without implementation", "    implementation: process\n", "", "has process settings but no implementation"},
		{"wrong secret names", "      access_key_id:\n", "      access_key:\n", "exactly access_key_id and secret_access_key"},
		{"unknown wasabi environment", "      environment_id: staging\n", "      environment_id: testing\n", "must name a configured environment"},
		{"cross-environment storage", "      environment_id: staging\n", "      environment_id: production\n", "same environment as the server"},
		{"key prefix", "key_prefix: server/", "key_prefix: server", "key_prefix must end with /"},
		{"plain http endpoint", "https://s3.us-west-1", "http://s3.us-west-1", "endpoint_url must be an HTTPS URL"},
		{"relative process path", "path: /usr/lib/gameserver-controller/execution-adapter", "path: execution-adapter", "process.path must be an absolute"},
		{"env without equals", "env: [LANG=C.UTF-8]", "env: [LANG]", "process.env entries must be NAME=value"},
		{"lower-case env name", "env: [LANG=C.UTF-8]", "env: [lang=C]", "process.env entries must be NAME=value"},
		{"long start timeout", "start_timeout: 10s", "start_timeout: 1h", "start_timeout must be"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Replace(implementedConfig, test.before, test.after, 1)
			if input == implementedConfig {
				t.Fatalf("replacement %q did not apply", test.before)
			}
			_, err := Parse(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// integrationConfig implements the Velocity and Cloudflare adapters declared by
// withNetwork and gives the staging server a proxy profile with managed
// hostnames in the zone.
func integrationConfig() string {
	input := withNetwork(`    network:
      join_mode: proxy
      proxy: integration.velocity
      dns: integration.cloudflare-dns
      backend_hostname_template: "{backend}.{environment}.mc.example.com"
      backends:
        - id: paper-main
          port: 25565
`)
	return strings.NewReplacer(
		"  - id: integration.velocity\n    category: integration\n", `  - id: integration.velocity
    category: integration
    implementation: velocity
    secret_refs:
      bridge_token:
        secret_ref: systemd-credential://velocity-staging-bridge-token
        purpose: staging proxy bridge token
    velocity:
      environment_id: staging
      bridge_url: http://127.0.0.1:25580
      proxy_instance_id: proxy-staging-1
      timeout: 15s
`,
		"  - id: integration.cloudflare-dns\n    category: integration\n", `  - id: integration.cloudflare-dns
    category: integration
    implementation: cloudflare
    secret_refs:
      api_token:
        secret_ref: systemd-credential://cloudflare-staging-dns-token
        purpose: staging DNS edit token
    cloudflare:
      environment_id: staging
      zone_id: 0123456789abcdef0123456789abcdef
      zone_name: example.com
`).Replace(input)
}

func TestParseIntegrationAdapterProfiles(t *testing.T) {
	input := integrationConfig()
	if !strings.Contains(input, "implementation: velocity") || !strings.Contains(input, "implementation: cloudflare") || !strings.Contains(input, "backend_hostname_template") {
		t.Fatal("fixture replacement did not apply")
	}
	value, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	var velocity, cloudflare *AdapterProfile
	for i := range value.Adapters {
		switch value.Adapters[i].Implementation {
		case ImplementationVelocity:
			velocity = &value.Adapters[i]
		case ImplementationCloudflare:
			cloudflare = &value.Adapters[i]
		}
	}
	if velocity == nil || velocity.Velocity.ProxyInstanceID != "proxy-staging-1" || cloudflare == nil || cloudflare.Cloudflare.ZoneName != "example.com" {
		t.Fatalf("integration profiles not decoded: %+v %+v", velocity, cloudflare)
	}
}

func TestValidateRejectsInvalidIntegrationProfiles(t *testing.T) {
	directDNS := strings.Replace(integrationConfig(), `    network:
      join_mode: proxy
      proxy: integration.velocity
      dns: integration.cloudflare-dns
      backend_hostname_template: "{backend}.{environment}.mc.example.com"
      backends:
        - id: paper-main
          port: 25565
`, `    network:
      join_mode: direct_dns
      dns: integration.cloudflare-dns
      join_hostname: play.example.com
      fallback_records:
        A: [203.0.113.10]
`, 1)
	if _, err := Parse(strings.NewReader(directDNS)); err != nil {
		t.Fatalf("valid direct_dns profile rejected: %v", err)
	}
	tests := []struct {
		name, base, before, after, want string
	}{
		{"velocity wrong category", "", "  - id: integration.velocity\n    category: integration\n", "  - id: integration.velocity\n    category: hosting\n", "requires category integration"},
		{"velocity wrong secret", "", "      bridge_token:\n", "      token:\n", "exactly bridge_token"},
		{"plain http to remote bridge", "", "bridge_url: http://127.0.0.1:25580", "bridge_url: http://proxy.example.com:25580", "velocity.bridge_url"},
		{"bridge url with path", "", "bridge_url: http://127.0.0.1:25580", "bridge_url: https://proxy.example.com/v1", "velocity.bridge_url"},
		{"velocity unknown environment", "", "      environment_id: staging\n      bridge_url", "      environment_id: testing\n      bridge_url", "velocity.environment_id must name"},
		{"proxy in another environment", "", "      environment_id: staging\n      bridge_url", "      environment_id: production\n      bridge_url", "belongs to environment production"},
		{"cloudflare wrong secret", "", "      api_token:\n", "      token:\n", "exactly api_token"},
		{"bad zone id", "", "zone_id: 0123456789abcdef0123456789abcdef", "zone_id: not-a-zone", "zone_id must be"},
		{"hostname outside zone", "", "zone_name: example.com", "zone_name: example.org", "is outside the DNS zone example.org"},
		{"direct_dns without fallback", "direct", "      fallback_records:\n        A: [203.0.113.10]\n", "", "fallback_records is required"},
		{"fallback wrong family", "direct", "A: [203.0.113.10]", "A: [2001:db8::1]", "invalid address"},
		{"fallback bad key", "direct", "A: [203.0.113.10]", "CNAME: [lobby.example.com]", "keys must be A or AAAA"},
		{"block without implementation", "", "    implementation: cloudflare\n", "", "has cloudflare settings but no implementation"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := integrationConfig()
			if test.base == "direct" {
				base = directDNS
			}
			input := strings.Replace(base, test.before, test.after, 1)
			if input == base {
				t.Fatalf("replacement %q did not apply", test.before)
			}
			_, err := Parse(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateRejectsDuplicateBackendIDsOnOneProxy(t *testing.T) {
	input := withNetwork(`    network:
      join_mode: proxy
      proxy: integration.velocity
      backends:
        - id: paper-main
          address_mode: instance_ip
          port: 25565
`)
	// Give the production server a backend with the same ID on the same proxy.
	input = strings.Replace(input, "      require_verified_backup_before_destroy: true\n      require_approval_for_destroy: true\n  - id: staging-server", `      require_verified_backup_before_destroy: true
      require_approval_for_destroy: true
    network:
      join_mode: proxy
      proxy: integration.velocity
      backends:
        - id: paper-main
          address_mode: instance_ip
          port: 25566
  - id: staging-server`, 1)
	if !strings.Contains(input, "port: 25566") {
		t.Fatal("fixture replacement did not apply")
	}
	if _, err := Parse(strings.NewReader(input)); err == nil || !strings.Contains(err.Error(), "is already used on proxy integration.velocity") {
		t.Fatalf("Parse error = %v", err)
	}
}
