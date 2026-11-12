package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	velocityTransferRequestSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:velocity-player-transfer-request"
	velocityTransferResultSchemaID  = "urn:gameserver-controller:0.21.0-draft:integrations:velocity-player-transfer-result"
)

func evaluateVelocityTransferPrivacy(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	baseRequest := asObject(input["transfer_request"])
	baseResult := asObject(input["transfer_result"])
	probes := arrayValue(input, "privacy_probes")
	logRedactionProbes := arrayValue(input, "log_redaction_probes")
	if len(probes) == 0 || len(logRedactionProbes) == 0 {
		return nil, fmt.Errorf("privacy fixture must include message and log canary probes")
	}

	evaluations := make([]any, 0, len(arrayValue(input, "evaluations")))
	for _, rawEvaluation := range arrayValue(input, "evaluations") {
		evaluation := asObject(rawEvaluation)
		request := asObject(evaluation["request"])
		if request == nil {
			request = baseRequest
		}
		result := asObject(evaluation["result"])
		if result == nil {
			result = baseResult
		}
		requestValid := pkg.validateSchema(velocityTransferRequestSchemaID, request) == nil
		resultValid := pkg.validateSchema(velocityTransferResultSchemaID, result) == nil
		requestContainsProbe, err := containsPrivacyProbe(request, probes)
		if err != nil {
			return nil, err
		}
		resultContainsProbe, err := containsPrivacyProbe(result, probes)
		if err != nil {
			return nil, err
		}
		logLines := arrayValue(evaluation, "diagnostic_log_lines")
		logText := make([]string, 0, len(logLines))
		for _, rawLine := range logLines {
			line, ok := rawLine.(string)
			if !ok {
				return nil, fmt.Errorf("diagnostic log lines must be strings")
			}
			logText = append(logText, line)
		}
		joinedLogs := strings.Join(logText, "\n")
		logsContainProbe, err := containsPrivacyProbe(joinedLogs, probes)
		if err != nil {
			return nil, err
		}
		logsContainSessionID, err := containsPrivacyProbe(joinedLogs, logRedactionProbes)
		if err != nil {
			return nil, err
		}

		transferValid := false
		if requestValid && resultValid {
			state, err := evaluate(pkg, map[string]any{
				"fixture_id": "velocity-transfer-partial-results",
				"input": map[string]any{
					"transfer_request": request,
					"transfer_result":  result,
					"validation_time":  input["validation_time"],
				},
			})
			if err != nil {
				return nil, err
			}
			transferValid = boolValue(state, "result_valid_and_bound")
		}
		privacyCompliant := requestValid && resultValid && transferValid &&
			!requestContainsProbe && !resultContainsProbe && !logsContainProbe && !logsContainSessionID
		caseResult := map[string]any{}
		caseResult["case_id"] = stringValue(evaluation, "case_id")
		caseResult["request_schema_valid"] = requestValid
		caseResult["result_schema_valid"] = resultValid
		caseResult["transfer_result_valid_and_bound"] = transferValid
		caseResult["request_contains_privacy_probe"] = requestContainsProbe
		caseResult["result_contains_privacy_probe"] = resultContainsProbe
		caseResult["logs_contain_privacy_probe"] = logsContainProbe
		caseResult["logs_contain_session_id"] = logsContainSessionID
		caseResult["privacy_compliant"] = privacyCompliant
		evaluations = append(evaluations, caseResult)
	}
	return map[string]any{"evaluations": evaluations}, nil
}

func containsPrivacyProbe(value any, probes []any) (bool, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	text := string(encoded)
	for _, rawProbe := range probes {
		probe, ok := rawProbe.(string)
		if !ok || probe == "" {
			return false, fmt.Errorf("privacy canaries must be non-empty strings")
		}
		if strings.Contains(text, probe) {
			return true, nil
		}
	}
	return false, nil
}

func init() {
	registerEvaluator(evaluateVelocityTransferPrivacy, "velocity-transfer-privacy-canaries")
}
