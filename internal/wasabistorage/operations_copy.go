package wasabistorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// copyObject performs a server-side copy within this profile and reports
// "copied" only after the new object version is read back and matches the
// expected size and SHA-256. It refuses to overwrite an existing destination.
func (a *Adapter) copyObject(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	input, err := a.decodeCopy(request)
	if err != nil {
		return nil, err
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := a.checkCopySource(ctx, client, input); err != nil {
		return nil, err
	}
	if err := a.checkCopyDestination(ctx, client, input.Destination); err != nil {
		return nil, err
	}
	result, err := client.CopyObject(ctx, a.copyRequest(input))
	if err != nil {
		return nil, sideEffectError("Wasabi server-side copy failed")
	}
	ref, err := a.copiedReference(input.Destination, result)
	if err != nil {
		return nil, err
	}
	count, err := a.verifyCopy(ctx, client, ref, input)
	if err != nil {
		return nil, err
	}
	return copyOutput(input.Destination, ref, count, result), nil
}

func (a *Adapter) decodeCopy(request adapter.InvokeRequest) (copyInput, error) {
	return decodeChecked(request, inputRules[copyInput]{
		rejection: "invalid object copy request",
		accept:    func(in copyInput) bool { return a.acceptsCopy(request, in) },
		check:     a.copyPreconditions,
	})
}

func (a *Adapter) acceptsCopy(request adapter.InvokeRequest, input copyInput) bool {
	return a.validRequestEnvironment(request) && a.validReference(input.SourceRef) && a.validTarget(input.Destination) &&
		validExpected(input.ExpectedSizeBytes, input.ExpectedChecksum, a.config.MaxObjectBytes)
}

// copyPreconditions refuses a copy the profile or a single request cannot make.
func (a *Adapter) copyPreconditions(input copyInput) error {
	switch {
	case a.refusedVersion(input.SourceRef.VersionID):
		return errors.New("an exact source object version is required by this storage profile")
	case input.SourceRef.Key == input.Destination.Key:
		return errors.New("copy destination must differ from its source")
	case input.ExpectedSizeBytes > maxServerSideCopySize:
		return errors.New("object is larger than the single-request server-side copy limit")
	}
	return nil
}

// checkCopySource confirms the requested source version and its size.
func (a *Adapter) checkCopySource(ctx context.Context, client s3Client, input copyInput) error {
	source, err := client.HeadObject(ctx, a.headRequest(input.SourceRef.Key, input.SourceRef.VersionID))
	if err != nil {
		return errors.New("Wasabi copy source could not be inspected")
	}
	if _, err := confirmedVersion(input.SourceRef.VersionID, source.VersionId); err != nil {
		return err
	}
	if source.ContentLength == nil || *source.ContentLength != input.ExpectedSizeBytes {
		return errors.New("Wasabi copy source size does not match the expected size")
	}
	return nil
}

// checkCopyDestination refuses a destination that exists or cannot be inspected.
func (a *Adapter) checkCopyDestination(ctx context.Context, client s3Client, destination objectTarget) error {
	_, err := client.HeadObject(ctx, a.headRequest(destination.Key, ""))
	if err == nil {
		return errors.New("copy destination already exists; refusing to overwrite it")
	}
	if !isMissing(err) {
		return errors.New("Wasabi copy destination could not be inspected")
	}
	return nil
}

func (a *Adapter) copyRequest(input copyInput) *s3.CopyObjectInput {
	return &s3.CopyObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(input.Destination.Key),
		CopySource: aws.String(a.copySource(input.SourceRef)), ChecksumAlgorithm: types.ChecksumAlgorithmSha256}
}

// copySource is the CopySource header for a source key and optional version.
func (a *Adapter) copySource(ref objectReference) string {
	source := a.config.Container + "/" + escapeKey(ref.Key)
	if ref.VersionID == "" {
		return source
	}
	return source + "?versionId=" + url.QueryEscape(ref.VersionID)
}

// copiedReference names the new object version, failing as a possible side
// effect when the profile requires a version the provider did not return.
func (a *Adapter) copiedReference(destination objectTarget, result *s3.CopyObjectOutput) (objectReference, error) {
	ref := objectReference{objectTarget: destination, VersionID: observedVersion(result.VersionId)}
	if a.missingRequiredVersion(ref.VersionID) {
		return objectReference{}, sideEffectError("Wasabi did not return the required immutable version for the copy")
	}
	return ref, nil
}

// verifyCopy reads the copy back and returns its size once its size and
// SHA-256 match the expected values.
func (a *Adapter) verifyCopy(ctx context.Context, client s3Client, ref objectReference, input copyInput) (int64, error) {
	readback := &s3.GetObjectInput{Bucket: aws.String(a.config.Container), Key: aws.String(ref.Key), VersionId: optionalVersion(ref.VersionID)}
	body, err := client.GetObject(ctx, readback)
	if err != nil {
		return 0, sideEffectError("Wasabi copy could not be read back for verification")
	}
	// The result rests on the bytes read and their digest; a failure to close
	// the response body afterwards cannot change it.
	defer body.Body.Close()
	hasher := sha256.New()
	count, err := io.Copy(hasher, io.LimitReader(body.Body, input.ExpectedSizeBytes+1))
	if err != nil || !copiedAsExpected(count, hasher.Sum(nil), input) {
		return 0, sideEffectError("Wasabi copy does not match the expected size and checksum")
	}
	return count, nil
}

func copiedAsExpected(count int64, digest []byte, input copyInput) bool {
	return count == input.ExpectedSizeBytes && strings.EqualFold(hex.EncodeToString(digest), input.ExpectedChecksum.Digest)
}

func copyOutput(destination objectTarget, ref objectReference, count int64, result *s3.CopyObjectOutput) map[string]any {
	value := map[string]any{"destination": destination, "copy_state": "copied", "object_ref": ref, "bytes_copied": count, "observed_at": now()}
	if result.CopyObjectResult != nil {
		addProviderETag(value, result.CopyObjectResult.ETag)
	}
	return value
}

// escapeKey URL-encodes each key segment for the CopySource header while keeping "/" separators.
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
