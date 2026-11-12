package main

import (
	"strings"
)

// evaluateStorageBackupPartialUploadNotVerified evaluates storage.backup.partial-upload-not-verified.
func evaluateStorageBackupPartialUploadNotVerified(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateStorageObjectChecksumMismatchBlocksRestore evaluates storage.object.checksum-mismatch-blocks-restore.
func evaluateStorageObjectChecksumMismatchBlocksRestore(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateStorageObjectEtagNotChecksum evaluates storage.object.etag-not-checksum.
func evaluateStorageObjectEtagNotChecksum(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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

// evaluateStorageStagingCannotDeleteProductionObject evaluates storage.staging.cannot-delete-production-object, storage.staging.production-delete-scope-denied and storage.production.backup-delete-authorized.
func evaluateStorageStagingCannotDeleteProductionObject(pkg *protocolPackage, fixture map[string]any) (map[string]any, error) {
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
