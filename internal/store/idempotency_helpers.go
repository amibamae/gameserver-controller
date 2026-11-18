// Package store contains operation persistence logic for the controller application.
package store

import (
	"bytes"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/amibamae/gameserver-controller/internal/inputbinding"
	"github.com/amibamae/gameserver-controller/internal/protocolstate"
)

// ==================== Error Variables ====================

var (
	ErrInvalidRecord      = errors.New("operation record has invalid field")
	ErrAlreadyExists      = errors.New("operation already exists in store")
	ErrNotFound           = errors.New("operation not found in store")
	ErrRevisionConflict   = errors.New("operation revision mismatch")
	ErrTerminalOperation  = errors.New("cannot modify terminal operation")
	ErrImmutableFields    = errors.New("immutable operation field changed")
	ErrKeyVersionMissing  = errors.New("idempotency verifier key version unavailable")
	ErrResourceConflict   = errors.New("resource locked by another operation")
	ErrIdempotencyConflict = errors.New("idempotency conflict: same request under different key")
	ErrIdempotencyExpired  = errors.New("idempotency expired: no verifier found for requested key version")
	ErrCorruptStore       = errors.New("operation store data appears corrupted")
	ErrStorageUnavailable = errors.New("operation storage unavailable")
	ErrUnresolvedEffect   = errors.New("terminal operation has unresolved effect")
	ErrLogicalServerNotFound     = errors.New("logical server not found")
	ErrLogicalServerAlreadyExists  = errors.New("logical server definition already exists")
	ErrLogicalServerTargetMismatch = errors.New("logical server instance ref mismatch")
	ErrInvalidBackupContext       = errors.New("backup context missing before lifecycle transition")
)

// ==================== Bucket Constants ====================

const (
	operationsBucketName           = "operations"
	metadataBucketName             = "metadata"
	idempotencyBucketName          = "idempotency"
	locksBucketName                = "resource_locks"
	operationLocksBucketName       = "operation_locks"
	keyVersionsBucketName          = "idempotency_key_versions"
	logicalServersBucketName       = "logical_servers"
	capacityReservationsBucketName = "capacity_reservations"
	managementAPIKeysBucketName    = "management_api_keys"
	hostHandoffsBucketName         = "host_handoffs"
	databaseSchema                 = "5"
)

var (
	operationsBucket           = []byte(operationsBucketName)
	metadataBucket             = []byte(metadataBucketName)
	idempotencyBucket          = []byte(idempotencyBucketName)
	locksBucket                = []byte(locksBucketName)
	operationLocksBucket       = []byte(operationLocksBucketName)
	keyVersionsBucket          = []byte(keyVersionsBucketName)
	logicalServersBucket       = []byte(logicalServersBucketName)
	capacityReservationsBucket = []byte(capacityReservationsBucketName)
	managementAPIKeysBucket    = []byte(managementAPIKeysBucketName)
	hostHandoffsBucket         = []byte(hostHandoffsBucketName)
	schemaKey                  = []byte("schema_version")
)

// ==================== Idempotency Types ====================

type IdempotencyVerifier struct {
	KeyVersion    string
	Digest        []byte
	BindingDigest []byte
}

type IdempotencyProof struct {
	ActiveKeyVersion string
	Candidates       []IdempotencyVerifier
}

// ==================== Field Validators ====================

// validIdentifier returns true if the value is non-empty and within max length.
func validIdentifier(value string, max int) bool {
	return value != "" && len(value) <= max
}

// validQualifiedName returns true if the value starts with a lowercase letter,
// contains only lowercase letters, digits, or dots, and is within length limits.
func validQualifiedName(value string, max int) bool {
	if len(value) < 2 || len(value) > max || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, ch := range value[1:] {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '.' {
			return false
		}
	}
	return true
}

// validCapabilityName returns true if the name starts with a lowercase letter,
// contains only lowercase letters, digits, dots, underscores, or hyphens.
func validCapabilityName(value string, max int) bool {
	if len(value) < 2 || len(value) > max || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, ch := range value[1:] {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '.' && ch != '_' && ch != '-' {
			return false
		}
	}
	return true
}

// validIdempotencyKey returns true if the key is within length limits and
// contains only printable ASCII characters except space, tab, newline, carriage return.
func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x21 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

// validFingerprint returns true if the fingerprint is in sha256:hex format
// with a valid SHA-256 hash (32 bytes).
func validFingerprint(value string) bool {
	if len(value) != len("sha256:") + 64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil && len(decoded) == 32
}

// invalidKeyVersionRune returns true if the rune is an invalid character in key versions.
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

// validKeyVersion returns true if the version string uses only alphanumeric characters
// and common separators, and is within length limits.
func validKeyVersion(version string) bool {
	if version == "" || len(version) > 64 {
		return false
	}
	return strings.IndexFunc(version, invalidKeyVersionRune) == -1
}

// isJSONObject returns true if the value is a valid JSON object (not array or primitive).
func isJSONObject(value json.RawMessage) bool {
	if len(value) == 0 || !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return false
	}
	return object != nil
}

// knownOperationStatus returns true if the status is a valid protocolstate.OperationStatus.
func knownOperationStatus(status protocolstate.OperationStatus) bool {
	switch status {
	case protocolstate.OperationPending, protocolstate.OperationRunning, protocolstate.OperationWaiting,
		protocolstate.OperationCancellationRequested, protocolstate.OperationReconciling,
		protocolstate.OperationNeedsOperator, protocolstate.OperationSucceeded,
		protocolstate.OperationFailed, protocolstate.OperationCancelled:
		return true
	default:
		return false
	}
}

// knownStepStatus returns true if the status is a valid protocolstate.StepStatus.
func knownStepStatus(status protocolstate.StepStatus) bool {
	switch status {
	case protocolstate.StepPending, protocolstate.StepRunning, protocolstate.StepWaiting,
		protocolstate.StepOutcomeUnknown, protocolstate.StepSucceeded, protocolstate.StepFailed,
		protocolstate.StepSkipped, protocolstate.StepCancelled:
		return true
	default:
		return false
	}
}

// validSecretReference returns true if the secret reference is well-formed.
func validSecretReference(ref SecretReference) bool {
	// SecretRef must be present and within length limits
	if ref.SecretRef == "" || len(ref.SecretRef) > 1024 || ref.Purpose == "" || len(ref.Purpose) > 100 {
		return false
	}

	// SecretRef must use http:// or https:// scheme
	separator := strings.Index(ref.SecretRef, "://")
	if separator < 1 || ref.SecretRef[0] < 'a' || ref.SecretRef[0] > 'z' {
		return false
	}

	// Validate characters in the host portion
	for _, ch := range ref.SecretRef[1:separator] {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '+' && ch != '.' && ch != '-' {
			return false
		}
	}

	// VersionRef has length limit
	if len(ref.VersionRef) > 500 {
		return false
	}

	// PermissionScope must have unique, non-empty entries
	seen := make(map[string]struct{}, len(ref.PermissionScope))
	for _, scope := range ref.PermissionScope {
		if scope == "" || len(scope) > 160 {
			return false
		}
		if _, exists := seen[scope]; exists {
			return false
		}
		seen[scope] = struct{}{}
	}

	return true
}

// invalidField returns an error with the specified field name for validation failures.
func invalidField(field string) error {
	return fmt.Errorf("%w: invalid %s", ErrInvalidRecord, field)
}

// containsString checks if a string exists in a slice.
func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

// equalTimePointer returns true if both pointers are nil or point to equal times.
func equalTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

// ==================== Idempotency Utilities ====================

// validateIdempotencyProof checks the proof structure and verifier candidates.
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

func verifierIndex(verifier IdempotencyVerifier) []byte {
	return []byte(verifier.KeyVersion + ":" + hex.EncodeToString(verifier.Digest))
}

// findVerifier searches for a candidate with the given key version.
func findVerifier(proof IdempotencyProof, version string) (IdempotencyVerifier, bool) {
	for _, candidate := range proof.Candidates {
		if candidate.KeyVersion == version {
			return candidate, true
		}
	}
	return IdempotencyVerifier{}, false
}

// requestMatches compares binding digests using constant-time comparison.
func requestMatches(entry idempotencyEntry, candidate IdempotencyVerifier) bool {
	if len(entry.BindingDigest) != len(candidate.BindingDigest) {
		return false
	}
	return subtle.ConstantTimeCompare(entry.BindingDigest, candidate.BindingDigest) == 1
}

// decodeIdempotencyEntry decodes JSON and validates the entry structure.
func decodeIdempotencyEntry(value []byte) (idempotencyEntry, error) {
	var entry idempotencyEntry
	if err := json.Unmarshal(value, &entry); err != nil || !entry.valid() {
		return idempotencyEntry{}, fmt.Errorf("%w: malformed idempotency entry", ErrCorruptStore)
	}
	return entry, nil
}

// idempotencyEntry represents a single idempotency record keyed by digest.
type idempotencyEntry struct {
	OperationID   string    `json:"operation_id"`
	BindingDigest []byte    `json:"binding_digest"`
	KeyVersion    string    `json:"key_version"`
	RecordedAt    time.Time `json:"recorded_at"`
}

func (entry idempotencyEntry) valid() bool {
	return validIdentifier(entry.OperationID, 200) && len(entry.BindingDigest) == 32 &&
		validKeyVersion(entry.KeyVersion) && !entry.RecordedAt.IsZero()
}

// ==================== Resource Lock Utilities ====================

type ResourceLock struct {
	Scope       string    `json:"scope"`
	OperationID string    `json:"operation_id"`
	AcquiredAt  time.Time `json:"acquired_at"`
}

func decodeResourceLock(value []byte) (ResourceLock, error) {
	var lock ResourceLock
	if err := json.Unmarshal(value, &lock); err != nil || lock.Scope == "" || lock.OperationID == "" || lock.AcquiredAt.IsZero() {
		return ResourceLock{}, fmt.Errorf("%w: malformed resource lock", ErrCorruptStore)
	}
	return lock, nil
}

// ==================== Validation Utilities for Record ====================

// validBackupInitialState returns true if the backup record contains only a backup_initial_lifecycle_state.
func validBackupInitialState(record OperationRecord) bool {
	if record.BackupInitialLifecycleState == "" || !protocolstate.IsServerStopped(string(record.BackupInitialLifecycleState)) {
		return false
	}
	if len(record.Steps) != 0 || len(record.RequestSnapshot.SecretRefs) != 0 || len(record.Target.InstanceRef) != 0 {
		return false
	}
	return true
}

// containsStepID checks if the slice contains a step with the given ID.
func containsStepID(steps []OperationStep, stepID string) bool {
	for _, step := range steps {
		if step.StepID == stepID {
			return true
		}
	}
	return false
}

// monotonicTimestamp ensures the timestamp advances from the previous value.
func monotonicTimestamp(previous, now time.Time) time.Time {
	if now.After(previous) {
		return now
	}
	return previous.Add(time.Nanosecond)
}
