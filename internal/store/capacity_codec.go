package store

import (
	"encoding/json"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

func decodeCapacityReservation(value []byte) (CapacityReservation, error) {
	var reservation CapacityReservation
	if err := json.Unmarshal(value, &reservation); err != nil {
		return CapacityReservation{}, fmt.Errorf("%w: decode capacity reservation: %v", ErrCorruptStore, err)
	}
	if err := validatePersistedCapacityReservation(reservation); err != nil {
		return CapacityReservation{}, fmt.Errorf("%w: invalid capacity reservation: %v", ErrCorruptStore, err)
	}
	return reservation, nil
}

// forEachCapacityReservation decodes every stored reservation in key order
// and passes it to visit with its key. It stops at the first decode failure,
// key/OperationID mismatch, or error returned by visit.
func forEachCapacityReservation(bucket *bolt.Bucket, visit func(key []byte, reservation CapacityReservation) error) error {
	cursor := bucket.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		reservation, err := decodeCapacityReservation(value)
		if err != nil {
			return err
		}
		if string(key) != reservation.OperationID {
			return fmt.Errorf("%w: capacity reservation key mismatch", ErrCorruptStore)
		}
		if err := visit(key, reservation); err != nil {
			return err
		}
	}
	return nil
}

// loadCapacityReservation reads and decodes the reservation stored under key,
// returning ErrCapacityNotFound when there is none.
func loadCapacityReservation(bucket *bolt.Bucket, key []byte) (CapacityReservation, error) {
	value := bucket.Get(key)
	if value == nil {
		return CapacityReservation{}, ErrCapacityNotFound
	}
	return decodeCapacityReservation(value)
}

// putCapacityReservation encodes reservation and stores it under key; what
// names the record in the encode error.
func putCapacityReservation(bucket *bolt.Bucket, key []byte, reservation CapacityReservation, what string) error {
	encoded, err := json.Marshal(reservation)
	if err != nil {
		return fmt.Errorf("encode %s: %w", what, err)
	}
	return bucket.Put(key, encoded)
}

// putUpdatedCapacityReservation validates a transitioned reservation before
// storing it under key.
func putUpdatedCapacityReservation(bucket *bolt.Bucket, key []byte, reservation CapacityReservation) error {
	if err := validatePersistedCapacityReservation(reservation); err != nil {
		return fmt.Errorf("%w: invalid updated capacity reservation: %v", ErrInvalidRecord, err)
	}
	return putCapacityReservation(bucket, key, reservation, "capacity reservation")
}
