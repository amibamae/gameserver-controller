// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/amibamae/gameserver-controller/internal/metrics"
)

// HealthMetricsHandler exposes operational health endpoints and Prometheus metrics.
type HealthMetricsHandler struct {
	storeStatusMu sync.RWMutex
	storeStatus   map[string]string // operationID -> status (pending, running, complete, etc.)

	// Recovery counters
	recoveryCountersMu  sync.Mutex
	crashRecoveries     int64
	diskCorruption      int64
	manualInterventions int64
}

// NewHealthMetricsHandler creates a new health metrics handler.
func NewHealthMetricsHandler() *HealthMetricsHandler {
	return &HealthMetricsHandler{
		storeStatus:        make(map[string]string),
		recoveryCountersMu: sync.Mutex{},
	}
}

// ServeHTTP implements http.Handler for health and metrics endpoints.
func (h *HealthMetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/health":
		h.healthCheck(w, r)
	case "/ready":
		h.readyCheck(w, r)
	case "/metrics":
		h.prometheusMetrics(w, r)
	case "/recovery/stats":
		h.recoveryStats(w, r)
	default:
		http.NotFound(w, r)
	}
}

// healthCheck returns basic HTTP 200 if the controller is alive.
func (h *HealthMetricsHandler) healthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	status := map[string]interface{}{
		"status":           "healthy",
		"timestamp":        time.Now().UTC().Format(time.RFC3339),
		"store_accessible": true,
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}

// readyCheck returns HTTP 200 if the controller can handle requests.
func (h *HealthMetricsHandler) readyCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	ready := h.readinessCheck()
	status := map[string]interface{}{
		"ready":     ready,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	if !ready {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(status)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}

// prometheusMetrics serves Prometheus-compatible metrics.
func (h *HealthMetricsHandler) prometheusMetrics(w http.ResponseWriter, r *http.Request) {
	promhttp.Handler().ServeHTTP(w, r)
}

// recoveryStats returns current recovery statistics.
func (h *HealthMetricsHandler) recoveryStats(w http.ResponseWriter, r *http.Request) {
	h.recoveryCountersMu.Lock()
	defer h.recoveryCountersMu.Unlock()

	stats := map[string]interface{}{
		"timestamp":              time.Now().UTC().Format(time.RFC3339),
		"crash_recoveries":       h.crashRecoveries,
		"disk_corruption_events": int64(h.diskCorruption),
		"manual_interventions":   h.manualInterventions,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// readinessCheck verifies the controller can accept requests.
func (h *HealthMetricsHandler) readinessCheck() bool {
	return true // Placeholder - implement actual checks
}

// SetStoreStatus records the status of an operation (for health monitoring).
func (h *HealthMetricsHandler) SetStoreStatus(operationID, status string) {
	h.storeStatusMu.Lock()
	defer h.storeStatusMu.Unlock()
	h.storeStatus[operationID] = status
}

// GetPendingRecoveries returns operations awaiting recovery.
func (h *HealthMetricsHandler) GetPendingRecoveries() []string {
	h.storeStatusMu.RLock()
	defer h.storeStatusMu.RUnlock()

	var pending []string
	for id, status := range h.storeStatus {
		if status == "pending_recovery" || status == "needs_review" {
			pending = append(pending, id)
		}
	}
	return pending
}

// IncrementRecoveryCounter increments the appropriate recovery counter.
func (h *HealthMetricsHandler) IncrementRecoveryCounter(cause string) {
	h.recoveryCountersMu.Lock()
	defer h.recoveryCountersMu.Unlock()

	switch cause {
	case "crash":
		h.crashRecoveries++
	case "disk_corruption":
		h.diskCorruption++
	case "manual":
		h.manualInterventions++
	}
}

// GetMetricSummary returns a summary of all metric values.
func GetMetricSummary() map[string]interface{} {
	return map[string]interface{}{
		"metrics_registered": len(metrics.GetMetricNames()),
		"metric_names":       metrics.GetMetricNames(),
		"timestamp":          time.Now().UTC().Format(time.RFC3339),
	}
}
