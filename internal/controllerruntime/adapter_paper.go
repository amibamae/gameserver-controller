package controllerruntime

import (
	"github.com/amibamae/gameserver-controller/internal/config"
)

// Paper implements Minecraft Java with the Paper server.
// This is a stub implementation for Phase 3; actual out-of-process JSON-RPC adapter
// will be replaced with a real one in Phase 4.

const (
	paperVersionSecret   = "paper_version"
	paperReleaseRefSecret = "release_ref"
)

// PaperSettings is the `paper:` block configuration.
type PaperSettings struct {
	ReleaseChannel string `yaml:"release_channel,omitempty"` // stable, beta, alpha
	ReleaseRef     string `yaml:"release_ref,omitempty"`    // exact version like "paper:1.21.4"
}

func (p *PaperSettings) Validate(check config.SettingsCheck) {
	check.Require(p.ReleaseChannel == "" || p.ReleaseChannel == "stable" || p.ReleaseChannel == "beta" || p.ReleaseChannel == "alpha", "release_channel must be stable, beta, or alpha")
	check.Require(p.ReleaseRef == "" || len(p.ReleaseRef) > 0, "release_ref must reference Paper like paper:1.21.4")
}

func init() {
	registerAdapter(config.Implementation{
		Name:        "paper",
		Category:    "game",
		Secrets:     []string{paperVersionSecret, paperReleaseRefSecret},
		NewSettings: func() config.ImplementationSettings { return &PaperSettings{} },
	}, adapterBuilder{
		check: func(b adapterBuild) error {
			return nil
		},
		build: func(b adapterBuild) (builtAdapter, error) {
			return builtAdapter{}, nil
		},
	})
}
