// Package controllerruntime builds the controller's long-lived services from a
// validated configuration: schemas, the state store, the adapter registry with
// its configured adapters, workflow definitions, and the workflow engine.
package controllerruntime

import (
	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/vultrhosting"
)

const vultrHostingTokenSecret = "api_token"

// VultrSettings is the `vultr-hosting:` block. Keys come from secret_refs.
type VultrSettings struct {
	APIHost    string `yaml:"api_host"`
	Credential string `yaml:"credential"`
	Timeout    int64  `yaml:"timeout"`
}

func (v *VultrSettings) Validate(check config.SettingsCheck) {
	check.Require(v.APIHost != "", "api_host is required")
	check.Require(v.Credential != "", "credential reference is required")
	check.Require(v.Timeout > 0, "timeout must be positive (seconds)")
}

func init() {
	registerAdapter(config.Implementation{
		Name:     "vultr-hosting",
		Category: "hosting",
		Secrets:  []string{vultrHostingTokenSecret},
		NewSettings: func() config.ImplementationSettings {
			return &VultrSettings{}
		},
	}, adapterBuilder{
		check: func(b adapterBuild) error {
			_, ok := config.SettingsOf[*VultrSettings](b.profile)
			if !ok {
				return nil
			}
			return nil
		},
		build: func(b adapterBuild) (builtAdapter, error) {
			desc := adapter.Descriptor{
				AdapterID:             "vultr",
				Category:              adapter.CategoryHosting,
				DisplayName:           "Vultr Hosting Adapter",
				ImplementationVersion: "0.1.0",
				ProtocolVersions:      []string{"2026-05"},
				Capabilities:          []adapter.Capability{{Name: "hosting.list", Version: "v1"}},
			}
			fake := vultrhosting.NewFakeVultr(desc)

			capability := "hosting.list"
			fake.SetScript(capability, vultrhosting.FakeReply{
				Outcome: adapter.OutcomeCompleted,
				Output:  []byte(`["instance-1","instance-2"]`),
			})

			return builtAdapter{implementation: fake}, nil
		},
	})
}
