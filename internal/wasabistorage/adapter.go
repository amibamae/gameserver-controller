// Package wasabistorage implements the bounded object-transfer capabilities
// of the controller's Wasabi S3-compatible storage adapter.
package wasabistorage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	contractVersion   = "1"
	maxS3ObjectSize   = int64(5 * 1024 * 1024 * 1024 * 1024)
	maxMultipartParts = int64(10000)
	maxPartSize       = int64(128 * 1024 * 1024)
	minPartSize       = int64(16 * 1024 * 1024)
)

// Config contains non-secret settings for one environment-specific Wasabi
// storage profile. Credential references point to separate systemd
// credentials containing the access key ID and secret access key.
type Config struct {
	AdapterID          string
	StorageProfileID   string
	EnvironmentID      string
	Container          string
	KeyPrefix          string
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
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	CopyObject(context.Context, *s3.CopyObjectInput, ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
	GetObjectRetention(context.Context, *s3.GetObjectRetentionInput, ...func(*s3.Options)) (*s3.GetObjectRetentionOutput, error)
	GetObjectLegalHold(context.Context, *s3.GetObjectLegalHoldInput, ...func(*s3.Options)) (*s3.GetObjectLegalHoldOutput, error)
}

type clientFactory func(region, endpoint, accessKeyID, secretAccessKey string) s3Client

// Adapter advertises put, get, head, list, copy, exact-version delete, and
// versioning and retention inspection for one storage profile. Checksum
// verification without a transfer is not advertised.
type Adapter struct {
	config          Config
	protocolVersion string
	credentials     CredentialResolver
	newClient       clientFactory
	// tokenKey signs list continuation tokens; tokens do not survive a restart.
	tokenKey [32]byte
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
	impl := &Adapter{config: config, protocolVersion: protocolVersion, credentials: resolver, newClient: factory}
	if _, err := rand.Read(impl.tokenKey[:]); err != nil {
		return nil, errors.New("could not create a continuation-token key")
	}
	return impl, nil
}

// ValidateConfig checks a profile without contacting Wasabi or resolving credentials.
func ValidateConfig(config Config) error { return validateConfig(config) }

func (a *Adapter) Descriptor() adapter.Descriptor {
	version := a.protocolVersion
	plain := func(name, schema string, mutating bool) adapter.Capability {
		return adapter.Capability{
			Name: name, Version: contractVersion, Mutating: mutating,
			InputSchemaURI:      "urn:gameserver-controller:" + version + ":storage:" + schema + "-input",
			OutputSchemaURI:     "urn:gameserver-controller:" + version + ":storage:" + schema + "-output",
			RequiredPermissions: []string{},
		}
	}
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
			plain("storage.object.head", "object-head", false),
			plain("storage.object.list", "object-list", false),
			plain("storage.object.copy", "object-copy", true),
			plain("storage.object.delete", "object-delete", true),
			plain("storage.object.versioning.inspect", "object-versioning-inspect", false),
			plain("storage.object.retention.inspect", "object-retention-inspect", false),
		},
	}
}

func (a *Adapter) Invoke(ctx context.Context, request adapter.InvokeRequest) (adapter.InvokeResponse, error) {
	if ctx == nil || a == nil {
		return adapter.InvokeResponse{}, errors.New("storage adapter and invocation context are required")
	}
	deadline := request.Params.RequestContext.Deadline
	if deadline.IsZero() || !deadline.After(time.Now()) {
		return adapter.InvokeResponse{}, errors.New("a future operation deadline is required")
	}
	callContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	handlers := map[string]func(context.Context, adapter.InvokeRequest) (any, error){
		"storage.object.put": a.put, "storage.object.get": a.get, "storage.object.head": a.head,
		"storage.object.list": a.list, "storage.object.copy": a.copyObject, "storage.object.delete": a.deleteObject,
		"storage.object.versioning.inspect": a.inspectVersioning, "storage.object.retention.inspect": a.inspectRetention,
	}
	handler, ok := handlers[request.Params.Capability]
	if !ok {
		return adapter.InvokeResponse{}, errors.New("unsupported Wasabi storage capability")
	}
	output, err := handler(callContext, request)
	if err != nil {
		return adapter.InvokeResponse{}, err
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

// confirmedVersion returns the exact version a read observed. A requested
// version must be echoed by the provider; with none requested, the observed
// version is kept when it is immutable.
func confirmedVersion(requested string, observed *string) (string, error) {
	if requested == "" {
		return observedVersion(observed), nil
	}
	if requested == "null" {
		return "", errors.New("the mutable null object version is not accepted as an exact version")
	}
	if observed == nil || *observed != requested {
		return "", errors.New("Wasabi did not confirm the requested object version")
	}
	return requested, nil
}

// observedVersion returns the provider's version ID, or "" when it reported
// none or only the mutable "null" version.
func observedVersion(observed *string) string {
	if observed != nil && immutableVersion(*observed) {
		return *observed
	}
	return ""
}

func immutableVersion(version string) bool {
	return version != "" && version != "null"
}

// missingRequiredVersion reports a profile that requires version IDs meeting
// an object without an immutable one.
func (a *Adapter) missingRequiredVersion(version string) bool {
	return a.config.RequireVersionIDs && !immutableVersion(version)
}

// decodeInput decodes a capability's input and applies its acceptance check;
// a malformed or refused input reports only the capability's rejection text.
func decodeInput[T any](request adapter.InvokeRequest, rejection string, accept func(T) bool) (T, error) {
	var input T
	if err := json.Unmarshal(request.Params.Input, &input); err != nil || !accept(input) {
		var refused T
		return refused, errors.New(rejection)
	}
	return input, nil
}

// acceptsTransfer reports a request from this profile's environment for an
// object inside its bucket and prefix, with a bounded size and a SHA-256.
func (a *Adapter) acceptsTransfer(request adapter.InvokeRequest, target objectTarget, size int64, expected checksum) bool {
	return a.validRequestEnvironment(request) && a.validTarget(target) && validExpected(size, expected, a.config.MaxObjectBytes)
}

func addProviderETag(value map[string]any, etag *string) {
	if etag != nil && *etag != "" {
		value["provider_etag"] = *etag
	}
}

func (a *Adapter) validTarget(value objectTarget) bool {
	return value.AdapterID == a.config.AdapterID && value.StorageProfile == a.config.StorageProfileID && value.EnvironmentID == a.config.EnvironmentID && value.Container == a.config.Container && strings.HasPrefix(value.Key, a.config.KeyPrefix)
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
		HTTPClient:  http.DefaultClient,
	}
	return s3.NewFromConfig(configuration, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	})
}
