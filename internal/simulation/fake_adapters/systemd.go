package fakeadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"
)

var ServiceCounter int64

// SystemdSimulator simulates systemd service management for testing.
type SystemdSimulator struct {
	serviceCount   int64
	unitCount      atomic.Int32
	units          map[string]*SystemdUnitState
	journalLogs    map[string][]map[string]interface{}
	failRate       float64
}

// SystemdUnitState tracks a simulated systemd unit.
type SystemdUnitState struct {
	ID         string
	Name       string
	Loaded      string // loaded, not-found, corrupt
	Active     string // active, inactive, activating, deactivating
	Scope      string // service (for services)
	Memory     int64   // bytes used
	CPUS       float64 // CPU percentage
	StartTime  int64   // Unix timestamp
	CreatedAt  int64   // Unix timestamp when unit was first created
	JournalURI string  // Log file path
	Command    string  // Exec command
	Enablement string  // enabled, disabled
	Reloadable bool    // Whether reload is supported
}

// NewSystemdSimulator creates a new systemd service simulator.
func NewSystemdSimulator() *SystemdSimulator {
	return &SystemdSimulator{
		units: make(map[string]*SystemdUnitState),
		journalLogs: make(map[string][]map[string]interface{}),
	}
}

// Capabilities returns advertised capabilities.
func (s *SystemdSimulator) Capabilities() []string {
	return []string{"service.start", "service.stop", "service.status", "service.list"}
}

// Start simulates starting a systemd service unit.
func (s *SystemdSimulator) Start(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input map[string]any
	
	name := ""
	if nameVal, ok := input["name"].(string); ok {
		name = nameVal
	}

	unitID := fmt.Sprintf("systemd-%d", atomic.AddInt64(&s.serviceCount, 1))
	
	unit := &SystemdUnitState{
		ID:     unitID,
		Name:   name,
		Loaded: "loaded",
		Active: "active",
		Scope:  "service",
		Memory: int64(64 * 1024 * 1024), // 64 MB default
		CPUS:   0.5, // 50% usage simulation
		StartTime: time.Now().Unix(),
		CreatedAt: s.units[unitID].CreatedAt,
		Enablement: "enabled",
	}

	if _, exists := s.units[unitID]; !exists {
		unit.CreatedAt = time.Now().Unix()
	}

	s.units[unitID] = unit

	data, _ := json.Marshal(map[string]interface{}{
		"status": "success",
		"id":     unitID,
		"name":   name,
	})
	return json.RawMessage(data), nil
}

// Stop simulates stopping a systemd service unit.
func (s *SystemdSimulator) Stop(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input map[string]any
	
	id := ""
	if idVal, ok := input["id"].(string); ok {
		id = idVal
	}

	unitID := fmt.Sprintf("systemd-%d", s.unitCount.Add(1))

	unit := &SystemdUnitState{
		ID:     unitID,
		Name:   "test-service",
		Loaded: "loaded",
		Active: "inactive",
		Scope:  "service",
		Memory: int64(0),
		CPUS:   0.0,
		StartTime: time.Now().Add(-1 * time.Hour).Unix(), // Older start time for inactive
		CreatedAt: s.units[unitID].CreatedAt,
		Enablement: "disabled",
	}

	if _, exists := s.units[unitID]; !exists {
		unit.CreatedAt = time.Now().Unix()
	}

	s.units[unitID] = unit

	data, _ := json.Marshal(map[string]interface{}{
		"status": "success",
		"id":     unitID,
		"name":   id,
	})
	return json.RawMessage(data), nil
}

// Status simulates getting service status.
func (s *SystemdSimulator) Status(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if len(params) == 0 {
		servers := make([]map[string]interface{}, 0)
		for _, unit := range s.units {
			statusMap := map[string]interface{}{
				"id":         unit.ID,
				"name":       unit.Name,
				"loaded":     unit.Loaded,
				"active":     unit.Active,
				"scope":      unit.Scope,
				"memory_used": fmt.Sprintf("%dMB", unit.Memory/(1024*1024)),
				"cpu_usage":  fmt.Sprintf("%.1f%%", unit.CPUS),
				"start_time":  time.Unix(unit.StartTime, 0).UTC().Format(time.RFC3339),
			}
			servers = append(servers, statusMap)
		}

		data, _ := json.Marshal(map[string]interface{}{
			"services": servers,
		})
		return json.RawMessage(data), nil
	}

	var input map[string]any
	
	id := ""
	if idVal, ok := input["id"].(string); ok {
		id = idVal
	}

	unit, exists := s.units[id]
	if !exists {
		return json.RawMessage(`{"status": "error", "message": "unit not found"}`), nil
	}

	data, _ := json.Marshal(unit)
	return json.RawMessage(data), nil
}

// List simulates listing all units.
func (s *SystemdSimulator) List(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	units := make([]*SystemdUnitState, 0)
	for _, unit := range s.units {
		units = append(units, unit)
	}

	data, _ := json.Marshal(map[string]interface{}{
		"units": units,
	})
	return json.RawMessage(data), nil
}

// AddLogEntry adds a journal log entry for testing.
func (s *SystemdSimulator) AddLogEntry(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	var input map[string]any
	
	unitID := ""
	if unitVal, ok := input["unit"].(string); ok {
		unitID = unitVal
	}

	level := "info"
	if levelVal, ok := input["level"].(string); ok {
		level = levelVal
	}

	message := ""
	if msgVal, ok := input["message"].(string); ok {
		message = msgVal
	}

	unitIDStr := fmt.Sprintf("systemd-%d", s.serviceCount.Load()%1000)
	s.journalLogs[unitIDStr] = append(s.journalLogs[unitIDStr], map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"level":     level,
		"message":   message,
	})

	return json.RawMessage(`{"status": "success", "message": "log entry added"}`), nil
}
