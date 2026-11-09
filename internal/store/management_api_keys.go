package store

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrAPIKeyNotFound = errors.New("management API key not found")
	ErrAPIKeyExists   = errors.New("management API key already exists")
	ErrAPIKeyInvalid  = errors.New("invalid management API key record")
	ErrAPIKeyRevoked  = errors.New("management API key revoked")
	ErrAPIKeyExpired  = errors.New("management API key expired")
	ErrAPIKeyAudience = errors.New("management API key audience mismatch")
	apiKeyIDPattern   = regexp.MustCompile(`^[a-f0-9]{32}$`)
	apiKeyNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
)

// ManagementAPIScope is an exact permission binding. Wildcard expansion is
// intentionally unsupported until a policy defines its semantics.
type ManagementAPIScope struct {
	Permission  string `json:"permission"`
	Environment string `json:"environment"`
	Resource    string `json:"resource"`
}

// ManagementAPIKeyRecord stores only a verifier for the one-time bearer
// secret. Callers must never expose Verifier in an API response or log.
type ManagementAPIKeyRecord struct {
	ID        string               `json:"id"`
	Verifier  []byte               `json:"verifier"`
	ActorID   string               `json:"actor_id"`
	Audience  string               `json:"audience"`
	Scopes    []ManagementAPIScope `json:"scopes"`
	CreatedAt time.Time            `json:"created_at"`
	ExpiresAt time.Time            `json:"expires_at"`
	CreatedBy string               `json:"created_by"`
	RevokedAt *time.Time           `json:"revoked_at,omitempty"`
	RevokedBy string               `json:"revoked_by,omitempty"`
}

// CreateManagementAPIKey persists a new verifier and exact scope set.
func (s *BoltStore) CreateManagementAPIKey(record ManagementAPIKeyRecord) error {
	record, err := normalizeManagementAPIKey(record)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode management API key: %w", err)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(managementAPIKeysBucket)
		if bucket.Get([]byte(record.ID)) != nil {
			return ErrAPIKeyExists
		}
		return bucket.Put([]byte(record.ID), encoded)
	})
}

// GetManagementAPIKey returns a stored verifier record for trusted controller
// authentication code. It is not a client-facing listing operation.
func (s *BoltStore) GetManagementAPIKey(id string) (ManagementAPIKeyRecord, error) {
	if !apiKeyIDPattern.MatchString(id) {
		return ManagementAPIKeyRecord{}, ErrAPIKeyNotFound
	}
	var record ManagementAPIKeyRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(managementAPIKeysBucket).Get([]byte(id))
		if value == nil {
			return ErrAPIKeyNotFound
		}
		if err := json.Unmarshal(value, &record); err != nil {
			return fmt.Errorf("%w: decode management API key", ErrCorruptStore)
		}
		if record.ID != id {
			return fmt.Errorf("%w: management API key index mismatch", ErrCorruptStore)
		}
		return nil
	})
	if err != nil {
		return ManagementAPIKeyRecord{}, err
	}
	return record, nil
}

// AuthenticateManagementAPIKey validates verifier, audience, expiry, and
// revocation in one read transaction. The returned record includes a verifier
// and must stay inside trusted controller code.
func (s *BoltStore) AuthenticateManagementAPIKey(id string, verifier []byte, audience string, now time.Time) (ManagementAPIKeyRecord, error) {
	if !apiKeyIDPattern.MatchString(id) || len(verifier) != 32 || strings.TrimSpace(audience) == "" || now.IsZero() {
		return ManagementAPIKeyRecord{}, ErrAPIKeyNotFound
	}
	var record ManagementAPIKeyRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		value := tx.Bucket(managementAPIKeysBucket).Get([]byte(id))
		if value == nil {
			return ErrAPIKeyNotFound
		}
		if err := json.Unmarshal(value, &record); err != nil || record.ID != id {
			return fmt.Errorf("%w: decode management API key", ErrCorruptStore)
		}
		if record.RevokedAt != nil {
			return ErrAPIKeyRevoked
		}
		if subtle.ConstantTimeCompare(record.Verifier, verifier) != 1 {
			return ErrAPIKeyNotFound
		}
		if record.Audience != audience {
			return ErrAPIKeyAudience
		}
		if !now.Before(record.ExpiresAt) {
			return ErrAPIKeyExpired
		}
		return nil
	})
	if err != nil {
		return ManagementAPIKeyRecord{}, err
	}
	return record, nil
}

// RevokeManagementAPIKey is idempotent and records the trusted revoker.
func (s *BoltStore) RevokeManagementAPIKey(id, revokedBy string, now time.Time) (ManagementAPIKeyRecord, error) {
	if !apiKeyIDPattern.MatchString(id) || strings.TrimSpace(revokedBy) == "" || len(revokedBy) > 200 || now.IsZero() {
		return ManagementAPIKeyRecord{}, ErrAPIKeyInvalid
	}
	var record ManagementAPIKeyRecord
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(managementAPIKeysBucket)
		value := bucket.Get([]byte(id))
		if value == nil {
			return ErrAPIKeyNotFound
		}
		if err := json.Unmarshal(value, &record); err != nil || record.ID != id {
			return fmt.Errorf("%w: decode management API key", ErrCorruptStore)
		}
		if record.RevokedAt != nil {
			return nil
		}
		revokedAt := now.UTC()
		record.RevokedAt = &revokedAt
		record.RevokedBy = revokedBy
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("encode revoked management API key: %w", err)
		}
		return bucket.Put([]byte(id), encoded)
	})
	if err != nil {
		return ManagementAPIKeyRecord{}, err
	}
	return record, nil
}

func normalizeManagementAPIKey(record ManagementAPIKeyRecord) (ManagementAPIKeyRecord, error) {
	if !apiKeyIDPattern.MatchString(record.ID) || len(record.Verifier) != 32 ||
		!apiKeyNamePattern.MatchString(record.ActorID) || strings.TrimSpace(record.Audience) == "" || len(record.Audience) > 200 ||
		!apiKeyNamePattern.MatchString(record.CreatedBy) || record.CreatedAt.IsZero() || record.ExpiresAt.IsZero() ||
		!record.ExpiresAt.After(record.CreatedAt) || len(record.Scopes) == 0 || len(record.Scopes) > 128 ||
		record.RevokedAt != nil || record.RevokedBy != "" {
		return ManagementAPIKeyRecord{}, ErrAPIKeyInvalid
	}
	seen := make(map[string]struct{}, len(record.Scopes))
	for _, scope := range record.Scopes {
		if !apiKeyNamePattern.MatchString(scope.Permission) || !apiKeyNamePattern.MatchString(scope.Environment) || !apiKeyNamePattern.MatchString(scope.Resource) {
			return ManagementAPIKeyRecord{}, ErrAPIKeyInvalid
		}
		key := scope.Permission + "\x00" + scope.Environment + "\x00" + scope.Resource
		if _, exists := seen[key]; exists {
			return ManagementAPIKeyRecord{}, ErrAPIKeyInvalid
		}
		seen[key] = struct{}{}
	}
	sort.Slice(record.Scopes, func(i, j int) bool {
		a, b := record.Scopes[i], record.Scopes[j]
		if a.Permission != b.Permission {
			return a.Permission < b.Permission
		}
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		return a.Resource < b.Resource
	})
	record.CreatedAt = record.CreatedAt.UTC()
	record.ExpiresAt = record.ExpiresAt.UTC()
	return record, nil
}
