package main

import (
	"strings"
)

// Register the storage and backup scope evaluators defined in this file.
func init() {
	registerEvaluator(withoutPackage(evaluateBackupWriteScope),
		"security.staging.cannot-write-production-backup",
		"security.staging.production-write-scope-denied",
		"security.production.backup-write-authorized")
	registerEvaluator(withoutPackage(evaluateStorageBackupPartialUploadNotVerified), "storage.backup.partial-upload-not-verified")
	registerEvaluator(withoutPackage(evaluateStorageObjectChecksumMismatchBlocksRestore), "storage.object.checksum-mismatch-blocks-restore")
	registerEvaluator(withoutPackage(evaluateStorageObjectEtagNotChecksum), "storage.object.etag-not-checksum")
	registerEvaluator(withoutPackage(evaluateBackupDeleteScope),
		"storage.staging.cannot-delete-production-object",
		"storage.staging.production-delete-scope-denied",
		"storage.production.backup-delete-authorized")
}

// evaluateBackupWriteScope evaluates the conformance fixtures
// security.staging.cannot-write-production-backup, security.staging.production-write-scope-denied,
// security.production.backup-write-authorized.
func evaluateBackupWriteScope(fixture map[string]any) (map[string]any, error) {
	actor := stringValue(fixture, "actor_environment")
	target := stringValue(fixture, "target_environment")
	requiredScope := "storage.backup.write:" + target
	allowed := actor == target && contains(arrayValue(fixture, "credential_scope"), requiredScope)
	category := any(nil)
	if !allowed {
		category = "authorization"
	}
	return map[string]any{"accepted": allowed, "side_effect_permitted": allowed, "error_category": category}, nil
}

// evaluateStorageBackupPartialUploadNotVerified evaluates the storage.backup.partial-upload-not-verified conformance fixture.
func evaluateStorageBackupPartialUploadNotVerified(fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	required := arrayValue(input, "required_object_ids")
	uploaded := arrayValue(input, "uploaded_object_ids")
	allUploaded := uniqueStrings(required) && uniqueStrings(uploaded) && sameStringSet(required, uploaded)
	verified := allUploaded && boolValue(input, "manifest_verified")
	return map[string]any{
		"all_required_objects_uploaded": allUploaded,
		"backup_verified":               verified,
		"destruction_allowed":           verified,
		"partial":                       !allUploaded || !boolValue(input, "manifest_verified"),
	}, nil
}

// evaluateStorageObjectChecksumMismatchBlocksRestore evaluates the storage.object.checksum-mismatch-blocks-restore conformance fixture.
func evaluateStorageObjectChecksumMismatchBlocksRestore(fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	sizeMatches := input["expected_size_bytes"] == input["actual_size_bytes"]
	expected := strings.ToLower(stringValue(input, "expected_checksum"))
	actual := strings.ToLower(stringValue(input, "actual_checksum"))
	checksumMatches := expected == actual
	verified, reason := sizeMatches && checksumMatches, any(nil)
	if !verified {
		reason = "checksum_mismatch"
		if !sizeMatches {
			reason = "size_mismatch"
		}
	}
	return map[string]any{"verified": verified, "restore_allowed": verified, "verification_reason": reason}, nil
}

// evaluateStorageObjectEtagNotChecksum evaluates the storage.object.etag-not-checksum conformance fixture.
func evaluateStorageObjectEtagNotChecksum(fixture map[string]any) (map[string]any, error) {
	input := asObject(fixture["input"])
	expected := strings.ToLower(stringValue(input, "expected_checksum"))
	verified := false
	for _, key := range []string{"trusted_provider_checksum", "content_digest"} {
		if value, ok := input[key].(string); ok && strings.ToLower(value) == expected {
			verified = true
		}
	}
	source := "insufficient_evidence"
	if verified {
		source = "trusted_checksum"
	}
	return map[string]any{"verified": verified, "verification_source": source}, nil
}

// evaluateBackupDeleteScope evaluates the conformance fixtures
// storage.staging.cannot-delete-production-object, storage.staging.production-delete-scope-denied,
// storage.production.backup-delete-authorized.
func evaluateBackupDeleteScope(fixture map[string]any) (map[string]any, error) {
	actor := stringValue(fixture, "actor_environment")
	target := stringValue(fixture, "target_environment")
	requiredScope := "storage.backup.delete:" + target
	allowed := actor == target && contains(arrayValue(fixture, "credential_scope"), requiredScope)
	category := any(nil)
	calls := 0
	if allowed {
		calls = 1
	} else {
		category = "authorization"
	}
	return map[string]any{
		"accepted": allowed, "error_category": category,
		"side_effect_permitted": allowed, "adapter_delete_calls": calls,
	}, nil
}
