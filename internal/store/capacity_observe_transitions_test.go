package store

import (
	"testing"
	"time"
)

func TestCapacityRefactorObserveCapacityTransitionsCharacterization(t *testing.T) {
	t.Run("identity and ordering conflicts", checkCapacityObserveIdentityConflicts)
	t.Run("present then replay then ordering", checkCapacityObserveReplayOrdering)
	t.Run("destroyed then absent is a transition error", checkCapacityObserveDestroyedTransition)
	t.Run("observation from create outcome unknown", checkCapacityObserveUnknownCreate)
}

func checkCapacityObserveIdentityConflicts(t *testing.T) {
	t.Helper()
	repository := openTestStore(t)
	reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
	stored := capacityRefactorRawGet(t, repository, "op-a")
	at := reserved.CreatedAt.Add(time.Second)
	wrongProvider := capacityObservation(reserved, "present", "vm", at)
	wrongProvider.Provider = "other"
	wrongKey := capacityObservation(reserved, "present", "vm", at)
	wrongKey.ResourceKey = "other"
	early := capacityObservation(reserved, "present", "vm", reserved.CreatedAt.Add(-time.Nanosecond))
	for _, observation := range []CapacityProviderObservation{wrongProvider, wrongKey, early} {
		result, err := repository.ObserveCapacity("op-a", observation)
		capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
		if result.OperationID != "" {
			t.Fatalf("result = %+v", result)
		}
	}
	earlyProvision := capacityObservation(reserved, "present", "vm", at)
	earlyProvision.ProvisionedAt = reserved.CreatedAt.Add(-time.Nanosecond)
	_, err := repository.ObserveCapacity("op-a", earlyProvision)
	capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": provider provisioning time precedes reservation")
	capacityRefactorAssertStored(t, repository, "op-a", stored)
}

func checkCapacityObserveReplayOrdering(t *testing.T) {
	t.Helper()
	repository := openTestStore(t)
	reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
	at := reserved.CreatedAt.Add(time.Second)
	present := capacityObservation(reserved, "present", "vm-a", at)
	present.ProvisionedAt = reserved.CreatedAt
	active, err := repository.ObserveCapacity("op-a", present)
	if err != nil {
		t.Fatal(err)
	}
	assertObservedCapacityActive(t, active, present)
	stored := capacityRefactorRawGet(t, repository, "op-a")
	if string(stored) != capacityRefactorJSON(t, active) {
		t.Fatalf("stored %s, returned %s", stored, capacityRefactorJSON(t, active))
	}
	replay, err := repository.ObserveCapacity("op-a", present)
	if err != nil || capacityRefactorJSON(t, replay) != string(stored) {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	capacityRefactorAssertStored(t, repository, "op-a", stored)
	checkCapacityObserveRejectedChanges(t, repository, reserved, present, stored)
	checkCapacityObserveLaterStates(t, repository, reserved, present)

}

func checkCapacityObserveRejectedChanges(t *testing.T, repository *BoltStore, reserved CapacityReservation, present CapacityProviderObservation, stored []byte) {
	t.Helper()
	at := present.ObservedAt
	sameTime := present
	sameTime.State = "absent"
	sameTime.ProvisionedAt = time.Time{}
	_, err := repository.ObserveCapacity("op-a", sameTime)
	capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
	older := capacityObservation(reserved, "absent", "", at.Add(-time.Nanosecond))
	_, err = repository.ObserveCapacity("op-a", older)
	capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
	otherInstance := capacityObservation(reserved, "present", "vm-b", at.Add(time.Second))
	_, err = repository.ObserveCapacity("op-a", otherInstance)
	capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
	capacityRefactorAssertStored(t, repository, "op-a", stored)

}

func assertObservedCapacityActive(t *testing.T, active CapacityReservation, present CapacityProviderObservation) {
	t.Helper()
	if active.State != CapacityActive || active.ProviderInstanceID != "vm-a" || !active.ProvisionedAt.Equal(present.ProvisionedAt) {
		t.Fatalf("active = %+v", active)
	}
	if active.LastObservation == nil || capacityRefactorJSON(t, *active.LastObservation) != capacityRefactorJSON(t, present) || active.UpdatedAt.Before(present.ObservedAt) {
		t.Fatalf("active = %+v", active)
	}
}

func checkCapacityObserveLaterStates(t *testing.T, repository *BoltStore, reserved CapacityReservation, present CapacityProviderObservation) {
	t.Helper()
	at := present.ObservedAt
	// A later present keeps the first provisioning time.
	later := capacityObservation(reserved, "present", "vm-a", at.Add(2*time.Second))
	later.ProvisionedAt = at
	next, err := repository.ObserveCapacity("op-a", later)
	if err != nil || !next.ProvisionedAt.Equal(reserved.CreatedAt) || next.LastObservation.ProvisionedAt != at {
		t.Fatalf("later present = %+v, %v", next, err)
	}
	checkCapacityObserveAbsentStates(t, repository, reserved, at)
}

func checkCapacityObserveAbsentStates(t *testing.T, repository *BoltStore, reserved CapacityReservation, at time.Time) {
	t.Helper()
	// Absent without an instance ID keeps the reservation's instance ID.
	absent, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "absent", "", at.Add(3*time.Second)))
	if err != nil || absent.State != CapacityAbsent || absent.ProviderInstanceID != "vm-a" || absent.LastObservation.ProviderInstanceID != "" {
		t.Fatalf("absent = %+v, %v", absent, err)
	}
	absentAgain, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "absent", "vm-a", at.Add(4*time.Second)))
	if err != nil || absentAgain.State != CapacityAbsent {
		t.Fatalf("absent again = %+v, %v", absentAgain, err)
	}
	releasedStored := capacityRefactorRawGet(t, repository, "op-a")
	_, err = repository.ObserveCapacity("op-a", capacityObservation(reserved, "destroyed", "", at.Add(5*time.Second)))
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	capacityRefactorAssertStored(t, repository, "op-a", releasedStored)
	// A released reservation may be seen present again.
	again, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "present", "vm-a", at.Add(6*time.Second)))
	if err != nil || again.State != CapacityActive {
		t.Fatalf("present after absent = %+v, %v", again, err)
	}
}

func checkCapacityObserveDestroyedTransition(t *testing.T) {
	t.Helper()
	repository := openTestStore(t)
	active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
	at := active.LastObservation.ObservedAt
	if _, err := repository.ObserveCapacity("op-a", capacityObservation(active, "destroyed", "vm-a", at.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	_, err := repository.ObserveCapacity("op-a", capacityObservation(active, "absent", "vm-a", at.Add(2*time.Second)))
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	// Instance mismatch is checked before the released-state rule.
	_, err = repository.ObserveCapacity("op-a", capacityObservation(active, "absent", "vm-b", at.Add(2*time.Second)))
	capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
}

func checkCapacityObserveUnknownCreate(t *testing.T) {
	t.Helper()
	repository := openTestStore(t)
	reserved := reserve(t, repository, capacityRequest("op-a", "srv", "staging", "g"))
	if _, err := repository.MarkCapacityCreateSent("op-a"); err != nil {
		t.Fatal(err)
	}
	absent, err := repository.ObserveCapacity("op-a", capacityObservation(reserved, "absent", "", reserved.CreatedAt.Add(time.Second)))
	if err != nil || absent.State != CapacityAbsent || absent.ProviderInstanceID != "" {
		t.Fatalf("absent = %+v, %v", absent, err)
	}
}
