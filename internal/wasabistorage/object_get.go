package wasabistorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// downloaded is what a verified read observed.
type downloaded struct {
	count   int64
	digest  string
	version *string
}

// get streams an object to the controller while hashing it, and reports it
// complete only when its size, checksum, and version all match.
func (a *Adapter) get(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	input, err := a.decodeGet(request)
	if err != nil {
		return nil, err
	}
	transfer, err := singleTransfer(request, "adapter_to_controller", input.ExpectedSizeBytes)
	if err != nil {
		return nil, err
	}
	sink, err := request.Params.RequestContext.CreateStream(transfer.TransferID)
	if err != nil {
		return nil, errors.New("download stream is unavailable")
	}
	// Closing only releases the controller's stream; the result already
	// reflects every byte written to it, so a Close error cannot change it.
	defer sink.Close()
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	observed, err := downloadVerified(ctx, client, input, sink)
	if err != nil {
		return nil, err
	}
	return a.getOutput(input, observed)
}

func (a *Adapter) decodeGet(request adapter.InvokeRequest) (getInput, error) {
	input, err := decodeInput(request, "invalid object download request", func(input getInput) bool {
		return a.acceptsTransfer(request, input.ObjectRef.objectTarget, input.ExpectedSizeBytes, input.ExpectedChecksum)
	})
	if err != nil {
		return getInput{}, err
	}
	if a.config.RequireVersionIDs && input.ObjectRef.VersionID == "" {
		return getInput{}, errors.New("an exact object version is required by this storage profile")
	}
	return input, nil
}

// downloadVerified copies at most one byte more than expected to sink and
// checks the size and SHA-256 digest. The provider body is closed on every
// path after it was opened.
func downloadVerified(ctx context.Context, client s3Client, input getInput, sink io.Writer) (downloaded, error) {
	ref := input.ObjectRef
	result, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(ref.Container), Key: aws.String(ref.Key), VersionId: optionalVersion(ref.VersionID)})
	if err != nil {
		return downloaded{}, errors.New("Wasabi object download failed")
	}
	// The body is read to its bound before closing, so a Close error cannot
	// change the verified result.
	defer result.Body.Close()
	hasher := sha256.New()
	count, err := io.Copy(io.MultiWriter(sink, hasher), io.LimitReader(result.Body, input.ExpectedSizeBytes+1))
	if err != nil || count != input.ExpectedSizeBytes {
		return downloaded{}, errors.New("Wasabi object size does not match the backup manifest")
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(digest, input.ExpectedChecksum.Digest) {
		return downloaded{}, errors.New("Wasabi object checksum does not match the backup manifest")
	}
	return downloaded{count: count, digest: digest, version: result.VersionId}, nil
}

func (a *Adapter) getOutput(input getInput, observed downloaded) (any, error) {
	ref := input.ObjectRef
	version, err := confirmedVersion(input.ObjectRef.VersionID, observed.version)
	if err != nil {
		return nil, err
	}
	ref.VersionID = version
	// decodeGet already requires an exact version under this profile, so
	// this check cannot fail today; it stays as a guard on the output.
	if a.missingRequiredVersion(ref.VersionID) {
		return nil, errors.New("Wasabi object did not resolve to an immutable version")
	}
	return map[string]any{"object_ref": ref, "transfer_state": "complete", "bytes_sent": observed.count,
		"computed_checksum": checksum{Algorithm: "sha256", Digest: observed.digest}, "observed_at": now()}, nil
}

// optionalVersion leaves the version unset when none was requested.
func optionalVersion(version string) *string {
	if version == "" {
		return nil
	}
	return aws.String(version)
}
