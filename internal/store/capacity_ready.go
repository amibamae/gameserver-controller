package store

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// RecordCapacityReady records the first exact Paper readiness timestamp for a
// controller-owned provider instance. Adopted or unmanaged instances have no
// capacity reservation and therefore do not create local boot samples.
func (s *BoltStore) RecordCapacityReady(serverID, environmentID, providerInstanceID string, readyAt time.Time) (CapacityReservation, error) {
	instance := capacityInstanceQuery{ServerID: serverID, EnvironmentID: environmentID, ProviderInstanceID: providerInstanceID}
	if !instance.valid() || !plausibleObservationTime(readyAt) {
		return CapacityReservation{}, fmt.Errorf("%w: invalid readiness measurement", ErrInvalidRecord)
	}
	return capacityTxValue(s.db.Update, func(tx *bolt.Tx) (CapacityReservation, error) {
		return recordCapacityReadyTx(tx, instance, readyAt)
	})
}

// capacityInstanceQuery names the provider instance a readiness measurement
// is for: the instance on ServerID in EnvironmentID and, when AdapterID is
// set, only one reserved through that hosting adapter.
type capacityInstanceQuery struct {
	ServerID           string
	EnvironmentID      string
	ProviderInstanceID string
	AdapterID          string
}

// valid requires the server, environment and instance identifiers to be
// present and within their bounds.
func (q capacityInstanceQuery) valid() bool {
	return validCapacityIdentifier(q.ServerID, 200) && validCapacityIdentifier(q.EnvironmentID, 200) &&
		validCapacityIdentifier(q.ProviderInstanceID, 240)
}

// matches reports whether reservation is for the queried instance and, when
// the query names an adapter, was reserved through it.
func (q capacityInstanceQuery) matches(reservation CapacityReservation) bool {
	return capacityReservationMatchesInstance(reservation, q.ServerID, q.EnvironmentID, q.ProviderInstanceID) &&
		(q.AdapterID == "" || reservation.ProviderAdapterID == q.AdapterID)
}

func recordCapacityReadyTx(tx *bolt.Tx, instance capacityInstanceQuery, readyAt time.Time) (CapacityReservation, error) {
	// Once a reservation matches, it is returned alongside any later error.
	bucket := tx.Bucket(capacityReservationsBucket)
	key, result, err := findCapacityReservationForInstance(bucket, instance)
	if err != nil {
		return result, err
	}
	if !capacityReadyAllowed(result, readyAt) {
		return result, ErrCapacityTransition
	}
	if !result.ReadyAt.IsZero() {
		if !result.ReadyAt.Equal(readyAt) {
			return result, ErrCapacityConflict
		}
		return result, nil
	}
	result.ReadyAt = readyAt.UTC()
	result.UpdatedAt = capacityUpdateTime(result.ReadyAt)
	if err := validatePersistedCapacityReservation(result); err != nil {
		return result, fmt.Errorf("%w: invalid readiness measurement: %v", ErrInvalidRecord, err)
	}
	return result, putCapacityReservation(bucket, key, result, "capacity readiness measurement")
}

// findCapacityReservationForInstance returns the key and value of the single
// reservation that instance matches. A second match is ErrCapacityConflict
// and no match is ErrCapacityNotFound. The first match is returned even with
// an error.
func findCapacityReservationForInstance(bucket *bolt.Bucket, instance capacityInstanceQuery) ([]byte, CapacityReservation, error) {
	found := false
	var matchedKey []byte
	var match CapacityReservation
	err := forEachCapacityReservation(bucket, func(key []byte, current CapacityReservation) error {
		if !instance.matches(current) {
			return nil
		}
		if found {
			return ErrCapacityConflict
		}
		found = true
		matchedKey = append([]byte(nil), key...)
		match = current
		return nil
	})
	if err == nil && !found {
		err = ErrCapacityNotFound
	}
	return matchedKey, match, err
}

func capacityReservationMatchesInstance(reservation CapacityReservation, serverID, environmentID, providerInstanceID string) bool {
	return reservation.ServerID == serverID && reservation.EnvironmentID == environmentID &&
		reservation.ProviderInstanceID == providerInstanceID
}

// capacityReadyAllowed reports whether readiness at readyAt may be recorded:
// the reservation is a controller-created active instance last seen present,
// and readyAt is no earlier than its creation or known provisioning time.
func capacityReadyAllowed(r CapacityReservation, readyAt time.Time) bool {
	if r.Adoption != nil || r.State != CapacityActive || r.LastObservation == nil || r.LastObservation.State != "present" {
		return false
	}
	return !readyAt.Before(r.CreatedAt) && notBeforeKnownTime(readyAt, r.ProvisionedAt)
}

// notBeforeKnownTime reports whether at is no earlier than floor, or floor
// is unknown (zero).
func notBeforeKnownTime(at, floor time.Time) bool {
	return floor.IsZero() || !at.Before(floor)
}
