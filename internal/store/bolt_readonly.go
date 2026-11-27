package store

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// OpenBoltReadOnly inspects an existing current-schema database without creating,
// migrating, chmodding or writing it. A live writer can hold its exclusive file
// lock, so callers must handle the bounded lock timeout and retry later.
func OpenBoltReadOnly(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open read-only operation store: %w", err)
	}
	if err := db.View(validateReadOnlySchema); err != nil {
		return nil, closeAfterFailedOpen(db, err)
	}
	if err := db.View(verifyRecoveryIndexes); err != nil {
		return nil, closeAfterFailedOpen(db, err)
	}
	return &BoltStore{db: db}, nil
}

func validateReadOnlySchema(tx *bolt.Tx) error {
	for _, name := range [][]byte{operationsBucket, metadataBucket, idempotencyBucket,
		locksBucket, operationLocksBucket, keyVersionsBucket, logicalServersBucket,
		capacityReservationsBucket, managementAPIKeysBucket, hostHandoffsBucket} {
		if tx.Bucket(name) == nil {
			return fmt.Errorf("read-only operation store: missing bucket %q", name)
		}
	}
	if string(tx.Bucket(metadataBucket).Get(schemaKey)) != databaseSchema {
		return fmt.Errorf("read-only operation store requires schema version %s", databaseSchema)
	}
	return nil
}
