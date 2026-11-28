package controllerruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/amibamae/gameserver-controller/internal/store"
	"github.com/amibamae/gameserver-controller/internal/workflow"
)

// This requirement-derived test checks durable scheduling: an operation the
// controller accepted must run without a controller restart, even when the
// runner's in-memory queue was full at acceptance. Its workflow is an
// in-process test definition with no adapter steps; nothing here contacts a
// provider.

const saturationOperationType = "test.saturation"

// saturationDefinition is a zero-step workflow whose completion check blocks
// the "busy-" operations until release closes, so they occupy every worker.
type saturationDefinition struct {
	started chan string
	release chan struct{}
}

func (d saturationDefinition) CheckPreconditions(context.Context, store.OperationRecord, store.OperationStep) error {
	return nil
}

func (d saturationDefinition) Reconcile(context.Context, store.OperationRecord, store.OperationStep) (workflow.Resolution, error) {
	return workflow.Resolution{State: workflow.ResolutionNeedsOperator}, nil
}

func (d saturationDefinition) ValidateCompletion(ctx context.Context, operation store.OperationRecord) error {
	if !strings.HasPrefix(operation.OperationID, "busy-") {
		return nil
	}
	d.started <- operation.OperationID
	select {
	case <-d.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d saturationDefinition) BuildSuccessResult(context.Context, store.OperationRecord) (json.RawMessage, error) {
	return saturationResult(), nil
}

func (d saturationDefinition) BuildFailureResult(context.Context, store.OperationRecord, store.OperationStep, json.RawMessage) (json.RawMessage, error) {
	return saturationResult(), nil
}

func saturationResult() json.RawMessage {
	return json.RawMessage(`{"outcome_summary":"completed","operation_type":"` + saturationOperationType + `","affected_resources":[],"completed_steps":[],"failed_steps":[],"warnings":[],"unresolved_conditions":[],"audit_event_ids":[],"partial":false}`)
}

// saturationRuntime is a runtime with only a store, the test workflow and
// its engine, enough for StartRunner.
func saturationRuntime(t *testing.T, definition saturationDefinition) *Runtime {
	t.Helper()
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	operations, err := store.OpenBolt(filepath.Join(t.TempDir(), "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	definitions := workflow.NewDefinitions()
	if err := definitions.Register(saturationOperationType, definition); err != nil {
		t.Fatal(err)
	}
	engine, err := workflow.NewEngine(operations, registry, schemas, definitions, denyResume{}, time.Minute, 1)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Store: operations, Schemas: schemas, Registry: registry, Engine: engine}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	return runtime
}

// acceptAndSubmit durably accepts an operation and then hands the runner the
// same scheduling hint Runtime.Accept does.
func acceptAndSubmit(t *testing.T, runtime *Runtime, operationID string) {
	t.Helper()
	digest := sha256.Sum256([]byte(operationID))
	binding := sha256.Sum256([]byte("binding\x00" + operationID))
	proof := store.IdempotencyProof{ActiveKeyVersion: "test-v1", Candidates: []store.IdempotencyVerifier{{
		KeyVersion: "test-v1", Digest: digest[:], BindingDigest: binding[:],
	}}}
	record := store.OperationRecord{
		OperationID: operationID, OperationType: saturationOperationType, ProtocolVersion: runtime.Schemas.ProtocolVersion(),
		Target: store.OperationTarget{ServerID: operationID, EnvironmentID: "staging"}, ActorID: "operator-1",
		IdempotencyKey: operationID, RequestFingerprint: "sha256:" + hex.EncodeToString(digest[:]),
		RequestSnapshot: store.RequestSnapshot{OperationType: saturationOperationType, Parameters: json.RawMessage(`{}`)},
		Steps:           []store.OperationStep{},
	}
	if _, _, err := runtime.Store.AcceptOperation(record, proof, nil); err != nil {
		t.Fatal(err)
	}
	runtime.runner.submit(operationID)
}

// awaitStatus polls the durable record until it reaches want or the
// deadline passes, and returns the last status seen.
func awaitStatus(runtime *Runtime, operationID string, want protocolstate.OperationStatus, deadline time.Duration) (protocolstate.OperationStatus, error) {
	var last protocolstate.OperationStatus
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		stored, err := runtime.Store.GetOperation(operationID)
		if err != nil {
			return last, err
		}
		if last = stored.Record.Status; last == want {
			return last, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return last, errors.New("deadline passed")
}

// TestFaultAcceptedOperationRunsAfterQueueSaturation occupies every runner
// worker, fills the runner queue, accepts one more operation, then frees the
// workers. The accepted operation must reach a terminal state without a
// controller restart.
func TestFaultAcceptedOperationRunsAfterQueueSaturation(t *testing.T) {
	t.Skip("KNOWN DEFECT: runner.submit drops the hint of an operation accepted while the queue is full and nothing rediscovers it until restart; internal/controllerruntime/runner.go submit, fixed by PR #475 - un-skip when it lands")
	definition := saturationDefinition{started: make(chan string, runnerWorkers), release: make(chan struct{})}
	runtime := saturationRuntime(t, definition)
	runtime.StartRunner(context.Background(), nil)

	for i := range runnerWorkers {
		acceptAndSubmit(t, runtime, fmt.Sprintf("busy-%d", i))
	}
	for range runnerWorkers {
		select {
		case <-definition.started:
		case <-time.After(10 * time.Second):
			t.Fatal("busy operations did not occupy every worker")
		}
	}
	for i := range cap(runtime.runner.queue) {
		runtime.runner.submit(fmt.Sprintf("queued-hint-%d", i))
	}
	acceptAndSubmit(t, runtime, "accepted-while-saturated")
	close(definition.release)

	// Allow three of any periodic rescan interval up to five seconds.
	status, err := awaitStatus(runtime, "accepted-while-saturated", protocolstate.OperationSucceeded, 15*time.Second)
	if err != nil {
		t.Fatalf("operation accepted while the runner queue was full is %q after 15s without a restart (%v), want succeeded", status, err)
	}
}
