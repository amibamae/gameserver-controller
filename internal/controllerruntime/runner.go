package controllerruntime

import (
	"context"
	"sync"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// runnerWorkers bounds how many operations run at once.
const runnerWorkers = 4

// runnerScanInterval bounds how frequently the durable store is rescanned.
// The workflow engine retains responsibility for safe retries and reconciliation.
const runnerScanInterval = 5 * time.Second

// runner executes accepted operations in the background. Queue entries are
// only scheduling hints; the durable store remains authoritative. An operation
// stays reserved until its worker returns, including while it is running.
type runner struct {
	run    func(context.Context, string)
	queue  chan string
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	queued map[string]bool
}

// StartRunner starts workers and rediscovers recoverable work from the durable
// store, including work whose original scheduling hint could not be queued.
// Build has already performed startup recovery before this method is called.
func (r *Runtime) StartRunner(ctx context.Context, log func(string, map[string]any)) {
	if r.runner != nil {
		return
	}
	if log == nil {
		log = func(string, map[string]any) {}
	}
	ctx, cancel := context.WithCancel(ctx)
	run := func(ctx context.Context, operationID string) {
		finished, err := r.Engine.Run(ctx, operationID)
		fields := map[string]any{"operation_id": operationID, "status": string(finished.Record.Status)}
		if err != nil && ctx.Err() == nil {
			fields["error"] = err.Error()
		}
		log("operation run finished", fields)
	}
	r.runner = newRunner(ctx, cancel, run)
	r.runner.startScan(ctx, r.Store.ListRecoverableOperations, log)
}

func newRunner(ctx context.Context, cancel context.CancelFunc, run func(context.Context, string)) *runner {
	w := &runner{run: run, queue: make(chan string, 1024), cancel: cancel, queued: map[string]bool{}}
	for range runnerWorkers {
		w.wg.Add(1)
		go w.work(ctx)
	}
	return w
}

func (w *runner) work(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case operationID := <-w.queue:
			if ctx.Err() == nil {
				w.run(ctx, operationID)
			}
			w.release(operationID)
		}
	}
}

func (w *runner) reserve(operationID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.queued[operationID] {
		return false
	}
	w.queued[operationID] = true
	return true
}

func (w *runner) release(operationID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.queued, operationID)
}

// submit is a non-blocking hint from acceptance. If the queue is full, the
// durable scan will rediscover the operation without requiring a restart.
func (w *runner) submit(operationID string) {
	if !w.reserve(operationID) {
		return
	}
	select {
	case w.queue <- operationID:
	default:
		w.release(operationID)
	}
}

func (w *runner) startScan(ctx context.Context, list func() ([]store.StoredOperation, error), log func(string, map[string]any)) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for ctx.Err() == nil {
			if err := w.scan(ctx, list); err != nil && ctx.Err() == nil {
				log("durable operation scan failed", map[string]any{"error": err.Error()})
			}
			if !waitForScan(ctx) {
				return
			}
		}
	}()
}

func waitForScan(ctx context.Context) bool {
	timer := time.NewTimer(runnerScanInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// scan preserves the store's recovery ordering. Unlike an acceptance hint,
// the scan waits for queue capacity, so an older full queue cannot permanently
// starve later operations. A snapshot may be stale by dispatch time; Engine
// re-reads durable state and enforces all preconditions.
func (w *runner) scan(ctx context.Context, list func() ([]store.StoredOperation, error)) error {
	operations, err := list()
	if err != nil {
		return err
	}
	for _, operation := range operations {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !automaticallyRunnable(operation.Record.Status) {
			continue
		}
		if !w.reserve(operation.Record.OperationID) {
			continue
		}
		if err := w.enqueueScan(ctx, operation.Record.OperationID); err != nil {
			return err
		}
	}
	return nil
}

func (w *runner) enqueueScan(ctx context.Context, operationID string) error {
	select {
	case w.queue <- operationID:
		return nil
	case <-ctx.Done():
		w.release(operationID)
		return ctx.Err()
	}
}

func automaticallyRunnable(status protocolstate.OperationStatus) bool {
	switch status {
	case protocolstate.OperationPending, protocolstate.OperationRunning,
		protocolstate.OperationWaiting, protocolstate.OperationReconciling:
		return true
	default:
		// Operator intervention and cancellation must not be bypassed.
		return false
	}
}

func (w *runner) stop() {
	w.cancel()
	w.wg.Wait()
}
