package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

// These tests call validateVerifiedProvisionBackup directly with one change
// to an otherwise valid verified backup, so each rule is pinned by its reason.

const (
	provisionBackupResultInvalid   = "selected backup has no valid controller-verified server.backup result"
	provisionBackupResultUnbound   = "selected backup result does not bind the requested backup, environment, and source generation"
	provisionBackupManifestUnbound = "selected backup manifest does not bind the exact server, environment, backup, and source generation"
	provisionBackupObjectInvalid   = "selected backup contains an invalid or cross-profile object reference"

	// The server-backup-result schema embeds the manifest and object rules, so
	// it rejects these documents before the function's own matching check,
	// which remains as defence in depth.
	provisionBackupSchemaFirst = provisionBackupResultInvalid
)

// backupDocument is a decoded backup result whose manifest and first object
// reference can be changed in place and encoded again.
type backupDocument struct {
	result, operationData, manifest, object, objectRef map[string]any
}

func decodeBackupDocument(t *testing.T, raw json.RawMessage) backupDocument {
	t.Helper()
	var d backupDocument
	if err := json.Unmarshal(raw, &d.result); err != nil {
		t.Fatal(err)
	}
	d.operationData = d.result["operation_data"].(map[string]any)
	d.manifest = d.operationData["manifest"].(map[string]any)
	d.object = d.manifest["objects"].([]any)[0].(map[string]any)
	d.objectRef = d.object["object_ref"].(map[string]any)
	return d
}

func (d backupDocument) encode(t *testing.T) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(d.result)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestValidateVerifiedProvisionBackupRules(t *testing.T) {
	tests := []struct {
		name     string
		backupID string
		change   func(*backupDocument, *ServerProvisionProfile, *VerifiedProvisionBackup)
		want     string
	}{
		{"integrity not verified", "", func(_ *backupDocument, _ *ServerProvisionProfile, v *VerifiedProvisionBackup) {
			v.IntegrityVerified = false
		}, provisionBackupResultInvalid},
		{"result without a summary", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			delete(d.result, "outcome_summary")
		}, provisionBackupResultInvalid},
		{"result of another operation", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.result["operation_type"] = "server.restore"
		}, provisionBackupSchemaFirst},
		{"partial backup", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.result["partial"] = true
		}, provisionBackupResultUnbound},
		{"another backup requested", "backup-other", nil, provisionBackupResultUnbound},
		{"backup from another environment", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.operationData["environment_id"] = "production"
		}, provisionBackupResultUnbound},
		{"result without a source generation", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.operationData["data_generation_id"] = ""
		}, provisionBackupSchemaFirst},
		{"unparseable verification time", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.operationData["verified_at"] = "yesterday"
		}, provisionBackupSchemaFirst},
		{"manifest for another server", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.manifest["server_id"] = "server-other"
		}, provisionBackupManifestUnbound},
		{"manifest for another backup", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.manifest["backup_id"] = "backup-other"
		}, provisionBackupManifestUnbound},
		{"manifest from another environment", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.manifest["environment_id"] = "production"
		}, provisionBackupManifestUnbound},
		{"manifest of another generation", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.manifest["data_generation_id"] = "gen-other"
		}, provisionBackupManifestUnbound},
		{"manifest without objects", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.manifest["objects"] = []any{}
		}, provisionBackupSchemaFirst},
		{"manifest with too many objects", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			objects := make([]any, 65)
			for i := range objects {
				objects[i] = d.object
			}
			d.manifest["objects"] = objects
		}, provisionBackupManifestUnbound},
		{"object in another storage adapter", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.objectRef["adapter_id"] = "storage.other"
		}, provisionBackupObjectInvalid},
		{"object in another storage profile", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.objectRef["storage_profile_id"] = "production-storage"
		}, provisionBackupObjectInvalid},
		{"object in another environment", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.objectRef["environment_id"] = "production"
		}, provisionBackupObjectInvalid},
		{"object with a negative size", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.object["size_bytes"] = -1
		}, provisionBackupSchemaFirst},
		{"object with another checksum algorithm", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.object["checksum_algorithm"] = "md5"
		}, provisionBackupSchemaFirst},
		{"object with a short checksum", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.object["checksum"] = strings.Repeat("a", 63)
		}, provisionBackupSchemaFirst},
		{"object with a non-hex checksum", "", func(d *backupDocument, _ *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			d.object["checksum"] = strings.Repeat("z", 64)
		}, provisionBackupSchemaFirst},
		{"unversioned object in a versioned profile", "", func(d *backupDocument, p *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			delete(d.objectRef, "version_id")
			p.BackupStorageVersioningEnabled = true
		}, "selected backup object lacks an immutable version reference"},
		{"unversioned object in an unversioned profile", "", func(d *backupDocument, p *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			delete(d.objectRef, "version_id")
			p.BackupStorageVersioningEnabled = false
		}, "ACCEPT"},
		{"versioned object in a versioned profile", "", func(_ *backupDocument, p *ServerProvisionProfile, _ *VerifiedProvisionBackup) {
			p.BackupStorageVersioningEnabled = true
		}, "ACCEPT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newProvisionPlanFixture(t)
			resolver, _ := provisionRestoreSetup(t, &f)
			stored, err := f.store.GetLogicalServer(provisionTestEnv, provisionTestServer)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeBackupDocument(t, resolver.result)
			profile := f.profile
			verified := VerifiedProvisionBackup{IntegrityVerified: true}
			if test.change != nil {
				test.change(&document, &profile, &verified)
			}
			verified.Result = document.encode(t)
			backupID := test.backupID
			if backupID == "" {
				backupID = "backup-123"
			}
			result, manifest, err := f.workflow.validateVerifiedProvisionBackup(stored.Record, backupID, profile, verified)
			if test.want == "ACCEPT" {
				if err != nil || !equalJSON(result, verified.Result) || !equalJSON(manifest, mustMarshal(t, document.manifest)) {
					t.Fatalf("validateVerifiedProvisionBackup = %s, %s, %v; want the result and its manifest", result, manifest, err)
				}
				return
			}
			if err == nil || result != nil || manifest != nil || err.Error() != test.want {
				t.Fatalf("validateVerifiedProvisionBackup = %s, %s, %v; want the error %q", result, manifest, err, test.want)
			}
		})
	}
}

func mustMarshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
