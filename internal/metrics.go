// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metric namespaces and subsystems
const (
	Namespace         = "gameserver_controller"
	SubsystemStore    = "store"
	SubsystemWorkflow = "workflow"
	SubsystemAdapter  = "adapter"
)

// Store metrics track operation persistence health
var (
	// Operations processed by type
	OperationCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemStore,
			Name:      "operations_total",
			Help:      "Total number of operations processed, grouped by operation type.",
		},
		[]string{"type", "status"},
	)

	// Persistence latency (histogram for SLI measurement)
	WriteDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: SubsystemStore,
			Name:      "write_duration_seconds",
			Help:      "Duration of write operations to the store, grouped by operation type.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"type"},
	)

	// Recovery events
	RecoveryCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemStore,
			Name:      "recoveries_total",
			Help:      "Total number of recovery attempts, grouped by cause.",
		},
		[]string{"cause"}, // crash, manual, automatic
	)

	// Data integrity violations
	IntegrityViolations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemStore,
			Name:      "integrity_violations_total",
			Help:      "Count of detected data integrity violations.",
		},
		[]string{"violation_type"}, // corruption, checksum_mismatch, etc.
	)
)

// Workflow metrics track execution health
var (
	// Workflow dispatches
	DispatchCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemWorkflow,
			Name:      "dispatches_total",
			Help:      "Total number of workflow steps dispatched to adapters.",
		},
		[]string{"adapter"},
	)

	// Workflow outcomes
	WorkflowOutcomes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemWorkflow,
			Name:      "outcomes_total",
			Help:      "Total workflow step outcomes, grouped by status.",
		},
		[]string{"outcome"}, // success, failed, suspended, complete, etc.
	)

	// Recovery path metrics
	ReconcileAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemWorkflow,
			Name:      "reconcile_attempts_total",
			Help:      "Number of reconciliation attempts for uncertain steps.",
		},
		[]string{"operation_id"}, // Consider sampling or hashing for cardinality
	)

	// Timeout events
	TimeoutEvents = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: Namespace,
			Subsystem: SubsystemWorkflow,
			Name:      "timeouts_total",
			Help:      "Number of timeout events detected.",
		},
		[]string{"adapter"},
	)
)

// Adapter metrics track provider health
var (
	// Provider status
	AdapterHealth = prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace: Namespace,
			Subsystem: SubsystemAdapter,
			Name:      "health",
			Help:      "1.0 if adapter is healthy, 0.0 if unhealthy.",
		},
		func() float64 {
			return 0 // Placeholder - implement in actual adapter package
		},
	)

	// Adapter response times
	AdapterResponseTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: Namespace,
			Subsystem: SubsystemAdapter,
			Name:      "response_time_seconds",
			Help:      "Adapter invocation response time.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"adapter"},
	)
)

// Register all metrics with Prometheus registry
func init() {
	prometheus.MustRegister(OperationCount)
	prometheus.MustRegister(WriteDuration)
	prometheus.MustRegister(RecoveryCount)
	prometheus.MustRegister(IntegrityViolations)
	prometheus.MustRegister(DispatchCount)
	prometheus.MustRegister(WorkflowOutcomes)
	prometheus.MustRegister(ReconcileAttempts)
	prometheus.MustRegister(TimeoutEvents)
	prometheus.MustRegister(AdapterHealth)
	prometheus.MustRegister(AdapterResponseTime)
}

// GetMetricNames returns a list of all metric names for documentation/monitoring dashboards
func GetMetricNames() []string {
	return []string{
		"gameserver_controller_store_operations_total",
		"gameserver_controller_store_write_duration_seconds",
		"gameserver_controller_store_recoveries_total",
		"gameserver_controller_store_integrity_violations_total",
		"gameserver_controller_workflow_dispatches_total",
		"gameserver_controller_workflow_outcomes_total",
		"gameserver_controller_workflow_reconcile_attempts_total",
		"gameserver_controller_workflow_timeouts_total",
		"gameserver_controller_adapter_health",
		"gameserver_controller_adapter_response_time_seconds",
	}
}
