package wasabistorage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/amibamae/gameserver-controller/internal/adapter"
	"github.com/amibamae/gameserver-controller/internal/capturestream"
	"github.com/amibamae/gameserver-controller/internal/schemaregistry"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const testProtocolVersion = "0.21.0-draft"
const testAdapterID = "storage.wasabi-staging"

func TestPutGetHeadAgainstProtocolRegistry(t *testing.T) {
	content := []byte("world archive bytes")
	digest := sha256.Sum256(content)
	provider := &memoryS3{objects: map[string][]byte{}}
	impl, err := newAdapter(testConfig(), testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := schemaregistry.Load()
	if err != nil {
		t.Fatal(err)
	}
	registry, err := adapter.NewRegistry(schemas)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(impl); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	target := objectTarget{AdapterID: testAdapterID, StorageProfile: testConfig().StorageProfileID,
		EnvironmentID: testConfig().EnvironmentID, Container: "backups", Key: "server/world.tar"}

	putInput := putInput{Target: target, ExpectedSizeBytes: int64(len(content)), ExpectedChecksum: checksum{Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])}}
	putResponse := invoke(t, registry, "storage.object.put", putInput, deadline, "controller_to_adapter", int64(len(content)),
		map[string]io.ReadCloser{testTransferID(): io.NopCloser(bytes.NewReader(content))}, nil)
	var putOutput struct {
		UploadState string          `json:"upload_state"`
		ObjectRef   objectReference `json:"object_ref"`
	}
	if err := json.Unmarshal(putResponse.Result.Output, &putOutput); err != nil {
		t.Fatal(err)
	}
	if putOutput.UploadState != "uploaded" || putOutput.ObjectRef.VersionID != "version-1" {
		t.Fatalf("unexpected put output: %+v", putOutput)
	}

	getInput := getInput{ObjectRef: putOutput.ObjectRef, ExpectedSizeBytes: int64(len(content)), ExpectedChecksum: putInput.ExpectedChecksum}
	destination := &bytes.Buffer{}
	getResponse := invoke(t, registry, "storage.object.get", getInput, deadline, "adapter_to_controller", int64(len(content)),
		nil, map[string]io.WriteCloser{testTransferID(): nopWriteCloser{destination}})
	var getOutput struct {
		TransferState string   `json:"transfer_state"`
		BytesSent     int64    `json:"bytes_sent"`
		Checksum      checksum `json:"computed_checksum"`
	}
	if err := json.Unmarshal(getResponse.Result.Output, &getOutput); err != nil {
		t.Fatal(err)
	}
	if getOutput.TransferState != "complete" || getOutput.BytesSent != int64(len(content)) || getOutput.Checksum.Digest != hex.EncodeToString(digest[:]) || !bytes.Equal(destination.Bytes(), content) {
		t.Fatalf("unexpected get result: %+v, body=%q", getOutput, destination.Bytes())
	}

	headResponse := invoke(t, registry, "storage.object.head", headInput{ObjectRef: putOutput.ObjectRef}, deadline, "", 0, nil, nil)
	var headOutput struct {
		ObjectState string `json:"object_state"`
		SizeBytes   int64  `json:"size_bytes"`
	}
	if err := json.Unmarshal(headResponse.Result.Output, &headOutput); err != nil {
		t.Fatal(err)
	}
	if headOutput.ObjectState != "present" || headOutput.SizeBytes != int64(len(content)) {
		t.Fatalf("unexpected head result: %+v", headOutput)
	}
}

func TestPutRejectsWrongChecksumBeforeProviderWrite(t *testing.T) {
	provider := &memoryS3{objects: map[string][]byte{}}
	impl, err := newAdapter(testConfig(), testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("bytes")
	input := putInput{Target: objectTarget{AdapterID: testAdapterID, StorageProfile: "staging-storage", EnvironmentID: "staging",
		Container: "backups", Key: "server/world.tar"}, ExpectedSizeBytes: int64(len(content)),
		ExpectedChecksum: checksum{Algorithm: "sha256", Digest: strings.Repeat("0", 64)}}
	request := requestFor("storage.object.put", input, time.Now().Add(time.Minute), "controller_to_adapter", int64(len(content)))
	request.Params.RequestContext.SetStreamSources(map[string]io.ReadCloser{testTransferID(): io.NopCloser(bytes.NewReader(content))})
	_, err = impl.Invoke(context.Background(), request)
	if err == nil {
		t.Fatal("accepted an upload with the wrong checksum")
	}
	if len(provider.objects) != 0 {
		t.Fatalf("invalid stream reached the provider; stored %d objects", len(provider.objects))
	}
}

func TestObjectOperationsRejectWrongEnvironment(t *testing.T) {
	provider := &memoryS3{objects: map[string][]byte{}, uploads: map[string]memoryUpload{}}
	impl, err := newAdapter(testConfig(), testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	input := headInput{ObjectRef: objectReference{objectTarget: objectTarget{AdapterID: testAdapterID,
		StorageProfile: "staging-storage", EnvironmentID: "staging", Container: "backups", Key: "server/world.tar"}}}
	request := requestFor("storage.object.head", input, time.Now().Add(time.Minute), "", 0)
	request.Params.RequestContext.EnvironmentID = "production"
	if _, err := impl.Invoke(context.Background(), request); err == nil {
		t.Fatal("accepted a request from outside the configured environment")
	}
}

func TestObjectOperationsRejectBucketAndKeyOutsideProfile(t *testing.T) {
	provider := &memoryS3{objects: map[string][]byte{}, uploads: map[string]memoryUpload{}}
	impl, err := newAdapter(testConfig(), testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []objectTarget{
		{AdapterID: testAdapterID, StorageProfile: "staging-storage", EnvironmentID: "staging", Container: "other-bucket", Key: "server/world.tar"},
		{AdapterID: testAdapterID, StorageProfile: "staging-storage", EnvironmentID: "staging", Container: "backups", Key: "production/world.tar"},
	} {
		input := headInput{ObjectRef: objectReference{objectTarget: target}}
		request := requestFor("storage.object.head", input, time.Now().Add(time.Minute), "", 0)
		if _, err := impl.Invoke(context.Background(), request); err == nil {
			t.Fatalf("accepted object outside configured bucket/prefix: %#v", target)
		}
	}
}

func TestGetRequiresProviderConfirmationOfRequestedVersion(t *testing.T) {
	provider := &memoryS3{objects: map[string][]byte{"backups/server/world.tar": []byte("world")}, uploads: map[string]memoryUpload{}, omitVersion: true}
	config := testConfig()
	config.RequireVersionIDs = true
	impl, err := newAdapter(config, testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("world")
	digest := sha256.Sum256(content)
	input := getInput{ObjectRef: objectReference{objectTarget: objectTarget{AdapterID: testAdapterID,
		StorageProfile: config.StorageProfileID, EnvironmentID: config.EnvironmentID, Container: config.Container,
		Key: "server/world.tar"}, VersionID: "version-1"}, ExpectedSizeBytes: int64(len(content)),
		ExpectedChecksum: checksum{Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])}}
	request := requestFor("storage.object.get", input, time.Now().Add(time.Minute), "adapter_to_controller", int64(len(content)))
	request.Params.RequestContext.SetStreamDestinations(map[string]io.WriteCloser{testTransferID(): nopWriteCloser{io.Discard}})
	if _, err := impl.Invoke(context.Background(), request); err == nil {
		t.Fatal("accepted a versioned read without provider confirmation")
	}
}

func TestConfigRejectsUntrustedEndpointAndSharedKeys(t *testing.T) {
	value := testConfig()
	value.EndpointURL = "https://169.254.169.254/latest/meta-data"
	if err := validateConfig(value); err == nil {
		t.Fatal("accepted a non-Wasabi endpoint")
	}
	value = testConfig()
	value.SecretAccessKeyRef = value.AccessKeyIDRef
	if err := validateConfig(value); err == nil {
		t.Fatal("accepted shared access and secret key references")
	}
}

func TestMultipartUploadKeepsBoundedPartSizeAndReturnsVersion(t *testing.T) {
	provider := &memoryS3{objects: map[string][]byte{}, uploads: map[string]memoryUpload{}}
	config := testConfig()
	config.MaxObjectBytes = 1 << 30
	impl, err := newAdapter(config, testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("w"), int(minPartSize+1))
	digest := sha256.Sum256(content)
	input := putInput{Target: objectTarget{AdapterID: testAdapterID, StorageProfile: config.StorageProfileID,
		EnvironmentID: config.EnvironmentID, Container: "backups", Key: "server/large/world.tar"},
		ExpectedSizeBytes: int64(len(content)), ExpectedChecksum: checksum{Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])}}
	deadline := time.Now().Add(3 * time.Minute)
	request := requestFor("storage.object.put", input, deadline, "controller_to_adapter", int64(len(content)))
	request.Params.RequestContext.SetStreamSources(map[string]io.ReadCloser{testTransferID(): io.NopCloser(bytes.NewReader(content))})
	response, err := impl.Invoke(context.Background(), request)
	if err != nil {
		t.Fatalf("multipart upload: %v", err)
	}
	if len(provider.objects) != 1 || !bytes.Equal(provider.objects["backups/server/large/world.tar"], content) {
		t.Fatalf("multipart upload did not preserve object data; objects=%d", len(provider.objects))
	}
	var result map[string]any
	if err := json.Unmarshal(response.Result.Output, &result); err != nil {
		t.Fatal(err)
	}
	if result["upload_state"] != "uploaded" || int64(result["bytes_received"].(float64)) != int64(len(content)) {
		t.Fatalf("unexpected multipart result: %#v", result)
	}
}

func TestPutAcceptsVerifiedEmptyObject(t *testing.T) {
	provider := &memoryS3{objects: map[string][]byte{}, uploads: map[string]memoryUpload{}}
	impl, err := newAdapter(testConfig(), testProtocolVersion, testCredentials{}, func(_, _, _, _ string) s3Client { return provider })
	if err != nil {
		t.Fatal(err)
	}
	emptyDigest := sha256.Sum256(nil)
	input := putInput{Target: objectTarget{AdapterID: testAdapterID, StorageProfile: "staging-storage", EnvironmentID: "staging",
		Container: "backups", Key: "server/empty"}, ExpectedSizeBytes: 0,
		ExpectedChecksum: checksum{Algorithm: "sha256", Digest: hex.EncodeToString(emptyDigest[:])}}
	request := requestFor("storage.object.put", input, time.Now().Add(time.Minute), "controller_to_adapter", 1)
	request.Params.RequestContext.SetStreamSources(map[string]io.ReadCloser{testTransferID(): io.NopCloser(bytes.NewReader(nil))})
	if _, err := impl.Invoke(context.Background(), request); err != nil {
		t.Fatalf("empty object upload: %v", err)
	}
	if value, exists := provider.objects["backups/server/empty"]; !exists || len(value) != 0 {
		t.Fatalf("empty object was not uploaded: %#v", provider.objects)
	}
}

func invoke(t *testing.T, registry *adapter.Registry, capability string, input any, deadline time.Time, direction string, maximum int64, sources map[string]io.ReadCloser, destinations map[string]io.WriteCloser) adapter.InvokeResponse {
	t.Helper()
	request := requestFor(capability, input, deadline, direction, maximum)
	if sources != nil {
		request.Params.RequestContext.SetStreamSources(sources)
	}
	if destinations != nil {
		request.Params.RequestContext.SetStreamDestinations(destinations)
	}
	response, err := registry.Invoke(context.Background(), testAdapterID, request)
	if err != nil {
		t.Fatalf("invoke %s: %v", capability, err)
	}
	return response
}

func requestFor(capability string, input any, deadline time.Time, direction string, maximum int64) adapter.InvokeRequest {
	encoded, _ := json.Marshal(input)
	request := adapter.InvokeRequest{JSONRPC: "2.0", ID: "request-1", Method: "adapter.invoke", Params: adapter.InvokeParams{
		ProtocolVersion: testProtocolVersion, Capability: capability, Input: encoded,
		RequestContext: adapter.RequestContext{RequestID: "request-1", CorrelationID: "correlation-1", OperationID: "operation-1", StepID: "step-1", ServerID: "server-1", EnvironmentID: "staging", Deadline: deadline,
			SecretRefs: []adapter.SecretReference{{SecretRef: testConfig().AccessKeyIDRef, Purpose: "Wasabi access key"}, {SecretRef: testConfig().SecretAccessKeyRef, Purpose: "Wasabi secret key"}}},
	}}
	if direction != "" {
		request.Params.RequestContext.StreamTransfers = []capturestream.Reference{{TransferID: testTransferID(),
			Direction: direction, MaximumBytes: maximum, ExpiresAt: deadline.Add(-time.Second)}}
	}
	return request
}

func testTransferID() string { return strings.Repeat("a", 64) }

func testConfig() Config {
	return Config{AdapterID: testAdapterID, StorageProfileID: "staging-storage", EnvironmentID: "staging", Container: "backups", KeyPrefix: "server/", Region: "us-west-1",
		EndpointURL: "https://s3.us-west-1.wasabisys.com", AccessKeyIDRef: "systemd-credential://wasabi-staging-access",
		SecretAccessKeyRef: "systemd-credential://wasabi-staging-secret", MaxObjectBytes: 1 << 30}
}

type testCredentials struct{}

func (testCredentials) WithCredential(reference string, use func([]byte) error) error {
	if strings.HasSuffix(reference, "access") {
		return use([]byte("test-access-key"))
	}
	return use([]byte("test-secret-key"))
}

type memoryS3 struct {
	objects     map[string][]byte
	uploads     map[string]memoryUpload
	omitVersion bool
	// Fields used by the list/copy/delete/inspection tests.
	versioning  types.BucketVersioningStatus
	retention   *types.ObjectLockRetention
	legalHold   types.ObjectLockLegalHoldStatus
	noLock      bool
	corruptCopy bool
	deletes     int
	copies      int
}

type memoryUpload struct {
	key   string
	parts map[int32][]byte
}

func (m *memoryS3) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	value, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	m.objects[aws.ToString(input.Bucket)+"/"+aws.ToString(input.Key)] = value
	return &s3.PutObjectOutput{ETag: aws.String("\"fake-etag\""), VersionId: aws.String("version-1")}, nil
}

func (m *memoryS3) UploadPart(_ context.Context, input *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	upload, ok := m.uploads[aws.ToString(input.UploadId)]
	if !ok || input.PartNumber == nil {
		return nil, errors.New("unknown multipart upload")
	}
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	upload.parts[*input.PartNumber] = data
	m.uploads[aws.ToString(input.UploadId)] = upload
	return &s3.UploadPartOutput{ETag: aws.String("\"part-etag\"")}, nil
}
func (m *memoryS3) CreateMultipartUpload(_ context.Context, input *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	const uploadID = "upload-1"
	m.uploads[uploadID] = memoryUpload{key: aws.ToString(input.Bucket) + "/" + aws.ToString(input.Key), parts: map[int32][]byte{}}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String(uploadID)}, nil
}
func (m *memoryS3) CompleteMultipartUpload(_ context.Context, input *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	id := aws.ToString(input.UploadId)
	upload, ok := m.uploads[id]
	if !ok || input.MultipartUpload == nil || len(input.MultipartUpload.Parts) == 0 {
		return nil, errors.New("unknown multipart upload")
	}
	var data bytes.Buffer
	for _, part := range input.MultipartUpload.Parts {
		if part.PartNumber == nil {
			return nil, errors.New("multipart part number missing")
		}
		data.Write(upload.parts[*part.PartNumber])
	}
	m.objects[upload.key] = data.Bytes()
	delete(m.uploads, id)
	return &s3.CompleteMultipartUploadOutput{VersionId: aws.String("version-1")}, nil
}
func (m *memoryS3) AbortMultipartUpload(_ context.Context, input *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	delete(m.uploads, aws.ToString(input.UploadId))
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (m *memoryS3) GetObject(_ context.Context, input *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	value, ok := m.objects[aws.ToString(input.Bucket)+"/"+aws.ToString(input.Key)]
	if !ok {
		return nil, errors.New("missing object")
	}
	result := &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(value))}
	if !m.omitVersion {
		result.VersionId = aws.String("version-1")
	}
	return result, nil
}

func (m *memoryS3) HeadObject(_ context.Context, input *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	value, ok := m.objects[aws.ToString(input.Bucket)+"/"+aws.ToString(input.Key)]
	if !ok {
		return nil, codedError("NotFound")
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(value))), ETag: aws.String("\"fake-etag\""), VersionId: aws.String("version-1")}, nil
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
