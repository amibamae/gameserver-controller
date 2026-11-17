package bootstrap

import (
	"strconv"
	"strings"
	"testing"
)

// TestRenderRejectsEmptyPlan tests that Validate() rejects an empty Plan field.
func TestRenderRejectsEmptyPlan(t *testing.T) {
	settings := Settings{
		ControllerIP:     "203.0.113.10/32",
		MinecraftPort:    25565,
		VoiceChatPort:    24454,
		WasabiRemotePath: "example-bucket/backups",
	}
	settings.Plan = "" // Empty plan should be rejected
	if _, err := Render(settings); err == nil {
		t.Fatal("Render() accepted an empty Plan field")
	} else {
		expectedError := "Vultr plan must be specified"
		if !strings.Contains(err.Error(), expectedError) {
			t.Fatalf("unexpected error message: %q", err.Error())
		}
	}
}

// TestRenderAcceptsValidPlan tests that a valid Vultr plan is accepted.
func TestRenderAcceptsValidPlan(t *testing.T) {
	testCases := []struct {
		name  string
		plan  string
		valid bool
	}{
		{"vc2-1-0150", "vc2-1-0150", true},
		{"vc4-1-0150", "vc4-1-0150", true},
		{"vc8-1-0150", "vc8-1-0150", true},
		{"vc32-1-0150", "vc32-1-0150", true},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			settings := Settings{
				ControllerIP:     "203.0.113.10/32",
				MinecraftPort:    25565,
				VoiceChatPort:    24454,
				WasabiRemotePath: "example-bucket/backups",
				Plan:             tc.plan,
			}
			script, err := Render(settings)
			if tc.valid {
				if err != nil {
					t.Fatalf("Render() rejected valid plan %q: %v", tc.plan, err)
				}
				if len(script) == 0 {
					t.Fatal("Render() returned empty script for valid plan")
				}
			} else {
				if err == nil {
					t.Fatalf("Render() accepted invalid plan %q", tc.plan)
				}
			}
		})
	}
}

// TestHeapCalculationMatchesVultrPlan tests that heap values are correctly calculated from plan RAM.
func TestHeapCalculationMatchesVultrPlan(t *testing.T) {
	testCases := []struct {
		name     string
		plan     string
		minValue uint64 // 50% of plan RAM
		maxValue uint64 // full plan RAM (or fallback for unknown plans)
	}{
		{"vc2", "vc2-1-0150", vultrVC2 / 2, vultrVC2},     // 2GiB RAM → min=1GiB, max=2GiB
		{"vc4", "vc4-1-0150", vultrVC4 / 2, vultrVC4},     // 4GiB RAM → min=2GiB, max=4GiB
		{"vc8", "vc8-1-0150", vultrVC8 / 2, vultrVC8},     // 8GiB RAM → min=4GiB, max=8GiB
		{"vc32", "vc32-1-0150", vultrVC32 / 2, vultrVC32}, // 32GiB RAM → min=16GiB, max=32GiB
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			settings := Settings{
				ControllerIP:     "203.0.113.10/32",
				MinecraftPort:    25565,
				VoiceChatPort:    24454,
				WasabiRemotePath: "example-bucket/backups",
				Plan:             tc.plan,
			}
			script, err := Render(settings)
			if err != nil {
				t.Fatal(err)
			}
			text := string(script)

			// Verify heap values are calculated correctly from plan RAM (50% min, full max)
			if !strings.Contains(text, "-Xms"+strconv.Itoa(int(tc.minValue))) {
				t.Logf("rendered script snippet:\n%s", extractJavaCommand(text))
				t.Fatalf("rendered script is missing heap min: %d", tc.minValue)
			}
			if !strings.Contains(text, "-Xmx"+strconv.Itoa(int(tc.maxValue))) {
				t.Logf("rendered script snippet:\n%s", extractJavaCommand(text))
				t.Fatalf("rendered script is missing heap max: %d", tc.maxValue)
			}

			// Verify each marker appears exactly once to prevent double substitution errors
			if strings.Count(text, "-Xms"+strconv.Itoa(int(tc.minValue))) != 1 {
				t.Errorf("heap min marker appears multiple times")
			}
			if strings.Count(text, "-Xmx"+strconv.Itoa(int(tc.maxValue))) != 1 {
				t.Errorf("heap max marker appears multiple times")
			}
		})
	}
}

// TestHeapCalculationForUnknownPlan tests that unknown plans fall back to vc2 RAM.
func TestHeapCalculationForUnknownPlan(t *testing.T) {
	settings := Settings{
		ControllerIP:     "203.0.113.10/32",
		MinecraftPort:    25565,
		VoiceChatPort:    24454,
		WasabiRemotePath: "example-bucket/backups",
		Plan:             "unknown-plan-xyz", // Unknown plan should fall back to vc2
	}
	script, err := Render(settings)
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)

	// Should use fallback heap values (vc2 RAM = 2GiB, min=1GiB, max=2GiB)
	expectedFallbackMin := vultrVC2 / 2 // 50% of vc2
	expectedFallbackMax := vultrVC2     // vc2 full

	if !strings.Contains(text, "-Xms"+strconv.Itoa(int(expectedFallbackMin))) {
		t.Errorf("unknown plan should use fallback heap min: %d", expectedFallbackMin)
	}
	if !strings.Contains(text, "-Xmx"+strconv.Itoa(int(expectedFallbackMax))) {
		t.Errorf("unknown plan should use fallback heap max: %d", expectedFallbackMax)
	}
}

// extractJavaCommand extracts and returns the java exec line from the rendered script for debugging.
func extractJavaCommand(text string) string {
	start := strings.LastIndex(text, "exec /usr/bin/java ")
	if start < 0 {
		return text // fallback if not found
	}
	end := strings.Index(text[start:], "\n")
	if end > 0 {
		text = text[start : start+end]
	}
	return text[:min(len(text), 200)]
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestMCrConSectionExists tests that the mcrcon installation section exists in the template.
func TestMCrConSectionExists(t *testing.T) {
	script, err := Render(Settings{
		ControllerIP:     "203.0.113.10/32",
		MinecraftPort:    25565,
		VoiceChatPort:    24454,
		WasabiRemotePath: "example-bucket/backups",
		Plan:             "vc2-1-0150",
	})
	if err != nil {
		t.Fatal(err)
	}

	text := string(script)

	// Check that mcrcon installation section exists with expected content
	checks := []string{
		`section "Installing and configuring mcrcon"`,
		`curl -fsSL https://github.com/misey/mcrcon/releases/download/v1.2.0/mcrcon_linux_x86_64`,
		`--output /tmp/mcrcon`,
		`mcrcon --version`,
		`MCRCON_RCON_HOST="127.0.0.1"`,
		`MCRCON_RCON_PORT="25577"`,
		`/etc/default/mcrcon`,
		`mcrcon.socket`,
		`mcrcon.service`,
		`minecraft-admin ALL=(root) NOPASSWD: /usr/local/bin/mcrcon *`,
	}

	for _, check := range checks {
		if !strings.Contains(text, check) {
			t.Errorf("rendered script is missing mcrcon section content: %q", check)
		}
	}
}

// TestMCrConSectionHasCorrectOrder tests that the mcrcon section appears in the correct location.
func TestMCrConSectionHasCorrectOrder(t *testing.T) {
	script, err := Render(Settings{
		ControllerIP:     "203.0.113.10/32",
		MinecraftPort:    25565,
		VoiceChatPort:    24454,
		WasabiRemotePath: "example-bucket/backups",
		Plan:             "vc2-1-0150",
	})
	if err != nil {
		t.Fatal(err)
	}

	text := string(script)

	// mcrcon section should appear after initial security setup (before firewall and services)
	mcrconPos := strings.Index(text, `section "Installing and configuring mcrcon"`)

	if mcrconPos < 0 {
		t.Fatal("missing mcrcon section")
	}

	// Verify it appears before the final validation section to ensure logical ordering
	finalValidationPos := strings.Index(text, `section "Final validation"`)
	if finalValidationPos < 0 || mcrconPos > finalValidationPos {
		t.Errorf("mcrcon section should appear before Final validation, but got mcrcon:%d final:%d", mcrconPos, finalValidationPos)
	}
}
