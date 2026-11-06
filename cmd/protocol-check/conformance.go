package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

func (pkg *protocolPackage) runConformance() error {
	paths, err := filepath.Glob(filepath.Join(pkg.root, "protocol", "conformance", "test-vectors", "*.json"))
	if err != nil {
		return err
	}
	sort.Strings(paths)
	var failures []string
	for _, path := range paths {
		fixture, err := parseJSONFile(path)
		if err == nil {
			var actual map[string]any
			actual, err = evaluate(pkg, asObject(fixture))
			if err == nil {
				var encoded []byte
				encoded, err = json.Marshal(actual)
				if err == nil {
					var normalized any
					normalized, err = parseJSON(encoded, filepath.Base(path)+" result")
					if err == nil {
						err = compareExpected(normalized, asObject(asObject(fixture)["expected"]), "$")
					}
				}
			}
		}
		if err != nil {
			failures = append(failures, filepath.Base(path)+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("conformance failed:\n - %s", strings.Join(failures, "\n - "))
	}
	return nil
}

func evaluate(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	fixtureID := stringValue(fixture, "fixture_id")
	input := asObject(fixture["input"])
	switch fixtureID {
	case "lifecycle.reject.unlisted-transition":
		transition := asObject(fixture["requested_transition"])
		allowed := legalTransitions[[2]string{stringValue(fixture, "initial_state"), stringValue(transition, "to")}]
		result := map[string]any{"accepted": allowed, "side_effect_permitted": allowed}
		if !allowed {
			result["error_code"] = "core.lifecycle.invalid_transition"
		}
		return result, nil
	case "operations.reconcile.unknown-create-outcome":
		initial := asObject(fixture["initial"])
		unknown := boolValue(initial, "dispatch_recorded") && !boolValue(initial, "provider_response_received")
		result := map[string]any{
			"operation_status":         "reconciling",
			"step_status":              "outcome_unknown",
			"retry_create_immediately": false,
			"inspect_exact_target_or_search_by_idempotency_reference": unknown,
			"if_unresolvable": "needs_operator",
		}
		if !unknown {
			result["operation_status"] = initial["operation_status"]
			result["step_status"] = initial["step_status"]
			result["retry_create_immediately"] = true
			result["inspect_exact_target_or_search_by_idempotency_reference"] = false
			result["if_unresolvable"] = nil
		}
		return result, nil
	case "safety.destroy.blocked-stale-data-generation", "safety.destroy.blocked-unverified-backup":
		state := asObject(fixture["initial_state"])
		backup := asObject(state["backup"])
		code := any(nil)
		if !boolValue(backup, "verified") {
			code = "core.safety.backup_not_verified"
		} else if state["data_generation_status"] != "known" || backup["data_generation_id"] != state["data_generation_id"] {
			code = "core.safety.backup_generation_mismatch"
		}
		accepted := code == nil
		lifecycle := state["lifecycle_state"]
		if accepted {
			lifecycle = "destroying"
		}
		calls := 0
		if accepted {
			calls = 1
		}
		return map[string]any{
			"accepted": accepted, "error_code": code,
			"provider_destroy_calls": calls, "logical_server_state": lifecycle,
		}, nil
	case "security.staging.cannot-write-production-backup":
		actor := stringValue(fixture, "actor_environment")
		target := stringValue(fixture, "target_environment")
		requiredScope := "storage.backup.write:" + target
		allowed := actor == target || contains(arrayValue(fixture, "credential_scope"), requiredScope)
		category := any(nil)
		if !allowed {
			category = "authorization"
		}
		return map[string]any{"accepted": allowed, "side_effect_permitted": allowed, "error_category": category}, nil
	case "operations.typed-define-start-stop-results":
		defineOperation := asObject(input["define_operation"])
		startOperation := asObject(input["start_operation"])
		stopOperation := asObject(input["stop_operation"])
		for _, operation := range []struct {
			name  string
			value map[string]any
		}{
			{"server.define", defineOperation},
			{"server.start", startOperation},
			{"server.stop", stopOperation},
		} {
			if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:operations:operation", operation.value); err != nil {
				return nil, fmt.Errorf("%s operation does not match schema: %w", operation.name, err)
			}
		}
		defineResult := asObject(defineOperation["result"])
		startResult := asObject(startOperation["result"])
		stopResult := asObject(stopOperation["result"])
		defineData := asObject(defineResult["operation_data"])
		startData := asObject(startResult["operation_data"])
		stopData := asObject(stopResult["operation_data"])
		defineTarget, startTarget, stopTarget := asObject(defineOperation["target"]), asObject(startOperation["target"]), asObject(stopOperation["target"])
		startAssertion := asObject(startData["readiness_assertion"])
		startInstance := asObject(startTarget["instance_ref"])
		assertedInstance := asObject(startAssertion["instance_ref"])
		startCompleted, err := parseTime(stringValue(startOperation, "completed_at"))
		if err != nil {
			return nil, err
		}
		assertionExpires, err := parseTime(stringValue(startAssertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		serverIdentityConsistent := stringValue(defineData, "server_id") == stringValue(startData, "server_id") &&
			stringValue(startData, "server_id") == stringValue(stopData, "server_id") &&
			stringValue(defineData, "environment_id") == stringValue(startData, "environment_id") &&
			stringValue(startData, "environment_id") == stringValue(stopData, "environment_id")
		startReadinessBound := stringValue(startData, "server_id") == stringValue(startAssertion, "server_id") &&
			stringValue(startData, "environment_id") == stringValue(startAssertion, "environment_id") &&
			stringValue(startData, "server_id") == stringValue(startTarget, "server_id") &&
			stringValue(startData, "environment_id") == stringValue(startTarget, "environment_id") &&
			reflect.DeepEqual(startData["instance_ref"], startTarget["instance_ref"]) &&
			reflect.DeepEqual(startTarget["instance_ref"], startAssertion["instance_ref"]) &&
			stringValue(startInstance, "environment_id") == stringValue(startData, "environment_id") &&
			stringValue(assertedInstance, "environment_id") == stringValue(startAssertion, "environment_id")
		startReadinessCurrent := startReadinessBound && startCompleted.Before(assertionExpires) &&
			stringValue(startData, "lifecycle_state") == "ready"
		defineNoInfrastructure := stringValue(defineData, "lifecycle_state") == "unprovisioned" &&
			len(arrayValue(defineResult, "affected_resources")) == 0
		gameSave, execution, proxyDrain := asObject(stopData["game_save"]), asObject(stopData["execution"]), asObject(stopData["proxy_drain"])
		stopBound := stringValue(stopData, "server_id") == stringValue(stopTarget, "server_id") &&
			stringValue(stopData, "environment_id") == stringValue(stopTarget, "environment_id") &&
			reflect.DeepEqual(stopData["instance_ref"], stopTarget["instance_ref"])
		stopPostconditions := stopBound && stringValue(stopData, "lifecycle_state") == "stopped" &&
			stringValue(gameSave, "outcome") == "confirmed" && stringValue(execution, "outcome") == "stopped" &&
			stringValue(proxyDrain, "outcome") == "drained"
		return map[string]any{
			"schemas_valid":                     true,
			"server_identity_consistent":        serverIdentityConsistent,
			"define_has_no_infrastructure":      defineNoInfrastructure,
			"start_readiness_bound_and_current": startReadinessCurrent,
			"stop_postconditions_confirmed":     stopPostconditions,
		}, nil
	case "execution.logs.health.delete.safety":
		logsInput, logsOutput := asObject(input["logs_input"]), asObject(input["logs_output"])
		healthInput, healthOutput := asObject(input["health_input"]), asObject(input["health_output"])
		deleteInput, deleteOutput := asObject(input["delete_input"]), asObject(input["delete_output"])
		for schemaID, message := range map[string]struct {
			value map[string]any
			name  string
		}{
			"urn:gameserver-controller:0.7.0-draft:execution:systemd-unit-logs-read-input":       {logsInput, "logs input"},
			"urn:gameserver-controller:0.7.0-draft:execution:systemd-unit-logs-read-output":      {logsOutput, "logs output"},
			"urn:gameserver-controller:0.7.0-draft:execution:systemd-unit-health-inspect-input":  {healthInput, "health input"},
			"urn:gameserver-controller:0.7.0-draft:execution:systemd-unit-health-inspect-output": {healthOutput, "health output"},
			"urn:gameserver-controller:0.7.0-draft:execution:systemd-unit-delete-input":          {deleteInput, "delete input"},
			"urn:gameserver-controller:0.7.0-draft:execution:systemd-unit-delete-output":         {deleteOutput, "delete output"},
		} {
			if err := pkg.validateSchema(schemaID, message.value); err != nil {
				return nil, fmt.Errorf("%s does not match schema: %w", message.name, err)
			}
		}
		logsBound := reflect.DeepEqual(logsInput["unit_ref"], logsOutput["unit_ref"])
		healthBound := reflect.DeepEqual(healthInput["unit_ref"], healthOutput["unit_ref"])
		deleteBound := reflect.DeepEqual(deleteInput["unit_ref"], deleteOutput["unit_ref"])
		maxBytes, err := integerValue(logsInput["max_bytes"])
		if err != nil {
			return nil, err
		}
		var totalBytes int64
		for i, rawLine := range arrayValue(logsOutput, "lines") {
			line, ok := rawLine.(string)
			if !ok {
				return nil, fmt.Errorf("log lines must be strings")
			}
			totalBytes += int64(len([]byte(line)))
			if i > 0 {
				totalBytes++
			}
		}
		return map[string]any{
			"schemas_valid": true, "unit_targets_bound": logsBound && healthBound && deleteBound,
			"logs_within_bound": totalBytes <= maxBytes, "logs_redacted": boolValue(logsOutput, "redaction_applied"),
			"unknown_health_not_healthy": stringValue(healthOutput, "health") == "unknown",
			"persistent_data_preserved":  boolValue(deleteOutput, "persistent_data_preserved"),
		}, nil
	case "integration.proxy.register.expired-readiness-rejected":
		registerInput, registerOutput := asObject(input["register_input"]), asObject(input["register_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:integrations:proxy-backend-register-input", registerInput); err != nil {
			return nil, fmt.Errorf("register input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:integrations:proxy-backend-register-output", registerOutput); err != nil {
			return nil, fmt.Errorf("register output does not match schema: %w", err)
		}
		requestTarget, assertion := asObject(registerInput["target"]), asObject(registerInput["readiness_assertion"])
		targetInstance := asObject(requestTarget["instance_ref"])
		assertedInstance := asObject(assertion["instance_ref"])
		bound := reflect.DeepEqual(requestTarget, registerOutput["target"]) &&
			stringValue(requestTarget, "server_id") == stringValue(assertion, "server_id") &&
			stringValue(requestTarget, "environment_id") == stringValue(assertion, "environment_id") &&
			stringValue(requestTarget, "environment_id") == stringValue(targetInstance, "environment_id") &&
			stringValue(assertion, "environment_id") == stringValue(assertedInstance, "environment_id") &&
			reflect.DeepEqual(requestTarget["instance_ref"], assertion["instance_ref"])
		current, err := parseTime(stringValue(input, "validation_time"))
		if err != nil {
			return nil, err
		}
		expires, err := parseTime(stringValue(assertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		readinessCurrent := bound && current.Before(expires)
		rejected := stringValue(registerOutput, "request_outcome") == "rejected" &&
			stringValue(registerOutput, "registration_state") == "unregistered" &&
			stringValue(registerOutput, "routability") == "not_routable"
		return map[string]any{
			"schemas_valid": true, "target_bound": bound, "readiness_current": readinessCurrent,
			"registration_rejected": rejected, "routing_permitted": readinessCurrent && stringValue(registerOutput, "routability") == "routable",
		}, nil
	case "integration.proxy.register.accepted-is-not-routable":
		registerInput, registerOutput := asObject(input["register_input"]), asObject(input["register_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:integrations:proxy-backend-register-input", registerInput); err != nil {
			return nil, fmt.Errorf("register input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:integrations:proxy-backend-register-output", registerOutput); err != nil {
			return nil, fmt.Errorf("register output does not match schema: %w", err)
		}
		requestTarget, assertion := asObject(registerInput["target"]), asObject(registerInput["readiness_assertion"])
		targetInstance := asObject(requestTarget["instance_ref"])
		assertedInstance := asObject(assertion["instance_ref"])
		bound := reflect.DeepEqual(requestTarget, registerOutput["target"]) &&
			stringValue(requestTarget, "server_id") == stringValue(assertion, "server_id") &&
			stringValue(requestTarget, "environment_id") == stringValue(assertion, "environment_id") &&
			stringValue(requestTarget, "environment_id") == stringValue(targetInstance, "environment_id") &&
			stringValue(assertion, "environment_id") == stringValue(assertedInstance, "environment_id") &&
			reflect.DeepEqual(requestTarget["instance_ref"], assertion["instance_ref"])
		current, err := parseTime(stringValue(input, "validation_time"))
		if err != nil {
			return nil, err
		}
		expires, err := parseTime(stringValue(assertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		readinessCurrent := bound && current.Before(expires)
		confirmed := stringValue(registerOutput, "request_outcome") == "confirmed"
		routingPermitted := readinessCurrent && confirmed &&
			stringValue(registerOutput, "registration_state") == "registered" &&
			stringValue(registerOutput, "routability") == "routable"
		return map[string]any{
			"schemas_valid": true, "target_bound": bound, "readiness_current": readinessCurrent,
			"registration_confirmed": confirmed, "accepted_not_routable": stringValue(registerOutput, "request_outcome") != "accepted" || stringValue(registerOutput, "routability") != "routable", "routing_permitted": routingPermitted,
		}, nil
	case "integration.proxy.unknown-drain-and-unregister-require-inspection":
		drainInput, drainOutput := asObject(input["drain_input"]), asObject(input["drain_output"])
		unregisterInput, unregisterOutput := asObject(input["unregister_input"]), asObject(input["unregister_output"])
		for schemaID, message := range map[string]struct {
			value map[string]any
			name  string
		}{
			"urn:gameserver-controller:0.7.0-draft:integrations:proxy-backend-drain-input":       {drainInput, "drain input"},
			"urn:gameserver-controller:0.7.0-draft:integrations:proxy-backend-drain-output":      {drainOutput, "drain output"},
			"urn:gameserver-controller:0.7.0-draft:integrations:proxy-backend-unregister-input":  {unregisterInput, "unregister input"},
			"urn:gameserver-controller:0.7.0-draft:integrations:proxy-backend-unregister-output": {unregisterOutput, "unregister output"},
		} {
			if err := pkg.validateSchema(schemaID, message.value); err != nil {
				return nil, fmt.Errorf("%s does not match schema: %w", message.name, err)
			}
		}
		drainUnknown := stringValue(drainOutput, "request_outcome") == "outcome_unknown" &&
			stringValue(drainOutput, "registration_state") == "unknown" && stringValue(drainOutput, "routability") == "unknown"
		unregisterUnknown := stringValue(unregisterOutput, "request_outcome") == "outcome_unknown" &&
			stringValue(unregisterOutput, "registration_state") == "unknown" && stringValue(unregisterOutput, "routability") == "unknown"
		bound := reflect.DeepEqual(drainInput["target"], drainOutput["target"]) &&
			reflect.DeepEqual(unregisterInput["target"], unregisterOutput["target"])
		return map[string]any{
			"schemas_valid": true, "drain_requires_inspection": drainUnknown,
			"unregister_requires_inspection": unregisterUnknown, "retry_without_inspection": !drainUnknown || !unregisterUnknown || !bound,
		}, nil
	case "game.release.install.configure.fail-closed":
		releaseInput := asObject(input["release_describe_input"])
		releaseOutput := asObject(input["release_describe_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:release-describe-input", releaseInput); err != nil {
			return nil, fmt.Errorf("release input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:release-describe-output", releaseOutput); err != nil {
			return nil, fmt.Errorf("release output does not match schema: %w", err)
		}
		releaseBound := stringValue(releaseInput, "release_id") == stringValue(releaseOutput, "release_id")
		if !releaseBound {
			return nil, fmt.Errorf("release output must describe the exact requested release")
		}
		artifacts := arrayValue(releaseOutput, "artifacts")
		artifactIDs := make([]any, 0, len(artifacts))
		checksummed := true
		for _, rawArtifact := range artifacts {
			artifact := asObject(rawArtifact)
			artifactIDs = append(artifactIDs, artifact["artifact_id"])
			checksum := asObject(artifact["checksum"])
			if stringValue(checksum, "algorithm") != "sha256" || stringValue(checksum, "digest") == "" {
				checksummed = false
			}
		}
		if !uniqueStrings(artifactIDs) {
			return nil, fmt.Errorf("release artifact IDs must be unique")
		}
		installInput := asObject(input["install_input"])
		installOutput := asObject(input["install_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:install-input", installInput); err != nil {
			return nil, fmt.Errorf("install input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:install-output", installOutput); err != nil {
			return nil, fmt.Errorf("install output does not match schema: %w", err)
		}
		installBound := reflect.DeepEqual(installInput["target"], installOutput["target"]) &&
			stringValue(installInput, "release_id") == stringValue(installOutput, "release_id")
		if !installBound || stringValue(installInput, "release_id") != stringValue(releaseOutput, "release_id") {
			return nil, fmt.Errorf("install messages must remain bound to the described release and exact target")
		}
		installationBlocked := stringValue(installOutput, "artifact_verification") == "mismatch" &&
			stringValue(installOutput, "outcome") == "rejected" && !boolValue(installOutput, "installation_started")
		configureInput := asObject(input["configure_input"])
		configureOutput := asObject(input["configure_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:configure-input", configureInput); err != nil {
			return nil, fmt.Errorf("configure input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:configure-output", configureOutput); err != nil {
			return nil, fmt.Errorf("configure output does not match schema: %w", err)
		}
		configureBound := reflect.DeepEqual(configureInput["target"], configureOutput["target"]) &&
			stringValue(configureInput, "release_id") == stringValue(configureOutput, "release_id")
		if !configureBound || stringValue(configureInput, "release_id") != stringValue(releaseOutput, "release_id") ||
			configureInput["configuration_schema_uri"] != releaseOutput["configuration_schema_uri"] ||
			!reflect.DeepEqual(configureInput["configuration_schema_checksum"], releaseOutput["configuration_schema_checksum"]) {
			return nil, fmt.Errorf("configuration must remain bound to the described release schema and target")
		}
		invalidNotApplied := stringValue(configureOutput, "validation_result") == "invalid" &&
			stringValue(configureOutput, "outcome") == "rejected" && !boolValue(configureOutput, "application_started")
		return map[string]any{
			"release_schema_valid": true, "release_bound": releaseBound, "release_artifacts_checksummed": checksummed,
			"install_schema_valid": true, "installation_blocked_before_start": installationBlocked,
			"configure_schema_valid": true, "invalid_configuration_not_applied": invalidNotApplied,
		}, nil
	case "game.install.repeat-same-release-is-idempotent":
		installInput := asObject(input["install_request"])
		installOutput := asObject(input["install_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:install-input", installInput); err != nil {
			return nil, fmt.Errorf("install input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:install-output", installOutput); err != nil {
			return nil, fmt.Errorf("install output does not match schema: %w", err)
		}
		bound := reflect.DeepEqual(installInput["target"], installOutput["target"]) &&
			stringValue(installInput, "release_id") == stringValue(installOutput, "release_id") &&
			stringValue(installInput, "release_id") == stringValue(input, "release_id")
		requiredArtifacts := arrayValue(input, "required_artifact_ids")
		verifiedArtifacts := arrayValue(installOutput, "verified_artifact_ids")
		complete := uniqueStrings(requiredArtifacts) && uniqueStrings(verifiedArtifacts) && sameStringSet(requiredArtifacts, verifiedArtifacts)
		noDuplicateInstall := stringValue(installOutput, "outcome") == "already_installed" &&
			stringValue(installOutput, "artifact_verification") == "verified" && !boolValue(installOutput, "installation_started")
		return map[string]any{
			"schema_valid": true, "target_bound": bound, "artifact_set_complete": complete,
			"duplicate_install_started": !noDuplicateInstall,
		}, nil
	case "game.validate-installation.unknown-blocks-start":
		validateInput := asObject(input["validate_request"])
		validateOutput := asObject(input["validate_output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:validate-installation-input", validateInput); err != nil {
			return nil, fmt.Errorf("validation input does not match schema: %w", err)
		}
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:validate-installation-output", validateOutput); err != nil {
			return nil, fmt.Errorf("validation output does not match schema: %w", err)
		}
		bound := reflect.DeepEqual(validateInput["target"], validateOutput["target"]) &&
			stringValue(validateInput, "release_id") == stringValue(validateOutput, "release_id")
		unknown := stringValue(validateOutput, "validation_result") == "unknown"
		return map[string]any{
			"schema_valid": true, "target_bound": bound,
			"validation_result": stringValue(validateOutput, "validation_result"),
			"start_permitted":   !unknown && stringValue(validateOutput, "validation_result") == "valid",
		}, nil
	case "game.readiness.unknown-required-check-blocks-ready":
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:readiness-check-input", input); err != nil {
			return nil, fmt.Errorf("readiness input does not match schema: %w", err)
		}
		output := asObject(fixture["output"])
		if err := pkg.validateSchema("urn:gameserver-controller:0.7.0-draft:game:readiness-check-output", output); err != nil {
			return nil, fmt.Errorf("readiness output does not match schema: %w", err)
		}
		bound := input["server_id"] == output["server_id"] &&
			input["environment_id"] == output["environment_id"] &&
			input["game_release_id"] == output["game_release_id"] &&
			reflect.DeepEqual(input["instance_ref"], output["instance_ref"])
		if !bound {
			return nil, fmt.Errorf("readiness output must remain bound to the exact requested target and release")
		}
		requiredIDs := arrayValue(input, "required_check_ids")
		if !uniqueStrings(requiredIDs) {
			return nil, fmt.Errorf("required check IDs must be unique")
		}
		requested := make(map[string]bool, len(requiredIDs))
		for _, rawID := range requiredIDs {
			requested[rawID.(string)] = true
		}
		results := arrayValue(output, "check_results")
		if len(results) != len(requested) {
			return nil, fmt.Errorf("readiness output must contain exactly one result for every required check")
		}
		observed := make(map[string]string, len(results))
		for _, rawResult := range results {
			item := asObject(rawResult)
			checkID := stringValue(item, "check_id")
			if !requested[checkID] {
				return nil, fmt.Errorf("readiness output contains an unrequested check %q", checkID)
			}
			if _, exists := observed[checkID]; exists {
				return nil, fmt.Errorf("readiness output contains duplicate check %q", checkID)
			}
			observed[checkID] = stringValue(item, "result")
		}
		allPassed, hasFailure, hasUnknown := true, false, false
		for checkID := range requested {
			result, exists := observed[checkID]
			if !exists {
				return nil, fmt.Errorf("readiness output is missing required check %q", checkID)
			}
			switch result {
			case "pass":
			case "fail":
				allPassed, hasFailure = false, true
			case "unknown":
				allPassed, hasUnknown = false, true
			default:
				return nil, fmt.Errorf("unknown readiness result %q for check %q", result, checkID)
			}
		}
		readiness := "ready"
		if hasFailure {
			readiness = "not_ready"
		} else if hasUnknown {
			readiness = "unknown"
		}
		return map[string]any{
			"schema_valid": true, "target_bound": bound,
			"required_checks_complete": true, "all_required_checks_passed": allPassed,
			"readiness": readiness, "ready_transition_permitted": allPassed,
		}, nil
	case "readiness-required-before-transfer":
		assertion := asObject(input["readiness_assertion"])
		current, err := parseTime(stringValue(input, "validation_time"))
		if err != nil {
			return nil, err
		}
		expires, err := parseTime(stringValue(assertion, "expires_at"))
		if err != nil {
			return nil, err
		}
		expired := !current.Before(expires)
		outcome, code := "accepted", any(nil)
		if expired {
			outcome, code = "rejected", "readiness_assertion_expired"
		}
		calls := len(arrayValue(input, "player_session_ids"))
		if expired {
			calls = 0
		}
		return map[string]any{"outcome": outcome, "error_code": code, "proxy_transfer_calls": calls}, nil
	case "velocity-transfer-partial-results":
		sessions := arrayValue(input, "player_session_ids")
		results := arrayValue(input, "session_outcomes")
		if !uniqueStrings(sessions) {
			return nil, fmt.Errorf("requested session IDs must be unique")
		}
		if len(results) != len(sessions) {
			return nil, fmt.Errorf("one session outcome is required for every requested session")
		}
		requested := make(map[string]bool)
		for _, id := range sessions {
			requested[id.(string)] = true
		}
		resultIDs := make([]any, 0, len(results))
		counts := map[string]int{"transferred": 0, "already_at_destination": 0, "disconnected": 0, "rejected": 0, "failed": 0, "outcome_unknown": 0}
		for _, result := range results {
			item := asObject(result)
			id := item["proxy_session_id"]
			outcome := stringValue(item, "outcome")
			resultIDs = append(resultIDs, id)
			if _, ok := counts[outcome]; !ok {
				return nil, fmt.Errorf("unknown transfer outcome: %q", outcome)
			}
			counts[outcome]++
		}
		if !uniqueStrings(resultIDs) {
			return nil, fmt.Errorf("session outcomes must not contain duplicate IDs")
		}
		for _, id := range resultIDs {
			if str, ok := id.(string); !ok || !requested[str] {
				return nil, fmt.Errorf("session outcomes must contain exactly the requested sessions")
			}
		}
		if len(requested) != len(resultIDs) {
			return nil, fmt.Errorf("session outcomes must contain exactly the requested sessions")
		}
		normalizedCounts := make(map[string]any, len(counts))
		hasUnknown := counts["outcome_unknown"] > 0
		for outcome, count := range counts {
			normalizedCounts[outcome] = count
		}
		return map[string]any{
			"requested_count":                      len(sessions),
			"outcome_counts":                       normalizedCounts,
			"retry_unknown_without_reconciliation": !hasUnknown,
		}, nil
	case "storage.backup.partial-upload-not-verified":
		required := arrayValue(input, "required_object_ids")
		uploaded := arrayValue(input, "uploaded_object_ids")
		allUploaded := uniqueStrings(required) && uniqueStrings(uploaded) && sameStringSet(required, uploaded)
		verified := allUploaded && boolValue(input, "manifest_verified")
		return map[string]any{
			"all_required_objects_uploaded": allUploaded,
			"backup_verified":               verified,
			"destruction_allowed":           verified,
			"partial":                       !allUploaded || !boolValue(input, "manifest_verified"),
		}, nil
	case "storage.object.checksum-mismatch-blocks-restore":
		sizeMatches := input["expected_size_bytes"] == input["actual_size_bytes"]
		expected := strings.ToLower(stringValue(input, "expected_checksum"))
		actual := strings.ToLower(stringValue(input, "actual_checksum"))
		checksumMatches := expected == actual
		verified, reason := sizeMatches && checksumMatches, any(nil)
		if !verified {
			reason = "checksum_mismatch"
			if !sizeMatches {
				reason = "size_mismatch"
			}
		}
		return map[string]any{"verified": verified, "restore_allowed": verified, "verification_reason": reason}, nil
	case "storage.object.etag-not-checksum":
		expected := strings.ToLower(stringValue(input, "expected_checksum"))
		verified := false
		for _, key := range []string{"trusted_provider_checksum", "content_digest"} {
			if value, ok := input[key].(string); ok && strings.ToLower(value) == expected {
				verified = true
			}
		}
		source := "insufficient_evidence"
		if verified {
			source = "trusted_checksum"
		}
		return map[string]any{"verified": verified, "verification_source": source}, nil
	case "storage.staging.cannot-delete-production-object":
		target := stringValue(fixture, "target_environment")
		requiredScope := "storage.backup.delete:" + target
		allowed := contains(arrayValue(fixture, "credential_scope"), requiredScope)
		category := any(nil)
		calls := 0
		if allowed {
			calls = 1
		} else {
			category = "authorization"
		}
		return map[string]any{
			"accepted": allowed, "error_category": category,
			"side_effect_permitted": allowed, "adapter_delete_calls": calls,
		}, nil
	case "operations.idempotency.same-key-different-request":
		first, second := asObject(fixture["first_request"]), asObject(fixture["second_request"])
		sameBinding := first["actor_id"] == second["actor_id"] && first["idempotency_key"] == second["idempotency_key"]
		conflict := sameBinding && !reflect.DeepEqual(first, second)
		code := any(nil)
		count := 2
		if conflict {
			code, count = "core.operation.idempotency_conflict", 1
		}
		return map[string]any{"second_request_accepted": !conflict, "error_code": code, "operation_count": count}, nil
	case "operations.events.monotonic-sequence":
		events := arrayValue(fixture, "events")
		if len(events) == 0 {
			return nil, fmt.Errorf("event sequence must not be empty")
		}
		var operationID string
		oneOperation := true
		sequences := make([]int64, 0, len(events))
		for index, rawEvent := range events {
			event := asObject(rawEvent)
			if err := pkg.validateSchema(operationEventSchemaID, event); err != nil {
				return nil, fmt.Errorf("event %d does not match schema: %w", index, err)
			}
			id := stringValue(event, "operation_id")
			if index == 0 {
				operationID = id
			} else if id != operationID {
				oneOperation = false
			}
			sequence, err := integerValue(event["sequence"])
			if err != nil {
				return nil, err
			}
			sequences = append(sequences, sequence)
		}
		increasing := oneOperation
		for i := 1; i < len(sequences); i++ {
			if sequences[i] <= sequences[i-1] {
				increasing = false
			}
		}
		return map[string]any{"schema_valid": true, "sequence_strictly_increasing": increasing}, nil
	case "hosting.provision.valid.standard-os":
		if err := pkg.validateSchema(provisionSchemaID, input); err != nil {
			return nil, err
		}
		return map[string]any{"schema_valid": true, "side_effect_permitted": false}, nil
	default:
		return nil, fmt.Errorf("no evaluator registered for fixture %q", fixtureID)
	}
}

func sameStringSet(left, right []any) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]bool, len(left))
	for _, value := range left {
		str, ok := value.(string)
		if !ok {
			return false
		}
		set[str] = true
	}
	for _, value := range right {
		str, ok := value.(string)
		if !ok || !set[str] {
			return false
		}
	}
	return true
}

func integerValue(value any) (int64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Int64()
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func compareExpected(actual, expected any, path string) error {
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualMap := asObject(actual)
		if actualMap == nil {
			return fmt.Errorf("%s: expected an object", path)
		}
		keys := make([]string, 0, len(expectedValue))
		for key := range expectedValue {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, ok := actualMap[key]
			if !ok {
				return fmt.Errorf("%s.%s: missing expected value", path, key)
			}
			if err := compareExpected(value, expectedValue[key], path+"."+key); err != nil {
				return err
			}
		}
	default:
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("%s: expected %v, got %v", path, expected, actual)
		}
	}
	return nil
}

var legalTransitions = map[[2]string]bool{
	{"unprovisioned", "provisioning"}: true,
	{"provisioning", "preparing"}:     true,
	{"provisioning", "failed"}:        true,
	{"preparing", "restoring"}:        true,
	{"preparing", "starting"}:         true,
	{"preparing", "failed"}:           true,
	{"restoring", "starting"}:         true,
	{"restoring", "stopped"}:          true,
	{"restoring", "failed"}:           true,
	{"starting", "validating"}:        true,
	{"starting", "failed"}:            true,
	{"validating", "ready"}:           true,
	{"validating", "degraded"}:        true,
	{"validating", "failed"}:          true,
	{"ready", "degraded"}:             true,
	{"ready", "draining"}:             true,
	{"ready", "stopping"}:             true,
	{"degraded", "validating"}:        true,
	{"degraded", "draining"}:          true,
	{"degraded", "stopping"}:          true,
	{"degraded", "failed"}:            true,
	{"draining", "ready"}:             true,
	{"draining", "stopping"}:          true,
	{"draining", "failed"}:            true,
	{"stopping", "backing_up"}:        true,
	{"stopping", "stopped"}:           true,
	{"stopping", "failed"}:            true,
	{"backing_up", "starting"}:        true,
	{"backing_up", "stopped"}:         true,
	{"backing_up", "destroying"}:      true,
	{"backing_up", "failed"}:          true,
	{"stopped", "starting"}:           true,
	{"stopped", "restoring"}:          true,
	{"stopped", "backing_up"}:         true,
	{"stopped", "destroying"}:         true,
	{"destroying", "destroyed"}:       true,
	{"destroying", "failed"}:          true,
	{"destroyed", "provisioning"}:     true,
	{"failed", "provisioning"}:        true,
	{"failed", "preparing"}:           true,
	{"failed", "restoring"}:           true,
	{"failed", "stopping"}:            true,
	{"failed", "destroying"}:          true,
}
