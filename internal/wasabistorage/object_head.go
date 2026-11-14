package wasabistorage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// head reports whether an object exists and, when it does, the provider's
// size, ETag, modification time, and full-object SHA-256.
func (a *Adapter) head(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	input, err := decodeInput(request, "invalid object inspection request", func(input headInput) bool {
		return a.validRequestEnvironment(request) && a.validReference(input.ObjectRef)
	})
	if err != nil {
		return nil, err
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	ref := input.ObjectRef
	result, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(ref.Container), Key: aws.String(ref.Key), VersionId: optionalVersion(ref.VersionID)})
	if isMissing(err) {
		return map[string]any{"object_ref": ref, "object_state": "absent", "observed_at": now()}, nil
	}
	if err != nil {
		return nil, errors.New("Wasabi object inspection failed")
	}
	return a.presentObject(ref, result)
}

func (a *Adapter) presentObject(ref objectReference, result *s3.HeadObjectOutput) (any, error) {
	version, err := confirmedVersion(ref.VersionID, result.VersionId)
	if err != nil {
		return nil, err
	}
	ref.VersionID = version
	if a.missingRequiredVersion(ref.VersionID) {
		return nil, errors.New("Wasabi object has no immutable version ID")
	}
	value := map[string]any{"object_ref": ref, "object_state": "present", "observed_at": now()}
	describeObject(value, result)
	return value, nil
}

// describeObject adds each provider field that was reported.
func describeObject(value map[string]any, result *s3.HeadObjectOutput) {
	if result.ContentLength != nil {
		value["size_bytes"] = *result.ContentLength
	}
	addProviderETag(value, result.ETag)
	if result.LastModified != nil {
		value["last_modified"] = result.LastModified.UTC().Format(time.RFC3339Nano)
	}
	if digest, ok := fullObjectSHA256(result); ok {
		value["provider_checksum"] = checksum{Algorithm: "sha256", Digest: digest}
	}
}

// fullObjectSHA256 returns the provider's whole-object SHA-256 in hex. A
// composite (per-part) checksum, or one that does not decode to a SHA-256
// digest, is not reported.
func fullObjectSHA256(result *s3.HeadObjectOutput) (string, bool) {
	if result.ChecksumType != types.ChecksumTypeFullObject || result.ChecksumSHA256 == nil {
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(*result.ChecksumSHA256)
	if err != nil || len(decoded) != sha256.Size {
		return "", false
	}
	return hex.EncodeToString(decoded), true
}
