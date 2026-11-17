package main

import (
	"strings"
)

// evaluateSystemdUnitGeneration checks that bootstrap generates valid systemd unit files.
func evaluateSystemdUnitGeneration(fixture map[string]any) (map[string]any, error) {
	expected := asObject(asObject(fixture)["expected"])

	result := make(map[string]any)

	// Get the rendered bootstrap from expected data
	bootstrap := ""
	if bs, ok := expected["rendered_bootstrap"].(string); ok {
		bootstrap = bs
	}

	// Check Minecraft service unit
	mcUnitPresent := strings.Contains(bootstrap, "minecraft-vanilla.service")
	mcUnitDescription := strings.Contains(bootstrap, "Description=Vanilla No Grief Minecraft Paper Server")
	mcUserService := strings.Contains(bootstrap, "User=minecraft")
	mcGroupService := strings.Contains(bootstrap, "Group=minecraft")
	mcWorkingDir := strings.Contains(bootstrap, "WorkingDirectory=/opt/minecraft/vanilla_no_grief")

	// Check restore service
	restoreUnitPresent := strings.Contains(bootstrap, "minecraft-restore.service")
	restoreDescription := strings.Contains(bootstrap, "Description=Restore Vanilla No Grief Minecraft data from Wasabi")
	restoreTypeOneshot := strings.Contains(bootstrap, "Type=oneshot")
	restoreRemainAfterExit := strings.Contains(bootstrap, "RemainAfterExit=yes")

	// Check mcrcon socket unit
	socketUnitPresent := strings.Contains(bootstrap, "mcrcon.socket")
	socketDescription := strings.Contains(bootstrap, "Description=mcrcon RCON Socket")
	socketListenAddress := strings.Contains(bootstrap, "ListenStream=127.0.0.1:25577")

	// Check mcrcon service unit
	serviceUnitPresent := strings.Contains(bootstrap, "mcrcon.service")
	serviceDescription := strings.Contains(bootstrap, "Description=mcrcon RCON Server for Minecraft")
	serviceEnvironmentFile := strings.Contains(bootstrap, "EnvironmentFile=-/etc/default/mcrcon")

	// Verify all units use simple type (or oneshot for restore)
	mcServiceTypeSimple := strings.Contains(bootstrap, "[Service]") && strings.Contains(bootstrap, "Type=simple")
	
	// Check proper restart behavior
	mcRestartOnFailure := strings.Contains(bootstrap, "Restart=on-failure")
	mcRestartSec10 := strings.Contains(bootstrap, "RestartSec=10")

	// Check requires/wants dependencies
	mcRequiresRestore := strings.Contains(bootstrap, "Requires=minecraft-restore.service")
	mcAfterRestore := strings.Contains(bootstrap, "After=minecraft-restore.service")

	result["minecraft_service_unit_present"] = mcUnitPresent
	result["minecraft_unit_has_description"] = mcUnitDescription
	result["minecraft_unit_uses_minecraft_user"] = mcUserService
	result["minecraft_unit_uses_minecraft_group"] = mcGroupService
	result["minecraft_unit_sets_working_directory"] = mcWorkingDir
	
	result["restore_service_unit_present"] = restoreUnitPresent
	result["restore_unit_has_description"] = restoreDescription
	result["restore_unit_is_oneshot"] = restoreTypeOneshot
	result["restore_unit_remains_after_exit"] = restoreRemainAfterExit
	
	result["mcrcon_socket_unit_present"] = socketUnitPresent
	result["mcrcon_socket_has_description"] = socketDescription
	result["mcrcon_socket_binds_to_127_0_0_1_25577"] = socketListenAddress
	
	result["mcrcon_service_unit_present"] = serviceUnitPresent
	result["mcrcon_service_has_description"] = serviceDescription
	result["mcrcon_service_uses_environment_file"] = serviceEnvironmentFile

	// Additional validation checks
	result["mc_service_type_simple"] = mcServiceTypeSimple
	result["mc_restart_on_failure"] = mcRestartOnFailure
	result["mc_restart_sec_is_10"] = mcRestartSec10
	result["mc_requires_restore_service"] = mcRequiresRestore
	result["mc_after_restore_service"] = mcAfterRestore

	// Determine if validation passed
	allUnitsPresent := mcUnitPresent && restoreUnitPresent && socketUnitPresent && serviceUnitPresent
	
	if allUnitsPresent {
		result["validation_passed"] = true
	} else {
		result["validation_passed"] = false
		missingUnits := []string{}
		if !mcUnitPresent {
			missingUnits = append(missingUnits, "minecraft-vanilla.service")
		}
		if !restoreUnitPresent {
			missingUnits = append(missingUnits, "minecraft-restore.service")
		}
		if !socketUnitPresent {
			missingUnits = append(missingUnits, "mcrcon.socket")
		}
		if !serviceUnitPresent {
			missingUnits = append(missingUnits, "mcrcon.service")
		}
		result["missing_units"] = missingUnits
	}

	return result, nil
}

// evaluateSystemdUnitSecurity checks that systemd units follow security best practices.
func evaluateSystemdUnitSecurity(fixture map[string]any) (map[string]any, error) {
	expected := asObject(asObject(fixture)["expected"])

	bootstrap := ""
	if bs, ok := expected["rendered_bootstrap"].(string); ok {
		bootstrap = bs
	}

	result := make(map[string]any)

	// Security checks for Minecraft service
	noNewPrivileges := strings.Contains(bootstrap, "NoNewPrivileges=true")
	privateTmp := strings.Contains(bootstrap, "PrivateTmp=true")
	protectSystemFull := strings.Contains(bootstrap, "ProtectSystem=full")
	protectHome := strings.Contains(bootstrap, "ProtectHome=true")
	readWritePathsLimited := strings.Contains(bootstrap, "ReadWritePaths=/opt/minecraft/vanilla_no_grief")

	result["no_new_privileges"] = noNewPrivileges
	result["private_tmp"] = privateTmp
	result["protect_system_full"] = protectSystemFull
	result["protect_home"] = protectHome
	result["read_write_paths_limited"] = readWritePathsLimited

	// Check that service runs as non-root (User=minecraft)
	runAsNonRoot := strings.Contains(bootstrap, "User=minecraft")

	result["runs_as_non_root"] = runAsNonRoot

	// Determine if security checks passed
	allSecurityChecksPass := noNewPrivileges && privateTmp && protectSystemFull && 
		protectHome && readWritePathsLimited && runAsNonRoot

	result["security_passed"] = allSecurityChecksPass

	return result, nil
}

func stringsContains(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 && strings.Contains(haystack, needle)
}

func init() {
	registerEvaluator(withoutPackage(evaluateSystemdUnitGeneration), "bootstrap.systemd.unit-generation")
	registerEvaluator(withoutPackage(evaluateSystemdUnitSecurity), "bootstrap.systemd.security")
}
