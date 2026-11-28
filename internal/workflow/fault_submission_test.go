package workflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
	"github.com/amibamae/gameserver-controller/internal/store"
)

// resubmission is the record a client retry carries: the same request under
// the same idempotency proof, with the fresh operation ID a retry generates.
func resubmission(accepted store.OperationRecord, operationID string) store.OperationRecord {
	record := accepted
	record.OperationID = operationID
	record.Status = ""
	record.CreatedAt, record.UpdatedAt, record.StartedAt = time.Time{}, time.Time{}, nil
	record.Steps = append([]store.OperationStep(nil), accepted.Steps...)
	return record
}

// changedRequestProof is testProof(value) with the same idempotency key
// digest but a different request binding, as a changed request has.
func changedRequestProof(value byte) store.IdempotencyProof {
	proof := testProof(value)
	proof.Candidates[0].BindingDigest = bytes.Repeat([]byte{value + 99}, 32)
	return proof
}

// runConcurrently runs the operation from every engine in parallel, workers
// times each, and returns the errors the runs reported.
func runConcurrently(engines []*Engine, operationID string, workers int) []error {
	var (
		mu     sync.Mutex
		errs   []error
		wg     sync.WaitGroup
		start  = make(chan struct{})
		report = func(err error) {
			mu.Lock()
			errs = append(errs, err)
			mu.Unlock()
		}
	)
	for _, engine := range engines {
		for range workers {
			wg.Add(1)
			go func(engine *Engine) {
				defer wg.Done()
				<-start
				_, err := engine.Run(context.Background(), operationID)
				report(err)
			}(engine)
		}
	}
	close(start)
	wg.Wait()
	return errs
}

// concurrentRunRefusal reports whether err is one of the refusals a
// concurrent run of the same operation may legitimately get.
func concurrentRunRefusal(err error) bool {
	return err == nil || errors.Is(err, ErrOperationInProgress) || errors.Is(err, store.ErrRevisionConflict)
}

// TestFaultDuplicateSubmissionNeverMutatesTwice submits the same request
// again before, during and after its run, from several controller
// processes at once, and requires one operation and one provider mutation.
func TestFaultDuplicateSubmissionNeverMutatesTwice(t *testing.T) {
	env := newFaultEnv(t, completedReplies(4)...)
	original := env.fixture.operation.Record

	replayed, wasReplayed, err := env.fixture.store.AcceptOperation(resubmission(original, "fault-retry-1"), testProof(1), nil)
	if err != nil || !wasReplayed || replayed.Record.OperationID != env.id {
		t.Fatalf("retry before the run = %q replayed=%v err=%v, want the original %q replayed", replayed.Record.OperationID, wasReplayed, err, env.id)
	}

	engines := []*Engine{env.startController(t, crashPoint{}), env.startController(t, crashPoint{})}
	for _, err := range runConcurrently(engines, replayed.Record.OperationID, 4) {
		if !concurrentRunRefusal(err) {
			t.Errorf("concurrent run error = %v, want nil, ErrOperationInProgress or ErrRevisionConflict", err)
		}
	}
	expectFaultState(t, "after concurrent runs", env.state(t),
		faultState{Operation: protocolstate.OperationSucceeded, Step: protocolstate.StepSucceeded, Attempt: 1, Calls: 1, Effects: 1})

	again, wasReplayed, err := env.fixture.store.AcceptOperation(resubmission(original, "fault-retry-2"), testProof(1), nil)
	if err != nil || !wasReplayed || again.Record.OperationID != env.id || again.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("retry after success = %q/%s replayed=%v err=%v, want the succeeded original", again.Record.OperationID, again.Record.Status, wasReplayed, err)
	}
	if _, err := engines[0].Run(context.Background(), again.Record.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.fixture.store.GetOperation("fault-retry-2"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a replayed retry stored its own operation: err=%v, want ErrNotFound", err)
	}
	_, _, err = env.fixture.store.AcceptOperation(resubmission(original, "fault-retry-3"), changedRequestProof(1), nil)
	if !errors.Is(err, store.ErrIdempotencyConflict) || err.Error() != "idempotency key was reused with different request data" {
		t.Errorf("changed request under the same key: err=%v, want ErrIdempotencyConflict", err)
	}
	expectFaultState(t, "after retries", env.state(t),
		faultState{Operation: protocolstate.OperationSucceeded, Step: protocolstate.StepSucceeded, Attempt: 1, Calls: 1, Effects: 1})
}

// TestFaultConcurrentAcceptanceOfSameRequestCreatesOneOperation races
// identical submissions and requires exactly one new operation.
func TestFaultConcurrentAcceptanceOfSameRequestCreatesOneOperation(t *testing.T) {
	env := newFaultEnv(t, completedReplies(2)...)
	original := env.fixture.operation.Record
	const submitters = 8
	ids := make([]string, submitters)
	replays := make([]bool, submitters)
	errs := make([]error, submitters)
	var wg sync.WaitGroup
	for i := range submitters {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			accepted, replayed, err := env.fixture.store.AcceptOperation(resubmission(original, fmt.Sprintf("fault-race-%d", i)), testProof(1), nil)
			ids[i], replays[i], errs[i] = accepted.Record.OperationID, replayed, err
		}(i)
	}
	wg.Wait()
	for i := range submitters {
		if errs[i] != nil || !replays[i] || ids[i] != env.id {
			t.Errorf("submitter %d = %q replayed=%v err=%v, want the original %q replayed", i, ids[i], replays[i], errs[i], env.id)
		}
	}
}

// TestFaultConflictingMutationsOnOneServerNeverRunTogether accepts
// conflicting operations for one logical server while another holds its
// lock, then races new ones after it finished. Only one may be accepted at
// a time, and only accepted operations reach the provider.
func TestFaultConflictingMutationsOnOneServerNeverRunTogether(t *testing.T) {
	fixture := newLifecycleEngineFixture(t)
	if err := fixture.adapter.SetScript(testCapability, completedReplies(3)...); err != nil {
		t.Fatal(err)
	}
	scope := store.LogicalServerLockScope("staging", "server-1")
	conflicting := func(id string) store.OperationRecord {
		return resubmission(fixture.operation.Record, id)
	}

	_, _, err := fixture.store.AcceptOperation(conflicting("fault-conflict-held"), testProof(20), []string{scope})
	wantHeld := fmt.Sprintf("resource is locked by another operation: scope %q owned by operation %q", scope, fixture.operation.Record.OperationID)
	if !errors.Is(err, store.ErrResourceConflict) || err.Error() != wantHeld {
		t.Fatalf("conflicting acceptance while locked: err=%v, want %q", err, wantHeld)
	}
	if completed, err := fixture.engine.Run(context.Background(), fixture.operation.Record.OperationID); err != nil || completed.Record.Status != protocolstate.OperationSucceeded {
		t.Fatalf("lock holder run = %s, err=%v", completed.Record.Status, err)
	}

	const racers = 6
	accepted := make(chan string, racers)
	conflicts := make(chan error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stored, _, err := fixture.store.AcceptOperation(conflicting(fmt.Sprintf("fault-conflict-%d", i)), testProof(byte(30+i)), []string{scope})
			if err != nil {
				conflicts <- err
				return
			}
			accepted <- stored.Record.OperationID
		}(i)
	}
	wg.Wait()
	close(accepted)
	close(conflicts)
	var winners []string
	for id := range accepted {
		winners = append(winners, id)
	}
	for err := range conflicts {
		if !errors.Is(err, store.ErrResourceConflict) {
			t.Errorf("losing acceptance error = %v, want ErrResourceConflict", err)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("accepted %d of %d conflicting operations (%v), want exactly 1", len(winners), racers, winners)
	}
	lock, err := fixture.store.GetResourceLock(scope)
	if err != nil || lock.OperationID != winners[0] {
		t.Fatalf("server lock = %+v err=%v, want it held by %q", lock, err, winners[0])
	}
	if calls := len(fixture.adapter.Calls()); calls != 1 {
		t.Fatalf("provider calls before the winner runs = %d, want 1 (the finished lock holder only)", calls)
	}
}
