package store

import (
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ReserveCapacity atomically enforces the total and capacity-group limits.
// A reservation is durable before a caller can start a provider side effect.
// Repeating the same operation with identical identity fields is idempotent.
func (s *BoltStore) ReserveCapacity(reservation CapacityReservation, totalLimit, groupLimit int) (CapacityReservation, bool, error) {
	if err := validateCapacityReservationRequest(reservation); err != nil {
		return CapacityReservation{}, false, err
	}
	if totalLimit < 1 || groupLimit < 1 {
		return CapacityReservation{}, false, fmt.Errorf("%w: invalid capacity limits", ErrInvalidRecord)
	}
	result, err := capacityTxValue(s.db.Update, func(tx *bolt.Tx) (reservedCapacity, error) {
		reserved, created, err := reserveCapacityTx(tx, reservation, totalLimit, groupLimit)
		return reservedCapacity{reservation: reserved, created: created}, err
	})
	if err != nil {
		return CapacityReservation{}, false, err
	}
	return result.reservation, result.created, nil
}

// reservedCapacity is what reserveCapacityTx returns, carried out of the
// ReserveCapacity transaction as one value.
type reservedCapacity struct {
	reservation CapacityReservation
	created     bool
}

// reserveCapacityTx returns the stored reservation for a repeated request,
// or stores a new one when the slot, the provider resource key, and the
// total and group limits allow it. created reports a new reservation.
func reserveCapacityTx(tx *bolt.Tx, reservation CapacityReservation, totalLimit, groupLimit int) (CapacityReservation, bool, error) {
	bucket := tx.Bucket(capacityReservationsBucket)
	if existing, found, err := replayCapacityReservation(bucket, reservation); found {
		return existing, false, err
	}
	usage, err := countCapacityUsage(bucket, reservation)
	if err != nil {
		return CapacityReservation{}, false, err
	}
	if usage.reaches(totalLimit, groupLimit) {
		return CapacityReservation{}, false, ErrCapacityReached
	}
	now := time.Now().UTC()
	reservation.State = CapacityReserved
	reservation.CreatedAt = now
	reservation.UpdatedAt = now
	if err := putCapacityReservation(bucket, []byte(reservation.OperationID), reservation, "capacity reservation"); err != nil {
		return CapacityReservation{}, false, err
	}
	return reservation, true, nil
}

// replayCapacityReservation reports whether a reservation is already stored
// for request's operation. When one is, it returns that reservation if
// request repeats its identity, and otherwise the zero value with
// ErrCapacityConflict or the decode error.
func replayCapacityReservation(bucket *bolt.Bucket, request CapacityReservation) (CapacityReservation, bool, error) {
	value := bucket.Get([]byte(request.OperationID))
	if value == nil {
		return CapacityReservation{}, false, nil
	}
	existing, err := decodeCapacityReservation(value)
	if err != nil {
		return CapacityReservation{}, true, err
	}
	if !sameCapacityRequest(existing, request) {
		return CapacityReservation{}, true, ErrCapacityConflict
	}
	return existing, true, nil
}

// capacityUsage counts the unreleased reservations in total and in the
// request's capacity group.
type capacityUsage struct {
	total int
	group int
}

// reaches reports whether one more reservation would exceed either limit.
func (u capacityUsage) reaches(totalLimit, groupLimit int) bool {
	return u.total >= totalLimit || u.group >= groupLimit
}

// countCapacityUsage counts the unreleased reservations, in key order, and
// stops at the first one that already holds request's slot or provider
// resource key.
func countCapacityUsage(bucket *bolt.Bucket, request CapacityReservation) (capacityUsage, error) {
	var usage capacityUsage
	err := forEachCapacityReservation(bucket, func(_ []byte, existing CapacityReservation) error {
		if capacityStateReleased(existing.State) {
			return nil
		}
		if err := capacitySlotConflict(existing, request); err != nil {
			return err
		}
		usage.total++
		if existing.CapacityGroup == request.CapacityGroup {
			usage.group++
		}
		return nil
	})
	return usage, err
}

// capacitySlotConflict rejects request when the unreleased existing
// reservation already holds its server slot or its provider resource key.
func capacitySlotConflict(existing, request CapacityReservation) error {
	if existing.ServerID == request.ServerID && existing.EnvironmentID == request.EnvironmentID {
		return fmt.Errorf("%w: server %q in environment %q", ErrCapacityOccupied, request.ServerID, request.EnvironmentID)
	}
	if existing.Provider == request.Provider && existing.ResourceKey == request.ResourceKey {
		return fmt.Errorf("%w: provider resource key %q is already reserved", ErrCapacityConflict, request.ResourceKey)
	}
	return nil
}

// MarkCapacityCreateSent is a one-time dispatch claim. Callers must claim it
// immediately before sending the provider create request; a repeated claim
// fails so a retry cannot accidentally dispatch the same create again. The
// outcome remains counted until reconciled.
func (s *BoltStore) MarkCapacityCreateSent(operationID string) (CapacityReservation, error) {
	return s.claimCapacityDispatch(operationID, CapacityReserved, CapacityCreateOutcomeUnknown)
}

// ReleaseUnsentCapacityReservation removes a reservation only while it is
// still in the pre-dispatch state. Once a create may have reached the
// provider, the reservation must remain for reconciliation.
func (s *BoltStore) ReleaseUnsentCapacityReservation(operationID, resourceKey string) error {
	if !validCapacityIdentifier(operationID, 200) || !validCapacityIdentifier(resourceKey, 240) {
		return ErrCapacityNotFound
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(capacityReservationsBucket)
		key := []byte(operationID)
		current, err := loadCapacityReservation(bucket, key)
		if err != nil {
			return err
		}
		if current.ResourceKey != resourceKey {
			return ErrCapacityConflict
		}
		if current.State != CapacityReserved {
			return ErrCapacityTransition
		}
		return bucket.Delete(key)
	})
}

// MarkCapacityDestroySent is a one-time dispatch claim. Callers must claim it
// immediately before sending the provider destroy request; a repeated claim
// fails so a retry cannot accidentally dispatch the same destroy again.
// Capacity remains occupied until provider reconciliation confirms absence or
// destruction.
func (s *BoltStore) MarkCapacityDestroySent(operationID string) (CapacityReservation, error) {
	return s.claimCapacityDispatch(operationID, CapacityActive, CapacityDestroyOutcomeUnknown)
}

// claimCapacityDispatch moves the reservation from the from state to the to
// state, and fails with ErrCapacityTransition unless it is in from.
func (s *BoltStore) claimCapacityDispatch(operationID string, from, to CapacityState) (CapacityReservation, error) {
	return s.updateCapacity(operationID, func(current CapacityReservation) (CapacityReservation, error) {
		if current.State != from {
			return CapacityReservation{}, ErrCapacityTransition
		}
		current.State = to
		return current, nil
	})
}

func validateCapacityReservationRequest(reservation CapacityReservation) error {
	if reservation.ProviderAdapterID != "" && !validCapacityIdentifier(reservation.ProviderAdapterID, 160) {
		return ErrInvalidRecord
	}
	if !capacityIdentifiersValid(reservation) || !capacityControllerFieldsUnset(reservation) {
		return fmt.Errorf("%w: invalid capacity reservation request", ErrInvalidRecord)
	}
	return nil
}

// capacityControllerFieldsUnset requires a reservation request to leave every
// field the store assigns (instance, state, times, evidence) empty.
func capacityControllerFieldsUnset(r CapacityReservation) bool {
	return r.ProviderInstanceID == "" && r.State == "" && capacityEvidenceUnset(r) &&
		allTimesZero(r.CreatedAt, r.UpdatedAt, r.ProvisionedAt, r.ReadyAt)
}

// capacityEvidenceUnset reports whether the reservation carries neither a
// provider observation nor adoption evidence.
func capacityEvidenceUnset(r CapacityReservation) bool {
	return r.LastObservation == nil && r.Adoption == nil
}

// sameCapacityRequest reports whether two reservations carry the same
// request identity.
func sameCapacityRequest(existing, requested CapacityReservation) bool {
	return capacityRequestIdentityOf(existing) == capacityRequestIdentityOf(requested)
}

// capacityRequestIdentity is the part of a reservation a repeated
// ReserveCapacity request must match.
type capacityRequestIdentity struct {
	OperationID       string
	ServerID          string
	EnvironmentID     string
	CapacityGroup     string
	Provider          string
	ProviderAdapterID string
	ResourceKey       string
}

func capacityRequestIdentityOf(r CapacityReservation) capacityRequestIdentity {
	return capacityRequestIdentity{
		OperationID: r.OperationID, ServerID: r.ServerID, EnvironmentID: r.EnvironmentID, CapacityGroup: r.CapacityGroup,
		Provider: r.Provider, ProviderAdapterID: r.ProviderAdapterID, ResourceKey: r.ResourceKey,
	}
}
