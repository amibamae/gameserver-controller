package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// These tests pin AcceptOperation's rejection paths that the rest of the suite
// does not reach: argument guards, a reused operation ID, a malformed
// definition, and corrupt idempotency or logical-server indexes.

func verifier(version string, digest, binding byte) IdempotencyVerifier {
	return IdempotencyVerifier{KeyVersion: version, Digest: bytes.Repeat([]byte{digest}, 32), BindingDigest: bytes.Repeat([]byte{binding}, 32)}
}

func proofOf(active string, candidates ...IdempotencyVerifier) IdempotencyProof {
	return IdempotencyProof{ActiveKeyVersion: active, Candidates: candidates}
}

// serverBackupRecord returns an acceptable server.backup for the logical
// server that testLogicalServerFixture writes, with the lock scope it needs.
func serverBackupRecord(id string) (OperationRecord, string) {
	record := testOperation(id)
	record.OperationType = "server.backup"
	record.RequestSnapshot.OperationType = record.OperationType
	record.RequestFingerprint = "sha256:" + strings.Repeat("d", 64)
	return record, LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID)
}

func requireRejected(t *testing.T, err, want error, message string) {
	t.Helper()
	if !errors.Is(err, want) || !strings.Contains(err.Error(), message) {
		t.Fatalf("AcceptOperation error = %v, want %v containing %q", err, want, message)
	}
}

func TestAcceptOperationRejectsInvalidArguments(t *testing.T) {
	valid := proofOf("v1", verifier("v1", 1, 2))
	tests := []struct {
		name    string
		mutate  func(*OperationRecord, *IdempotencyProof, *[]string)
		message string
	}{
		{"missing idempotency key", func(r *OperationRecord, _ *IdempotencyProof, _ *[]string) { r.IdempotencyKey = "" }, "requires an idempotency key and request fingerprint"},
		{"malformed fingerprint", func(r *OperationRecord, _ *IdempotencyProof, _ *[]string) { r.RequestFingerprint = "sha256:zz" }, "requires an idempotency key and request fingerprint"},
		{"no verifier candidates", func(_ *OperationRecord, p *IdempotencyProof, _ *[]string) { p.Candidates = nil }, "missing active key version or verifier candidates"},
		{"empty lock scope", func(_ *OperationRecord, _ *IdempotencyProof, s *[]string) { *s = append(*s, "") }, "invalid lock scope"},
		{"server operation without its server lock", func(_ *OperationRecord, _ *IdempotencyProof, s *[]string) { *s = []string{"other-scope"} }, "requires lock"},
		{"operation that is not pending", func(r *OperationRecord, _ *IdempotencyProof, _ *[]string) { r.Status = protocolstate.OperationRunning }, "new operations must start pending"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			testLogicalServerFixture(t, store)
			record, scope := serverBackupRecord("argument-check")
			proof, scopes := valid, []string{scope}
			test.mutate(&record, &proof, &scopes)
			_, _, err := store.AcceptOperation(record, proof, scopes)
			requireRejected(t, err, ErrInvalidRecord, test.message)
			if _, getErr := store.GetOperation(record.OperationID); !errors.Is(getErr, ErrNotFound) {
				t.Fatalf("rejected operation was stored: %v", getErr)
			}
		})
	}
}

func TestAcceptOperationRejectsReusedOperationID(t *testing.T) {
	store := openTestStore(t)
	testLogicalServerFixture(t, store)
	record, scope := serverBackupRecord("reused-id")
	if _, _, err := store.AcceptOperation(record, proofOf("v1", verifier("v1", 1, 2)), []string{scope}); err != nil {
		t.Fatal(err)
	}
	// A different idempotency verifier, so this is not a replay.
	_, _, err := store.AcceptOperation(record, proofOf("v1", verifier("v1", 3, 4)), []string{scope})
	requireRejected(t, err, ErrAlreadyExists, ErrAlreadyExists.Error())
}

func TestAcceptOperationRejectsDefinitionWithSteps(t *testing.T) {
	store := openTestStore(t)
	record := testOperation("definition-with-steps")
	record.OperationType = "server.define"
	record.RequestSnapshot.OperationType = record.OperationType
	record.RequestFingerprint = "sha256:" + strings.Repeat("e", 64)
	scope := LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID)
	_, _, err := store.AcceptOperation(record, proofOf("v1", verifier("v1", 1, 2)), []string{scope})
	requireRejected(t, err, ErrInvalidRecord, "server.define must contain only a logical definition")
}

func TestAcceptOperationRejectsCorruptLogicalServer(t *testing.T) {
	store := openTestStore(t)
	server := testLogicalServerFixture(t, store)
	// Store the record under the key of another server.
	server.Record.ServerID = "server-2"
	encoded, err := encodeLogicalServer(server.Record)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(logicalServersBucket).Put(logicalServerKey("staging", "server-1"), encoded)
	}); err != nil {
		t.Fatal(err)
	}
	record, scope := serverBackupRecord("corrupt-server")
	_, _, err = store.AcceptOperation(record, proofOf("v1", verifier("v1", 1, 2)), []string{scope})
	requireRejected(t, err, ErrCorruptStore, "logical server key does not match its record")
}

func TestAcceptOperationRejectsCorruptIdempotencyIndex(t *testing.T) {
	first := verifier("v1", 1, 2)
	tests := []struct {
		name    string
		corrupt func(*testing.T, *BoltStore)
		replay  func() (OperationRecord, IdempotencyProof)
		message string
	}{
		{
			name: "entry recorded under another key version",
			corrupt: func(t *testing.T, store *BoltStore) {
				rewriteIdempotencyEntry(t, store, first, func(entry *idempotencyEntry) { entry.KeyVersion = "v2" })
			},
			replay: func() (OperationRecord, IdempotencyProof) {
				record, _ := serverBackupRecord("replay")
				return record, proofOf("v1", first)
			},
			message: "idempotency entry key version mismatch",
		},
		{
			name:    "verifier replayed by another actor",
			corrupt: func(*testing.T, *BoltStore) {},
			replay: func() (OperationRecord, IdempotencyProof) {
				record, _ := serverBackupRecord("replay")
				record.ActorID = "operator-2"
				return record, proofOf("v1", first)
			},
			message: "idempotency verifier actor mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t)
			testLogicalServerFixture(t, store)
			record, scope := serverBackupRecord("original")
			if _, _, err := store.AcceptOperation(record, proofOf("v1", first), []string{scope}); err != nil {
				t.Fatal(err)
			}
			test.corrupt(t, store)
			replayRecord, proof := test.replay()
			_, _, err := store.AcceptOperation(replayRecord, proof, []string{scope})
			requireRejected(t, err, ErrCorruptStore, test.message)
		})
	}
}

func TestAcceptOperationRejectsVerifiersForTwoOperations(t *testing.T) {
	store := openTestStore(t)
	testLogicalServerFixture(t, store)
	v1, v2 := verifier("v1", 1, 2), verifier("v2", 3, 4)
	// The first operation is recorded under v1, the second under v2.
	first := testOperation("first")
	first.RequestFingerprint = "sha256:" + strings.Repeat("a", 64)
	if _, _, err := store.AcceptOperation(first, proofOf("v1", v1), nil); err != nil {
		t.Fatal(err)
	}
	second := testOperation("second")
	second.RequestFingerprint = "sha256:" + strings.Repeat("b", 64)
	if _, _, err := store.AcceptOperation(second, proofOf("v2", verifier("v1", 9, 9), v2), nil); err != nil {
		t.Fatal(err)
	}
	// A request whose verifiers match both entries names two operations.
	replay := testOperation("third")
	replay.RequestFingerprint = "sha256:" + strings.Repeat("c", 64)
	_, _, err := store.AcceptOperation(replay, proofOf("v2", v1, v2), nil)
	requireRejected(t, err, ErrCorruptStore, "verifier candidates identify multiple operations")
}

func rewriteIdempotencyEntry(t *testing.T, store *BoltStore, candidate IdempotencyVerifier, change func(*idempotencyEntry)) {
	t.Helper()
	if err := store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(idempotencyBucket)
		var entry idempotencyEntry
		if err := json.Unmarshal(bucket.Get(verifierIndex(candidate)), &entry); err != nil {
			return err
		}
		change(&entry)
		encoded, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		return bucket.Put(verifierIndex(candidate), encoded)
	}); err != nil {
		t.Fatal(err)
	}
}
