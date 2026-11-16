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
	privacy := velocityPrivacyFixture{
		baseRequest:    asObject(input["transfer_request"]),
		baseResult:     asObject(input["transfer_result"]),
		messageProbes:  arrayValue(input, "privacy_probes"),
		logProbes:      arrayValue(input, "log_redaction_probes"),
		validationTime: input["validation_time"],
	}
	if len(privacy.messageProbes) == 0 || len(privacy.logProbes) == 0 {
		return nil, fmt.Errorf("privacy fixture must include message and log canary probes")
	}

	cases := arrayValue(input, "evaluations")
	evaluations := make([]any, 0, len(cases))
	for _, rawEvaluation := range cases {
		result, err := privacy.evaluateCase(pkg, asObject(rawEvaluation))
		if err != nil {
			return nil, err
		}
		evaluations = append(evaluations, result)
	}
	return map[string]any{"evaluations": evaluations}, nil
}

type velocityPrivacyFixture struct {
	baseRequest, baseResult  map[string]any
	messageProbes, logProbes []any
	validationTime           any
}

func (f velocityPrivacyFixture) evaluateCase(pkg *protocolPackage, evaluation map[string]any) (map[string]any, error) {
	request := asObject(evaluation["request"])
	if request == nil {
		request = f.baseRequest
	}
	result := asObject(evaluation["result"])
	if result == nil {
		result = f.baseResult
	}
	requestValid := pkg.validateSchema(velocityTransferRequestSchemaID, request) == nil
	resultValid := pkg.validateSchema(velocityTransferResultSchemaID, result) == nil
	probes, err := f.inspectProbes(request, result, arrayValue(evaluation, "diagnostic_log_lines"))
	if err != nil {
		return nil, err
	}

	transferValid := false
	if requestValid && resultValid {
		transferValid, err = evaluateVelocityTransferBinding(pkg, request, result, f.validationTime)
		if err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"case_id":                         stringValue(evaluation, "case_id"),
		"request_schema_valid":            requestValid,
		"result_schema_valid":             resultValid,
		"transfer_result_valid_and_bound": transferValid,
		"request_contains_privacy_probe":  probes.request,
		"result_contains_privacy_probe":   probes.result,
		"logs_contain_privacy_probe":      probes.logs,
		"logs_contain_session_id":         probes.sessionID,
		"privacy_compliant":               transferValid && !probes.found(),
	}, nil
}

func evaluateVelocityTransferBinding(pkg *protocolPackage, request, result map[string]any, validationTime any) (bool, error) {
	state, err := evaluate(pkg, map[string]any{
		"fixture_id": "velocity-transfer-partial-results",
		"input": map[string]any{
			"transfer_request": request,
			"transfer_result":  result,
			"validation_time":  validationTime,
		},
	})
	if err != nil {
		return false, err
	}
	return boolValue(state, "result_valid_and_bound"), nil
}

type velocityPrivacyProbeMatches struct {
	request, result, logs, sessionID bool
}

func (m velocityPrivacyProbeMatches) found() bool {
	return m.request || m.result || m.logs || m.sessionID
}

func (f velocityPrivacyFixture) inspectProbes(request, result map[string]any, logLines []any) (velocityPrivacyProbeMatches, error) {
	requestMatch, err := containsPrivacyProbe(request, f.messageProbes)
	if err != nil {
		return velocityPrivacyProbeMatches{}, err
	}
	resultMatch, err := containsPrivacyProbe(result, f.messageProbes)
	if err != nil {
		return velocityPrivacyProbeMatches{}, err
	}
	logText, err := joinDiagnosticLogs(logLines)
	if err != nil {
		return velocityPrivacyProbeMatches{}, err
	}
	logMatch, err := containsPrivacyProbe(logText, f.messageProbes)
	if err != nil {
		return velocityPrivacyProbeMatches{}, err
	}
	sessionMatch, err := containsPrivacyProbe(logText, f.logProbes)
	if err != nil {
		return velocityPrivacyProbeMatches{}, err
	}
	return velocityPrivacyProbeMatches{
		request: requestMatch, result: resultMatch, logs: logMatch, sessionID: sessionMatch,
	}, nil
}

func joinDiagnosticLogs(lines []any) (string, error) {
	text := make([]string, 0, len(lines))
	for _, rawLine := range lines {
		line, ok := rawLine.(string)
		if !ok {
			return "", fmt.Errorf("diagnostic log lines must be strings")
		}
		text = append(text, line)
	}
	return strings.Join(text, "\n"), nil
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
