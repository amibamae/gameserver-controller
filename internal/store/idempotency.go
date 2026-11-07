package store

import (
	"crypto/subtle"
	"encoding/hex"
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

func validateIdempotencyProof(proof IdempotencyProof) error {
	if !validKeyVersion(proof.ActiveKeyVersion) || len(proof.Candidates) == 0 {
		return fmt.Errorf("%w: missing active key version or verifier candidates", ErrInvalidRecord)
	}
	activeFound := false
	seen := make(map[string]struct{}, len(proof.Candidates))
	for _, candidate := range proof.Candidates {
		if !validKeyVersion(candidate.KeyVersion) || len(candidate.Digest) != 32 || len(candidate.BindingDigest) != 32 {
			return fmt.Errorf("%w: invalid idempotency verifier", ErrInvalidRecord)
		}
		if _, ok := seen[candidate.KeyVersion]; ok {
			return fmt.Errorf("%w: duplicate idempotency key version", ErrInvalidRecord)
		}
		seen[candidate.KeyVersion] = struct{}{}
		activeFound = activeFound || candidate.KeyVersion == proof.ActiveKeyVersion
	}
	if !activeFound {
		return fmt.Errorf("%w: active key verifier is missing", ErrInvalidRecord)
	}
	return nil
}

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

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func requiredLogicalServerLock(record OperationRecord) (string, bool) {
	if strings.HasPrefix(record.OperationType, "server.") {
		return LogicalServerLockScope(record.Target.EnvironmentID, record.Target.ServerID), true
	}
	return "", false
}

// LogicalServerLockScope returns the canonical resource lock key for a server
// in one environment. Length prefixes make arbitrary IDs unambiguous.
func LogicalServerLockScope(environmentID, serverID string) string {
	return fmt.Sprintf("server/%d:%s/%d:%s", len(environmentID), environmentID, len(serverID), serverID)
}

func verifierIndex(verifier IdempotencyVerifier) []byte {
	return []byte(verifier.KeyVersion + ":" + hex.EncodeToString(verifier.Digest))
}

func findVerifier(proof IdempotencyProof, version string) (IdempotencyVerifier, bool) {
	for _, candidate := range proof.Candidates {
		if candidate.KeyVersion == version {
			return candidate, true
		}
	}
	return IdempotencyVerifier{}, false
}

func requestMatches(entry idempotencyEntry, candidate IdempotencyVerifier) bool {
	return len(entry.BindingDigest) == len(candidate.BindingDigest) && subtle.ConstantTimeCompare(entry.BindingDigest, candidate.BindingDigest) == 1
}

func decodeIdempotencyEntry(value []byte) (idempotencyEntry, error) {
	var entry idempotencyEntry
	if err := json.Unmarshal(value, &entry); err != nil || !validIdentifier(entry.OperationID, 200) || len(entry.BindingDigest) != 32 || !validKeyVersion(entry.KeyVersion) || entry.RecordedAt.IsZero() {
		return idempotencyEntry{}, fmt.Errorf("%w: malformed idempotency entry", ErrCorruptStore)
	}
	return entry, nil
}

func decodeResourceLock(value []byte) (ResourceLock, error) {
	var lock ResourceLock
	if err := json.Unmarshal(value, &lock); err != nil || lock.Scope == "" || lock.OperationID == "" || lock.AcquiredAt.IsZero() {
		return ResourceLock{}, fmt.Errorf("%w: malformed resource lock", ErrCorruptStore)
	}
	return lock, nil
}

func validKeyVersion(version string) bool {
	if version == "" || len(version) > 64 {
		return false
	}
	return strings.IndexFunc(version, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-')
	}) == -1
}
