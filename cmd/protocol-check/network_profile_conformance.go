package main

import (
	"sort"
	"strings"
)

const networkProfileSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:network-profile"

// transferRequirement stands for "a transfer capability such as
// integration.velocity.players.transfer" in integrations section 6.
const transferRequirement = "integration.*.players.transfer"

// evaluateNetworkProfileRequirements is the reference evaluator for
// integrations section 6. It derives required capabilities from the network
// profile, then checks them against adapters registered in the server's
// environment.
func evaluateNetworkProfileRequirements(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	evaluations := make([]any, 0, len(arrayValue(input, "evaluations")))
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		profile := asObject(evaluation["network_profile"])
		environment := stringValue(evaluation, "environment_id")
		required := referenceNetworkRequirements(profile)
		adapters := map[string]map[string]bool{}
		adapterEnvironment := map[string]string{}
		for _, rawAdapter := range arrayValue(evaluation, "registered_adapters") {
			registered := asObject(rawAdapter)
			id := stringValue(registered, "adapter_id")
			adapterEnvironment[id] = stringValue(registered, "environment_id")
			adapters[id] = map[string]bool{}
			for _, capability := range arrayValue(registered, "capabilities") {
				if name, ok := capability.(string); ok {
					adapters[id][name] = true
				}
			}
		}
		roleAdapter := map[string]string{"proxy": stringValue(profile, "proxy_adapter_id"), "dns": stringValue(profile, "dns_adapter_id")}
		missing := []map[string]string{}
		for _, requirement := range required {
			id := roleAdapter[requirement["role"]]
			capabilities, registered := adapters[id]
			if !registered || adapterEnvironment[id] != environment || !referenceSatisfies(capabilities, requirement["capability"]) {
				missing = append(missing, requirement)
			}
		}
		evaluations = append(evaluations, map[string]any{
			"case_id":               stringValue(evaluation, "case_id"),
			"profile_schema_valid":  pkg.validateSchema(networkProfileSchemaID, profile) == nil,
			"required_capabilities": requirementsJSON(required),
			"missing_capabilities":  requirementsJSON(missing),
			"planning_permitted":    len(missing) == 0,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func referenceNetworkRequirements(profile map[string]any) []map[string]string {
	var required []map[string]string
	add := func(role string, capabilities ...string) {
		for _, capability := range capabilities {
			required = append(required, map[string]string{"role": role, "capability": capability})
		}
	}
	dns := []string{"integration.dns.record.inspect", "integration.dns.record.upsert", "integration.dns.record.delete"}
	switch stringValue(profile, "join_mode") {
	case "proxy":
		add("proxy", "integration.proxy.backend.inspect", "integration.proxy.backend.register", "integration.proxy.backend.drain",
			"integration.proxy.backend.unregister", "integration.proxy.cohort.inspect", "integration.proxy.session.inspect", transferRequirement)
		for _, rawBackend := range arrayValue(profile, "backends") {
			if stringValue(asObject(rawBackend), "address_mode") == "hostname" {
				add("dns", dns...)
				break
			}
		}
	case "direct_dns":
		add("dns", dns...)
	}
	sort.Slice(required, func(i, j int) bool {
		if required[i]["role"] != required[j]["role"] {
			return required[i]["role"] < required[j]["role"]
		}
		return required[i]["capability"] < required[j]["capability"]
	})
	return required
}

func referenceSatisfies(capabilities map[string]bool, requirement string) bool {
	if requirement != transferRequirement {
		return capabilities[requirement]
	}
	for name := range capabilities {
		if strings.HasPrefix(name, "integration.") && strings.HasSuffix(name, ".players.transfer") && strings.Count(name, ".") == 3 {
			return true
		}
	}
	return false
}

func requirementsJSON(requirements []map[string]string) []any {
	result := make([]any, 0, len(requirements))
	for _, requirement := range requirements {
		result = append(result, map[string]any{"role": requirement["role"], "capability": requirement["capability"]})
	}
	return result
}
