package simulation

import (
	"testing"
)

func TestNewEnvironment(t *testing.T) {
	config := DefaultConfig()
	env, err := NewEnvironment(config)
	if err != nil {
		t.Fatalf("NewEnvironment failed: %v", err)
	}

	if env == nil {
		t.Fatal("env is nil")
	}

	if env.config.Timeout != config.Timeout {
		t.Errorf("unexpected timeout: got %v, want %v", env.config.Timeout, config.Timeout)
	}
}

func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if config.Timeout <= 0 {
		t.Error("default timeout should be positive")
	}

	if config.EnableMetrics != true {
		t.Error("default EnableMetrics should be true")
	}
}
