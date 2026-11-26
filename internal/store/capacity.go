package store

import (
	"errors"
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
	ProviderAdapterID  string                       `json:"provider_adapter_id,omitempty"`
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
	ProvisionedAt      time.Time                    `json:"provisioned_at,omitempty"`
	ReadyAt            time.Time                    `json:"ready_at,omitempty"`
	Adoption           *CapacityAdoption            `json:"adoption,omitempty"`
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
	ProvisionedAt      time.Time `json:"provisioned_at,omitempty"`
}

func (s *BoltStore) GetCapacityReservation(operationID string) (CapacityReservation, error) {
	return capacityReservationTx(s.db.View, operationID, loadCapacityReservation)
}

func (s *BoltStore) ListCapacityReservations() ([]CapacityReservation, error) {
	var result []CapacityReservation
	err := s.db.View(func(tx *bolt.Tx) error {
		return forEachCapacityReservation(tx.Bucket(capacityReservationsBucket), func(_ []byte, reservation CapacityReservation) error {
			result = append(result, reservation)
			return nil
		})
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

// capacityTxRunner opens a Bolt transaction and runs fn in it, as
// (*bolt.DB).Update and (*bolt.DB).View do.
type capacityTxRunner func(fn func(*bolt.Tx) error) error

// capacityTxValue runs fn in one transaction opened by run and returns the
// value fn produced with the transaction's error. The value is returned even
// when there is an error, as fn left it.
func capacityTxValue[T any](run capacityTxRunner, fn func(*bolt.Tx) (T, error)) (T, error) {
	var result T
	err := run(func(tx *bolt.Tx) error {
		var err error
		result, err = fn(tx)
		return err
	})
	return result, err
}

// capacityReservationTx runs fn on the reservation key for operationID in
// one transaction opened by run. An invalid operationID is
// ErrCapacityNotFound, and any error returns the zero reservation.
func capacityReservationTx(run capacityTxRunner, operationID string, fn func(bucket *bolt.Bucket, key []byte) (CapacityReservation, error)) (CapacityReservation, error) {
	if !validCapacityIdentifier(operationID, 200) {
		return CapacityReservation{}, ErrCapacityNotFound
	}
	result, err := capacityTxValue(run, func(tx *bolt.Tx) (CapacityReservation, error) {
		return fn(tx.Bucket(capacityReservationsBucket), []byte(operationID))
	})
	if err != nil {
		return CapacityReservation{}, err
	}
	return result, nil
}
