package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/hosthandoff"
)

// ErrHostHandoffNotFound means a create has no single-use host key record,
// for example because it predates the handoff.
var ErrHostHandoffNotFound = errors.New("host key handoff record not found")

// HostHandoffRecord is the durable state of one instance's SSH host key
// handoff (protocol hosting section 6.1) and of the steps that follow it. It
// holds only public keys and progress, never a private key or credential.
type HostHandoffRecord struct {
	OperationID string `json:"operation_id"`
	hosthandoff.Handoff
	InstanceID string `json:"instance_id,omitempty"`
	// Progress after the host key is pinned. Each flag only ever turns on.
	BootstrapReady    bool      `json:"bootstrap_ready"`
	CredentialsSent   bool      `json:"credentials_sent"`
	SecurityFinalized bool      `json:"security_finalized"`
	RestoreStarted    bool      `json:"restore_started"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ClaimCapacityCreateWithHandoff is MarkCapacityCreateSent plus the
// handoff record, in one transaction: the reservation must still be unsent,
// and the stored single-use public key is the one the caller is about to
// send. A concurrent caller that loses the claim cannot overwrite it.
func (s *BoltStore) ClaimCapacityCreateWithHandoff(operationID string, handoff hosthandoff.Handoff) (CapacityReservation, error) {
	if !validCapacityIdentifier(operationID, 200) {
		return CapacityReservation{}, ErrCapacityNotFound
	}
	if err := handoff.Validate(); err != nil || handoff.State != hosthandoff.PendingRotation {
		return CapacityReservation{}, fmt.Errorf("%w: invalid host key handoff", ErrInvalidRecord)
	}
	var result CapacityReservation
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(capacityReservationsBucket)
		value := bucket.Get([]byte(operationID))
		if value == nil {
			return ErrCapacityNotFound
		}
		current, err := decodeCapacityReservation(value)
		if err != nil {
			return err
		}
		if current.State != CapacityReserved {
			return ErrCapacityTransition
		}
		current.State = CapacityCreateOutcomeUnknown
		current.UpdatedAt = time.Now().UTC()
		if current.UpdatedAt.Before(current.CreatedAt) {
			current.UpdatedAt = current.CreatedAt
		}
		if err := validatePersistedCapacityReservation(current); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidRecord, err)
		}
		encoded, err := json.Marshal(current)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(operationID), encoded); err != nil {
			return err
		}
		record := HostHandoffRecord{OperationID: operationID, Handoff: handoff, UpdatedAt: current.UpdatedAt}
		if err := putHostHandoff(tx, record); err != nil {
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

// GetHostHandoff reads one handoff record.
func (s *BoltStore) GetHostHandoff(operationID string) (HostHandoffRecord, error) {
	var record HostHandoffRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(hostHandoffsBucket).Get([]byte(operationID))
		if value == nil {
			return ErrHostHandoffNotFound
		}
		var err error
		record, err = decodeHostHandoff(value)
		return err
	})
	return record, err
}

// UpdateHostHandoff applies change to one record. The single-use key, its
// times, the operation, and a recorded instance ID never change; the state
// only moves from pending_rotation to rotated; a pinned key never changes;
// and progress flags never turn off.
func (s *BoltStore) UpdateHostHandoff(operationID string, change func(*HostHandoffRecord) error) (HostHandoffRecord, error) {
	var result HostHandoffRecord
	err := s.db.Update(func(tx *bolt.Tx) error {
		value := tx.Bucket(hostHandoffsBucket).Get([]byte(operationID))
		if value == nil {
			return ErrHostHandoffNotFound
		}
		current, err := decodeHostHandoff(value)
		if err != nil {
			return err
		}
		next := current
		if err := change(&next); err != nil {
			return err
		}
		// Check for forbidden changes to immutable fields
		if err := checkImmutableFields(current, next); err != nil {
			return err
		}
		// Validate state transition and progress flag monotonicity
		if err := validateStateAndProgress(current, next); err != nil {
			return err
		}
		next.UpdatedAt = time.Now().UTC()
		if err := putHostHandoff(tx, next); err != nil {
			return err
		}
		result = next
		return nil
	})
	return result, err
}

// checkImmutableFields validates that the operation ID, host key, times, and instance ID never change.
func checkImmutableFields(current, next HostHandoffRecord) error {
	if next.OperationID != current.OperationID {
		return fmt.Errorf("%w: operation ID cannot change", ErrInvalidRecord)
	}
	if next.OneTimeHostKey != current.OneTimeHostKey {
		return fmt.Errorf("%w: one-time host key cannot change", ErrInvalidRecord)
	}
	if !next.CreatedAt.Equal(current.CreatedAt) || !next.ExpiresAt.Equal(current.ExpiresAt) {
		return fmt.Errorf("%w: host key timestamps cannot change", ErrInvalidRecord)
	}
	if current.InstanceID != "" && next.InstanceID != current.InstanceID {
		return fmt.Errorf("%w: instance ID once recorded cannot change", ErrInvalidRecord)
	}
	if current.State == hosthandoff.Rotated && (next.State != hosthandoff.Rotated || next.PinnedHostKey != current.PinnedHostKey) {
		return fmt.Errorf("%w: when rotated, state and pinned key cannot change", ErrInvalidRecord)
	}
	return nil
}

// validateStateAndProgress ensures progress flags only turn on once and credentials can't be sent before rotation.
func validateStateAndProgress(current, next HostHandoffRecord) error {
	if current.UserDataReplaced && !next.UserDataReplaced {
		return fmt.Errorf("%w: userdata replaced flag never turns off", ErrInvalidRecord)
	}
	if current.BootstrapReady && !next.BootstrapReady {
		return fmt.Errorf("%w: bootstrap ready flag never turns off", ErrInvalidRecord)
	}
	if current.CredentialsSent && !next.CredentialsSent {
		return fmt.Errorf("%w: credentials sent flag never turns off", ErrInvalidRecord)
	}
	if current.SecurityFinalized && !next.SecurityFinalized {
		return fmt.Errorf("%w: security finalized flag never turns off", ErrInvalidRecord)
	}
	if current.RestoreStarted && !next.RestoreStarted {
		return fmt.Errorf("%w: restore started flag never turns off", ErrInvalidRecord)
	}
	if next.CredentialsSent && next.State != hosthandoff.Rotated {
		return fmt.Errorf("%w: credentials recorded as sent before the host key was pinned", ErrInvalidRecord)
	}
	return nil
}

func putHostHandoff(tx *bolt.Tx, record HostHandoffRecord) error {
	if !validCapacityIdentifier(record.OperationID, 200) || record.Handoff.Validate() != nil ||
		(record.InstanceID != "" && !validCapacityIdentifier(record.InstanceID, 200)) {
		return fmt.Errorf("%w: invalid host key handoff record", ErrInvalidRecord)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return tx.Bucket(hostHandoffsBucket).Put([]byte(record.OperationID), encoded)
}

func decodeHostHandoff(value []byte) (HostHandoffRecord, error) {
	var record HostHandoffRecord
	if err := json.Unmarshal(value, &record); err != nil || record.Handoff.Validate() != nil {
		return HostHandoffRecord{}, fmt.Errorf("%w: invalid host key handoff record", ErrCorruptStore)
	}
	return record, nil
}
