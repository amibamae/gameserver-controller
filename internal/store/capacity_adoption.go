package store

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"time"

	bolt "go.etcd.io/bbolt"
)

// CapacityAdoption records the exact provider metadata checked when an operator
// explicitly brings an existing VM under managed capacity. It is local state,
// not a game readiness assertion or permission to destroy the instance.
type CapacityAdoption struct {
	InstanceID string    `json:"instance_id"`
	IP         string    `json:"ip"`
	Region     string    `json:"region"`
	Plan       string    `json:"plan"`
	OSID       int       `json:"os_id"`
	OS         string    `json:"os"`
	ObservedAt time.Time `json:"observed_at"`
}

// publicIPv4 reports whether value is a global unicast, non-private IPv4
// address.
func publicIPv4(value string) bool {
	ip, err := netip.ParseAddr(value)
	return err == nil && ip.Is4() && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

// validAdoptedInstance reports whether the adopted instance's identifiers
// and OS ID are usable.
func validAdoptedInstance(a CapacityAdoption) bool {
	return validCapacityIdentifier(a.InstanceID, 200) && validCapacityIdentifier(a.Region, 200) &&
		validCapacityIdentifier(a.Plan, 200) && validCapacityIdentifier(a.OS, 500) && a.OSID >= 1
}

// plausibleObservationTime reports whether at is set and no more than five
// minutes in the future.
func plausibleObservationTime(at time.Time) bool {
	return !at.IsZero() && !at.After(time.Now().UTC().Add(5*time.Minute))
}

func validateCapacityAdoption(a CapacityAdoption) error {
	if !publicIPv4(a.IP) || !validAdoptedInstance(a) || !plausibleObservationTime(a.ObservedAt) {
		return ErrInvalidRecord
	}
	return nil
}

// AdoptCapacity commits ownership, exact observed identity, and occupied capacity
// in one transaction. Callers must verify the provider observation and operator
// approval first. A failed transaction leaves no reservation behind.
func (s *BoltStore) AdoptCapacity(request CapacityReservation, evidence CapacityAdoption, totalLimit, groupLimit int) (CapacityReservation, error) {
	// Validate inputs before entering transaction
	if err := validateAdoptCapacityInputs(request, evidence, totalLimit, groupLimit); err != nil {
		return CapacityReservation{}, ErrInvalidRecord
	}
	var result CapacityReservation
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		result, err = adoptCapacityTx(tx, request, evidence, totalLimit, groupLimit)
		return err
	})
	if err != nil {
		return CapacityReservation{}, err
	}
	return result, nil
}

// validateAdoptCapacityInputs performs all pre-transaction validation.
func validateAdoptCapacityInputs(request CapacityReservation, evidence CapacityAdoption, totalLimit, groupLimit int) error {
	if err := validateCapacityReservationRequest(request); err != nil {
		return err
	}
	if err := validateCapacityAdoption(evidence); err != nil {
		return err
	}
	if totalLimit < 1 || groupLimit < 1 {
		return fmt.Errorf("%w: limits must be positive", ErrInvalidRecord)
	}
	return nil
}

// adoptCapacityTx handles the transactional logic: reserve capacity and either activate or conflict-check.
func adoptCapacityTx(tx *bolt.Tx, request CapacityReservation, evidence CapacityAdoption, totalLimit, groupLimit int) (CapacityReservation, error) {
	bucket := tx.Bucket(capacityReservationsBucket)
	if err := checkProviderInstanceUniqueness(bucket, request, evidence.InstanceID); err != nil {
		return CapacityReservation{}, err
	}
	current, created, err := reserveCapacityTx(tx, request, totalLimit, groupLimit)
	if err != nil {
		return CapacityReservation{}, err
	}
	if created {
		return activateAdoptedCapacity(bucket, current, evidence)
	}
	if current.State != CapacityActive || current.Adoption == nil || !sameAdoptionIdentity(*current.Adoption, evidence) {
		return CapacityReservation{}, ErrCapacityConflict
	}
	return current, nil
}

// checkProviderInstanceUniqueness ensures the same provider ID cannot occupy two independently named slots.
func checkProviderInstanceUniqueness(bucket *bolt.Bucket, request CapacityReservation, instanceID string) error {
	cursor := bucket.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		current, err := decodeCapacityReservation(value)
		if err != nil {
			return err
		}
		if string(key) != current.OperationID {
			return ErrCorruptStore
		}
		// Conflict if a non-released reservation from the same provider owns this instance ID and has a different operation ID
		if current.OperationID != request.OperationID && !capacityStateReleased(current.State) && current.Provider == request.Provider && current.ProviderInstanceID == instanceID {
			return ErrCapacityConflict
		}
	}
	return nil
}

// activateAdoptedCapacity records the exact observed identity and occupied capacity.
func activateAdoptedCapacity(bucket *bolt.Bucket, current CapacityReservation, evidence CapacityAdoption) (CapacityReservation, error) {
	current.State = CapacityActive
	current.ProviderInstanceID = evidence.InstanceID
	current.Adoption = &evidence
	current.CreatedAt = evidence.ObservedAt
	if current.UpdatedAt.Before(evidence.ObservedAt) {
		current.UpdatedAt = evidence.ObservedAt
	}
	current.LastObservation = &CapacityProviderObservation{Provider: current.Provider, ResourceKey: current.ResourceKey,
		ProviderInstanceID: evidence.InstanceID, State: "present", ObservedAt: evidence.ObservedAt}
	if err := validatePersistedCapacityReservation(current); err != nil {
		return CapacityReservation{}, err
	}
	encoded, err := json.Marshal(current)
	if err != nil {
		return CapacityReservation{}, err
	}
	if err := bucket.Put([]byte(current.OperationID), encoded); err != nil {
		return CapacityReservation{}, err
	}
	return current, nil
}

// sameAdoptionIdentity compares all identity fields of the adoption evidence.
func sameAdoptionIdentity(a, b CapacityAdoption) bool {
	return a.InstanceID == b.InstanceID &&
		a.IP == b.IP &&
		a.Region == b.Region &&
		a.Plan == b.Plan &&
		a.OSID == b.OSID &&
		a.OS == b.OS
}
