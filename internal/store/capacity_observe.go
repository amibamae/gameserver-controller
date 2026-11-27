package store

import (
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// ObserveCapacity records provider state only when it refers to the exact
// provider resource reserved by the controller. Absent/destroyed observations
// are the only states that release capacity.
func (s *BoltStore) ObserveCapacity(operationID string, observation CapacityProviderObservation) (CapacityReservation, error) {
	if err := validateCapacityObservation(observation); err != nil {
		return CapacityReservation{}, err
	}
	return s.updateCapacity(operationID, func(current CapacityReservation) (CapacityReservation, error) {
		return applyCapacityObservation(current, observation)
	})
}

// capacityObservationRules are the checks validateCapacityObservation applies,
// in order, each with the problem it reports when the observation fails it.
var capacityObservationRules = []struct {
	valid   func(CapacityProviderObservation) bool
	problem string
}{
	{capacityObservationIdentified, "invalid provider observation"},
	{capacityObservationStateKnown, "unsupported provider observation state"},
	{presentObservationNamesInstance, "present observation requires provider_instance_id"},
	{capacityObservationProvisionOrdered, "invalid provider provisioning timestamp"},
	{capacityObservationInstanceIDValid, "invalid provider_instance_id"},
}

// validateCapacityObservation reports, as ErrInvalidRecord, the problem of
// the first rule in capacityObservationRules that observation breaks.
func validateCapacityObservation(observation CapacityProviderObservation) error {
	for _, rule := range capacityObservationRules {
		if !rule.valid(observation) {
			return fmt.Errorf("%w: %s", ErrInvalidRecord, rule.problem)
		}
	}
	return nil
}

// capacityObservationIdentified requires a bounded provider and resource key
// and a plausible observation time.
func capacityObservationIdentified(observation CapacityProviderObservation) bool {
	return validCapacityIdentifier(observation.Provider, 100) && validCapacityIdentifier(observation.ResourceKey, 240) &&
		plausibleObservationTime(observation.ObservedAt)
}

// capacityObservationStateKnown requires a provider state the store
// understands.
func capacityObservationStateKnown(observation CapacityProviderObservation) bool {
	return knownCapacityObservationState(observation.State)
}

// presentObservationNamesInstance requires a present observation to carry a
// valid provider instance ID.
func presentObservationNamesInstance(observation CapacityProviderObservation) bool {
	return observation.State != "present" || validCapacityIdentifier(observation.ProviderInstanceID, 240)
}

// capacityObservationInstanceIDValid requires the provider instance ID, when
// set, to be valid.
func capacityObservationInstanceIDValid(observation CapacityProviderObservation) bool {
	return observation.ProviderInstanceID == "" || validCapacityIdentifier(observation.ProviderInstanceID, 240)
}

func applyCapacityObservation(current CapacityReservation, observation CapacityProviderObservation) (CapacityReservation, error) {
	if err := checkCapacityObservationIdentity(current, observation); err != nil {
		return CapacityReservation{}, err
	}
	replay, err := capacityObservationReplay(current.LastObservation, observation)
	if err != nil {
		return CapacityReservation{}, err
	}
	if replay {
		return current, nil
	}
	if err := checkCapacityObservationTransition(current, observation); err != nil {
		return CapacityReservation{}, err
	}
	return withCapacityObservation(current, observation), nil
}

// checkCapacityObservationIdentity rejects an observation of another provider
// resource, or one observed or provisioned before the reservation existed.
func checkCapacityObservationIdentity(current CapacityReservation, observation CapacityProviderObservation) error {
	if current.Provider != observation.Provider || current.ResourceKey != observation.ResourceKey {
		return ErrCapacityConflict
	}
	if observation.ObservedAt.Before(current.CreatedAt) {
		return ErrCapacityConflict
	}
	if !observation.ProvisionedAt.IsZero() && observation.ProvisionedAt.Before(current.CreatedAt) {
		return fmt.Errorf("%w: provider provisioning time precedes reservation", ErrInvalidRecord)
	}
	return nil
}

// capacityObservationReplay orders observation after last (nil when there is
// none). It reports true for an exact replay of last, and ErrCapacityConflict
// for an older observation or a different one at the same time.
func capacityObservationReplay(last *CapacityProviderObservation, observation CapacityProviderObservation) (bool, error) {
	if last == nil {
		return false, nil
	}
	if observation.ObservedAt.Before(last.ObservedAt) {
		return false, ErrCapacityConflict
	}
	if !observation.ObservedAt.Equal(last.ObservedAt) {
		return false, nil
	}
	if sameCapacityObservation(*last, observation) {
		return true, nil
	}
	return false, ErrCapacityConflict
}

// checkCapacityObservationTransition rejects a different provider instance,
// and a released reservation moving to the other released state.
func checkCapacityObservationTransition(current CapacityReservation, observation CapacityProviderObservation) error {
	if current.ProviderInstanceID != "" && observation.ProviderInstanceID != "" && current.ProviderInstanceID != observation.ProviderInstanceID {
		return ErrCapacityConflict
	}
	if capacityStateReleased(current.State) && observation.State != "present" && observation.State != string(current.State) {
		return ErrCapacityTransition
	}
	return nil
}

// withCapacityObservation returns current moved to the observed state, with
// the first known provisioning time, the observed instance ID when given, and
// observation as its last observation.
func withCapacityObservation(current CapacityReservation, observation CapacityProviderObservation) CapacityReservation {
	switch observation.State {
	case "present":
		current.State = CapacityActive
		if current.ProvisionedAt.IsZero() {
			current.ProvisionedAt = observation.ProvisionedAt
		}
	case "absent":
		current.State = CapacityAbsent
	case "destroyed":
		current.State = CapacityDestroyed
	}
	if observation.ProviderInstanceID != "" {
		current.ProviderInstanceID = observation.ProviderInstanceID
	}
	copyObservation := observation
	current.LastObservation = &copyObservation
	return current
}

func observeCapacityTx(tx *bolt.Tx, operationID string, observation CapacityProviderObservation) error {
	if err := validateCapacityObservation(observation); err != nil {
		return err
	}
	key := []byte(operationID)
	bucket := tx.Bucket(capacityReservationsBucket)
	current, err := loadCapacityReservation(bucket, key)
	if err != nil {
		return err
	}
	next, err := applyCapacityObservation(current, observation)
	if err != nil {
		return err
	}
	if !capacityTrackedFieldsChanged(current, next) {
		return nil
	}
	// Unlike updateCapacity, only the observation time floors UpdatedAt here.
	next.UpdatedAt = capacityUpdateTime(observation.ObservedAt)
	return putUpdatedCapacityReservation(bucket, key, next)
}

// sameCapacityObservation reports whether two observations name the same
// provider resource and instance in the same state at the same instants.
func sameCapacityObservation(left, right CapacityProviderObservation) bool {
	return left.Provider == right.Provider && left.ResourceKey == right.ResourceKey &&
		left.ProviderInstanceID == right.ProviderInstanceID && sameCapacityObservationStateAndTimes(left, right)
}

// sameCapacityObservationStateAndTimes reports whether two observations have
// the same state and the same observation and provisioning instants.
func sameCapacityObservationStateAndTimes(left, right CapacityProviderObservation) bool {
	return left.State == right.State && left.ObservedAt.Equal(right.ObservedAt) && left.ProvisionedAt.Equal(right.ProvisionedAt)
}
