package backupmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Manifest represents an immutable backup manifest with checksum verification.
type Manifest struct {
	ObjectRef  ObjectReference   `json:"object_ref"`
	VersionID  string            `json:"version_id,omitempty"`
	Format     string            `json:"format"`
	Size       int64             `json:"size_bytes"`
	SHA256     Checksum          `json:"sha256"`
	UploadedAt time.Time         `json:"uploaded_at"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Retention  RetentionPolicy   `json:"retention_policy"`
}

// ObjectReference identifies an object across adapters.
type ObjectReference struct {
	ObjectTarget `json:",inline"`
	VersionID    string `json:"version_id,omitempty"`
}

// ObjectTarget contains the base adapter and location information.
type ObjectTarget struct {
	AdapterID      string `json:"adapter_id"`
	StorageProfile string `json:"storage_profile_id"`
	EnvironmentID  string `json:"environment_id"`
	Container      string `json:"container"`
	Key            string `json:"key"`
}

// Checksum wraps a hash digest.
type Checksum struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
}

// RetentionPolicy defines the retention rules for backup manifests.
type RetentionPolicy struct {
	MaxAge    *time.Duration `json:"max_age,omitempty"`
	RetainedN int            `json:"retained_n,omitempty"` // number of backups to keep (0 = unlimited)
	Enabled   bool           `json:"enabled"`
}

// BuildManifest creates a new manifest with the current timestamp.
func BuildManifest(object ObjectReference, format string, size int64, sha256sum Checksum, metadata map[string]string) Manifest {
	return Manifest{
		ObjectRef:  object,
		VersionID:  object.VersionID,
		Format:     format,
		Size:       size,
		SHA256:     sha256sum,
		UploadedAt: time.Now().UTC(),
		Metadata:   metadata,
		Retention:  RetentionPolicy{Enabled: true},
	}
}

// targetComplete reports whether every field that locates an object is set.
func targetComplete(t ObjectTarget) bool {
	for _, field := range []string{t.AdapterID, t.StorageProfile, t.EnvironmentID, t.Container, t.Key} {
		if field == "" {
			return false
		}
	}
	return true
}

// ValidateManifest checks that the manifest has all required fields.
func ValidateManifest(m Manifest) error {
	if !targetComplete(m.ObjectRef.ObjectTarget) {
		return fmt.Errorf("object reference is invalid: missing adapter_id=%s, storage_profile=%s, environment_id=%s, container=%s, key=%s", m.ObjectRef.AdapterID, m.ObjectRef.StorageProfile, m.ObjectRef.EnvironmentID, m.ObjectRef.Container, m.ObjectRef.Key)
	}
	if m.Format == "" {
		return fmt.Errorf("manifest format is required")
	}
	if m.Size < 0 {
		return fmt.Errorf("size must be non-negative: %d", m.Size)
	}
	if len(m.SHA256.Digest) == 0 || len(m.SHA256.Digest) != 64 {
		return fmt.Errorf("SHA-256 checksum is required and must be 64 hex characters")
	}
	if m.Retention.Enabled && (m.Retention.RetainedN <= 0 || m.Retention.MaxAge == nil) {
		return fmt.Errorf("retention policy requires both retained_n and max_age when enabled")
	}
	return nil
}

// ComputeSHA256 computes a SHA-256 hash of the input data.
func ComputeSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// VerifyChecksum checks if the provided checksum matches the manifest's checksum.
func VerifyChecksum(m Manifest, receivedChecksum Checksum) bool {
	if len(m.SHA256.Digest) == 0 || len(receivedChecksum.Digest) == 0 {
		return false
	}
	return m.SHA256.Digest == receivedChecksum.Digest && m.SHA256.Algorithm == receivedChecksum.Algorithm
}

// ManifestExpired checks if the manifest has exceeded its retention period.
func (m Manifest) ManifestExpired() bool {
	if !m.Retention.Enabled {
		return false
	}
	if m.Retention.MaxAge == nil || *m.Retention.MaxAge <= 0 {
		return false
	}
	return time.Since(m.UploadedAt) > *m.Retention.MaxAge
}

// PruneOlderThan keeps only manifests newer than the threshold.
func PruneOlderThan(manifests []Manifest, maxAge time.Duration) []Manifest {
	now := time.Now()
	cutoff := now.Add(-maxAge)
	var pruned []Manifest
	for _, m := range manifests {
		if m.UploadedAt.After(cutoff) {
			pruned = append(pruned, m)
		}
	}
	return pruned
}

// KeepOldestN keeps only the N newest manifests and removes older ones.
func KeepOldestN(manifests []Manifest, n int) []Manifest {
	if n <= 0 {
		return manifests
	}
	sorted := make([]Manifest, len(manifests))
	copy(sorted, manifests)
	// Sort descending by uploaded time (newest first) so we keep the newest N
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].UploadedAt.After(sorted[i].UploadedAt) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	if n >= len(sorted) {
		return sorted
	}
	return sorted[:n] // keep first N (newest) entries
}
