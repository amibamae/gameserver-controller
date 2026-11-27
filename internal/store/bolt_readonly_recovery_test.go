package store

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func readOnlyFixtureBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReadOnlyStoreRejectsInvalidRecoveryIndexesWithoutRepair(t *testing.T) {
	fixture := newRecoveryIndexFixture(t)
	err := fixture.corruptAndVerify(t, func(t *testing.T, tx *bolt.Tx) {
		mustPut(t, tx.Bucket(keyVersionsBucket), []byte("invalid"), nil)
	})
	want := ErrCorruptStore.Error() + ": invalid idempotency key version metadata"
	if err == nil || err.Error() != want {
		t.Fatalf("fixture corruption = %v; want %q", err, want)
	}
	before := readOnlyFixtureBytes(t, fixture.path)
	_, err = OpenBoltReadOnly(fixture.path)
	if !errors.Is(err, ErrCorruptStore) || err.Error() != want {
		t.Fatalf("read-only recovery validation = %v; want %q", err, want)
	}
	if !bytes.Equal(before, readOnlyFixtureBytes(t, fixture.path)) {
		t.Fatal("read-only validation changed corrupt recovery indexes")
	}
	// An exclusive writer can open only after the failed read released its lock.
	writer, err := bolt.Open(fixture.path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("failed read-only open retained its lock: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyStoreRejectsMutatingStoreMethod(t *testing.T) {
	fixture := newRecoveryIndexFixture(t)
	before := readOnlyFixtureBytes(t, fixture.path)
	reader, err := OpenBoltReadOnly(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, mutationErr := reader.ReserveCapacity(CapacityReservation{
		OperationID: "readonly-create", ServerID: "pilot", EnvironmentID: "staging",
		CapacityGroup: "shared", Provider: "vultr", ResourceKey: "gsc-readonly",
	}, 1, 1)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(mutationErr, bolt.ErrDatabaseReadOnly) || mutationErr.Error() != "database is in read-only mode" {
		t.Fatalf("mutation = %v; want database is in read-only mode", mutationErr)
	}
	if !bytes.Equal(before, readOnlyFixtureBytes(t, fixture.path)) {
		t.Fatal("mutating method changed the read-only database")
	}
}
