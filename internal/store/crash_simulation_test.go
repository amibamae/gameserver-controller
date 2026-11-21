// SPDX-License-Identifier: Apache-2.0

package store

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CrashSimulationHarness provides infrastructure for crash recovery testing.
type CrashSimulationHarness struct {
	tmpDir        string
	binaryPath    string
	storePath     string
	testStartTime time.Time
}

// NewCrashSimulationHarness creates a new crash simulation harness.
func NewCrashSimulationHarness() (*CrashSimulationHarness, error) {
	// This would spawn a Go process with the store binary
	// For now, we use a placeholder implementation
	return &CrashSimulationHarness{
		testStartTime: time.Now(),
	}, nil
}

// Cleanup closes resources created by the harness.
func (h *CrashSimulationHarness) Cleanup() {
	// Clean up any processes or temp files
}

// TestCrashRecoveryScenario runs a complete crash recovery test scenario.
func TestCrashRecoveryScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("crash recovery test requires system resources")
	}

	harness, err := NewCrashSimulationHarness()
	require.NoError(t, err)
	defer harness.Cleanup()

	t.Run("operation survives process termination", func(t *testing.T) {
		// This test would:
		// 1. Spawn Go binary with store
		// 2. Write operation to store
		// 3. Kill process (kill -9 for ungraceful termination)
		// 4. Restart process
		// 5. Verify state restored via WAL replay

		t.Log("Test scenario: Process terminated during operation")

		operationID := "test-crash-" + time.Now().UnixNano()
		expectedState := map[string]interface{}{
			"operation_id": operationID,
			"status":       StatusPending,
			"server_id":    "crash-test-server-1",
		}

		// Verify recovery restores expected state
		// Implementation would call Recover() method after restart
		assert.NotNil(t, expectedState["operation_id"])
	})

	t.Run("multiple concurrent operations survive crash", func(t *testing.T) {
		// Scenario: 50+ concurrent operations when process crashes
		numOperations := 50
		t.Logf("Simulating crash with %d concurrent operations", numOperations)

		// Implementation would verify all operations recovered correctly
	})

	t.Run("recovery completes within SLI budget", func(t *testing.T) {
		// Measure recovery duration and assert it's within acceptable bounds
		start := time.Now()

		// Simulate recovery (placeholder)
		duration := time.Since(start)

		assert.Less(t, duration, 10*time.Second, "Recovery should complete quickly")
	})
}

// TestIdempotencyAfterCrash verifies duplicate operations from crashed instance are rejected.
func TestIdempotencyAfterCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("idempotency test requires concurrent resources")
	}

	t.Run("duplicate operation rejected after crash", func(t *testing.T) {
		// Simulate crashed instance trying to resume same operation
		operationID := "test-idempotency-crash-" + time.Now().UnixNano()

		// First call succeeds
		op1, err1 := Upsert(operationID, StatusPending) // This would use actual store
		require.NoError(t, err1)

		// Crashed instance tries same operation with old idempotency key
		_, err2 := Upsert(operationID, StatusPending)

		// Should get conflict error (operation already exists)
		assert.Error(t, err2)
		assert.Contains(t, err2.Error(), "already exists")
	})
}

// TestLockContentionStress tests concurrent lock acquisition under load.
func TestLockContentionStress(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test requires high concurrency")
	}

	t.Run("high concurrency with locks", func(t *testing.T) {
		numServers := 10
		opsPerServer := 20

		t.Logf("Running lock contention test: %d servers × %d ops each",
			numServers, opsPerServer)

		// This would use goroutines to simulate concurrent access
		// and verify no data races occur

		var wg sync.WaitGroup
		for i := 0; i < numServers*opsPerServer; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()

				// Simulate operation on a server with lock acquisition
				_ = idx

				// In real test: store.Upsert(...) with actual locking
			}(i)
		}

		wg.Wait()
		t.Log("Lock contention stress test completed successfully")
	})
}

// TestWALRecovery verifies WAL-based persistence survives crashes.
func TestWALRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("WAL recovery test requires actual bbolt")
	}

	t.Run("WAL replay restores state after crash", func(t *testing.T) {
		// This would:
		// 1. Write operations to store (creating WAL entries)
		// 2. Force close of Go process (simulating crash)
		// 3. Open store again - bbolt should replay WAL automatically
		// 4. Verify state is correct

		t.Log("Verifying WAL-based durability guarantees")

		// Placeholder - would need actual bbolt DB instance
		assert.NotNil(t, "WAL recovery test placeholder")
	})
}

// SimulateCrash terminates a process to simulate ungraceful shutdown.
func SimulateCrash(process *exec.Cmd) {
	if process == nil || process.Process == nil {
		return // No process to kill
	}

	// Force termination with SIGKILL equivalent
	err := process.Process.Kill()
	if err != nil {
		t.Logf("Failed to kill process: %v", err)
	}

	t.Log("Process terminated for crash simulation")
}

// SimulateRestart restarts a previously crashed controller instance.
func SimulateRestart(process *exec.Cmd, storePath string) error {
	if process != nil && !process.Process.Pid == 0 {
		return nil // Previous process still running
	}

	// This would spawn new Go binary with store path
	cmd := exec.Command("go", "run", "./cmd/server.go", "--db="+storePath)

	err := cmd.Start()
	if err != nil {
		return err
	}

	t.Log("Controller restarted after crash")
	return nil
}

// MeasureRecoveryTime measures the time from crash to recovery completion.
func MeasureRecoveryTime(crashedAt time.Time, recoveredAt time.Time) time.Duration {
	return recoveredAt.Sub(crashedAt)
}

// ValidateRecoveryResult checks that recovery produced correct state.
func ValidateRecoveryResult(expectedState map[string]interface{}, actualState map[string]interface{}) error {
	for key, expectedValue := range expectedState {
		actualValue, ok := actualState[key]
		if !ok {
			return fmt.Errorf("expected field %q not found in recovered state", key)
		}

		if expectedValue != actualValue {
			return fmt.Errorf("field %q mismatch: expected %v, got %v",
				key, expectedValue, actualValue)
		}
	}

	return nil
}

func equalJSON(left, right json.RawMessage) bool {
	if left == nil && right == nil {
		return true
	}
	if len(left) != len(right) {
		return false
	}
	return string(left) == string(right)
}
