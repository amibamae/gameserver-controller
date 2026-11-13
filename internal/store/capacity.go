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
		var err error
		result, created, err = reserveCapacityTx(tx, reservation, totalLimit, groupLimit)
		return err
	})
	if err != nil {
		return CapacityReservation{}, false, err
	}
	return result, created, nil
}

func reserveCapacityTx(tx *bolt.Tx, reservation CapacityReservation, totalLimit, groupLimit int) (CapacityReservation, bool, error) {
	bucket := tx.Bucket(capacityReservationsBucket)
	if existingValue := bucket.Get([]byte(reservation.OperationID)); existingValue != nil {
		existing, err := decodeCapacityReservation(existingValue)
		if err != nil {
			return CapacityReservation{}, false, err
		}
		if !sameCapacityRequest(existing, reservation) {
			return CapacityReservation{}, false, ErrCapacityConflict
		}
		return existing, false, nil
	}

	total, groupCount := 0, 0
	cursor := bucket.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		existing, err := decodeCapacityReservation(value)
		if err != nil {
			return CapacityReservation{}, false, err
		}
		if string(key) != existing.OperationID {
			return CapacityReservation{}, false, fmt.Errorf("%w: capacity reservation key mismatch", ErrCorruptStore)
		}
		if capacityStateReleased(existing.State) {
			continue
		}
		if existing.ServerID == reservation.ServerID && existing.EnvironmentID == reservation.EnvironmentID {
			return CapacityReservation{}, false, fmt.Errorf("%w: server %q in environment %q", ErrCapacityOccupied, reservation.ServerID, reservation.EnvironmentID)
		}
		if existing.Provider == reservation.Provider && existing.ResourceKey == reservation.ResourceKey {
			return CapacityReservation{}, false, fmt.Errorf("%w: provider resource key %q is already reserved", ErrCapacityConflict, reservation.ResourceKey)
		}
		total++
		if existing.CapacityGroup == reservation.CapacityGroup {
			groupCount++
		}
	}
	if total >= totalLimit {
		return CapacityReservation{}, false, ErrCapacityReached
	}
	if groupCount >= groupLimit {
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

// MarkCapacityCreateSent is a one-time dispatch claim. Callers must claim it
// immediately before sending the provider create request; a repeated claim
// fails so a retry cannot accidentally dispatch the same create again. The
// outcome remains counted until reconciled.
func (s *BoltStore) MarkCapacityCreateSent(operationID string) (CapacityReservation, error) {
	return s.updateCapacity(operationID, func(current CapacityReservation) (CapacityReservation, error) {
		if current.State != CapacityReserved {
			return CapacityReservation{}, ErrCapacityTransition
		}
		current.State = CapacityCreateOutcomeUnknown
		return current, nil
	})
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
	return s.updateCapacity(operationID, func(current CapacityReservation) (CapacityReservation, error) {
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
	if err := validateCapacityObservation(observation); err != nil {
		return CapacityReservation{}, err
	}
	return s.updateCapacity(operationID, func(current CapacityReservation) (CapacityReservation, error) {
		return applyCapacityObservation(current, observation)
	})
}

func validateCapacityObservation(observation CapacityProviderObservation) error {
	if !validCapacityIdentifier(observation.Provider, 100) || !validCapacityIdentifier(observation.ResourceKey, 240) ||
		observation.ObservedAt.IsZero() || observation.ObservedAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return fmt.Errorf("%w: invalid provider observation", ErrInvalidRecord)
	}
	switch observation.State {
	case "present", "absent", "destroyed":
	default:
		return fmt.Errorf("%w: unsupported provider observation state", ErrInvalidRecord)
	}
	if observation.State == "present" && !validCapacityIdentifier(observation.ProviderInstanceID, 240) {
		return fmt.Errorf("%w: present observation requires provider_instance_id", ErrInvalidRecord)
	}
	if !observation.ProvisionedAt.IsZero() && (observation.State != "present" || observation.ProvisionedAt.After(observation.ObservedAt)) {
		return fmt.Errorf("%w: invalid provider provisioning timestamp", ErrInvalidRecord)
	}
	if observation.ProviderInstanceID != "" && !validCapacityIdentifier(observation.ProviderInstanceID, 240) {
		return fmt.Errorf("%w: invalid provider_instance_id", ErrInvalidRecord)
	}
	return nil
}

func applyCapacityObservation(current CapacityReservation, observation CapacityProviderObservation) (CapacityReservation, error) {
	if current.Provider != observation.Provider || current.ResourceKey != observation.ResourceKey {
		return CapacityReservation{}, ErrCapacityConflict
	}
	if observation.ObservedAt.Before(current.CreatedAt) {
		return CapacityReservation{}, ErrCapacityConflict
	}
	if !observation.ProvisionedAt.IsZero() && observation.ProvisionedAt.Before(current.CreatedAt) {
		return CapacityReservation{}, fmt.Errorf("%w: provider provisioning time precedes reservation", ErrInvalidRecord)
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
	return current, nil
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

// RecordCapacityReady records the first exact Paper readiness timestamp for a
// controller-owned provider instance. Adopted or unmanaged instances have no
// capacity reservation and therefore do not create local boot samples.
func (s *BoltStore) RecordCapacityReady(serverID, environmentID, providerInstanceID string, readyAt time.Time) (CapacityReservation, error) {
	if !validCapacityIdentifier(serverID, 200) || !validCapacityIdentifier(environmentID, 200) ||
		!validCapacityIdentifier(providerInstanceID, 240) || readyAt.IsZero() || readyAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return CapacityReservation{}, fmt.Errorf("%w: invalid readiness measurement", ErrInvalidRecord)
	}
	var result CapacityReservation
	err := s.db.Update(func(tx *bolt.Tx) error {
		var err error
		result, err = recordCapacityReadyTx(tx, serverID, environmentID, providerInstanceID, "", readyAt)
		return err
	})
	return result, err
}

func recordCapacityReadyTx(tx *bolt.Tx, serverID, environmentID, providerInstanceID, adapterID string, readyAt time.Time) (CapacityReservation, error) {
	var result CapacityReservation
	err := func() error {
		bucket := tx.Bucket(capacityReservationsBucket)
		cursor := bucket.Cursor()
		found := false
		var matchedKey []byte
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			current, err := decodeCapacityReservation(value)
			if err != nil {
				return err
			}
			if string(key) != current.OperationID {
				return fmt.Errorf("%w: capacity reservation key mismatch", ErrCorruptStore)
			}
			if current.ServerID != serverID || current.EnvironmentID != environmentID || current.ProviderInstanceID != providerInstanceID || (adapterID != "" && current.ProviderAdapterID != adapterID) {
				continue
			}
			if found {
				return ErrCapacityConflict
			}
			found = true
			matchedKey = append([]byte(nil), key...)
			result = current
		}
		if !found {
			return ErrCapacityNotFound
		}
		if result.Adoption != nil || result.State != CapacityActive || result.LastObservation == nil || result.LastObservation.State != "present" {
			return ErrCapacityTransition
		}
		if readyAt.Before(result.CreatedAt) || (!result.ProvisionedAt.IsZero() && readyAt.Before(result.ProvisionedAt)) {
			return ErrCapacityTransition
		}
		if !result.ReadyAt.IsZero() {
			if !result.ReadyAt.Equal(readyAt) {
				return ErrCapacityConflict
			}
			return nil
		}
		result.ReadyAt = readyAt.UTC()
		result.UpdatedAt = capacityUpdateTime(result.ReadyAt)
		if err := validatePersistedCapacityReservation(result); err != nil {
			return fmt.Errorf("%w: invalid readiness measurement: %v", ErrInvalidRecord, err)
		}
		return putCapacityReservation(bucket, matchedKey, result, "capacity readiness measurement")
	}()
	return result, err
}

// recordCapacityLifecycleOutcomeTx ties boot samples and confirmed destruction
// releases to the corresponding committed lifecycle result. Missing
// reservations and already-recorded first boots are normal for unmanaged
// servers and subsequent starts.
func recordCapacityLifecycleOutcomeTx(tx *bolt.Tx, operation OperationRecord, server LogicalServer) error {
	if operation.OperationType == "server.destroy" && operation.Status == "succeeded" && server.LifecycleState == "destroyed" {
		return recordDestroyedCapacityTx(tx, operation, server)
	}
	if operation.OperationType != "server.start" || operation.Status != "succeeded" || server.LifecycleState != "ready" {
		return nil
	}
	var result struct {
		Data struct {
			Assertion struct {
				ServerID      string          `json:"server_id"`
				EnvironmentID string          `json:"environment_id"`
				InstanceRef   json.RawMessage `json:"instance_ref"`
				ObservedAt    time.Time       `json:"observed_at"`
			} `json:"readiness_assertion"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(operation.Result, &result) != nil {
		return nil
	}
	assertion := result.Data.Assertion
	if assertion.ServerID != server.ServerID || assertion.EnvironmentID != server.EnvironmentID ||
		!equalCanonicalJSON(assertion.InstanceRef, server.InstanceRef) || assertion.ObservedAt.IsZero() {
		return nil
	}
	var instance struct {
		ExternalID string `json:"external_id"`
		AdapterID  string `json:"adapter_id"`
	}
	if json.Unmarshal(server.InstanceRef, &instance) != nil || instance.ExternalID == "" || instance.AdapterID == "" {
		return nil
	}
	_, err := recordCapacityReadyTx(tx, server.ServerID, server.EnvironmentID, instance.ExternalID, instance.AdapterID, assertion.ObservedAt)
	if errors.Is(err, ErrCapacityNotFound) || errors.Is(err, ErrCapacityConflict) || errors.Is(err, ErrCapacityTransition) {
		return nil
	}
	return err
}

// recordDestroyedCapacityTx releases a managed VM slot in the same Bolt
// transaction that commits the successful server.destroy result and logical
// server transition. The provider's exact final absence step is the evidence
// used for the capacity observation; accepted or ambiguous destroys never
// reach this path.
func recordDestroyedCapacityTx(tx *bolt.Tx, operation OperationRecord, server LogicalServer) error {
	var result struct {
		OperationType string `json:"operation_type"`
		OperationData struct {
			InstanceRef         json.RawMessage `json:"instance_ref"`
			VerifiedBackupID    string          `json:"verified_backup_id"`
			DataGenerationID    string          `json:"data_generation_id"`
			ProviderAbsence     bool            `json:"provider_absence_confirmed"`
			FinalLifecycleState string          `json:"final_lifecycle_state"`
		} `json:"operation_data"`
	}
	if json.Unmarshal(operation.Result, &result) != nil || result.OperationType != "server.destroy" ||
		!equalCanonicalJSON(result.OperationData.InstanceRef, operation.Target.InstanceRef) ||
		result.OperationData.VerifiedBackupID == "" || result.OperationData.DataGenerationID != server.DataGenerationID ||
		!result.OperationData.ProviderAbsence || result.OperationData.FinalLifecycleState != "destroyed" {
		return fmt.Errorf("%w: server.destroy success lacks exact absence and backup evidence", ErrInvalidRecord)
	}
	var absence struct {
		InstanceRef json.RawMessage `json:"instance_ref"`
		State       string          `json:"state"`
		ObservedAt  time.Time       `json:"observed_at"`
	}
	var absenceStep *OperationStep
	for i := range operation.Steps {
		if operation.Steps[i].StepID == "verify-instance-absence" {
			absenceStep = &operation.Steps[i]
			break
		}
	}
	if absenceStep == nil || absenceStep.Status != "succeeded" || json.Unmarshal(absenceStep.Output, &absence) != nil ||
		!equalCanonicalJSON(absence.InstanceRef, operation.Target.InstanceRef) || absence.State != "destroyed" || absence.ObservedAt.IsZero() {
		return fmt.Errorf("%w: server.destroy success lacks exact provider absence observation", ErrInvalidRecord)
	}
	var instance struct {
		AdapterID    string `json:"adapter_id"`
		ResourceKind string `json:"resource_kind"`
		ExternalID   string `json:"external_id"`
	}
	if json.Unmarshal(operation.Target.InstanceRef, &instance) != nil || instance.AdapterID == "" || instance.ResourceKind != "instance" || instance.ExternalID == "" {
		return fmt.Errorf("%w: server.destroy target is not an exact provider instance", ErrInvalidRecord)
	}
	if absence.ObservedAt.After(time.Now().UTC().Add(5 * time.Minute)) {
		return fmt.Errorf("%w: provider absence observation is too far in the future", ErrInvalidRecord)
	}
	var match *CapacityReservation
	cursor := tx.Bucket(capacityReservationsBucket).Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		reservation, err := decodeCapacityReservation(value)
		if err != nil {
			return err
		}
		if string(key) != reservation.OperationID {
			return fmt.Errorf("%w: capacity reservation key mismatch", ErrCorruptStore)
		}
		if reservation.ServerID != server.ServerID || reservation.EnvironmentID != server.EnvironmentID {
			continue
		}
		if reservation.State == CapacityAbsent || reservation.State == CapacityDestroyed {
			continue
		}
		if (reservation.ProviderAdapterID != "" && reservation.ProviderAdapterID != instance.AdapterID) || reservation.ProviderInstanceID != instance.ExternalID {
			return fmt.Errorf("%w: managed VM reservation does not match the destroyed instance", ErrCapacityConflict)
		}
		if match != nil {
			return fmt.Errorf("%w: multiple active managed VM reservations match one destroyed server", ErrCapacityConflict)
		}
		copyReservation := reservation
		match = &copyReservation
	}
	if match == nil {
		return nil
	}
	if match.State != CapacityActive && match.State != CapacityDestroyOutcomeUnknown {
		return fmt.Errorf("%w: cannot release managed VM capacity from state %q", ErrCapacityTransition, match.State)
	}
	return observeCapacityTx(tx, match.OperationID, CapacityProviderObservation{
		Provider: match.Provider, ResourceKey: match.ResourceKey, ProviderInstanceID: instance.ExternalID,
		State: "destroyed", ObservedAt: absence.ObservedAt,
	})
}

func (s *BoltStore) GetCapacityReservation(operationID string) (CapacityReservation, error) {
	if !validCapacityIdentifier(operationID, 200) {
		return CapacityReservation{}, ErrCapacityNotFound
	}
	var result CapacityReservation
	err := s.db.View(func(tx *bolt.Tx) error {
		var err error
		result, err = loadCapacityReservation(tx.Bucket(capacityReservationsBucket), []byte(operationID))
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
		current, err := loadCapacityReservation(bucket, key)
		if err != nil {
			return err
		}
		next, err := transition(current)
		if err != nil {
			return err
		}
		if next.OperationID != current.OperationID || next.CreatedAt != current.CreatedAt || next.ProviderAdapterID != current.ProviderAdapterID ||
			(!current.ProvisionedAt.IsZero() && !next.ProvisionedAt.Equal(current.ProvisionedAt)) ||
			(!current.ReadyAt.IsZero() && !next.ReadyAt.Equal(current.ReadyAt)) {
			return fmt.Errorf("%w: immutable capacity reservation fields changed", ErrCorruptStore)
		}
		if capacityTrackedFieldsChanged(current, next) {
			next.UpdatedAt = capacityUpdateTime(capacityObservedAt(next.LastObservation), next.ProvisionedAt, next.ReadyAt)
			if err := putUpdatedCapacityReservation(bucket, key, next); err != nil {
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

// capacityTrackedFieldsChanged reports whether a transition changed any field
// whose change must be persisted with a new UpdatedAt.
func capacityTrackedFieldsChanged(current, next CapacityReservation) bool {
	return next.State != current.State || next.ProviderInstanceID != current.ProviderInstanceID || next.LastObservation != current.LastObservation ||
		!next.ProvisionedAt.Equal(current.ProvisionedAt) || !next.ReadyAt.Equal(current.ReadyAt)
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

func validateCapacityReservationRequest(reservation CapacityReservation) error {
	if reservation.ProviderAdapterID != "" && !validCapacityIdentifier(reservation.ProviderAdapterID, 160) {
		return ErrInvalidRecord
	}
	if !validCapacityIdentifier(reservation.OperationID, 200) || !validCapacityIdentifier(reservation.ServerID, 200) ||
		!validCapacityIdentifier(reservation.EnvironmentID, 200) || !validCapacityIdentifier(reservation.CapacityGroup, 100) ||
		!validCapacityIdentifier(reservation.Provider, 100) || !validCapacityIdentifier(reservation.ResourceKey, 240) ||
		reservation.ProviderInstanceID != "" || reservation.State != "" || !reservation.CreatedAt.IsZero() || !reservation.UpdatedAt.IsZero() ||
		!reservation.ProvisionedAt.IsZero() || !reservation.ReadyAt.IsZero() || reservation.LastObservation != nil || reservation.Adoption != nil {
		return fmt.Errorf("%w: invalid capacity reservation request", ErrInvalidRecord)
	}
	return nil
}

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
	return validateCapacityAdoption(*r.Adoption) == nil && r.ProviderInstanceID == r.Adoption.InstanceID &&
		r.CreatedAt.Equal(r.Adoption.ObservedAt) && r.LastObservation != nil &&
		r.ProvisionedAt.IsZero() && r.ReadyAt.IsZero()
}

// capacityIdentityValid requires every identifier to be present and bounded
// (the provider adapter ID is optional) and the record to have a creation
// time no later than its update time.
func capacityIdentityValid(r CapacityReservation) bool {
	if r.ProviderAdapterID != "" && !validCapacityIdentifier(r.ProviderAdapterID, 160) {
		return false
	}
	return validCapacityIdentifier(r.OperationID, 200) && validCapacityIdentifier(r.ServerID, 200) &&
		validCapacityIdentifier(r.EnvironmentID, 200) && validCapacityIdentifier(r.CapacityGroup, 100) &&
		validCapacityIdentifier(r.Provider, 100) && validCapacityIdentifier(r.ResourceKey, 240) &&
		!r.CreatedAt.IsZero() && !r.UpdatedAt.Before(r.CreatedAt)
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
	if !r.ProvisionedAt.IsZero() && (r.ProvisionedAt.Before(r.CreatedAt) || r.ProvisionedAt.After(r.UpdatedAt)) {
		return false
	}
	return r.ReadyAt.IsZero() || !(r.ReadyAt.Before(r.CreatedAt) ||
		(!r.ProvisionedAt.IsZero() && r.ReadyAt.Before(r.ProvisionedAt)) || r.ReadyAt.After(r.UpdatedAt))
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
	if observation.Provider != r.Provider || observation.ResourceKey != r.ResourceKey || observation.ObservedAt.Before(r.CreatedAt) ||
		(observation.State == "present" && !validCapacityIdentifier(observation.ProviderInstanceID, 240)) {
		return false
	}
	if observation.ProviderInstanceID != "" && (!validCapacityIdentifier(observation.ProviderInstanceID, 240) ||
		(r.ProviderInstanceID != "" && r.ProviderInstanceID != observation.ProviderInstanceID)) {
		return false
	}
	if !observation.ProvisionedAt.IsZero() && (observation.State != "present" || observation.ProvisionedAt.After(observation.ObservedAt)) {
		return false
	}
	switch observation.State {
	case "present", "absent", "destroyed":
	default:
		return false
	}
	return !((r.State == CapacityAbsent && observation.State != "absent") || (r.State == CapacityDestroyed && observation.State != "destroyed") ||
		(r.State == CapacityActive && observation.State != "present"))
}

func sameCapacityRequest(existing, requested CapacityReservation) bool {
	return existing.OperationID == requested.OperationID && existing.ServerID == requested.ServerID &&
		existing.EnvironmentID == requested.EnvironmentID && existing.CapacityGroup == requested.CapacityGroup &&
		existing.Provider == requested.Provider && existing.ProviderAdapterID == requested.ProviderAdapterID && existing.ResourceKey == requested.ResourceKey
}

func capacityStateReleased(state CapacityState) bool {
	return state == CapacityAbsent || state == CapacityDestroyed
}

func sameCapacityObservation(left, right CapacityProviderObservation) bool {
	return left.Provider == right.Provider && left.ResourceKey == right.ResourceKey &&
		left.ProviderInstanceID == right.ProviderInstanceID && left.State == right.State &&
		left.ObservedAt.Equal(right.ObservedAt) && left.ProvisionedAt.Equal(right.ProvisionedAt)
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
