package controllerruntime

import (
	"github.com/amibamae/gameserver-controller/internal/config"
	"github.com/amibamae/gameserver-controller/internal/vultrhosting"
)

// Read-only Vultr hosting adapter (`implementation: vultr`).

const vultrAPITokenSecret = "api_token"

// VultrSettings is the `vultr:` block. The API token comes from secret_refs.
type VultrSettings struct {
	EnvironmentID string `yaml:"environment_id"`
}

func (v *VultrSettings) BoundEnvironment() string { return v.EnvironmentID }

func (v *VultrSettings) Validate(check config.SettingsCheck) {
	check.Require(check.KnownEnvironment(v.EnvironmentID), "environment_id must name a configured environment")
}

func vultrConfig(profile config.AdapterProfile) vultrhosting.Config {
	v, ok := config.SettingsOf[*VultrSettings](profile)
	if !ok {
		v = &VultrSettings{}
	}
	return vultrhosting.Config{
		AdapterID: profile.ID, EnvironmentID: v.EnvironmentID,
		APITokenRef: profile.SecretRefs[vultrAPITokenSecret].SecretRef,
	}
}

func init() {
	registerAdapter(config.Implementation{
		Name: "vultr", Category: "hosting", Secrets: []string{vultrAPITokenSecret},
		NewSettings: func() config.ImplementationSettings { return &VultrSettings{} },
	}, adapterBuilder{
		check: func(b adapterBuild) error {
			_, err := vultrhosting.New(vultrConfig(b.profile), b.protocolVersion, unavailableCredentials{}, nil)
			return err
		},
		build: func(b adapterBuild) (builtAdapter, error) {
			credentials, err := b.credentials()
			if err != nil {
				return builtAdapter{}, err
			}
			adapter, err := vultrhosting.New(vultrConfig(b.profile), b.protocolVersion, credentials, nil)
			return builtAdapter{implementation: adapter}, err
		},
	})
}
