package store

import (
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestCapacityRefactorRecordCapacityReadyCharacterization(t *testing.T) {
	t.Run("invalid input", testCapacityReadyInvalidInput)
	t.Run("not found returns zero", testCapacityReadyNotFound)
	t.Run("transition errors return the matched reservation", testCapacityReadyTransitionErrors)
	t.Run("ready before provisioned", testCapacityReadyBeforeProvisioned)
	t.Run("adopted reservation", testCapacityReadyAdoptedReservation)
	t.Run("records once in UTC", testCapacityReadyRecordsOnceInUTC)
	t.Run("future ready floors updated time", testCapacityReadyFutureMeasurement)
	t.Run("duplicate match returns first by key", testCapacityReadyDuplicateMatch)
	t.Run("corrupt entry after match returns match", testCapacityReadyCorruptEntry)
	t.Run("adapter filter only in tx form", testCapacityReadyAdapterFilter)
}

func testCapacityReadyInvalidInput(t *testing.T) {
	repository := openTestStore(t)
	now := time.Now().UTC()
	for name, call := range map[string]struct {
		server, environment, instance string
		measuredAt                    time.Time
	}{
		"server":   {"", "staging", "vm", now},
		"env":      {"srv", strings.Repeat("e", 201), "vm", now},
		"instance": {"srv", "staging", strings.Repeat("i", 241), now},
		"zero":     {"srv", "staging", "vm", time.Time{}},
		"future":   {"srv", "staging", "vm", now.Add(6 * time.Minute)},
	} {
		result, err := repository.RecordCapacityReady(call.server, call.environment, call.instance, call.measuredAt)
		if result.OperationID != "" {
			t.Fatalf("%s result = %+v", name, result)
		}
		capacityRefactorAssertErr(t, err, ErrInvalidRecord, capacityRefactorInvalid+": invalid readiness measurement")
	}
}

func testCapacityReadyNotFound(t *testing.T) {
	repository := openTestStore(t)
	capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
	for _, call := range [][3]string{{"other", "staging", "vm-a"}, {"srv", "prod", "vm-a"}, {"srv", "staging", "vm-b"}} {
		result, err := repository.RecordCapacityReady(call[0], call[1], call[2], time.Now().UTC())
		capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
		if result.OperationID != "" {
			t.Fatalf("not found result = %+v", result)
		}
	}
}

func testCapacityReadyTransitionErrors(t *testing.T) {
	repository := openTestStore(t)
	active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
	stored := capacityRefactorRawGet(t, repository, "op-a")
	result, err := repository.RecordCapacityReady("srv", "staging", "vm-a", active.CreatedAt.Add(-time.Nanosecond))
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	if capacityRefactorJSON(t, result) != string(stored) {
		t.Fatalf("result = %+v", result)
	}
	destroying, err := repository.MarkCapacityDestroySent("op-a")
	if err != nil {
		t.Fatal(err)
	}
	result, err = repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	if capacityRefactorJSON(t, result) != capacityRefactorJSON(t, destroying) {
		t.Fatalf("result = %+v", result)
	}
}

func testCapacityReadyBeforeProvisioned(t *testing.T) {
	repository := openTestStore(t)
	reserved, _, err := repository.ReserveCapacity(capacityRequest("op-a", "srv", "staging", "g"), 9, 9)
	if err != nil {
		t.Fatal(err)
	}
	provisioned := reserved.CreatedAt.Add(time.Second)
	observePresent(t, repository, reserved, "vm-a", provisioned.Add(time.Second), provisioned)
	result, err := repository.RecordCapacityReady("srv", "staging", "vm-a", provisioned.Add(-time.Nanosecond))
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	if result.OperationID != "op-a" || !result.ReadyAt.IsZero() {
		t.Fatalf("result = %+v", result)
	}
}

func testCapacityReadyAdoptedReservation(t *testing.T) {
	repository := openTestStore(t)
	evidence := adoptionEvidence()
	adopted, err := repository.AdoptCapacity(capacityRequest("op-adopt", "srv", "staging", "g"), evidence, 9, 9)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.RecordCapacityReady("srv", "staging", evidence.InstanceID, time.Now().UTC())
	capacityRefactorAssertSentinel(t, err, ErrCapacityTransition)
	if capacityRefactorJSON(t, result) != capacityRefactorJSON(t, adopted) {
		t.Fatalf("result = %+v", result)
	}
}

func testCapacityReadyRecordsOnceInUTC(t *testing.T) {
	repository := openTestStore(t)
	active := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
	readyAt := active.LastObservation.ObservedAt.In(time.FixedZone("east", 7200))
	ready, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt)
	if err != nil {
		t.Fatal(err)
	}
	if ready.ReadyAt.Location() != time.UTC || !ready.ReadyAt.Equal(readyAt) {
		t.Fatalf("ready = %+v", ready)
	}
	if ready.UpdatedAt.Before(ready.ReadyAt) || ready.State != CapacityActive || ready.UpdatedAt.Before(active.UpdatedAt) {
		t.Fatalf("ready = %+v", ready)
	}
	stored := capacityRefactorRawGet(t, repository, "op-a")
	if string(stored) != capacityRefactorJSON(t, ready) {
		t.Fatalf("stored %s, returned %+v", stored, ready)
	}

	assertCapacityReadyIdempotent(t, repository, readyAt, stored)
}

func assertCapacityReadyIdempotent(t *testing.T, repository *BoltStore, readyAt time.Time, stored []byte) {
	t.Helper()
	again, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt.UTC())
	if err != nil || capacityRefactorJSON(t, again) != string(stored) {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	different, err := repository.RecordCapacityReady("srv", "staging", "vm-a", readyAt.Add(time.Second))
	capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
	if capacityRefactorJSON(t, different) != string(stored) {
		t.Fatalf("conflict result = %+v", different)
	}
	capacityRefactorAssertStored(t, repository, "op-a", stored)
}

func testCapacityReadyFutureMeasurement(t *testing.T) {
	repository := openTestStore(t)
	capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
	future := time.Now().UTC().Add(5*time.Minute - time.Second)
	ready, err := repository.RecordCapacityReady("srv", "staging", "vm-a", future)
	if err != nil || !ready.UpdatedAt.Equal(future) || !ready.ReadyAt.Equal(future) {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
}

func testCapacityReadyDuplicateMatch(t *testing.T) {
	repository := openTestStore(t)
	first := capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
	if _, err := repository.ObserveCapacity("op-a", capacityObservation(first, "absent", "", first.LastObservation.ObservedAt.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	capacityRefactorActive(t, repository, "op-b", "srv", "vm-a")
	firstStored := capacityRefactorRawGet(t, repository, "op-a")
	result, err := repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
	capacityRefactorAssertSentinel(t, err, ErrCapacityConflict)
	if capacityRefactorJSON(t, result) != string(firstStored) {
		t.Fatalf("conflict result = %+v", result)
	}
}

func testCapacityReadyCorruptEntry(t *testing.T) {
	repository := openTestStore(t)
	capacityRefactorActive(t, repository, "op-a", "srv", "vm-a")
	stored := capacityRefactorRawGet(t, repository, "op-a")
	capacityRefactorRawPut(t, repository, "op-z", []byte("nope"))
	result, err := repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
	capacityRefactorAssertErr(t, err, ErrCorruptStore, "")
	if capacityRefactorJSON(t, result) != string(stored) {
		t.Fatalf("corrupt result = %+v", result)
	}
	mismatch := capacityRefactorLoad(t, repository, "op-a")
	capacityRefactorRawPut(t, repository, "op-z", []byte(capacityRefactorJSON(t, mismatch)))
	_, err = repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
	capacityRefactorAssertErr(t, err, ErrCorruptStore, capacityRefactorCorrupt+": capacity reservation key mismatch")
}

func testCapacityReadyAdapterFilter(t *testing.T) {
	repository := openTestStore(t)
	request := capacityRequest("op-a", "srv", "staging", "g")
	request.ProviderAdapterID = "hosting.vultr"
	reserved := reserve(t, repository, request)
	observePresent(t, repository, reserved, "vm-a", reserved.CreatedAt.Add(time.Second), time.Time{})
	var result CapacityReservation
	err := repository.db.Update(func(tx *bolt.Tx) error {
		var err error
		result, err = recordCapacityReadyTx(tx, "srv", "staging", "vm-a", "hosting.other", time.Now().UTC())
		return err
	})
	capacityRefactorAssertSentinel(t, err, ErrCapacityNotFound)
	if result.OperationID != "" {
		t.Fatalf("result = %+v", result)
	}
	ready, err := repository.RecordCapacityReady("srv", "staging", "vm-a", time.Now().UTC())
	if err != nil || ready.ReadyAt.IsZero() {
		t.Fatalf("ready = %+v, %v", ready, err)
	}
}
