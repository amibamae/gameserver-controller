// Package wasabistorage implements the bounded object-transfer capabilities
// of the controller's Wasabi S3-compatible storage adapter.
package wasabistorage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/credential"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	contractVersion   = "1"
	maxS3ObjectSize   = int64(5 * 1024 * 1024 * 1024 * 1024)
	maxMultipartParts = int64(10000)
	maxPartSize       = int64(128 * 1024 * 1024)
	minPartSize       = int64(16 * 1024 * 1024)
)

var (
	adapterIDPattern = regexp.MustCompile(`^[a-z][a-z0-9.-]{1,159}$`)
	regionPattern    = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

// Config contains non-secret settings for one environment-specific Wasabi
// storage profile. Credential references point to separate systemd
// credentials containing the access key ID and secret access key.
type Config struct {
	AdapterID          string
	StorageProfileID   string
	EnvironmentID      string
	Region             string
	EndpointURL        string
	AccessKeyIDRef     string
	SecretAccessKeyRef string
	MaxObjectBytes     int64
	RequireVersionIDs  bool
}

// CredentialResolver resolves only references explicitly supplied in this
// profile. Values are passed to the SDK for the duration of an invocation.
type CredentialResolver interface {
	WithCredential(string, func([]byte) error) error
}

type s3Client interface {
	manager.UploadAPIClient
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
}

type clientFactory func(region, endpoint, accessKeyID, secretAccessKey string) s3Client

// Adapter advertises only put, get, and head. Listing, deletion, copying,
// versioning policy, and retention inspection remain unimplemented and are
// deliberately not advertised.
type Adapter struct {
	config          Config
	protocolVersion string
	credentials     CredentialResolver
	newClient       clientFactory
}

// New constructs an adapter using the systemd credential resolver and the AWS
// SDK's S3 client. protocolVersion must match the loaded protocol registry.
func New(config Config, protocolVersion string, resolver CredentialResolver) (*Adapter, error) {
	return newAdapter(config, protocolVersion, resolver, newS3Client)
}

func newAdapter(config Config, protocolVersion string, resolver CredentialResolver, factory clientFactory) (*Adapter, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if protocolVersion == "" || resolver == nil || factory == nil {
		return nil, errors.New("protocol version, credential resolver, and S3 client factory are required")
	}
	return &Adapter{config: config, protocolVersion: protocolVersion, credentials: resolver, newClient: factory}, nil
}

func validateConfig(config Config) error {
	if !adapterIDPattern.MatchString(config.AdapterID) || !strings.HasPrefix(config.AdapterID, "storage.") || config.StorageProfileID == "" || config.EnvironmentID == "" || !regionPattern.MatchString(config.Region) {
		return errors.New("storage profile, environment, and Wasabi region are required")
	}
	if config.MaxObjectBytes < 1 || config.MaxObjectBytes > maxS3ObjectSize || config.MaxObjectBytes > maxPartSize*maxMultipartParts {
		return errors.New("maximum object size must be between 1 byte and the bounded multipart limit")
	}
	if !strings.HasPrefix(config.AccessKeyIDRef, credential.SystemdReferenceScheme) ||
		!strings.HasPrefix(config.SecretAccessKeyRef, credential.SystemdReferenceScheme) ||
		config.AccessKeyIDRef == config.SecretAccessKeyRef {
		return errors.New("separate systemd credential references for both Wasabi keys are required")
	}
	endpoint, err := url.Parse(config.EndpointURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Hostname() == "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("Wasabi endpoint must be an HTTPS URL without user information, query, or fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	if net.ParseIP(host) != nil || !(host == "wasabisys.com" || strings.HasSuffix(host, ".wasabisys.com")) {
		return errors.New("Wasabi endpoint host must be within wasabisys.com")
	}
	if endpoint.Port() != "" && endpoint.Port() != "443" {
		return errors.New("Wasabi endpoint must use HTTPS port 443")
	}
	return nil
}

func (a *Adapter) Descriptor() adapter.Descriptor {
	version := a.protocolVersion
	capability := func(name, schema string, mutating bool, direction string) adapter.Capability {
		return adapter.Capability{
			Name: name, Version: contractVersion, Mutating: mutating,
			InputSchemaURI:      "urn:gameserver-controller:" + version + ":storage:" + schema + "-input",
			OutputSchemaURI:     "urn:gameserver-controller:" + version + ":storage:" + schema + "-output",
			Constraints:         map[string]any{"stream_transfer_direction": direction, "maximum_object_bytes": a.config.MaxObjectBytes},
			RequiredPermissions: []string{},
		}
	}
	return adapter.Descriptor{
		AdapterID: a.config.AdapterID, Category: adapter.CategoryStorage, DisplayName: "Wasabi S3-compatible object storage",
		ImplementationVersion: "0.1.0", ProtocolVersions: []string{version}, ContractVersions: []string{contractVersion},
		RequiredPermissions: []string{}, StreamTransportProfiles: []string{"local-unix-stream-v1"},
		Capabilities: []adapter.Capability{
			capability("storage.object.put", "object-put", true, "controller_to_adapter"),
			capability("storage.object.get", "object-get", false, "adapter_to_controller"),
			{Name: "storage.object.head", Version: contractVersion, Mutating: false,
				InputSchemaURI:  "urn:gameserver-controller:" + version + ":storage:object-head-input",
				OutputSchemaURI: "urn:gameserver-controller:" + version + ":storage:object-head-output", RequiredPermissions: []string{}},
		},
	}
}

func (a *Adapter) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if ctx == nil || a == nil {
		return adapter.InvokeResponse{}, errors.New("storage adapter and invocation context are required")
	}
	var output any
	switch request.Params.Capability {
	case "storage.object.put":
		value, err := a.put(ctx, request)
		if err != nil {
			return adapter.InvokeResponse{}, err
		}
		output = value
	case "storage.object.get":
		value, err := a.get(ctx, request)
		if err != nil {
			return adapter.InvokeResponse{}, err
		}
		output = value
	case "storage.object.head":
		value, err := a.head(ctx, request)
		if err != nil {
			return adapter.InvokeResponse{}, err
		}
		output = value
	default:
		return adapter.InvokeResponse{}, errors.New("unsupported Wasabi storage capability")
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return adapter.InvokeResponse{}, errors.New("could not encode Wasabi storage result")
	}
	return adapter.InvokeResponse{JSONRPC: "2.0", ID: request.ID, Result: adapter.InvokeResult{
		Outcome: adapter.OutcomeCompleted, Output: encoded, ObservedAt: time.Now().UTC(),
	}}, nil
}

type objectTarget struct {
	AdapterID      string `json:"adapter_id"`
	StorageProfile string `json:"storage_profile_id"`
	EnvironmentID  string `json:"environment_id"`
	Container      string `json:"container"`
	Key            string `json:"key"`
}

type objectReference struct {
	objectTarget
	VersionID string `json:"version_id,omitempty"`
}

type checksum struct {
	Algorithm string `json:"algorithm"`
	Digest    string `json:"digest"`
}

type putInput struct {
	Target            objectTarget `json:"target"`
	ExpectedSizeBytes int64        `json:"expected_size_bytes"`
	ExpectedChecksum  checksum     `json:"expected_checksum"`
}

type getInput struct {
	ObjectRef         objectReference `json:"object_ref"`
	ExpectedSizeBytes int64           `json:"expected_size_bytes"`
	ExpectedChecksum  checksum        `json:"expected_checksum"`
}

type headInput struct {
	ObjectRef objectReference `json:"object_ref"`
}

func (a *Adapter) put(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	var input putInput
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !a.validRequestEnvironment(request) || !a.validTarget(input.Target) || !validExpected(input.ExpectedSizeBytes, input.ExpectedChecksum, a.config.MaxObjectBytes) {
		return nil, errors.New("invalid object upload request")
	}
	transfer, err := singleTransfer(request, "controller_to_adapter", input.ExpectedSizeBytes)
	if err != nil {
		return nil, err
	}
	stream, err := request.Params.RequestContext.OpenStream(transfer.TransferID)
	if err != nil {
		return nil, errors.New("upload stream is unavailable")
	}
	defer stream.Close()
	staged, err := stageVerifiedUpload(stream, input.ExpectedSizeBytes, input.ExpectedChecksum.Digest)
	if err != nil {
		return nil, errors.New("upload stream did not match its declared size and checksum")
	}
	defer func() {
		_ = staged.Close()
		_ = os.Remove(staged.Name())
	}()
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
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
	ref := objectReference{objectTarget: input.Target}
	if result.VersionID != nil {
		ref.VersionID = *result.VersionID
	}
	if a.config.RequireVersionIDs && (ref.VersionID == "" || ref.VersionID == "null") {
		return nil, sideEffectError("Wasabi did not return the required immutable object version")
	}
	value := map[string]any{"target": input.Target, "upload_state": "uploaded", "object_ref": ref,
		"bytes_received": input.ExpectedSizeBytes, "observed_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if result.ETag != nil && *result.ETag != "" {
		value["provider_etag"] = *result.ETag
	}
	return value, nil
}

func stageVerifiedUpload(stream io.Reader, expectedSize int64, expectedDigest string) (*os.File, error) {
	file, err := os.CreateTemp("", "gsc-wasabi-upload-*")
	if err != nil {
		return nil, errors.New("private upload staging is unavailable")
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	hasher := sha256.New()
	count, err := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(stream, expectedSize+1))
	if err != nil || count != expectedSize || !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expectedDigest) {
		cleanup()
		return nil, errors.New("upload data does not match the declared size and checksum")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, errors.New("private upload staging could not be rewound")
	}
	return file, nil
}

func partSize(objectSize int64) int64 {
	partSize := minPartSize
	if required := (objectSize + maxMultipartParts - 1) / maxMultipartParts; required > partSize {
		partSize = required
	}
	return partSize
}

func (a *Adapter) get(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	var input getInput
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !a.validRequestEnvironment(request) || !a.validReference(input.ObjectRef) || !validExpected(input.ExpectedSizeBytes, input.ExpectedChecksum, a.config.MaxObjectBytes) {
		return nil, errors.New("invalid object download request")
	}
	if a.config.RequireVersionIDs && input.ObjectRef.VersionID == "" {
		return nil, errors.New("an exact object version is required by this storage profile")
	}
	transfer, err := singleTransfer(request, "adapter_to_controller", input.ExpectedSizeBytes)
	if err != nil {
		return nil, err
	}
	sink, err := request.Params.RequestContext.CreateStream(transfer.TransferID)
	if err != nil {
		return nil, errors.New("download stream is unavailable")
	}
	defer sink.Close()
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	get := &s3.GetObjectInput{Bucket: aws.String(input.ObjectRef.Container), Key: aws.String(input.ObjectRef.Key)}
	if input.ObjectRef.VersionID != "" {
		get.VersionId = aws.String(input.ObjectRef.VersionID)
	}
	result, err := client.GetObject(ctx, get)
	if err != nil {
		return nil, errors.New("Wasabi object download failed")
	}
	defer result.Body.Close()
	hasher := sha256.New()
	writer := io.MultiWriter(sink, hasher)
	count, err := io.Copy(writer, io.LimitReader(result.Body, input.ExpectedSizeBytes+1))
	if err != nil || count != input.ExpectedSizeBytes {
		return nil, errors.New("Wasabi object size does not match the backup manifest")
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(digest, input.ExpectedChecksum.Digest) {
		return nil, errors.New("Wasabi object checksum does not match the backup manifest")
	}
	ref := input.ObjectRef
	if result.VersionId != nil && *result.VersionId != "" {
		if ref.VersionID != "" && ref.VersionID != *result.VersionId {
			return nil, errors.New("Wasabi returned a different object version")
		}
		ref.VersionID = *result.VersionId
	}
	if a.config.RequireVersionIDs && (ref.VersionID == "" || ref.VersionID == "null") {
		return nil, errors.New("Wasabi object did not resolve to an immutable version")
	}
	return map[string]any{"object_ref": ref, "transfer_state": "complete", "bytes_sent": count,
		"computed_checksum": checksum{Algorithm: "sha256", Digest: digest}, "observed_at": time.Now().UTC().Format(time.RFC3339Nano)}, nil
}

func (a *Adapter) head(ctx context.Context, request adapter.InvokeRequest) (any, error) {
	var input headInput
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !a.validRequestEnvironment(request) || !a.validReference(input.ObjectRef) {
		return nil, errors.New("invalid object inspection request")
	}
	client, err := a.openClient(ctx, request)
	if err != nil {
		return nil, err
	}
	head := &s3.HeadObjectInput{Bucket: aws.String(input.ObjectRef.Container), Key: aws.String(input.ObjectRef.Key)}
	if input.ObjectRef.VersionID != "" {
		head.VersionId = aws.String(input.ObjectRef.VersionID)
	}
	result, err := client.HeadObject(ctx, head)
	if err != nil {
		var missing interface{ ErrorCode() string }
		if errors.As(err, &missing) && (missing.ErrorCode() == "NotFound" || missing.ErrorCode() == "NoSuchKey") {
			return map[string]any{"object_ref": input.ObjectRef, "object_state": "absent", "observed_at": time.Now().UTC().Format(time.RFC3339Nano)}, nil
		}
		return nil, errors.New("Wasabi object inspection failed")
	}
	ref := input.ObjectRef
	if result.VersionId != nil && *result.VersionId != "" {
		if ref.VersionID != "" && ref.VersionID != *result.VersionId {
			return nil, errors.New("Wasabi returned a different object version")
		}
		ref.VersionID = *result.VersionId
	}
	if a.config.RequireVersionIDs && (ref.VersionID == "" || ref.VersionID == "null") {
		return nil, errors.New("Wasabi object has no immutable version ID")
	}
	value := map[string]any{"object_ref": ref, "object_state": "present", "observed_at": time.Now().UTC().Format(time.RFC3339Nano)}
	if result.ContentLength != nil {
		value["size_bytes"] = *result.ContentLength
	}
	if result.ETag != nil && *result.ETag != "" {
		value["provider_etag"] = *result.ETag
	}
	if result.LastModified != nil {
		value["last_modified"] = result.LastModified.UTC().Format(time.RFC3339Nano)
	}
	if result.ChecksumSHA256 != nil {
		decoded, decodeErr := base64.StdEncoding.DecodeString(*result.ChecksumSHA256)
		if decodeErr == nil && len(decoded) == sha256.Size {
			value["provider_checksum"] = checksum{Algorithm: "sha256", Digest: hex.EncodeToString(decoded)}
		}
	}
	return value, nil
}

func (a *Adapter) validTarget(value objectTarget) bool {
	return value.AdapterID == a.config.AdapterID && value.StorageProfile == a.config.StorageProfileID && value.EnvironmentID == a.config.EnvironmentID && value.Container != "" && value.Key != ""
}

func (a *Adapter) validRequestEnvironment(request adapter.InvokeRequest) bool {
	return request.Params.RequestContext.EnvironmentID == a.config.EnvironmentID
}

func (a *Adapter) validReference(value objectReference) bool {
	return a.validTarget(value.objectTarget)
}

func validExpected(size int64, expected checksum, maximum int64) bool {
	return size >= 0 && size <= maximum && expected.Algorithm == "sha256" && len(expected.Digest) == sha256.Size*2 && isHex(expected.Digest)
}

func isHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func singleTransfer(request adapter.InvokeRequest, direction string, expectedSize int64) (capturestream.Reference, error) {
	if len(request.Params.RequestContext.StreamTransfers) != 1 {
		return capturestream.Reference{}, errors.New("storage transfer requires exactly one stream handle")
	}
	transfer := request.Params.RequestContext.StreamTransfers[0]
	if transfer.Direction != direction || transfer.MaximumBytes < expectedSize || !transfer.ExpiresAt.After(time.Now()) || transfer.ExpiresAt.After(request.Params.RequestContext.Deadline) {
		return capturestream.Reference{}, errors.New("storage stream handle does not match the bounded transfer")
	}
	return transfer, nil
}

func (a *Adapter) openClient(ctx context.Context, request adapter.InvokeRequest) (s3Client, error) {
	refs := request.Params.RequestContext.SecretRefs
	if len(refs) != 2 || !containsSecretRef(refs, a.config.AccessKeyIDRef) || !containsSecretRef(refs, a.config.SecretAccessKeyRef) {
		return nil, errors.New("Wasabi credential references are not authorized for this invocation")
	}
	var client s3Client
	err := a.credentials.WithCredential(a.config.AccessKeyIDRef, func(access []byte) error {
		return a.credentials.WithCredential(a.config.SecretAccessKeyRef, func(secret []byte) error {
			if len(access) == 0 || len(secret) == 0 {
				return errors.New("credential unavailable")
			}
			client = a.newClient(a.config.Region, a.config.EndpointURL, string(access), string(secret))
			return nil
		})
	})
	if err != nil || client == nil || ctx.Err() != nil {
		return nil, errors.New("Wasabi credentials are unavailable")
	}
	return client, nil
}

func containsSecretRef(refs []adapter.SecretReference, reference string) bool {
	for _, value := range refs {
		if value.SecretRef == reference {
			return true
		}
	}
	return false
}

func sideEffectError(message string) error {
	return &adapter.InvocationError{MayHaveSideEffects: true, Cause: errors.New(message)}
}

func newS3Client(region, endpoint, accessKeyID, secretAccessKey string) s3Client {
	configuration := aws.Config{
		Region:      region,
		Credentials: aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
		HTTPClient:  &http.Client{Timeout: 2 * time.Minute},
	}
	return s3.NewFromConfig(configuration, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
}
