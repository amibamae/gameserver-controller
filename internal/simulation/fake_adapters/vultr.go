package fakeadapters

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

// VultrSimulator simulates Vultr hosting API operations for testing.
type VultrSimulator struct {
	instanceCount int64
	instances      map[string]InstanceState
	failedCalls    atomic.Int32
}

// InstanceState tracks a simulated Vultr instance's lifecycle state.
type InstanceState struct {
	ID          string
	Name        string
	Plan        string
	State       string // running, stopped, provisioning
	IP          string
	DiskSize    int64  // GB
	CreatedAt   int64  // Unix timestamp
	Destroyed   bool
	CPUs        int64
}

// VultrConfig provides configuration for the simulator.
type VultrConfig struct {
	PlanCatalog map[string]InstancePlan
	DefaultRAM  int64
}

// InstancePlan represents a simulated VM instance plan with resources.
type InstancePlan struct {
	RAM     int64 // GB
	CPUs    int64
	Disk    int64 // GB
	Region  string
	VPC     bool
}

// Default config with common Vultr plans.
var defaultPlans = map[string]InstancePlan{
	"vc1-1c-1gb": {RAM: 1, CPUs: 1, Disk: 25, Region: "dc"},     // Cheap testing VM
	"vc2-1c-2gb": {RAM: 2, CPUs: 1, Disk: 50, Region: "dc"},     // Live prod VM
	"vc3-1c-3gb": {RAM: 3, CPUs: 1, Disk: 75, Region: "dc"},
	"vc4-2c-4gb": {RAM: 4, CPUs: 2, Disk: 100, Region: "dc"},
}

// NewVultrSimulator creates a new Vultr hosting simulator.
func NewVultrSimulator(config *VultrConfig) *VultrSimulator {
	return &VultrSimulator{
		instances: make(map[string]InstanceState),
	}
}

// Capabilities returns advertised capabilities.
func (v *VultrSimulator) Capabilities() []string {
	return []string{"hosting.create", "hosting.destroy", "hosting.info", "hosting.list"}
}

// Create simulates creating a Vultr VM instance.
func (v *VultrSimulator) Create(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	if v.failedCalls.Load() > 0 {
		v.failedCalls.Add(-1)
		return nil, fmt.Errorf("simulated failure: instance creation")
	}

	var input map[string]any
	if err := json.Unmarshal(params, &input); err != nil {
		return nil, fmt.Errorf("parse create params: %w", err)
	}

	planID := ""
	if inputPlan, ok := input["plan"].(string); ok {
		planID = inputPlan
	}
	
	var plan InstancePlan
	if len(planID) > 0 {
		if p, exists := defaultPlans[planID]; exists {
			plan = p
		} else {
			plan = defaultPlans["vc2-1c-2gb"]
		}
	} else {
		plan = defaultPlans["vc2-1c-2gb"]
	}

	id := fmt.Sprintf("vultr-%d", atomic.AddInt64(&v.instanceCount, 1))
	
	state := InstanceState{
		ID:       id,
		Name:     stringValue(input, "name"),
		Plan:     planID,
		State:    "running",
		IP:       fmt.Sprintf("10.53.%d.42", int(plan.Disk%256)),
		DiskSize: plan.Disk,
		CPUs:     plan.CPUs,
	}

	data, _ := json.Marshal(state)
	return json.RawMessage(data), nil
}

// Destroy simulates destroying a Vultr instance.
func (v *VultrSimulator) Destroy(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	statusRaw, _ := json.Marshal(map[string]string{
		"status": "success",
	})
	return json.RawMessage(statusRaw), nil
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	raw, ok := m[key]
	if !ok {
		return ""
	}
	s, _ := raw.(string)
	return s
}
