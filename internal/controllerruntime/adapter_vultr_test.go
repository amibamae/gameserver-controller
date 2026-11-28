package controllerruntime

import (
	"strings"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/vultrhosting"
)

const declaredHosting = "  - id: hosting.local\n    category: hosting\n"

// vultrConfigFixture binds the fixture's hosting profile to staging with
// Vultr. A Vultr profile serves one environment, so the production server
// moves to its own declared-only hosting profile.
var vultrConfigFixture = strings.Replace(strings.Replace(configFixture, declaredHosting, `  - id: hosting.production
    category: hosting
  - id: hosting.local
    category: hosting
    implementation: vultr
    secret_refs:
      api_token:
        secret_ref: systemd-credential://vultr-staging-api-token
        purpose: read-only Vultr instance inspection
    vultr:
      environment_id: staging
`, 1), "      hosting: hosting.local\n", "      hosting: hosting.production\n", 1)

func TestVultrProfileParsesValidatesAndConfiguresTheAdapter(t *testing.T) {
	if vultrConfigFixture == configFixture {
		t.Fatal("test fixture replacement did not apply")
	}
	value, err := config.Parse(strings.NewReader(vultrConfigFixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(value); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	var profile config.AdapterProfile
	for _, candidate := range value.Adapters {
		if candidate.ID == "hosting.local" {
			profile = candidate
		}
	}
	want := vultrhosting.Config{AdapterID: "hosting.local", EnvironmentID: "staging", APITokenRef: "systemd-credential://vultr-staging-api-token"}
	if got := vultrConfig(profile); got != want {
		t.Fatalf("vultrConfig = %+v, want %+v", got, want)
	}
	if settings, ok := config.SettingsOf[*VultrSettings](profile); !ok || settings.BoundEnvironment() != "staging" {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestVultrConfigWithoutSettingsLeavesTheEnvironmentEmpty(t *testing.T) {
	got := vultrConfig(config.AdapterProfile{ID: "hosting.local"})
	if got != (vultrhosting.Config{AdapterID: "hosting.local"}) {
		t.Fatalf("vultrConfig = %+v", got)
	}
}

func TestValidateRejectsInvalidVultrProfiles(t *testing.T) {
	tests := []struct{ name, before, after, want string }{
		{"unknown environment", "      environment_id: staging\n", "      environment_id: testing\n", "environment_id must name a configured environment"},
		{"wrong secret name", "      api_token:\n", "      token:\n", "exactly api_token"},
		{"wrong category", "    category: hosting\n    implementation: vultr", "    category: storage\n    implementation: vultr", "requires category hosting"},
		{"shared across environments", "      hosting: hosting.production\n", "      hosting: hosting.local\n", "must use an adapter bound to the same environment as the server"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Replace(vultrConfigFixture, test.before, test.after, 1)
			if input == vultrConfigFixture {
				t.Fatalf("replacement %q did not apply", test.before)
			}
			_, err := config.Parse(strings.NewReader(input))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}
