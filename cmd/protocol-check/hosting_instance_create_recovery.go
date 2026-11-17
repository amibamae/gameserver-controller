package main

const (
	hostingInstanceCreateInputSchemaID = "urn:gameserver-controller:0.21.0-draft:hosting:instance-create-input"
	hostingInstanceFindInputSchemaID   = "urn:gameserver-controller:0.21.0-draft:hosting:instance-find-by-resource-key-input"
	hostingInstanceFindOutputSchemaID  = "urn:gameserver-controller:0.21.0-draft:hosting:instance-find-by-resource-key-output"
)

func evaluateInstanceCreateRecovery(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	createRequest := asObject(input["create_request"])
	createValid := pkg.validateSchema(hostingInstanceCreateInputSchemaID, createRequest) == nil
	resourceKey := stringValue(createRequest, "resource_key")

	lookups := make([]any, 0, len(arrayValue(input, "lookups")))
	for _, rawLookup := range arrayValue(input, "lookups") {
		lookup := asObject(rawLookup)
		output := asObject(lookup["output"])
		lookupInput := map[string]any{"resource_key": resourceKey}
		inputValid := pkg.validateSchema(hostingInstanceFindInputSchemaID, lookupInput) == nil
		outputValid := pkg.validateSchema(hostingInstanceFindOutputSchemaID, output) == nil
		bound := resourceKey != "" && stringValue(output, "resource_key") == resourceKey
		matches := arrayValue(output, "matches")
		schemaValid := inputValid && outputValid
		exactMatch := hasExactRecoveryInstance(matches, stringValue(createRequest, "environment_id"))
		reconciled := createValid && schemaValid && bound && exactMatch
		lookups = append(lookups, map[string]any{
			"case_id":              stringValue(lookup, "case_id"),
			"schema_valid":         schemaValid,
			"match_count":          len(matches),
			"reconcile_as_created": reconciled,
			"retry_create":         false,
			"operator_review":      !reconciled,
		})
	}
	return map[string]any{
		"create_request_schema_valid": createValid,
		"lookups":                     lookups,
		"retry_requires_authoritative_safe_to_retry_evidence": true,
	}, nil
}

func hasExactRecoveryInstance(matches []any, environmentID string) bool {
	if len(matches) != 1 {
		return false
	}
	ref := asObject(asObject(matches[0])["instance_ref"])
	return stringValue(ref, "resource_kind") == "instance" &&
		stringValue(ref, "environment_id") == environmentID &&
		stringValue(ref, "adapter_id") != "" && stringValue(ref, "external_id") != ""
}

func init() {
	registerEvaluator(evaluateInstanceCreateRecovery, "hosting.instance.find-by-resource-key.create-recovery")
}
