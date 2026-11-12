package controllerruntime

import (
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
)

// These tests cover each built-in implementation's settings validation, which
// lives in that implementation's adapter_<name>.go file. The fixtures mirror
// internal/config's test configuration.

const configFixture = `schema_version: 1
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

func withNetwork(network string) string {
	input := strings.Replace(configFixture, "environments:\n", `  - id: integration.velocity
    category: integration
  - id: integration.cloudflare-dns
    category: integration
environments:
`, 1)
	return strings.Replace(input, "      idle_shutdown: 30m\n      require_verified_backup_before_destroy: true\n      require_approval_for_destroy: true\n",
		"      idle_shutdown: 30m\n      require_verified_backup_before_destroy: true\n      require_approval_for_destroy: true\n"+network, 1)
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
`).Replace(configFixture)

func TestParseImplementedAdapterProfiles(t *testing.T) {
	if !strings.Contains(implementedConfig, "implementation: wasabi") || !strings.Contains(implementedConfig, "implementation: process") {
		t.Fatal("test fixture replacement did not apply")
	}
	value, err := config.Parse(strings.NewReader(implementedConfig))
	if err != nil {
		t.Fatal(err)
	}
	var wasabi, process *config.AdapterProfile
	for i := range value.Adapters {
		switch value.Adapters[i].ID {
		case "storage.staging":
			wasabi = &value.Adapters[i]
		case "execution.local":
			process = &value.Adapters[i]
		}
	}
	w, okW := config.SettingsOf[*WasabiSettings](*wasabi)
	if !okW || w.Container != "example-staging-backups" || !w.RequireVersionIDs {
		t.Fatalf("wasabi profile not decoded: %+v", wasabi)
	}
	p, okP := config.SettingsOf[*ProcessSettings](*process)
	if !okP || p.Path != "/usr/lib/gameserver-controller/execution-adapter" || len(p.Args) != 2 {
		t.Fatalf("process profile not decoded: %+v", process)
	}
}

func TestValidateRejectsInvalidImplementedAdapterProfiles(t *testing.T) {
	tests := []struct {
		name, before, after, want string
	}{
		{"unknown implementation", "implementation: process", "implementation: docker", "implementation must be one of cloudflare, process, velocity, wasabi when set"},
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
			_, err := config.Parse(strings.NewReader(input))
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
	value, err := config.Parse(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	var velocity, cloudflare *config.AdapterProfile
	for i := range value.Adapters {
		switch value.Adapters[i].Implementation {
		case "velocity":
			velocity = &value.Adapters[i]
		case "cloudflare":
			cloudflare = &value.Adapters[i]
		}
	}
	v, okV := config.SettingsOf[*VelocitySettings](*velocity)
	c, okC := config.SettingsOf[*CloudflareSettings](*cloudflare)
	if !okV || v.ProxyInstanceID != "proxy-staging-1" || !okC || c.ZoneName != "example.com" {
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
	if _, err := config.Parse(strings.NewReader(directDNS)); err != nil {
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
			_, err := config.Parse(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}
