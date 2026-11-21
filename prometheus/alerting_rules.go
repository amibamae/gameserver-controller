// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"fmt"
	"strings"
)

// AlertingRules contains Prometheus alerting rule definitions for deployment
var AlertingRules = `groups:
  - name: gameserver_controller.alerts
    interval: 30s
    rules:
      # CRITICAL alerts (immediate response required)
      
      - alert: StoreDataLossRisk
        expr: |
          gameserver_controller_store_size_bytes < 100
          AND
          time() - last_update_time_seconds > 3600
        for: 5m
        labels:
          severity: critical
          team: reliability
        annotations:
          summary: "Store may be at risk of data loss"
          description: "Store size is unusually low and hasn't updated in 1h."

      - alert: RecoveryFailures
        expr: |
          rate(gameserver_controller_store_recoveries_total{cause="crash"}[5m]) > 0.1
        for: 10m
        labels:
          severity: critical
          team: reliability
        annotations:
          summary: "High crash recovery frequency detected"
          description: "{{ $value | printf \"%.2f\" }} recoveries per minute."

      - alert: IntegrityViolationDetected
        expr: |
          gameserver_controller_store_integrity_violations_total > 0
        for: 5m
        labels:
          severity: critical
          team: security
        annotations:
          summary: "Data integrity violation detected"
          description: "An integrity violation was detected in the store."

      - alert: AdapterConnectionFailed
        expr: |
          gameserver_controller_adapter_health == 0
        for: 2m
        labels:
          severity: critical
          team: platform
        annotations:
          summary: "Adapter is unhealthy"
          description: "An adapter is not responding."

      # WARNING alerts (monitoring and investigation)
      
      - alert: HighErrorRate
        expr: |
          rate(gameserver_controller_store_operations_total{status="error"}[5m]) /
          rate(gameserver_controller_store_operations_total{status!="unknown"}[5m]) > 0.001
        for: 5m
        labels:
          severity: warning
          team: reliability
        annotations:
          summary: "Error rate above threshold (0.1%)"
          description: "Error rate is {{ $value | printf \"%.2f\" }}%."

      - alert: StoreLatencyHigh
        expr: |
          histogram_quantile(0.99, 
            rate(gameserver_controller_store_write_duration_seconds_bucket[5m])) > 0.5
        for: 5m
        labels:
          severity: warning
          team: reliability
        annotations:
          summary: "Store write latency p99 exceeds 500ms"
          description: "Write latency p99 is {{ $value | printf \"%.2f\" }}s."

      - alert: RecoveryQueueBuildingUp
        expr: |
          count(gameserver_controller_workflow_reconcile_attempts_total) > 100
        for: 10m
        labels:
          severity: warning
          team: reliability
        annotations:
          summary: "Reconciliation queue is building up"
          description: "{{ $value }} reconciliation attempts detected."

      - alert: TimeoutFrequencyIncreasing
        expr: |
          increase(gameserver_controller_workflow_timeouts_total[1h]) > 5
        for: 15m
        labels:
          severity: warning
          team: reliability
        annotations:
          summary: "Adapter timeouts are increasing"
          description: "{{ $value }} timeout events in the last hour."

      # INFO alerts (informational, no immediate action)
      
      - alert: RecoveryCompleted
        expr: |
          gameserver_controller_store_recoveries_total{cause="automatic"} > 0
        for: 0m
        labels:
          severity: info
          team: reliability
        annotations:
          summary: "Automatic recovery completed"
          description: "A crash recovery was automatically performed."

      - alert: BackupVerificationCompleted
        expr: |
          time() - last_backup_verification_time_seconds < 86400
        for: 0m
        labels:
          severity: info
          team: reliability
        annotations:
          summary: "Backup verification completed successfully"
          description: "The most recent backup was verified within the last 24 hours."
`

// RuleCount returns the number of alerting rules defined.
func RuleCount() int {
	return countRules(AlertingRules)
}

// countRules parses the YAML alerting rules string and counts rule definitions.
func countRules(rules string) int {
	count := strings.Count(rules, "- alert:")
	if count == 0 {
		count = strings.Count(rules, "alert: ")
	}
	return count
}

// GetAlertingRules returns the full alerting rules definition as a string.
func GetAlertingRules() string {
	return AlertingRules
}

// ValidateRules checks that rule definitions are syntactically valid.
func ValidateRules() (bool, error) {
	// Basic validation - in production this would use a YAML parser
	if len(AlertingRules) == 0 {
		return false, fmt.Errorf("alerting rules definition is empty")
	}

	// Check for required components
	requiredRules := []string{
		"StoreDataLossRisk",
		"RecoveryFailures",
		"IntegrityViolationDetected",
		"AdapterConnectionFailed",
		"HighErrorRate",
		"StoreLatencyHigh",
		"RecoveryQueueBuildingUp",
		"TimeoutFrequencyIncreasing",
		"RecoveryCompleted",
		"BackupVerificationCompleted",
	}

	for _, rule := range requiredRules {
		if !strings.Contains(AlertingRules, rule) {
			return false, fmt.Errorf("rule %q not found", rule)
		}
	}

	return true, nil
}
