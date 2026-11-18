package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var (
	ErrIdempotencyConflict = errors.New("idempotency key was reused with different request data")
	ErrIdempotencyExpired  = errors.New("idempotency key is retained but its operation record has expired")
	ErrResourceConflict    = errors.New("resource is locked by another operation")
	ErrKeyVersionMissing   = errors.New("an idempotency verifier key version is unavailable")
	ErrStoreNeedsMigration = errors.New("operation store contains legacy records requiring migration")
)

type IdempotencyVerifier struct {
	KeyVersion    string
	Digest        []byte
	BindingDigest []byte
}

type IdempotencyProof struct {
	ActiveKeyVersion string
	Candidates       []IdempotencyVerifier
}

type ResourceLock struct {
	Scope       string    `json:"scope"`
	OperationID string    `json:"operation_id"`
	AcquiredAt  time.Time `json:"acquired_at"`
}

type idempotencyEntry struct {
	OperationID   string    `json:"operation_id"`
	BindingDigest []byte    `json:"binding_digest"`
	KeyVersion    string    `json:"key_version"`
	RecordedAt    time.Time `json:"recorded_at"`
}

// ValidOperationID returns true if the operation ID is valid.
func (entry idempotencyEntry) ValidOperationID() bool {
	return validIdentifier(entry.OperationID, 200)
}

// ValidBindingDigest returns true if the binding digest has the correct length.
func (entry idempotencyEntry) ValidBindingDigest() bool {
	return len(entry.BindingDigest) == 32
}

// ValidKeyVersion returns true if the key version is valid.
func (entry idempotencyEntry) ValidKeyVersion() bool {
	return validKeyVersion(entry.KeyVersion)
}

// ValidRecordedAt returns true if the recorded timestamp is set.
func (entry idempotencyEntry) ValidRecordedAt() bool {
	return !entry.RecordedAt.IsZero()
}

// valid combines all field validations for an idempotency entry.
func (entry idempotencyEntry) valid() bool {
	return entry.ValidOperationID() && entry.ValidBindingDigest() &&
		entry.ValidKeyVersion() && entry.ValidRecordedAt()
}

// ValidScope returns true if the scope is non-empty and within limits.
func (lock ResourceLock) ValidScope() bool {
	return len(lock.Scope) > 0 && len(lock.Scope) <= 512
}

// ValidOperationID returns true if the operation ID is set.
func (lock ResourceLock) ValidOperationID() bool {
	return lock.OperationID != ""
}

// ValidAcquiredAt returns true if the acquisition timestamp is set.
func (lock ResourceLock) ValidAcquiredAt() bool {
	return !lock.AcquiredAt.IsZero()
}

// decodeResourceLock unmarshals and validates a resource lock.
func decodeResourceLock(value []byte) (ResourceLock, error) {
	var lock ResourceLock
	if err := json.Unmarshal(value, &lock); err != nil {
		return ResourceLock{}, fmt.Errorf("%w: failed to unmarshal resource lock", ErrCorruptStore)
	}
	if !lock.ValidScope() || !lock.ValidOperationID() || !lock.ValidAcquiredAt() {
		return ResourceLock{}, fmt.Errorf("%w: malformed resource lock", ErrCorruptStore)
	}
	return lock, nil
}

// HasActiveKeyVersion returns true if the proof has a valid active key version.
func (proof IdempotencyProof) HasActiveKeyVersion() bool {
	return validKeyVersion(proof.ActiveKeyVersion)
}

// HasCandidates returns true if there are verifier candidates.
func (proof IdempotencyProof) HasCandidates() bool {
	return len(proof.Candidates) > 0
}

// ValidVerifier returns true if a candidate verifier has valid fields.
func verifierValid(candidate IdempotencyVerifier) bool {
	return validKeyVersion(candidate.KeyVersion) && len(candidate.Digest) == 32 && len(candidate.BindingDigest) == 32
}

// HasNoDuplicateVersions returns true if no key version appears twice.
func (proof IdempotencyProof) HasNoDuplicateVersions() bool {
	seen := make(map[string]struct{}, len(proof.Candidates))
	for _, candidate := range proof.Candidates {
		if _, ok := seen[candidate.KeyVersion]; ok {
			return false
		}
		seen[candidate.KeyVersion] = struct{}{}
	}
	return true
}

// HasActiveVerifier returns true if a verifier with the active key version is present.
func (proof IdempotencyProof) HasActiveVerifier() bool {
	for _, candidate := range proof.Candidates {
		if candidate.KeyVersion == proof.ActiveKeyVersion {
			return true
		}
	}
	return false
}

// validateIdempotencyProof validates the idempotency proof structure.
func validateIdempotencyProof(proof IdempotencyProof) error {
	if !proof.HasActiveKeyVersion() || !proof.HasCandidates() {
		return fmt.Errorf("%w: missing active key version or verifier candidates", ErrInvalidRecord)
	}
	for _, candidate := range proof.Candidates {
		if !verifierValid(candidate) {
			return fmt.Errorf("%w: invalid idempotency verifier", ErrInvalidRecord)
		}
	}
	if !proof.HasNoDuplicateVersions() {
		return fmt.Errorf("%w: duplicate idempotency key version", ErrInvalidRecord)
	}
	if !proof.HasActiveVerifier() {
		return fmt.Errorf("%w: active key verifier is missing", ErrInvalidRecord)
	}
	return nil
}

// validKeyVersion returns true if the version string is non-empty and within size limits.
func validKeyVersion(version string) bool {
	if len(version) == 0 || len(version) > 64 {
		return false
	}
	return strings.IndexFunc(version, invalidKeyVersionRune) == -1
}

// invalidKeyVersionRune returns true for characters not allowed in key versions.
func invalidKeyVersionRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return false
	case r == '.', r == '_', r == '-':
		return false
	default:
		return true
	}
}

// verifierIndex creates a hash-based key for looking up an idempotency entry.
func verifierIndex(verifier IdempotencyVerifier) []byte {
	digest := sha256.Sum256(verifier.Digest)
	return append([]byte{byte(len(verifier.Digest))}, digest[:]...)
}

// findVerifier locates a verifier candidate by key version, if present.
func findVerifier(proof IdempotencyProof, version string) (IdempotencyVerifier, bool) {
	for _, candidate := range proof.Candidates {
		if candidate.KeyVersion == version {
			return candidate, true
		}
	}
	return IdempotencyVerifier{}, false
}

// requestMatches returns true if the entry and verifier have compatible bindings.
func requestMatches(entry idempotencyEntry, candidate IdempotencyVerifier) bool {
	if !entry.ValidBindingDigest() || !validKeyVersion(candidate.KeyVersion) {
		return false
	}
	return subtle.ConstantTimeCompare(entry.BindingDigest, candidate.Digest) == 1
}

// decodeIdempotencyEntry unmarshals and validates an idempotency entry.
func decodeIdempotencyEntry(value []byte) (idempotencyEntry, error) {
	var entry idempotencyEntry
	if err := json.Unmarshal(value, &entry); err != nil || !entry.valid() {
		return idempotencyEntry{}, fmt.Errorf("%w: malformed idempotency entry", ErrCorruptStore)
	}
	return entry, nil
}

// encodeIdempotencyEntry marshals and validates an entry.
func encodeIdempotencyEntry(entry idempotencyEntry) ([]byte, error) {
	if !entry.valid() {
		return nil, fmt.Errorf("%w: entry is invalid", ErrCorruptStore)
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode idempotency entry: %w", err)
	}
	return data, nil
}

// compareIdempotencyEntries compares two entries for equality.
func compareIdempotencyEntries(a, b idempotencyEntry) int {
	if a.OperationID < b.OperationID {
		return -1
	}
	if a.OperationID > b.OperationID {
		return 1
	}
	if len(a.BindingDigest) < len(b.BindingDigest) {
		return -1
	}
	if len(a.BindingDigest) > len(b.BindingDigest) {
		return 1
	}
	for i := range a.BindingDigest {
		if a.BindingDigest[i] < b.BindingDigest[i] {
			return -1
		}
		if a.BindingDigest[i] > b.BindingDigest[i] {
			return 1
		}
	}
	if a.KeyVersion < b.KeyVersion {
		return -1
	}
	if a.KeyVersion > b.KeyVersion {
		return 1
	}
	if a.RecordedAt.Before(b.RecordedAt) {
		return -1
	}
	if a.RecordedAt.After(b.RecordedAt) {
		return 1
	}
	return 0
}

// sortIdempotencyEntries sorts entries by operation ID, binding digest, and key version.
func sortIdempotencyEntries(entries []idempotencyEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		cmp := compareIdempotencyEntries(entries[i], entries[j])
		return cmp < 0
	})
}

// getOperationIDFromEntry retrieves the operation ID from an idempotency entry.
func getOperationIDFromEntry(entry idempotencyEntry) string {
	return entry.OperationID
}

// validateLockScopes returns a sorted copy of scopes if all are valid and non-duplicate.
func validateLockScopes(scopes []string) ([]string, error) {
	copyOfScopes := append([]string(nil), scopes...)
	sort.Strings(copyOfScopes)
	for i, scope := range copyOfScopes {
		if scope == "" || len(scope) > 512 {
			return nil, fmt.Errorf("%w: invalid lock scope", ErrInvalidRecord)
		}
		if i > 0 && copyOfScopes[i-1] == scope {
			return nil, fmt.Errorf("%w: duplicate lock scope", ErrInvalidRecord)
		}
	}
	return copyOfScopes, nil
}

// containsString returns true if values contains expected.
func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// requiredLogicalServerLock returns the lock scope for server operations and true.
func requiredLogicalServerLock(record OperationRecord) (string, bool) {
	if strings.HasPrefix(record.OperationType, "server.") {
		return LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID), true
	}
	return "", false
}

// LogicalServerLockScope returns the canonical resource lock key for a server.
func LogicalServerLockScope(environmentID, serverID string) string {
	return fmt.Sprintf("server/%s:%d", environmentID, serverID)
}
