// Package backupintegrity provides bounded readback verification of
// controller-owned backups from a separately configured S3-compatible store.
package backupintegrity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/amibamae/gameserver-controller/internal/workflow"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Getter is the narrow S3 client required for remote object readback.
type Getter interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// WasabiReader reads the complete remote object and computes its own SHA-256.
// It never returns bytes, log messages or storage credentials.
type WasabiReader struct {
	client      Getter
	adapter     string
	profile     string
	environment string
	bucket      string
	maxBytes    int64
}

func NewWasabiReader(client Getter, adapterID, profile, environment, bucket string, maxBytes int64) (*WasabiReader, error) {
	if client == nil || adapterID == "" || profile == "" || environment == "" || bucket == "" || maxBytes < 1 {
		return nil, errors.New("remote S3 client, exact adapter/profile/environment/bucket and byte limit are required")
	}
	return &WasabiReader{
		client: client, adapter: adapterID, profile: profile, environment: environment,
		bucket: bucket, maxBytes: maxBytes,
	}, nil
}

type objectRef struct {
	AdapterID        string `json:"adapter_id"`
	StorageProfileID string `json:"storage_profile_id"`
	EnvironmentID    string `json:"environment_id"`
	Container        string `json:"container"`
	Key              string `json:"key"`
	VersionID        string `json:"version_id"`
}

func (r *WasabiReader) Readback(ctx context.Context, raw json.RawMessage) (workflow.BackupObjectReadback, error) {
	if ctx == nil {
		return workflow.BackupObjectReadback{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return workflow.BackupObjectReadback{}, err
	}
	var ref objectRef
	if json.Unmarshal(raw, &ref) != nil || !r.matches(ref) {
		return workflow.BackupObjectReadback{}, errors.New("remote object reference does not match the configured Wasabi scope")
	}
	input := &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(ref.Key)}
	if ref.VersionID != "" {
		input.VersionId = aws.String(ref.VersionID)
	}
	output, err := r.client.GetObject(ctx, input)
	if err != nil {
		return workflow.BackupObjectReadback{}, errors.New("remote backup object could not be read")
	}
	if output == nil || output.Body == nil {
		return workflow.BackupObjectReadback{}, errors.New("remote backup object has no content")
	}
	defer output.Body.Close()
	digest := sha256.New()
	read, err := io.Copy(digest, io.LimitReader(output.Body, r.maxBytes+1))
	if err != nil {
		return workflow.BackupObjectReadback{}, fmt.Errorf("hash remote backup object: %w", err)
	}
	if read > r.maxBytes || (output.ContentLength != nil && read != *output.ContentLength) {
		return workflow.BackupObjectReadback{}, errors.New("remote backup object is truncated or exceeds configured bound")
	}
	return workflow.BackupObjectReadback{
		Remote: true, SizeBytes: read, SHA256: hex.EncodeToString(digest.Sum(nil)),
	}, nil
}

func (r *WasabiReader) matches(ref objectRef) bool {
	return ref.AdapterID == r.adapter && ref.StorageProfileID == r.profile &&
		ref.EnvironmentID == r.environment && ref.Container == r.bucket &&
		ref.Key != ""
}
