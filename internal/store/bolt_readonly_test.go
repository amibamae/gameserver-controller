package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestReadOnlyStorePreservesDatabaseAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenBoltReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ListCapacityReservations(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || info.Mode() != afterInfo.Mode() {
		t.Fatal("read-only open modified the database")
	}
}

func TestReadOnlyStoreRejectsOldSchemaWithoutMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(metadataBucket).Put(schemaKey, []byte("4"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = OpenBoltReadOnly(path)
	if err == nil || err.Error() != "read-only operation store requires schema version 5" {
		t.Fatalf("got %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only open migrated database")
	}
}

func TestReadOnlyStoreTimesOutWhenWriterOwnsDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = OpenBoltReadOnly(path)
	if !errors.Is(err, bolt.ErrTimeout) {
		t.Fatalf("expected bounded lock timeout, got %v", err)
	}
}

func TestReadOnlyStoreNeverCreatesMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if db, err := OpenBoltReadOnly(path); err == nil {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("created database: %v", err)
	}
}

func TestReadOnlyStoreRejectsIncompleteSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = OpenBoltReadOnly(path)
	if err == nil || err.Error() != "read-only operation store: missing bucket \"operations\"" {
		t.Fatalf("got %v", err)
	}
}
