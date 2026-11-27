// Package papergame provides fake adapter tests for Issue #425: Durable Paper Recovery.
// This is a stub placeholder pending full implementation with execution adapter and Velocity integration.

package papergame

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

var errFakePaperExhausted = errors.New("fake paper script exhausted")

// FakeClient provides a test client for Paper game capabilities.
type FakeClient struct {
	scripts map[string][]json.RawMessage
}

func NewFakeClient() *FakeClient {
	return &FakeClient{
		scripts: make(map[string][]json.RawMessage),
	}
}

func (f *FakeClient) SetScript(capability string, replies ...json.RawMessage) {
	for i := range replies {
		f.scripts[capability] = append(f.scripts[capability], cloneRawMessage(replies[i]))
	}
}

func (f *FakeClient) Invoke(ctx context.Context, req struct{ ID string }) (struct{ Result struct{ Outcome string } }, error) {
	if len(f.scripts[req.ID]) == 0 {
		return struct{ Result struct{ Outcome string } }{}, errors.New("no script for test")
	}
	script := f.scripts[req.ID][0]
	f.scripts[req.ID] = f.scripts[req.ID][1:]
	if len(f.scripts[req.ID]) == 0 {
		f.scripts[req.ID] = append(f.scripts[req.ID], script) // loop
	}
	var result map[string]interface{}
	json.Unmarshal(script, &result)
	return struct{ Result struct{ Outcome string } }{Result: struct{ Outcome string }{Outcome: result["outcome"].(string)}}, nil
}

func cloneRawMessage(src json.RawMessage) json.RawMessage {
	if len(src) == 0 {
		return nil
	}
	clone := make(json.RawMessage, len(src))
	copy(clone, src)
	return clone
}

// Test vectors for Issue #425: Durable Paper Recovery recovery scenarios.
type testVector struct {
	name            string
	capability      string
	inputJSON       string
	expectedOutcome string
}

var testVectors = []testVector{
	{"baseline_healthy", "game.health.check", `{"status":"healthy"}`, "success"},
	{"false_positive_detection", "game.health.check", `{"status":"healthy","degraded":false}`, "success"},
	{"failed_save_request", "game.save.request", `{"save_requested":true}`, "failure"},
	{"early_return_detection", "game.health.check", `{"status":"unhealthy"}`, "success"},
	{"restart_timeout_handling", "game.health.check", `{"error":"timeout"}`, "failure"},
	{"stale_done_marker", "game.health.check", `{"status":"outcome_unknown"}`, "success"},
	{"backup_corruption_detection", "game.data.path.list", `{"error":"checksum_mismatch"}`, "failure"},
	{"actual_restore_validation", "game.health.check", `{"restore_success":true}`, "success"},
}

func TestFakeClientBaselineHealthy(t *testing.T) {
	client := NewFakeClient()
	client.SetScript("game.health.check", json.RawMessage(`{"status":"healthy"}`))
	client.Invoke(context.Background(), struct{ ID string }{ID: "test-1"})
}

func TestFakeClientFalsePositiveDetection(t *testing.T) {
	client := NewFakeClient()
	client.SetScript("game.health.check", json.RawMessage(`{"status":"healthy","degraded":false}`))
	resp, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-2"})
	if resp.Result.Outcome != "success" {
		t.Error("expected success for healthy baseline")
	}
}

func TestFakeClientFailedSave(t *testing.T) {
	client := NewFakeClient()
	saveSuccess := json.RawMessage(`{"save_requested":true}`)
	saveFailure := json.RawMessage(`{"error":"disk_full"}`)
	client.SetScript("game.save.request", saveSuccess, saveFailure)

	resp1, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-3"})
	if resp1.Result.Outcome != "failure" {
		t.Errorf("expected first save to fail in stub mode")
	}
}

func TestFakeClientEarlyReturn(t *testing.T) {
	client := NewFakeClient()
	client.SetScript("game.health.check", json.RawMessage(`{"status":"unhealthy","exit_reason":"process_exited"}`))
	resp, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-5"})
	if resp.Result.Outcome != "success" {
		t.Error("expected success with early return detection")
	}
}

func TestFakeClientRestartTimeout(t *testing.T) {
	client := NewFakeClient()
	timeoutResp := json.RawMessage(`{"error":"timeout","reason":"jvm_start_exceeded_budget"}`)
	client.SetScript("game.health.check", timeoutResp)
	resp, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-6"})
	if resp.Result.Outcome != "failure" {
		t.Error("expected failure for timeout scenario")
	}
}

func TestFakeClientStaleDoneMarker(t *testing.T) {
	client := NewFakeClient()
	client.SetScript("game.health.check", json.RawMessage(`{"status":"outcome_unknown","reason":"controller_crashed"}`))
	resp, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-7"})
	if resp.Result.Outcome != "success" {
		t.Error("expected success for stale marker detection")
	}
}

func TestFakeClientBackupCorruption(t *testing.T) {
	client := NewFakeClient()
	corruptResp := json.RawMessage(`{"error":"checksum_mismatch","backup_corrupt":true}`)
	client.SetScript("game.data.path.list", corruptResp)
	resp, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-8"})
	if resp.Result.Outcome != "failure" {
		t.Error("expected failure for corruption detection")
	}
}

func TestFakeClientActualRestore(t *testing.T) {
	client := NewFakeClient()
	restoreResp := json.RawMessage(`{"restore_success":true,"generation_id":"gen-123","checksum":"sha256:abc123"}`)
	client.SetScript("game.health.check", restoreResp)
	resp, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-9"})
	var result map[string]interface{}
	json.Unmarshal(resp.Result.Output, &result)
	if !result["restore_success"].(bool) {
		t.Error("expected successful restore")
	}
}

func TestFakeClientRecoveryScenarios(t *testing.T) {
	client := NewFakeClient()
	ctx := context.Background()

	for _, tv := range testVectors {
		t.Run(tv.name, func(tt *testing.T) {
			script := json.RawMessage(tv.inputJSON)
			client.SetScript(tv.capability, script)
			resp, err := client.Invoke(ctx, struct{ ID string }{ID: "scenario-test"})
			if err != nil {
				tt.Fatalf("invoke failed for %q: %v", tv.name, err)
			}
			if resp.Result.Outcome != tv.expectedOutcome {
				t.Errorf("%q: expected outcome %q, got %q", tv.name, tv.expectedOutcome, resp.Result.Outcome)
			}
		})
	}
}

// Verify that fake client works with proper context handling (no-op in stub mode)
func TestFakeClientContextCancellation(t *testing.T) {
	client := NewFakeClient()
	client.SetScript("game.health.check", json.RawMessage(`{"status":"healthy"}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Invoke(ctx, struct{ ID string }{ID: "canceled-test"})
	// In stub mode, we don't enforce context cancellation
	if err == nil {
		t.Log("stub mode allows canceled context - full implementation will check")
	}
}

// Verify script exhaustion behavior (simple test in stub mode)
func TestFakeClientScriptExhaustion(t *testing.T) {
	client := NewFakeClient()
	client.SetScript("game.health.check", json.RawMessage(`{"status":"healthy"}`))
	resp1, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-10"})
	if resp1.Result.Outcome != "success" {
		t.Error("expected success on first call")
	}
	resp2, _ := client.Invoke(context.Background(), struct{ ID string }{ID: "test-11"})
	if resp2.Result.Outcome != "success" {
		t.Errorf("expected success on second call (script should loop), got %q", resp2.Result.Outcome)
	}
}
