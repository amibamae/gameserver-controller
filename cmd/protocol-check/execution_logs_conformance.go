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
		totalBytes, logText := summarizeExecutionLogLines(arrayValue(response, "lines"))
		withinBound := maxBytesErr == nil && totalBytes <= maxBytes
		probesAbsent := executionLogProbesAbsent(logText, arrayValue(evaluation, "redaction_probes"))
		schemasValid := requestValid && responseValid
		caseResult := map[string]any{
			"case_id":                 stringValue(evaluation, "case_id"),
			"schemas_valid":           schemasValid,
			"unit_bound":              unitBound,
			"total_bytes":             totalBytes,
			"logs_within_bound":       withinBound,
			"redaction_probes_absent": probesAbsent,
			"response_accepted":       schemasValid && unitBound && withinBound && probesAbsent,
		}
		evaluations = append(evaluations, caseResult)
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func summarizeExecutionLogLines(rawLines []any) (int64, string) {
	var totalBytes int64
	lines := make([]string, 0, len(rawLines))
	for index, rawLine := range rawLines {
		line, ok := rawLine.(string)
		if !ok {
			continue
		}
		lines = append(lines, line)
		totalBytes += int64(len(line))
		// Use the original position so malformed fixtures retain their byte accounting.
		if index > 0 {
			totalBytes++
		}
	}
	return totalBytes, strings.Join(lines, "\n")
}

func executionLogProbesAbsent(logText string, probes []any) bool {
	for _, rawProbe := range probes {
		probe, ok := rawProbe.(string)
		if !ok || probe == "" || strings.Contains(logText, probe) {
			return false
		}
	}
	return true
}

func init() {
	registerEvaluator(evaluateExecutionLogRead, "execution.unit.logs.byte-bound-and-redaction")
}
