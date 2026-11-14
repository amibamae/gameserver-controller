package wasabistorage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// These characterization tests pin the put, get, and head paths: exact error
// text, whether a failure may have side effects, which streams are closed, and
// that private upload staging never outlives a call.

// storageR3Ref is the encoded test object reference without its closing brace.
const storageR3Ref = `{"adapter_id":"storage.wasabi-staging","storage_profile_id":"staging-storage","environment_id":"staging","container":"backups","key":"server/world.tar"`

var storageR3Content = []byte("world archive bytes")

func storageR3PutInput() putInput {
	return putInput{Target: storageR3Target("server/world.tar"), ExpectedSizeBytes: int64(len(storageR3Content)), ExpectedChecksum: storageR3Checksum(storageR3Content)}
}

// storageR3PutRequest builds a put request whose one upload stream carries
// content and counts its Close calls.
func storageR3PutRequest(input putInput, content []byte) (adapter.InvokeRequest, *storageR3Reader) {
	request := requestFor("storage.object.put", input, time.Now().Add(time.Minute), "controller_to_adapter", int64(len(storageR3Content)))
	source := &storageR3Reader{Reader: bytes.NewReader(content)}
	request.Params.RequestContext.SetStreamSources(map[string]io.ReadCloser{testTransferID(): source})
	return request, source
}

type storageR3PutCase struct {
	name       string
	config     func(*Config)
	client     func(*storageR3Client)
	creds      CredentialResolver
	content    []byte // stream content; defaults to the declared content
	input      func(*putInput)
	request    func(*adapter.InvokeRequest)
	noSource   bool
	ctx        func() context.Context
	want       string
	sideEffect bool
	opened     bool // the upload stream was claimed and must be closed once
	putCalls   int
}

func (c storageR3PutCase) run(t *testing.T) {
	tempDir := storageR3PrivateTemp(t)
	client := storageR3NewClient()
	client.tempDir = tempDir
	storageR3Apply(c.client, client)
	impl := storageR3Adapter(t, storageR3Config(c.config), client, c.creds, nil)
	input := storageR3PutInput()
	storageR3Apply(c.input, &input)
	content := storageR3Content
	if c.content != nil {
		content = c.content
	}
	request, source := storageR3PutRequest(input, content)
	if c.noSource {
		request.Params.RequestContext.ClearStreamSources()
	}
	storageR3Apply(c.request, &request)
	response, err := impl.Invoke(storageR3Context(c.ctx), request)
	storageR3Failed(t, response, err, c.want, c.sideEffect)
	storageR3Equal(t, "upload stream closes", source.closed, storageR3Times(c.opened))
	storageR3Equal(t, "provider uploads", client.putCalls, c.putCalls)
	storageR3NoStaging(t, tempDir)
}

func TestStorageR3PutPaths(t *testing.T) {
	invalid := "invalid object upload request"
	transfer := "storage stream handle does not match the bounded transfer"
	staging := "upload stream did not match its declared size and checksum"
	unauthorized := "Wasabi credential references are not authorized for this invocation"
	required := func(c *Config) { c.RequireVersionIDs = true }
	tests := []storageR3PutCase{
		{name: "malformed input", request: func(r *adapter.InvokeRequest) { r.Params.Input = json.RawMessage(`{`) }, want: invalid},
		{name: "unknown shape", request: func(r *adapter.InvokeRequest) { r.Params.Input = json.RawMessage(`[]`) }, want: invalid},
		{name: "wrong environment", request: func(r *adapter.InvokeRequest) { r.Params.RequestContext.EnvironmentID = "production" }, want: invalid},
		{name: "target adapter", input: func(i *putInput) { i.Target.AdapterID = "storage.other" }, want: invalid},
		{name: "target profile", input: func(i *putInput) { i.Target.StorageProfile = "other" }, want: invalid},
		{name: "target environment", input: func(i *putInput) { i.Target.EnvironmentID = "production" }, want: invalid},
		{name: "target container", input: func(i *putInput) { i.Target.Container = "other" }, want: invalid},
		{name: "target prefix", input: func(i *putInput) { i.Target.Key = "other/world.tar" }, want: invalid},
		{name: "negative size", input: func(i *putInput) { i.ExpectedSizeBytes = -1 }, want: invalid},
		{name: "size above profile", config: func(c *Config) { c.MaxObjectBytes = 4 }, want: invalid},
		{name: "checksum algorithm", input: func(i *putInput) { i.ExpectedChecksum.Algorithm = "md5" }, want: invalid},
		{name: "checksum length", input: func(i *putInput) { i.ExpectedChecksum.Digest = "abcd" }, want: invalid},
		{name: "checksum hex", input: func(i *putInput) { i.ExpectedChecksum.Digest = strings.Repeat("z", 64) }, want: invalid},
		{name: "no transfer", request: func(r *adapter.InvokeRequest) { r.Params.RequestContext.StreamTransfers = nil }, want: "storage transfer requires exactly one stream handle"},
		{name: "two transfers", request: func(r *adapter.InvokeRequest) {
			r.Params.RequestContext.StreamTransfers = append(r.Params.RequestContext.StreamTransfers, r.Params.RequestContext.StreamTransfers[0])
		}, want: "storage transfer requires exactly one stream handle"},
		{name: "wrong direction", request: func(r *adapter.InvokeRequest) {
			r.Params.RequestContext.StreamTransfers[0].Direction = "adapter_to_controller"
		}, want: transfer},
		{name: "transfer too small", request: func(r *adapter.InvokeRequest) { r.Params.RequestContext.StreamTransfers[0].MaximumBytes = 3 }, want: transfer},
		{name: "transfer expired", request: func(r *adapter.InvokeRequest) {
			r.Params.RequestContext.StreamTransfers[0].ExpiresAt = time.Now().Add(-time.Second)
		}, want: transfer},
		{name: "transfer outlives deadline", request: func(r *adapter.InvokeRequest) {
			r.Params.RequestContext.StreamTransfers[0].ExpiresAt = r.Params.RequestContext.Deadline.Add(time.Second)
		}, want: transfer},
		{name: "no stream source", noSource: true, want: "upload stream is unavailable"},
		{name: "short stream", content: storageR3Content[:3], want: staging, opened: true},
		{name: "long stream", content: append(append([]byte(nil), storageR3Content...), 'x'), want: staging, opened: true},
		{name: "wrong bytes", content: bytes.Repeat([]byte("x"), len(storageR3Content)), want: staging, opened: true},
		{name: "secret refs missing", request: func(r *adapter.InvokeRequest) {
			r.Params.RequestContext.SecretRefs = r.Params.RequestContext.SecretRefs[:1]
		},
			want: unauthorized, opened: true},
		{name: "secret refs foreign", request: func(r *adapter.InvokeRequest) {
			r.Params.RequestContext.SecretRefs[1].SecretRef = "systemd-credential://other"
		}, want: unauthorized, opened: true},
		{name: "secret refs extra", request: func(r *adapter.InvokeRequest) {
			r.Params.RequestContext.SecretRefs = append(r.Params.RequestContext.SecretRefs, r.Params.RequestContext.SecretRefs[0])
		}, want: unauthorized, opened: true},
		{name: "credential resolver fails", creds: storageR3Credentials{err: errors.New("no credential")}, want: "Wasabi credentials are unavailable", opened: true},
		{name: "credential empty", creds: storageR3Credentials{values: map[string]string{testConfig().AccessKeyIDRef: "access"}}, want: "Wasabi credentials are unavailable", opened: true},
		{name: "context canceled", ctx: storageR3Canceled, want: "Wasabi credentials are unavailable", opened: true},
		{name: "provider rejects", client: func(c *storageR3Client) { c.putErr = errors.New("boom") }, want: "Wasabi upload failed", sideEffect: true, opened: true, putCalls: 1},
		{name: "required version missing", config: required, client: func(c *storageR3Client) { c.putVersion = nil },
			want: "Wasabi did not return the required immutable object version", sideEffect: true, opened: true, putCalls: 1},
		{name: "required version null", config: required, client: func(c *storageR3Client) { c.putVersion = aws.String("null") },
			want: "Wasabi did not return the required immutable object version", sideEffect: true, opened: true, putCalls: 1},
		{name: "required version empty", config: required, client: func(c *storageR3Client) { c.putVersion = aws.String("") },
			want: "Wasabi did not return the required immutable object version", sideEffect: true, opened: true, putCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

type storageR3PutSuccess struct {
	name    string
	config  func(*Config)
	version *string
	etag    *string
	want    string
}

func (c storageR3PutSuccess) run(t *testing.T) {
	tempDir := storageR3PrivateTemp(t)
	config := storageR3Config(c.config)
	client := storageR3NewClient()
	client.tempDir = tempDir
	client.putVersion, client.putETag = c.version, c.etag
	var calls []storageR3FactoryCall
	impl := storageR3Adapter(t, config, client, storageR3Credentials{values: map[string]string{
		config.AccessKeyIDRef: "access-value", config.SecretAccessKeyRef: "secret-value"}}, &calls)
	request, source := storageR3PutRequest(storageR3PutInput(), storageR3Content)
	response, err := impl.Invoke(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	storageR3Equal(t, "response JSON-RPC version", response.JSONRPC, "2.0")
	storageR3Equal(t, "response ID", response.ID, "request-1")
	storageR3Equal(t, "outcome", response.Result.Outcome, adapter.OutcomeCompleted)
	storageR3Equal(t, "observed time set", response.Result.ObservedAt.IsZero(), false)
	storageR3Equal(t, "put output", storageR3Wire(t, response), c.want)
	storageR3Equal(t, "upload stream closes", source.closed, 1)
	storageR3Equal(t, "client factory calls", storageR3JSON(t, len(calls)), "1")
	storageR3Equal(t, "client factory call", calls[0], storageR3FactoryCall{"us-west-1", "https://s3.us-west-1.wasabisys.com", "access-value", "secret-value"})
	put := client.putInput
	storageR3Sent(t, put.Bucket, put.Key, nil, nil)
	storageR3Equal(t, "provider content length", aws.ToInt64(put.ContentLength), int64(len(storageR3Content)))
	storageR3Equal(t, "provider checksum algorithm", put.ChecksumAlgorithm, types.ChecksumAlgorithmSha256)
	storageR3Equal(t, "staging modes during transfer", storageR3JSON(t, client.stagedModes), "[384]")
	storageR3NoStaging(t, tempDir)
}

func TestStorageR3PutSuccessOutput(t *testing.T) {
	target := `"target":` + storageR3Ref + `},"upload_state":"uploaded"}`
	tests := []storageR3PutSuccess{
		{"version and etag", nil, aws.String("version-1"), aws.String("\"etag-1\""),
			`{"bytes_received":19,"object_ref":` + storageR3Ref + `,"version_id":"version-1"},"observed_at":"T","provider_etag":"\"etag-1\"",` + target},
		{"required version present", func(c *Config) { c.RequireVersionIDs = true }, aws.String("version-2"), nil,
			`{"bytes_received":19,"object_ref":` + storageR3Ref + `,"version_id":"version-2"},"observed_at":"T",` + target},
		{"null version and empty etag", nil, aws.String("null"), aws.String(""),
			`{"bytes_received":19,"object_ref":` + storageR3Ref + `},"observed_at":"T",` + target},
		{"no version", nil, nil, nil,
			`{"bytes_received":19,"object_ref":` + storageR3Ref + `},"observed_at":"T",` + target},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

type storageR3GetCase struct {
	name       string
	config     func(*Config)
	client     func(*storageR3Client)
	creds      CredentialResolver
	input      func(*getInput)
	request    func(*adapter.InvokeRequest)
	noSink     bool
	want       string
	sinkOpened bool
	bodyOpened bool
}

func (c storageR3GetCase) run(t *testing.T) {
	client := storageR3NewClient()
	client.getBody = storageR3Content
	storageR3Apply(c.client, client)
	impl := storageR3Adapter(t, storageR3Config(c.config), client, c.creds, nil)
	input := getInput{ObjectRef: objectReference{objectTarget: storageR3Target("server/world.tar")}, ExpectedSizeBytes: int64(len(storageR3Content)), ExpectedChecksum: storageR3Checksum(storageR3Content)}
	storageR3Apply(c.input, &input)
	request := requestFor("storage.object.get", input, time.Now().Add(time.Minute), "adapter_to_controller", int64(len(storageR3Content)))
	sink := &storageR3Writer{}
	if !c.noSink {
		request.Params.RequestContext.SetStreamDestinations(map[string]io.WriteCloser{testTransferID(): sink})
	}
	storageR3Apply(c.request, &request)
	response, err := impl.Invoke(context.Background(), request)
	storageR3Failed(t, response, err, c.want, false)
	storageR3Equal(t, "download stream closes", sink.closed, storageR3Times(c.sinkOpened))
	storageR3Equal(t, "provider bodies opened", len(client.bodies), storageR3Times(c.bodyOpened))
	for _, body := range client.bodies {
		storageR3Equal(t, "provider body closes", body.closed, 1)
	}
}

func TestStorageR3GetPaths(t *testing.T) {
	invalid := "invalid object download request"
	size := "Wasabi object size does not match the backup manifest"
	unconfirmed := "Wasabi did not confirm the requested object version"
	tests := []storageR3GetCase{
		{name: "malformed input", request: func(r *adapter.InvokeRequest) { r.Params.Input = json.RawMessage(`{`) }, want: invalid},
		{name: "wrong environment", request: func(r *adapter.InvokeRequest) { r.Params.RequestContext.EnvironmentID = "production" }, want: invalid},
		{name: "reference prefix", input: func(i *getInput) { i.ObjectRef.Key = "other/world.tar" }, want: invalid},
		{name: "reference container", input: func(i *getInput) { i.ObjectRef.Container = "other" }, want: invalid},
		{name: "size above profile", config: func(c *Config) { c.MaxObjectBytes = 4 }, want: invalid},
		{name: "checksum algorithm", input: func(i *getInput) { i.ExpectedChecksum.Algorithm = "sha1" }, want: invalid},
		{name: "required version absent", config: func(c *Config) { c.RequireVersionIDs = true }, want: "an exact object version is required by this storage profile"},
		{name: "no transfer", request: func(r *adapter.InvokeRequest) { r.Params.RequestContext.StreamTransfers = nil }, want: "storage transfer requires exactly one stream handle"},
		{name: "wrong direction", request: func(r *adapter.InvokeRequest) {
			r.Params.RequestContext.StreamTransfers[0].Direction = "controller_to_adapter"
		},
			want: "storage stream handle does not match the bounded transfer"},
		{name: "no destination", noSink: true, want: "download stream is unavailable"},
		{name: "secret refs missing", request: func(r *adapter.InvokeRequest) { r.Params.RequestContext.SecretRefs = nil },
			want: "Wasabi credential references are not authorized for this invocation", sinkOpened: true},
		{name: "credential resolver fails", creds: storageR3Credentials{err: errors.New("no credential")}, want: "Wasabi credentials are unavailable", sinkOpened: true},
		{name: "provider rejects", client: func(c *storageR3Client) { c.getErr = errors.New("boom") }, want: "Wasabi object download failed", sinkOpened: true},
		{name: "object short", client: func(c *storageR3Client) { c.getBody = storageR3Content[:4] }, want: size, sinkOpened: true, bodyOpened: true},
		{name: "object long", client: func(c *storageR3Client) { c.getBody = append(append([]byte(nil), storageR3Content...), '!') }, want: size, sinkOpened: true, bodyOpened: true},
		{name: "object altered", client: func(c *storageR3Client) { c.getBody = bytes.Repeat([]byte("x"), len(storageR3Content)) },
			want: "Wasabi object checksum does not match the backup manifest", sinkOpened: true, bodyOpened: true},
		{name: "requested version not confirmed", input: func(i *getInput) { i.ObjectRef.VersionID = "version-9" }, want: unconfirmed, sinkOpened: true, bodyOpened: true},
		{name: "requested version unreported", input: func(i *getInput) { i.ObjectRef.VersionID = "version-1" }, client: func(c *storageR3Client) { c.getVersion = nil },
			want: unconfirmed, sinkOpened: true, bodyOpened: true},
		{name: "requested null version", input: func(i *getInput) { i.ObjectRef.VersionID = "null" },
			want: "the mutable null object version is not accepted as an exact version", sinkOpened: true, bodyOpened: true},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

type storageR3GetSuccess struct {
	name        string
	config      func(*Config)
	requested   string
	observed    *string
	expected    string // digest case as supplied by the caller
	wantVersion *string
	want        string
}

func (c storageR3GetSuccess) run(t *testing.T) {
	client := storageR3NewClient()
	client.getBody, client.getVersion = storageR3Content, c.observed
	impl := storageR3Adapter(t, storageR3Config(c.config), client, nil, nil)
	ref := objectReference{objectTarget: storageR3Target("server/world.tar"), VersionID: c.requested}
	input := getInput{ObjectRef: ref, ExpectedSizeBytes: int64(len(storageR3Content)), ExpectedChecksum: checksum{Algorithm: "sha256", Digest: c.expected}}
	request := requestFor("storage.object.get", input, time.Now().Add(time.Minute), "adapter_to_controller", int64(len(storageR3Content)))
	sink := &storageR3Writer{}
	request.Params.RequestContext.SetStreamDestinations(map[string]io.WriteCloser{testTransferID(): sink})
	response, err := impl.Invoke(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	storageR3Equal(t, "get output", storageR3Wire(t, response), c.want)
	storageR3Equal(t, "bytes delivered", sink.String(), string(storageR3Content))
	storageR3Equal(t, "download stream closes", sink.closed, 1)
	storageR3Equal(t, "provider bodies opened", len(client.bodies), 1)
	storageR3Equal(t, "provider body closes", client.bodies[0].closed, 1)
	storageR3Sent(t, client.getInput.Bucket, client.getInput.Key, client.getInput.VersionId, c.wantVersion)
}

func TestStorageR3GetSuccessOutput(t *testing.T) {
	digest := storageR3Checksum(storageR3Content).Digest
	computed := `{"bytes_sent":19,"computed_checksum":{"algorithm":"sha256","digest":"` + digest + `"},"object_ref":` + storageR3Ref
	tests := []storageR3GetSuccess{
		{"latest with version", nil, "", aws.String("version-1"), digest, nil,
			computed + `,"version_id":"version-1"},"observed_at":"T","transfer_state":"complete"}`},
		{"latest unversioned", nil, "", aws.String("null"), strings.ToUpper(digest), nil,
			computed + `},"observed_at":"T","transfer_state":"complete"}`},
		{"exact version required", func(c *Config) { c.RequireVersionIDs = true }, "version-3", aws.String("version-3"), digest, aws.String("version-3"),
			computed + `,"version_id":"version-3"},"observed_at":"T","transfer_state":"complete"}`},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}
