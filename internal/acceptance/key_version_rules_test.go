package acceptance

import (
	"strings"
	"testing"
)

// TestValidKeyVersionCharacters pins which key version names are accepted.
func TestValidKeyVersionCharacters(t *testing.T) {
	for value, want := range map[string]bool{
		"v1": true, "Key_2026.11-a": true, "0": true, strings.Repeat("a", 64): true,
		"": false, strings.Repeat("a", 65): false, "v 1": false, "v/1": false, "v:1": false, "é": false, "v1\n": false,
	} {
		if got := validKeyVersion(value); got != want {
			t.Errorf("validKeyVersion(%q) = %t, want %t", value, got, want)
		}
	}
}
