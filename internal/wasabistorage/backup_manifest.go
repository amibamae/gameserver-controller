// This file provides backup manifest lifecycle management for Wasabi storage adapter.
package wasabistorage

import (
	"errors"
)

var (
	errInvalidManifest        = errors.New("manifest data does not match expected format")
	errManifestNotInPrefix    = errors.New("manifest is not in the manifest prefix")
	errManifestReadFailed     = errors.New("failed to read manifest from Wasabi")
	errManifestVerifyChecksum = errors.New("manifest Checksum verification failed")
	errManifestExpired        = errors.New("manifest has exceeded its retention period")
)

// The manifest lifecycle capabilities were removed from the adapter until
// they are fully implemented and verified with tests.
// Placeholder functions left for future re-implementation.
