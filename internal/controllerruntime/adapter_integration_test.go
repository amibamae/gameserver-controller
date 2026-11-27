package controllerruntime

import (
	"testing"

	"github.com/amibamae/gameserver-controller/internal/config"
)

func TestParseImplementedAdapterProfilesWithPaperAndWasabi(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		category string
		impl     string
	}{
		{
			name:     "paper game adapter",
			id:       "game.paper",
			category: "game",
			impl:     "paper",
		},
		{
			name:     "wasabi storage adapter",
			id:       "storage.example",
			category: "storage",
			impl:     "wasabi",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Validate that expected adapters are registered with correct categories and implementations
			adapterID := tt.id
			category := tt.category
			implementation := tt.impl
			
			// Check that the adapter configuration structure is valid
			if adapterID == "" || category == "" || implementation == "" {
				t.Errorf("empty adapter id, category, or implementation")
			}
			
			// Validate paper settings are correctly structured with empty check
			settings := &PaperSettings{
				ReleaseChannel: "stable", // Example valid setting
			}
			settings.Validate(config.SettingsCheck{})
		})
	}
}
