package store

import (
	"time"
)

// validatePersistedCapacityReservation rejects a stored reservation that breaks
// any of the persistence rules below. Every rule reports the same
// ErrInvalidRecord, so a new rule is one more function in this list.
func validatePersistedCapacityReservation(reservation CapacityReservation) error {
	for _, valid := range []func(CapacityReservation) bool{
		capacityAdoptionConsistent,
		capacityIdentityValid,
		capacityStateValid,
		capacityTimesOrdered,
		capacityObservationConsistent,
	} {
		if !valid(reservation) {
			return ErrInvalidRecord
		}
	}
	return nil
}

// capacityAdoptionConsistent requires an adopted reservation to carry a valid
// adoption record for its own provider instance, created when the adoption
// was observed, with an observation and no provision or ready time.
func capacityAdoptionConsistent(r CapacityReservation) bool {
	if r.Adoption == nil {
		return true
	}
	return capacityAdoptionMatchesReservation(r) && r.LastObservation != nil && allTimesZero(r.ProvisionedAt, r.ReadyAt)
}

// capacityAdoptionMatchesReservation requires the adoption record to be
// valid, for the reservation's provider instance, and observed when the
// reservation was created. r.Adoption must not be nil.
func capacityAdoptionMatchesReservation(r CapacityReservation) bool {
	return validateCapacityAdoption(*r.Adoption) == nil && r.ProviderInstanceID == r.Adoption.InstanceID &&
		r.CreatedAt.Equal(r.Adoption.ObservedAt)
}

// capacityIdentityValid requires every identifier to be present and bounded
// (the provider adapter ID is optional) and the record to have a creation
// time no later than its update time.
func capacityIdentityValid(r CapacityReservation) bool {
	if r.ProviderAdapterID != "" && !validCapacityIdentifier(r.ProviderAdapterID, 160) {
		return false
	}
	return capacityIdentifiersValid(r) && !r.CreatedAt.IsZero() && !r.UpdatedAt.Before(r.CreatedAt)
}

// capacityIdentifiersValid requires every required reservation identifier to
// be present and within its length bound.
func capacityIdentifiersValid(r CapacityReservation) bool {
	return capacityIdentifiersWithin([]capacityIdentifierBound{
		{r.OperationID, 200}, {r.ServerID, 200}, {r.EnvironmentID, 200},
		{r.CapacityGroup, 100}, {r.Provider, 100}, {r.ResourceKey, 240},
	})
}

// capacityIdentifierBound is an identifier and the most bytes it may hold.
type capacityIdentifierBound struct {
	value string
	max   int
}

// capacityIdentifiersWithin reports whether every identifier is valid
// within its bound.
func capacityIdentifiersWithin(bounds []capacityIdentifierBound) bool {
	for _, bound := range bounds {
		if !validCapacityIdentifier(bound.value, bound.max) {
			return false
		}
	}
	return true
}

// capacityStateValid requires a known state, and an active reservation to name
// its provider instance.
func capacityStateValid(r CapacityReservation) bool {
	switch r.State {
	case CapacityReserved, CapacityCreateOutcomeUnknown, CapacityActive, CapacityDestroyOutcomeUnknown, CapacityAbsent, CapacityDestroyed:
	default:
		return false
	}
	return r.State != CapacityActive || validCapacityIdentifier(r.ProviderInstanceID, 240)
}

// capacityTimesOrdered requires provision and ready times, when set, to fall
// within the record's lifetime, with ready no earlier than provisioned.
func capacityTimesOrdered(r CapacityReservation) bool {
	if !r.ProvisionedAt.IsZero() && !capacityTimeWithin(r.ProvisionedAt, r.CreatedAt, r.UpdatedAt) {
		return false
	}
	return r.ReadyAt.IsZero() || (capacityTimeWithin(r.ReadyAt, r.CreatedAt, r.UpdatedAt) && notBeforeKnownTime(r.ReadyAt, r.ProvisionedAt))
}

// capacityTimeWithin reports whether at falls in [from, to].
func capacityTimeWithin(at, from, to time.Time) bool {
	return !at.Before(from) && !at.After(to)
}

// allTimesZero reports whether every time is the zero time.
func allTimesZero(times ...time.Time) bool {
	for _, at := range times {
		if !at.IsZero() {
			return false
		}
	}
	return true
}

// capacityObservationConsistent requires the last provider observation, if
// any, to be for this provider and resource key, observed after creation, with
// a known state that matches the reservation state, and an instance ID (present
// whenever the instance was seen) that agrees with the reservation's.
func capacityObservationConsistent(r CapacityReservation) bool {
	observation := r.LastObservation
	if observation == nil {
		return true
	}
	return capacityObservationMatchesReservation(r, *observation) && capacityObservationProvisionOrdered(*observation) &&
		knownCapacityObservationState(observation.State) && capacityStateAgreesWithObservation(r.State, observation.State)
}

// capacityObservationMatchesReservation requires the observation to be for
// the reservation's provider resource, no earlier than its creation, with a
// valid instance ID when present and one that agrees with the reservation's.
func capacityObservationMatchesReservation(r CapacityReservation, observation CapacityProviderObservation) bool {
	return capacityObservationForResource(r, observation) && presentObservationNamesInstance(observation) &&
		capacityObservationInstanceAgrees(r.ProviderInstanceID, observation)
}

// capacityObservationForResource requires the observation to be for the
// reservation's provider and resource key, and no earlier than its creation.
func capacityObservationForResource(r CapacityReservation, observation CapacityProviderObservation) bool {
	return observation.Provider == r.Provider && observation.ResourceKey == r.ResourceKey && !observation.ObservedAt.Before(r.CreatedAt)
}

// capacityObservationInstanceAgrees requires the observed instance ID, when
// set, to be valid and to equal the reserved one, when that is set.
func capacityObservationInstanceAgrees(reserved string, observation CapacityProviderObservation) bool {
	if observation.ProviderInstanceID == "" {
		return true
	}
	return validCapacityIdentifier(observation.ProviderInstanceID, 240) && (reserved == "" || reserved == observation.ProviderInstanceID)
}

// capacityObservationProvisionOrdered allows a provisioning time only on a
// present observation, and no later than the observation itself.
func capacityObservationProvisionOrdered(observation CapacityProviderObservation) bool {
	return observation.ProvisionedAt.IsZero() || (observation.State == "present" && !observation.ProvisionedAt.After(observation.ObservedAt))
}

// knownCapacityObservationState reports whether state is a provider
// observation state the store understands.
func knownCapacityObservationState(state string) bool {
	switch state {
	case "present", "absent", "destroyed":
		return true
	}
	return false
}

// capacityStateAgreesWithObservation requires active, absent and destroyed
// reservations to match the last observed provider state; other states may
// carry any observation.
func capacityStateAgreesWithObservation(state CapacityState, observed string) bool {
	switch state {
	case CapacityActive:
		return observed == "present"
	case CapacityAbsent:
		return observed == "absent"
	case CapacityDestroyed:
		return observed == "destroyed"
	}
	return true
}

func capacityStateReleased(state CapacityState) bool {
	return state == CapacityAbsent || state == CapacityDestroyed
}

func validCapacityIdentifier(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
