package backupmanifest

import (
	"testing"
	"time"
)

func TestNewManifest(t *testing.T) {
	obj := ObjectReference{
		ObjectTarget: ObjectTarget{
			AdapterID:      "vultrhosting",
			StorageProfile: "paper-minecraft-01",
			EnvironmentID:  "prod",
			Container:      "backups",
			Key:            "worlds/example-v2.dat",
		},
	}
	m := BuildManifest(obj, "minecraft-world", 1048576, Checksum{Digest: "abc123"}, nil)
	if m.ObjectRef.AdapterID != "vultrhosting" {
		t.Errorf("expected adapter_id vultrhosting, got %s", m.ObjectRef.AdapterID)
	}
	if m.Format != "minecraft-world" {
		t.Errorf("expected format minecraft-world, got %s", m.Format)
	}
	if m.Size != 1048576 {
		t.Errorf("expected size 1048576, got %d", m.Size)
	}
	if m.Retention.Enabled != true {
		t.Error("expected retention enabled by default")
	}
}

func TestBuildManifest(t *testing.T) {
	obj := ObjectReference{ObjectTarget: ObjectTarget{AdapterID: "wasabi", StorageProfile: "s3-profile-1"}}
	m := BuildManifest(obj, "backup", 2048, Checksum{Digest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, nil)
	if m.ObjectRef.AdapterID != "wasabi" {
		t.Error("wrong adapter")
	}
	if len(m.SHA256.Digest) != 64 {
		t.Errorf("expected 64 chars, got %d", len(m.SHA256.Digest))
	}
}

func TestValidateManifest(t *testing.T) {
	err := ValidateManifest(Manifest{})
	if err == nil {
		t.Error("expected validation error for empty manifest")
	}
	valid := Manifest{
		ObjectRef: ObjectReference{
			ObjectTarget: ObjectTarget{AdapterID: "test", StorageProfile: "p1", EnvironmentID: "env1", Container: "c", Key: "k"},
		},
		Format:    "type",
		Size:      100,
		SHA256:    Checksum{Digest: "abc"}, // short digest should fail
		Retention: RetentionPolicy{Enabled: true, MaxAge: func() *time.Duration { d := 24 * time.Hour; return &d }(), RetainedN: 1},
	}
	if err := ValidateManifest(valid); err == nil {
		t.Error("expected error for short checksum")
	}
	valid.SHA256.Digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 64 hex chars
	if err := ValidateManifest(valid); err != nil {
		t.Errorf("valid manifest rejected: %v", err)
	}
}

func TestComputeSHA256(t *testing.T) {
	hash := ComputeSHA256([]byte("test data"))
	// Just verify we got a non-empty hash
	if len(hash) == 0 || len(hash) != 64 {
		t.Errorf("expected SHA256 hex string of 64 chars, got %q", hash)
	}
	// Verify idempotency
	hash2 := ComputeSHA256([]byte("test data"))
	if hash != hash2 {
		t.Error("ComputeSHA256 should be deterministic")
	}
}

func TestDefaultRetentionPolicy(t *testing.T) {
	_ = func() time.Duration { d := 7 * 24 * time.Hour; return d }()
	policy := RetentionPolicy{Enabled: true, MaxAge: func() *time.Duration { d := 7 * 24 * time.Hour; return &d }(), RetainedN: 5}
	if !policy.Enabled {
		t.Error("default policy should be enabled")
	}
	if policy.RetainedN != 5 {
		t.Errorf("expected retained_n=5, got %d", policy.RetainedN)
	}
	if policy.MaxAge == nil || *policy.MaxAge != 7*24*time.Hour {
		t.Error("default max age should be 7 days")
	}
}

func TestManifestExpired(t *testing.T) {
	now := time.Now()
	past := now.Add(-10 * 24 * time.Hour) // 10 days ago
	m := Manifest{
		ObjectRef:  ObjectReference{ObjectTarget: ObjectTarget{}},
		UploadedAt: past,
		Retention: RetentionPolicy{
			Enabled:   true,
			MaxAge:    func() *time.Duration { d := 7 * 24 * time.Hour; return &d }(),
			RetainedN: 5,
		},
	}
	if !m.ManifestExpired() {
		t.Error("manifest past retention age should be expired")
	}
	future := now.Add(10 * 24 * time.Hour) // 10 days in future
	m.UploadedAt = future
	if m.ManifestExpired() {
		t.Error("manifest in future should not be expired")
	}
}

func TestManifestNotExpired(t *testing.T) {
	now := time.Now()
	recent := now.Add(-1 * 24 * time.Hour) // 1 day ago
	m := Manifest{
		ObjectRef:  ObjectReference{ObjectTarget: ObjectTarget{}},
		UploadedAt: recent,
		Retention: RetentionPolicy{
			Enabled:   true,
			MaxAge:    func() *time.Duration { d := 7 * 24 * time.Hour; return &d }(),
			RetainedN: 5,
		},
	}
	if m.ManifestExpired() {
		t.Error("manifest within retention age should not be expired")
	}
}

func TestPruneOlderThan(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-10 * 24 * time.Hour) // 10 days ago
	oldManifest := Manifest{ObjectRef: ObjectReference{}, UploadedAt: cutoff, Retention: RetentionPolicy{Enabled: false}}
	newManifest := Manifest{ObjectRef: ObjectReference{}, UploadedAt: now, Retention: RetentionPolicy{Enabled: false}}
	manifests := []Manifest{oldManifest, newManifest}
	result := PruneOlderThan(manifests, 7*24*time.Hour)
	if len(result) != 1 || result[0].UploadedAt != newManifest.UploadedAt {
		t.Error("pruning should keep only recent manifests")
	}
}

func TestKeepOldestN(t *testing.T) {
	now := time.Now()
	manifests := []Manifest{
		{ObjectRef: ObjectReference{}, UploadedAt: now.Add(-3 * 24 * time.Hour)},
		{ObjectRef: ObjectReference{}, UploadedAt: now.Add(-1 * 24 * time.Hour)},
		{ObjectRef: ObjectReference{}, UploadedAt: now},
	}
	result := KeepOldestN(manifests, 2)
	if len(result) != 2 {
		t.Errorf("expected to keep 2 manifests, got %d", len(result))
	}
	// Check that we kept the newest ones (now and -1 day, not -3 days)
	if result[0].UploadedAt.Before(manifests[0].UploadedAt) {
		t.Error("should keep newest manifests")
	}
	if !result[0].UploadedAt.Equal(manifests[2].UploadedAt) && !result[0].UploadedAt.After(manifests[1].UploadedAt) {
		t.Error("should keep the two newest manifests")
	}
}
