package store

import (
	"errors"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// Recovery index checks must fail closed even when the store can otherwise
// open and its buckets exist.
func TestRecoveryIndexesValidateKeyVersionMetadata(t *testing.T) {
	repository := openTestStore(t)
	if err := repository.db.View(verifyRecoveryIndexes); err != nil {
		t.Fatalf("valid recovery indexes: %v", err)
	}
	if err := repository.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(keyVersionsBucket).Put([]byte("invalid version"), []byte("present"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.db.View(verifyRecoveryIndexes); !errors.Is(err, ErrCorruptStore) {
		t.Fatalf("corrupt key version index: got %v, want ErrCorruptStore", err)
	}
}
