package wasabistorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Shared fakes and assertions for the storage adapter's characterization
// tests: a provider that records what it was asked, streams that count their
// Close calls, and checks for exact error text and side-effect reporting.

type storageR3Client struct {
	*memoryS3
	putErr      error
	putVersion  *string
	putETag     *string
	putCalls    int
	putInput    *s3.PutObjectInput
	stagedModes []os.FileMode
	tempDir     string

	getErr     error
	getBody    []byte
	getVersion *string
	getInput   *s3.GetObjectInput
	bodies     []*storageR3Reader

	headErr    error
	headOutput *s3.HeadObjectOutput
	headInput  *s3.HeadObjectInput
}

func (c *storageR3Client) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	c.putCalls++
	c.putInput = input
	c.recordStagedModes()
	if _, err := io.ReadAll(input.Body); err != nil {
		return nil, err
	}
	if c.putErr != nil {
		return nil, c.putErr
	}
	return &s3.PutObjectOutput{ETag: c.putETag, VersionId: c.putVersion}, nil
}

// recordStagedModes notes the mode of each private upload staging file that
// exists while the provider is reading the upload.
func (c *storageR3Client) recordStagedModes() {
	if c.tempDir == "" {
		return
	}
	// Glob fails only for a malformed pattern, and this one is constant.
	matches, _ := filepath.Glob(filepath.Join(c.tempDir, "gsc-wasabi-upload-*"))
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil {
			c.stagedModes = append(c.stagedModes, info.Mode())
		}
	}
}

func (c *storageR3Client) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	c.getInput = input
	if c.getErr != nil {
		return nil, c.getErr
	}
	body := &storageR3Reader{Reader: bytes.NewReader(c.getBody)}
	c.bodies = append(c.bodies, body)
	return &s3.GetObjectOutput{Body: body, VersionId: c.getVersion}, nil
}

func (c *storageR3Client) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	c.headInput = input
	if c.headErr != nil {
		return nil, c.headErr
	}
	return c.headOutput, nil
}

type storageR3Reader struct {
	io.Reader
	closed int
}

func (r *storageR3Reader) Close() error { r.closed++; return nil }

type storageR3Writer struct {
	bytes.Buffer
	closed int
}

func (w *storageR3Writer) Close() error { w.closed++; return nil }

type storageR3Credentials struct {
	values map[string]string
	err    error
}

func (c storageR3Credentials) WithCredential(reference string, use func([]byte) error) error {
	if c.err != nil {
		return c.err
	}
	return use([]byte(c.values[reference]))
}

type storageR3FactoryCall struct{ region, endpoint, access, secret string }

func storageR3Adapter(t *testing.T, config Config, client *storageR3Client, credentials CredentialResolver, calls *[]storageR3FactoryCall) *Adapter {
	t.Helper()
	if credentials == nil {
		credentials = testCredentials{}
	}
	impl, err := newAdapter(config, testProtocolVersion, credentials, func(region, endpoint, access, secret string) s3Client {
		if calls != nil {
			*calls = append(*calls, storageR3FactoryCall{region, endpoint, access, secret})
		}
		return client
	})
	if err != nil {
		t.Fatal(err)
	}
	return impl
}

func storageR3NewClient() *storageR3Client {
	return &storageR3Client{memoryS3: &memoryS3{objects: map[string][]byte{}, uploads: map[string]memoryUpload{}},
		putVersion: aws.String("version-1"), putETag: aws.String("\"etag-1\""), getVersion: aws.String("version-1")}
}

// storageR3Apply runs an optional table mutation.
func storageR3Apply[T any](mutate func(*T), value *T) {
	if mutate != nil {
		mutate(value)
	}
}

func storageR3Config(mutate func(*Config)) Config {
	config := testConfig()
	storageR3Apply(mutate, &config)
	return config
}

func storageR3Context(build func() context.Context) context.Context {
	if build == nil {
		return context.Background()
	}
	return build()
}

func storageR3Canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func storageR3Target(key string) objectTarget {
	return objectTarget{AdapterID: testAdapterID, StorageProfile: "staging-storage", EnvironmentID: "staging", Container: "backups", Key: key}
}

func storageR3Checksum(content []byte) checksum {
	digest := sha256.Sum256(content)
	return checksum{Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])}
}

// storageR3Times converts an expected "was it opened" flag to a Close count.
func storageR3Times(opened bool) int {
	if opened {
		return 1
	}
	return 0
}

func storageR3Equal[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// storageR3Outcome returns the error text and whether the adapter reported
// that the failure may have changed the provider.
func storageR3Outcome(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var invocation *adapter.InvocationError
	if errors.As(err, &invocation) {
		return err.Error(), invocation.MayHaveSideEffects
	}
	return err.Error(), false
}

// storageR3Text returns an error's text, or "" for no error.
func storageR3Text(err error) string {
	text, _ := storageR3Outcome(err)
	return text
}

// storageR3Failed checks an Invoke failure: exact text, side-effect flag, and
// that no output was produced.
func storageR3Failed(t *testing.T, response adapter.InvokeResponse, err error, want string, sideEffect bool) {
	t.Helper()
	got, side := storageR3Outcome(err)
	storageR3Equal(t, "error", got, want)
	storageR3Equal(t, "may have side effects", side, sideEffect)
	storageR3Equal(t, "output on failure", string(response.Result.Output), "")
}

// storageR3PrivateTemp points os.CreateTemp at a fresh directory so a test
// can see every upload staging file.
func storageR3PrivateTemp(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func storageR3NoStaging(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	storageR3Equal(t, "upload staging files left behind", len(entries), 0)
}

var storageR3ObservedAt = regexp.MustCompile(`"observed_at":"([^"]*)"`)

// storageR3Wire returns the exact encoded output with its observed_at value,
// once checked to be a UTC RFC 3339 timestamp, replaced by "T".
func storageR3Wire(t *testing.T, response adapter.InvokeResponse) string {
	t.Helper()
	output := string(response.Result.Output)
	matches := storageR3ObservedAt.FindAllStringSubmatch(output, -1)
	storageR3Equal(t, "observed_at fields in "+output, len(matches), 1)
	if _, err := time.Parse(time.RFC3339Nano, matches[0][1]); err != nil {
		t.Fatalf("observed_at %q: %v", matches[0][1], err)
	}
	storageR3Equal(t, "observed_at is UTC", strings.HasSuffix(matches[0][1], "Z"), true)
	return storageR3ObservedAt.ReplaceAllString(output, `"observed_at":"T"`)
}

func storageR3JSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// storageR3Sent checks the provider request's bucket, key, and version.
func storageR3Sent(t *testing.T, bucket, key, version *string, wantVersion *string) {
	t.Helper()
	storageR3Equal(t, "provider bucket", aws.ToString(bucket), "backups")
	storageR3Equal(t, "provider key", aws.ToString(key), "server/world.tar")
	storageR3Equal(t, "provider version set", version != nil, wantVersion != nil)
	storageR3Equal(t, "provider version", aws.ToString(version), aws.ToString(wantVersion))
}
