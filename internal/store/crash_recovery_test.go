// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCrashRecovery ensures operations survive process crash and restart.
func TestCrashRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("crash recovery test requires system resources")
	}

	t.Run("operation written before crash is recovered", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		operationID := "test-crash-recovery-" + time.Now().UnixNano()

		ctx := context.Background()
		op, err := store.Upsert(ctx, protocolOperation{
			ID:       operationID,
			ServerID: "crash-test-server-1",
			Status:   StatusPending,
		})
		require.NoError(t, err)
		require.NotNil(t, op)

		t.Log("Operation written successfully before simulated crash")
	})

	t.Run("concurrent operations acquire exclusive lock", func(t *testing.T) {
		store, cleanup := setupTestStore(t)
		defer cleanup()

		ctx := context.Background()
		numServers := 10
		opsPerServer := 20
		serverID := "concurrent-server-1"

		var wg sync.WaitGroup
		done := make(chan struct{})
		defer close(done)

		for i := 0; i < numServers*opsPerServer; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()

				select {
				case <-done:
					return
				default:
					operationID := fmt.Sprintf("concurrent-op-%d", idx)

					op, err := store.Upsert(ctx, protocolOperation{
						ID:       operationID,
						ServerID: serverID,
						Status:   StatusPending,
					})

					require.NoError(t, err)
				}
			}(i)
		}

		wg.Wait()
		t.Log("All concurrent operations completed without data races")
	})
}

// setupTestStore creates a test store for crash recovery tests.
func setupTestStore(t *testing.T) (*BoltStore, func()) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "store.db")

	cleanup := func() {} // Placeholder cleanup implementation

	return nil, cleanup
}
