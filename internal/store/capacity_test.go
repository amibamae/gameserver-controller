package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReserveCapacityIsAtomicAcrossConcurrentCreates(t *testing.T) {
	store := openTestStore(t)
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, operationID := range []string{"create-a", "create-b"} {
		wait.Add(1)
		go func(operationID string) {
			defer wait.Done()
			_, _, err := store.ReserveCapacity(capacityRequest(operationID, "server-"+operationID, "staging", "staging"), 1, 1)
			results <- err
		}(operationID)
	}
	wait.Wait()
	close(results)

	created, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrCapacityReached):
			rejected++
		default:
			t.Fatalf("unexpected reservation result: %v", err)
		}
	}
	if created != 1 || rejected != 1 {
		t.Fatalf("got %d accepted and %d rejected reservations; want one of each", created, rejected)
	}
}

func TestCapacityCreateDispatchClaimIsOneShotUnderConcurrency(t *testing.T) {
	store := openTestStore(t)
	reservation, _, err := store.ReserveCapacity(capacityRequest("op-claim", "server-claim", "staging", "shared_nonproduction"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	const callers = 12
	start := make(chan struct{})
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, claimErr := store.MarkCapacityCreateSent(reservation.OperationID)
			results <- claimErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	succeeded, rejected := 0, 0
	for claimErr := range results {
		switch {
		case claimErr == nil:
			succeeded++
		case errors.Is(claimErr, ErrCapacityTransition):
			rejected++
		default:
			t.Fatalf("unexpected dispatch claim error: %v", claimErr)
		}
	}
	if succeeded != 1 || rejected != callers-1 {
		t.Fatalf("dispatch claims succeeded=%d rejected=%d; want 1 and %d", succeeded, rejected, callers-1)
	}
}

func TestCapacityDestroyDispatchClaimIsOneShotUnderConcurrency(t *testing.T) {
	store := openTestStore(t)
	reservation, _, err := store.ReserveCapacity(capacityRequest("op-destroy-claim", "server-destroy-claim", "staging", "shared_nonproduction"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err = store.ObserveCapacity(reservation.OperationID, capacityObservation(reservation, "present", "instance-claim", time.Now().UTC().Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	const callers = 12
	start := make(chan struct{})
	results := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, claimErr := store.MarkCapacityDestroySent(reservation.OperationID)
			results <- claimErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	succeeded, rejected := 0, 0
	for claimErr := range results {
		switch {
		case claimErr == nil:
			succeeded++
		case errors.Is(claimErr, ErrCapacityTransition):
			rejected++
		default:
			t.Fatalf("unexpected destroy dispatch claim error: %v", claimErr)
		}
	}
	if succeeded != 1 || rejected != callers-1 {
		t.Fatalf("destroy dispatch claims succeeded=%d rejected=%d; want 1 and %d", succeeded, rejected, callers-1)
	}
}

func TestReserveCapacityIsIdempotentAndRejectsOccupiedSlot(t *testing.T) {
	store := openTestStore(t)
	request := capacityRequest("op-1", "server-1", "staging", "shared_nonproduction")
	first, created, err := store.ReserveCapacity(request, 1, 1)
	if err != nil || !created || first.State != CapacityReserved {
		t.Fatalf("first reservation = (%+v, %t, %v)", first, created, err)
	}
	second, created, err := store.ReserveCapacity(request, 1, 1)
	if err != nil || created || second.OperationID != first.OperationID || second.State != first.State {
		t.Fatalf("idempotent reservation = (%+v, %t, %v)", second, created, err)
	}
	conflict := request
	conflict.ResourceKey = "other-resource"
	if _, _, err := store.ReserveCapacity(conflict, 1, 1); !errors.Is(err, ErrCapacityConflict) {
		t.Fatalf("same operation with changed identity: got %v, want conflict", err)
	}
	otherOperation := capacityRequest("op-2", "server-1", "staging", "shared_nonproduction")
	if _, _, err := store.ReserveCapacity(otherOperation, 1, 1); !errors.Is(err, ErrCapacityOccupied) {
		t.Fatalf("occupied logical server slot: got %v, want ErrCapacityOccupied", err)
	}
}

func TestUnresolvedCreateAndDestroyKeepCapacityUntilProviderObservation(t *testing.T) {
	store := openTestStore(t)
	reservation, _, err := store.ReserveCapacity(capacityRequest("op-create", "server-1", "staging", "shared_nonproduction"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkCapacityCreateSent(reservation.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkCapacityCreateSent(reservation.OperationID); !errors.Is(err, ErrCapacityTransition) {
		t.Fatalf("duplicate create dispatch claim: got %v, want ErrCapacityTransition", err)
	}
	assertCapacityBlocked(t, store, "op-blocked-1", "server-2", "testing", "shared_nonproduction", 1, 1)

	reservation, err = store.ObserveCapacity(reservation.OperationID, capacityObservation(reservation, "absent", "", time.Now().UTC().Add(time.Second)))
	if err != nil || reservation.State != CapacityAbsent {
		t.Fatalf("confirmed absent observation = (%+v, %v)", reservation, err)
	}
	reservation, created, err := store.ReserveCapacity(capacityRequest("op-adopt", "server-2", "testing", "shared_nonproduction"), 1, 1)
	if err != nil || !created {
		t.Fatalf("reservation after confirmed absence = (%+v, %t, %v)", reservation, created, err)
	}

	presentAt := time.Now().UTC().Add(2 * time.Second)
	reservation, err = store.ObserveCapacity(reservation.OperationID, capacityObservation(reservation, "present", "instance-42", presentAt))
	if err != nil || reservation.State != CapacityActive {
		t.Fatalf("present observation = (%+v, %v)", reservation, err)
	}
	reservation, err = store.MarkCapacityDestroySent(reservation.OperationID)
	if err != nil || reservation.State != CapacityDestroyOutcomeUnknown {
		t.Fatalf("destroy request state = (%+v, %v)", reservation, err)
	}
	if _, err := store.MarkCapacityDestroySent(reservation.OperationID); !errors.Is(err, ErrCapacityTransition) {
		t.Fatalf("duplicate destroy dispatch claim: got %v, want ErrCapacityTransition", err)
	}
	assertCapacityBlocked(t, store, "op-blocked-2", "server-3", "staging", "shared_nonproduction", 1, 1)

	reservation, err = store.ObserveCapacity(reservation.OperationID, capacityObservation(reservation, "destroyed", "instance-42", presentAt.Add(time.Second)))
	if err != nil || reservation.State != CapacityDestroyed {
		t.Fatalf("confirmed destruction observation = (%+v, %v)", reservation, err)
	}
	if _, _, err := store.ReserveCapacity(capacityRequest("op-after-destroy", "server-3", "staging", "shared_nonproduction"), 1, 1); err != nil {
		t.Fatalf("reservation after confirmed destruction: %v", err)
	}
}

func TestCapacityReservationPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.db")
	store, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.ReserveCapacity(capacityRequest("op-persist", "server-1", "production", "production"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkCapacityCreateSent(created.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.GetCapacityReservation("op-persist")
	if err != nil || loaded.State != CapacityCreateOutcomeUnknown {
		t.Fatalf("reopened reservation = (%+v, %v)", loaded, err)
	}
	assertCapacityBlocked(t, reopened, "op-after-restart", "server-2", "testing", "shared_nonproduction", 1, 1)
}

func TestCapacityGroupLimitIsEnforcedAlongsideTotal(t *testing.T) {
	store := openTestStore(t)
	if _, _, err := store.ReserveCapacity(capacityRequest("prod-1", "prod-a", "production", "production"), 2, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ReserveCapacity(capacityRequest("prod-2", "prod-b", "production", "production"), 2, 1); !errors.Is(err, ErrCapacityReached) {
		t.Fatalf("second production reservation: got %v, want ErrCapacityReached", err)
	}
	if _, _, err := store.ReserveCapacity(capacityRequest("test-1", "test-a", "testing", "shared_nonproduction"), 2, 1); err != nil {
		t.Fatalf("shared nonproduction slot should be independent: %v", err)
	}
	if _, _, err := store.ReserveCapacity(capacityRequest("stage-1", "stage-a", "staging", "shared_nonproduction"), 2, 1); !errors.Is(err, ErrCapacityReached) {
		t.Fatalf("staging should share the nonproduction group limit: got %v", err)
	}
}

func TestObserveCapacityRequiresMatchingFreshProviderEvidence(t *testing.T) {
	store := openTestStore(t)
	reservation, _, err := store.ReserveCapacity(capacityRequest("op-evidence", "server-1", "staging", "shared_nonproduction"), 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	observation := capacityObservation(reservation, "present", "instance-1", time.Now().UTC().Add(time.Second))
	wrongProvider := observation
	wrongProvider.Provider = "other-provider"
	if _, err := store.ObserveCapacity(reservation.OperationID, wrongProvider); !errors.Is(err, ErrCapacityConflict) {
		t.Fatalf("wrong provider: got %v, want conflict", err)
	}
	if _, err := store.ObserveCapacity(reservation.OperationID, observation); err != nil {
		t.Fatal(err)
	}
	stale := capacityObservation(reservation, "absent", "instance-1", observation.ObservedAt.Add(-time.Second))
	if _, err := store.ObserveCapacity(reservation.OperationID, stale); !errors.Is(err, ErrCapacityConflict) {
		t.Fatalf("stale absence: got %v, want conflict", err)
	}
	wrongInstance := capacityObservation(reservation, "absent", "different-instance", observation.ObservedAt.Add(time.Second))
	if _, err := store.ObserveCapacity(reservation.OperationID, wrongInstance); !errors.Is(err, ErrCapacityConflict) {
		t.Fatalf("wrong instance: got %v, want conflict", err)
	}
	duplicate := observation
	duplicate.ObservedAt = observation.ObservedAt.In(time.FixedZone("equivalent UTC", 0))
	if loaded, err := store.ObserveCapacity(reservation.OperationID, duplicate); err != nil || loaded.State != CapacityActive {
		t.Fatalf("replayed equivalent observation = (%+v, %v)", loaded, err)
	}
}

func capacityRequest(operationID, serverID, environmentID, group string) CapacityReservation {
	return CapacityReservation{
		OperationID: operationID, ServerID: serverID, EnvironmentID: environmentID,
		CapacityGroup: group, Provider: "vultr", ResourceKey: "gsc-" + serverID,
	}
}

func capacityObservation(reservation CapacityReservation, state, instanceID string, observedAt time.Time) CapacityProviderObservation {
	return CapacityProviderObservation{
		Provider: reservation.Provider, ResourceKey: reservation.ResourceKey,
		ProviderInstanceID: instanceID, State: state, ObservedAt: observedAt,
	}
}

func assertCapacityBlocked(t *testing.T, store *BoltStore, operationID, serverID, environmentID, group string, totalLimit, groupLimit int) {
	t.Helper()
	if _, _, err := store.ReserveCapacity(capacityRequest(operationID, serverID, environmentID, group), totalLimit, groupLimit); !errors.Is(err, ErrCapacityReached) {
		t.Fatalf("reservation while unresolved state occupies capacity: got %v, want ErrCapacityReached", err)
	}
}
