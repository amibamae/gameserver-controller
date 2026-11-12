package main

import (
	"reflect"
	"strings"
)

func evaluateExecutionHealthDiagnostics(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	evaluations := make([]any, 0)
	for _, raw := range arrayValue(asObject(fixture["input"]), "evaluations") {
		evaluation := asObject(raw)
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		schemasValid := pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-input", request) == nil &&
			pkg.validateSchema("urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-health-inspect-output", response) == nil
		unitBound := reflect.DeepEqual(request["unit_ref"], response["unit_ref"])
		lines := make([]string, 0)
		for _, rawLine := range arrayValue(response, "safe_diagnostics") {
			line, ok := rawLine.(string)
			if ok {
				lines = append(lines, line)
			}
		}
		text := strings.Join(lines, "\n")
		probes := arrayValue(evaluation, "redaction_probes")
		probesAbsent := len(probes) > 0
		for _, rawProbe := range probes {
			probe, ok := rawProbe.(string)
			if !ok || probe == "" || strings.Contains(text, probe) {
				probesAbsent = false
			}
		}
		accepted := schemasValid && unitBound && probesAbsent
		evaluations = append(evaluations, map[string]any{
			"case_id":                 stringValue(evaluation, "case_id"),
			"schemas_valid":           schemasValid,
			"unit_bound":              unitBound,
			"redaction_probes_absent": probesAbsent,
			"response_accepted":       accepted,
			"healthy_confirmed":       accepted && stringValue(response, "health") == "healthy",
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func init() {
	registerEvaluator(evaluateExecutionHealthDiagnostics, "execution.unit.health.diagnostic-redaction-and-binding")
}
