package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	notificationEventSchemaID  = "urn:gameserver-controller:0.21.0-draft:integrations:notification-event"
	notificationInputSchemaID  = "urn:gameserver-controller:0.21.0-draft:integrations:notification-send-input"
	notificationOutputSchemaID = "urn:gameserver-controller:0.21.0-draft:integrations:notification-send-output"
)

// referenceOutagePolicy is the section 7 probe policy, in seconds.
type referenceOutagePolicy struct {
	checkInterval, threshold, initialBackoff, maxBackoff int64
}

func outagePolicy(object map[string]any) (referenceOutagePolicy, error) {
	var policy referenceOutagePolicy
	for _, field := range []struct {
		name   string
		target *int64
	}{
		{"check_interval_seconds", &policy.checkInterval}, {"failure_threshold", &policy.threshold},
		{"initial_backoff_seconds", &policy.initialBackoff}, {"max_backoff_seconds", &policy.maxBackoff},
	} {
		value, err := integerValue(object[field.name])
		if err != nil {
			return policy, fmt.Errorf("policy %s: %w", field.name, err)
		}
		*field.target = value
	}
	return policy, nil
}

// referenceBackoff is initial·2^(n-1), capped at the maximum.
func referenceBackoff(policy referenceOutagePolicy, failures int64) int64 {
	delay := policy.initialBackoff
	for i := int64(1); i < failures && delay < policy.maxBackoff; i++ {
		delay *= 2
	}
	return min(delay, policy.maxBackoff)
}

func referenceEventID(adapterID, eventType, outageStartedAt, occurredAt string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{adapterID, eventType, outageStartedAt, occurredAt}, "\x00")))
	return "evt-" + hex.EncodeToString(sum[:16])
}

// evaluateProxyOutageTransitions is the reference evaluator for the section
// 7 outage rules: threshold, backoff, reason change, recovery, and flap
// suppression.
func evaluateProxyOutageTransitions(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	subject := asObject(input["subject"])
	adapterID := stringValue(subject, "adapter_id")
	evaluations := []any{}
	for _, raw := range arrayValue(input, "evaluations") {
		evaluation := asObject(raw)
		policyObject := asObject(input["default_policy"])
		if override := asObject(evaluation["policy"]); override != nil {
			policyObject = override
		}
		policy, err := outagePolicy(policyObject)
		if err != nil {
			return nil, err
		}
		var (
			failures      int64
			start         string
			declared      string
			events        = []any{}
			delays        = []any{}
			schemaInvalid bool
		)
		emit := func(eventType, at string) {
			severity := "critical"
			if eventType == "integration.proxy.recovered" {
				severity = "info"
			}
			count := max(failures, 1)
			full := map[string]any{
				"event_id": referenceEventID(adapterID, eventType, start, at), "event_type": eventType, "severity": severity,
				"occurred_at": at, "environment_id": stringValue(subject, "environment_id"), "adapter_id": adapterID, "subject": subject,
				"outage_started_at": start, "consecutive_failures": json.Number(strconv.FormatInt(count, 10)),
				"summary": "Proxy " + adapterID + " event " + eventType + " for the outage that started " + start + ".",
			}
			if pkg.validateSchema(notificationEventSchemaID, full) != nil {
				schemaInvalid = true
			}
			events = append(events, map[string]any{
				"event_id": full["event_id"], "event_type": eventType, "severity": severity, "occurred_at": at,
				"outage_started_at": start, "consecutive_failures": full["consecutive_failures"],
			})
		}
		for _, rawProbe := range arrayValue(evaluation, "probes") {
			probe := asObject(rawProbe)
			at, result := stringValue(probe, "observed_at"), stringValue(probe, "result")
			if result == "up" {
				if declared != "" {
					emit("integration.proxy.recovered", at)
				}
				failures, start, declared = 0, "", ""
				delays = append(delays, json.Number(strconv.FormatInt(policy.checkInterval, 10)))
				continue
			}
			if failures == 0 {
				start = at
			}
			failures++
			if (declared == "" && failures >= policy.threshold) || (declared != "" && declared != result) {
				declared = result
				eventType := "integration.proxy.unreachable"
				if result == "identity_mismatch" {
					eventType = "integration.proxy.identity_mismatch"
				}
				emit(eventType, at)
			}
			delays = append(delays, json.Number(strconv.FormatInt(referenceBackoff(policy, failures), 10)))
		}
		evaluations = append(evaluations, map[string]any{
			"case_id": stringValue(evaluation, "case_id"), "events": events,
			"next_probe_delays_seconds": delays, "events_schema_valid": !schemaInvalid,
		})
	}
	return map[string]any{"evaluations": evaluations}, nil
}

// evaluateNotificationSend is the reference evaluator for delivery outcomes
// and the no-player-data rule.
func evaluateNotificationSend(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	maxAttempts, err := integerValue(input["max_attempts"])
	if err != nil {
		return nil, fmt.Errorf("max_attempts: %w", err)
	}
	probes := []string{}
	for _, probe := range arrayValue(input, "privacy_probes") {
		if text, ok := probe.(string); ok && text != "" {
			probes = append(probes, text)
		}
	}
	evaluations := []any{}
	for _, raw := range arrayValue(input, "evaluations") {
		evaluation := asObject(raw)
		attempt, err := integerValue(evaluation["attempt"])
		if err != nil {
			return nil, fmt.Errorf("attempt: %w", err)
		}
		sendInput := evaluation["input"]
		inputValid := pkg.validateSchema(notificationInputSchemaID, sendInput) == nil
		absent := true
		for _, probe := range probes {
			if containsSubstring(sendInput, probe) {
				absent = false
			}
		}
		result := map[string]any{
			"case_id": stringValue(evaluation, "case_id"), "input_schema_valid": inputValid,
			"privacy_probes_absent": absent, "send_permitted": inputValid && absent,
			"delivered": false, "retry_permitted": false, "retry_delay_seconds": json.Number("0"),
		}
		if inputValid && absent {
			output := evaluation["output"]
			outputValid := pkg.validateSchema(notificationOutputSchemaID, output) == nil
			sentID := stringValue(asObject(asObject(sendInput)["event"]), "event_id")
			bound := sentID != "" && stringValue(asObject(output), "event_id") == sentID
			effective := "outcome_unknown"
			if outputValid && bound {
				effective = stringValue(asObject(output), "outcome")
			}
			retry := effective == "outcome_unknown" && attempt < maxAttempts
			delay := int64(0)
			if retry {
				delay = 30 << (attempt - 1)
			}
			result["output_schema_valid"], result["output_bound"], result["effective_outcome"] = outputValid, bound, effective
			result["delivered"], result["retry_permitted"] = effective == "delivered", retry
			result["retry_delay_seconds"] = json.Number(strconv.FormatInt(delay, 10))
		}
		evaluations = append(evaluations, result)
	}
	return map[string]any{"evaluations": evaluations}, nil
}

// containsSubstring reports whether any string or member name in value
// contains target.
func containsSubstring(value any, target string) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			if strings.Contains(name, target) || containsSubstring(child, target) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsSubstring(child, target) {
				return true
			}
		}
	case string:
		return strings.Contains(typed, target)
	}
	return false
}

func init() {
	registerEvaluator(evaluateProxyOutageTransitions, "integration.notification.proxy-outage-transitions")
	registerEvaluator(evaluateNotificationSend, "integration.notification.send.outcome-and-privacy")
}
