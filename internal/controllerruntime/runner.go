package controllerruntime

import (
	"context"
	"sync"
)

// runnerWorkers bounds how many operations run at once.
const runnerWorkers = 4

// runner executes accepted operations in the background with the workflow
// engine. An operation is run at most once at a time; the engine itself
// refuses concurrent runs of the same operation.
type runner struct {
	run    func(context.Context, string)
	queue  chan string
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	queued map[string]bool
}

// StartRunner starts the background workers and queues the unfinished
// operations found at startup. Log receives one line per finished run.
// Close stops it.
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
	for _, operationID := range r.recovered {
		r.runner.submit(operationID)
	}
}

func newRunner(ctx context.Context, cancel context.CancelFunc, run func(context.Context, string)) *runner {
	w := &runner{run: run, queue: make(chan string, 1024), cancel: cancel, queued: map[string]bool{}}
	for range runnerWorkers {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case operationID := <-w.queue:
					w.mu.Lock()
					delete(w.queued, operationID)
					w.mu.Unlock()
					w.run(ctx, operationID)
				}
			}
		}()
	}
	return w
}

// submit queues an operation unless it is already queued. A full queue drops
// the request; the operation stays durable and is resumed at the next start.
func (w *runner) submit(operationID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.queued[operationID] {
		return
	}
	select {
	case w.queue <- operationID:
		w.queued[operationID] = true
	default:
	}
}

func (w *runner) stop() {
	w.cancel()
	w.wg.Wait()
}
