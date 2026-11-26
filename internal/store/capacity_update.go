package store

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

func (s *BoltStore) updateCapacity(operationID string, transition func(CapacityReservation) (CapacityReservation, error)) (CapacityReservation, error) {
	return capacityReservationTx(s.db.Update, operationID, func(bucket *bolt.Bucket, key []byte) (CapacityReservation, error) {
		return updateCapacityTx(bucket, key, transition)
	})
}

// updateCapacityTx applies transition to the reservation stored under key
// inside the caller's transaction. It refuses changes to immutable fields and
// writes only when a tracked field changed, returning next either way.
func updateCapacityTx(bucket *bolt.Bucket, key []byte, transition func(CapacityReservation) (CapacityReservation, error)) (CapacityReservation, error) {
	current, err := loadCapacityReservation(bucket, key)
	if err != nil {
		return CapacityReservation{}, err
	}
	next, err := transition(current)
	if err != nil {
		return CapacityReservation{}, err
	}
	if capacityImmutableFieldsChanged(current, next) {
		return CapacityReservation{}, fmt.Errorf("%w: immutable capacity reservation fields changed", ErrCorruptStore)
	}
	if !capacityTrackedFieldsChanged(current, next) {
		return next, nil
	}
	next.UpdatedAt = capacityUpdateTime(capacityObservedAt(next.LastObservation), next.ProvisionedAt, next.ReadyAt)
	if err := putUpdatedCapacityReservation(bucket, key, next); err != nil {
		return CapacityReservation{}, err
	}
	return next, nil
}

// capacityImmutableFieldsChanged reports whether a transition changed the
// reservation identity, its creation time, or a provision or ready time that
// was already recorded. CreatedAt is compared with == on purpose.
func capacityImmutableFieldsChanged(current, next CapacityReservation) bool {
	return next.OperationID != current.OperationID || next.CreatedAt != current.CreatedAt || next.ProviderAdapterID != current.ProviderAdapterID ||
		capacityRecordedTimesChanged(current, next)
}

// capacityRecordedTimesChanged reports whether a transition changed a
// provision or ready time that was already recorded.
func capacityRecordedTimesChanged(current, next CapacityReservation) bool {
	return recordedTimeChanged(current.ProvisionedAt, next.ProvisionedAt) || recordedTimeChanged(current.ReadyAt, next.ReadyAt)
}

// recordedTimeChanged reports whether next differs from a recorded (non-zero)
// time.
func recordedTimeChanged(recorded, next time.Time) bool {
	return !recorded.IsZero() && !next.Equal(recorded)
}

// capacityTrackedFieldsChanged reports whether a transition changed any field
// whose change must be persisted with a new UpdatedAt.
func capacityTrackedFieldsChanged(current, next CapacityReservation) bool {
	return next.State != current.State || next.ProviderInstanceID != current.ProviderInstanceID || next.LastObservation != current.LastObservation ||
		capacityEvidenceTimesChanged(current, next)
}

// capacityEvidenceTimesChanged reports whether a transition changed the
// provision or ready time.
func capacityEvidenceTimesChanged(current, next CapacityReservation) bool {
	return !next.ProvisionedAt.Equal(current.ProvisionedAt) || !next.ReadyAt.Equal(current.ReadyAt)
}

// capacityUpdateTime returns the current UTC time, raised to the latest of
// floors so UpdatedAt never precedes evidence recorded on the reservation.
func capacityUpdateTime(floors ...time.Time) time.Time {
	updated := time.Now().UTC()
	for _, floor := range floors {
		if updated.Before(floor) {
			updated = floor
		}
	}
	return updated
}

// capacityObservedAt returns the observation time, or the zero time when
// there is no observation.
func capacityObservedAt(observation *CapacityProviderObservation) time.Time {
	if observation == nil {
		return time.Time{}
	}
	return observation.ObservedAt
}
