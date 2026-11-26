package wasabistorage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Scripted fakes and a table runner for the characterization tests of the
// list, copy, delete, versioning and retention operations. The fake records
// every S3 call and its input, and returns the outputs a case scripts.

const (
	wasabiR5Credentials = "Wasabi credential references are not authorized for this invocation"
	wasabiR5Unconfirmed = "Wasabi did not confirm the requested object version"
)

// wasabiR5TokenKey replaces the random per-instance key so tests can sign
// continuation tokens themselves.
var wasabiR5TokenKey = [32]byte{1, 2, 3, 4, 5, 6, 7, 8}

type wasabiR5Head struct {
	out *s3.HeadObjectOutput
	err error
}

type wasabiR5Client struct {
	*memoryS3
	calls []string

	listOut *s3.ListObjectsV2Output
	listErr error
	listIn  *s3.ListObjectsV2Input

	heads  []wasabiR5Head
	headIn []*s3.HeadObjectInput

	copyOut *s3.CopyObjectOutput
	copyErr error
	copyIn  *s3.CopyObjectInput

	getBody io.Reader
	getErr  error
	getIn   *s3.GetObjectInput
	body    *storageR3Reader

	deleteErr error
	deleteIn  *s3.DeleteObjectInput

	versioningOut *s3.GetBucketVersioningOutput
	versioningErr error
	versioningIn  *s3.GetBucketVersioningInput

	retentionOut *s3.GetObjectRetentionOutput
	retentionErr error
	retentionIn  *s3.GetObjectRetentionInput

	holdOut *s3.GetObjectLegalHoldOutput
	holdErr error
	holdIn  *s3.GetObjectLegalHoldInput
}

func (c *wasabiR5Client) ListObjectsV2(_ context.Context, input *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	c.calls, c.listIn = append(c.calls, "ListObjectsV2"), input
	return c.listOut, c.listErr
}

func (c *wasabiR5Client) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	c.calls, c.headIn = append(c.calls, "HeadObject"), append(c.headIn, input)
	if len(c.heads) == 0 {
		return nil, errors.New("unscripted HeadObject")
	}
	next := c.heads[0]
	c.heads = c.heads[1:]
	return next.out, next.err
}

func (c *wasabiR5Client) CopyObject(_ context.Context, input *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	c.calls, c.copyIn = append(c.calls, "CopyObject"), input
	return c.copyOut, c.copyErr
}

func (c *wasabiR5Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	c.calls, c.getIn = append(c.calls, "GetObject"), input
	if c.getErr != nil {
		return nil, c.getErr
	}
	c.body = &storageR3Reader{Reader: c.getBody}
	return &s3.GetObjectOutput{Body: c.body}, nil
}

func (c *wasabiR5Client) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	c.calls, c.deleteIn = append(c.calls, "DeleteObject"), input
	if c.deleteErr != nil {
		return nil, c.deleteErr
	}
	return &s3.DeleteObjectOutput{}, nil
}

func (c *wasabiR5Client) GetBucketVersioning(_ context.Context, input *s3.GetBucketVersioningInput, _ ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	c.calls, c.versioningIn = append(c.calls, "GetBucketVersioning"), input
	return c.versioningOut, c.versioningErr
}

func (c *wasabiR5Client) GetObjectRetention(_ context.Context, input *s3.GetObjectRetentionInput, _ ...func(*s3.Options)) (*s3.GetObjectRetentionOutput, error) {
	c.calls, c.retentionIn = append(c.calls, "GetObjectRetention"), input
	return c.retentionOut, c.retentionErr
}

func (c *wasabiR5Client) GetObjectLegalHold(_ context.Context, input *s3.GetObjectLegalHoldInput, _ ...func(*s3.Options)) (*s3.GetObjectLegalHoldOutput, error) {
	c.calls, c.holdIn = append(c.calls, "GetObjectLegalHold"), input
	return c.holdOut, c.holdErr
}

// wasabiR5Case is one invocation of an operation: the profile, the input,
// request changes, the provider script, and the exact expected outcome.
type wasabiR5Case struct {
	name   string
	config func(*Config)
	input  any
	mutate func(*adapter.InvokeRequest)
	script func(*wasabiR5Client)
	// want is the exact error text; "" expects success with output.
	want       string
	sideEffect bool
	// calls is the space-separated sequence of S3 calls the case must make.
	calls  string
	output string
	check  func(*testing.T, *wasabiR5Client)
}

func wasabiR5Run(t *testing.T, capability string, cases []wasabiR5Case) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, capability) })
	}
}

func (c wasabiR5Case) run(t *testing.T, capability string) {
	client := &wasabiR5Client{memoryS3: newProvider(nil)}
	storageR3Apply(c.script, client)
	impl := wasabiR5Adapter(t, storageR3Config(c.config), client)
	request := requestFor(capability, c.input, time.Now().Add(time.Minute), "", 0)
	storageR3Apply(c.mutate, &request)
	response, err := impl.Invoke(context.Background(), request)
	storageR3Equal(t, "S3 calls", strings.Join(client.calls, " "), c.calls)
	if c.check != nil {
		c.check(t, client)
	}
	if c.want != "" {
		storageR3Failed(t, response, err, c.want, c.sideEffect)
		return
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	storageR3Equal(t, "output", storageR3Wire(t, response), c.output)
}

func wasabiR5Adapter(t *testing.T, config Config, client s3Client) *Adapter {
	t.Helper()
	impl, err := newAdapter(config, testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return client })
	if err != nil {
		t.Fatal(err)
	}
	impl.tokenKey = wasabiR5TokenKey
	return impl
}

func wasabiR5BadJSON(request *adapter.InvokeRequest) {
	request.Params.Input = json.RawMessage(`{`)
}

func wasabiR5WrongEnvironment(request *adapter.InvokeRequest) {
	request.Params.RequestContext.EnvironmentID = "production"
}

func wasabiR5NoSecrets(request *adapter.InvokeRequest) {
	request.Params.RequestContext.SecretRefs = nil
}

func wasabiR5RequireVersions(config *Config) { config.RequireVersionIDs = true }

// wasabiR5Coded is a provider error carrying an S3 error code.
func wasabiR5Coded(code string) error { return fmt.Errorf("provider: %w", codedError(code)) }

// wasabiR5Output encodes an expected result with observed_at replaced by "T".
func wasabiR5Output(t *testing.T, value map[string]any) string {
	t.Helper()
	value["observed_at"] = "T"
	return storageR3JSON(t, value)
}

// wasabiR5Sign builds a continuation token over payload with the test key.
func wasabiR5Sign(payload string) string {
	mac := hmac.New(sha256.New, wasabiR5TokenKey[:])
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// wasabiR5Payload is a token payload in the adapter's field order.
func wasabiR5Payload(environment, container, prefix, provider string) string {
	return fmt.Sprintf(`{"e":%q,"c":%q,"p":%q,"t":%q}`, environment, container, prefix, provider)
}

func wasabiR5Container(mutate func(*containerReference)) containerReference {
	value := testContainer()
	storageR3Apply(mutate, &value)
	return value
}

func wasabiR5Ref(mutate func(*objectReference)) objectReference {
	value := testRef("server/world.tar", "version-1")
	storageR3Apply(mutate, &value)
	return value
}

func wasabiR5Version(version string) func(*objectReference) {
	return func(ref *objectReference) { ref.VersionID = version }
}

func wasabiR5OutsideRef(ref *objectReference) { ref.Key = "production/world.tar" }

func wasabiR5OtherBucket(ref *objectReference) { ref.Container = "other-bucket" }

// wasabiR5Sent checks one provider request's bucket, key and version.
func wasabiR5Sent(t *testing.T, what string, bucket, key, version *string, want string) {
	t.Helper()
	storageR3Equal(t, what+" bucket", aws.ToString(bucket), "backups")
	storageR3Equal(t, what+" key", aws.ToString(key), "server/world.tar")
	storageR3Equal(t, what+" version set", version != nil, want != "")
	storageR3Equal(t, what+" version", aws.ToString(version), want)
}
