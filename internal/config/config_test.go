package config

import (
	"strings"
	"testing"
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
  - id: storage.local
    category: storage
  - id: execution.local
    category: execution
  - id: game.paper
    category: game
    secret_refs:
      server_password:
        secret_ref: env://GAME_SERVER_PASSWORD
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
      storage: storage.local
      execution: execution.local
      game: game.paper
    lifecycle:
      require_verified_backup_before_destroy: true
      require_approval_for_destroy: true
  - id: staging-server
    environment_id: staging
    adapters:
      hosting: hosting.local
      storage: storage.local
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
	if value.SchemaVersion != SchemaVersion || len(value.Servers) != 2 || len(value.Adapters) != 4 {
		t.Fatalf("unexpected decoded config summary: version=%d adapters=%d servers=%d", value.SchemaVersion, len(value.Adapters), len(value.Servers))
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
		{"literal secret", "env://GAME_SERVER_PASSWORD", "hunter2", "secret_ref"},
		{"malformed environment name", "env://GAME_SERVER_PASSWORD", "env://BAD-NAME", "secret_ref"},
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
