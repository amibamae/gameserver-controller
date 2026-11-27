package wasabistorage

import "errors"

// Classification of S3 provider errors by their error code.

func errorCode(err error) string {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) {
		return coded.ErrorCode()
	}
	return ""
}

func isMissing(err error) bool {
	switch errorCode(err) {
	case "NotFound", "NoSuchKey", "NoSuchVersion":
		return true
	}
	return false
}

func noObjectLock(err error) bool {
	switch errorCode(err) {
	case "ObjectLockConfigurationNotFoundError", "NoSuchObjectLockConfiguration":
		return true
	}
	return false
}
