package main

func evaluateBackupInitialStatePolicy(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0)
	for _, raw := range arrayValue(asObject(fixture["input"]), "evaluations") {
		evaluation := asObject(raw)
		operation := asObject(evaluation["operation"])
		parameters := asObject(asObject(operation["request_snapshot"])["parameters"])
		initial := stringValue(operation, "backup_initial_lifecycle_state")
		final := stringValue(asObject(asObject(operation["result"])["operation_data"]), "final_lifecycle_state")
		expectedFinal := ""
		switch initial {
		case "ready":
			expectedFinal = "ready"
			if resume, present := parameters["resume_after_backup"].(bool); present && !resume {
				expectedFinal = "stopped"
			}
		case "degraded":
			expectedFinal = "stopped"
			if resume, present := parameters["resume_after_backup"].(bool); present && resume {
				expectedFinal = "ready"
			}
		case "stopped":
			expectedFinal = "stopped"
		}
		result := map[string]any{
			"case_id":                    stringValue(evaluation, "case_id"),
			"operation_schema_valid":     pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation) == nil,
			"final_state_matches_policy": expectedFinal != "" && final == expectedFinal,
		}
		results = append(results, result)
	}
	return map[string]any{"evaluations": results}, nil
}

func init() {
	registerEvaluator(evaluateBackupInitialStatePolicy, "operations.backup.initial-state-resume-policy")
}
