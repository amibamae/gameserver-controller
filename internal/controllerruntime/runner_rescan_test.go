package controllerruntime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

func runnerRecord(id string, status protocolstate.OperationStatus) store.StoredOperation {
	return store.StoredOperation{Record: store.OperationRecord{OperationID: id, Status: status}}
}

func TestRunnerScanRediscoversDroppedHint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := make(chan struct{}, runnerWorkers)
	gate := make(chan struct{})
	target := make(chan struct{}, 1)
	w := newRunner(ctx, cancel, func(ctx context.Context, id string) {
		if id == "target" {
			target <- struct{}{}
			return
		}
		if id == "worker" || id[:min(len(id), 7)] == "worker-" {
			started <- struct{}{}
			select {
			case <-gate:
			case <-ctx.Done():
			}
		}
	})
	defer w.stop()
	for i := range runnerWorkers {
		w.submit(fmt.Sprintf("worker-%d", i))
	}
	for range runnerWorkers {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("workers did not start")
		}
	}
	for i := range cap(w.queue) {
		w.submit(fmt.Sprintf("queued-%d", i))
	}
	w.submit("target")
	w.mu.Lock()
	reserved := w.queued["target"]
	w.mu.Unlock()
	if reserved {
		t.Fatal("expected full queue to drop only the scheduling hint")
	}
	done := make(chan error, 1)
	go func() {
		done <- w.scan(ctx, func() ([]store.StoredOperation, error) {
			return []store.StoredOperation{runnerRecord("target", protocolstate.OperationPending)}, nil
		})
	}()
	close(gate)
	select {
	case <-target:
	case <-ctx.Done():
		t.Fatal("durably pending operation was not rediscovered")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunnerScanKeepsActiveOperationReserved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	w := newRunner(ctx, cancel, func(ctx context.Context, _ string) {
		started <- struct{}{}
		<-ctx.Done()
	})
	defer w.stop()
	w.submit("active")
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("operation did not start")
	}
	w.submit("active")
	if err := w.scan(ctx, func() ([]store.StoredOperation, error) {
		return []store.StoredOperation{runnerRecord("active", protocolstate.OperationRunning)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	reserved := w.queued["active"]
	queueLength := len(w.queue)
	w.mu.Unlock()
	if !reserved || queueLength != 0 {
		t.Fatal("active operation lost its reservation or was queued again")
	}
	select {
	case <-started:
		t.Fatal("operation dispatched twice")
	default:
	}
}

func TestRunnerScanSelectsOnlyAutomaticWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w := &runner{queue: make(chan string, 8), queued: map[string]bool{}}
	records := []store.StoredOperation{
		runnerRecord("pending", protocolstate.OperationPending),
		runnerRecord("running", protocolstate.OperationRunning),
		runnerRecord("waiting", protocolstate.OperationWaiting),
		runnerRecord("reconciling", protocolstate.OperationReconciling),
		runnerRecord("operator", protocolstate.OperationNeedsOperator),
		runnerRecord("cancel", protocolstate.OperationCancellationRequested),
		runnerRecord("done", protocolstate.OperationSucceeded),
	}
	if err := w.scan(ctx, func() ([]store.StoredOperation, error) { return records, nil }); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"pending", "running", "waiting", "reconciling"} {
		select {
		case got := <-w.queue:
			if got != want {
				t.Fatalf("queued %q, want %q", got, want)
			}
		default:
			t.Fatalf("missing %q", want)
		}
	}
	if len(w.queue) != 0 {
		t.Fatal("operator, cancellation or terminal work was scheduled")
	}
}
