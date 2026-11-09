// Package authkey issues and validates opaque, audience-bound controller API
// keys. Keys identify actors; authorization remains a separate core decision.
package authkey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/store"
)

const tokenPrefix = "gsc1_"

var ErrUnauthenticated = errors.New("unauthenticated")

type keyStore interface {
	CreateManagementAPIKey(store.ManagementAPIKeyRecord) error
	AuthenticateManagementAPIKey(string, []byte, string, time.Time) (store.ManagementAPIKeyRecord, error)
	RevokeManagementAPIKey(string, string, time.Time) (store.ManagementAPIKeyRecord, error)
}

type Manager struct {
	store keyStore
}

type Scope = store.ManagementAPIScope

type IssueRequest struct {
	ActorID   string
	Audience  string
	Scopes    []Scope
	CreatedBy string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// IssuedKey returns the bearer value exactly once, alongside metadata safe to
// display. Callers must not persist or log Secret.
type IssuedKey struct {
	ID        string
	Secret    string `json:"-"`
	ActorID   string
	Audience  string
	Scopes    []Scope
	CreatedAt time.Time
	ExpiresAt time.Time
}

func (IssuedKey) String() string   { return "IssuedKey{Secret:[redacted]}" }
func (IssuedKey) GoString() string { return "IssuedKey{Secret:[redacted]}" }

type Principal struct {
	KeyID     string
	ActorID   string
	Audience  string
	Scopes    []Scope
	ExpiresAt time.Time
}

func NewManager(keyStore keyStore) (*Manager, error) {
	if keyStore == nil {
		return nil, errors.New("management API key store is required")
	}
	return &Manager{store: keyStore}, nil
}

// Issue creates a random 256-bit bearer secret and stores only its SHA-256
// verifier. CreatedAt and ExpiresAt must be supplied by trusted controller
// code; no client timestamp is used as authority.
func (m *Manager) Issue(request IssueRequest) (IssuedKey, error) {
	if m == nil || m.store == nil || request.CreatedAt.IsZero() || !request.ExpiresAt.After(request.CreatedAt) {
		return IssuedKey{}, errors.New("valid API key manager and expiry are required")
	}
	var idBytes [16]byte
	var secretBytes [32]byte
	if _, err := rand.Read(idBytes[:]); err != nil {
		return IssuedKey{}, errors.New("generate API key identity failed")
	}
	if _, err := rand.Read(secretBytes[:]); err != nil {
		return IssuedKey{}, errors.New("generate API key secret failed")
	}
	defer zero(secretBytes[:])
	id := hex.EncodeToString(idBytes[:])
	secret := base64.RawURLEncoding.EncodeToString(secretBytes[:])
	verifier := sha256.Sum256(secretBytes[:])
	record := store.ManagementAPIKeyRecord{
		ID: id, Verifier: verifier[:], ActorID: request.ActorID, Audience: request.Audience,
		Scopes: append([]Scope(nil), request.Scopes...), CreatedAt: request.CreatedAt.UTC(),
		ExpiresAt: request.ExpiresAt.UTC(), CreatedBy: request.CreatedBy,
	}
	if err := m.store.CreateManagementAPIKey(record); err != nil {
		return IssuedKey{}, fmt.Errorf("store management API key: %w", err)
	}
	return IssuedKey{ID: id, Secret: tokenPrefix + id + "_" + secret, ActorID: request.ActorID,
		Audience: request.Audience, Scopes: append([]Scope(nil), request.Scopes...),
		CreatedAt: request.CreatedAt.UTC(), ExpiresAt: request.ExpiresAt.UTC()}, nil
}

// Authenticate establishes only the trusted actor identity. It does not grant
// any scope automatically; callers must ask the authorization policy.
func (m *Manager) Authenticate(token, audience string, now time.Time) (Principal, error) {
	if m == nil || m.store == nil {
		return Principal{}, ErrUnauthenticated
	}
	id, secret, ok := parseToken(token)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	verifier := sha256.Sum256(secret)
	zero(secret)
	record, err := m.store.AuthenticateManagementAPIKey(id, verifier[:], audience, now.UTC())
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{KeyID: record.ID, ActorID: record.ActorID, Audience: record.Audience,
		Scopes: append([]Scope(nil), record.Scopes...), ExpiresAt: record.ExpiresAt}, nil
}

func (m *Manager) Revoke(id, revokedBy string, now time.Time) error {
	if m == nil || m.store == nil {
		return errors.New("management API key store is unavailable")
	}
	_, err := m.store.RevokeManagementAPIKey(id, revokedBy, now.UTC())
	if err != nil {
		return fmt.Errorf("revoke management API key: %w", err)
	}
	return nil
}

func parseToken(token string) (string, []byte, bool) {
	if len(token) > 256 || !strings.HasPrefix(token, tokenPrefix) {
		return "", nil, false
	}
	parts := strings.SplitN(strings.TrimPrefix(token, tokenPrefix), "_", 2)
	if len(parts) != 2 || len(parts[0]) != 32 {
		return "", nil, false
	}
	idBytes, err := hex.DecodeString(parts[0])
	if err != nil || len(idBytes) != 16 {
		return "", nil, false
	}
	secret, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(secret) != 32 {
		zero(secret)
		return "", nil, false
	}
	return parts[0], secret, true
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
