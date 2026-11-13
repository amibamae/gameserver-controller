package store

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// These characterization tests pin the bbolt store paths that the r3 store
// refactor splits into named stages: the exact error values and messages,
// and the stored bytes after every call. Failed calls must leave every
// bucket exactly as it was.

func storeR3AssertErr(t *testing.T, err, want error, message string) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("error = %v, want nil", err)
		}
		return
	}
	if !errors.Is(err, want) || err.Error() != message {
		t.Fatalf("error = %v, want %v with message %q", err, want, message)
	}
}

// storeR3Snapshot copies every bucket of the database.
func storeR3Snapshot(t *testing.T, db *bolt.DB) map[string]map[string]string {
	t.Helper()
	snapshot := make(map[string]map[string]string)
	if err := db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, bucket *bolt.Bucket) error {
			values := make(map[string]string)
			snapshot[string(name)] = values
			return bucket.ForEach(func(key, value []byte) error {
				values[string(key)] = string(value)
				return nil
			})
		})
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func storeR3AssertUnchanged(t *testing.T, db *bolt.DB, before map[string]map[string]string) {
	t.Helper()
	if after := storeR3Snapshot(t, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed call changed the store:\nbefore=%v\nafter=%v", before, after)
	}
}

func storeR3RawGet(t *testing.T, store *BoltStore, bucket, key []byte) []byte {
	t.Helper()
	var value []byte
	if err := store.db.View(func(tx *bolt.Tx) error {
		value = append([]byte(nil), tx.Bucket(bucket).Get(key)...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func storeR3RawPut(t *testing.T, store *BoltStore, bucket, key, value []byte) {
	t.Helper()
	if err := store.db.Update(func(tx *bolt.Tx) error {
		if value == nil {
			return tx.Bucket(bucket).Delete(key)
		}
		return tx.Bucket(bucket).Put(key, value)
	}); err != nil {
		t.Fatal(err)
	}
}

func storeR3JSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// storeR3RawDB writes a database directly with bbolt, without OpenBolt.
func storeR3RawDB(t *testing.T, path string, setup func(*bolt.Tx) error) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(setup); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func storeR3RawSnapshot(t *testing.T, path string) map[string]map[string]string {
	t.Helper()
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("reopen raw database (was it left open?): %v", err)
	}
	defer db.Close()
	return storeR3Snapshot(t, db)
}

func TestStoreR3OpenBoltArgumentsCharacterization(t *testing.T) {
	_, err := OpenBolt("")
	storeR3AssertErr(t, err, ErrInvalidRecord, "open operation store: invalid operation record: empty path")

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBolt(filepath.Join(blocker, "state", "controller.db")); err == nil || !strings.HasPrefix(err.Error(), "create operation store directory: ") {
		t.Fatalf("directory under a file: %v", err)
	}
	if _, err := OpenBolt(t.TempDir()); err == nil || !strings.HasPrefix(err.Error(), "open bbolt operation store: ") {
		t.Fatalf("directory as database: %v", err)
	}
}

func TestStoreR3OpenBoltFreshStoreCharacterization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "controller.db")
	opened, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := (*BoltStore)(nil).Close(); err != nil {
		t.Fatalf("nil close: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode = %o, want 600", info.Mode().Perm())
	}
	want := map[string]map[string]string{metadataBucketName: {"schema_version": "5"}}
	for _, name := range []string{operationsBucketName, idempotencyBucketName, locksBucketName, operationLocksBucketName, keyVersionsBucketName, logicalServersBucketName, capacityReservationsBucketName, managementAPIKeysBucketName, hostHandoffsBucketName} {
		want[name] = map[string]string{}
	}
	if got := storeR3RawSnapshot(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh store = %v, want %v", got, want)
	}
}

func TestStoreR3OpenBoltSchemaCharacterization(t *testing.T) {
	const migration = "initialize operation store: operation store contains legacy records requiring migration"
	putIn := func(bucket []byte, key, value string) func(*bolt.Tx) error {
		return func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists(bucket)
			if err != nil {
				return err
			}
			return b.Put([]byte(key), []byte(value))
		}
	}
	schema := func(version string) func(*bolt.Tx) error { return putIn(metadataBucket, string(schemaKey), version) }
	tests := []struct {
		name    string
		setup   []func(*bolt.Tx) error
		want    error
		message string
	}{
		{name: "unversioned empty file"},
		{name: "unversioned with empty buckets", setup: []func(*bolt.Tx) error{func(tx *bolt.Tx) error {
			_, err := tx.CreateBucket(operationsBucket)
			return err
		}}},
		{name: "unversioned management keys are not legacy", setup: []func(*bolt.Tx) error{putIn(managementAPIKeysBucket, "k", "v")}},
		{name: "unversioned host handoffs are not legacy", setup: []func(*bolt.Tx) error{putIn(hostHandoffsBucket, "k", "v")}},
		{name: "unversioned operations", setup: []func(*bolt.Tx) error{putIn(operationsBucket, "k", "v")}, want: ErrStoreNeedsMigration, message: migration},
		{name: "unversioned idempotency", setup: []func(*bolt.Tx) error{putIn(idempotencyBucket, "k", "v")}, want: ErrStoreNeedsMigration, message: migration},
		{name: "unversioned locks", setup: []func(*bolt.Tx) error{putIn(locksBucket, "k", "v")}, want: ErrStoreNeedsMigration, message: migration},
		{name: "unversioned operation locks", setup: []func(*bolt.Tx) error{putIn(operationLocksBucket, "k", "v")}, want: ErrStoreNeedsMigration, message: migration},
		{name: "unversioned key versions", setup: []func(*bolt.Tx) error{putIn(keyVersionsBucket, "k", "v")}, want: ErrStoreNeedsMigration, message: migration},
		{name: "unversioned logical servers", setup: []func(*bolt.Tx) error{putIn(logicalServersBucket, "k", "v")}, want: ErrStoreNeedsMigration, message: migration},
		{name: "unversioned capacity", setup: []func(*bolt.Tx) error{putIn(capacityReservationsBucket, "k", "v")}, want: ErrStoreNeedsMigration, message: migration},
		{name: "v1 empty", setup: []func(*bolt.Tx) error{schema("1")}},
		{name: "v1 with key versions only", setup: []func(*bolt.Tx) error{schema("1"), putIn(keyVersionsBucket, "v1", "\x01")}},
		{name: "v1 with operations", setup: []func(*bolt.Tx) error{schema("1"), putIn(operationsBucket, "k", "v")}, want: ErrStoreNeedsMigration, message: migration},
		{name: "v2", setup: []func(*bolt.Tx) error{schema("2")}},
		{name: "v3", setup: []func(*bolt.Tx) error{schema("3")}},
		{name: "v4", setup: []func(*bolt.Tx) error{schema("4")}},
		{name: "v5", setup: []func(*bolt.Tx) error{schema("5")}},
		{name: "v6", setup: []func(*bolt.Tx) error{schema("6")}, want: errStoreR3MessageOnly, message: `initialize operation store: unsupported operation store schema version "6"`},
		{name: "padded v5", setup: []func(*bolt.Tx) error{schema("05")}, want: errStoreR3MessageOnly, message: `initialize operation store: unsupported operation store schema version "05"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) { storeR3RunOpenCase(t, test.setup, test.want, test.message) })
	}
}

// errStoreR3MessageOnly marks an expected error that wraps no sentinel, so
// only its message is compared.
var errStoreR3MessageOnly = errors.New("message only")

func storeR3RunOpenCase(t *testing.T, setups []func(*bolt.Tx) error, want error, message string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "controller.db")
	storeR3RawDB(t, path, func(tx *bolt.Tx) error {
		for _, setup := range setups {
			if err := setup(tx); err != nil {
				return err
			}
		}
		return nil
	})
	before := storeR3RawSnapshot(t, path)
	opened, err := OpenBolt(path)
	if want == nil {
		storeR3AssertMigrated(t, path, opened, err, before)
		return
	}
	if want == errStoreR3MessageOnly {
		want = err
	}
	storeR3AssertErr(t, err, want, message)
	if after := storeR3RawSnapshot(t, path); !reflect.DeepEqual(before, after) {
		t.Fatalf("failed open changed the file: before=%v after=%v", before, after)
	}
}

// storeR3AssertMigrated requires a successful open to leave schema 5, all
// ten buckets, and every value other than the schema version unchanged.
func storeR3AssertMigrated(t *testing.T, path string, opened *BoltStore, err error, before map[string]map[string]string) {
	t.Helper()
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}
	after := storeR3RawSnapshot(t, path)
	if got := after[metadataBucketName]["schema_version"]; got != "5" {
		t.Fatalf("schema = %q, want 5", got)
	}
	if len(after) != 10 {
		t.Fatalf("buckets = %d, want 10: %v", len(after), after)
	}
	after[metadataBucketName]["schema_version"] = before[metadataBucketName]["schema_version"]
	for name, values := range before {
		if !reflect.DeepEqual(storeR3WithoutEmpty(values), storeR3WithoutEmpty(after[name])) {
			t.Fatalf("open changed bucket %s: before=%v after=%v", name, values, after[name])
		}
	}
}

func storeR3WithoutEmpty(values map[string]string) map[string]string {
	kept := make(map[string]string, len(values))
	for key, value := range values {
		if value != "" {
			kept[key] = value
		}
	}
	return kept
}
