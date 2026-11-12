package main

import (
	"reflect"
	"strings"
)

const systemdUnitLogsReadInputSchemaID = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-input"
const systemdUnitLogsReadOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:execution:systemd-unit-logs-read-output"

func evaluateExecutionLogRead(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	evaluations := make([]any, 0, len(arrayValue(input, "evaluations")))
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		request := asObject(evaluation["request"])
		response := asObject(evaluation["response"])
		requestValid := pkg.validateSchema(systemdUnitLogsReadInputSchemaID, request) == nil
		responseValid := pkg.validateSchema(systemdUnitLogsReadOutputSchemaID, response) == nil
		unitBound := reflect.DeepEqual(request["unit_ref"], response["unit_ref"])
		maxBytes, maxBytesErr := integerValue(request["max_bytes"])
		var totalBytes int64
		lines := make([]string, 0, len(arrayValue(response, "lines")))
		for index, rawLine := range arrayValue(response, "lines") {
			line, ok := rawLine.(string)
			if !ok {
				continue
			}
			lines = append(lines, line)
			totalBytes += int64(len([]byte(line)))
			if index > 0 {
				totalBytes++
			}
		}
		withinBound := maxBytesErr == nil && totalBytes <= maxBytes
		logText := strings.Join(lines, "\n")
		probesAbsent := true
		for _, rawProbe := range arrayValue(evaluation, "redaction_probes") {
			probe, ok := rawProbe.(string)
			if !ok || probe == "" || strings.Contains(logText, probe) {
				probesAbsent = false
			}
		}
		accepted := requestValid && responseValid && unitBound && withinBound && probesAbsent
		caseResult := map[string]any{}
		caseResult["case_id"] = stringValue(evaluation, "case_id")
		caseResult["schemas_valid"] = requestValid && responseValid
		caseResult["unit_bound"] = unitBound
		caseResult["total_bytes"] = totalBytes
		caseResult["logs_within_bound"] = withinBound
		caseResult["redaction_probes_absent"] = probesAbsent
		caseResult["response_accepted"] = accepted
		evaluations = append(evaluations, caseResult)
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func init() {
	registerEvaluator(evaluateExecutionLogRead, "execution.unit.logs.byte-bound-and-redaction")
}
