package wasabistorage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// put stages and verifies the controller's stream before anything reaches
// Wasabi, then uploads the verified copy.
func (a *Adapter) put(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	input, err := decodeInput(request, "invalid object upload request", func(input putInput) bool {
		return a.acceptsTransfer(request, input.Target, input.ExpectedSizeBytes, input.ExpectedChecksum)
	})
	if err != nil {
		return nil, err
	}
	result, err := a.uploadVerified(ctx, request, input)
	if err != nil {
		return nil, err
	}
	return a.putOutput(input, result)
}

// uploadVerified claims the upload stream, stages it privately, and uploads
// the staged copy. The staging file is closed and removed, then the stream is
// closed, on every path after each was opened.
func (a *Adapter) uploadVerified(ctx context.Context, request adapter.InvokeRequest, input putInput) (*manager.UploadOutput, error) {
	transfer, err := singleTransfer(request, "controller_to_adapter", input.ExpectedSizeBytes)
	if err != nil {
		return nil, err
	}
	stream, err := request.Params.RequestContext.OpenStream(transfer.TransferID)
	if err != nil {
		return nil, errors.New("upload stream is unavailable")
	}
	// Closing only releases the controller's stream; by then its bytes have
	// been staged or rejected, so a Close error cannot change the result.
	defer stream.Close()
	staged, err := stageVerifiedUpload(stream, input.ExpectedSizeBytes, input.ExpectedChecksum.Digest)
	if err != nil {
		return nil, errors.New("upload stream did not match its declared size and checksum")
	}
	defer discardStaged(staged)
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	return uploadStaged(ctx, client, input, staged)
}

func uploadStaged(ctx context.Context, client s3Client, input putInput, staged *os.File) (*manager.UploadOutput, error) {
	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = partSize(input.ExpectedSizeBytes)
		u.Concurrency = 1
		u.LeavePartsOnError = false
	})
	result, err := uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket: aws.String(input.Target.Container), Key: aws.String(input.Target.Key),
		Body: staged, ContentLength: aws.Int64(input.ExpectedSizeBytes),
		ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
	})
	if err != nil {
		return nil, sideEffectError("Wasabi upload failed")
	}
	return result, nil
}

func (a *Adapter) putOutput(input putInput, result *manager.UploadOutput) (any, error) {
	ref := objectReference{objectTarget: input.Target, VersionID: observedVersion(result.VersionID)}
	if a.missingRequiredVersion(ref.VersionID) {
		return nil, sideEffectError("Wasabi did not return the required immutable object version")
	}
	value := map[string]any{"target": input.Target, "upload_state": "uploaded", "object_ref": ref,
		"bytes_received": input.ExpectedSizeBytes, "observed_at": now()}
	addProviderETag(value, result.ETag)
	return value, nil
}

// stageVerifiedUpload copies at most one byte more than expected into a
// private temporary file and returns it rewound only when its size and
// SHA-256 digest match. A rejected copy is closed and removed.
func stageVerifiedUpload(stream io.Reader, expectedSize int64, expectedDigest string) (*os.File, error) {
	file, err := os.CreateTemp("", "gsc-wasabi-upload-*")
	if err != nil {
		return nil, errors.New("private upload staging is unavailable")
	}
	hasher := sha256.New()
	count, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(stream, expectedSize+1))
	if err != nil || count != expectedSize || !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expectedDigest) {
		discardStaged(file)
		return nil, errors.New("upload data does not match the declared size and checksum")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		discardStaged(file)
		return nil, errors.New("private upload staging could not be rewound")
	}
	return file, nil
}

// discardStaged closes and removes private upload staging. Neither error can
// change the result: the upload has finished or been refused, and removing
// the name does not depend on Close succeeding.
func discardStaged(file *os.File) {
	_ = file.Close()
	_ = os.Remove(file.Name())
}

func partSize(objectSize int64) int64 {
	partSize := minPartSize
	if required := (objectSize + maxMultipartParts - 1) / maxMultipartParts; required > partSize {
		partSize = required
	}
	return partSize
}
