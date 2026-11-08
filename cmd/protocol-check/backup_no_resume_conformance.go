package main

func evaluateBackupNoResumePolicy(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	results := make([]any, 0)
	for _, raw := range arrayValue(asObject(fixture["input"]), "evaluations") {
		evaluation := asObject(raw)
		operation := asObject(evaluation["operation"])
		parameters := asObject(asObject(operation["request_snapshot"])["parameters"])
		data := asObject(asObject(operation["result"])["operation_data"])
		result := map[string]any{}
		result["case_id"] = stringValue(evaluation, "case_id")
		result["parameters_schema_valid"] = pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:backup-parameters", parameters) == nil
		result["operation_schema_valid"] = pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:operations:operation", operation) == nil
		resume, explicit := parameters["resume_after_backup"].(bool)
		result["final_state_matches_policy"] = explicit && !resume && stringValue(data, "final_lifecycle_state") == "stopped"
		results = append(results, result)
	}
	return map[string]any{"evaluations": results}, nil
}
