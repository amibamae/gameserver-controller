// Package privacy provides log filtering for sensitive data.
package privacy

import (
	"testing"
)

func TestFilterLogRedactsIPs(t *testing.T) {
	input := `2026-11-25T10:30:00Z info user=alice ip=192.168.1.100 action=login success=true`
	expected := `2026-11-25T10:30:00Z info user=alice ip=[REDACTED] action=login success=true`

	result := FilterLog([]byte(input))
	if string(result) != expected {
		t.Errorf("FilterLog() = %q, want %q", result, expected)
	}
}

func TestFilterLogRedactsUUIDs(t *testing.T) {
	input := `2026-11-25T10:30:00Z info session-id=550e8400-e29b-41d4-a716-446655440000 action=view`
	expected := `2026-11-25T10:30:00Z info session-id=[REDACTED] action=view`

	result := FilterLog([]byte(input))
	if string(result) != expected {
		t.Errorf("FilterLog() = %q, want %q", result, expected)
	}
}

func TestFilterLogRedactsSHAHashes(t *testing.T) {
	input := `2026-11-25T10:30:00Z info sha256=a1b2c3d4e5f6789012345678901234567890123456789012345678901234abcd action=verify`
	expected := `2026-11-25T10:30:00Z info sha256=[REDACTED] action=verify`

	result := FilterLog([]byte(input))
	if string(result) != expected {
		t.Errorf("FilterLog() = %q, want %q", result, expected)
	}
}

func TestFilterLogPreservesNonSensitiveData(t *testing.T) {
	input := `2026-11-25T10:30:00Z info message="Hello World" status=ok`
	expected := `2026-11-25T10:30:00Z info message="Hello World" status=ok`

	result := FilterLog([]byte(input))
	if string(result) != expected {
		t.Errorf("FilterLog() = %q, want %q", result, expected)
	}
}

func TestFilterLogMultipleSensitiveFields(t *testing.T) {
	input := `2026-11-25T10:30:00Z info ip=10.0.0.1 uuid=123e4567-e89b-12d3-a456-426614174000 action=test`
	expected := `2026-11-25T10:30:00Z info ip=[REDACTED] uuid=[REDACTED] action=test`

	result := FilterLog([]byte(input))
	if string(result) != expected {
		t.Errorf("FilterLog() = %q, want %q", result, expected)
	}
}
