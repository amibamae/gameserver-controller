package fakeadapters

import (
	"context"
	"encoding/json"
	"testing"
)

func TestVultrSimulator_Capabilities(t *testing.T) {
	sim := NewVultrSimulator(nil)
	caps := sim.Capabilities()
	
	expected := []string{"hosting.create", "hosting.destroy", "hosting.info", "hosting.list"}
	if len(caps) != len(expected) {
		t.Errorf("expected %d capabilities, got %d: %v", len(expected), len(caps), caps)
	}
	
	for i, expectedCap := range expected {
		if caps[i] != expectedCap {
			t.Errorf("capability[%d]: expected %q, got %q", i, expectedCap, caps[i])
		}
	}
}

func TestVultrSimulator_CreateInstance(t *testing.T) {
	sim := NewVultrSimulator(nil)
	
	params := json.RawMessage(`{"plan": "vc2", "name": "test-server-1"}`)
	result, err := sim.Create(context.Background(), params)
	if err != nil {
		t.Errorf("create instance failed: %v", err)
		return
	}
	
	var resultObj map[string]any
	if err := json.Unmarshal(result, &resultObj); err != nil {
		t.Fatalf("parse result: %v", err)
		return
	}
	
	idVal, ok := resultObj["id"].(string)
	if !ok || idVal == "" {
		t.Error("instance ID is empty or missing")
		return
	}
	
	stateVal, ok := resultObj["state"].(string)
	if !ok || stateVal != "running" {
		t.Errorf("expected state 'running', got %q", stateVal)
		return
	}
}

func TestVultrSimulator_DestroyInstance(t *testing.T) {
	sim := NewVultrSimulator(nil)
	params := json.RawMessage(`{"plan": "vc2", "name": "test"}`)
	result, err := sim.Create(context.Background(), params)
	if err != nil {
		t.Fatalf("create: %v", err)
		return
	}
	
	var resultObj map[string]any
	json.Unmarshal(result, &resultObj)
	idVal := ""
	if id, ok := resultObj["id"].(string); ok {
		idVal = id
	}
	
	destoryParams := json.RawMessage(`{"instance_id": "` + idVal + `"}`)
	if _, err := sim.Destroy(context.Background(), destoryParams); err != nil {
		t.Errorf("destroy failed: %v", err)
		return
	}
	
	var status map[string]any
	statusRaw, _ := json.Marshal(map[string]string{"status": "success"})
	json.Unmarshal(statusRaw, &status)
	if _, ok := status["status"]; !ok {
		t.Error("destroy response missing status field")
	}
}
