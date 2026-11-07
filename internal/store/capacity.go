package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrCapacityReached    = errors.New("managed VM capacity reached")
	ErrCapacityOccupied   = errors.New("managed VM slot is occupied")
	ErrCapacityConflict   = errors.New("capacity reservation conflicts with stored state")
	ErrCapacityNotFound   = errors.New("capacity reservation not found")
	ErrCapacityTransition = errors.New("invalid capacity reservation transition")
)

type CapacityState string

const (
	CapacityReserved              CapacityState = "reserved"
	CapacityCreateOutcomeUnknown  CapacityState = "create_outcome_unknown"
	CapacityActive                CapacityState = "active"
	CapacityDestroyOutcomeUnknown CapacityState = "destroy_outcome_unknown"
	CapacityAbsent                CapacityState = "absent"
	CapacityDestroyed             CapacityState = "destroyed"
)

// CapacityReservation occupies a managed server slot from before a provider
// call until a provider observation confirms the resource is absent/destroyed.
// OperationID is the idempotency identity; ResourceKey is assigned by the
// controller before create/adopt so an ambiguous create can be reconciled.
type CapacityReservation struct {
	OperationID        string                       `json:"operation_id"`
	ServerID           string                       `json:"server_id"`
	EnvironmentID      string                       `json:"environment_id"`
	CapacityGroup      string                       `json:"capacity_group"`
	Provider           string                       `json:"provider"`
	ResourceKey        string                       `json:"resource_key"`
	ProviderInstanceID string                       `json:"provider_instance_id,omitempty"`
	State              CapacityState                `json:"state"`
	CreatedAt          time.Time                    `json:"created_at"`
	UpdatedAt          time.Time                    `json:"updated_at"`
	LastObservation    *CapacityProviderObservation `json:"last_observation,omitempty"`
}

// CapacityProviderObservation is evidence returned by the configured provider
// adapter after looking up the exact controller ResourceKey. Store methods
// still verify provider, resource identity, instance identity, and time order.
// Only trusted adapter code should pass observations to ObserveCapacity.
type CapacityProviderObservation struct {
	Provider           string    `json:"provider"`
	ResourceKey        string    `json:"resource_key"`
	ProviderInstanceID string    `json:"provider_instance_id,omitempty"`
	State              string    `json:"state"`
	ObservedAt         time.Time `json:"observed_at"`
}

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
	var result CapacityReservation
	created := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(capacityReservationsBucket)
		if existingValue := bucket.Get([]byte(reservation.OperationID)); existingValue != nil {
			existing, err := decodeCapacityReservation(existingValue)
			if err != nil {
				return err
			}
			if !sameCapacityRequest(existing, reservation) {
				return ErrCapacityConflict
			}
			result = existing
			return nil
		}

		total, groupCount := 0, 0
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			existing, err := decodeCapacityReservation(value)
			if err != nil {
				return err
			}
			if string(key) != existing.OperationID {
				return fmt.Errorf("%w: capacity reservation key mismatch", ErrCorruptStore)
			}
			if capacityStateReleased(existing.State) {
				continue
			}
			if existing.ServerID == reservation.ServerID && existing.EnvironmentID == reservation.EnvironmentID {
				return fmt.Errorf("%w: server %q in environment %q", ErrCapacityOccupied, reservation.ServerID, reservation.EnvironmentID)
			}
			if existing.Provider == reservation.Provider && existing.ResourceKey == reservation.ResourceKey {
				return fmt.Errorf("%w: provider resource key %q is already reserved", ErrCapacityConflict, reservation.ResourceKey)
			}
			total++
			if existing.CapacityGroup == reservation.CapacityGroup {
				groupCount++
			}
		}
		if total >= totalLimit {
			return ErrCapacityReached
		}
		if groupCount >= groupLimit {
			return ErrCapacityReached
		}
		now := time.Now().UTC()
		reservation.State = CapacityReserved
		reservation.CreatedAt = now
		reservation.UpdatedAt = now
		encoded, err := json.Marshal(reservation)
		if err != nil {
			return fmt.Errorf("encode capacity reservation: %w", err)
		}
		if err := bucket.Put([]byte(reservation.OperationID), encoded); err != nil {
			return err
		}
		result, created = reservation, true
		return nil
	})
	if err != nil {
		return CapacityReservation{}, false, err
	}
	return result, created, nil
}

// MarkCapacityCreateSent must be called immediately before dispatching a
// provider create request. The outcome remains counted until reconciled.
func (s *BoltStore) MarkCapacityCreateSent(operationID string) (CapacityReservation, error) {
	return s.updateCapacity(operationID, func(current CapacityReservation) (CapacityReservation, error) {
		if current.State == CapacityCreateOutcomeUnknown {
			return current, nil
		}
		if current.State != CapacityReserved {
			return CapacityReservation{}, ErrCapacityTransition
		}
		current.State = CapacityCreateOutcomeUnknown
		return current, nil
	})
}

// MarkCapacityDestroySent keeps a deletion in capacity until provider
// reconciliation confirms absence or destruction.
func (s *BoltStore) MarkCapacityDestroySent(operationID string) (CapacityReservation, error) {
	return s.updateCapacity(operationID, func(current CapacityReservation) (CapacityReservation, error) {
		if current.State == CapacityDestroyOutcomeUnknown {
			return current, nil
		}
		if current.State != CapacityActive {
			return CapacityReservation{}, ErrCapacityTransition
		}
		current.State = CapacityDestroyOutcomeUnknown
		return current, nil
	})
}

// ObserveCapacity records provider state only when it refers to the exact
// provider resource reserved by the controller. Absent/destroyed observations
// are the only states that release capacity.
func (s *BoltStore) ObserveCapacity(operationID string, observation CapacityProviderObservation) (CapacityReservation, error) {
	if !validCapacityIdentifier(observation.Provider, 100) || !validCapacityIdentifier(observation.ResourceKey, 240) ||
		observation.ObservedAt.IsZero() || observation.ObservedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return CapacityReservation{}, fmt.Errorf("%w: invalid provider observation", ErrInvalidRecord)
	}
	switch observation.State {
	case "present", "absent", "destroyed":
	default:
		return CapacityReservation{}, fmt.Errorf("%w: unsupported provider observation state", ErrInvalidRecord)
	}
	if observation.State == "present" && !validCapacityIdentifier(observation.ProviderInstanceID, 240) {
		return CapacityReservation{}, fmt.Errorf("%w: present observation requires provider_instance_id", ErrInvalidRecord)
	}
	if observation.ProviderInstanceID != "" && !validCapacityIdentifier(observation.ProviderInstanceID, 240) {
		return CapacityReservation{}, fmt.Errorf("%w: invalid provider_instance_id", ErrInvalidRecord)
	}
	return s.updateCapacity(operationID, func(current CapacityReservation) (CapacityReservation, error) {
		if current.Provider != observation.Provider || current.ResourceKey != observation.ResourceKey {
			return CapacityReservation{}, ErrCapacityConflict
		}
		if observation.ObservedAt.Before(current.CreatedAt) {
			return CapacityReservation{}, ErrCapacityConflict
		}
		if current.LastObservation != nil {
			if observation.ObservedAt.Before(current.LastObservation.ObservedAt) {
				return CapacityReservation{}, ErrCapacityConflict
			}
			if observation.ObservedAt.Equal(current.LastObservation.ObservedAt) {
				if sameCapacityObservation(*current.LastObservation, observation) {
					return current, nil
				}
				return CapacityReservation{}, ErrCapacityConflict
			}
		}
		if current.ProviderInstanceID != "" && observation.ProviderInstanceID != "" && current.ProviderInstanceID != observation.ProviderInstanceID {
			return CapacityReservation{}, ErrCapacityConflict
		}
		if current.State == CapacityAbsent || current.State == CapacityDestroyed {
			if observation.State != "present" && observation.State != string(current.State) {
				return CapacityReservation{}, ErrCapacityTransition
			}
		}
		switch observation.State {
		case "present":
			current.State = CapacityActive
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
		return current, nil
	})
}

func (s *BoltStore) GetCapacityReservation(operationID string) (CapacityReservation, error) {
	if !validCapacityIdentifier(operationID, 200) {
		return CapacityReservation{}, ErrCapacityNotFound
	}
	var result CapacityReservation
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(capacityReservationsBucket).Get([]byte(operationID))
		if value == nil {
			return ErrCapacityNotFound
		}
		var err error
		result, err = decodeCapacityReservation(value)
		return err
	})
	if err != nil {
		return CapacityReservation{}, err
	}
	return result, nil
}

func (s *BoltStore) ListCapacityReservations() ([]CapacityReservation, error) {
	var result []CapacityReservation
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(capacityReservationsBucket).Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			reservation, err := decodeCapacityReservation(value)
			if err != nil {
				return err
			}
			if string(key) != reservation.OperationID {
				return fmt.Errorf("%w: capacity reservation key mismatch", ErrCorruptStore)
			}
			result = append(result, reservation)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].OperationID < result[j].OperationID
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (s *BoltStore) updateCapacity(operationID string, transition func(CapacityReservation) (CapacityReservation, error)) (CapacityReservation, error) {
	if !validCapacityIdentifier(operationID, 200) {
		return CapacityReservation{}, ErrCapacityNotFound
	}
	var result CapacityReservation
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(capacityReservationsBucket)
		key := []byte(operationID)
		value := bucket.Get(key)
		if value == nil {
			return ErrCapacityNotFound
		}
		current, err := decodeCapacityReservation(value)
		if err != nil {
			return err
		}
		next, err := transition(current)
		if err != nil {
			return err
		}
		if next.OperationID != current.OperationID || next.CreatedAt != current.CreatedAt {
			return fmt.Errorf("%w: immutable capacity reservation fields changed", ErrCorruptStore)
		}
		if next.State != current.State || next.ProviderInstanceID != current.ProviderInstanceID || next.LastObservation != current.LastObservation {
			next.UpdatedAt = time.Now().UTC()
			encoded, err := json.Marshal(next)
			if err != nil {
				return fmt.Errorf("encode capacity reservation: %w", err)
			}
			if err := bucket.Put(key, encoded); err != nil {
				return err
			}
		}
		result = next
		return nil
	})
	if err != nil {
		return CapacityReservation{}, err
	}
	return result, nil
}

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

func validateCapacityReservationRequest(reservation CapacityReservation) error {
	if !validCapacityIdentifier(reservation.OperationID, 200) || !validCapacityIdentifier(reservation.ServerID, 200) ||
		!validCapacityIdentifier(reservation.EnvironmentID, 200) || !validCapacityIdentifier(reservation.CapacityGroup, 100) ||
		!validCapacityIdentifier(reservation.Provider, 100) || !validCapacityIdentifier(reservation.ResourceKey, 240) ||
		reservation.ProviderInstanceID != "" || reservation.State != "" || !reservation.CreatedAt.IsZero() || !reservation.UpdatedAt.IsZero() || reservation.LastObservation != nil {
		return fmt.Errorf("%w: invalid capacity reservation request", ErrInvalidRecord)
	}
	return nil
}

func validatePersistedCapacityReservation(reservation CapacityReservation) error {
	if !validCapacityIdentifier(reservation.OperationID, 200) || !validCapacityIdentifier(reservation.ServerID, 200) ||
		!validCapacityIdentifier(reservation.EnvironmentID, 200) || !validCapacityIdentifier(reservation.CapacityGroup, 100) ||
		!validCapacityIdentifier(reservation.Provider, 100) || !validCapacityIdentifier(reservation.ResourceKey, 240) ||
		reservation.CreatedAt.IsZero() || reservation.UpdatedAt.Before(reservation.CreatedAt) {
		return ErrInvalidRecord
	}
	switch reservation.State {
	case CapacityReserved, CapacityCreateOutcomeUnknown, CapacityActive, CapacityDestroyOutcomeUnknown, CapacityAbsent, CapacityDestroyed:
	default:
		return ErrInvalidRecord
	}
	if reservation.State == CapacityActive && !validCapacityIdentifier(reservation.ProviderInstanceID, 240) {
		return ErrInvalidRecord
	}
	if reservation.LastObservation != nil {
		observation := reservation.LastObservation
		if observation.Provider != reservation.Provider || observation.ResourceKey != reservation.ResourceKey || observation.ObservedAt.Before(reservation.CreatedAt) ||
			(observation.State == "present" && !validCapacityIdentifier(observation.ProviderInstanceID, 240)) {
			return ErrInvalidRecord
		}
		if observation.ProviderInstanceID != "" && (!validCapacityIdentifier(observation.ProviderInstanceID, 240) ||
			(reservation.ProviderInstanceID != "" && reservation.ProviderInstanceID != observation.ProviderInstanceID)) {
			return ErrInvalidRecord
		}
		switch observation.State {
		case "present", "absent", "destroyed":
		default:
			return ErrInvalidRecord
		}
		if (reservation.State == CapacityAbsent && observation.State != "absent") || (reservation.State == CapacityDestroyed && observation.State != "destroyed") ||
			(reservation.State == CapacityActive && observation.State != "present") {
			return ErrInvalidRecord
		}
	}
	return nil
}

func sameCapacityRequest(existing, requested CapacityReservation) bool {
	return existing.OperationID == requested.OperationID && existing.ServerID == requested.ServerID &&
		existing.EnvironmentID == requested.EnvironmentID && existing.CapacityGroup == requested.CapacityGroup &&
		existing.Provider == requested.Provider && existing.ResourceKey == requested.ResourceKey
}

func capacityStateReleased(state CapacityState) bool {
	return state == CapacityAbsent || state == CapacityDestroyed
}

func sameCapacityObservation(left, right CapacityProviderObservation) bool {
	return left.Provider == right.Provider && left.ResourceKey == right.ResourceKey &&
		left.ProviderInstanceID == right.ProviderInstanceID && left.State == right.State &&
		left.ObservedAt.Equal(right.ObservedAt)
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
