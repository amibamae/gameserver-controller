// Package controllerruntime provides optional monitoring adapter integrations.
package controllerruntime

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

// VictoriaLogsAdapter is an optional log forwarding adapter for VictoriaLogs.
// This adapter is fail-closed - controller continues working if it's unavailable.
type VictoriaLogsAdapter struct {
	serverURL string
	client    *http.Client
	logFormat string
}

var _ Adapter = (*VictoriaLogsAdapter)(nil)

// NewVictoriaLogsAdapter creates a new VictoriaLogs adapter stub for optional integration.
func NewVictoriaLogsAdapter(serverURL string, options ...Option) *VictoriaLogsAdapter {
	a := &VictoriaLogsAdapter{
		serverURL: serverURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		logFormat: "json",
	}

	for _, opt := range options {
		opt(a)
	}

	return a
}

// HealthCheck performs a health check against VictoriaLogs (optional feature).
func (a *VictoriaLogsAdapter) HealthCheck() error {
	req, err := http.NewRequest("GET", a.serverURL+"/healthz", nil)
	if err != nil {
		return err
	}

	resp, err := a.client.Do(req)
	if err != nil {
		// Fail-closed: log the error but don't block controller
		return fmt.Errorf("victorialogs health check failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("victorialogs unhealthy: %s, body=%q", resp.Status, body)
	}

	return nil
}

// SendLogs forwards log entries to VictoriaLogs if available.
func (a *VictoriaLogsAdapter) SendLogs(logs []byte) error {
	if logs == nil || len(logs) == 0 {
		return nil
	}

	// Try sending to VictoriaLogs
	req, err := http.NewRequest("POST", a.serverURL+"/api/v1/query_log", bytes.NewReader(logs))
	if err != nil {
		return fmt.Errorf("victorialogs send request failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-VictoriaLogs-Format", a.logFormat)

	resp, err := a.client.Do(req)
	if err != nil {
		// Fail-closed: log the error but don't block controller
		return fmt.Errorf("victorialogs send failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("victorialogs HTTP error %d: %q", resp.StatusCode, body)
	}

	return nil
}

// Close implements the Adapter interface.
func (a *VictoriaLogsAdapter) Close() error { return nil }

// VictoriaLogsOption is a function for configuring the adapter.
type VictoriaLogsOption func(*VictoriaLogsAdapter)

// WithLogFormat sets the log format for VictoriaLogs.
func WithVictoriaLogsLogFormat(format string) VictoriaLogsOption {
	return func(a *VictoriaLogsAdapter) {
		a.logFormat = format
	}
}
