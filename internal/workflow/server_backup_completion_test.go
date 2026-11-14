package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/amibamae/gameserver-controller/internal/store"
)

func TestBackupCompletionRejectsInvalidOrCancelledRequestsBeforeReadingState(t *testing.T) {
	w := &ServerBackupWorkflow{}
	backup := store.OperationRecord{OperationType: "server.backup"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	const invalid = "server.backup completion validation requires an operation and context"
	if err := w.ValidateCompletion(nil, backup); err == nil || err.Error() != invalid {
		t.Fatalf("nil context error = %v, want %q", err, invalid)
	}
	if err := w.ValidateCompletion(cancelled, store.OperationRecord{}); err == nil || err.Error() != invalid {
		t.Fatalf("wrong operation error = %v, want %q", err, invalid)
	}
	if err := w.ValidateCompletion(cancelled, backup); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v, want context.Canceled", err)
	}
}
