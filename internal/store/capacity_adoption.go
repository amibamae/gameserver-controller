package store

import (
	"encoding/json"
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

func validateCapacityAdoption(a CapacityAdoption) error {
	ip, err := netip.ParseAddr(a.IP)
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() ||
		!validCapacityIdentifier(a.InstanceID, 200) || !validCapacityIdentifier(a.Region, 200) ||
		!validCapacityIdentifier(a.Plan, 200) || !validCapacityIdentifier(a.OS, 500) || a.OSID < 1 ||
		a.ObservedAt.IsZero() || a.ObservedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return ErrInvalidRecord
	}
	return nil
}

// AdoptCapacity commits ownership, exact observed identity, and occupied capacity
// in one transaction. Callers must verify the provider observation and operator
// approval first. A failed transaction leaves no reservation behind.
func (s *BoltStore) AdoptCapacity(request CapacityReservation, evidence CapacityAdoption, totalLimit, groupLimit int) (CapacityReservation, error) {
	if validateCapacityReservationRequest(request) != nil || validateCapacityAdoption(evidence) != nil || totalLimit < 1 || groupLimit < 1 {
		return CapacityReservation{}, ErrInvalidRecord
	}
	var result CapacityReservation
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(capacityReservationsBucket)
		// The same provider ID cannot occupy two independently named slots.
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			current, err := decodeCapacityReservation(value)
			if err != nil {
				return err
			}
			if string(key) != current.OperationID {
				return ErrCorruptStore
			}
			if current.OperationID != request.OperationID && !capacityStateReleased(current.State) && current.Provider == request.Provider && current.ProviderInstanceID == evidence.InstanceID {
				return ErrCapacityConflict
			}
		}
		current, created, err := reserveCapacityTx(tx, request, totalLimit, groupLimit)
		if err != nil {
			return err
		}
		if !created {
			if current.State != CapacityActive || current.Adoption == nil || !sameAdoptionIdentity(*current.Adoption, evidence) {
				return ErrCapacityConflict
			}
			result = current
			return nil
		}
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
			return err
		}
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(current.OperationID), encoded); err != nil {
			return err
		}
		result = current
		return nil
	})
	if err != nil {
		return CapacityReservation{}, err
	}
	return result, nil
}

func sameAdoptionIdentity(a, b CapacityAdoption) bool {
	return a.InstanceID == b.InstanceID && a.IP == b.IP && a.Region == b.Region && a.Plan == b.Plan && a.OSID == b.OSID && a.OS == b.OS
}
