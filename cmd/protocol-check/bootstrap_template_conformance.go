package main

import (
	"strings"
)

// evaluateBootstrapTemplateContent checks that the bootstrap template contains required sections.
func evaluateBootstrapTemplateContent(fixture map[string]any) (map[string]any, error) {
	expected := asObject(asObject(fixture)["expected"])

	result := make(map[string]any)

	// Get the rendered bootstrap script
	renderedBootstrap := ""
	if render, ok := expected["rendered_bootstrap"].(string); ok {
		renderedBootstrap = render
	}

	// Check for required template content
	templateContents := []struct {
		pattern    string
		required   bool
	}{
		{"mcrcon", true},
		{"/usr/local/bin/mcrcon", true},
		{"ListenStream=127.0.0.1:25577", true},
		{"RCON_PASSWORD", true},
		{"minecraft-restore.service", true},
		{"minecraft-vanilla.service", true},
		{"[Service]", true},
		{"Type=simple", false},
		{"ExecStart=/usr/local/sbin/minecraft-start.sh", true},
		{"systemctl daemon-reload", true},
	}

	hasMissingSection := false
	for _, section := range templateContents {
		contentPresent := strings.Contains(renderedBootstrap, section.pattern)
		
		if !contentPresent && section.required {
			if _, exists := result["missing_required_section"]; !exists {
				result["missing_required_section"] = section.pattern
			}
			hasMissingSection = true
		}
	}

	if hasMissingSection {
		result["validation_passed"] = false
	} else {
		result["validation_passed"] = true
	}

	return result, nil
}

// evaluateBootstrapTemplatePlacement tests that bootstrap content is placed in correct locations.
func evaluateBootstrapTemplatePlacement(fixture map[string]any) (map[string]any, error) {
	expected := asObject(asObject(fixture)["expected"])

	result := make(map[string]any)

	// Get rendered bootstrap content
	renderedBootstrap := ""
	if render, ok := expected["rendered_bootstrap"].(string); ok {
		renderedBootstrap = render
	}

	// Verify mcrcon installation section placement
	mcrconPresent := strings.Contains(renderedBootstrap, "# Install mcrcon for Minecraft server control.")
	rconConfigPresent := strings.Contains(renderedBootstrap, "cat > /etc/minecraft-mcrcon/password.conf")
	socketUnitPresent := strings.Contains(renderedBootstrap, "[Socket]") && strings.Contains(renderedBootstrap, "ListenStream=127.0.0.1:25577")

	result["mcrcon_section_present"] = mcrconPresent
	result["rcon_config_setup_present"] = rconConfigPresent
	result["socket_unit_present"] = socketUnitPresent

	// Check that RCON binds to localhost only (no firewall needed)
	bindsToLoopback := strings.Contains(renderedBootstrap, "MCRCON_RCON_HOST=\"127.0.0.1\"")

	result["rcon_binds_to_localhost"] = bindsToLoopback
	result["validation_passed"] = mcrconPresent && rconConfigPresent && socketUnitPresent && bindsToLoopback

	return result, nil
}

func init() {
	registerEvaluator(withoutPackage(evaluateBootstrapTemplateContent), "bootstrap.template.mcrcon.installation")
	registerEvaluator(withoutPackage(evaluateBootstrapTemplatePlacement), "bootstrap.template.placement")
}
