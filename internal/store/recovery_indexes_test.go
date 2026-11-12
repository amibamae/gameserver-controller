package store

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// recoveryIndexFixture is a closed store holding one active server.backup
// operation with its idempotency entry and logical-server lock.
type recoveryIndexFixture struct {
	path        string
	operationID string
	scope       string
	verifierKey []byte
}

func newRecoveryIndexFixture(t *testing.T) recoveryIndexFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "controller.db")
	store, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	testLogicalServerFixture(t, store)
	record := testOperation("recovery-index-check")
	record.OperationType = "server.backup"
	record.RequestSnapshot.OperationType = record.OperationType
	record.RequestFingerprint = "sha256:" + strings.Repeat("b", 64)
	scope := LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID)
	digest := bytes.Repeat([]byte{1}, 32)
	proof := IdempotencyProof{
		ActiveKeyVersion: "v1",
		Candidates:       []IdempotencyVerifier{{KeyVersion: "v1", Digest: digest, BindingDigest: bytes.Repeat([]byte{2}, 32)}},
	}
	if _, _, err := store.AcceptOperation(record, proof, []string{scope}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return recoveryIndexFixture{path: path, operationID: record.OperationID, scope: scope, verifierKey: []byte("v1:" + hex.EncodeToString(digest))}
}

// corruptAndVerify applies corrupt in one write transaction, then runs
// verifyRecoveryIndexes over the result.
func (f recoveryIndexFixture) corruptAndVerify(t *testing.T, corrupt func(*testing.T, *bolt.Tx)) error {
	t.Helper()
	db, err := bolt.Open(f.path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Update(func(tx *bolt.Tx) error { corrupt(t, tx); return nil }); err != nil {
		t.Fatal(err)
	}
	return db.View(verifyRecoveryIndexes)
}

func mustPut(t *testing.T, bucket *bolt.Bucket, key, value []byte) {
	t.Helper()
	if err := bucket.Put(key, value); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestVerifyRecoveryIndexesAcceptsConsistentStore(t *testing.T) {
	f := newRecoveryIndexFixture(t)
	if err := f.corruptAndVerify(t, func(*testing.T, *bolt.Tx) {}); err != nil {
		t.Fatalf("verifyRecoveryIndexes: %v", err)
	}
}

func TestVerifyRecoveryIndexesRejectsInconsistentIndexes(t *testing.T) {
	lock := func(scope, operationID string) ResourceLock {
		return ResourceLock{Scope: scope, OperationID: operationID, AcquiredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	}
	tests := []struct {
		name    string
		corrupt func(*testing.T, *bolt.Tx, recoveryIndexFixture)
		want    error
		message string
	}{
		{"invalid key version name", func(t *testing.T, tx *bolt.Tx, _ recoveryIndexFixture) {
			mustPut(t, tx.Bucket(keyVersionsBucket), []byte("bad version"), []byte("x"))
		}, ErrCorruptStore, "invalid idempotency key version metadata"},
		{"empty key version metadata", func(t *testing.T, tx *bolt.Tx, _ recoveryIndexFixture) {
			mustPut(t, tx.Bucket(keyVersionsBucket), []byte("v2"), []byte{})
		}, ErrCorruptStore, "invalid idempotency key version metadata"},
		{"malformed idempotency entry", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(idempotencyBucket), f.verifierKey, []byte("{}"))
		}, ErrCorruptStore, "malformed idempotency entry"},
		{"verifier key with a short digest", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			bucket := tx.Bucket(idempotencyBucket)
			mustPut(t, bucket, []byte("v1:abcd"), bucket.Get(f.verifierKey))
		}, ErrCorruptStore, "invalid idempotency verifier index"},
		{"verifier key that is not hex", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			bucket := tx.Bucket(idempotencyBucket)
			mustPut(t, bucket, []byte("v1:"+strings.Repeat("z", 64)), bucket.Get(f.verifierKey))
		}, ErrCorruptStore, "invalid idempotency verifier index"},
		{"entry for an unrecorded key version", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			bucket := tx.Bucket(idempotencyBucket)
			var entry idempotencyEntry
			if err := json.Unmarshal(bucket.Get(f.verifierKey), &entry); err != nil {
				t.Fatal(err)
			}
			if err := bucket.Delete(f.verifierKey); err != nil {
				t.Fatal(err)
			}
			entry.KeyVersion = "v9"
			mustPut(t, bucket, []byte("v9:"+strings.Repeat("a", 64)), mustJSON(t, entry))
		}, ErrCorruptStore, "unknown key version"},
		{"second entry for one operation", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			bucket := tx.Bucket(idempotencyBucket)
			mustPut(t, bucket, []byte("v1:"+strings.Repeat("c", 64)), bucket.Get(f.verifierKey))
		}, ErrCorruptStore, "multiple idempotency entries"},
		{"reverse index is not JSON", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(operationLocksBucket), []byte(f.operationID), []byte("not json"))
		}, ErrCorruptStore, "decode operation lock scopes"},
		{"empty reverse index", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(operationLocksBucket), []byte(f.operationID), []byte("[]"))
		}, ErrCorruptStore, "empty operation lock index"},
		{"invalid scope in reverse index", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(operationLocksBucket), []byte(f.operationID), mustJSON(t, []string{""}))
		}, ErrCorruptStore, "invalid operation lock scopes"},
		{"reverse index out of order", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(operationLocksBucket), []byte(f.operationID), mustJSON(t, []string{f.scope, "a-scope"}))
		}, ErrCorruptStore, "not in stable order"},
		{"reverse index for a missing operation", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(operationLocksBucket), []byte("missing-operation"), mustJSON(t, []string{f.scope}))
		}, ErrCorruptStore, "resource locks refer to a missing operation"},
		{"terminal operation in reverse index", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			bucket := tx.Bucket(operationsBucket)
			var envelope operationEnvelope
			if err := json.Unmarshal(bucket.Get([]byte(f.operationID)), &envelope); err != nil {
				t.Fatal(err)
			}
			envelope.Record.Status = protocolstate.OperationCancelled
			completed := envelope.Record.UpdatedAt
			envelope.Record.CompletedAt = &completed
			mustPut(t, bucket, []byte(f.operationID), mustJSON(t, envelope))
		}, ErrCorruptStore, "terminal operation still owns resource locks"},
		{"forward lock missing", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			if err := tx.Bucket(locksBucket).Delete([]byte(f.scope)); err != nil {
				t.Fatal(err)
			}
		}, ErrCorruptStore, "is missing"},
		{"forward lock owned by another operation", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(locksBucket), []byte(f.scope), mustJSON(t, lock(f.scope, "other-operation")))
		}, ErrCorruptStore, "does not own lock"},
		{"forward lock keyed by another scope", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(locksBucket), []byte("zz-extra"), mustJSON(t, lock("zz-other", f.operationID)))
		}, ErrCorruptStore, "resource lock scope mismatch"},
		{"forward lock for a missing operation", func(t *testing.T, tx *bolt.Tx, _ recoveryIndexFixture) {
			mustPut(t, tx.Bucket(locksBucket), []byte("zz-extra"), mustJSON(t, lock("zz-extra", "missing-operation")))
		}, ErrCorruptStore, "resource lock refers to a missing operation"},
		{"forward lock missing from reverse index", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			mustPut(t, tx.Bucket(locksBucket), []byte("zz-extra"), mustJSON(t, lock("zz-extra", f.operationID)))
		}, ErrCorruptStore, "resource lock has no active owner index"},
		{"server operation without idempotency entry", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			if err := tx.Bucket(idempotencyBucket).Delete(f.verifierKey); err != nil {
				t.Fatal(err)
			}
		}, ErrStoreNeedsMigration, ErrStoreNeedsMigration.Error()},
		{"active server operation without its logical-server lock", func(t *testing.T, tx *bolt.Tx, f recoveryIndexFixture) {
			other := "zz-other-scope"
			mustPut(t, tx.Bucket(operationLocksBucket), []byte(f.operationID), mustJSON(t, []string{other}))
			if err := tx.Bucket(locksBucket).Delete([]byte(f.scope)); err != nil {
				t.Fatal(err)
			}
			mustPut(t, tx.Bucket(locksBucket), []byte(other), mustJSON(t, lock(other, f.operationID)))
		}, ErrStoreNeedsMigration, "missing its logical-server lock"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newRecoveryIndexFixture(t)
			err := f.corruptAndVerify(t, func(t *testing.T, tx *bolt.Tx) { test.corrupt(t, tx, f) })
			if !errors.Is(err, test.want) || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("verifyRecoveryIndexes error = %v, want %v containing %q", err, test.want, test.message)
			}
		})
	}
}
