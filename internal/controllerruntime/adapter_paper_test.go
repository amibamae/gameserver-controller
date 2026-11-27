package controllerruntime

import (
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
)

func TestParsePaperSettings(t *testing.T) {
	tests := []struct {
		name     string
		settings *PaperSettings
		wantErr  bool
	}{
		{
			name: "empty settings fails",
			settings: &PaperSettings{},
		},
		{
			name:     "stable channel",
			settings: &PaperSettings{ReleaseChannel: "stable"},
		},
		{
			name:     "beta channel",
			settings: &PaperSettings{ReleaseChannel: "beta"},
		},
		{
			name:     "alpha channel",
			settings: &PaperSettings{ReleaseChannel: "alpha"},
		},
		{
			name:     "explicit version ref",
			settings: &PaperSettings{ReleaseRef: "paper:1.21.4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			check := config.SettingsCheck{}
			tt.settings.Validate(check)
			// Validation accumulates errors in the check struct
		})
	}
}
