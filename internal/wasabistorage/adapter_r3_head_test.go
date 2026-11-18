package wasabistorage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// These characterization tests pin the head path: exact error text, the
// absent-object codes, and which provider fields reach the output.

type storageR3HeadCase struct {
	name    string
	config  func(*Config)
	client  func(*storageR3Client)
	creds   CredentialResolver
	ref     func(*objectReference)
	request func(*adapter.InvokeRequest)
	want    string
}

func (c storageR3HeadCase) run(t *testing.T) {
	client := storageR3NewClient()
	client.headOutput = &s3.HeadObjectOutput{VersionId: aws.String("version-1")}
	storageR3Apply(c.client, client)
	impl := storageR3Adapter(t, storageR3Config(c.config), client, c.creds, nil)
	ref := objectReference{objectTarget: storageR3Target("server/world.tar")}
	storageR3Apply(c.ref, &ref)
	request := requestFor("storage.object.head", headInput{ObjectRef: ref}, time.Now().Add(time.Minute), "", 0)
	storageR3Apply(c.request, &request)
	response, err := impl.Invoke(context.Background(), request)
	storageR3Failed(t, response, err, c.want, false)
}

func TestStorageR3HeadPaths(t *testing.T) {
	invalid := "invalid object inspection request"
	failed := "Wasabi object inspection failed"
	required := func(c *Config) { c.RequireVersionIDs = true }
	tests := []storageR3HeadCase{
		{name: "malformed input", request: func(r *adapter.InvokeRequest) { r.Params.Input = json.RawMessage(`{`) }, want: invalid},
		{name: "wrong environment", request: func(r *adapter.InvokeRequest) { r.Params.RequestContext.EnvironmentID = "production" }, want: invalid},
		{name: "reference prefix", ref: func(r *objectReference) { r.Key = "other/world.tar" }, want: invalid},
		{name: "secret refs missing", request: func(r *adapter.InvokeRequest) { r.Params.RequestContext.SecretRefs = nil },
			want: "Wasabi credential references are not authorized for this invocation"},
		{name: "credential resolver fails", creds: storageR3Credentials{err: errors.New("no")}, want: "Wasabi credentials are unavailable"},
		{name: "access denied", client: func(c *storageR3Client) { c.headErr = codedError("AccessDenied") }, want: failed},
		{name: "uncoded failure", client: func(c *storageR3Client) { c.headErr = errors.New("NotFound") }, want: failed},
		{name: "requested version not confirmed", ref: func(r *objectReference) { r.VersionID = "version-9" }, want: "Wasabi did not confirm the requested object version"},
		{name: "requested null version", ref: func(r *objectReference) { r.VersionID = "null" }, want: "the mutable null object version is not accepted as an exact version"},
		{name: "required version unreported", config: required, client: func(c *storageR3Client) { c.headOutput.VersionId = nil },
			want: "Wasabi object has no immutable version ID"},
		{name: "required version null", config: required, client: func(c *storageR3Client) { c.headOutput.VersionId = aws.String("null") },
			want: "Wasabi object has no immutable version ID"},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

type storageR3HeadOutput struct {
	name        string
	config      func(*Config)
	requested   string
	err         error
	output      *s3.HeadObjectOutput
	wantVersion *string
	want        string
}

func (c storageR3HeadOutput) run(t *testing.T) {
	client := storageR3NewClient()
	client.headErr, client.headOutput = c.err, c.output
	impl := storageR3Adapter(t, storageR3Config(c.config), client, nil, nil)
	input := headInput{ObjectRef: objectReference{objectTarget: storageR3Target("server/world.tar"), VersionID: c.requested}}
	response, err := impl.Invoke(context.Background(), requestFor("storage.object.head", input, time.Now().Add(time.Minute), "", 0))
	if err != nil {
		t.Fatal(err)
	}
	storageR3Equal(t, "head output", storageR3Wire(t, response), c.want)
	storageR3Sent(t, client.headInput.Bucket, client.headInput.Key, client.headInput.VersionId, c.wantVersion)
}

func TestStorageR3HeadOutputs(t *testing.T) {
	modified := time.Date(2030, 4, 5, 6, 7, 8, 9, time.FixedZone("plus2", 2*3600))
	digest := sha256.Sum256([]byte("world"))
	full := base64.StdEncoding.EncodeToString(digest[:])
	short := base64.StdEncoding.EncodeToString(digest[:31])
	absent := `{"object_ref":` + storageR3Ref + `},"object_state":"absent","observed_at":"T"}`
	present := `{"object_ref":` + storageR3Ref + `},"object_state":"present","observed_at":"T"}`
	tests := []storageR3HeadOutput{
		{name: "not found", err: codedError("NotFound"), want: absent},
		{name: "no such key", err: codedError("NoSuchKey"), want: absent},
		{name: "no such version keeps request", requested: "version-7", err: codedError("NoSuchVersion"), wantVersion: aws.String("version-7"),
			want: `{"object_ref":` + storageR3Ref + `,"version_id":"version-7"},"object_state":"absent","observed_at":"T"}`},
		{name: "absent with required version", config: func(c *Config) { c.RequireVersionIDs = true }, err: codedError("NotFound"), want: absent},
		{name: "every field", output: &s3.HeadObjectOutput{VersionId: aws.String("version-1"), ContentLength: aws.Int64(5), ETag: aws.String("\"e\""),
			LastModified: &modified, ChecksumType: types.ChecksumTypeFullObject, ChecksumSHA256: aws.String(full)},
			want: `{"last_modified":"2030-04-05T04:07:08.000000009Z","object_ref":` + storageR3Ref + `,"version_id":"version-1"},"object_state":"present","observed_at":"T","provider_checksum":{"algorithm":"sha256","digest":"` +
				hex.EncodeToString(digest[:]) + `"},"provider_etag":"\"e\"","size_bytes":5}`},
		{name: "exact version", requested: "version-1", wantVersion: aws.String("version-1"), output: &s3.HeadObjectOutput{VersionId: aws.String("version-1"), ContentLength: aws.Int64(0)},
			want: `{"object_ref":` + storageR3Ref + `,"version_id":"version-1"},"object_state":"present","observed_at":"T","size_bytes":0}`},
		{name: "composite checksum and empty etag", output: &s3.HeadObjectOutput{ETag: aws.String(""), ChecksumType: types.ChecksumTypeComposite, ChecksumSHA256: aws.String(full)}, want: present},
		{name: "full checksum missing", output: &s3.HeadObjectOutput{ChecksumType: types.ChecksumTypeFullObject}, want: present},
		{name: "checksum not base64", output: &s3.HeadObjectOutput{ChecksumType: types.ChecksumTypeFullObject, ChecksumSHA256: aws.String("%%%")}, want: present},
		{name: "checksum wrong length", output: &s3.HeadObjectOutput{ChecksumType: types.ChecksumTypeFullObject, ChecksumSHA256: aws.String(short)}, want: present},
		{name: "null version unrequired", output: &s3.HeadObjectOutput{VersionId: aws.String("null")}, want: present},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}
