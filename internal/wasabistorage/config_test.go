package wasabistorage

import (
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		wantError   bool
		errorPrefix string
	}{
		{
			name:      "missing adapter id fails",
			config:    Config{},
			wantError: true,
		},
		{
			name:      "empty storage profile fails",
			config:    Config{AdapterID: "storage.test"},
			wantError: true,
		},
		{
			name: "valid config passes",
			config: Config{
				AdapterID:          "storage.example",
				StorageProfileID:   "example-profile",
				EnvironmentID:      "staging",
				Container:          "game-worlds-staging",
				KeyPrefix:          "worlds/",
				Region:             "us-east-1",
				EndpointURL:        "https://wasabisys.com",
				AccessKeyIDRef:     "systemd-credential://example-wasabi-access-key-id",
				SecretAccessKeyRef: "systemd-credential://example-wasabi-secret-access-key",
				MaxObjectBytes:     100 * 1024 * 1024, // 100MB within multipart limit
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateConfig(tt.config)
			if (err != nil) != tt.wantError {
				t.Errorf("validateConfig() error = %v, wantError %v", err, tt.wantError)
			}
			if tt.wantError && err != nil {
				if !strings.HasPrefix(err.Error(), tt.errorPrefix) {
					t.Logf("unexpected error prefix: got %q, want %q", err.Error(), tt.errorPrefix)
				}
			}
		})
	}
}
